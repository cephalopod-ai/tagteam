package tagteam

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func autostashFixture(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "tracked.txt")
	runGit(t, repo, "commit", "-m", "init")
	return repo
}

func TestRestoreAutostashUsesObjectIdentityAcrossNewerStash(t *testing.T) {
	repo := autostashFixture(t)
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("original user change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	originalOID, err := gitAutostash(repo, "identity-test")
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(originalOID, "stash@{") {
		t.Fatalf("gitAutostash returned positional ref %q", originalOID)
	}

	if err := os.WriteFile(filepath.Join(repo, "newer.txt"), []byte("newer stash\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "stash", "push", "-u", "-m", "newer-external-stash")
	newerOID := strings.TrimSpace(runGit(t, repo, "rev-parse", "refs/stash^{commit}"))
	if newerOID == originalOID {
		t.Fatal("newer stash did not move refs/stash")
	}

	if err := restoreAutostash(repo, t.TempDir(), originalOID); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(repo, "tracked.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original user change\n" {
		t.Fatalf("restored tracked.txt = %q", data)
	}
	list := runGit(t, repo, "stash", "list", "--format=%H")
	if !strings.Contains(list, originalOID) {
		t.Fatalf("restored autostash recovery point was removed: %s", list)
	}
	if !strings.Contains(list, newerOID) {
		t.Fatalf("newer external stash was removed: %s", list)
	}
}

func TestRestoreAutostashConflictPreservesStashAndRecoveryArtifact(t *testing.T) {
	repo := autostashFixture(t)
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("original user change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stashOID, err := gitAutostash(repo, "conflict-test")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("agent change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	if err := restoreAutostash(repo, runDir, stashOID); err == nil {
		t.Fatal("conflicting autostash restore succeeded")
	}
	data, err := os.ReadFile(filepath.Join(repo, "tracked.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "agent change\n" {
		t.Fatalf("conflict overwrote current worktree: %q", data)
	}
	if ref, err := findStashRefByOID(repo, stashOID); err != nil || ref == "" {
		t.Fatalf("original stash not recoverable ref=%q err=%v", ref, err)
	}
	var recovery AutostashRecovery
	payload, err := os.ReadFile(filepath.Join(runDir, "autostash-recovery.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(payload, &recovery); err != nil {
		t.Fatal(err)
	}
	if recovery.StashOID != stashOID || recovery.Status != "apply_failed" || len(recovery.SafeCommands) == 0 {
		t.Fatalf("recovery artifact = %#v", recovery)
	}
}

func TestFinishPreflightCleanupCannotReturnCleanSuccess(t *testing.T) {
	repo := autostashFixture(t)
	runDir := t.TempDir()
	final := FinalRun{
		SchemaVersion: ArtifactSchemaVersion,
		RunID:         filepath.Base(runDir),
		RunDir:        runDir,
		Workdir:       repo,
		Mode:          ModeSolo,
		Status:        RunStatusPassed,
		Verdict:       "done",
	}
	var runErr error
	app := NewApp(DefaultConfig())
	app.finishPreflightCleanup(RunOptions{Workdir: repo, Mode: ModeSolo}, runDir, func(string) error {
		return errors.New("restore conflict")
	}, &final, &runErr)
	if runErr == nil || final.Status != RunStatusFailed || final.ExitCode == ExitSuccess || final.BlockingReason != string(ReasonAutostashRestore) {
		t.Fatalf("runErr=%v final=%#v", runErr, final)
	}
	persisted, present, err := readControlFinalOptional(runDir)
	if err != nil || !present {
		t.Fatalf("read persisted final present=%t err=%v", present, err)
	}
	if persisted.Status != RunStatusFailed || persisted.ExitCode == ExitSuccess {
		t.Fatalf("persisted final still claims success: %#v", persisted)
	}
}

func TestAutostashCleanupRestoresAfterInterruptedRun(t *testing.T) {
	repo := autostashFixture(t)
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("interrupted user change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, cleanup, err := preflight(RunOptions{Workdir: repo, Autostash: true}, "interrupted-run")
	if err != nil {
		t.Fatal(err)
	}
	if cleanup == nil {
		t.Fatal("autostash preflight returned no cleanup")
	}
	final := FinalRun{RunID: "interrupted-run", RunDir: t.TempDir(), Workdir: repo, Mode: ModeSolo, Status: RunStatusCancelled}
	runErr := context.Canceled
	NewApp(DefaultConfig()).finishPreflightCleanup(RunOptions{Workdir: repo, Mode: ModeSolo}, final.RunDir, cleanup, &final, &runErr)
	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("interrupted run error = %v", runErr)
	}
	data, err := os.ReadFile(filepath.Join(repo, "tracked.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "interrupted user change\n" {
		t.Fatalf("interrupted cleanup restored %q", data)
	}
}
