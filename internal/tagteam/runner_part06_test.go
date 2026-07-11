package tagteam

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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

func TestConciseAdapterErrorPreservesHeadTailAndArtifactPath(t *testing.T) {
	message := "HEAD-MARKER\n" + strings.Repeat("x", 6000) + "\nTAIL-MARKER"
	got := conciseAdapterError(message, "/tmp/run/stderr.log")
	for _, want := range []string{"HEAD-MARKER", "TAIL-MARKER", "bytes omitted", "full stderr: /tmp/run/stderr.log"} {
		if !strings.Contains(got, want) {
			t.Fatalf("concise error missing %q: %q", want, got)
		}
	}
	if len(got) >= len(message) {
		t.Fatalf("adapter error was not reduced: got=%d original=%d", len(got), len(message))
	}
	if small := conciseAdapterError("small failure", "/tmp/stderr.log"); small != "small failure" {
		t.Fatalf("small error changed: %q", small)
	}
}

func TestRunAdapterPersistsStalledStatusAfterWatchdogCancellation(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("baseline\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "baseline")

	runDir := t.TempDir()
	adapter := fakeAdapter{
		build: func(role Role, req Request) (*CommandSpec, error) {
			return &CommandSpec{Argv: []string{"sh", "-c", "sleep 10"}, Dir: repo}, nil
		},
		parse: func(role Role, raw []byte) (Result, error) {
			return Result{Raw: raw}, nil
		},
	}
	started := time.Now()
	_, err := NewApp(DefaultConfig()).runAdapter(context.Background(), adapter, RoleReporter, Request{
		Context:         context.Background(),
		Workdir:         repo,
		RunDir:          runDir,
		Timeout:         5 * time.Second,
		WatchdogTimeout: 500 * time.Millisecond,
		Phase:           "watchdog regression",
	}, false)
	if err == nil || !strings.Contains(err.Error(), string(ReasonStalled)) {
		t.Fatalf("runAdapter() error = %v, want stalled reason", err)
	}
	if elapsed := time.Since(started); elapsed > 4*time.Second {
		t.Fatalf("watchdog cancellation took %s", elapsed)
	}

	var progress LiveProgress
	readJSONFile(t, filepath.Join(runDir, liveProgressArtifact), &progress)
	if progress.Status != "stalled" {
		t.Fatalf("live progress status = %q, want stalled", progress.Status)
	}
}

func TestRunDirectAdapterUsesWatchdogAndPersistsLogicalProgress(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "baseline\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "baseline")

	runDir := t.TempDir()
	adapter := fakeDirectAdapter{
		build: func(role Role, req Request) (*CommandSpec, error) {
			return &CommandSpec{Argv: []string{"direct"}, Dir: repo}, nil
		},
		direct: func(role Role, req Request) (Result, error) {
			<-req.Context.Done()
			return Result{}, req.Context.Err()
		},
	}
	started := time.Now()
	_, err := NewApp(DefaultConfig()).runAdapter(context.Background(), adapter, RoleScout, Request{
		Context:         context.Background(),
		Workdir:         repo,
		RunDir:          runDir,
		Timeout:         2 * time.Second,
		WatchdogTimeout: 50 * time.Millisecond,
		Phase:           "relay scout",
		ProgressRole:    RoleScout,
	}, false)
	if !errors.Is(err, errInvocationStalled) {
		t.Fatalf("runAdapter() error = %v, want stalled", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("direct watchdog took %s", elapsed)
	}
	var progress LiveProgress
	readJSONFile(t, filepath.Join(runDir, liveProgressArtifact), &progress)
	if progress.Status != "stalled" || progress.Role != RoleScout {
		t.Fatalf("direct progress = %#v", progress)
	}
}
