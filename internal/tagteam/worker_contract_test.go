package tagteam

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCaptureWorktreeSnapshotDoesNotFollowDirectorySymlink(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "baseline\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	results := filepath.Join(repo, "pytest-of-user", "pytest-0")
	firstTarget := filepath.Join(results, "test_result0")
	secondTarget := filepath.Join(results, "test_result1")
	if err := os.MkdirAll(firstTarget, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(secondTarget, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(results, "test_resultcurrent")
	if err := os.Symlink(firstTarget, link); err != nil {
		t.Fatal(err)
	}

	before, err := captureWorktreeSnapshot(context.Background(), repo)
	if err != nil {
		t.Fatalf("captureWorktreeSnapshot() followed directory symlink: %v", err)
	}
	entry, ok := before["pytest-of-user/pytest-0/test_resultcurrent"]
	if !ok || !strings.HasPrefix(entry, "symlink:??:") {
		t.Fatalf("directory symlink fingerprint = %q, want symlink status", entry)
	}

	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secondTarget, link); err != nil {
		t.Fatal(err)
	}
	after, err := captureWorktreeSnapshot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if changed := worktreeDelta(before, after); len(changed) != 1 || changed[0] != "pytest-of-user/pytest-0/test_resultcurrent" {
		t.Fatalf("worktree delta = %#v, want retargeted symlink", changed)
	}
}
