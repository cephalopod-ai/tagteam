package tagteam

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExecutionPlanStatusTransitions(t *testing.T) {
	workPlan := WorkPlan{
		Summary: "two packages",
		Packages: []WorkPackage{
			{ID: "P1", Title: "First", Goal: "Do first", Acceptance: []string{"first ok"}, Validation: []string{"go test ./..."}},
			{ID: "P2", Title: "Second", Goal: "Do second", Acceptance: []string{"second ok"}, Validation: []string{"go test ./..."}},
		},
		SelectedPackage: "P1",
	}
	plan := newExecutionPlanFromWorkPlan("run-1", ModeSupervisor, workPlan, "supervisor-initial")
	if len(plan.Items) != 2 {
		t.Fatalf("items = %#v", plan.Items)
	}
	if plan.Items[0].Status != PlanStatusInProgress || plan.Items[1].Status != PlanStatusPending {
		t.Fatalf("initial statuses = %#v", plan.Items)
	}
	setPlanItemStatus(plan, "P1", PlanStatusPassed, "supervisor", "review passed")
	deferRemainingPlanItems(plan, "P1", "runner", "not auto-running remaining work")
	finalizeExecutionPlan(plan, ExitSuccess)
	summary := summarizeExecutionPlan("/tmp/run", plan)
	if plan.Status != "passed" || summary.Passed != 1 || summary.Deferred != 1 {
		t.Fatalf("plan=%#v summary=%#v", plan, summary)
	}
}

func TestPreflightBranchModeCreatesBranch(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	_, cleanup, err := preflight(RunOptions{Workdir: repo, GitSafety: "branch"}, "2026-07-07T120000Z")
	if err != nil {
		t.Fatalf("preflight() error = %v", err)
	}
	if cleanup != nil {
		defer func() {
			if err := cleanup(""); err != nil {
				t.Errorf("preflight cleanup: %v", err)
			}
		}()
	}
	branch := strings.TrimSpace(runGit(t, repo, "rev-parse", "--abbrev-ref", "HEAD"))
	if branch != "tagteam/2026-07-07T120000Z" {
		t.Fatalf("branch = %q", branch)
	}
}

func TestPreflightAllowDirtyCreatesCheckpointBranch(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")
	original := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	baseline, cleanup, err := preflight(RunOptions{Workdir: repo, AllowDirty: true}, "2026-07-11T120000Z")
	if err != nil {
		t.Fatalf("preflight() error = %v", err)
	}
	if cleanup != nil {
		t.Fatalf("allow-dirty checkpoint must persist its branch")
	}
	branch := strings.TrimSpace(runGit(t, repo, "rev-parse", "--abbrev-ref", "HEAD"))
	if branch != "tagteam/2026-07-11T120000Z" {
		t.Fatalf("branch = %q", branch)
	}
	head := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	if baseline != head {
		t.Fatalf("baseline = %q, HEAD = %q", baseline, head)
	}
	if parent := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD^")); parent != original {
		t.Fatalf("checkpoint parent = %q, want %q", parent, original)
	}
	if status := strings.TrimSpace(runGit(t, repo, "status", "--porcelain")); status != "" {
		t.Fatalf("checkpoint worktree is dirty: %q", status)
	}
	if got := strings.TrimSpace(runGit(t, repo, "show", "HEAD:new.txt")); got != "new" {
		t.Fatalf("checkpoint omitted untracked file: %q", got)
	}
}

func TestPreflightDryRunAllowDirtyDoesNotCheckpoint(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "baseline\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	originalBranch := strings.TrimSpace(runGit(t, repo, "rev-parse", "--abbrev-ref", "HEAD"))
	originalHead := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	mustWriteFile(t, filepath.Join(repo, "README.md"), "changed\n")
	mustWriteFile(t, filepath.Join(repo, "untracked.txt"), "untracked\n")
	originalStatus := runGit(t, repo, "status", "--porcelain")

	baseline, cleanup, err := preflight(RunOptions{Workdir: repo, AllowDirty: true, DryRun: true}, "2026-07-24T000000Z")
	if err != nil {
		t.Fatalf("preflight() error = %v", err)
	}
	if cleanup != nil {
		t.Fatal("dry-run preflight must not schedule mutating cleanup")
	}
	if baseline != originalHead {
		t.Fatalf("baseline = %q, want %q", baseline, originalHead)
	}
	if branch := strings.TrimSpace(runGit(t, repo, "rev-parse", "--abbrev-ref", "HEAD")); branch != originalBranch {
		t.Fatalf("dry-run changed branch to %q, want %q", branch, originalBranch)
	}
	if head := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD")); head != originalHead {
		t.Fatalf("dry-run changed HEAD to %q, want %q", head, originalHead)
	}
	if status := runGit(t, repo, "status", "--porcelain"); status != originalStatus {
		t.Fatalf("dry-run changed worktree status to %q, want %q", status, originalStatus)
	}
}

func TestPreflightAllowDirtyRejectsWhitespaceInvalidCheckpoint(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "baseline\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "changed\n\n")

	_, _, err := preflight(RunOptions{Workdir: repo, AllowDirty: true}, "2026-07-23T000000Z")
	if err == nil || !strings.Contains(err.Error(), "validate dirty-worktree checkpoint") {
		t.Fatalf("preflight() error = %v, want checkpoint validation failure", err)
	}
}

func TestDeterministicDiffIgnoresTagteamRunDirButIncludesUntrackedFiles(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".tagteam/\ntagteam\ndocs/logs/session/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "internal", "tagteam"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "internal", "tagteam", "tracked.go"), []byte("package tagteam\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-f", ".gitignore", "README.md", "internal/tagteam/tracked.go")
	runGit(t, repo, "commit", "-m", "init")
	baseline := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))

	if err := os.MkdirAll(filepath.Join(repo, ".tagteam", "runs", "test"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".tagteam", "runs", "test", "ignored.txt"), []byte("ignore me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "internal", "tagteam", "tracked.go"), []byte("package tagteam\n\nconst changed = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "notes.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "staged.txt"), []byte("already staged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ignoredStagedPath := filepath.Join(repo, "docs", "logs", "session", "072026", "entry.md")
	if err := os.MkdirAll(filepath.Dir(ignoredStagedPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ignoredStagedPath, []byte("durable session log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "staged.txt")
	runGit(t, repo, "add", "-f", "docs/logs/session/072026/entry.md")

	patch, _, _, _, err := deterministicDiffOutputs(context.Background(), repo, baseline, filepath.Join(repo, ".tagteam", "tmp.index"))
	if err != nil {
		t.Fatalf("deterministicDiffOutputs() error = %v", err)
	}
	text := string(patch)
	if !strings.Contains(text, "diff --git a/README.md b/README.md") {
		t.Fatalf("patch missing README change:\n%s", text)
	}
	if !strings.Contains(text, "diff --git a/notes.txt b/notes.txt") {
		t.Fatalf("patch missing untracked file:\n%s", text)
	}
	if !strings.Contains(text, "diff --git a/staged.txt b/staged.txt") {
		t.Fatalf("patch missing staged addition:\n%s", text)
	}
	if !strings.Contains(text, "diff --git a/docs/logs/session/072026/entry.md b/docs/logs/session/072026/entry.md") {
		t.Fatalf("patch missing explicitly staged ignored addition:\n%s", text)
	}
	if !strings.Contains(text, "diff --git a/internal/tagteam/tracked.go b/internal/tagteam/tracked.go") {
		t.Fatalf("patch missing tracked ignored-path change:\n%s", text)
	}
	if strings.Contains(text, ".tagteam") {
		t.Fatalf("patch should not include .tagteam artifacts:\n%s", text)
	}
}

func TestRunAdversaryDoesNotRetryInvocationFailures(t *testing.T) {
	app := NewApp(DefaultConfig())
	opts := RunOptions{
		Workdir:   t.TempDir(),
		Adversary: RoleTarget{Adapter: "missing"},
		Timeout:   time.Second,
	}
	_, _, _, err := app.runAdversary(context.Background(), opts, 1, opts.Workdir, filepath.Join(opts.Workdir, "schema.json"), "prompt", "HEAD", "diff", filepath.Join(opts.Workdir, "diff.patch"), "", "", nil, RelayContext{}, "", nil)
	if err == nil {
		t.Fatal("expected error")
	}
}
