package tagteam

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteLiveProgressCapturesWorktreeDiff(t *testing.T) {
	repo := t.TempDir()
	mustRunProgressTestCommand(t, repo, "git", "init", "-q")
	mustRunProgressTestCommand(t, repo, "git", "config", "user.email", "tagteam@example.com")
	mustRunProgressTestCommand(t, repo, "git", "config", "user.name", "Tagteam Test")
	tracked := filepath.Join(repo, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRunProgressTestCommand(t, repo, "git", "add", "tracked.txt")
	mustRunProgressTestCommand(t, repo, "git", "commit", "-qm", "baseline")
	if err := os.WriteFile(tracked, []byte("before\nafter\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "new file.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".tagteam"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".tagteam", "ignored.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(repo, ".tagteam", "runs", "test")
	lastActivity := time.Now().Add(-2 * time.Minute)
	progress, err := writeLiveProgress(context.Background(), Request{Workdir: repo, RunDir: runDir, ProgressLastActivity: &lastActivity}, RoleCoder, "round 1 worker", time.Now().Add(-time.Second), "running")
	if err != nil {
		t.Fatal(err)
	}
	if progress.FilesChanged != 2 {
		t.Fatalf("files_changed = %d, want 2: %#v", progress.FilesChanged, progress.ChangedFiles)
	}
	if progress.Additions != 2 || progress.Deletions != 0 {
		t.Fatalf("numstat = +%d -%d, want +2 -0", progress.Additions, progress.Deletions)
	}
	if progress.DiffHash == "" || progress.DiffHash == sha256Sum(nil) {
		t.Fatalf("diff hash does not include untracked content: %q", progress.DiffHash)
	}
	if progress.NoProgressFor != "2m0s" {
		t.Fatalf("no_progress_for = %q, want 2m0s", progress.NoProgressFor)
	}
	data, err := os.ReadFile(filepath.Join(runDir, liveProgressArtifact))
	if err != nil {
		t.Fatal(err)
	}
	var persisted LiveProgress
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Phase != "round 1 worker" || persisted.Status != "running" {
		t.Fatalf("persisted progress = %#v", persisted)
	}
}

func TestWriteWaitingProgressPersistsQueueContext(t *testing.T) {
	runDir := t.TempDir()
	started := time.Now().Add(-2 * time.Second)
	if err := writeWaitingProgress(Request{RunDir: runDir, InvocationID: "invoke-1"}, RoleCoder, "round 1 coder", started, "claude", 4242); err != nil {
		t.Fatal(err)
	}
	var progress LiveProgress
	data, err := os.ReadFile(filepath.Join(runDir, liveProgressArtifact))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &progress); err != nil {
		t.Fatal(err)
	}
	if progress.Status != "waiting" || progress.WaitingFor != "claude" || progress.HolderPID != 4242 || progress.Elapsed == "" {
		t.Fatalf("waiting progress = %#v", progress)
	}
}

func TestRunAdapterPersistsLogicalProgressRole(t *testing.T) {
	repo := t.TempDir()
	mustRunProgressTestCommand(t, repo, "git", "init", "-q")
	mustRunProgressTestCommand(t, repo, "git", "config", "user.email", "tagteam@example.com")
	mustRunProgressTestCommand(t, repo, "git", "config", "user.name", "Tagteam Test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("baseline\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRunProgressTestCommand(t, repo, "git", "add", "README.md")
	mustRunProgressTestCommand(t, repo, "git", "commit", "-qm", "baseline")
	runDir := t.TempDir()
	adapter := fakeAdapter{
		build: func(role Role, req Request) (*CommandSpec, error) {
			return &CommandSpec{Argv: []string{"sh", "-c", "printf ok"}, Dir: repo, Output: req.OutputPath}, nil
		},
		parse: func(role Role, raw []byte) (Result, error) {
			return Result{Text: string(raw), Raw: raw}, nil
		},
	}
	_, err := NewApp(DefaultConfig()).runAdapter(context.Background(), adapter, RoleAdversary, Request{
		Context:      context.Background(),
		Workdir:      repo,
		RunDir:       runDir,
		OutputPath:   filepath.Join(runDir, "review.json"),
		Timeout:      10 * time.Second,
		Phase:        "round 1 supervisor",
		ProgressRole: RoleSupervisor,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	var progress LiveProgress
	data, err := os.ReadFile(filepath.Join(runDir, liveProgressArtifact))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &progress); err != nil {
		t.Fatal(err)
	}
	if progress.Role != RoleSupervisor || progress.Status != "completed" {
		t.Fatalf("logical progress role was not persisted: %#v", progress)
	}
}

func TestWritePreexistingWorktreeRecordsCumulativeBaseline(t *testing.T) {
	repo := t.TempDir()
	mustRunProgressTestCommand(t, repo, "git", "init", "-q")
	mustRunProgressTestCommand(t, repo, "git", "config", "user.email", "tagteam@example.com")
	mustRunProgressTestCommand(t, repo, "git", "config", "user.name", "Tagteam Test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("baseline\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRunProgressTestCommand(t, repo, "git", "add", "README.md")
	mustRunProgressTestCommand(t, repo, "git", "commit", "-qm", "baseline")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	if err := writePreexistingWorktree(context.Background(), repo, runDir, "baseline-sha"); err != nil {
		t.Fatal(err)
	}
	var artifact PreexistingWorktree
	data, err := os.ReadFile(filepath.Join(runDir, preexistingWorktreeArtifact))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &artifact); err != nil {
		t.Fatal(err)
	}
	if strings.Join(artifact.Files, ",") != "README.md,new.txt" || artifact.Baseline != "baseline-sha" {
		t.Fatalf("pre-existing worktree artifact = %#v", artifact)
	}
}

func TestWriteRedactedBytesRecreatesDeletedRunDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deleted", "run", "worker.md")
	if err := writeRedactedBytes(path, []byte("result\n"), nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "result\n" {
		t.Fatalf("output = %q", string(data))
	}
}

func TestWorkerPromptsProtectHostArtifacts(t *testing.T) {
	prompts := []string{
		workerSystemPrompt,
		BuildWorkerImplementPrompt("/repo", "ship it", "brief"),
		BuildWorkerFixPrompt(2, "ship it", "diff", Review{Verdict: "needs_changes"}),
	}
	for _, prompt := range prompts {
		if !strings.Contains(prompt, "Never modify or delete .tagteam") {
			t.Fatalf("worker prompt does not protect host artifacts:\n%s", prompt)
		}
	}
	contractPrompt := workerContractPrompt("implement")
	if !strings.Contains(contractPrompt, "must not modify files outside") || !strings.Contains(contractPrompt, "pnpm approve-builds") {
		t.Fatalf("worker contract does not prohibit mutating validation commands:\n%s", contractPrompt)
	}
}

func mustRunProgressTestCommand(t *testing.T, workdir, binary string, args ...string) {
	t.Helper()
	if _, err := runCommand(context.Background(), workdir, binary, args...); err != nil {
		t.Fatal(err)
	}
}
