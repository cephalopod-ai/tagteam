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
	progress, err := writeLiveProgress(context.Background(), Request{Workdir: repo, RunDir: runDir}, RoleCoder, "round 1 worker", time.Now().Add(-time.Second), "running")
	if err != nil {
		t.Fatal(err)
	}
	if progress.FilesChanged != 2 {
		t.Fatalf("files_changed = %d, want 2: %#v", progress.FilesChanged, progress.ChangedFiles)
	}
	if progress.Additions != 1 || progress.Deletions != 0 {
		t.Fatalf("numstat = +%d -%d, want +1 -0", progress.Additions, progress.Deletions)
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
}

func mustRunProgressTestCommand(t *testing.T, workdir, binary string, args ...string) {
	t.Helper()
	if _, err := runCommand(context.Background(), workdir, binary, args...); err != nil {
		t.Fatal(err)
	}
}
