package tagteam

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveControlRepositoryAcceptsSymlinkAliasAndSubdirectory(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	canonical, err := resolveControlRepository(repo)
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "repo-alias")
	if err := os.Symlink(repo, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	viaAlias, err := resolveControlRepository(alias)
	if err != nil {
		t.Fatal(err)
	}
	if viaAlias.CanonicalRoot != canonical.CanonicalRoot || viaAlias.RepoID != canonical.RepoID {
		t.Fatalf("symlink alias repository = %#v, want %#v", viaAlias, canonical)
	}
	sub := filepath.Join(repo, "nested")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	viaSub, err := resolveControlRepository(sub)
	if err != nil {
		t.Fatal(err)
	}
	if viaSub.CanonicalRoot != canonical.CanonicalRoot || viaSub.RepoID != canonical.RepoID {
		t.Fatalf("subdirectory repository = %#v, want %#v", viaSub, canonical)
	}
}

func TestNormalizeControlLaunchRejectsHostileAndEscapingScopes(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	if err := os.MkdirAll(filepath.Join(repo, "internal", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repo, "escape-link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Join(repo, "internal"), filepath.Join(repo, "internal-alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(repo, "missing-target"), filepath.Join(repo, "broken-link")); err != nil {
		t.Fatal(err)
	}
	// Nested parent symlink that stays internal.
	if err := os.MkdirAll(filepath.Join(repo, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(repo, "internal"), filepath.Join(repo, "nested", "up")); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		paths   []string
		wantErr string
	}{
		{name: "absolute", paths: []string{"/tmp"}, wantErr: "repo-relative"},
		{name: "traversal", paths: []string{"../outside"}, wantErr: "parent traversal"},
		{name: "glob", paths: []string{"internal/*"}, wantErr: "glob"},
		{name: "backslash", paths: []string{`internal\pkg`}, wantErr: "path separator"},
		{name: "lexical duplicate", paths: []string{"./internal/", "internal/"}, wantErr: "duplicates"},
		{name: "real-path duplicate", paths: []string{"internal/", "internal-alias/"}, wantErr: "real-path resolution"},
		{name: "escaping symlink", paths: []string{"escape-link/"}, wantErr: "escapes"},
		{name: "broken symlink", paths: []string{"broken-link"}, wantErr: "broken symlink"},
	}
	// Root alias that resolves to the repository top-level must collide with ".".
	if err := os.Symlink(repo, filepath.Join(repo, "root-alias")); err != nil {
		t.Fatal(err)
	}
	rootDup := controlLaunchFixture(t, repo)
	rootDup.AllowedPaths = []string{".", "root-alias/"}
	if _, err := NormalizeControlLaunch(rootDup); err == nil || !strings.Contains(err.Error(), "real-path resolution") {
		t.Fatalf("root alias duplicate error = %v, want real-path resolution", err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := controlLaunchFixture(t, repo)
			spec.AllowedPaths = tc.paths
			_, err := NormalizeControlLaunch(spec)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, tc.wantErr)
			}
		})
	}

	// Real-path duplicates via nested parent + alias are rejected.
	spec := controlLaunchFixture(t, repo)
	spec.AllowedPaths = []string{"internal-alias/", "nested/up/"}
	if _, err := NormalizeControlLaunch(spec); err == nil || !strings.Contains(err.Error(), "real-path resolution") {
		t.Fatalf("nested/alias duplicate error = %v", err)
	}
	spec.AllowedPaths = []string{"internal-alias/"}
	normalized, err := NormalizeControlLaunch(spec)
	if err != nil {
		t.Fatal(err)
	}
	if got := normalized.AllowedPaths[0]; got != "internal/" {
		t.Fatalf("internal alias normalized to %q, want internal/", got)
	}
	spec.AllowedPaths = []string{"nested/up/pkg/"}
	normalized, err = NormalizeControlLaunch(spec)
	if err != nil {
		t.Fatal(err)
	}
	if got := normalized.AllowedPaths[0]; got != "internal/pkg/" {
		t.Fatalf("nested parent scope = %q, want internal/pkg/", got)
	}
}

func TestNormalizeControlLaunchRejectsMismatchedRepoID(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	spec := controlLaunchFixture(t, repo)
	spec.Repository.RepoID = "not-the-derived-id"
	if _, err := NormalizeControlLaunch(spec); err == nil || !strings.Contains(err.Error(), "repo_id does not match") {
		t.Fatalf("error = %v", err)
	}
}

func TestControlApprovalInvalidatesWhenCanonicalScopeChanges(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	if err := os.MkdirAll(filepath.Join(repo, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(repo, "internal"), filepath.Join(repo, "alias")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	spec := controlLaunchFixture(t, repo)
	spec.AllowedPaths = []string{"alias/"}
	first, err := ControlActionDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	// Retarget the alias to a different in-repo directory; the approved
	// canonical scope (and thus the action digest) must change.
	if err := os.Remove(filepath.Join(repo, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "other"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(repo, "other"), filepath.Join(repo, "alias")); err != nil {
		t.Fatal(err)
	}
	second, err := ControlActionDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("canonical action digest did not change when alias target changed")
	}
}

func TestResolveControlRunDirectoryRejectsEscapingRunSymlink(t *testing.T) {
	service, runID, runDir := controlServiceFixture(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Replace the real run directory with a symlink to an external path.
	if err := os.RemoveAll(runDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, runDir); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, _, err := resolveControlRunDirectory(service.RepositoryRoot, service.StateRoot, runID); err == nil || !strings.Contains(err.Error(), "escapes the resolved state root") {
		t.Fatalf("escaping run dir error = %v", err)
	}
	// Prove Status does not read the external secret via Plan/Findings.
	if _, err := service.Plan(runID, "", 10); err == nil {
		t.Fatal("plan unexpectedly succeeded through escaping run symlink")
	}
	// Internal symlink under runs root remains usable when target stays inside.
	if err := os.Remove(runDir); err != nil {
		t.Fatal(err)
	}
	realRun := runDir + "-real"
	if err := os.MkdirAll(realRun, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realRun, runDir); err != nil {
		t.Fatal(err)
	}
	resolved, _, err := resolveControlRunDirectory(service.RepositoryRoot, service.StateRoot, runID)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != realRun && resolved != filepath.Clean(realRun) {
		// EvalSymlinks may return the cleaned real path.
		canonical, _ := filepath.EvalSymlinks(realRun)
		if resolved != canonical {
			t.Fatalf("internal run symlink resolved to %q, want %q", resolved, realRun)
		}
	}
}

func TestControlCancelRejectsEscapingArtifactSymlink(t *testing.T) {
	repo, baseline := createResumeFixtureRepo(t)
	stateRoot := t.TempDir()
	service := ControlService{RepositoryRoot: repo, StateRoot: stateRoot, ProducerVersion: "test"}
	runtime := NewControlRuntime(service, DefaultConfig(), nil)
	repository, err := resolveControlRepository(repo)
	if err != nil {
		t.Fatal(err)
	}
	runID := "cancel-symlink-escape"
	runDir, err := createRunDir(repo, stateRoot, runID)
	if err != nil {
		t.Fatal(err)
	}
	writeResumeFixture(t, runDir, runID, repo, baseline, RunStatusRunning)
	if err := os.Remove(filepath.Join(runDir, "final.json")); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "secret-final.json")
	if err := os.WriteFile(sentinel, []byte(`{"status":"external_sentinel","run_id":"leaked"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	finalPath := filepath.Join(runDir, "final.json")
	if err := os.Symlink(sentinel, finalPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	locator, err := resolveStateLocator(repo, stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := controlCancelTarget(locator, repository.CanonicalRoot, runID); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("controlCancelTarget error = %v, want escapes", err)
	}
	request := ControlCancelRequest{SchemaVersion: ControlContractVersion, Repository: repository, RunID: runID}
	request.Approval = validCancelApproval(t, request, "cancel-escape-final")
	if _, err := runtime.Cancel(context.Background(), request); err == nil {
		t.Fatal("Cancel accepted escaping final.json symlink")
	}
	after, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after), "external_sentinel") {
		t.Fatalf("external sentinel was modified: %s", after)
	}
}

func TestControlCancelRejectsEscapingStateAndLockSymlinks(t *testing.T) {
	repo, baseline := createResumeFixtureRepo(t)
	stateRoot := t.TempDir()
	service := ControlService{RepositoryRoot: repo, StateRoot: stateRoot, ProducerVersion: "test"}
	runtime := NewControlRuntime(service, DefaultConfig(), nil)
	repository, err := resolveControlRepository(repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, artifact := range []string{"state.json", "run.lock"} {
		t.Run(artifact, func(t *testing.T) {
			runID := "cancel-escape-" + strings.ReplaceAll(artifact, ".", "-")
			runDir, err := createRunDir(repo, stateRoot, runID)
			if err != nil {
				t.Fatal(err)
			}
			writeResumeFixture(t, runDir, runID, repo, baseline, RunStatusRunning)
			if err := os.Remove(filepath.Join(runDir, "final.json")); err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			sentinel := filepath.Join(outside, "secret-"+artifact)
			if err := os.WriteFile(sentinel, []byte(`{"pid":1,"status":"external"}`), 0o644); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(runDir, artifact)
			if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if err := os.Symlink(sentinel, target); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			request := ControlCancelRequest{SchemaVersion: ControlContractVersion, Repository: repository, RunID: runID}
			request.Approval = validCancelApproval(t, request, "cancel-escape-"+artifact)
			if _, err := runtime.Cancel(context.Background(), request); err == nil {
				t.Fatalf("Cancel accepted escaping %s symlink", artifact)
			}
			after, err := os.ReadFile(sentinel)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(after), "external") {
				t.Fatalf("external sentinel for %s was modified: %s", artifact, after)
			}
		})
	}
}

func TestControlCancelRejectsEscapingCancelRequestWrite(t *testing.T) {
	repo, baseline := createResumeFixtureRepo(t)
	stateRoot := t.TempDir()
	service := ControlService{RepositoryRoot: repo, StateRoot: stateRoot, ProducerVersion: "test"}
	runtime := NewControlRuntime(service, DefaultConfig(), nil)
	repository, err := resolveControlRepository(repo)
	if err != nil {
		t.Fatal(err)
	}
	runID := "cancel-request-escape"
	runDir, err := createRunDir(repo, stateRoot, runID)
	if err != nil {
		t.Fatal(err)
	}
	writeResumeFixture(t, runDir, runID, repo, baseline, RunStatusRunning)
	if err := os.Remove(filepath.Join(runDir, "final.json")); err != nil {
		t.Fatal(err)
	}
	// Stale owner so cancel would try to persist status after writing request.
	if err := writeJSONDurable(filepath.Join(runDir, "run.lock"), runLockRecord{PID: os.Getpid() + 100000, CreatedAt: time.Now().UTC()}, true, true); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "cancel-request.json")
	if err := os.WriteFile(sentinel, []byte(`{"status":"external_cancel"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cancelPath := filepath.Join(runDir, controlCancelRequestName)
	if err := os.Symlink(sentinel, cancelPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	request := ControlCancelRequest{SchemaVersion: ControlContractVersion, Repository: repository, RunID: runID}
	request.Approval = validCancelApproval(t, request, "cancel-request-escape")
	if _, err := runtime.Cancel(context.Background(), request); err == nil {
		t.Fatal("Cancel wrote through escaping cancel-request symlink")
	}
	after, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after), "external_cancel") {
		t.Fatalf("external cancel-request sentinel modified: %s", after)
	}
}

func TestOptionsForLaunchRejectsRetargetedApprovedScopeSymlink(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	if err := os.MkdirAll(filepath.Join(repo, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "other"), 0o755); err != nil {
		t.Fatal(err)
	}
	scopeLink := filepath.Join(repo, "internal")
	// Prepare with a real internal/ directory, then replace it with a symlink
	// to other/ after approval binds the canonical scope "internal/".
	spec := controlLaunchFixture(t, repo)
	spec.AllowedPaths = []string{"internal/"}
	normalized, err := NormalizeControlLaunch(spec)
	if err != nil {
		t.Fatal(err)
	}
	if got := normalized.AllowedPaths[0]; got != "internal/" {
		t.Fatalf("approved scope = %q", got)
	}
	cfg := DefaultConfig()
	cfg.TestPresets = map[string]TestPresetConfig{"go-test": {Command: "go test ./..."}}
	runtime := NewControlRuntime(ControlService{RepositoryRoot: repo, StateRoot: t.TempDir(), ProducerVersion: "test"}, cfg, nil)
	if _, err := runtime.optionsForLaunch(normalized); err != nil {
		t.Fatalf("pre-retarget optionsForLaunch: %v", err)
	}
	if err := os.Remove(scopeLink); err != nil {
		// internal may be non-empty; remove contents then the dir.
		if err := os.RemoveAll(scopeLink); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(repo, "other"), scopeLink); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := runtime.optionsForLaunch(normalized); err == nil {
		t.Fatal("optionsForLaunch accepted retargeted approved scope symlink")
	}
}

func TestControlCancelRejectsRunDirectoryReplacementBeforeRequestWrite(t *testing.T) {
	repo, baseline := createResumeFixtureRepo(t)
	stateRoot := t.TempDir()
	service := ControlService{RepositoryRoot: repo, StateRoot: stateRoot, ProducerVersion: "test"}
	runtime := NewControlRuntime(service, DefaultConfig(), nil)
	repository, err := resolveControlRepository(repo)
	if err != nil {
		t.Fatal(err)
	}
	runID := "cancel-rundir-replace"
	runDir, err := createRunDir(repo, stateRoot, runID)
	if err != nil {
		t.Fatal(err)
	}
	writeResumeFixture(t, runDir, runID, repo, baseline, RunStatusRunning)
	if err := os.Remove(filepath.Join(runDir, "final.json")); err != nil {
		t.Fatal(err)
	}
	// Stale owner so cancel would attempt to write cancel-request then status.
	if err := writeJSONDurable(filepath.Join(runDir, "run.lock"), runLockRecord{PID: os.Getpid() + 100000, CreatedAt: time.Now().UTC()}, true, true); err != nil {
		t.Fatal(err)
	}
	// Prove the target is initially cancelable.
	locator, err := resolveStateLocator(repo, stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := controlCancelTarget(locator, repository.CanonicalRoot, runID); err != nil {
		t.Fatalf("precondition controlCancelTarget: %v", err)
	}
	outside := t.TempDir()
	externalRun := filepath.Join(outside, "external-run")
	if err := os.MkdirAll(externalRun, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(externalRun, "sentinel.txt"), []byte("external_cancel_rundir"), 0o644); err != nil {
		t.Fatal(err)
	}
	backup := runDir + ".bak"
	if err := os.Rename(runDir, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(externalRun, runDir); err != nil {
		_ = os.Rename(backup, runDir)
		t.Skipf("symlinks unavailable: %v", err)
	}
	request := ControlCancelRequest{SchemaVersion: ControlContractVersion, Repository: repository, RunID: runID}
	request.Approval = validCancelApproval(t, request, "cancel-rundir-replace")
	if _, err := runtime.Cancel(context.Background(), request); err == nil {
		t.Fatal("Cancel accepted run directory replaced with external symlink")
	}
	// No external cancel-request.json may be written or read.
	if _, err := os.Stat(filepath.Join(externalRun, controlCancelRequestName)); !os.IsNotExist(err) {
		t.Fatalf("external cancel-request written: %v", err)
	}
	sentinel, err := os.ReadFile(filepath.Join(externalRun, "sentinel.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(sentinel) != "external_cancel_rundir" {
		t.Fatalf("external run dir modified: %s", sentinel)
	}
	// Direct IO helper must also refuse the replaced directory.
	if _, _, _, err := resolveControlCancelIO(repo, stateRoot, runID); err == nil {
		t.Fatal("resolveControlCancelIO accepted escaping run directory")
	}
}

func TestRevalidateControlAllowedPathsDetectsMutation(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	if err := os.MkdirAll(filepath.Join(repo, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(repo, "internal"), filepath.Join(repo, "scope")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	allowed, err := canonicalizeControlAllowedPaths(repo, []string{"scope/"})
	if err != nil {
		t.Fatal(err)
	}
	if err := revalidateControlAllowedPaths(repo, allowed); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repo, "scope")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "other"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(repo, "other"), filepath.Join(repo, "scope")); err != nil {
		t.Fatal(err)
	}
	// Canonical list was "internal/"; revalidation of that list still passes.
	if err := revalidateControlAllowedPaths(repo, allowed); err != nil {
		t.Fatal(err)
	}
	// Pre-canonical alias input no longer matches after the target moves.
	if err := revalidateControlAllowedPaths(repo, []string{"scope/"}); err == nil {
		t.Fatal("expected revalidation failure for mutated alias input")
	}
}

func TestEnsureCanonicalRunsRootRejectsEscapingRunsAndRepoStateSymlinks(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	stateRoot := t.TempDir()
	locator, err := resolveStateLocator(repo, stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	// Fresh locator: missing runs is fine and stays under the host state root.
	resolved, err := ensureCanonicalRunsRoot(locator)
	if err != nil {
		t.Fatal(err)
	}
	canonicalState, err := canonicalPath(stateRoot, false)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != canonicalState && !pathWithin(canonicalState, resolved) {
		t.Fatalf("expected runs root under state root %q, got %q", canonicalState, resolved)
	}
	if err := locator.Prepare(); err != nil {
		t.Fatal(err)
	}
	okRoot, err := ensureCanonicalRunsRoot(locator)
	if err != nil {
		t.Fatal(err)
	}
	if okRoot != canonicalState && !pathWithin(canonicalState, okRoot) {
		t.Fatalf("prepared runs root %q not under state root %q", okRoot, canonicalState)
	}

	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "sentinel.txt"), []byte("external_runs_root"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Replace runs with an escaping symlink.
	if err := os.RemoveAll(locator.RunsRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, locator.RunsRoot); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := ensureCanonicalRunsRoot(locator); err == nil {
		t.Fatal("ensureCanonicalRunsRoot accepted escaping runs symlink")
	}
	// Start must also refuse before creating a run under the redirected root.
	service := ControlService{RepositoryRoot: repo, StateRoot: stateRoot, ProducerVersion: "test"}
	runtime := NewControlRuntime(service, DefaultConfig(), nil)
	startReq := controlStartFixture(t, repo)
	startReq.IdempotencyKey = "escape-runs-start"
	startReq.Approval.Nonce = "escape-runs-start"
	if digest, derr := ControlStartActionDigest(startReq); derr != nil {
		t.Fatal(derr)
	} else {
		startReq.Approval.ActionDigest = digest
	}
	if _, err := runtime.Start(context.Background(), startReq); err == nil {
		t.Fatal("Start accepted escaping runs root symlink")
	}
	// No external run directories or artifacts may appear.
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != "sentinel.txt" {
			t.Fatalf("external runs root gained entry %q", entry.Name())
		}
	}
	sentinel, err := os.ReadFile(filepath.Join(outside, "sentinel.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(sentinel) != "external_runs_root" {
		t.Fatalf("external sentinel modified: %s", sentinel)
	}

	// Restore a real runs dir, then replace the repo-id parent with an escape.
	if err := os.Remove(locator.RunsRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(locator.RunsRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	outsideRepo := t.TempDir()
	if err := os.WriteFile(filepath.Join(outsideRepo, "sentinel.txt"), []byte("external_repo_state"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(locator.RepoRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideRepo, locator.RepoRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureCanonicalRunsRoot(locator); err == nil {
		t.Fatal("ensureCanonicalRunsRoot accepted escaping repo-state symlink")
	}
	if _, _, err := resolveControlRunDirectory(repo, stateRoot, "any-run"); err == nil {
		t.Fatal("resolveControlRunDirectory accepted escaping repo-state symlink")
	}
	startReq.IdempotencyKey = "escape-repo-state-start"
	startReq.Approval.Nonce = "escape-repo-state-start"
	// Recompute digest after idempotency key change.
	if digest, derr := ControlStartActionDigest(startReq); derr != nil {
		t.Fatal(derr)
	} else {
		startReq.Approval.ActionDigest = digest
	}
	if _, err := runtime.Start(context.Background(), startReq); err == nil {
		t.Fatal("Start accepted escaping repo-state symlink")
	}
	repoEntries, err := os.ReadDir(outsideRepo)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range repoEntries {
		if entry.Name() != "sentinel.txt" {
			t.Fatalf("external repo state gained entry %q", entry.Name())
		}
	}
}

func TestControlLifecycleRejectsEscapingRunsRootForResumeAndCancel(t *testing.T) {
	repo, baseline := createResumeFixtureRepo(t)
	stateRoot := t.TempDir()
	service := ControlService{RepositoryRoot: repo, StateRoot: stateRoot, ProducerVersion: "test"}
	runtime := NewControlRuntime(service, DefaultConfig(), nil)
	repository, err := resolveControlRepository(repo)
	if err != nil {
		t.Fatal(err)
	}
	runID := "runs-root-escape-lifecycle"
	runDir, err := createRunDir(repo, stateRoot, runID)
	if err != nil {
		t.Fatal(err)
	}
	writeResumeFixture(t, runDir, runID, repo, baseline, RunStatusRunning)
	locator, err := resolveStateLocator(repo, stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	// Move real runs aside and plant escaping runs symlink containing the run.
	backupRuns := locator.RunsRoot + ".bak"
	if err := os.Rename(locator.RunsRoot, backupRuns); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, locator.RunsRoot); err != nil {
		_ = os.Rename(backupRuns, locator.RunsRoot)
		t.Skipf("symlinks unavailable: %v", err)
	}
	// Place a copy of the run under the external target so a naive path would work.
	externalRun := filepath.Join(outside, runID)
	if err := os.MkdirAll(externalRun, 0o700); err != nil {
		t.Fatal(err)
	}
	writeResumeFixture(t, externalRun, runID, repo, baseline, RunStatusRunning)
	if err := os.WriteFile(filepath.Join(externalRun, "sentinel.txt"), []byte("external_via_runs"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := resolveControlRunDirectory(repo, stateRoot, runID); err == nil {
		t.Fatal("resolveControlRunDirectory accepted escaping runs root")
	}
	resumeReq := ControlResumeRequest{SchemaVersion: ControlContractVersion, Repository: repository, RunID: runID}
	resumeReq.Approval = validResumeApproval(t, resumeReq, "runs-root-escape-resume")
	if _, err := runtime.Resume(context.Background(), resumeReq); err == nil {
		t.Fatal("Resume accepted escaping runs root")
	}
	cancelReq := ControlCancelRequest{SchemaVersion: ControlContractVersion, Repository: repository, RunID: runID}
	cancelReq.Approval = validCancelApproval(t, cancelReq, "runs-root-escape-cancel")
	if _, err := runtime.Cancel(context.Background(), cancelReq); err == nil {
		t.Fatal("Cancel accepted escaping runs root")
	}
	if _, err := os.Stat(filepath.Join(externalRun, "resume.json")); !os.IsNotExist(err) {
		t.Fatalf("external resume.json written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(externalRun, controlCancelRequestName)); !os.IsNotExist(err) {
		t.Fatalf("external cancel-request written: %v", err)
	}
	sentinel, err := os.ReadFile(filepath.Join(externalRun, "sentinel.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(sentinel) != "external_via_runs" {
		t.Fatalf("external run modified: %s", sentinel)
	}
}
