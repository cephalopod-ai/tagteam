package tagteam

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeterministicDiffOutputsSupportsEmptyBaseline(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	runGit(t, repo, "commit", "--allow-empty", "-m", "baseline")
	baseline := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))

	if err := os.WriteFile(filepath.Join(repo, "probe.md"), []byte("probe\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch, _, status, _, err := deterministicDiffOutputs(context.Background(), repo, baseline, filepath.Join(t.TempDir(), "review.index"))
	if err != nil {
		t.Fatalf("deterministicDiffOutputs() error = %v", err)
	}
	if !strings.Contains(string(patch), "diff --git a/probe.md b/probe.md") {
		t.Fatalf("patch missing untracked file:\n%s", patch)
	}
	if got := string(status); got != "A\x00probe.md\x00" {
		t.Fatalf("status = %q, want addition for probe.md", got)
	}
}
