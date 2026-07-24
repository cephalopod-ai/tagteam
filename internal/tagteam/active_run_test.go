package tagteam

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestActivateRunWritesRunningPointerAtStatePath(t *testing.T) {
	workdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workdir, ".tagteam"), 0o755); err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(workdir, ".tagteam", "runs", "run-1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}

	activateRun(workdir, "run-1", runDir, ModeSolo)

	active, err := readActiveRun(workdir)
	if err != nil {
		t.Fatalf("readActiveRun() error = %v", err)
	}
	if active.RunID != "run-1" {
		t.Fatalf("run_id = %q, want run-1", active.RunID)
	}
	if active.Status != "running" {
		t.Fatalf("status = %q, want running", active.Status)
	}
	if active.StatePath != filepath.Join(runDir, "state.json") {
		t.Fatalf("state_path = %q, want %q", active.StatePath, filepath.Join(runDir, "state.json"))
	}
	if active.FinalPath != filepath.Join(runDir, "final.json") {
		t.Fatalf("final_path = %q, want %q", active.FinalPath, filepath.Join(runDir, "final.json"))
	}
	if active.RunDir != runDir {
		t.Fatalf("run_dir = %q, want %q", active.RunDir, runDir)
	}
	if active.Mode != ModeSolo {
		t.Fatalf("mode = %q, want %q", active.Mode, ModeSolo)
	}
	if active.SchemaVersion != ArtifactSchemaVersion {
		t.Fatalf("schema_version = %d, want %d", active.SchemaVersion, ArtifactSchemaVersion)
	}
	if active.StartedAt.IsZero() || active.UpdatedAt.IsZero() {
		t.Fatalf("expected started_at/updated_at to be set: %#v", active)
	}
}

func TestDeactivateRunCompletedRemovesPointer(t *testing.T) {
	workdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workdir, ".tagteam"), 0o755); err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(workdir, ".tagteam", "runs", "run-1")
	activateRun(workdir, "run-1", runDir, ModeSolo)

	deactivateRun(workdir, "run-1", true)

	if _, err := os.Stat(activeRunPath(workdir)); !os.IsNotExist(err) {
		t.Fatalf("expected active.json to be removed after a completed run, stat err = %v", err)
	}
}

func TestDeactivateRunFailedMarksStatusFailed(t *testing.T) {
	workdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workdir, ".tagteam"), 0o755); err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(workdir, ".tagteam", "runs", "run-1")
	activateRun(workdir, "run-1", runDir, ModeSolo)

	deactivateRun(workdir, "run-1", false)

	active, err := readActiveRun(workdir)
	if err != nil {
		t.Fatalf("readActiveRun() error = %v", err)
	}
	if active.Status != "failed" {
		t.Fatalf("status = %q, want failed", active.Status)
	}
	if active.Status == "running" {
		t.Fatal("active pointer must not be left running after a failed run")
	}
}

func TestFinalizeAndClearActiveRunIgnoreStalePointer(t *testing.T) {
	workdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workdir, ".tagteam"), 0o755); err != nil {
		t.Fatal(err)
	}
	activateRun(workdir, "newer-run", "irrelevant", ModeSolo)

	if err := finalizeActiveRun(workdir, "older-run", "failed"); err != nil {
		t.Fatalf("finalizeActiveRun() error = %v", err)
	}
	if err := clearActiveRun(workdir, "older-run"); err != nil {
		t.Fatalf("clearActiveRun() error = %v", err)
	}

	active, err := readActiveRun(workdir)
	if err != nil {
		t.Fatalf("expected newer run's pointer to remain: %v", err)
	}
	if active.RunID != "newer-run" || active.Status != "running" {
		t.Fatalf("stale run must not modify a newer run's pointer: %#v", active)
	}
}

func TestReadActiveRunMissingReturnsError(t *testing.T) {
	workdir := t.TempDir()
	if _, err := readActiveRun(workdir); err == nil {
		t.Fatal("expected error reading a missing active.json")
	}
}

func TestRunSolo_SuccessfulRunRemovesActivePointer(t *testing.T) {
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
		Prompt:  "add a feature",
		Workdir: repo,
		Mode:    ModeSolo,
		Coder:   RoleTarget{Adapter: "claude"},
		Rounds:  1,
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if final.RunID == "" {
		t.Fatal("expected a run id")
	}
	if _, err := os.Stat(activeRunPath(repo)); !os.IsNotExist(err) {
		t.Fatalf("expected active.json to be removed after a successful run, stat err = %v", err)
	}

	var latest LatestRun
	readJSONFile(t, statePathForWorkdir(repo, "latest.json"), &latest)
	if latest.RunID != final.RunID {
		t.Fatalf("latest run id = %q, want %q (existing latest/final behavior must still pass)", latest.RunID, final.RunID)
	}
}

// TestRunSolo_MidRunFailureSnapshotReportsFailedNotRunning is an end-to-end
// regression test: runSolo has no recovery defer to rewrite state.json on a
// mid-run adapter failure (unlike runLoop/Review), so BuildRunSnapshot must
// still surface the failure via active.json rather than showing the dead run
// as still "running" forever.
func TestRunSolo_MidRunFailureSnapshotReportsFailedNotRunning(t *testing.T) {
	installFakeBinaries(t, map[string]string{"claude": fakeClaudeInvokeFailScript})

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "hello\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	app := NewApp(DefaultConfig())
	final, err := app.Run(context.Background(), RunOptions{
		Prompt:  "add a feature",
		Workdir: repo,
		Mode:    ModeSolo,
		Coder:   RoleTarget{Adapter: "claude"},
		Rounds:  1,
		Timeout: 10 * time.Second,
	})
	if err == nil {
		t.Fatal("expected the solo coder adapter to fail")
	}
	if final.RunDir == "" {
		t.Fatal("expected a run dir for the failed solo run")
	}

	snapshot, err := BuildRunSnapshot(repo, final.RunDir)
	if err != nil {
		t.Fatalf("BuildRunSnapshot() error = %v", err)
	}
	if snapshot.Status == "running" {
		t.Fatal("snapshot must not report a dead solo run as still running")
	}
	if snapshot.Status != "failed" {
		t.Fatalf("status = %q, want failed", snapshot.Status)
	}
}

func TestRunSoloCancellationPersistsCancelledTerminalRun(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "adapter-started")
	installFakeBinaries(t, map[string]string{"claude": `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "1.0.0"
  exit 0
fi
printf x >> "$CANCEL_MARKER"
sleep 30
`})
	t.Setenv("CANCEL_MARKER", marker)

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "hello\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan struct {
		final FinalRun
		err   error
	}, 1)
	go func() {
		final, err := NewApp(DefaultConfig()).Run(ctx, RunOptions{
			Prompt:  "add a feature",
			Workdir: repo,
			Mode:    ModeSolo,
			Coder:   RoleTarget{Adapter: "claude"},
			Rounds:  1,
			Timeout: 10 * time.Second,
		})
		result <- struct {
			final FinalRun
			err   error
		}{final, err}
	}()

	deadline := time.Now().Add(3 * time.Second)
	for !fileExists(marker) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !fileExists(marker) {
		cancel()
		t.Fatal("adapter did not start before cancellation")
	}
	cancel()

	select {
	case got := <-result:
		if got.err == nil {
			t.Fatal("cancelled run unexpectedly succeeded")
		}
		if got.final.Status != RunStatusCancelled || got.final.Verdict != "cancelled" || got.final.BlockingReason != string(ReasonCancelled) {
			t.Fatalf("cancelled final = %#v", got.final)
		}
		if markerBytes, err := os.ReadFile(marker); err != nil || string(markerBytes) != "x" {
			t.Fatalf("adapter invocations = %q err=%v, want one", markerBytes, err)
		}
		var state RunState
		readJSONFile(t, filepath.Join(got.final.RunDir, "state.json"), &state)
		if state.Status != string(RunStatusCancelled) || state.BlockingReason != string(ReasonCancelled) {
			t.Fatalf("cancelled state = %#v", state)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled run did not finish promptly")
	}
}

func TestRunLoop_FailureAfterRunDirCreationDoesNotLeaveActiveRunning(t *testing.T) {
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

	active, readErr := readActiveRun(repo)
	if readErr != nil {
		t.Fatalf("expected active.json to still exist for postmortem: %v", readErr)
	}
	if active.Status == "running" {
		t.Fatal("active pointer must not be left running after a failed run")
	}
	if active.Status != "failed" {
		t.Fatalf("status = %q, want failed", active.Status)
	}
}
