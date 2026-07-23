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
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRunAdversaryUsesConfiguredFallbackAfterPrimaryFailure(t *testing.T) {
	installFakeBinaries(t, map[string]string{
		"claude": fakeClaudeInvokeFailScript,
		"codex":  fakeCodexPassingReviewScript,
	})

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "hello\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "hello\nworld\n")

	cfg := DefaultConfig()
	app := NewApp(cfg)
	runDir := filepath.Join(repo, ".tagteam", "runs", "fallback-test")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	schemaPath := filepath.Join(runDir, "review-schema.json")
	if err := os.WriteFile(schemaPath, []byte(ReviewSchema), 0o644); err != nil {
		t.Fatal(err)
	}
	diffPath := filepath.Join(runDir, "diff.patch")
	if err := os.WriteFile(diffPath, []byte("diff --git a/README.md b/README.md\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	final := FinalRun{Adapters: map[string]string{"adversary": "claude"}, Models: map[string]string{"adversary": "sonnet-5"}}
	initFinalState(&final, RunOptions{EnvOverlay: map[string]string{}})
	opts := RunOptions{
		Prompt:    "review",
		Workdir:   repo,
		Mode:      ModeAdversarial,
		Adversary: RoleTarget{Adapter: "claude", Model: "sonnet-5"},
		LossPolicy: RoleLossPolicies{
			Reviewer: LossPolicyReplaceThenBlock,
		},
		FallbacksByTarget: TargetFallbacks{
			"claude:sonnet-5": []string{"codex:gpt-5.4"},
		},
		Timeout:        5 * time.Second,
		MaxOutputBytes: 2 * 1024 * 1024,
	}

	review, _, outputPath, err := app.runAdversary(context.Background(), opts, 1, runDir, schemaPath, opts.Prompt, "HEAD", "diff --git a/README.md b/README.md\n", diffPath, "", "", nil, RelayContext{}, "", &final)
	if err != nil {
		t.Fatalf("runAdversary() error = %v", err)
	}
	if review == nil || review.Verdict != "pass" {
		t.Fatalf("review = %#v", review)
	}
	if !strings.Contains(outputPath, "fallback-codex-gpt-5-4") {
		t.Fatalf("output path = %q", outputPath)
	}
	if !final.Degraded || final.DegradedReason != string(ReasonFallbackUsed) {
		t.Fatalf("final degradation = degraded=%v reason=%q", final.Degraded, final.DegradedReason)
	}
	if final.Adapters["adversary"] != "codex" || final.Models["adversary"] != "gpt-5.4" {
		t.Fatalf("final adapter/model = %#v %#v", final.Adapters, final.Models)
	}
	status := final.RoleStatuses["adversary"]
	if status.Selected != "codex:gpt-5.4" || len(status.Attempts) != 2 {
		t.Fatalf("role status = %#v", status)
	}
}

func TestRunAdversaryRepairsReviewJSONWithWorker(t *testing.T) {
	installFakeBinaries(t, map[string]string{
		"claude": fakeClaudeInvalidReviewScript,
		"codex":  fakeCodexPassingReviewScript,
	})

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "hello\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	app := NewApp(DefaultConfig())
	runDir := filepath.Join(repo, ".tagteam", "runs", "repair-test")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	schemaPath := filepath.Join(runDir, "review-schema.json")
	if err := os.WriteFile(schemaPath, []byte(ReviewSchema), 0o644); err != nil {
		t.Fatal(err)
	}
	diffPath := filepath.Join(runDir, "diff.patch")
	if err := os.WriteFile(diffPath, []byte("diff --git a/README.md b/README.md\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	final := FinalRun{Adapters: map[string]string{"worker": "codex", "supervisor": "claude"}, Models: map[string]string{"worker": "gpt-5.4-mini", "supervisor": "sonnet"}}
	initFinalState(&final, RunOptions{EnvOverlay: map[string]string{}})
	opts := RunOptions{
		Prompt:         "review",
		Workdir:        repo,
		Mode:           ModeSupervisor,
		Coder:          RoleTarget{Adapter: "codex", Model: "gpt-5.4-mini"},
		Adversary:      RoleTarget{Adapter: "claude", Model: "sonnet"},
		JSONRepair:     "worker",
		Timeout:        5 * time.Second,
		MaxOutputBytes: 2 * 1024 * 1024,
	}

	review, _, outputPath, err := app.runAdversary(context.Background(), opts, 1, runDir, schemaPath, opts.Prompt, "HEAD", "diff --git a/README.md b/README.md\n", diffPath, "", "", nil, RelayContext{}, "", &final)
	if err != nil {
		t.Fatalf("runAdversary() error = %v", err)
	}
	if review == nil || review.Verdict != "pass" {
		t.Fatalf("review = %#v", review)
	}
	if !final.Degraded || final.DegradedReason != string(ReasonJSONRepairUsed) {
		t.Fatalf("final degradation = degraded=%v reason=%q", final.Degraded, final.DegradedReason)
	}
	if !fileExists(outputPath + ".repaired.json") {
		t.Fatalf("expected repaired artifact for %s", outputPath)
	}
}

func TestRunAdapterSurfacesClaudeErrorEnvelopeOnNonzeroExit(t *testing.T) {
	installFakeBinaries(t, map[string]string{
		"claude": `#!/bin/sh
if [ "$1" = "--version" ]; then echo "1.0.0"; exit 0; fi
printf '%s' '{"type":"result","subtype":"error_max_structured_output_retries","is_error":true,"result":"","errors":["Failed to provide valid structured output after 5 attempts"]}'
exit 1
`,
	})

	runDir := t.TempDir()
	workdir := t.TempDir()
	runGit(t, workdir, "init")
	_, err := NewApp(DefaultConfig()).runAdapter(context.Background(), &ClaudeAdapter{}, RoleAdversary, Request{
		Prompt:     "review",
		Workdir:    workdir,
		RunDir:     runDir,
		Timeout:    5 * time.Second,
		OutputPath: filepath.Join(runDir, "review.json"),
	}, false)
	if err == nil || !strings.Contains(err.Error(), "claude reported error_max_structured_output_retries") {
		t.Fatalf("runAdapter() error = %v, want Claude envelope subtype", err)
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
	      "estimated_seconds": 60,
	      "allowed_scope": ["a.go"],
	      "acceptance": ["first passes"],
	      "validation": ["go test ./..."]
	    },
	    {
	      "id": "P2",
	      "title": "Second",
	      "goal": "Do second",
	      "estimated_seconds": 60,
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
	    {"id":"P1","title":"First","goal":"Do first","estimated_seconds":60,"allowed_scope":["a.go"],"acceptance":["ok"],"validation":["go test ./..."]},
	    {"id":"P2","title":"Second","goal":"Do second","estimated_seconds":60,"allowed_scope":["b.go"],"acceptance":["ok"],"validation":["go test ./..."]}
	  ],
	  "selected_package": "P1"
	}`)
	_, err := parseWorkPlan(raw, "", 1)
	if err == nil {
		t.Fatal("expected max package error")
	}
}

func TestParseWorkPlanExtractsFencedJSON(t *testing.T) {
	raw := []byte("```json\n{\"summary\":\"split work\",\"packages\":[{\"id\":\"P1\",\"title\":\"First\",\"goal\":\"Do first\",\"estimated_seconds\":60,\"allowed_scope\":[\"a.go\"],\"acceptance\":[\"ok\"],\"validation\":[\"go test ./...\"]}],\"selected_package\":\"P1\"}\n```")
	plan, err := parseWorkPlan(raw, "", 5)
	if err != nil {
		t.Fatalf("parseWorkPlan() error = %v", err)
	}
	if plan.SelectedPackage != "P1" {
		t.Fatalf("selected package = %q", plan.SelectedPackage)
	}
}

func TestParseWorkPlanExtractsWrappedJSON(t *testing.T) {
	raw := []byte("Here is the work plan:\n{\"schema_version\":1,\"summary\":\"split work\",\"packages\":[{\"id\":\"P1\",\"title\":\"First\",\"goal\":\"Do first\",\"estimated_seconds\":60,\"allowed_scope\":[\"a.go\"],\"acceptance\":[\"ok\"],\"validation\":[\"go test ./...\"]}],\"selected_package\":\"P1\"}\nDone.")
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
				// Non-login shell: a login shell (-l) would source profile files
				// that can echo to stdout (e.g. an nvm banner) and corrupt the
				// adapter output this test parses as JSON.
				Argv: []string{"sh", "-c", "printf '{\"schema_version\":2,\"verdict\":\"pass\",\"summary\":\"ok\",\"findings\":[],\"test_suggestions\":[],\"data_loss_checks\":{\"malformed_input_preservation\":{\"status\":\"not_applicable\",\"evidence\":\"not applicable\"},\"annotation_history_retention\":{\"status\":\"not_applicable\",\"evidence\":\"not applicable\"},\"ambiguous_identity_handling\":{\"status\":\"not_applicable\",\"evidence\":\"not applicable\"},\"read_only_non_mutation\":{\"status\":\"pass\",\"evidence\":\"read-only\"}},\"prior_finding_dispositions\":[]}'"},
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

func TestRunAdapterStoresNormalizedGrokWorkerArtifact(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "baseline\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "baseline")

	worker := `{"schema_version":1,"status":"completed","summary":"implemented","files_changed":[],"checks_run":[],"remaining_risks":[]}`
	raw := `{"text":` + strconv.Quote(worker) + `,"thought":"provider-only reasoning"}`
	runDir := t.TempDir()
	outputPath := filepath.Join(runDir, "coder-round-1.md")
	adapter := fakeAdapter{
		id: "grok",
		build: func(role Role, req Request) (*CommandSpec, error) {
			return &CommandSpec{Argv: []string{"sh", "-c", "printf '%s' '" + raw + "'"}, Dir: repo}, nil
		},
		parse: (&GrokAdapter{}).ParseResult,
	}
	result, err := NewApp(DefaultConfig()).runAdapter(context.Background(), adapter, RoleCoder, Request{
		Context:               context.Background(),
		Workdir:               repo,
		RunDir:                runDir,
		OutputPath:            outputPath,
		Timeout:               time.Second,
		RequireWorkerContract: true,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Worker == nil || result.Worker.Summary != "implemented" {
		t.Fatalf("worker result = %#v", result.Worker)
	}
	for _, path := range []string{outputPath, outputPath + ".raw"} {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if strings.Contains(string(data), "provider-only reasoning") {
			t.Fatalf("artifact %s retained provider-only reasoning", path)
		}
	}
	data, readErr := os.ReadFile(outputPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	parsedWorker, parseErr := parseWorkerResult(data)
	if parseErr != nil || parsedWorker.Summary != "implemented" {
		t.Fatalf("normalized worker artifact = %q, parse error = %v", data, parseErr)
	}
	stdoutPaths, globErr := filepath.Glob(filepath.Join(runDir, "deliveries", "*.stdout.txt"))
	if globErr != nil || len(stdoutPaths) != 1 {
		t.Fatalf("stdout artifacts = %v, error = %v", stdoutPaths, globErr)
	}
	stdout, readErr := os.ReadFile(stdoutPaths[0])
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(stdout), "provider-only reasoning") {
		t.Fatalf("invocation stdout retained provider-only reasoning")
	}
}

type fakeAdapter struct {
	id    string
	build func(role Role, req Request) (*CommandSpec, error)
	parse func(role Role, raw []byte) (Result, error)
}

func (f fakeAdapter) ID() string {
	if f.id != "" {
		return f.id
	}
	return "fake"
}
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

func TestRunEditorWithContractRetryRetriesNoEditIdentityResponse(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "hello\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	calls := 0
	adapter := fakeDirectAdapter{
		build: func(role Role, req Request) (*CommandSpec, error) {
			return &CommandSpec{Argv: []string{"fake"}, Dir: repo, Output: req.OutputPath}, nil
		},
		direct: func(role Role, req Request) (Result, error) {
			calls++
			if calls == 1 {
				return Result{Raw: []byte("I am running on Gemini.")}, nil
			}
			raw := []byte(`{"schema_version":1,"status":"completed","summary":"completed without edits","files_changed":[],"checks_run":[],"remaining_risks":[]}`)
			return Result{Raw: raw, Text: string(raw)}, nil
		},
	}
	before, err := captureWorktreeSnapshot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(t.TempDir(), "worker-round-1.md")
	result, err := NewApp(DefaultConfig()).runEditorWithContractRetry(context.Background(), RunOptions{
		Workdir: repo,
		Mode:    ModeSupervisor,
		Timeout: 10 * time.Second,
	}, adapter, Request{
		Context:               context.Background(),
		Prompt:                "implement the task",
		Workdir:               repo,
		RunDir:                filepath.Dir(outputPath),
		OutputPath:            outputPath,
		Timeout:               10 * time.Second,
		Phase:                 "worker",
		RequireWorkerContract: true,
	}, before)
	if err != nil {
		t.Fatalf("runEditorWithContractRetry() error = %v", err)
	}
	if calls != 2 || result.Worker == nil {
		t.Fatalf("calls=%d result=%#v", calls, result)
	}
	if !fileExists(outputPath+".retry-prompt.md") || !fileExists(filepath.Join(filepath.Dir(outputPath), "worker-round-1.retry.md")) {
		t.Fatal("expected preserved retry prompt and retry output")
	}
}

func TestValidateWorkerResultExplainsIgnoredClaim(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, ".gitignore"), ".giles/\n")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "baseline\n")
	runGit(t, repo, "add", ".gitignore", "README.md")
	runGit(t, repo, "commit", "-m", "init")
	before, err := captureWorktreeSnapshot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	ignoredPath := filepath.Join(repo, ".giles", "feature-ledger", "entry.md")
	mustWriteFile(t, ignoredPath, "required local ledger\n")
	result := Result{Text: `{"schema_version":1,"status":"completed","summary":"wrote ledger","files_changed":[".giles/feature-ledger/entry.md"],"checks_run":[],"remaining_risks":[]}`}
	err = validateWorkerResultForRequest(context.Background(), Request{Workdir: repo, RequireWorkerContract: true}, &result, before)
	if err == nil || !IsOutputContractError(err) {
		t.Fatalf("error = %T %v, want output contract error", err, err)
	}
	for _, want := range []string{"ignored paths", ".giles/feature-ledger/entry.md", "git add -f"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ignored-path diagnostic missing %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "required local ledger") {
		t.Fatalf("ignored file contents leaked into diagnostic: %v", err)
	}
}

func TestValidateWorkerResultNormalizesUnchangedCumulativeClaims(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "previous.txt"), "baseline\n")
	mustWriteFile(t, filepath.Join(repo, "current.txt"), "baseline\n")
	runGit(t, repo, "add", "previous.txt", "current.txt")
	runGit(t, repo, "commit", "-m", "init")
	mustWriteFile(t, filepath.Join(repo, "previous.txt"), "changed in round one\n")
	before, err := captureWorktreeSnapshot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(repo, "current.txt"), "changed in round two\n")
	result := Result{Text: `{"schema_version":1,"status":"completed","summary":"fixed review","files_changed":["previous.txt","current.txt"],"checks_run":[],"remaining_risks":[]}`}
	if err := validateWorkerResultForRequest(context.Background(), Request{Workdir: repo, RequireWorkerContract: true}, &result, before); err != nil {
		t.Fatalf("validateWorkerResultForRequest() error = %v", err)
	}
	if result.Worker == nil || strings.Join(result.Worker.FilesChanged, ",") != "current.txt" {
		t.Fatalf("normalized worker = %#v", result.Worker)
	}
	if len(result.Worker.RemainingRisks) != 1 || !strings.Contains(result.Worker.RemainingRisks[0], "normalized files_changed") {
		t.Fatalf("normalization disclosure = %#v", result.Worker.RemainingRisks)
	}
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
