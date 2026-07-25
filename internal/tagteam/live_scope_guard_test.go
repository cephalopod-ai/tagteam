package tagteam

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAllowedScopeForRoundIntersectsOperatorAndPackage(t *testing.T) {
	opts := RunOptions{AllowedPaths: []string{"docs/", "README.md"}}
	selected := &WorkPackage{AllowedScope: []string{"docs/guide/", "README.md", "internal/"}}
	got := allowedScopeForRound(opts, selected)
	want := []string{"README.md", "docs/guide/"}
	if len(got) != len(want) {
		t.Fatalf("scope = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("scope = %q, want %q", got, want)
		}
	}
}

func TestRunAdapterLiveScopeGuardCancelsOutOfScopeEditor(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "baseline\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "baseline")

	adapter := fakeAdapter{
		build: func(role Role, req Request) (*CommandSpec, error) {
			return &CommandSpec{Argv: []string{"sh", "-c", "printf unsafe > outside.md; sleep 10"}, Dir: repo}, nil
		},
		parse: func(role Role, raw []byte) (Result, error) { return Result{Raw: raw}, nil },
	}
	started := time.Now()
	_, err := NewApp(DefaultConfig()).runAdapter(context.Background(), adapter, RoleCoder, Request{
		Context:               context.Background(),
		Workdir:               repo,
		RunDir:                t.TempDir(),
		Timeout:               15 * time.Second,
		Phase:                 "scope guard regression",
		ProgressRole:          RoleCoder,
		RequireWorkerContract: true,
		AllowedScope:          []string{"README.md"},
	}, false)
	var violation *LiveScopeViolationError
	if !errors.As(err, &violation) {
		t.Fatalf("error = %T %v, want LiveScopeViolationError", err, err)
	}
	if len(violation.Paths) != 1 || violation.Paths[0] != "outside.md" {
		t.Fatalf("violation paths = %#v", violation.Paths)
	}
	if elapsed := time.Since(started); elapsed >= 5*time.Second {
		t.Fatalf("scope guard took %s, want prompt cancellation", elapsed)
	}
	if _, statErr := os.Stat(filepath.Join(repo, "outside.md")); statErr != nil {
		t.Fatalf("partial diff should remain for review: %v", statErr)
	}
}
