package tagteam

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func syncFixture(t *testing.T) (repo, peer, sourceBranch string) {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	runGit(t, root, "init", "--bare", remote)

	repo = filepath.Join(root, "repo")
	runGit(t, root, "init", repo)
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "baseline\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "baseline")
	sourceBranch = strings.TrimSpace(runGit(t, repo, "branch", "--show-current"))
	runGit(t, repo, "remote", "add", "origin", remote)
	runGit(t, repo, "push", "-u", "origin", sourceBranch)
	runGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/"+sourceBranch)

	peer = filepath.Join(root, "peer")
	runGit(t, root, "clone", remote, peer)
	runGit(t, peer, "config", "user.email", "peer@example.com")
	runGit(t, peer, "config", "user.name", "Peer User")
	return repo, peer, sourceBranch
}

func commitPeerChange(t *testing.T, peer, name string) string {
	t.Helper()
	mustWriteFile(t, filepath.Join(peer, name), "from peer\n")
	runGit(t, peer, "add", name)
	runGit(t, peer, "commit", "-m", "peer change")
	runGit(t, peer, "push")
	return strings.TrimSpace(runGit(t, peer, "rev-parse", "HEAD"))
}

func TestPreflightSyncCheckpointsDirtyWorktreeAndFastForwardsSource(t *testing.T) {
	repo, peer, sourceBranch := syncFixture(t)
	upstreamHead := commitPeerChange(t, peer, "upstream.txt")
	mustWriteFile(t, filepath.Join(repo, "local.txt"), "local work\n")

	baseline, cleanup, err := preflight(RunOptions{Workdir: repo, GitSafety: "sync"}, "sync-dirty")
	if err != nil {
		t.Fatalf("preflight() error = %v", err)
	}
	if cleanup != nil {
		t.Fatal("sync preflight must not schedule cleanup after checkpointing")
	}
	if branch := strings.TrimSpace(runGit(t, repo, "branch", "--show-current")); branch != "tagteam/sync-dirty" {
		t.Fatalf("branch = %q", branch)
	}
	if head := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD")); baseline != head {
		t.Fatalf("baseline = %q, want current HEAD %q", baseline, head)
	}
	if sourceHead := strings.TrimSpace(runGit(t, repo, "rev-parse", sourceBranch)); sourceHead != upstreamHead {
		t.Fatalf("source branch = %q, want fast-forwarded upstream %q", sourceHead, upstreamHead)
	}
	if status := strings.TrimSpace(runGit(t, repo, "status", "--porcelain")); status != "" {
		t.Fatalf("worktree remains dirty after sync preflight: %q", status)
	}
	if got := strings.TrimSpace(runGit(t, repo, "show", "HEAD:local.txt")); got != "local work" {
		t.Fatalf("checkpoint omitted local change: %q", got)
	}
	if got := strings.TrimSpace(runGit(t, repo, "show", "HEAD:upstream.txt")); got != "from peer" {
		t.Fatalf("run branch omitted upstream change: %q", got)
	}
}

func TestPreflightSyncRejectsDivergentSourceAndPreservesCheckpoint(t *testing.T) {
	repo, peer, sourceBranch := syncFixture(t)
	mustWriteFile(t, filepath.Join(repo, "local-commit.txt"), "local commit\n")
	runGit(t, repo, "add", "local-commit.txt")
	runGit(t, repo, "commit", "-m", "local commit")
	commitPeerChange(t, peer, "upstream.txt")
	mustWriteFile(t, filepath.Join(repo, "dirty.txt"), "checkpoint me\n")

	_, _, err := preflight(RunOptions{Workdir: repo, GitSafety: "sync"}, "sync-diverged")
	if err == nil || !strings.Contains(err.Error(), "sync preflight refuses non-fast-forward") {
		t.Fatalf("preflight() error = %v, want divergent-branch failure", err)
	}
	if branch := strings.TrimSpace(runGit(t, repo, "branch", "--show-current")); branch != sourceBranch {
		t.Fatalf("branch = %q, want source branch %q after failure", branch, sourceBranch)
	}
	if status := strings.TrimSpace(runGit(t, repo, "status", "--porcelain")); status != "" {
		t.Fatalf("source worktree remains dirty after failure: %q", status)
	}
	checkpoint := "tagteam/sync-diverged"
	if got := strings.TrimSpace(runGit(t, repo, "show", checkpoint+":dirty.txt")); got != "checkpoint me" {
		t.Fatalf("checkpoint branch did not preserve dirty work: %q", got)
	}
}

func TestPreflightSyncAbortsCheckpointRebaseConflict(t *testing.T) {
	repo, peer, sourceBranch := syncFixture(t)
	mustWriteFile(t, filepath.Join(peer, "README.md"), "upstream version\n")
	runGit(t, peer, "add", "README.md")
	runGit(t, peer, "commit", "-m", "upstream README")
	runGit(t, peer, "push")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "local version\n")

	_, _, err := preflight(RunOptions{Workdir: repo, GitSafety: "sync"}, "sync-conflict")
	if err == nil || !strings.Contains(err.Error(), "checkpoint \"tagteam/sync-conflict\" conflicts") {
		t.Fatalf("preflight() error = %v, want checkpoint rebase conflict", err)
	}
	if branch := strings.TrimSpace(runGit(t, repo, "branch", "--show-current")); branch != sourceBranch {
		t.Fatalf("branch = %q, want restored source branch %q", branch, sourceBranch)
	}
	if status := strings.TrimSpace(runGit(t, repo, "status", "--porcelain")); status != "" {
		t.Fatalf("source worktree remains dirty after rebase abort: %q", status)
	}
	if got := strings.TrimSpace(runGit(t, repo, "show", sourceBranch+":README.md")); got != "upstream version" {
		t.Fatalf("source branch did not retain fast-forwarded content: %q", got)
	}
	if got := strings.TrimSpace(runGit(t, repo, "show", "tagteam/sync-conflict:README.md")); got != "local version" {
		t.Fatalf("checkpoint branch did not preserve local content: %q", got)
	}
}

func TestPreflightSyncSupportsLocalReposWithoutUpstream(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "baseline\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "baseline")
	mustWriteFile(t, filepath.Join(repo, "local.txt"), "local work\n")

	_, _, err := preflight(RunOptions{Workdir: repo, GitSafety: "sync"}, "sync-local")
	if err != nil {
		t.Fatalf("preflight() error = %v", err)
	}
	if branch := strings.TrimSpace(runGit(t, repo, "branch", "--show-current")); branch != "tagteam/sync-local" {
		t.Fatalf("branch = %q", branch)
	}
	if got := strings.TrimSpace(runGit(t, repo, "show", "HEAD:local.txt")); got != "local work" {
		t.Fatalf("checkpoint omitted local change: %q", got)
	}
}

func TestResolveOptions_DefaultAndProfileGitSafety(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Profiles["branch-only"] = ProfileConfig{GitSafety: "branch"}

	defaults, err := ResolveOptions(cfg, nil, FlagInputs{Timeout: 15 * time.Minute}, map[string]bool{}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() default error = %v", err)
	}
	if defaults.GitSafety != "sync" {
		t.Fatalf("default git safety = %q, want sync", defaults.GitSafety)
	}
	profile, err := ResolveOptions(cfg, nil, FlagInputs{Profile: "branch-only", Timeout: 15 * time.Minute}, map[string]bool{}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() profile error = %v", err)
	}
	if profile.GitSafety != "branch" {
		t.Fatalf("profile git safety = %q, want branch", profile.GitSafety)
	}
}

func TestSanitizeUntrustedRepoConfigStripsProfileGitSafety(t *testing.T) {
	cfg := sanitizeUntrustedRepoConfig(Config{Profiles: map[string]ProfileConfig{
		"unsafe": {GitSafety: "sync"},
	}})
	if got := cfg.Profiles["unsafe"].GitSafety; got != "" {
		t.Fatalf("untrusted profile git safety = %q, want empty", got)
	}
}
