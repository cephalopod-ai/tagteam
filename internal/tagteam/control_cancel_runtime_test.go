package tagteam

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestControlRuntimeCancelPersistsStatusAfterOwnerRestart(t *testing.T) {
	repo, baseline := createResumeFixtureRepo(t)
	stateRoot := t.TempDir()
	runID := "control-cancel-stale-owner"
	service := ControlService{RepositoryRoot: repo, StateRoot: stateRoot, ProducerVersion: "test"}
	runtime := NewControlRuntime(service, DefaultConfig(), nil)
	runDir, err := createRunDir(repo, stateRoot, runID)
	if err != nil {
		t.Fatal(err)
	}
	writeResumeFixture(t, runDir, runID, repo, baseline, RunStatusRunning)
	if err := os.Remove(filepath.Join(runDir, "final.json")); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONDurable(filepath.Join(runDir, "run.lock"), runLockRecord{PID: os.Getpid() + 100000, CreatedAt: time.Now().UTC()}, true, true); err != nil {
		t.Fatal(err)
	}
	repository, err := resolveControlRepository(repo)
	if err != nil {
		t.Fatal(err)
	}
	request := ControlCancelRequest{SchemaVersion: ControlContractVersion, Repository: repository, RunID: runID}
	request.Approval = validCancelApproval(t, request, "cancel-once")

	first, err := runtime.Cancel(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.RunID != runID {
		t.Fatalf("cancel handle = %#v", first)
	}
	status, err := runtime.Status(runID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Run.Status != string(RunStatusCancelled) || status.Run.BlockingReason != string(ReasonCancelled) {
		t.Fatalf("cancelled status = %#v", status.Run)
	}
	locator, err := resolveStateLocator(repo, stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := readControlApprovalLedger(filepath.Join(locator.RepoRoot, controlApprovalLedgerName))
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger.Cancels) != 1 || ledger.Cancels[0].Nonce != request.Approval.Nonce || ledger.Cancels[0].OwnerPID == 0 {
		t.Fatalf("cancel ledger = %#v", ledger.Cancels)
	}

	secondRuntime := NewControlRuntime(service, DefaultConfig(), nil)
	second, err := secondRuntime.Cancel(context.Background(), request)
	if err != nil || second != first {
		t.Fatalf("idempotent cancel = %#v, err=%v; first=%#v", second, err, first)
	}
}

func TestControlRuntimeCancelFromFreshRuntimeSignalsOriginalJob(t *testing.T) {
	repo, baseline := createResumeFixtureRepo(t)
	stateRoot := t.TempDir()
	runID := "control-cancel-fresh-runtime"
	service := ControlService{RepositoryRoot: repo, StateRoot: stateRoot, ProducerVersion: "test"}
	runDir, err := createRunDir(repo, stateRoot, runID)
	if err != nil {
		t.Fatal(err)
	}
	writeResumeFixture(t, runDir, runID, repo, baseline, RunStatusRunning)
	if err := os.Remove(filepath.Join(runDir, "final.json")); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONDurable(filepath.Join(runDir, "run.lock"), runLockRecord{PID: os.Getpid(), CreatedAt: time.Now().UTC()}, true, true); err != nil {
		t.Fatal(err)
	}
	repository, err := resolveControlRepository(repo)
	if err != nil {
		t.Fatal(err)
	}
	firstRuntime := NewControlRuntime(service, DefaultConfig(), nil)
	jobContext, cancelJob := context.WithCancel(context.Background())
	defer cancelJob()
	firstRuntime.mu.Lock()
	firstRuntime.jobs[runID] = func() { cancelJob() }
	firstRuntime.mu.Unlock()
	request := ControlCancelRequest{SchemaVersion: ControlContractVersion, Repository: repository, RunID: runID}
	request.Approval = validCancelApproval(t, request, "cancel-from-new-runtime")

	secondRuntime := NewControlRuntime(service, DefaultConfig(), nil)
	if _, err := secondRuntime.Cancel(jobContext, request); err != nil {
		t.Fatal(err)
	}
	select {
	case <-jobContext.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("original runtime did not observe the durable cancellation request")
	}
}

func validCancelApproval(t *testing.T, request ControlCancelRequest, nonce string) ControlApproval {
	t.Helper()
	digest, err := ControlCancelActionDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	return ControlApproval{ActionDigest: digest, ApprovedAt: now.Add(-time.Second), ExpiresAt: now.Add(5 * time.Minute), Nonce: nonce}
}
