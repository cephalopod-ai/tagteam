package tagteam

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestReviewValidation(t *testing.T) {
	review := Review{
		Verdict: "pass",
		Summary: "ok",
		Findings: []Finding{
			{Severity: "major", File: "main.go", Issue: "bug", Fix: "fix it"},
		},
	}
	if err := review.Validate(); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestApplyReviewCapsKeepsBlockingFindings(t *testing.T) {
	findings := make([]Finding, 0, 51)
	for i := 0; i < 50; i++ {
		findings = append(findings, Finding{
			Severity: "nit",
			File:     "main.go",
			Issue:    "minor issue",
			Fix:      "adjust wording",
		})
	}
	findings = append(findings, Finding{
		Severity: "blocker",
		File:     "main.go",
		Issue:    "blocking bug",
		Fix:      "fix the bug",
	})

	review := &Review{
		Verdict:  "needs_changes",
		Summary:  "review",
		Findings: findings,
	}

	applyReviewCaps(review, 50)

	if len(review.Findings) != 50 {
		t.Fatalf("findings len = %d, want 50", len(review.Findings))
	}
	if review.Findings[0].Severity != "blocker" {
		t.Fatalf("first finding severity = %q, want blocker", review.Findings[0].Severity)
	}
	if !review.HasBlockingFindings() {
		t.Fatal("expected blocking findings to survive capping")
	}
	if got := (&App{}).computeExitCode(FinalRun{Review: review}); got != ExitBlockingFindings {
		t.Fatalf("computeExitCode() = %d, want %d", got, ExitBlockingFindings)
	}
}

func TestReviewSchemaRequiresAllFindingProperties(t *testing.T) {
	var schema struct {
		Properties struct {
			Findings struct {
				Items struct {
					Required   []string       `json:"required"`
					Properties map[string]any `json:"properties"`
				} `json:"items"`
			} `json:"findings"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(ReviewSchema), &schema); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	required := map[string]bool{}
	for _, key := range schema.Properties.Findings.Items.Required {
		required[key] = true
	}
	for key := range schema.Properties.Findings.Items.Properties {
		if !required[key] {
			t.Fatalf("finding property %q is not required", key)
		}
	}
}

func TestWorkPlanSchemaRequiresCoreFields(t *testing.T) {
	var schema struct {
		Required   []string `json:"required"`
		Properties struct {
			Packages struct {
				Items struct {
					Required []string `json:"required"`
				} `json:"items"`
			} `json:"packages"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(WorkPlanSchema), &schema); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	required := map[string]bool{}
	for _, key := range schema.Required {
		required[key] = true
	}
	for _, key := range []string{"schema_version", "summary", "packages", "selected_package"} {
		if !required[key] {
			t.Fatalf("work plan schema missing required key %q", key)
		}
	}
	packageRequired := map[string]bool{}
	for _, key := range schema.Properties.Packages.Items.Required {
		packageRequired[key] = true
	}
	for _, key := range []string{"id", "title", "goal", "acceptance", "validation"} {
		if !packageRequired[key] {
			t.Fatalf("work plan package schema missing required key %q", key)
		}
	}
}

func TestOrchestrationPolicy_RelaySimplifiesOnce(t *testing.T) {
	decision := newOrchestrationDecision("run", ModeRelay)
	mode := applyRelaySimplificationPolicy(&decision, OrchestrationAdvisory{
		SchemaVersion:  ArtifactSchemaVersion,
		Source:         "supervisor",
		Recommendation: "simplify",
		TargetMode:     ModeSupervisor,
		Reason:         "direct path is enough",
		Confidence:     "high",
	})
	if mode != ModeSupervisor {
		t.Fatalf("mode = %q", mode)
	}
	if decision.AppliedTransition == nil || decision.AppliedTransition.From != ModeRelay || decision.AppliedTransition.To != ModeSupervisor {
		t.Fatalf("transition = %#v", decision.AppliedTransition)
	}
	if !decision.TransitionLimitConsumed {
		t.Fatal("expected transition limit consumed")
	}
}

func TestOrchestrationPolicy_SupervisorEscalatesOnlyOnAgreement(t *testing.T) {
	worker := OrchestrationAdvisory{SchemaVersion: ArtifactSchemaVersion, Source: "worker", Recommendation: "escalate", TargetMode: ModeRelay, Reason: "need context", Confidence: "high"}
	supervisor := OrchestrationAdvisory{SchemaVersion: ArtifactSchemaVersion, Source: "supervisor", Recommendation: "escalate", TargetMode: ModeRelay, Reason: "agree", Confidence: "high"}
	decision := newOrchestrationDecision("run", ModeSupervisor)
	if mode := applySupervisorEscalationPolicy(&decision, worker, supervisor); mode != ModeRelay {
		t.Fatalf("mode = %q", mode)
	}
	if decision.AppliedTransition == nil || decision.AppliedTransition.To != ModeRelay {
		t.Fatalf("transition = %#v", decision.AppliedTransition)
	}
}

func TestOrchestrationPolicy_ConflictingSignalsPreferSimplerMode(t *testing.T) {
	worker := OrchestrationAdvisory{SchemaVersion: ArtifactSchemaVersion, Source: "worker", Recommendation: "escalate", TargetMode: ModeRelay, Reason: "need context", Confidence: "high"}
	supervisor := OrchestrationAdvisory{SchemaVersion: ArtifactSchemaVersion, Source: "supervisor", Recommendation: "keep", TargetMode: ModeSupervisor, Reason: "simple enough", Confidence: "high"}
	decision := newOrchestrationDecision("run", ModeSupervisor)
	if mode := applySupervisorEscalationPolicy(&decision, worker, supervisor); mode != ModeSupervisor {
		t.Fatalf("mode = %q", mode)
	}
	if decision.AppliedTransition != nil || decision.TransitionLimitConsumed {
		t.Fatalf("unexpected transition: %#v", decision)
	}
}

func TestRolePromptsDoNotLeakConflictingAuthority(t *testing.T) {
	workerPrompts := []string{
		BuildWorkerImplementPrompt("/repo", "ship it", "brief"),
		BuildWorkerFixPrompt(2, "ship it", "diff", Review{Verdict: "needs_changes", Summary: "fix", Findings: []Finding{{Severity: "major", File: "main.go", Issue: "bug", Fix: "fix"}}}),
	}
	for _, prompt := range workerPrompts {
		lower := strings.ToLower(prompt)
		if strings.Contains(lower, "adversarial reviewer") || strings.Contains(lower, "adversary") {
			t.Fatalf("supervisor-worker prompt leaked adversarial wording:\n%s", prompt)
		}
	}

	scoutPrompt := strings.ToLower(BuildScoutPrompt("/repo", "ship it", "brief", "recon", "pre", "", "", ""))
	for _, forbidden := range []string{"use \"pass\"", "needs_changes", "blocking findings", "produce blocking findings"} {
		if strings.Contains(scoutPrompt, forbidden) {
			t.Fatalf("scout prompt leaked reviewer authority %q:\n%s", forbidden, scoutPrompt)
		}
	}

	coderPrompt := strings.ToLower(BuildCoderPrompt("/repo", "ship it"))
	for _, forbidden := range []string{"you are read-only", "do not edit files"} {
		if strings.Contains(coderPrompt, forbidden) {
			t.Fatalf("coder prompt leaked read-only instruction %q:\n%s", forbidden, coderPrompt)
		}
	}
}

func TestPrepareReviewInput_UsesStdinWhenSupported(t *testing.T) {
	input := prepareReviewInput(&ClaudeAdapter{}, "diff --git a b", "/tmp/diff.patch")
	if !input.ViaStdin {
		t.Fatal("expected stdin input")
	}
	if len(input.Stdin) == 0 {
		t.Fatal("expected stdin bytes")
	}
}

func TestPrepareReviewInput_UsesFileReferenceForLargeDiff(t *testing.T) {
	diff := strings.Repeat("x", maxReviewInputBytes+1)
	input := prepareReviewInput(&ClaudeAdapter{}, diff, "/tmp/diff.patch")
	if input.ViaStdin {
		t.Fatal("did not expect stdin input")
	}
	if !strings.Contains(input.PromptRef, "/tmp/diff.patch") {
		t.Fatalf("prompt ref = %q", input.PromptRef)
	}
}

func TestPrepareReviewInput_UsesFileReferenceForLargeInlinePrompt(t *testing.T) {
	diff := strings.Repeat("x", maxInlineReviewPromptBytes+1)
	input := prepareReviewInput(&CodexAdapter{}, diff, "/tmp/diff.patch")
	if input.ViaStdin {
		t.Fatal("did not expect stdin input")
	}
	if !strings.Contains(input.PromptRef, "/tmp/diff.patch") {
		t.Fatalf("prompt ref = %q", input.PromptRef)
	}
}

func TestLoadRepoInstructions_DeterministicOrder(t *testing.T) {
	repo := t.TempDir()
	for _, item := range []struct {
		path string
		text string
	}{
		{"AGENTS.md", "root agents"},
		{"agent.md", "lower agent"},
		{filepath.Join(".tagteam", "AGENTS.md"), "tagteam agents"},
		{filepath.Join(".codex", "AGENTS.md"), "codex agents"},
		{filepath.Join(".claude", "AGENTS.md"), "claude agents"},
		{filepath.Join(".agy", "AGENTS.md"), "agy agents"},
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(repo, item.path)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, item.path), []byte(item.text+"\r\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	bundle, err := loadRepoInstructions(context.Background(), repo, maxRepoInstructionBytes)
	if err != nil {
		t.Fatalf("loadRepoInstructions() error = %v", err)
	}
	wantOrder := []string{"AGENTS.md", "agent.md", ".tagteam/AGENTS.md", ".codex/AGENTS.md", ".claude/AGENTS.md", ".agy/AGENTS.md"}
	last := -1
	for _, want := range wantOrder {
		idx := strings.Index(bundle.Text, "BEGIN "+want)
		if idx < 0 {
			t.Fatalf("bundle missing %s:\n%s", want, bundle.Text)
		}
		if idx <= last {
			t.Fatalf("%s appeared out of order in bundle:\n%s", want, bundle.Text)
		}
		last = idx
	}
	if strings.Contains(bundle.Text, "\r") {
		t.Fatalf("bundle should normalize CRLF to LF: %q", bundle.Text)
	}
}

func TestLoadRepoInstructions_MissingFilesEmpty(t *testing.T) {
	bundle, err := loadRepoInstructions(context.Background(), t.TempDir(), maxRepoInstructionBytes)
	if err != nil {
		t.Fatalf("loadRepoInstructions() error = %v", err)
	}
	if bundle.Text != "" {
		t.Fatalf("bundle text = %q", bundle.Text)
	}
	if bundle.Metadata.SourceCount != 0 || len(bundle.Metadata.Sources) != 0 {
		t.Fatalf("metadata = %#v", bundle.Metadata)
	}
}

func TestLoadRepoInstructions_OnlyExactAllowedFiles(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".codex", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".codex", "AGENTS.md"), []byte("allowed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".codex", "skills", "SKILL.md"), []byte("recursive secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".claude", "settings.json"), []byte(`{"ignored":true}`), 0o644); err == nil {
		t.Fatal("expected write to fail before directory exists")
	}
	if err := os.MkdirAll(filepath.Join(repo, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".claude", "settings.json"), []byte(`{"ignored":true}`), 0o644); err != nil {
		t.Fatal(err)
	}

	bundle, err := loadRepoInstructions(context.Background(), repo, maxRepoInstructionBytes)
	if err != nil {
		t.Fatalf("loadRepoInstructions() error = %v", err)
	}
	if !strings.Contains(bundle.Text, "allowed") {
		t.Fatalf("bundle missing allowed file:\n%s", bundle.Text)
	}
	if strings.Contains(bundle.Text, "recursive secret") || strings.Contains(bundle.Text, "ignored") {
		t.Fatalf("bundle loaded disallowed content:\n%s", bundle.Text)
	}
	if bundle.Metadata.SourceCount != 1 {
		t.Fatalf("source count = %d", bundle.Metadata.SourceCount)
	}
}

func TestLoadRepoInstructions_TruncatesDeterministically(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte(strings.Repeat("a", 200)), 0o644); err != nil {
		t.Fatal(err)
	}
	first, err := loadRepoInstructions(context.Background(), repo, 64)
	if err != nil {
		t.Fatalf("loadRepoInstructions() error = %v", err)
	}
	second, err := loadRepoInstructions(context.Background(), repo, 64)
	if err != nil {
		t.Fatalf("loadRepoInstructions() second error = %v", err)
	}
	if first.Text != second.Text {
		t.Fatalf("truncation not deterministic:\nfirst=%q\nsecond=%q", first.Text, second.Text)
	}
	if len(first.Text) > 64 {
		t.Fatalf("bundle length = %d, want <= 64", len(first.Text))
	}
	if !first.Metadata.Truncated || !first.Metadata.Sources[0].Truncated {
		t.Fatalf("expected truncation metadata: %#v", first.Metadata)
	}
}

func TestLoadAndPersistRepoInstructions_WritesArtifacts(t *testing.T) {
	repo := t.TempDir()
	runDir := filepath.Join(repo, ".tagteam", "runs", "test")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("follow repo rules\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	text, err := loadAndPersistRepoInstructions(context.Background(), RunOptions{
		Workdir:                 repo,
		RespectRepoInstructions: true,
	}, runDir)
	if err != nil {
		t.Fatalf("loadAndPersistRepoInstructions() error = %v", err)
	}
	if !strings.Contains(text, "follow repo rules") {
		t.Fatalf("instruction text = %q", text)
	}
	for _, path := range []string{"repo-instructions.md", "repo-instructions.json"} {
		if !fileExists(filepath.Join(runDir, path)) {
			t.Fatalf("expected %s artifact", path)
		}
	}
}

func TestWithRepoInstructions_AppendsBundle(t *testing.T) {
	prompt := withRepoInstructions("base prompt", "rules")
	if !strings.Contains(prompt, "base prompt") || !strings.Contains(prompt, repoInstructionsPromptHeader) || !strings.Contains(prompt, "rules") {
		t.Fatalf("prompt = %q", prompt)
	}
	if got := withRepoInstructions("base prompt", " "); got != "base prompt" {
		t.Fatalf("empty repo instructions changed prompt: %q", got)
	}
}

func TestMergeCommandEnvOverlayDoesNotOverrideShell(t *testing.T) {
	t.Setenv("TAGTEAM_TEST_ENV", "shell")
	env := mergeCommandEnv(map[string]string{
		"TAGTEAM_TEST_ENV": "dotenv",
		"TAGTEAM_NEW_ENV":  "overlay",
	}, nil)
	values := map[string]string{}
	for _, item := range env {
		key, value, _ := strings.Cut(item, "=")
		values[key] = value
	}
	if values["TAGTEAM_TEST_ENV"] != "shell" {
		t.Fatalf("TAGTEAM_TEST_ENV = %q", values["TAGTEAM_TEST_ENV"])
	}
	if values["TAGTEAM_NEW_ENV"] != "overlay" {
		t.Fatalf("TAGTEAM_NEW_ENV = %q", values["TAGTEAM_NEW_ENV"])
	}
}

func TestMergeCommandEnvForRoleRestrictsReadOnlySecrets(t *testing.T) {
	t.Setenv("TAGTEAM_SECRET_TOKEN", "secret")
	t.Setenv("PATH", "/safe/path")

	restricted := envMap(mergeCommandEnvForRole(RoleAdversary, map[string]string{
		"TAGTEAM_OVERLAY_KEY": "overlay",
	}, []string{"TAGTEAM_EXTRA_KEY=extra"}))
	if _, ok := restricted["TAGTEAM_SECRET_TOKEN"]; ok {
		t.Fatalf("restricted role inherited TAGTEAM_SECRET_TOKEN")
	}
	if restricted["TAGTEAM_OVERLAY_KEY"] != "overlay" {
		t.Fatalf("overlay missing from restricted env: %#v", restricted)
	}
	if restricted["TAGTEAM_EXTRA_KEY"] != "extra" {
		t.Fatalf("extra env missing from restricted env: %#v", restricted)
	}
	if restricted["PATH"] != "/safe/path" {
		t.Fatalf("PATH = %q", restricted["PATH"])
	}

	coder := envMap(mergeCommandEnvForRole(RoleCoder, nil, nil))
	if coder["TAGTEAM_SECRET_TOKEN"] != "secret" {
		t.Fatalf("coder role should inherit parent env")
	}
}

func TestMergeCommandEnvForRoleForwardsProviderAuth(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "ant-key")
	t.Setenv("CUSTOM_AUTH_TOKEN", "tok")
	t.Setenv("TAGTEAM_SECRET_TOKEN", "secret")

	for _, role := range []Role{RoleAdversary, RoleSupervisor, RoleScout, RoleReporter} {
		restricted := envMap(mergeCommandEnvForRole(role, nil, nil))
		if restricted["ANTHROPIC_API_KEY"] != "ant-key" {
			t.Fatalf("role %q did not receive ANTHROPIC_API_KEY: %#v", role, restricted)
		}
		if restricted["CUSTOM_AUTH_TOKEN"] != "tok" {
			t.Fatalf("role %q did not receive *_AUTH_TOKEN key: %#v", role, restricted)
		}
		// A non-auth secret must still be stripped — forward narrowly.
		if _, ok := restricted["TAGTEAM_SECRET_TOKEN"]; ok {
			t.Fatalf("role %q leaked non-auth secret TAGTEAM_SECRET_TOKEN", role)
		}
	}
}

func envMap(env []string) map[string]string {
	values := map[string]string{}
	for _, item := range env {
		key, value, _ := strings.Cut(item, "=")
		values[key] = value
	}
	return values
}

func TestExecutionPlanStatusTransitions(t *testing.T) {
	workPlan := WorkPlan{
		Summary: "two packages",
		Packages: []WorkPackage{
			{ID: "P1", Title: "First", Goal: "Do first", Acceptance: []string{"first ok"}, Validation: []string{"go test ./..."}},
			{ID: "P2", Title: "Second", Goal: "Do second", Acceptance: []string{"second ok"}, Validation: []string{"go test ./..."}},
		},
		SelectedPackage: "P1",
	}
	plan := newExecutionPlanFromWorkPlan("run-1", ModeSupervisor, workPlan, "supervisor-initial")
	if len(plan.Items) != 2 {
		t.Fatalf("items = %#v", plan.Items)
	}
	if plan.Items[0].Status != PlanStatusInProgress || plan.Items[1].Status != PlanStatusPending {
		t.Fatalf("initial statuses = %#v", plan.Items)
	}
	setPlanItemStatus(plan, "P1", PlanStatusPassed, "supervisor", "review passed")
	deferRemainingPlanItems(plan, "P1", "runner", "not auto-running remaining work")
	finalizeExecutionPlan(plan, ExitSuccess)
	summary := summarizeExecutionPlan("/tmp/run", plan)
	if plan.Status != "passed" || summary.Passed != 1 || summary.Deferred != 1 {
		t.Fatalf("plan=%#v summary=%#v", plan, summary)
	}
}

func TestPreflightBranchModeCreatesBranch(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	_, cleanup, err := preflight(RunOptions{Workdir: repo, GitSafety: "branch"}, "2026-07-07T120000Z")
	if err != nil {
		t.Fatalf("preflight() error = %v", err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	branch := strings.TrimSpace(runGit(t, repo, "rev-parse", "--abbrev-ref", "HEAD"))
	if branch != "tagteam/2026-07-07T120000Z" {
		t.Fatalf("branch = %q", branch)
	}
}

func TestDeterministicDiffIgnoresTagteamRunDirButIncludesUntrackedFiles(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".tagteam/\ntagteam\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "internal", "tagteam"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "internal", "tagteam", "tracked.go"), []byte("package tagteam\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-f", ".gitignore", "README.md", "internal/tagteam/tracked.go")
	runGit(t, repo, "commit", "-m", "init")
	baseline := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))

	if err := os.MkdirAll(filepath.Join(repo, ".tagteam", "runs", "test"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".tagteam", "runs", "test", "ignored.txt"), []byte("ignore me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "internal", "tagteam", "tracked.go"), []byte("package tagteam\n\nconst changed = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "notes.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	patch, _, _, _, err := deterministicDiffOutputs(context.Background(), repo, baseline, filepath.Join(repo, ".tagteam", "tmp.index"))
	if err != nil {
		t.Fatalf("deterministicDiffOutputs() error = %v", err)
	}
	text := string(patch)
	if !strings.Contains(text, "diff --git a/README.md b/README.md") {
		t.Fatalf("patch missing README change:\n%s", text)
	}
	if !strings.Contains(text, "diff --git a/notes.txt b/notes.txt") {
		t.Fatalf("patch missing untracked file:\n%s", text)
	}
	if !strings.Contains(text, "diff --git a/internal/tagteam/tracked.go b/internal/tagteam/tracked.go") {
		t.Fatalf("patch missing tracked ignored-path change:\n%s", text)
	}
	if strings.Contains(text, ".tagteam") {
		t.Fatalf("patch should not include .tagteam artifacts:\n%s", text)
	}
}

func TestRunAdversaryDoesNotRetryInvocationFailures(t *testing.T) {
	app := NewApp(DefaultConfig())
	opts := RunOptions{
		Workdir:   t.TempDir(),
		Adversary: RoleTarget{Adapter: "missing"},
		Timeout:   time.Second,
	}
	_, _, _, err := app.runAdversary(context.Background(), opts, 1, opts.Workdir, filepath.Join(opts.Workdir, "schema.json"), "prompt", "HEAD", "diff", filepath.Join(opts.Workdir, "diff.patch"), "", "", nil, RelayContext{}, "")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestEnsureGitignoreEntry_AppendsOnce(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("node_modules/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureGitignoreEntry(repo, ".tagteam/"); err != nil {
		t.Fatalf("ensureGitignoreEntry() error = %v", err)
	}
	if err := ensureGitignoreEntry(repo, ".tagteam/"); err != nil {
		t.Fatalf("ensureGitignoreEntry() second error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(repo, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), ".tagteam/") != 1 {
		t.Fatalf(".gitignore contents = %q", string(data))
	}
}

func TestGitDirty_IgnoresTeamCLIRoot(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")
	if err := os.MkdirAll(filepath.Join(repo, ".tagteam", "runs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".tagteam", "runs", "artifact.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirty, err := gitDirty(repo)
	if err != nil {
		t.Fatalf("gitDirty() error = %v", err)
	}
	if dirty {
		t.Fatal("expected .tagteam artifacts to be ignored")
	}
}

func TestCaptureDiffArtifact_IncludesUntrackedAndExcludesTagteam(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")
	baseline := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))

	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "new file.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(repo, ".tagteam", "runs", "test-run")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "internal-artifact.txt"), []byte("ignore me\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	artifact, err := captureDiffArtifact(context.Background(), repo, baseline, runDir, 1)
	if err != nil {
		t.Fatalf("captureDiffArtifact() error = %v", err)
	}
	for _, path := range []string{artifact.PatchPath, artifact.NumstatPath, artifact.FilesPath, artifact.SHA256Path} {
		if !fileExists(path) {
			t.Fatalf("expected artifact %s", path)
		}
	}
	if !strings.Contains(artifact.Patch, "diff --git a/README.md b/README.md") {
		t.Fatalf("patch missing tracked modification:\n%s", artifact.Patch)
	}
	if !strings.Contains(artifact.Patch, "diff --git a/new file.txt b/new file.txt") {
		t.Fatalf("patch missing untracked file:\n%s", artifact.Patch)
	}
	if strings.Contains(artifact.Patch, ".tagteam") {
		t.Fatalf("patch should exclude .tagteam artifacts:\n%s", artifact.Patch)
	}
	hashBytes, err := os.ReadFile(artifact.SHA256Path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(artifact.Patch))
	if strings.TrimSpace(string(hashBytes)) != hex.EncodeToString(sum[:]) {
		t.Fatalf("sha256 artifact mismatch")
	}
	if got := artifact.ChangedFiles(); !reflect.DeepEqual(got, []string{"README.md", "new file.txt"}) {
		t.Fatalf("changed files = %#v", got)
	}
}

func TestComputeExitCode_UsesLastTestOnly(t *testing.T) {
	app := NewApp(DefaultConfig())
	final := FinalRun{
		Tests: []TestRun{
			{Passed: false},
			{Passed: true},
		},
	}
	if got := app.computeExitCode(final); got != ExitSuccess {
		t.Fatalf("exit code = %d", got)
	}
}

func TestRoleLabels(t *testing.T) {
	editor, reviewer := roleLabels(ModeSupervisor)
	if editor != "worker" || reviewer != "supervisor" {
		t.Fatalf("supervisor labels = %q/%q", editor, reviewer)
	}
	editor, reviewer = roleLabels(ModeSolo)
	if editor != "solo" || reviewer != "" {
		t.Fatalf("solo labels = %q/%q", editor, reviewer)
	}
	editor, reviewer = roleLabels(ModeAdversarial)
	if editor != "coder" || reviewer != "adversary" {
		t.Fatalf("adversarial labels = %q/%q", editor, reviewer)
	}
	editor, reviewer = roleLabels(ModeRelay)
	if editor != "coder" || reviewer != "supervisor" {
		t.Fatalf("relay labels = %q/%q", editor, reviewer)
	}
	editor, reviewer = roleLabels("")
	if editor != "coder" || reviewer != "adversary" {
		t.Fatalf("zero-value mode labels = %q/%q", editor, reviewer)
	}
}

func TestSupervisorBriefRole(t *testing.T) {
	if got := supervisorBriefRole(false); got != RoleSupervisor {
		t.Fatalf("supervisorBriefRole(false) = %q", got)
	}
	if got := supervisorBriefRole(true); got != RoleCoder {
		t.Fatalf("supervisorBriefRole(true) = %q", got)
	}
}

func TestBuildRoundLimitReportPromptStopsWork(t *testing.T) {
	prompt := BuildRoundLimitReportPrompt("worker", "supervisor", ModeSupervisor, "ship it", "diff", Review{
		Verdict:  "needs_changes",
		Summary:  "needs fixes",
		Findings: []Finding{{Severity: "major", File: "main.go", Issue: "bug", Fix: "fix it"}},
	}, []TestRun{{Command: "go test ./...", Passed: false}})
	for _, want := range []string{
		"The user-defined round limit has been reached",
		"Do not edit",
		"It is acceptable if the change is incomplete",
		"disagree",
		"What remains incomplete or risky",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestParseWorkPlanSelectsRequestedPackage(t *testing.T) {
	raw := []byte(`{
	  "summary": "split work",
	  "packages": [
	    {
	      "id": "P1",
	      "title": "First",
	      "goal": "Do first",
	      "allowed_scope": ["a.go"],
	      "acceptance": ["first passes"],
	      "validation": ["go test ./..."]
	    },
	    {
	      "id": "P2",
	      "title": "Second",
	      "goal": "Do second",
	      "allowed_scope": ["b.go"],
	      "acceptance": ["second passes"],
	      "validation": ["go test ./..."]
	    }
	  ],
	  "selected_package": "P1",
	  "defer": ["P2"]
	}`)
	plan, err := parseWorkPlan(raw, "P2", 5)
	if err != nil {
		t.Fatalf("parseWorkPlan() error = %v", err)
	}
	if plan.SelectedPackage != "P2" {
		t.Fatalf("selected package = %q", plan.SelectedPackage)
	}
	pkg, ok := plan.Selected()
	if !ok || pkg.ID != "P2" {
		t.Fatalf("selected = %#v ok=%t", pkg, ok)
	}
}

func TestParseWorkPlanRejectsTooManyPackages(t *testing.T) {
	raw := []byte(`{
	  "summary": "split work",
	  "packages": [
	    {"id":"P1","title":"First","goal":"Do first","acceptance":["ok"],"validation":["go test ./..."]},
	    {"id":"P2","title":"Second","goal":"Do second","acceptance":["ok"],"validation":["go test ./..."]}
	  ],
	  "selected_package": "P1"
	}`)
	_, err := parseWorkPlan(raw, "", 1)
	if err == nil {
		t.Fatal("expected max package error")
	}
}

func TestParseWorkPlanExtractsFencedJSON(t *testing.T) {
	raw := []byte("```json\n{\"summary\":\"split work\",\"packages\":[{\"id\":\"P1\",\"title\":\"First\",\"goal\":\"Do first\",\"acceptance\":[\"ok\"],\"validation\":[\"go test ./...\"]}],\"selected_package\":\"P1\"}\n```")
	plan, err := parseWorkPlan(raw, "", 5)
	if err != nil {
		t.Fatalf("parseWorkPlan() error = %v", err)
	}
	if plan.SelectedPackage != "P1" {
		t.Fatalf("selected package = %q", plan.SelectedPackage)
	}
}

func TestParseWorkPlanExtractsWrappedJSON(t *testing.T) {
	raw := []byte("Here is the work plan:\n{\"schema_version\":1,\"summary\":\"split work\",\"packages\":[{\"id\":\"P1\",\"title\":\"First\",\"goal\":\"Do first\",\"acceptance\":[\"ok\"],\"validation\":[\"go test ./...\"]}],\"selected_package\":\"P1\"}\nDone.")
	plan, err := parseWorkPlan(raw, "", 5)
	if err != nil {
		t.Fatalf("parseWorkPlan() error = %v", err)
	}
	if plan.SchemaVersion != ArtifactSchemaVersion || plan.SelectedPackage != "P1" {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestParseWorkPlanInvalidReturnsContractError(t *testing.T) {
	_, err := parseWorkPlan([]byte(`{"schema_version":1,"summary":"missing packages","selected_package":"P1"}`), "", 5)
	if err == nil {
		t.Fatal("expected parseWorkPlan() error")
	}
	if !IsOutputContractError(err) {
		t.Fatalf("expected OutputContractError, got %T: %v", err, err)
	}
}

func TestBuildRelaySupervisorReviewPromptIncludesPostScout(t *testing.T) {
	prompt := BuildRelaySupervisorReviewPrompt(
		"ship it",
		"abc123",
		"brief",
		Scout{Mode: "recon", Summary: "mapped files", RelevantFiles: []string{"runner.go"}, DoNotBlock: true},
		Scout{Mode: "polish", Summary: "cleanup", Items: []ScoutItem{{Severity: "minor", File: "runner.go", Line: 12, Issue: "duplication", Suggestion: "extract helper"}}, DoNotBlock: true},
		"instructions",
		"diff",
		"tests passed",
		false,
	)
	for _, want := range []string{"Post-scout advisory JSON", `"mode": "polish"`, "duplication", "advisory only"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestRunAdapter_WritesMissingTranscript(t *testing.T) {
	app := NewApp(DefaultConfig())
	tmp := t.TempDir()
	outputPath := filepath.Join(tmp, "adversary-round-1.json")
	adapter := fakeAdapter{
		build: func(role Role, req Request) (*CommandSpec, error) {
			return &CommandSpec{
				Argv: []string{"sh", "-lc", "printf '{\"verdict\":\"pass\",\"summary\":\"ok\",\"findings\":[],\"test_suggestions\":[]}'"},
				Dir:  tmp,
			}, nil
		},
		parse: func(role Role, raw []byte) (Result, error) {
			var review Review
			if err := json.Unmarshal(raw, &review); err != nil {
				return Result{}, err
			}
			return Result{Raw: raw, Review: &review}, nil
		},
	}
	_, err := app.runAdapter(context.Background(), adapter, RoleAdversary, Request{
		Context:    context.Background(),
		Prompt:     "review prompt",
		RunDir:     tmp,
		OutputPath: outputPath,
		Timeout:    time.Second,
		InputMode:  "inline",
	}, false)
	if err != nil {
		t.Fatalf("runAdapter() error = %v", err)
	}
	if !fileExists(outputPath) {
		t.Fatal("expected transcript file to be written")
	}
	if !fileExists(outputPath + ".raw") {
		t.Fatal("expected raw output quarantine artifact")
	}
	if !fileExists(outputPath + ".parsed.json") {
		t.Fatal("expected parsed output artifact")
	}
	deliveries, err := filepath.Glob(filepath.Join(tmp, "deliveries", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 1 {
		t.Fatalf("delivery records = %v", deliveries)
	}
	var delivery DeliveryRecord
	data, err := os.ReadFile(deliveries[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &delivery); err != nil {
		t.Fatal(err)
	}
	if delivery.SchemaVersion != ArtifactSchemaVersion || delivery.PromptPath == "" || delivery.OutputPath != outputPath || delivery.InputMode != "inline" {
		t.Fatalf("delivery = %#v", delivery)
	}
}

func TestRunAdapter_DirectAdapterRejectsOversizeBeforeWritingTranscript(t *testing.T) {
	app := NewApp(DefaultConfig())
	tmp := t.TempDir()
	outputPath := filepath.Join(tmp, "adversary-round-1.json")
	adapter := fakeDirectAdapter{
		build: func(role Role, req Request) (*CommandSpec, error) {
			return &CommandSpec{
				Argv: []string{"openai-compatible", "POST", "https://example.test/v1/chat/completions"},
				Dir:  tmp,
			}, nil
		},
		direct: func(role Role, req Request) (Result, error) {
			raw := []byte(strings.Repeat("x", 32))
			return Result{Raw: raw, Text: string(raw)}, nil
		},
	}
	_, err := app.runAdapter(context.Background(), adapter, RoleAdversary, Request{
		Context:        context.Background(),
		Prompt:         "review prompt",
		RunDir:         tmp,
		OutputPath:     outputPath,
		Timeout:        time.Second,
		InputMode:      "inline",
		MaxOutputBytes: 8,
	}, false)
	if err == nil {
		t.Fatal("expected runAdapter() error")
	}
	if fileExists(outputPath) {
		t.Fatal("expected transcript file to remain unwritten")
	}
}

func TestRunAdapter_RoleInvocationBudgetBlocksNextCall(t *testing.T) {
	app := NewApp(DefaultConfig())
	tmp := t.TempDir()
	budget := &InvocationBudget{Max: 1}
	adapter := fakeDirectAdapter{
		build: func(role Role, req Request) (*CommandSpec, error) {
			return &CommandSpec{Argv: []string{"fake"}, Dir: tmp}, nil
		},
		direct: func(role Role, req Request) (Result, error) {
			return Result{Text: "ok"}, nil
		},
	}
	req := Request{Context: context.Background(), Prompt: "prompt", RunDir: tmp, Budget: budget}
	if _, err := app.runAdapter(context.Background(), adapter, RoleReporter, req, false); err != nil {
		t.Fatalf("first runAdapter() error = %v", err)
	}
	_, err := app.runAdapter(context.Background(), adapter, RoleReporter, req, false)
	if err == nil || !strings.Contains(err.Error(), "max_role_invocations=1") {
		t.Fatalf("expected invocation budget error, got %v", err)
	}
	if budget.Used != 1 {
		t.Fatalf("budget used = %d", budget.Used)
	}
}

func TestBuildReviewBundleWritesExpectedFiles(t *testing.T) {
	runDir := t.TempDir()
	diffPath := filepath.Join(runDir, "diff-round-1.patch")
	filesPath := filepath.Join(runDir, "diff-round-1.files.json")
	mustWriteFile(t, diffPath, "diff --git a/README.md b/README.md\n")
	mustWriteFile(t, filesPath, "{}\n")
	bundle, err := buildReviewBundle(runDir, RunOptions{
		Prompt:    "review this",
		Mode:      ModeRelay,
		Coder:     RoleTarget{Adapter: "codex", Model: "gpt"},
		Adversary: RoleTarget{Adapter: "claude", Model: "sonnet"},
		Scout:     RoleTarget{Adapter: "agy", Model: "flash"},
	}, "supervisor", 1, "abc123", DiffArtifact{PatchPath: diffPath, FilesPath: filesPath}, "tests passed", filepath.Join(runDir, "coder-round-1.md"), RelayContext{}, nil)
	if err != nil {
		t.Fatalf("buildReviewBundle() error = %v", err)
	}
	for _, path := range []string{bundle.PromptPath, bundle.ConfigSummaryPath, bundle.TestOutputPath, filepath.Join(filepath.Dir(bundle.PromptPath), "bundle.json")} {
		if !fileExists(path) {
			t.Fatalf("expected bundle file %s", path)
		}
	}
	if bundle.DiffPath != diffPath || bundle.FilesPath != filesPath {
		t.Fatalf("bundle diff/files paths = %#v", bundle)
	}
	if bundle.CoderOutputPath == "" {
		t.Fatalf("expected coder output path in bundle: %#v", bundle)
	}
}

func TestRunAdapter_RedactsOverlaySecretInValidationArtifact(t *testing.T) {
	app := NewApp(DefaultConfig())
	tmp := t.TempDir()
	outputPath := filepath.Join(tmp, "adversary-round-1.json")
	adapter := fakeAdapter{
		build: func(role Role, req Request) (*CommandSpec, error) {
			return &CommandSpec{
				Argv: []string{"sh", "-c", "printf '{\"bad\":true}'"},
				Dir:  tmp,
			}, nil
		},
		parse: func(role Role, raw []byte) (Result, error) {
			return Result{}, &OutputContractError{Err: fmt.Errorf("provider echoed overlay-secret-token")}
		},
	}
	_, err := app.runAdapter(context.Background(), adapter, RoleAdversary, Request{
		Context:    context.Background(),
		Prompt:     "review prompt",
		RunDir:     tmp,
		OutputPath: outputPath,
		EnvOverlay: map[string]string{"PURDUE_API_KEY": "overlay-secret-token"},
		Timeout:    time.Second,
		InputMode:  "inline",
	}, false)
	if err == nil {
		t.Fatal("expected runAdapter() error")
	}
	data, readErr := os.ReadFile(outputPath + ".validation-error.txt")
	if readErr != nil {
		t.Fatalf("read validation artifact: %v", readErr)
	}
	if strings.Contains(string(data), "overlay-secret-token") {
		t.Fatalf("validation artifact leaked overlay secret: %q", string(data))
	}
	if !strings.Contains(string(data), redactedSecret) {
		t.Fatalf("validation artifact missing redaction marker: %q", string(data))
	}
}

func TestReadRunPrompt_FallsBackToMeta(t *testing.T) {
	runDir := t.TempDir()
	meta := Meta{Prompt: "review prompt"}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "meta.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	prompt, err := readRunPrompt(runDir, "")
	if err != nil {
		t.Fatalf("readRunPrompt() error = %v", err)
	}
	if prompt != "review prompt" {
		t.Fatalf("prompt = %q", prompt)
	}
}

type fakeAdapter struct {
	build func(role Role, req Request) (*CommandSpec, error)
	parse func(role Role, raw []byte) (Result, error)
}

func (f fakeAdapter) ID() string { return "fake" }
func (f fakeAdapter) Detect(ctx context.Context) (VersionInfo, error) {
	return VersionInfo{Found: true, Runnable: true}, nil
}
func (f fakeAdapter) Capabilities() CapabilitySet { return CapabilitySet{} }
func (f fakeAdapter) BuildCmd(role Role, req Request) (*CommandSpec, error) {
	return f.build(role, req)
}
func (f fakeAdapter) ParseResult(role Role, raw []byte) (Result, error) {
	return f.parse(role, raw)
}

type fakeDirectAdapter struct {
	build  func(role Role, req Request) (*CommandSpec, error)
	direct func(role Role, req Request) (Result, error)
}

func (f fakeDirectAdapter) ID() string { return "fake-direct" }
func (f fakeDirectAdapter) Detect(ctx context.Context) (VersionInfo, error) {
	return VersionInfo{Found: true, Runnable: true}, nil
}
func (f fakeDirectAdapter) Capabilities() CapabilitySet { return CapabilitySet{} }
func (f fakeDirectAdapter) BuildCmd(role Role, req Request) (*CommandSpec, error) {
	return f.build(role, req)
}
func (f fakeDirectAdapter) ParseResult(role Role, raw []byte) (Result, error) {
	return Result{}, nil
}
func (f fakeDirectAdapter) RunDirect(role Role, req Request) (Result, error) {
	return f.direct(role, req)
}

func runGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return string(out)
}

func TestIntegration_GoslingAdversaryRejected(t *testing.T) {
	app := NewApp(DefaultConfig())
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	opts := RunOptions{
		Workdir:   repo,
		Adversary: RoleTarget{Adapter: "gosling"},
		Timeout:   time.Second,
	}
	_, err := app.Review(context.Background(), opts, "review this code")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "gosling is not supported as an adversary adapter") {
		t.Fatalf("expected error containing 'gosling is not supported as an adversary adapter', got: %v", err)
	}
}

func TestRunLoop_PersistsFinalOnMidRunFailure(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "hello\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	app := NewApp(DefaultConfig())
	final, err := app.Run(context.Background(), RunOptions{
		Prompt:    "ship it",
		Workdir:   repo,
		Mode:      ModeAdversarial,
		Coder:     RoleTarget{Adapter: "missing-adapter"},
		Adversary: RoleTarget{Adapter: "claude"},
		Rounds:    1,
		Timeout:   10 * time.Second,
	})
	if err == nil {
		t.Fatal("expected unknown adapter error")
	}
	if final.RunDir == "" {
		t.Fatal("expected run dir in failed final")
	}
	var persisted FinalRun
	readJSONFile(t, filepath.Join(final.RunDir, "final.json"), &persisted)
	if persisted.ExitCode != ExitInvalidArguments {
		t.Fatalf("persisted exit = %d, want %d", persisted.ExitCode, ExitInvalidArguments)
	}
	if persisted.Verdict != "error" {
		t.Fatalf("persisted verdict = %q", persisted.Verdict)
	}
	var latest LatestRun
	readJSONFile(t, filepath.Join(repo, ".tagteam", "latest.json"), &latest)
	if latest.RunID != final.RunID || latest.FinalPath != filepath.Join(final.RunDir, "final.json") {
		t.Fatalf("latest = %#v final=%#v", latest, final)
	}
}

func TestReview_PreflightsReviewerRunnable(t *testing.T) {
	oldLookPath := execLookPath
	oldCommandContext := execCommandContext
	defer func() {
		execLookPath = oldLookPath
		execCommandContext = oldCommandContext
	}()
	execLookPath = func(file string) (string, error) {
		return "", exec.ErrNotFound
	}

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	app := NewApp(DefaultConfig())
	_, err := app.Review(context.Background(), RunOptions{
		Workdir:   repo,
		Mode:      ModeAdversarial,
		Coder:     RoleTarget{Adapter: "codex"},
		Adversary: RoleTarget{Adapter: "claude"},
		DryRun:    true,
		Timeout:   time.Second,
	}, "review this diff")
	if err == nil {
		t.Fatal("expected preflight error")
	}
	if got := ExitCode(err); got != ExitPreflightFailed {
		t.Fatalf("exit code = %d, want %d: %v", got, ExitPreflightFailed, err)
	}
	if !strings.Contains(err.Error(), "claude not runnable") {
		t.Fatalf("error = %v", err)
	}
}

func TestReview_PersistsFinalOnReviewerFailure(t *testing.T) {
	installFakeBinaries(t, map[string]string{"claude": fakeClaudeInvokeFailScript})

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	app := NewApp(DefaultConfig())
	final, err := app.Review(context.Background(), RunOptions{
		Workdir:   repo,
		Mode:      ModeAdversarial,
		Coder:     RoleTarget{Adapter: "codex"},
		Adversary: RoleTarget{Adapter: "claude"},
		Timeout:   time.Second,
	}, "review this diff")
	if err == nil {
		t.Fatal("expected reviewer failure")
	}
	if final.RunDir == "" {
		t.Fatal("expected failed review to return run dir")
	}
	var persisted FinalRun
	readJSONFile(t, filepath.Join(final.RunDir, "final.json"), &persisted)
	if persisted.ExitCode != ExitAdapterFailure {
		t.Fatalf("persisted exit = %d, want %d", persisted.ExitCode, ExitAdapterFailure)
	}
	if persisted.Verdict != "error" {
		t.Fatalf("persisted verdict = %q", persisted.Verdict)
	}
	if persisted.LatestDiffPath == "" {
		t.Fatalf("expected diff path in persisted failure")
	}
	if status := persisted.RoleStatuses["adversary"]; status.Status != "failed" {
		t.Fatalf("adversary status = %#v", status)
	}
	var latest LatestRun
	readJSONFile(t, filepath.Join(repo, ".tagteam", "latest.json"), &latest)
	if latest.RunID != final.RunID || latest.FinalPath != filepath.Join(final.RunDir, "final.json") {
		t.Fatalf("latest = %#v final=%#v", latest, final)
	}
}

func TestReview_DoesNotPreflightExplicitEditorTarget(t *testing.T) {
	oldLookPath := execLookPath
	oldCommandContext := execCommandContext
	defer func() {
		execLookPath = oldLookPath
		execCommandContext = oldCommandContext
	}()
	var probed []string
	execLookPath = func(file string) (string, error) {
		probed = append(probed, file)
		if file == "claude" {
			return filepath.Join("/mock/bin", file), nil
		}
		return "", exec.ErrNotFound
	}
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "printf", "1.0")
	}

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	app := NewApp(DefaultConfig())
	_, err := app.Review(context.Background(), RunOptions{
		Workdir:       repo,
		Mode:          ModeAdversarial,
		Coder:         RoleTarget{Adapter: "codex"},
		CoderExplicit: true,
		Adversary:     RoleTarget{Adapter: "claude"},
		DryRun:        true,
		Timeout:       time.Second,
	}, "review this diff")
	if err != nil {
		t.Fatalf("Review() error = %v", err)
	}
	if strings.Contains(strings.Join(probed, ","), "codex") {
		t.Fatalf("review should not preflight explicit editor adapter, probed=%v", probed)
	}
}

func TestReview_DoesNotPreflightExplicitScoutTarget(t *testing.T) {
	oldLookPath := execLookPath
	oldCommandContext := execCommandContext
	defer func() {
		execLookPath = oldLookPath
		execCommandContext = oldCommandContext
	}()
	var probed []string
	execLookPath = func(file string) (string, error) {
		probed = append(probed, file)
		if file == "claude" {
			return filepath.Join("/mock/bin", file), nil
		}
		return "", exec.ErrNotFound
	}
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "printf", "1.0")
	}

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	app := NewApp(DefaultConfig())
	_, err := app.Review(context.Background(), RunOptions{
		Workdir:       repo,
		Mode:          ModeRelay,
		Scout:         RoleTarget{Adapter: "agy"},
		ScoutExplicit: true,
		Coder:         RoleTarget{Adapter: "codex"},
		Adversary:     RoleTarget{Adapter: "claude"},
		DryRun:        true,
		Timeout:       time.Second,
	}, "review this diff")
	if err != nil {
		t.Fatalf("Review() error = %v", err)
	}
	if strings.Contains(strings.Join(probed, ","), "agy") {
		t.Fatalf("review should not preflight explicit scout adapter, probed=%v", probed)
	}
}

func TestReview_UnknownReviewerStillInvalidArguments(t *testing.T) {
	app := NewApp(DefaultConfig())
	_, err := app.Review(context.Background(), RunOptions{
		Workdir:   t.TempDir(),
		Mode:      ModeAdversarial,
		Coder:     RoleTarget{Adapter: "codex"},
		Adversary: RoleTarget{Adapter: "missing"},
		DryRun:    true,
		Timeout:   time.Second,
	}, "review this diff")
	if err == nil {
		t.Fatal("expected invalid-arguments error")
	}
	if got := ExitCode(err); got != ExitInvalidArguments {
		t.Fatalf("exit code = %d, want %d: %v", got, ExitInvalidArguments, err)
	}
}

func TestRunLoop_SupervisorModeDryRun(t *testing.T) {
	oldLookPath := execLookPath
	oldCommandContext := execCommandContext
	defer func() {
		execLookPath = oldLookPath
		execCommandContext = oldCommandContext
	}()
	execLookPath = func(file string) (string, error) {
		return filepath.Join("/mock/bin", file), nil
	}
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "printf", "1.0")
	}

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	app := NewApp(DefaultConfig())
	opts := RunOptions{
		Prompt:    "add a feature",
		Workdir:   repo,
		Mode:      ModeSupervisor,
		Coder:     RoleTarget{Adapter: "agy"},
		Adversary: RoleTarget{Adapter: "claude"},
		Rounds:    2,
		DryRun:    true,
		Timeout:   5 * time.Second,
	}
	final, err := app.Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if final.Verdict != "pass" {
		t.Fatalf("verdict = %q", final.Verdict)
	}
	if final.RoundsCompleted != 1 {
		t.Fatalf("rounds completed = %d", final.RoundsCompleted)
	}
	if final.Adapters["worker"] != "agy" || final.Adapters["supervisor"] != "claude" {
		t.Fatalf("adapters = %#v", final.Adapters)
	}
	if _, ok := final.Adapters["coder"]; ok {
		t.Fatalf("did not expect legacy 'coder' label in supervisor mode: %#v", final.Adapters)
	}
	if !strings.Contains(final.LatestReviewPath, "supervisor-round-1.json") {
		t.Fatalf("latest review path = %q", final.LatestReviewPath)
	}
}

func TestRunLoop_SoloModeWritesArtifactsWithoutReview(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "claude-args.log")
	t.Setenv("CLAUDE_ARGS_LOG", logPath)
	installFakeClaudeBinary(t)

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	app := NewApp(DefaultConfig())
	final, err := app.Run(context.Background(), RunOptions{
		Prompt:  "add a feature",
		Workdir: repo,
		Mode:    ModeSolo,
		Coder:   RoleTarget{Adapter: "claude", Model: "sonnet"},
		Rounds:  1,
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if final.Mode != ModeSolo {
		t.Fatalf("mode = %q", final.Mode)
	}
	if final.Review != nil || final.LatestReviewPath != "" {
		t.Fatalf("solo run should not have review: review=%#v path=%q", final.Review, final.LatestReviewPath)
	}
	if final.Verdict != "done" || final.ExitCode != ExitSuccess {
		t.Fatalf("verdict/exit = %q/%d", final.Verdict, final.ExitCode)
	}
	if final.Adapters["solo"] != "claude" || final.Models["solo"] != "sonnet" {
		t.Fatalf("adapters/models = %#v %#v", final.Adapters, final.Models)
	}
	for _, name := range []string{
		"solo-round-1.md",
		"diff-round-1.patch",
		"diff-round-1.numstat",
		"diff-round-1.files.json",
		"diff-round-1.sha256",
		"final.json",
	} {
		if !fileExists(filepath.Join(final.RunDir, name)) {
			t.Fatalf("expected solo artifact %s in %s", name, final.RunDir)
		}
	}
	if fileExists(filepath.Join(final.RunDir, "review-schema.json")) {
		t.Fatal("solo run should not write review schema")
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read claude args log: %v", err)
	}
	log := string(data)
	if strings.Contains(log, "--append-system-prompt") {
		t.Fatalf("solo mode should not use adapter-specific system prompt append:\n%s", log)
	}
	if !strings.Contains(log, "solo tagteam run") {
		t.Fatalf("solo prompt missing role framing:\n%s", log)
	}
}

func TestRunLoop_SoloModeTestFailureExitsTwo(t *testing.T) {
	installFakeClaudeBinary(t)

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	app := NewApp(DefaultConfig())
	final, err := app.Run(context.Background(), RunOptions{
		Prompt:  "add a feature",
		Workdir: repo,
		Mode:    ModeSolo,
		Coder:   RoleTarget{Adapter: "claude"},
		Rounds:  1,
		TestCmd: "false",
		Timeout: 10 * time.Second,
	})
	if err == nil {
		t.Fatal("expected test failure error")
	}
	if ExitCode(err) != ExitTestsFailed || final.ExitCode != ExitTestsFailed {
		t.Fatalf("exit = err:%d final:%d err=%v", ExitCode(err), final.ExitCode, err)
	}
	if final.Review != nil {
		t.Fatalf("solo test failure should not have review: %#v", final.Review)
	}
	if len(final.Tests) != 1 || final.Tests[0].Passed {
		t.Fatalf("tests = %#v", final.Tests)
	}
}

func TestRun_NormalizesEmptyModeToSupervisor(t *testing.T) {
	oldLookPath := execLookPath
	oldCommandContext := execCommandContext
	defer func() {
		execLookPath = oldLookPath
		execCommandContext = oldCommandContext
	}()
	execLookPath = func(file string) (string, error) {
		return filepath.Join("/mock/bin", file), nil
	}
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "printf", "1.0")
	}

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	app := NewApp(DefaultConfig())
	final, err := app.Run(context.Background(), RunOptions{
		Prompt:    "add a feature",
		Workdir:   repo,
		Coder:     RoleTarget{Adapter: "agy"},
		Adversary: RoleTarget{Adapter: "claude"},
		Rounds:    1,
		DryRun:    true,
		Timeout:   5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if final.Mode != ModeSupervisor {
		t.Fatalf("empty mode should normalize to supervisor, got %q", final.Mode)
	}
	if final.Adapters["worker"] != "agy" || final.Adapters["supervisor"] != "claude" {
		t.Fatalf("expected supervisor-mode labels, got %#v", final.Adapters)
	}
}

func TestRunLoop_SupervisorCanEditUsesCoderRoleForBrief(t *testing.T) {
	oldLookPath := execLookPath
	oldCommandContext := execCommandContext
	defer func() {
		execLookPath = oldLookPath
		execCommandContext = oldCommandContext
	}()
	execLookPath = func(file string) (string, error) {
		return filepath.Join("/mock/bin", file), nil
	}
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "printf", "1.0")
	}

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	app := NewApp(DefaultConfig())
	opts := RunOptions{
		Prompt:            "add a feature",
		Workdir:           repo,
		Mode:              ModeSupervisor,
		Coder:             RoleTarget{Adapter: "agy"},
		Adversary:         RoleTarget{Adapter: "claude"},
		SupervisorCanEdit: true,
		Rounds:            1,
		DryRun:            true,
		Timeout:           5 * time.Second,
	}
	final, err := app.Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if final.Verdict != "pass" {
		t.Fatalf("verdict = %q", final.Verdict)
	}
}

func TestRunLoop_SupervisorSlicingWritesWorkPlanAndScopesWorker(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "claude-args.log")
	installFakeClaudeBinary(t)

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	app := NewApp(DefaultConfig())
	final, err := app.Run(context.Background(), RunOptions{
		Prompt:            "add a feature",
		Workdir:           repo,
		Mode:              ModeSupervisor,
		Coder:             RoleTarget{Adapter: "claude"},
		Adversary:         RoleTarget{Adapter: "claude"},
		SupervisorSlicing: true,
		MaxPackages:       5,
		Rounds:            1,
		Timeout:           10 * time.Second,
		// The slicing invocation runs the claude adapter under RoleSupervisor
		// (restricted env), so the args-log path must travel via the overlay,
		// which the restricted env forwards, rather than the ambient shell env.
		EnvOverlay: map[string]string{"CLAUDE_ARGS_LOG": logPath},
	})
	if err == nil {
		t.Fatal("expected blocking-findings error from fake supervisor review")
	}
	if final.WorkPlan == nil {
		t.Fatal("expected work plan in final run")
	}
	if final.SelectedPackage == nil || final.SelectedPackage.ID != "P1" {
		t.Fatalf("selected package = %#v", final.SelectedPackage)
	}
	if len(final.RemainingPackages) != 1 || !strings.Contains(final.RemainingPackages[0], "P2") {
		t.Fatalf("remaining packages = %#v", final.RemainingPackages)
	}
	for _, name := range []string{"supervisor-work-plan.json", "work-plan-schema.json", "supervisor-brief.md"} {
		if !fileExists(filepath.Join(final.RunDir, name)) {
			t.Fatalf("expected artifact %s in %s", name, final.RunDir)
		}
	}
	for _, name := range []string{"plan.json", "plan-events.jsonl"} {
		if !fileExists(filepath.Join(final.RunDir, name)) {
			t.Fatalf("expected plan artifact %s in %s", name, final.RunDir)
		}
	}
	plan, err := readExecutionPlan(final.RunDir)
	if err != nil {
		t.Fatalf("readExecutionPlan() error = %v", err)
	}
	if plan.Status != "failed" {
		t.Fatalf("plan status = %q", plan.Status)
	}
	if len(plan.Items) != 3 {
		t.Fatalf("plan items = %#v", plan.Items)
	}
	if plan.Items[0].ID != "P1" || plan.Items[0].Status != PlanStatusFailed {
		t.Fatalf("selected plan item = %#v", plan.Items[0])
	}
	if plan.Items[1].ID != "P2" || plan.Items[1].Status != PlanStatusPending {
		t.Fatalf("deferred plan item should remain pending on failed selected package: %#v", plan.Items[1])
	}
	if plan.Items[2].ID != "R1-F1" || plan.Items[2].Status != PlanStatusNeedsArbitration {
		t.Fatalf("review finding item = %#v", plan.Items[2])
	}
	if final.Plan == nil || final.Plan.Total != 3 || final.Plan.Failed != 1 || final.Plan.Arbitration != 1 {
		t.Fatalf("final plan summary = %#v", final.Plan)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read claude args log: %v", err)
	}
	sections := strings.Split(string(data), "\n---\n")
	if len(sections) < 2 {
		t.Fatalf("expected at least slicing and worker invocations, log:\n%s", string(data))
	}
	slicingInvocation := ""
	workerInvocation := ""
	for _, section := range sections {
		if strings.Contains(section, "implementation work packages") {
			slicingInvocation = section
		}
		if strings.Contains(section, "You are the worker") && strings.Contains(section, "Selected work package") {
			workerInvocation = section
		}
	}
	if slicingInvocation == "" {
		t.Fatalf("missing slicing invocation, log:\n%s", string(data))
	}
	if !strings.Contains(slicingInvocation, "--json-schema") || !strings.Contains(slicingInvocation, `"packages"`) {
		t.Fatalf("slicing invocation did not use work-plan schema:\n%s", slicingInvocation)
	}
	if workerInvocation == "" {
		t.Fatalf("missing worker invocation, log:\n%s", string(data))
	}
	if !strings.Contains(workerInvocation, "Selected work package") || !strings.Contains(workerInvocation, "P1") {
		t.Fatalf("worker invocation was not package-scoped:\n%s", workerInvocation)
	}
	if strings.Contains(workerInvocation, "Deferred follow-up") {
		t.Fatalf("worker invocation should not include deferred package details:\n%s", workerInvocation)
	}
}

// fakeClaudeScript returns a shell script that emulates the parts of the
// `claude` CLI tagteam depends on: --version for Detect, and, for -p
// invocations, a coder-style "ok" result unless the invocation carries the
// adversary/supervisor "dontAsk" permission mode, in which case it returns a
// blocking (needs_changes/major) review.
const fakeClaudeScript = `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "1.0.0"
  exit 0
fi
stdin=$(cat)
if [ -n "$CLAUDE_ARGS_LOG" ]; then
  printf '%s\n%s\n---\n' "$*" "$stdin" >> "$CLAUDE_ARGS_LOG"
fi
match="$* $stdin"
case "$match" in
  *"bounded host-controlled orchestration workflow"*)
    mode="supervisor"
    case "$match" in *"Current mode: relay"*) mode="relay" ;; esac
    source="supervisor"
    case "$match" in *"implementation worker/coder"*) source="worker" ;; esac
    rec="keep"
    target="$mode"
    reason="current mode is appropriate"
    case "$TAGTEAM_TEST_ORCH_ADVISORY:$mode:$source" in
      simplify:relay:supervisor)
        rec="simplify"; target="supervisor"; reason="direct supervisor workflow is enough"
        ;;
      escalate:supervisor:worker)
        rec="escalate"; target="relay"; reason="worker needs scout context"
        ;;
      escalate:supervisor:supervisor)
        rec="escalate"; target="relay"; reason="supervisor agrees scout is needed"
        ;;
      conflict:supervisor:worker)
        rec="escalate"; target="relay"; reason="worker wants more context"
        ;;
      conflict:supervisor:supervisor)
        rec="keep"; target="supervisor"; reason="supervisor prefers simpler mode"
        ;;
      invalid:*)
        printf '%s' '{"result":"not-json","session_id":"","total_cost_usd":0}'
        exit 0
        ;;
    esac
    printf '{"result":"{\\"schema_version\\":1,\\"recommendation\\":\\"%s\\",\\"target_mode\\":\\"%s\\",\\"reason\\":\\"%s\\",\\"confidence\\":\\"high\\"}","session_id":"","total_cost_usd":0}' "$rec" "$target" "$reason"
    exit 0
    ;;
  *"implementation work packages"*)
    printf '%s' '{"result":"{\"schema_version\":1,\"summary\":\"Implement package one\",\"packages\":[{\"id\":\"P1\",\"title\":\"Package one\",\"goal\":\"Do the first slice\",\"allowed_scope\":[\"README.md\"],\"acceptance\":[\"README updated\"],\"validation\":[\"go test ./...\"]},{\"id\":\"P2\",\"title\":\"Package two\",\"goal\":\"Deferred follow-up\",\"allowed_scope\":[\"README.md\"],\"acceptance\":[\"follow-up done\"],\"validation\":[\"go test ./...\"]}],\"selected_package\":\"P1\",\"defer\":[\"P2\"]}","session_id":"","total_cost_usd":0}'
    exit 0
    ;;
esac
is_review=0
for arg in "$@"; do
  if [ "$arg" = "dontAsk" ]; then
    is_review=1
  fi
done
if [ "$is_review" = "1" ]; then
  printf '%s' '{"result":"{\"verdict\":\"needs_changes\",\"summary\":\"needs fixes\",\"findings\":[{\"severity\":\"major\",\"file\":\"main.go\",\"issue\":\"bug\",\"fix\":\"fix it\"}],\"test_suggestions\":[]}","session_id":"","total_cost_usd":0}'
else
  printf '%s' '{"result":"ok","session_id":"sess1","total_cost_usd":0}'
fi
`

const fakeClaudeInvokeFailScript = `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "1.0.0"
  exit 0
fi
echo "reviewer exploded" >&2
exit 7
`

const fakeAgyScript = `#!/bin/sh
if [ "$1" = "--help" ] || [ "$1" = "--version" ]; then
  echo "agy fake"
  exit 0
fi
stdin=$(cat)
if [ -n "$AGY_ARGS_LOG" ]; then
  printf '%s\n%s\n---\n' "$*" "$stdin" >> "$AGY_ARGS_LOG"
fi
printf '%s' '{"relevant_files":["README.md"],"likely_entry_points":["README"],"existing_patterns":["plain text"],"risks":["none"],"suggested_tests":["go test ./..."]}'
`

const fakeAgyFailScript = `#!/bin/sh
if [ "$1" = "--help" ] || [ "$1" = "--version" ]; then
  echo "agy fake"
  exit 0
fi
echo "scout adapter failed" >&2
exit 2
`

const fakeAgyInvalidScript = `#!/bin/sh
if [ "$1" = "--help" ] || [ "$1" = "--version" ]; then
  echo "agy fake"
  exit 0
fi
printf '%s' 'not-json'
`

func installFakeClaudeBinary(t *testing.T) {
	t.Helper()
	binDir := t.TempDir()
	scriptPath := filepath.Join(binDir, "claude")
	if err := os.WriteFile(scriptPath, []byte(fakeClaudeScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func installFakeBinaries(t *testing.T, scripts map[string]string) {
	t.Helper()
	binDir := t.TempDir()
	for name, script := range scripts {
		scriptPath := filepath.Join(binDir, name)
		if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestRunLoop_RelayModeWritesExpectedArtifacts(t *testing.T) {
	installFakeBinaries(t, map[string]string{
		"agy":    fakeAgyScript,
		"claude": fakeClaudeScript,
	})
	agyLogPath := filepath.Join(t.TempDir(), "agy.log")

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	app := NewApp(DefaultConfig())
	final, err := app.Run(context.Background(), RunOptions{
		Prompt:         "add a feature",
		Workdir:        repo,
		Mode:           ModeRelay,
		Scout:          RoleTarget{Adapter: "agy", Model: "gemini-3.5-flash-low"},
		Coder:          RoleTarget{Adapter: "claude"},
		Adversary:      RoleTarget{Adapter: "claude"},
		ScoutMode:      "recon",
		PostScoutMode:  "polish",
		ScoutRetrieval: true,
		Rounds:         1,
		Timeout:        10 * time.Second,
		EnvOverlay:     map[string]string{"AGY_ARGS_LOG": agyLogPath},
	})
	if err == nil {
		t.Fatal("expected blocking-findings error from fake supervisor review")
	}
	if final.Mode != ModeRelay {
		t.Fatalf("mode = %q", final.Mode)
	}
	for _, name := range []string{
		"supervisor-brief.md",
		"orchestration-decision.json",
		"retrieval-round-1.json",
		"scout-context-round-1.json",
		"scout-execution-round-1.json",
		"scout-round-1.json",
		"supervisor-instructions.md",
		"coder-round-1.md",
		"diff-round-1.patch",
		"post-scout-execution-round-1.json",
		"post-scout-round-1.json",
		"supervisor-review-round-1.json",
		"final.json",
	} {
		if !fileExists(filepath.Join(final.RunDir, name)) {
			t.Fatalf("expected relay artifact %s in %s", name, final.RunDir)
		}
	}
	if final.Adapters["scout"] != "agy" || final.Adapters["coder"] != "claude" || final.Adapters["supervisor"] != "claude" {
		t.Fatalf("adapters = %#v", final.Adapters)
	}
	agyLog, err := os.ReadFile(agyLogPath)
	if err != nil {
		t.Fatalf("read agy log: %v", err)
	}
	if !strings.Contains(string(agyLog), "Host retrieval evidence") {
		t.Fatalf("scout prompt did not include retrieval context:\n%s", string(agyLog))
	}
}

func TestRunLoop_RelaySimplifiesToSupervisorBeforeScout(t *testing.T) {
	t.Setenv("TAGTEAM_TEST_ORCH_ADVISORY", "simplify")
	installFakeClaudeBinary(t)

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "hello\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	app := NewApp(DefaultConfig())
	final, err := app.Run(context.Background(), RunOptions{
		Prompt:             "fix typo",
		Workdir:            repo,
		Mode:               ModeRelay,
		Scout:              RoleTarget{Adapter: "agy", Model: "gemini-3.5-flash-low"},
		Coder:              RoleTarget{Adapter: "claude"},
		Adversary:          RoleTarget{Adapter: "claude"},
		ScoutMode:          "recon",
		PostScoutMode:      "polish",
		ScoutRetrieval:     true,
		ScoutFailurePolicy: "continue",
		Rounds:             1,
		Timeout:            10 * time.Second,
		EnvOverlay:         map[string]string{"TAGTEAM_TEST_ORCH_ADVISORY": "simplify"},
	})
	if err == nil {
		t.Fatal("expected blocking review error from fake supervisor")
	}
	if final.Mode != ModeSupervisor {
		t.Fatalf("mode = %q", final.Mode)
	}
	if fileExists(filepath.Join(final.RunDir, "scout-round-1.json")) {
		t.Fatal("scout should be skipped after relay simplification")
	}
	var decision OrchestrationDecision
	readJSONFile(t, filepath.Join(final.RunDir, orchestrationDecisionArtifact), &decision)
	if decision.AppliedTransition == nil || decision.AppliedTransition.From != ModeRelay || decision.AppliedTransition.To != ModeSupervisor {
		t.Fatalf("decision = %#v", decision)
	}
	if !decision.TransitionLimitConsumed {
		t.Fatal("expected transition limit consumed")
	}
}

func TestRunLoop_SupervisorEscalatesToRelayWhenBothAgentsAgree(t *testing.T) {
	t.Setenv("TAGTEAM_TEST_ORCH_ADVISORY", "escalate")
	installFakeBinaries(t, map[string]string{
		"agy":    fakeAgyScript,
		"claude": fakeClaudeScript,
	})

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "hello\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	app := NewApp(DefaultConfig())
	final, err := app.Run(context.Background(), RunOptions{
		Prompt:             "map this unfamiliar repo and fix the bug",
		Workdir:            repo,
		Mode:               ModeSupervisor,
		Scout:              RoleTarget{Adapter: "agy", Model: "gemini-3.5-flash-low"},
		Coder:              RoleTarget{Adapter: "claude"},
		Adversary:          RoleTarget{Adapter: "claude"},
		ScoutMode:          "recon",
		PostScoutMode:      "polish",
		ScoutRetrieval:     false,
		ScoutFailurePolicy: "continue",
		Rounds:             1,
		Timeout:            10 * time.Second,
		EnvOverlay:         map[string]string{"TAGTEAM_TEST_ORCH_ADVISORY": "escalate"},
	})
	if err == nil {
		t.Fatal("expected blocking review error from fake supervisor")
	}
	if final.Mode != ModeRelay {
		t.Fatalf("mode = %q", final.Mode)
	}
	if !fileExists(filepath.Join(final.RunDir, "scout-round-1.json")) {
		t.Fatal("expected scout to run after escalation")
	}
	var decision OrchestrationDecision
	readJSONFile(t, filepath.Join(final.RunDir, orchestrationDecisionArtifact), &decision)
	if decision.AppliedTransition == nil || decision.AppliedTransition.From != ModeSupervisor || decision.AppliedTransition.To != ModeRelay {
		t.Fatalf("decision = %#v", decision)
	}
	if len(decision.Advisories) != 2 {
		t.Fatalf("advisories = %#v", decision.Advisories)
	}
}

func TestRunLoop_AdvisoryFailureFallsBackToOriginalMode(t *testing.T) {
	t.Setenv("TAGTEAM_TEST_ORCH_ADVISORY", "invalid")
	installFakeBinaries(t, map[string]string{
		"agy":    fakeAgyScript,
		"claude": fakeClaudeScript,
	})

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "hello\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	app := NewApp(DefaultConfig())
	final, err := app.Run(context.Background(), RunOptions{
		Prompt:             "add a feature",
		Workdir:            repo,
		Mode:               ModeRelay,
		Scout:              RoleTarget{Adapter: "agy", Model: "gemini-3.5-flash-low"},
		Coder:              RoleTarget{Adapter: "claude"},
		Adversary:          RoleTarget{Adapter: "claude"},
		ScoutMode:          "recon",
		PostScoutMode:      "polish",
		ScoutRetrieval:     false,
		ScoutFailurePolicy: "continue",
		Rounds:             1,
		Timeout:            10 * time.Second,
		EnvOverlay:         map[string]string{"TAGTEAM_TEST_ORCH_ADVISORY": "invalid"},
	})
	if err == nil {
		t.Fatal("expected blocking review error from fake supervisor")
	}
	if final.Mode != ModeRelay {
		t.Fatalf("mode = %q", final.Mode)
	}
	if !fileExists(filepath.Join(final.RunDir, "scout-round-1.json")) {
		t.Fatal("expected original relay mode to continue after advisory failure")
	}
	var decision OrchestrationDecision
	readJSONFile(t, filepath.Join(final.RunDir, orchestrationDecisionArtifact), &decision)
	if !decision.Degraded || decision.FinalMode != ModeRelay {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestRunLoop_RelayModeDisabledScoutRetrievalSkipsArtifact(t *testing.T) {
	installFakeBinaries(t, map[string]string{
		"agy":    fakeAgyScript,
		"claude": fakeClaudeScript,
	})

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	app := NewApp(DefaultConfig())
	final, err := app.Run(context.Background(), RunOptions{
		Prompt:             "add a feature",
		Workdir:            repo,
		Mode:               ModeRelay,
		Scout:              RoleTarget{Adapter: "agy", Model: "gemini-3.5-flash-low"},
		Coder:              RoleTarget{Adapter: "claude"},
		Adversary:          RoleTarget{Adapter: "claude"},
		ScoutMode:          "recon",
		PostScoutMode:      "polish",
		ScoutRetrieval:     false,
		ScoutFailurePolicy: "fail",
		Rounds:             1,
		Timeout:            10 * time.Second,
	})
	if err == nil {
		t.Fatal("expected blocking-findings error from fake supervisor review")
	}
	if fileExists(filepath.Join(final.RunDir, "retrieval-round-1.json")) {
		t.Fatal("did not expect retrieval artifact when retrieval is disabled")
	}
	if !fileExists(filepath.Join(final.RunDir, "scout-context-round-1.json")) {
		t.Fatal("expected scout context artifact")
	}
	if !fileExists(filepath.Join(final.RunDir, "scout-execution-round-1.json")) {
		t.Fatal("expected scout execution artifact")
	}
	if !fileExists(filepath.Join(final.RunDir, "scout-round-1.json")) {
		t.Fatal("expected normal scout artifact")
	}
}

func TestRunLoop_RelayModeRetrievalUnavailableStillRunsScout(t *testing.T) {
	installFakeBinaries(t, map[string]string{
		"agy":    fakeAgyScript,
		"claude": fakeClaudeScript,
	})
	oldLookPath := execLookPath
	execLookPath = func(file string) (string, error) {
		if file == "rg" {
			return "", exec.ErrNotFound
		}
		return oldLookPath(file)
	}
	t.Cleanup(func() { execLookPath = oldLookPath })

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "hello\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	app := NewApp(DefaultConfig())
	final, err := app.Run(context.Background(), RunOptions{
		Prompt:             "add a feature",
		Workdir:            repo,
		Mode:               ModeRelay,
		Scout:              RoleTarget{Adapter: "agy", Model: "gemini-3.5-flash-low"},
		Coder:              RoleTarget{Adapter: "claude"},
		Adversary:          RoleTarget{Adapter: "claude"},
		ScoutMode:          "recon",
		PostScoutMode:      "polish",
		ScoutRetrieval:     true,
		ScoutFailurePolicy: "continue",
		Rounds:             1,
		Timeout:            10 * time.Second,
	})
	if err == nil {
		t.Fatal("expected blocking-findings error from fake supervisor review")
	}
	var retrieval RetrievalArtifact
	readJSONFile(t, filepath.Join(final.RunDir, "retrieval-round-1.json"), &retrieval)
	if retrieval.Status != "unavailable" {
		t.Fatalf("retrieval = %#v", retrieval)
	}
	var status ScoutExecutionArtifact
	readJSONFile(t, filepath.Join(final.RunDir, "scout-execution-round-1.json"), &status)
	if !status.RetrievalDegraded || !status.ScoutRan || !status.ScoutSucceeded {
		t.Fatalf("scout status = %#v", status)
	}
}

func TestRunLoop_RelayModeScoutInvocationFailureContinue(t *testing.T) {
	installFakeBinaries(t, map[string]string{
		"agy":    fakeAgyFailScript,
		"claude": fakeClaudeScript,
	})

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "hello\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	app := NewApp(DefaultConfig())
	final, err := app.Run(context.Background(), RunOptions{
		Prompt:             "add a feature",
		Workdir:            repo,
		Mode:               ModeRelay,
		Scout:              RoleTarget{Adapter: "agy"},
		Coder:              RoleTarget{Adapter: "claude"},
		Adversary:          RoleTarget{Adapter: "claude"},
		ScoutMode:          "recon",
		PostScoutMode:      "polish",
		ScoutRetrieval:     false,
		ScoutFailurePolicy: "continue",
		Rounds:             1,
		Timeout:            10 * time.Second,
	})
	if err == nil {
		t.Fatal("expected blocking-findings error after continuing without scout")
	}
	if !fileExists(filepath.Join(final.RunDir, "coder-round-1.md")) {
		t.Fatal("expected coder to run after scout failure")
	}
	if !fileExists(filepath.Join(final.RunDir, "supervisor-review-round-1.json")) {
		t.Fatal("expected supervisor review to run after scout failure")
	}
	if !final.Degraded || final.DegradedReason == "" {
		t.Fatalf("expected final degradation metadata, final=%#v", final)
	}
	if len(final.RoleLosses) == 0 {
		t.Fatalf("expected role loss metadata, final=%#v", final)
	}
	var status ScoutExecutionArtifact
	readJSONFile(t, filepath.Join(final.RunDir, "scout-execution-round-1.json"), &status)
	if !status.ScoutRan || status.ScoutSucceeded || status.FailureClass != scoutFailureClassInvocation || !status.ContinuedWithoutScoutContext {
		t.Fatalf("scout status = %#v", status)
	}
	var postStatus ScoutExecutionArtifact
	readJSONFile(t, filepath.Join(final.RunDir, "post-scout-execution-round-1.json"), &postStatus)
	if !postStatus.ScoutRan || postStatus.ScoutSucceeded || postStatus.FailureClass != scoutFailureClassInvocation || !postStatus.ContinuedWithoutScoutContext {
		t.Fatalf("post-scout status = %#v", postStatus)
	}
}

func TestRunLoop_RelayModeScoutInvocationFailureFail(t *testing.T) {
	installFakeBinaries(t, map[string]string{
		"agy":    fakeAgyFailScript,
		"claude": fakeClaudeScript,
	})

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "hello\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	app := NewApp(DefaultConfig())
	final, err := app.Run(context.Background(), RunOptions{
		Prompt:             "add a feature",
		Workdir:            repo,
		Mode:               ModeRelay,
		Scout:              RoleTarget{Adapter: "agy"},
		Coder:              RoleTarget{Adapter: "claude"},
		Adversary:          RoleTarget{Adapter: "claude"},
		ScoutMode:          "recon",
		PostScoutMode:      "polish",
		ScoutRetrieval:     false,
		ScoutFailurePolicy: "fail",
		Rounds:             1,
		Timeout:            10 * time.Second,
	})
	if err == nil {
		t.Fatal("expected strict scout failure")
	}
	if !strings.Contains(err.Error(), "scout_failure_policy=fail") {
		t.Fatalf("error = %v", err)
	}
	if fileExists(filepath.Join(final.RunDir, "coder-round-1.md")) {
		t.Fatal("coder should not run after strict scout failure")
	}
	var status ScoutExecutionArtifact
	readJSONFile(t, filepath.Join(final.RunDir, "scout-execution-round-1.json"), &status)
	if !status.ScoutRan || status.ScoutSucceeded || status.FailureClass != scoutFailureClassInvocation || status.ContinuedWithoutScoutContext {
		t.Fatalf("scout status = %#v", status)
	}
}

func TestRunLoop_RelayModeScoutOutputFailureContinue(t *testing.T) {
	installFakeBinaries(t, map[string]string{
		"agy":    fakeAgyInvalidScript,
		"claude": fakeClaudeScript,
	})

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "hello\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	app := NewApp(DefaultConfig())
	final, err := app.Run(context.Background(), RunOptions{
		Prompt:             "add a feature",
		Workdir:            repo,
		Mode:               ModeRelay,
		Scout:              RoleTarget{Adapter: "agy"},
		Coder:              RoleTarget{Adapter: "claude"},
		Adversary:          RoleTarget{Adapter: "claude"},
		ScoutMode:          "recon",
		PostScoutMode:      "polish",
		ScoutRetrieval:     false,
		ScoutFailurePolicy: "continue",
		Rounds:             1,
		Timeout:            10 * time.Second,
	})
	if err == nil {
		t.Fatal("expected blocking-findings error after continuing without scout")
	}
	var status ScoutExecutionArtifact
	readJSONFile(t, filepath.Join(final.RunDir, "scout-execution-round-1.json"), &status)
	if status.FailureClass != scoutFailureClassOutput || !status.ContinuedWithoutScoutContext {
		t.Fatalf("scout status = %#v", status)
	}
}

func TestRunLoop_RelayModeScoutContextUnknownKeepsRetrieval(t *testing.T) {
	installFakeBinaries(t, map[string]string{
		"agy":    fakeAgyScript,
		"claude": fakeClaudeScript,
	})
	agyLogPath := filepath.Join(t.TempDir(), "agy.log")

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "hello codex\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	app := NewApp(DefaultConfig())
	final, err := app.Run(context.Background(), RunOptions{
		Prompt:         "add codex feature",
		Workdir:        repo,
		Mode:           ModeRelay,
		Scout:          RoleTarget{Adapter: "agy", Model: "gemini-3.5-flash-low"},
		Coder:          RoleTarget{Adapter: "claude"},
		Adversary:      RoleTarget{Adapter: "claude"},
		ScoutMode:      "recon",
		PostScoutMode:  "polish",
		ScoutRetrieval: true,
		Rounds:         1,
		Timeout:        10 * time.Second,
		EnvOverlay:     map[string]string{"AGY_ARGS_LOG": agyLogPath},
	})
	if err == nil {
		t.Fatal("expected blocking-findings error from fake supervisor review")
	}
	var budget ScoutContextBudgetArtifact
	readJSONFile(t, filepath.Join(final.RunDir, "scout-context-round-1.json"), &budget)
	if budget.Status != scoutContextStatusUnknown || !budget.NoConfiguredLimit {
		t.Fatalf("budget = %#v", budget)
	}
	agyLog, err := os.ReadFile(agyLogPath)
	if err != nil {
		t.Fatalf("read agy log: %v", err)
	}
	if !strings.Contains(string(agyLog), "Host retrieval evidence") {
		t.Fatalf("retrieval should be unchanged for unknown budget:\n%s", string(agyLog))
	}
}

func TestRunLoop_RelayModeScoutContextNearLimitCompactsRetrieval(t *testing.T) {
	installFakeBinaries(t, map[string]string{
		"agy":    fakeAgyScript,
		"claude": fakeClaudeScript,
	})
	agyLogPath := filepath.Join(t.TempDir(), "agy.log")

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	for i := 0; i < 40; i++ {
		mustWriteFile(t, filepath.Join(repo, "src", fmt.Sprintf("file-%02d.go", i)), "package src\n// codex model registry evidence\n")
	}
	runGit(t, repo, "add", "src")
	runGit(t, repo, "commit", "-m", "init")

	// This test needs real host retrieval to inflate the scout prompt toward the
	// configured context limit; without rg, retrieval is unavailable and the
	// near-limit compaction branch never fires.
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg not installed")
	}

	cfg := DefaultConfig()
	cfg.Adapters.Agy.MaxContextTokens = testIntPtr(2100)
	cfg.Adapters.Agy.ReservedOutputTokens = testIntPtr(0)
	app := NewApp(cfg)
	final, err := app.Run(context.Background(), RunOptions{
		Prompt:         "codex model registry",
		Workdir:        repo,
		Mode:           ModeRelay,
		Scout:          RoleTarget{Adapter: "agy", Model: "gemini-3.5-flash-low"},
		Coder:          RoleTarget{Adapter: "claude"},
		Adversary:      RoleTarget{Adapter: "claude"},
		ScoutMode:      "recon",
		PostScoutMode:  "polish",
		ScoutRetrieval: true,
		Rounds:         1,
		Timeout:        10 * time.Second,
		EnvOverlay:     map[string]string{"AGY_ARGS_LOG": agyLogPath},
	})
	if err == nil {
		t.Fatal("expected blocking-findings error from fake supervisor review")
	}
	var budget ScoutContextBudgetArtifact
	readJSONFile(t, filepath.Join(final.RunDir, "scout-context-round-1.json"), &budget)
	if !budget.RetrievalCompacted {
		t.Fatalf("expected retrieval compaction, budget = %#v", budget)
	}
	if budget.Status == scoutContextStatusExceeds {
		t.Fatalf("expected compacted prompt to continue, budget = %#v", budget)
	}
}

func TestRunLoop_RelayModeScoutContextExceedsDisablesRetrieval(t *testing.T) {
	installFakeBinaries(t, map[string]string{
		"agy":    fakeAgyScript,
		"claude": fakeClaudeScript,
	})
	agyLogPath := filepath.Join(t.TempDir(), "agy.log")

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	for i := 0; i < 80; i++ {
		mustWriteFile(t, filepath.Join(repo, "src", fmt.Sprintf("file-%02d.go", i)), "package src\n// codex model registry evidence\n")
	}
	runGit(t, repo, "add", "src")
	runGit(t, repo, "commit", "-m", "init")

	// This test needs real host retrieval to push the scout prompt over the
	// configured context limit; without rg, retrieval is unavailable and the
	// retrieval-disable branch never fires.
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg not installed")
	}

	cfg := DefaultConfig()
	cfg.Adapters.Agy.MaxContextTokens = testIntPtr(1300)
	cfg.Adapters.Agy.ReservedOutputTokens = testIntPtr(0)
	app := NewApp(cfg)
	final, err := app.Run(context.Background(), RunOptions{
		Prompt:         "codex model registry",
		Workdir:        repo,
		Mode:           ModeRelay,
		Scout:          RoleTarget{Adapter: "agy", Model: "gemini-3.5-flash-low"},
		Coder:          RoleTarget{Adapter: "claude"},
		Adversary:      RoleTarget{Adapter: "claude"},
		ScoutMode:      "recon",
		PostScoutMode:  "polish",
		ScoutRetrieval: true,
		Rounds:         1,
		Timeout:        10 * time.Second,
		EnvOverlay:     map[string]string{"AGY_ARGS_LOG": agyLogPath},
	})
	if err == nil {
		t.Fatal("expected blocking-findings error from fake supervisor review")
	}
	var budget ScoutContextBudgetArtifact
	readJSONFile(t, filepath.Join(final.RunDir, "scout-context-round-1.json"), &budget)
	if !budget.RetrievalDisabledDueBudget {
		t.Fatalf("expected retrieval disabled due budget, budget = %#v", budget)
	}
	agyLog, err := os.ReadFile(agyLogPath)
	if err != nil {
		t.Fatalf("read agy log: %v", err)
	}
	preScoutInvocation := strings.Split(string(agyLog), "\n---\n")[0]
	if !strings.Contains(preScoutInvocation, "Host retrieval evidence:\n(not provided for this scout phase)") {
		t.Fatalf("retrieval context should have been disabled:\n%s", string(agyLog))
	}
}

func TestRunLoop_RelayModeScoutContextExceedsWithoutRetrievalFailsEarly(t *testing.T) {
	installFakeBinaries(t, map[string]string{
		"agy":    fakeAgyScript,
		"claude": fakeClaudeScript,
	})

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "hello\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	cfg := DefaultConfig()
	cfg.Adapters.Agy.MaxContextTokens = testIntPtr(100)
	cfg.Adapters.Agy.ReservedOutputTokens = testIntPtr(0)
	app := NewApp(cfg)
	final, err := app.Run(context.Background(), RunOptions{
		Prompt:             strings.Repeat("large prompt ", 200),
		Workdir:            repo,
		Mode:               ModeRelay,
		Scout:              RoleTarget{Adapter: "agy", Model: "gemini-3.5-flash-low"},
		Coder:              RoleTarget{Adapter: "claude"},
		Adversary:          RoleTarget{Adapter: "claude"},
		ScoutMode:          "recon",
		PostScoutMode:      "polish",
		ScoutRetrieval:     false,
		ScoutFailurePolicy: "fail",
		Rounds:             1,
		Timeout:            10 * time.Second,
	})
	if err == nil {
		t.Fatal("expected context budget error")
	}
	if ExitCode(err) != ExitPreflightFailed {
		t.Fatalf("exit code = %d err=%v", ExitCode(err), err)
	}
	if !strings.Contains(err.Error(), "scout context exceeds configured limit") {
		t.Fatalf("error = %v", err)
	}
	var budget ScoutContextBudgetArtifact
	readJSONFile(t, filepath.Join(final.RunDir, "scout-context-round-1.json"), &budget)
	if budget.Status != scoutContextStatusExceeds {
		t.Fatalf("budget = %#v", budget)
	}
	if fileExists(filepath.Join(final.RunDir, "scout-round-1.json")) {
		t.Fatal("scout should not have run after context failure")
	}
}

func TestFix_RestoresAdversarialModeAndTargetsOverSupervisorDefault(t *testing.T) {
	installFakeClaudeBinary(t)

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	app := NewApp(DefaultConfig())

	// Simulate `tagteam --mode adversarial "..."`: an explicit adversarial
	// run that exhausts its single round with a blocking review.
	runOpts := RunOptions{
		Prompt:    "add a feature",
		Workdir:   repo,
		Mode:      ModeAdversarial,
		Coder:     RoleTarget{Adapter: "claude"},
		Adversary: RoleTarget{Adapter: "claude"},
		Rounds:    1,
		Timeout:   10 * time.Second,
	}
	final, err := app.Run(context.Background(), runOpts)
	if err == nil {
		t.Fatal("expected blocking-findings error from the initial run")
	}
	if final.Mode != ModeAdversarial {
		t.Fatalf("persisted mode = %q", final.Mode)
	}
	if !final.Review.HasBlockingFindings() {
		t.Fatalf("expected the seeded review to have blocking findings: %#v", final.Review)
	}
	if !final.RoundLimitReached {
		t.Fatal("expected the initial run to record round limit exhaustion")
	}
	if len(final.RoundLimitReports) != 2 {
		t.Fatalf("expected final reports from both agents, got %#v", final.RoundLimitReports)
	}
	for _, report := range final.RoundLimitReports {
		if report.Path == "" || !fileExists(report.Path) {
			t.Fatalf("expected report artifact for %s at %q", report.Role, report.Path)
		}
		if strings.TrimSpace(report.Text) == "" {
			t.Fatalf("expected report text for %s", report.Role)
		}
	}

	// Simulate bare `tagteam fix`: no --mode/--worker/--supervisor flags,
	// so ResolveOptions would hand Fix the current (supervisor) defaults.
	fixOpts := RunOptions{
		Workdir:   repo,
		Mode:      ModeSupervisor,
		Coder:     RoleTarget{Adapter: "agy"},
		Adversary: RoleTarget{Adapter: "claude", Model: "opus"},
		Rounds:    1,
		Timeout:   10 * time.Second,
		// ModeExplicit/CoderExplicit/AdversaryExplicit left false: this is
		// exactly what ResolveOptions produces when the user didn't pass
		// --mode/--worker/--supervisor/-mc/-ma on the fix invocation.
	}
	fixed, err := app.Fix(context.Background(), fixOpts)
	if err == nil {
		t.Fatal("expected blocking-findings error from the resumed run")
	}
	if fixed.Mode != ModeAdversarial {
		t.Fatalf("fix should have resumed the saved adversarial mode, got %q", fixed.Mode)
	}
	if fixed.Coder.Adapter != "claude" || fixed.Adversary.Adapter != "claude" {
		t.Fatalf("fix should have resumed the saved coder/adversary targets: %#v / %#v", fixed.Coder, fixed.Adversary)
	}
	if fixed.Adapters["coder"] != "claude" || fixed.Adapters["adversary"] != "claude" {
		t.Fatalf("fix should report coder/adversary labels, got: %#v", fixed.Adapters)
	}
	if _, ok := fixed.Adapters["worker"]; ok {
		t.Fatalf("fix should not have switched to supervisor-mode labels: %#v", fixed.Adapters)
	}
	if !strings.Contains(fixed.LatestReviewPath, "adversary-round-1.json") {
		t.Fatalf("expected an adversary-labeled review artifact, got %q", fixed.LatestReviewPath)
	}
	if !fileExists(filepath.Join(fixed.RunDir, "coder-round-1.md")) {
		t.Fatalf("expected a coder-labeled transcript in %s", fixed.RunDir)
	}
}

func TestFix_ExplicitModeOverridesSavedRun(t *testing.T) {
	installFakeClaudeBinary(t)

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	app := NewApp(DefaultConfig())
	runOpts := RunOptions{
		Prompt:    "add a feature",
		Workdir:   repo,
		Mode:      ModeAdversarial,
		Coder:     RoleTarget{Adapter: "claude"},
		Adversary: RoleTarget{Adapter: "claude"},
		Rounds:    1,
		Timeout:   10 * time.Second,
	}
	if _, err := app.Run(context.Background(), runOpts); err == nil {
		t.Fatal("expected blocking-findings error from the initial run")
	}

	// Simulate `tagteam fix --mode adversarial -mc claude -ma claude`: the
	// caller explicitly pins mode/targets for this invocation, so Fix must
	// not override them even though they happen to match the saved run.
	fixOpts := RunOptions{
		Workdir:           repo,
		Mode:              ModeAdversarial,
		ModeExplicit:      true,
		Coder:             RoleTarget{Adapter: "claude"},
		Adversary:         RoleTarget{Adapter: "claude"},
		CoderExplicit:     true,
		AdversaryExplicit: true,
		Rounds:            1,
		Timeout:           10 * time.Second,
	}
	fixed, err := app.Fix(context.Background(), fixOpts)
	if err == nil {
		t.Fatal("expected blocking-findings error from the resumed run")
	}
	if fixed.Mode != ModeAdversarial {
		t.Fatalf("mode = %q", fixed.Mode)
	}
}

func TestFix_ProfileSelectedModeNotOverwrittenBySavedRun(t *testing.T) {
	installFakeClaudeBinary(t)

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	// Seed a saved run in supervisor mode (the current default), so a naive
	// resume would try to reapply supervisor mode/targets on the fix below.
	app := NewApp(DefaultConfig())
	seedOpts := RunOptions{
		Prompt:    "add a feature",
		Workdir:   repo,
		Mode:      ModeSupervisor,
		Coder:     RoleTarget{Adapter: "claude"},
		Adversary: RoleTarget{Adapter: "claude"},
		Rounds:    1,
		Timeout:   10 * time.Second,
	}
	if _, err := app.Run(context.Background(), seedOpts); err == nil {
		t.Fatal("expected blocking-findings error from the seed run")
	}

	// Resolve `tagteam fix --profile legacy` against a config whose
	// "legacy" profile selects adversarial mode with its own targets. This
	// mirrors ResolveOptions's real output: profile selection marks
	// mode/targets explicit even though the user passed no --mode/-mc/-ma
	// flags directly.
	cfg := DefaultConfig()
	cfg.Profiles["legacy"] = ProfileConfig{
		Mode:      "adversarial",
		Coder:     "claude:sonnet",
		Adversary: "claude:haiku",
	}
	fixOpts, err := ResolveOptions(cfg, nil, FlagInputs{
		Profile: "legacy",
		Workdir: repo,
		Timeout: 10 * time.Second,
	}, map[string]bool{}, "")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if !fixOpts.ModeExplicit || !fixOpts.CoderExplicit || !fixOpts.AdversaryExplicit {
		t.Fatalf("expected profile selection to mark mode/targets explicit: %#v", fixOpts)
	}

	fixed, err := app.Fix(context.Background(), fixOpts)
	if err == nil {
		t.Fatal("expected blocking-findings error from the resumed run")
	}
	if fixed.Mode != ModeAdversarial {
		t.Fatalf("fix should have kept the profile-resolved adversarial mode, got %q", fixed.Mode)
	}
	if fixed.Coder.Adapter != "claude" || fixed.Coder.Model != "sonnet" {
		t.Fatalf("fix should have kept the profile-resolved coder target, got %#v", fixed.Coder)
	}
	if fixed.Adversary.Adapter != "claude" || fixed.Adversary.Model != "haiku" {
		t.Fatalf("fix should have kept the profile-resolved adversary target, got %#v", fixed.Adversary)
	}
}

func TestFix_ProfileWithOnlyRoundsResumesSavedRunModeAndTargets(t *testing.T) {
	installFakeClaudeBinary(t)

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	// Seed a saved adversarial-mode run, distinct from the current
	// supervisor default, so a naive resume that ignores the saved run
	// would flip mode/targets away from it.
	app := NewApp(DefaultConfig())
	seedOpts := RunOptions{
		Prompt:    "add a feature",
		Workdir:   repo,
		Mode:      ModeAdversarial,
		Coder:     RoleTarget{Adapter: "claude"},
		Adversary: RoleTarget{Adapter: "claude"},
		Rounds:    1,
		Timeout:   10 * time.Second,
	}
	if _, err := app.Run(context.Background(), seedOpts); err == nil {
		t.Fatal("expected blocking-findings error from the seed run")
	}

	// Resolve `tagteam fix --profile quick`, where "quick" only overrides
	// rounds. A profile that doesn't touch mode/coder/adversary/worker/
	// supervisor must not block Fix() from resuming the saved run's mode
	// and targets.
	cfg := DefaultConfig()
	cfg.Profiles["quick"] = ProfileConfig{Rounds: 3}
	fixOpts, err := ResolveOptions(cfg, nil, FlagInputs{
		Profile: "quick",
		Workdir: repo,
		Timeout: 10 * time.Second,
	}, map[string]bool{}, "")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if fixOpts.ModeExplicit || fixOpts.CoderExplicit || fixOpts.AdversaryExplicit {
		t.Fatalf("expected a rounds-only profile to leave mode/targets non-explicit: %#v", fixOpts)
	}
	if fixOpts.Mode != ModeSupervisor {
		t.Fatalf("expected the rounds-only profile to resolve to the current supervisor default before Fix() resumes, got %q", fixOpts.Mode)
	}

	fixed, err := app.Fix(context.Background(), fixOpts)
	if err == nil {
		t.Fatal("expected blocking-findings error from the resumed run")
	}
	if fixed.Mode != ModeAdversarial {
		t.Fatalf("fix should have resumed the saved adversarial mode, got %q", fixed.Mode)
	}
	if fixed.Coder.Adapter != "claude" || fixed.Adversary.Adapter != "claude" {
		t.Fatalf("fix should have resumed the saved coder/adversary targets: %#v / %#v", fixed.Coder, fixed.Adversary)
	}
	if fixed.RoundsRequested != 3 {
		t.Fatalf("fix should still apply the profile's rounds override, got %d", fixed.RoundsRequested)
	}
}

func TestReview_PersistsEditorTargetForFixResume(t *testing.T) {
	installFakeClaudeBinary(t)

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	app := NewApp(DefaultConfig())

	// Simulate `tagteam review --mode adversarial -mc claude:sonnet -ma
	// claude:haiku`: a review-only run with a non-default editor target.
	// The editor is never invoked by Review(), so it doesn't need a
	// distinct fake binary.
	reviewOpts := RunOptions{
		Workdir:       repo,
		Mode:          ModeAdversarial,
		Coder:         RoleTarget{Adapter: "claude", Model: "sonnet"},
		CoderExplicit: true,
		Adversary:     RoleTarget{Adapter: "claude", Model: "haiku"},
		Timeout:       10 * time.Second,
	}
	reviewed, err := app.Review(context.Background(), reviewOpts, "review this diff")
	if err != nil {
		t.Fatalf("Review() error = %v", err)
	}
	if reviewed.Coder.Adapter != "claude" || reviewed.Coder.Model != "sonnet" {
		t.Fatalf("review should have persisted the editor target, got %#v", reviewed.Coder)
	}
	if reviewed.Review == nil || !reviewed.Review.HasBlockingFindings() {
		t.Fatalf("expected the seeded review to have blocking findings: %#v", reviewed.Review)
	}

	// Simulate bare `tagteam fix`: no --mode/--worker/--supervisor/-mc/-ma
	// flags, so ResolveOptions hands Fix the current (supervisor) defaults.
	fixOpts := RunOptions{
		Workdir:   repo,
		Mode:      ModeSupervisor,
		Coder:     RoleTarget{Adapter: "agy"},
		Adversary: RoleTarget{Adapter: "claude", Model: "opus"},
		Rounds:    1,
		Timeout:   10 * time.Second,
	}
	fixed, err := app.Fix(context.Background(), fixOpts)
	if err == nil {
		t.Fatal("expected blocking-findings error from the resumed run")
	}
	if fixed.Mode != ModeAdversarial {
		t.Fatalf("fix should have resumed the review-only run's adversarial mode, got %q", fixed.Mode)
	}
	if fixed.Coder.Adapter != "claude" || fixed.Coder.Model != "sonnet" {
		t.Fatalf("fix should have resumed the review-only run's editor target, got %#v", fixed.Coder)
	}
	if fixed.Adversary.Adapter != "claude" || fixed.Adversary.Model != "haiku" {
		t.Fatalf("fix should have resumed the review-only run's reviewer target, got %#v", fixed.Adversary)
	}
}

func TestReviewRejectsUnknownEditorTargetBeforePersisting(t *testing.T) {
	installFakeClaudeBinary(t)

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	app := NewApp(DefaultConfig())
	opts := RunOptions{
		Workdir:       repo,
		Mode:          ModeSupervisor,
		Coder:         RoleTarget{Adapter: "typo-worker"},
		CoderExplicit: true,
		Adversary:     RoleTarget{Adapter: "claude"},
		Timeout:       10 * time.Second,
	}
	_, err := app.Review(context.Background(), opts, "review this diff")
	if err == nil {
		t.Fatal("expected error")
	}
	exitErr, ok := err.(*ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %T", err)
	}
	if exitErr.Code != ExitInvalidArguments {
		t.Fatalf("exit code = %d, want %d", exitErr.Code, ExitInvalidArguments)
	}
	if !strings.Contains(err.Error(), "unknown worker adapter") {
		t.Fatalf("error = %q", err.Error())
	}
	if fileExists(filepath.Join(repo, ".tagteam", "latest.json")) {
		t.Fatal("review should fail before persisting latest.json")
	}
}

func TestReviewIgnoresUnknownDefaultEditorTarget(t *testing.T) {
	installFakeClaudeBinary(t)

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	app := NewApp(DefaultConfig())
	opts := RunOptions{
		Workdir:   repo,
		Mode:      ModeSupervisor,
		Coder:     RoleTarget{Adapter: "stale-default-worker"},
		Adversary: RoleTarget{Adapter: "claude"},
		Timeout:   10 * time.Second,
	}
	reviewed, err := app.Review(context.Background(), opts, "review this diff")
	if err != nil {
		t.Fatalf("Review() error = %v", err)
	}
	if reviewed.Coder.Adapter != "" {
		t.Fatalf("review should not persist implicit/default editor target, got %#v", reviewed.Coder)
	}
}

func TestFix_ExplicitModeDoesNotRestoreSavedTargets(t *testing.T) {
	installFakeClaudeBinary(t)

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	app := NewApp(DefaultConfig())
	seedOpts := RunOptions{
		Prompt:    "add a feature",
		Workdir:   repo,
		Mode:      ModeSupervisor,
		Coder:     RoleTarget{Adapter: "claude", Model: "worker-model"},
		Adversary: RoleTarget{Adapter: "claude", Model: "supervisor-model"},
		Rounds:    1,
		Timeout:   10 * time.Second,
	}
	if _, err := app.Run(context.Background(), seedOpts); err == nil {
		t.Fatal("expected blocking-findings error from the seed run")
	}

	fixOpts := RunOptions{
		Workdir:      repo,
		Mode:         ModeAdversarial,
		ModeExplicit: true,
		Coder:        RoleTarget{Adapter: "claude", Model: "coder-default"},
		Adversary:    RoleTarget{Adapter: "claude", Model: "adversary-default"},
		Rounds:       1,
		Timeout:      10 * time.Second,
	}
	fixed, err := app.Fix(context.Background(), fixOpts)
	if err == nil {
		t.Fatal("expected blocking-findings error from the resumed run")
	}
	if fixed.Mode != ModeAdversarial {
		t.Fatalf("fix should keep explicit mode, got %q", fixed.Mode)
	}
	if fixed.Coder.Model != "coder-default" {
		t.Fatalf("fix should not restore saved worker as coder, got %#v", fixed.Coder)
	}
	if fixed.Adversary.Model != "adversary-default" {
		t.Fatalf("fix should not restore saved supervisor as adversary, got %#v", fixed.Adversary)
	}
}

func TestFix_ExplicitSameModeRestoresSavedTargets(t *testing.T) {
	installFakeClaudeBinary(t)

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	app := NewApp(DefaultConfig())
	seedOpts := RunOptions{
		Prompt:    "add a feature",
		Workdir:   repo,
		Mode:      ModeSupervisor,
		Coder:     RoleTarget{Adapter: "claude", Model: "saved-worker"},
		Adversary: RoleTarget{Adapter: "claude", Model: "saved-supervisor"},
		Rounds:    1,
		Timeout:   10 * time.Second,
	}
	if _, err := app.Run(context.Background(), seedOpts); err == nil {
		t.Fatal("expected blocking-findings error from the seed run")
	}

	fixed, err := app.Fix(context.Background(), RunOptions{
		Workdir:      repo,
		Mode:         ModeSupervisor,
		ModeExplicit: true,
		Coder:        RoleTarget{Adapter: "claude", Model: "current-worker-default"},
		Adversary:    RoleTarget{Adapter: "claude", Model: "current-supervisor-default"},
		Rounds:       1,
		Timeout:      10 * time.Second,
	})
	if err == nil {
		t.Fatal("expected blocking-findings error from the resumed run")
	}
	if fixed.Mode != ModeSupervisor {
		t.Fatalf("mode = %q", fixed.Mode)
	}
	if fixed.Coder.Model != "saved-worker" {
		t.Fatalf("fix should restore saved worker target for explicit same-mode fix, got %#v", fixed.Coder)
	}
	if fixed.Adversary.Model != "saved-supervisor" {
		t.Fatalf("fix should restore saved supervisor target for explicit same-mode fix, got %#v", fixed.Adversary)
	}
}

func TestFixRejectsModeSpecificTargetAfterSavedModeRestore(t *testing.T) {
	installFakeClaudeBinary(t)

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	app := NewApp(DefaultConfig())
	seedOpts := RunOptions{
		Prompt:    "add a feature",
		Workdir:   repo,
		Mode:      ModeAdversarial,
		Coder:     RoleTarget{Adapter: "claude"},
		Adversary: RoleTarget{Adapter: "claude"},
		Rounds:    1,
		Timeout:   10 * time.Second,
	}
	if _, err := app.Run(context.Background(), seedOpts); err == nil {
		t.Fatal("expected blocking-findings error from the seed run")
	}

	_, err := app.Fix(context.Background(), RunOptions{
		Workdir:           repo,
		Mode:              ModeSupervisor,
		Coder:             RoleTarget{Adapter: "claude", Model: "worker-from-flag"},
		CoderExplicit:     true,
		CoderExplicitMode: ModeSupervisor,
		Adversary:         RoleTarget{Adapter: "claude", Model: "opus"},
		Rounds:            1,
		Timeout:           10 * time.Second,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	exitErr, ok := err.(*ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %T", err)
	}
	if exitErr.Code != ExitInvalidArguments {
		t.Fatalf("exit code = %d, want %d", exitErr.Code, ExitInvalidArguments)
	}
	if !strings.Contains(err.Error(), "selected for supervisor mode but latest run resumes as adversarial") {
		t.Fatalf("error = %q", err.Error())
	}
}

func TestFix_LegacyFinalJSONResumesAdversarialMode(t *testing.T) {
	installFakeClaudeBinary(t)

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")
	baseline := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))

	// Hand-write a final.json/latest.json pair shaped like a run saved
	// before supervisor/worker mode existed: no mode/coder/adversary
	// fields, only the legacy Adapters/Models maps with "coder"/"adversary"
	// keys and a saved blocking review.
	runDir := filepath.Join(repo, ".tagteam", "runs", "legacy-run")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "input.md"), []byte("add a feature"), 0o644); err != nil {
		t.Fatal(err)
	}
	legacyFinal := map[string]any{
		"run_id":   "legacy-run",
		"run_dir":  runDir,
		"workdir":  repo,
		"baseline": baseline,
		"verdict":  "needs_changes",
		"summary":  "needs fixes",
		"review": map[string]any{
			"verdict": "needs_changes",
			"summary": "needs fixes",
			"findings": []map[string]any{
				{"severity": "major", "file": "main.go", "issue": "bug", "fix": "fix it"},
			},
		},
		"adapters": map[string]string{"coder": "claude", "adversary": "claude"},
		"models":   map[string]string{"coder": "", "adversary": ""},
	}
	finalPath := filepath.Join(runDir, "final.json")
	finalBytes, err := json.MarshalIndent(legacyFinal, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(finalPath, finalBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	latest := LatestRun{RunID: "legacy-run", RunDir: runDir, FinalPath: finalPath, Verdict: "needs_changes"}
	latestBytes, err := json.MarshalIndent(latest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".tagteam", "latest.json"), latestBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	app := NewApp(DefaultConfig())
	// Simulate bare `tagteam fix`: no --mode/--worker/--supervisor flags,
	// so ResolveOptions hands Fix the current (supervisor) defaults, exactly
	// as in the non-legacy resume test above.
	fixOpts := RunOptions{
		Workdir:   repo,
		Mode:      ModeSupervisor,
		Coder:     RoleTarget{Adapter: "agy"},
		Adversary: RoleTarget{Adapter: "claude", Model: "opus"},
		Rounds:    1,
		Timeout:   10 * time.Second,
	}
	fixed, err := app.Fix(context.Background(), fixOpts)
	if err == nil {
		t.Fatal("expected blocking-findings error from the resumed run")
	}
	if fixed.Mode != ModeAdversarial {
		t.Fatalf("legacy final.json should resume as adversarial mode, got %q", fixed.Mode)
	}
	if fixed.Coder.Adapter != "claude" || fixed.Adversary.Adapter != "claude" {
		t.Fatalf("legacy final.json should reconstruct coder/adversary targets from the Adapters map: %#v / %#v", fixed.Coder, fixed.Adversary)
	}
	if fixed.Adapters["coder"] != "claude" || fixed.Adapters["adversary"] != "claude" {
		t.Fatalf("fix should report coder/adversary labels, got: %#v", fixed.Adapters)
	}
}

func TestEditorSystemPromptForMode(t *testing.T) {
	soloPrompt := editorSystemPromptForMode(ModeSolo)
	if soloPrompt != soloSystemPrompt {
		t.Fatal("solo mode should select soloSystemPrompt")
	}
	if strings.Contains(soloPrompt, "two-agent adversarial workflow") || strings.Contains(soloPrompt, "supervisor-worker") {
		t.Fatalf("solo prompt should not use multi-agent workflow wording: %q", soloPrompt)
	}

	supervisorPrompt := editorSystemPromptForMode(ModeSupervisor)
	if supervisorPrompt != workerSystemPrompt {
		t.Fatal("supervisor mode should select workerSystemPrompt")
	}
	if strings.Contains(supervisorPrompt, "two-agent adversarial workflow") {
		t.Fatalf("worker system prompt should not use adversarial coder wording: %q", supervisorPrompt)
	}
	if !strings.Contains(supervisorPrompt, "supervisor-worker") {
		t.Fatalf("worker system prompt should describe the supervisor-worker workflow: %q", supervisorPrompt)
	}

	adversarialPrompt := editorSystemPromptForMode(ModeAdversarial)
	if adversarialPrompt != coderSystemPrompt {
		t.Fatal("adversarial mode should select coderSystemPrompt")
	}
}

func TestRunLoop_SupervisorModeSendsWorkerSystemPrompt(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "claude-args.log")
	t.Setenv("CLAUDE_ARGS_LOG", logPath)
	installFakeClaudeBinary(t)

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	app := NewApp(DefaultConfig())
	opts := RunOptions{
		Prompt:    "add a feature",
		Workdir:   repo,
		Mode:      ModeSupervisor,
		Coder:     RoleTarget{Adapter: "claude"},
		Adversary: RoleTarget{Adapter: "claude"},
		Rounds:    1,
		Timeout:   10 * time.Second,
	}
	// The fake claude script always returns a blocking "needs_changes"
	// review, so Run() is expected to report an error; only the captured
	// invocation arguments matter for this test.
	_, _ = app.Run(context.Background(), opts)

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read claude args log: %v", err)
	}
	log := string(data)
	if !strings.Contains(log, "supervisor-worker coding workflow") {
		t.Fatalf("expected the worker invocation to carry the supervisor-worker system prompt, log:\n%s", log)
	}
	if strings.Contains(log, "two-agent adversarial workflow") {
		t.Fatalf("worker invocation should not carry the adversarial coder system prompt, log:\n%s", log)
	}
}

func TestDoctorRejectsGoslingAdversaryAfterStatusProbe(t *testing.T) {
	oldLookPath := execLookPath
	oldCommandContext := execCommandContext
	defer func() {
		execLookPath = oldLookPath
		execCommandContext = oldCommandContext
	}()

	execLookPath = func(file string) (string, error) {
		return filepath.Join("/mock/bin", file), nil
	}
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "printf", "1.0")
	}

	app := NewApp(DefaultConfig())
	status, err := app.Doctor(context.Background(), RunOptions{
		Coder:     RoleTarget{Adapter: "codex"},
		Adversary: RoleTarget{Adapter: "gosling"},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	exitErr, ok := err.(*ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %T", err)
	}
	if exitErr.Code != ExitInvalidArguments {
		t.Fatalf("exit code = %d, want %d", exitErr.Code, ExitInvalidArguments)
	}
	if !strings.Contains(err.Error(), "gosling is not supported as an adversary adapter") {
		t.Fatalf("error = %q", err.Error())
	}
	if len(status) == 0 || !status["gosling"].Found {
		t.Fatalf("expected populated status, got %#v", status)
	}
}

func readJSONFile(t *testing.T, path string, out any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}
