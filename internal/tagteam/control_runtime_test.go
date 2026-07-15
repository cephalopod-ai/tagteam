package tagteam

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestControlRuntimeRejectsInvalidApprovalBeforeWritingState(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	stateRoot := t.TempDir()
	runtime := NewControlRuntime(ControlService{RepositoryRoot: repo, StateRoot: stateRoot, ProducerVersion: "test"}, DefaultConfig(), nil)
	request := controlStartFixture(t, repo)
	request.Approval.ActionDigest = "wrong"
	if _, err := runtime.Start(context.Background(), request); err == nil {
		t.Fatal("invalid approval started a run")
	}
	if _, err := os.Stat(filepath.Join(repo, ".tagteam", repositoryPointerName)); !os.IsNotExist(err) {
		t.Fatalf("invalid approval wrote repository state: %v", err)
	}
}

func TestControlRuntimeStartIsIdempotentAndPersistsPreflightFailure(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	stateRoot := t.TempDir()
	service := ControlService{RepositoryRoot: repo, StateRoot: stateRoot, ProducerVersion: "test"}
	runtime := NewControlRuntime(service, DefaultConfig(), nil)
	request := controlStartFixture(t, repo)

	first, err := runtime.Start(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := runtime.Start(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.RunID != second.RunID || first.RunID == "" {
		t.Fatalf("idempotent handles = %#v %#v", first, second)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, statusErr := runtime.Status(first.RunID)
		if statusErr == nil && status.Run.Status == string(RunStatusFailed) {
			if status.Run.RunID != first.RunID {
				t.Fatalf("status run id = %q, want %q", status.Run.RunID, first.RunID)
			}
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("start failure was not persisted for run %q", first.RunID)
}

func TestControlRuntimeStartMatchesDirectRunTerminalRecord(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	stateRoot := t.TempDir()
	cfg := DefaultConfig()
	service := ControlService{RepositoryRoot: repo, StateRoot: stateRoot, ProducerVersion: "test"}
	runtime := NewControlRuntime(service, cfg, nil)
	request := controlStartFixture(t, repo)
	normalized, err := NormalizeControlLaunch(request.Launch)
	if err != nil {
		t.Fatal(err)
	}
	directOptions, err := runtime.optionsForLaunch(normalized)
	if err != nil {
		t.Fatal(err)
	}
	directOptions.RunID = "direct-control-equivalence"
	direct, directErr := NewApp(cfg).Run(context.Background(), directOptions)
	if directErr == nil || direct.RunID == "" {
		t.Fatalf("direct run = %#v, err=%v", direct, directErr)
	}

	handle, err := runtime.Start(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	waitForControlRunFailure(t, runtime, request.IdempotencyKey)
	mcp, err := runtime.Status(handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if direct.Mode != mcp.Run.Mode || string(direct.Status) != mcp.Run.Status || direct.Verdict != mcp.Run.Verdict || direct.ExitCode != mcp.Run.ExitCode || direct.BlockingReason != mcp.Run.BlockingReason {
		t.Fatalf("direct terminal=%#v; MCP terminal=%#v", direct, mcp.Run)
	}
}

func TestControlRuntimeRejectsApprovalNonceReplayAcrossIdempotencyKeys(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	runtime := NewControlRuntime(ControlService{RepositoryRoot: repo, StateRoot: t.TempDir(), ProducerVersion: "test"}, DefaultConfig(), nil)
	request := controlStartFixture(t, repo)
	if _, err := runtime.Start(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	replay := request
	replay.IdempotencyKey = "another-key"
	digest, err := ControlStartActionDigest(replay)
	if err != nil {
		t.Fatal(err)
	}
	replay.Approval.ActionDigest = digest
	if _, err := runtime.Start(context.Background(), replay); err == nil {
		t.Fatal("replayed approval nonce started a second run")
	}
	waitForControlRunFailure(t, runtime, request.IdempotencyKey)
}

func TestControlRuntimeRejectsSecondStartWhileFirstReservationIsPending(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	runtime := NewControlRuntime(ControlService{RepositoryRoot: repo, StateRoot: t.TempDir(), ProducerVersion: "test"}, DefaultConfig(), nil)
	locator, err := resolveStateLocator(repo, runtime.service.StateRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := locator.Prepare(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	ledger := controlApprovalLedger{SchemaVersion: ControlContractVersion, Starts: []controlStartRecord{{
		IdempotencyKey: "existing-start",
		ActionDigest:   "existing-digest",
		Nonce:          "existing-nonce",
		RunID:          "pending-control-start",
		CreatedAt:      now,
		ExpiresAt:      now.Add(time.Minute),
	}}}
	if err := writeJSONDurable(filepath.Join(locator.RepoRoot, controlApprovalLedgerName), ledger, false, true); err != nil {
		t.Fatal(err)
	}
	request := controlStartFixture(t, repo)
	request.IdempotencyKey = "new-start"
	digest, err := ControlStartActionDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	request.Approval.ActionDigest = digest
	request.Approval.Nonce = "new-nonce"
	if _, err := runtime.Start(context.Background(), request); err == nil || !strings.Contains(err.Error(), "already pending or active") {
		t.Fatalf("start error = %v, want pending reservation rejection", err)
	}
}

func TestControlRuntimeRejectsStartOutsideTheMCPWorktree(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	otherRepo, _ := createResumeFixtureRepo(t)
	runtime := NewControlRuntime(ControlService{RepositoryRoot: repo, StateRoot: t.TempDir(), ProducerVersion: "test"}, DefaultConfig(), nil)
	request := controlStartFixture(t, otherRepo)
	if _, err := runtime.Start(context.Background(), request); err == nil || !strings.Contains(err.Error(), "must match the MCP server worktree") {
		t.Fatalf("start error = %v, want server worktree rejection", err)
	}
}

func waitForControlRunFailure(t *testing.T, runtime *ControlRuntime, idempotencyKey string) {
	t.Helper()
	locator, err := resolveStateLocator(runtime.service.RepositoryRoot, runtime.service.StateRoot)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := readControlApprovalLedger(filepath.Join(locator.RepoRoot, controlApprovalLedgerName))
	if err != nil {
		t.Fatal(err)
	}
	var runID string
	for _, record := range ledger.Starts {
		if record.IdempotencyKey == idempotencyKey {
			runID = record.RunID
			break
		}
	}
	if runID == "" {
		t.Fatalf("missing control start record for %q", idempotencyKey)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, statusErr := runtime.Status(runID)
		if statusErr == nil && status.Run.Status == string(RunStatusFailed) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("control run %q did not reach a terminal failure", runID)
}

func controlStartFixture(t *testing.T, repo string) ControlStartRequest {
	t.Helper()
	launch := controlLaunchFixture(t, repo)
	launch.Team.Worker = &ControlRoleTarget{Adapter: "missing"}
	launch.Team.Supervisor = &ControlRoleTarget{Adapter: "missing"}
	launch.TestPreset = ""
	request := ControlStartRequest{SchemaVersion: ControlContractVersion, Launch: launch, IdempotencyKey: "session-1-generation-1"}
	digest, err := ControlStartActionDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	request.Approval = ControlApproval{ActionDigest: digest, ApprovedAt: now.Add(-time.Second), ExpiresAt: now.Add(5 * time.Minute), Nonce: "operator-approved-nonce"}
	return request
}

func TestControlRuntimeResolvesTrustedTestPresetIntoRunOptions(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	cfg := DefaultConfig()
	cfg.Defaults.Test = "make default-test"
	cfg.TestPresets = map[string]TestPresetConfig{
		"go-test": {
			Command:       "go test ./...",
			IdentityRegex: `FAIL:\s+(\S+)`,
		},
	}
	runtime := NewControlRuntime(ControlService{RepositoryRoot: repo, StateRoot: t.TempDir(), ProducerVersion: "test"}, cfg, nil)
	spec := controlLaunchFixture(t, repo)
	spec.TestPreset = "go-test"
	// Production Start normalizes before optionsForLaunch; revalidation binds
	// the approved canonical scope list, not raw caller input.
	normalized, err := NormalizeControlLaunch(spec)
	if err != nil {
		t.Fatal(err)
	}
	opts, err := runtime.optionsForLaunch(normalized)
	if err != nil {
		t.Fatal(err)
	}
	if opts.TestCmd != "go test ./..." {
		t.Fatalf("TestCmd = %q, want preset command", opts.TestCmd)
	}
	if opts.TestIdentityRegex != `FAIL:\s+(\S+)` {
		t.Fatalf("TestIdentityRegex = %q", opts.TestIdentityRegex)
	}
}

func TestControlRuntimeEmptyTestPresetFallsBackToTrustedDefaults(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	cfg := DefaultConfig()
	cfg.Defaults.Test = "make default-test"
	cfg.TestPresets = map[string]TestPresetConfig{
		"go-test": {Command: "go test ./..."},
	}
	runtime := NewControlRuntime(ControlService{RepositoryRoot: repo, StateRoot: t.TempDir(), ProducerVersion: "test"}, cfg, nil)
	spec := controlLaunchFixture(t, repo)
	spec.TestPreset = ""
	normalized, err := NormalizeControlLaunch(spec)
	if err != nil {
		t.Fatal(err)
	}
	opts, err := runtime.optionsForLaunch(normalized)
	if err != nil {
		t.Fatal(err)
	}
	if opts.TestCmd != "make default-test" {
		t.Fatalf("TestCmd = %q, want trusted default", opts.TestCmd)
	}
}

func TestControlRuntimeUnknownTestPresetIsDeterministic(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	cfg := DefaultConfig()
	cfg.TestPresets = map[string]TestPresetConfig{
		"go-test": {Command: "go test ./..."},
	}
	runtime := NewControlRuntime(ControlService{RepositoryRoot: repo, StateRoot: t.TempDir(), ProducerVersion: "test"}, cfg, nil)
	spec := controlLaunchFixture(t, repo)
	spec.TestPreset = "no-such-preset"
	normalized, err := NormalizeControlLaunch(spec)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.optionsForLaunch(normalized)
	if err == nil || err.Error() != `unknown test_preset "no-such-preset"` {
		t.Fatalf("error = %v, want deterministic unknown test_preset message", err)
	}
	if strings.Contains(err.Error(), "go-test") || strings.Contains(err.Error(), "go test") {
		t.Fatalf("error leaked registry contents: %v", err)
	}
}

func TestControlStartActionDigestBindsPresetNameNotResolvedCommand(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	request := ControlStartRequest{
		SchemaVersion:  ControlContractVersion,
		Launch:         controlLaunchFixture(t, repo),
		IdempotencyKey: "session-1-generation-1",
	}
	request.Launch.TestPreset = "go-test"
	first, err := ControlStartActionDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	// Resolution changes only RunOptions; the digest is independent of the
	// host registry command that a name maps to.
	second, err := ControlStartActionDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("digests diverged without launch change: %q %q", first, second)
	}
	request.Launch.TestPreset = "unit"
	third, err := ControlStartActionDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	if first == third {
		t.Fatal("start digest did not bind test_preset name")
	}
}
