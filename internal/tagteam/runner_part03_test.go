package tagteam

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
	readJSONFile(t, statePathForWorkdir(repo, "latest.json"), &latest)
	if latest.RunID != final.RunID || latest.FinalPath != filepath.Join(final.RunDir, "final.json") {
		t.Fatalf("latest = %#v final=%#v", latest, final)
	}
}

func TestRunSoloContinuesAfterZeroDeltaWorkerFallback(t *testing.T) {
	installFakeBinaries(t, map[string]string{
		"claude": `#!/bin/sh
if [ "$1" = "--version" ]; then echo "claude 1.0"; exit 0; fi
echo "primary worker failed" >&2
exit 7
`,
		"codex": `#!/bin/sh
if [ "$1" = "--version" ]; then echo "codex 1.0"; exit 0; fi
output=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then output="$2"; shift 2; continue; fi
  shift
done
printf 'after\n' > README.md
payload='{"schema_version":1,"status":"completed","summary":"fallback completed","files_changed":["README.md"],"checks_run":[],"remaining_risks":[]}'
if [ -n "$output" ]; then printf '%s' "$payload" > "$output"; else printf '%s' "$payload"; fi
`,
	})
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "before\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "baseline")

	final, err := NewApp(DefaultConfig()).Run(context.Background(), RunOptions{
		Prompt:       "update README",
		Workdir:      repo,
		Mode:         ModeSolo,
		Coder:        RoleTarget{Adapter: "claude", Model: "primary-model"},
		AllowedPaths: []string{"README.md"},
		LossPolicy:   RoleLossPolicies{Worker: LossPolicyReplaceThenBlock},
		Fallbacks:    RoleFallbacks{Worker: []string{"codex:fallback-model"}},
		Rounds:       1,
		Timeout:      10 * time.Second,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if final.Status != RunStatusDegraded || roleTargetString(final.Coder) != "codex:fallback-model" {
		t.Fatalf("final fallback state = status:%q coder:%#v", final.Status, final.Coder)
	}
	if final.Phase != "solo" || final.RoundsRequested != 1 || final.Budgets.MaxRounds != 1 {
		t.Fatalf("solo phase/budget = phase:%q requested:%d max:%d", final.Phase, final.RoundsRequested, final.Budgets.MaxRounds)
	}
	if final.Adapters["solo"] != "codex" || final.Models["solo"] != "fallback-model" {
		t.Fatalf("final role maps were not updated: adapters=%#v models=%#v", final.Adapters, final.Models)
	}
	var meta Meta
	readJSONFile(t, filepath.Join(final.RunDir, "meta.json"), &meta)
	if meta.Adapters["solo"] != "codex" || meta.Models["solo"] != "fallback-model" {
		t.Fatalf("meta role maps were not updated: adapters=%#v models=%#v", meta.Adapters, meta.Models)
	}
	if data, readErr := os.ReadFile(filepath.Join(repo, "README.md")); readErr != nil || string(data) != "after\n" {
		t.Fatalf("fallback edit = %q err=%v", data, readErr)
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

func TestReviewRunsConfiguredTestAndIncludesEvidence(t *testing.T) {
	installFakeBinaries(t, map[string]string{"claude": fakeClaudeScript})
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "before\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "baseline")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "after\n")
	argsLog := filepath.Join(t.TempDir(), "claude.log")
	t.Setenv("CLAUDE_ARGS_LOG", argsLog)

	final, err := NewApp(DefaultConfig()).Review(context.Background(), RunOptions{
		Workdir:        repo,
		Mode:           ModeSupervisor,
		Adversary:      RoleTarget{Adapter: "claude"},
		TestCmd:        "printf trusted-review-test",
		Timeout:        10 * time.Second,
		MaxOutputBytes: 2 * 1024 * 1024,
		EnvOverlay:     map[string]string{"CLAUDE_ARGS_LOG": argsLog},
	}, "review the diff")
	if err == nil {
		t.Fatal("expected fake reviewer blocking finding")
	}
	if len(final.Tests) != 1 || !final.Tests[0].Passed {
		t.Fatalf("tests = %#v", final.Tests)
	}
	logBytes, readErr := os.ReadFile(argsLog)
	if readErr != nil {
		t.Fatal(readErr)
	}
	logText := string(logBytes)
	if !strings.Contains(logText, "Command: printf trusted-review-test") || !strings.Contains(logText, "trusted-review-test") {
		t.Fatalf("review prompt missing trusted test evidence:\n%s", logText)
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
	readJSONFile(t, statePathForWorkdir(repo, "latest.json"), &latest)
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
