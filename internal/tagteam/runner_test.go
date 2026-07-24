package tagteam

import (
	"context"
	"encoding/json"
	"errors"
	"os"
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

func TestStructuredSchemasUseClaudeCompatibleDraft(t *testing.T) {
	for name, raw := range map[string]string{
		"review":                 ReviewSchema,
		"work plan":              WorkPlanSchema,
		"orchestration advisory": OrchestrationAdvisorySchema,
	} {
		var schema map[string]any
		if err := json.Unmarshal([]byte(raw), &schema); err != nil {
			t.Fatalf("decode %s schema: %v", name, err)
		}
		if got := schema["$schema"]; got != "http://json-schema.org/draft-07/schema#" {
			t.Fatalf("%s schema draft = %v", name, got)
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
	for _, key := range []string{"schema_version", "summary", "packages", "selected_package", "defer"} {
		if !required[key] {
			t.Fatalf("work plan schema missing required key %q", key)
		}
	}
	packageRequired := map[string]bool{}
	for _, key := range schema.Properties.Packages.Items.Required {
		packageRequired[key] = true
	}
	for _, key := range []string{"id", "title", "goal", "estimated_seconds", "allowed_scope", "acceptance", "validation"} {
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

func TestRelaySimplificationConstraintPreservesCallerOwnedScoutTopology(t *testing.T) {
	tests := []struct {
		name string
		opts RunOptions
		want string
	}{
		{
			name: "explicit scout target",
			opts: RunOptions{ScoutExplicit: true},
			want: "explicit scout target preserves relay topology",
		},
		{
			name: "strict scout failure policy",
			opts: RunOptions{ScoutFailurePolicy: "fail"},
			want: "strict scout policy preserves relay topology",
		},
		{
			name: "blocking scout loss policy",
			opts: RunOptions{LossPolicy: RoleLossPolicies{Scout: LossPolicyBlock}},
			want: "strict scout policy preserves relay topology",
		},
		{
			name: "unconstrained relay",
			opts: RunOptions{ScoutFailurePolicy: "continue"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := relaySimplificationConstraint(test.opts); got != test.want {
				t.Fatalf("relay simplification constraint = %q, want %q", got, test.want)
			}
		})
	}
}

func TestDoctorAdapterIDsIncludeEveryReportedProvider(t *testing.T) {
	want := []string{"codex", "codex-oss", "claude", "agy", "gosling", "grok", "openai-compatible"}
	got := DoctorAdapterIDs()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("doctor adapter IDs = %#v, want %#v", got, want)
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

	scoutPrompt := strings.ToLower(BuildScoutPrompt("/repo", "ship it", "brief", "recon", "pre", "", "", "", nil))
	for _, forbidden := range []string{"use \"pass\"", "needs_changes", "blocking findings", "produce blocking findings"} {
		if strings.Contains(scoutPrompt, forbidden) {
			t.Fatalf("scout prompt leaked reviewer authority %q:\n%s", forbidden, scoutPrompt)
		}
	}
	for _, required := range []string{"do not run tests", "after implementation instead"} {
		if !strings.Contains(scoutPrompt, required) {
			t.Fatalf("scout prompt missing execution boundary %q:\n%s", required, scoutPrompt)
		}
	}

	coderPrompt := strings.ToLower(BuildCoderPrompt("/repo", "ship it"))
	for _, forbidden := range []string{"you are read-only", "do not edit files"} {
		if strings.Contains(coderPrompt, forbidden) {
			t.Fatalf("coder prompt leaked read-only instruction %q:\n%s", forbidden, coderPrompt)
		}
	}
}

func TestValidateInvocationBudgetRejectsImpossibleModeCap(t *testing.T) {
	err := validateInvocationBudget(RunOptions{Mode: ModeRelay, Rounds: 1, MaxRoleInvocations: 1})
	if err == nil {
		t.Fatal("expected relay cap validation error")
	}
	if got := err.Error(); !strings.Contains(got, "at least 6 planned provider invocations") || !strings.Contains(got, "use 0 for unlimited") {
		t.Fatalf("error = %q", got)
	}
}

func TestValidateInvocationBudgetAcceptsUnlimitedAndModeMinimums(t *testing.T) {
	cases := []struct {
		mode   Mode
		rounds int
		cap    int
	}{
		{ModeSolo, 2, 2},
		{ModeSupervisor, 1, 3},
		{ModeRelay, 2, 9},
		{ModeAdversarial, 2, 4},
		{ModeRelay, 2, 0},
	}
	for _, tc := range cases {
		if err := validateInvocationBudget(RunOptions{Mode: tc.mode, Rounds: tc.rounds, MaxRoleInvocations: tc.cap}); err != nil {
			t.Fatalf("validateInvocationBudget(%s, rounds=%d, cap=%d): %v", tc.mode, tc.rounds, tc.cap, err)
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

func TestLoadRepoInstructions_SkipsAdapterMarkerFiles(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("root instructions\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{".codex", ".claude", ".agy", ".tagteam"} {
		if err := os.WriteFile(filepath.Join(repo, marker), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	bundle, err := loadRepoInstructions(context.Background(), repo, maxRepoInstructionBytes)
	if err != nil {
		t.Fatalf("loadRepoInstructions() error = %v", err)
	}
	if !strings.Contains(bundle.Text, "root instructions") {
		t.Fatalf("bundle missing root instructions: %q", bundle.Text)
	}
	if bundle.Metadata.SourceCount != 1 {
		t.Fatalf("source count = %d, want 1", bundle.Metadata.SourceCount)
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

func TestWithAdapterRepoInstructionsAvoidsNativeDuplicate(t *testing.T) {
	const prompt = "base prompt"
	const instructions = "follow repo rules"
	claude := &ClaudeAdapter{}
	if got := withAdapterRepoInstructions(claude, prompt, instructions); got != prompt {
		t.Fatalf("Claude prompt duplicated project instructions: %q", got)
	}
	for name, adapter := range map[string]Adapter{
		"codex": &CodexAdapter{IDValue: "codex"},
		"grok":  &GrokAdapter{},
		"agy":   &AgyAdapter{},
	} {
		got := withAdapterRepoInstructions(adapter, prompt, instructions)
		if !strings.Contains(got, repoInstructionsPromptHeader) || !strings.Contains(got, instructions) {
			t.Fatalf("%s prompt omitted explicit project instructions: %q", name, got)
		}
	}
}

func TestRunSupervisorWithFallbackPersistsSelection(t *testing.T) {
	runDir := t.TempDir()
	primary := RoleTarget{Adapter: "primary", Model: "primary-model"}
	fallback := RoleTarget{Adapter: "fallback", Model: "fallback-model"}
	opts := RunOptions{
		Adversary: primary,
		LossPolicy: RoleLossPolicies{
			Supervisor: LossPolicyReplaceThenBlock,
		},
		FallbacksByTarget: map[string][]string{
			roleTargetString(primary): {roleTargetString(fallback)},
		},
	}
	primaryAdapter := fakeAdapter{id: primary.Adapter}
	fallbackAdapter := fakeAdapter{id: fallback.Adapter}
	var reviewer Adapter = primaryAdapter
	meta := Meta{
		Adapters: map[string]string{"supervisor": primary.Adapter},
		Models:   map[string]string{"supervisor": primary.Model},
	}
	final := FinalRun{
		Adversary: primary,
		Adapters:  map[string]string{"supervisor": primary.Adapter},
		Models:    map[string]string{"supervisor": primary.Model},
	}
	var attempts []string
	result, err := (&App{}).runSupervisorWithFallback(context.Background(), &opts, map[string]Adapter{
		primary.Adapter:  primaryAdapter,
		fallback.Adapter: fallbackAdapter,
	}, runDir, "supervisor", &reviewer, &meta, &final, func(target RoleTarget, adapter Adapter) (Result, error) {
		attempts = append(attempts, roleTargetString(target))
		if target == primary {
			return Result{}, errors.New("primary supervisor timed out")
		}
		return Result{Text: "fallback brief"}, nil
	})
	if err != nil {
		t.Fatalf("runSupervisorWithFallback() error = %v", err)
	}
	if result.Text != "fallback brief" {
		t.Fatalf("result = %#v", result)
	}
	if got, want := attempts, []string{roleTargetString(primary), roleTargetString(fallback)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("attempts = %#v, want %#v", got, want)
	}
	if opts.Adversary != fallback || final.Adversary != fallback || reviewer.ID() != fallback.Adapter {
		t.Fatalf("selected fallback was not retained opts=%#v final=%#v reviewer=%s", opts.Adversary, final.Adversary, reviewer.ID())
	}
	if meta.Adapters["supervisor"] != fallback.Adapter || meta.Models["supervisor"] != fallback.Model {
		t.Fatalf("meta selection = %#v / %#v", meta.Adapters, meta.Models)
	}
	if final.Adapters["supervisor"] != fallback.Adapter || final.Models["supervisor"] != fallback.Model || !final.Degraded {
		t.Fatalf("final fallback state = %#v", final)
	}
	status := final.RoleStatuses["supervisor"]
	if status.Status != "completed" || status.Selected != roleTargetString(fallback) || !reflect.DeepEqual(status.Attempts, attempts) {
		t.Fatalf("supervisor status = %#v", status)
	}
	var persisted Meta
	readJSONFile(t, filepath.Join(runDir, "meta.json"), &persisted)
	if persisted.Adapters["supervisor"] != fallback.Adapter || persisted.Models["supervisor"] != fallback.Model {
		t.Fatalf("persisted selection = %#v / %#v", persisted.Adapters, persisted.Models)
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
		defer func() {
			if err := cleanup(""); err != nil {
				t.Errorf("preflight cleanup: %v", err)
			}
		}()
	}
	branch := strings.TrimSpace(runGit(t, repo, "rev-parse", "--abbrev-ref", "HEAD"))
	if branch != "tagteam/2026-07-07T120000Z" {
		t.Fatalf("branch = %q", branch)
	}
}

func TestPreflightAllowDirtyCreatesCheckpointBranch(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")
	original := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	baseline, cleanup, err := preflight(RunOptions{Workdir: repo, AllowDirty: true}, "2026-07-11T120000Z")
	if err != nil {
		t.Fatalf("preflight() error = %v", err)
	}
	if cleanup != nil {
		t.Fatalf("allow-dirty checkpoint must persist its branch")
	}
	branch := strings.TrimSpace(runGit(t, repo, "rev-parse", "--abbrev-ref", "HEAD"))
	if branch != "tagteam/2026-07-11T120000Z" {
		t.Fatalf("branch = %q", branch)
	}
	head := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	if baseline != head {
		t.Fatalf("baseline = %q, HEAD = %q", baseline, head)
	}
	if parent := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD^")); parent != original {
		t.Fatalf("checkpoint parent = %q, want %q", parent, original)
	}
	if status := strings.TrimSpace(runGit(t, repo, "status", "--porcelain")); status != "" {
		t.Fatalf("checkpoint worktree is dirty: %q", status)
	}
	if got := strings.TrimSpace(runGit(t, repo, "show", "HEAD:new.txt")); got != "new" {
		t.Fatalf("checkpoint omitted untracked file: %q", got)
	}
}

func TestPreflightDryRunAllowDirtyDoesNotCheckpoint(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "baseline\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	originalBranch := strings.TrimSpace(runGit(t, repo, "rev-parse", "--abbrev-ref", "HEAD"))
	originalHead := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	mustWriteFile(t, filepath.Join(repo, "README.md"), "changed\n")
	mustWriteFile(t, filepath.Join(repo, "untracked.txt"), "untracked\n")
	originalStatus := runGit(t, repo, "status", "--porcelain")

	baseline, cleanup, err := preflight(RunOptions{Workdir: repo, AllowDirty: true, DryRun: true}, "2026-07-24T000000Z")
	if err != nil {
		t.Fatalf("preflight() error = %v", err)
	}
	if cleanup != nil {
		t.Fatal("dry-run preflight must not schedule mutating cleanup")
	}
	if baseline != originalHead {
		t.Fatalf("baseline = %q, want %q", baseline, originalHead)
	}
	if branch := strings.TrimSpace(runGit(t, repo, "rev-parse", "--abbrev-ref", "HEAD")); branch != originalBranch {
		t.Fatalf("dry-run changed branch to %q, want %q", branch, originalBranch)
	}
	if head := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD")); head != originalHead {
		t.Fatalf("dry-run changed HEAD to %q, want %q", head, originalHead)
	}
	if status := runGit(t, repo, "status", "--porcelain"); status != originalStatus {
		t.Fatalf("dry-run changed worktree status to %q, want %q", status, originalStatus)
	}
}

func TestPreflightAllowDirtyRejectsWhitespaceInvalidCheckpoint(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "baseline\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "changed\n\n")

	_, _, err := preflight(RunOptions{Workdir: repo, AllowDirty: true}, "2026-07-23T000000Z")
	if err == nil || !strings.Contains(err.Error(), "validate dirty-worktree checkpoint") {
		t.Fatalf("preflight() error = %v, want checkpoint validation failure", err)
	}
}

func TestDeterministicDiffIgnoresTagteamRunDirButIncludesUntrackedFiles(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".tagteam/\ntagteam\ndocs/logs/session/\n"), 0o644); err != nil {
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
	if err := os.WriteFile(filepath.Join(repo, "staged.txt"), []byte("already staged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ignoredStagedPath := filepath.Join(repo, "docs", "logs", "session", "072026", "entry.md")
	if err := os.MkdirAll(filepath.Dir(ignoredStagedPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ignoredStagedPath, []byte("durable session log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "staged.txt")
	runGit(t, repo, "add", "-f", "docs/logs/session/072026/entry.md")

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
	if !strings.Contains(text, "diff --git a/staged.txt b/staged.txt") {
		t.Fatalf("patch missing staged addition:\n%s", text)
	}
	if !strings.Contains(text, "diff --git a/docs/logs/session/072026/entry.md b/docs/logs/session/072026/entry.md") {
		t.Fatalf("patch missing explicitly staged ignored addition:\n%s", text)
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
	_, _, _, err := app.runAdversary(context.Background(), opts, 1, opts.Workdir, filepath.Join(opts.Workdir, "schema.json"), "prompt", "HEAD", "diff", filepath.Join(opts.Workdir, "diff.patch"), "", "", nil, RelayContext{}, "", nil)
	if err == nil {
		t.Fatal("expected error")
	}
}
