package tagteam

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
		Scout:          RoleTarget{Adapter: "agy", Model: "gemini-3.6-flash-low"},
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
		Scout:          RoleTarget{Adapter: "agy", Model: "gemini-3.6-flash-low"},
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
	if !strings.Contains(preScoutInvocation, "Host evidence (retrieval and advisory symlink topology; topology never authorizes scope):\n(not provided for this scout phase)") {
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
		Scout:              RoleTarget{Adapter: "agy", Model: "gemini-3.6-flash-low"},
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
	installFakeBinaries(t, map[string]string{
		"claude": fakeClaudeScript,
		"codex": `#!/bin/sh
if [ "$1" = "--version" ]; then echo "codex 1.0"; exit 0; fi
output=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then output="$2"; shift 2; continue; fi
  shift
done
payload='{"schema_version":1,"status":"completed","summary":"profile coder completed","files_changed":[],"checks_run":[],"remaining_risks":[]}'
if [ -n "$output" ]; then printf '%s' "$payload" > "$output"; else printf '%s' "$payload"; fi
`,
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
		Coder:     "codex:gpt-5.6-terra",
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
	if fixed.Coder.Adapter != "codex" || fixed.Coder.Model != "gpt-5.6-terra" {
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
	if err == nil || ExitCode(err) != ExitBlockingFindings {
		t.Fatalf("Review() error = %v, want blocking review failure", err)
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
	if err == nil || ExitCode(err) != ExitBlockingFindings {
		t.Fatalf("Review() error = %v, want blocking review failure", err)
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
