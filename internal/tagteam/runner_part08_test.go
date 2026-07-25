package tagteam

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateWorkerResultExplainsIgnoredClaim(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, ".gitignore"), ".giles/\n")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "baseline\n")
	runGit(t, repo, "add", ".gitignore", "README.md")
	runGit(t, repo, "commit", "-m", "init")
	before, err := captureWorktreeSnapshot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	ignoredPath := filepath.Join(repo, ".giles", "feature-ledger", "entry.md")
	mustWriteFile(t, ignoredPath, "required local ledger\n")
	result := Result{Text: `{"schema_version":1,"status":"completed","summary":"wrote ledger","files_changed":[".giles/feature-ledger/entry.md"],"checks_run":[],"remaining_risks":[]}`}
	err = validateWorkerResultForRequest(context.Background(), Request{Workdir: repo, RequireWorkerContract: true}, &result, before)
	if err == nil || !IsOutputContractError(err) {
		t.Fatalf("error = %T %v, want output contract error", err, err)
	}
	for _, want := range []string{"ignored paths", ".giles/feature-ledger/entry.md", "git add -f"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ignored-path diagnostic missing %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "required local ledger") {
		t.Fatalf("ignored file contents leaked into diagnostic: %v", err)
	}
}

func TestValidateWorkerResultNormalizesUnchangedCumulativeClaims(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "previous.txt"), "baseline\n")
	mustWriteFile(t, filepath.Join(repo, "current.txt"), "baseline\n")
	runGit(t, repo, "add", "previous.txt", "current.txt")
	runGit(t, repo, "commit", "-m", "init")
	mustWriteFile(t, filepath.Join(repo, "previous.txt"), "changed in round one\n")
	before, err := captureWorktreeSnapshot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(repo, "current.txt"), "changed in round two\n")
	result := Result{Text: `{"schema_version":1,"status":"completed","summary":"fixed review","files_changed":["previous.txt","current.txt"],"checks_run":[],"remaining_risks":[]}`}
	if err := validateWorkerResultForRequest(context.Background(), Request{Workdir: repo, RequireWorkerContract: true}, &result, before); err != nil {
		t.Fatalf("validateWorkerResultForRequest() error = %v", err)
	}
	if result.Worker == nil || strings.Join(result.Worker.FilesChanged, ",") != "current.txt" {
		t.Fatalf("normalized worker = %#v", result.Worker)
	}
	if len(result.Worker.RemainingRisks) != 1 || !strings.Contains(result.Worker.RemainingRisks[0], "normalized files_changed") {
		t.Fatalf("normalization disclosure = %#v", result.Worker.RemainingRisks)
	}
}

func TestValidateWorkerResultIgnoresIndexOnlyTransitionForPreexistingEdit(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "existing.txt"), "baseline\n")
	runGit(t, repo, "add", "existing.txt")
	runGit(t, repo, "commit", "-m", "init")

	// This edit predates the invocation. Staging it is an index transition,
	// not a content change authored by the worker.
	mustWriteFile(t, filepath.Join(repo, "existing.txt"), "preexisting edit\n")
	before, err := captureWorktreeSnapshot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "existing.txt")

	result := Result{Text: `{"schema_version":1,"status":"completed","summary":"verified existing edit","files_changed":[],"checks_run":[],"remaining_risks":[]}`}
	if err := validateWorkerResultForRequest(context.Background(), Request{Workdir: repo, RequireWorkerContract: true}, &result, before); err != nil {
		t.Fatalf("validateWorkerResultForRequest() error = %v", err)
	}
	if result.Worker == nil || len(result.Worker.FilesChanged) != 0 {
		t.Fatalf("worker = %#v, want no content changes", result.Worker)
	}
}
