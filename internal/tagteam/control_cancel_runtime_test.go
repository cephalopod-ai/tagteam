package tagteam

import (
	"context"
	"errors"
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

func TestControlRuntimeCancelFromFreshRuntimeRejectsLiveRunItDoesNotOwn(t *testing.T) {
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
	firstRuntime.registerJob(runID, cancelJob)
	defer firstRuntime.unregisterJob(runID)
	request := ControlCancelRequest{SchemaVersion: ControlContractVersion, Repository: repository, RunID: runID}
	request.Approval = validCancelApproval(t, request, "cancel-from-new-runtime")

	secondRuntime := NewControlRuntime(service, DefaultConfig(), nil)
	if _, err := secondRuntime.Cancel(context.Background(), request); err == nil {
		t.Fatal("unowned live run cancellation reported success")
	} else {
		assertControlCancelError(t, err, "run_not_owned")
	}
	select {
	case <-jobContext.Done():
		t.Fatal("unowned live run cancellation signalled the original job")
	case <-time.After(150 * time.Millisecond):
	}
	if _, err := firstRuntime.Cancel(context.Background(), request); err != nil {
		t.Fatalf("owned live run cancellation failed: %v", err)
	}
	select {
	case <-jobContext.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("owned live run cancellation did not signal the job")
	}
}

func TestControlRuntimeCancellationWatcherStopsAfterTerminalRun(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	runtime := NewControlRuntime(ControlService{RepositoryRoot: repo, StateRoot: t.TempDir(), ProducerVersion: "test"}, DefaultConfig(), nil)
	request := controlStartFixture(t, repo)
	handle, err := runtime.Start(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	waitForControlRunFailure(t, runtime, request.IdempotencyKey)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runtime.mu.Lock()
		watcherActive := runtime.watcherCancel != nil || runtime.watcherDone != nil
		runtime.mu.Unlock()
		if !watcherActive {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("cancellation watcher remained active after terminal run %q", handle.RunID)
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

func assertControlCancelError(t *testing.T, err error, wantCode string) {
	t.Helper()
	var cancelErr *ControlCancelError
	if err == nil || !errors.As(err, &cancelErr) {
		t.Fatalf("error = %v, want ControlCancelError/%s", err, wantCode)
	}
	if cancelErr.ReasonCode != wantCode || cancelErr.Reason == "" {
		t.Fatalf("cancel error = %#v, want code %q and bounded reason", cancelErr, wantCode)
	}
}
