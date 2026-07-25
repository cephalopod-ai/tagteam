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

func TestRunAdapterLiveScopeGuardRejectsEmptyScopeIntersection(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "baseline\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "baseline")

	allowed := allowedScopeForRound(
		RunOptions{AllowedPaths: []string{"docs/"}},
		&WorkPackage{AllowedScope: []string{"internal/"}},
	)
	if len(allowed) != 0 {
		t.Fatalf("allowed scope = %#v, want empty intersection", allowed)
	}
	adapter := fakeAdapter{
		build: func(role Role, req Request) (*CommandSpec, error) {
			return &CommandSpec{Argv: []string{"sh", "-c", "printf blocked > README.md; sleep 10"}, Dir: repo}, nil
		},
		parse: func(role Role, raw []byte) (Result, error) { return Result{Raw: raw}, nil },
	}
	_, err := NewApp(DefaultConfig()).runAdapter(context.Background(), adapter, RoleCoder, Request{
		Context:               context.Background(),
		Workdir:               repo,
		RunDir:                t.TempDir(),
		Timeout:               15 * time.Second,
		Phase:                 "empty scope intersection regression",
		ProgressRole:          RoleCoder,
		RequireWorkerContract: true,
		AllowedScope:          allowed,
		EnforceAllowedScope:   true,
	}, false)
	var violation *LiveScopeViolationError
	if !errors.As(err, &violation) {
		t.Fatalf("error = %T %v, want LiveScopeViolationError", err, err)
	}
	if len(violation.Paths) != 1 || violation.Paths[0] != "README.md" {
		t.Fatalf("violation paths = %#v", violation.Paths)
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
		EnforceAllowedScope:   true,
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

func TestSettledOutOfScopeDeltaPathsIgnoresTransientArtifact(t *testing.T) {
	before := worktreeSnapshot{}
	transient := worktreeSnapshot{"tests/fixtures/generated.tmp": "??:temporary"}
	pending := map[string]time.Time{}
	now := time.Now()

	if paths := settledOutOfScopeDeltaPaths(before, transient, []string{"README.md"}, pending, now); len(paths) != 0 {
		t.Fatalf("first observation paths = %#v, want none", paths)
	}
	if paths := settledOutOfScopeDeltaPaths(before, worktreeSnapshot{}, []string{"README.md"}, pending, now.Add(liveScopeGuardInterval)); len(paths) != 0 {
		t.Fatalf("removed transient paths = %#v, want none", paths)
	}
	if len(pending) != 0 {
		t.Fatalf("pending transient paths = %#v, want none", pending)
	}
}
