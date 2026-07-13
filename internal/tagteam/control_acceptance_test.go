package tagteam

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// These acceptance playtests exercise the MCP control-plane lifecycle against
// malformed persisted state, hostile run identities, concurrent callers, and a
// failing adapter. Every failure must surface as a typed, recoverable control
// error (or a persisted terminal record) so an MCP host can recover without
// reading Tagteam source.

func writeMalformedControlLedger(t *testing.T, repo, stateRoot string) {
	t.Helper()
	locator, err := resolveStateLocator(repo, stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := locator.Prepare(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locator.RepoRoot, controlApprovalLedgerName), []byte("{ this is not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func corruptRunArtifact(t *testing.T, runDir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(runDir, name), []byte("{ not valid json ]"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func waitForControlJobsDrained(t *testing.T, runtime *ControlRuntime) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runtime.mu.Lock()
		remaining := len(runtime.jobs)
		runtime.mu.Unlock()
		if remaining == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("control runtime jobs did not drain")
}

func TestControlRuntimeStartRejectsMalformedApprovalLedger(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	stateRoot := t.TempDir()
	writeMalformedControlLedger(t, repo, stateRoot)
	runtime := NewControlRuntime(ControlService{RepositoryRoot: repo, StateRoot: stateRoot, ProducerVersion: "test"}, DefaultConfig(), nil)
	_, err := runtime.Start(context.Background(), controlStartFixture(t, repo))
	assertControlStartError(t, err, "approval_ledger_invalid")
}

func TestControlRuntimeResumeRejectsMalformedApprovalLedger(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	stateRoot := t.TempDir()
	writeMalformedControlLedger(t, repo, stateRoot)
	repository, err := resolveControlRepository(repo)
	if err != nil {
		t.Fatal(err)
	}
	request := ControlResumeRequest{SchemaVersion: ControlContractVersion, Repository: repository, RunID: "run-malformed-ledger"}
	request.Approval = validResumeApproval(t, request, "resume-ledger-nonce")
	runtime := NewControlRuntime(ControlService{RepositoryRoot: repo, StateRoot: stateRoot, ProducerVersion: "test"}, DefaultConfig(), nil)
	_, err = runtime.Resume(context.Background(), request)
	assertControlResumeError(t, err, "approval_ledger_invalid")
}

func TestControlRuntimeCancelRejectsMalformedApprovalLedger(t *testing.T) {
	repo, baseline := createResumeFixtureRepo(t)
	stateRoot := t.TempDir()
	runID := "cancel-malformed-ledger"
	cancellableRunFixture(t, repo, stateRoot, runID, baseline)
	writeMalformedControlLedger(t, repo, stateRoot)
	repository, err := resolveControlRepository(repo)
	if err != nil {
		t.Fatal(err)
	}
	request := ControlCancelRequest{SchemaVersion: ControlContractVersion, Repository: repository, RunID: runID}
	request.Approval = validCancelApproval(t, request, "cancel-ledger-nonce")
	runtime := NewControlRuntime(ControlService{RepositoryRoot: repo, StateRoot: stateRoot, ProducerVersion: "test"}, DefaultConfig(), nil)
	_, err = runtime.Cancel(context.Background(), request)
	assertControlCancelError(t, err, "approval_ledger_invalid")
}

func TestControlRuntimeResumeRejectsMalformedRunArtifacts(t *testing.T) {
	cases := []struct {
		artifact string
		code     string
	}{
		{"state.json", "state_invalid"},
		{"meta.json", "metadata_invalid"},
		{"final.json", "final_invalid"},
	}
	for _, tc := range cases {
		t.Run(tc.artifact, func(t *testing.T) {
			repo, baseline := createResumeFixtureRepo(t)
			stateRoot := t.TempDir()
			runID := "resume-malformed-" + tc.code
			runDir, err := createRunDir(repo, stateRoot, runID)
			if err != nil {
				t.Fatal(err)
			}
			writeResumeFixture(t, runDir, runID, repo, baseline, RunStatusRunning)
			corruptRunArtifact(t, runDir, tc.artifact)
			repository, err := resolveControlRepository(repo)
			if err != nil {
				t.Fatal(err)
			}
			request := ControlResumeRequest{SchemaVersion: ControlContractVersion, Repository: repository, RunID: runID}
			request.Approval = validResumeApproval(t, request, "resume-malformed-nonce")
			runtime := NewControlRuntime(ControlService{RepositoryRoot: repo, StateRoot: stateRoot, ProducerVersion: "test"}, DefaultConfig(), nil)
			_, err = runtime.Resume(context.Background(), request)
			assertControlResumeError(t, err, tc.code)
			// A malformed artifact must never consume the approval nonce.
			locator, err := resolveStateLocator(repo, stateRoot)
			if err != nil {
				t.Fatal(err)
			}
			ledger, err := readControlApprovalLedger(filepath.Join(locator.RepoRoot, controlApprovalLedgerName))
			if err != nil {
				t.Fatal(err)
			}
			if len(ledger.Resumes) != 0 {
				t.Fatalf("malformed %s consumed a resume approval: %#v", tc.artifact, ledger.Resumes)
			}
		})
	}
}

func TestControlRuntimeCancelRejectsMalformedRunArtifacts(t *testing.T) {
	for _, artifact := range []string{"final.json", "state.json"} {
		t.Run(artifact, func(t *testing.T) {
			repo, baseline := createResumeFixtureRepo(t)
			stateRoot := t.TempDir()
			runID := "cancel-malformed-" + artifact
			runDir, err := createRunDir(repo, stateRoot, runID)
			if err != nil {
				t.Fatal(err)
			}
			writeResumeFixture(t, runDir, runID, repo, baseline, RunStatusRunning)
			// state.json corruption is only reached once final.json is absent.
			if artifact == "state.json" {
				if err := os.Remove(filepath.Join(runDir, "final.json")); err != nil {
					t.Fatal(err)
				}
			}
			corruptRunArtifact(t, runDir, artifact)
			repository, err := resolveControlRepository(repo)
			if err != nil {
				t.Fatal(err)
			}
			request := ControlCancelRequest{SchemaVersion: ControlContractVersion, Repository: repository, RunID: runID}
			request.Approval = validCancelApproval(t, request, "cancel-malformed-nonce")
			runtime := NewControlRuntime(ControlService{RepositoryRoot: repo, StateRoot: stateRoot, ProducerVersion: "test"}, DefaultConfig(), nil)
			_, err = runtime.Cancel(context.Background(), request)
			assertControlCancelError(t, err, "cancel_unavailable")
		})
	}
}

func TestControlRuntimeLifecycleRejectsHostileRunID(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	repository, err := resolveControlRepository(repo)
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewControlRuntime(ControlService{RepositoryRoot: repo, StateRoot: t.TempDir(), ProducerVersion: "test"}, DefaultConfig(), nil)
	now := time.Now().UTC()
	approval := ControlApproval{ActionDigest: "unused", ApprovedAt: now.Add(-time.Second), ExpiresAt: now.Add(time.Minute), Nonce: "hostile"}
	for _, runID := range []string{"../escape", "..", "runs/../escape", "nested/child", `back\slash`} {
		t.Run(runID, func(t *testing.T) {
			_, resumeErr := runtime.Resume(context.Background(), ControlResumeRequest{SchemaVersion: ControlContractVersion, Repository: repository, RunID: runID, Approval: approval})
			assertControlResumeError(t, resumeErr, "invalid_request")
			_, cancelErr := runtime.Cancel(context.Background(), ControlCancelRequest{SchemaVersion: ControlContractVersion, Repository: repository, RunID: runID, Approval: approval})
			assertControlCancelError(t, cancelErr, "invalid_request")
		})
	}
}

// TestControlRuntimeStartIsSafeUnderConcurrentIdenticalRequests proves the run
// lock and approval ledger keep an idempotent start single-valued: concurrent
// identical requests yield one run, and losers fail only on the ledger lock.
func TestControlRuntimeStartIsSafeUnderConcurrentIdenticalRequests(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	stateRoot := t.TempDir()
	runtime := NewControlRuntime(ControlService{RepositoryRoot: repo, StateRoot: stateRoot, ProducerVersion: "test"}, DefaultConfig(), nil)
	request := controlStartFixture(t, repo)

	const workers = 8
	var wg sync.WaitGroup
	handles := make([]ControlRunHandle, workers)
	errs := make([]error, workers)
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			defer wg.Done()
			handles[i], errs[i] = runtime.Start(context.Background(), request)
		}(i)
	}
	wg.Wait()

	runID := ""
	successes := 0
	for i := 0; i < workers; i++ {
		if errs[i] == nil {
			successes++
			if runID == "" {
				runID = handles[i].RunID
			}
			if handles[i].RunID == "" || handles[i].RunID != runID {
				t.Fatalf("concurrent starts produced divergent run ids %q and %q", runID, handles[i].RunID)
			}
			continue
		}
		// A loser may only fail transiently on the ledger lock; it must never
		// create a second run or double-consume the nonce.
		assertControlStartError(t, errs[i], "approval_ledger_locked")
	}
	if successes == 0 {
		t.Fatal("no concurrent identical start succeeded")
	}
	locator, err := resolveStateLocator(repo, stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := readControlApprovalLedger(filepath.Join(locator.RepoRoot, controlApprovalLedgerName))
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger.Starts) != 1 {
		t.Fatalf("concurrent identical starts created %d run records, want 1", len(ledger.Starts))
	}
	if ledger.Starts[0].Nonce != request.Approval.Nonce || ledger.Starts[0].RunID != runID {
		t.Fatalf("ledger start record = %#v", ledger.Starts[0])
	}
	waitForControlJobsDrained(t, runtime)
}

// TestControlRuntimeStartConsumesNonceOnceUnderConcurrentDistinctKeys proves a
// single approval nonce cannot launch two runs even when distinct idempotency
// keys race: exactly one wins, and every loser is a typed, recoverable error.
func TestControlRuntimeStartConsumesNonceOnceUnderConcurrentDistinctKeys(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	stateRoot := t.TempDir()
	runtime := NewControlRuntime(ControlService{RepositoryRoot: repo, StateRoot: stateRoot, ProducerVersion: "test"}, DefaultConfig(), nil)
	const workers = 6
	const shared = "single-use-nonce"
	requests := make([]ControlStartRequest, workers)
	for i := 0; i < workers; i++ {
		request := controlStartFixture(t, repo)
		request.IdempotencyKey = "concurrent-key-" + string(rune('a'+i))
		digest, err := ControlStartActionDigest(request)
		if err != nil {
			t.Fatal(err)
		}
		request.Approval.ActionDigest = digest
		request.Approval.Nonce = shared
		requests[i] = request
	}

	var wg sync.WaitGroup
	errs := make([]error, workers)
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			defer wg.Done()
			_, errs[i] = runtime.Start(context.Background(), requests[i])
		}(i)
	}
	wg.Wait()

	successes := 0
	for i := 0; i < workers; i++ {
		if errs[i] == nil {
			successes++
			continue
		}
		var startErr *ControlStartError
		if !errors.As(errs[i], &startErr) || !startErr.Recoverable {
			t.Fatalf("concurrent loser error = %v, want recoverable ControlStartError", errs[i])
		}
	}
	if successes != 1 {
		t.Fatalf("distinct-key nonce race produced %d successes, want exactly 1", successes)
	}
	locator, err := resolveStateLocator(repo, stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := readControlApprovalLedger(filepath.Join(locator.RepoRoot, controlApprovalLedgerName))
	if err != nil {
		t.Fatal(err)
	}
	consumed := 0
	for _, record := range ledger.Starts {
		if record.Nonce == shared {
			consumed++
		}
	}
	if consumed != 1 {
		t.Fatalf("shared nonce consumed %d times, want exactly 1", consumed)
	}
	waitForControlJobsDrained(t, runtime)
}

// TestControlRuntimeStartPersistsTypedTerminalRecordOnAdapterFailure proves a
// weak or unrunnable adapter yields a complete terminal record (failed status,
// bounded blocking reason, nonzero exit) rather than a run reported as
// successful from missing evidence.
func TestControlRuntimeStartPersistsTypedTerminalRecordOnAdapterFailure(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	stateRoot := t.TempDir()
	runtime := NewControlRuntime(ControlService{RepositoryRoot: repo, StateRoot: stateRoot, ProducerVersion: "test"}, DefaultConfig(), nil)
	handle, err := runtime.Start(context.Background(), controlStartFixture(t, repo))
	if err != nil {
		t.Fatal(err)
	}
	waitForControlRunFailure(t, runtime, "session-1-generation-1")

	locator, err := resolveStateLocator(repo, stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	runDir, err := locator.RunDir(handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(runDir, "final.json"))
	if err != nil {
		t.Fatal(err)
	}
	var final FinalRun
	if err := json.Unmarshal(data, &final); err != nil {
		t.Fatal(err)
	}
	if final.Status != RunStatusFailed {
		t.Fatalf("terminal status = %q, want failed", final.Status)
	}
	if final.BlockingReason == "" || final.ExitCode == ExitSuccess {
		t.Fatalf("terminal record lacks a typed failure classification: %#v", final)
	}
	if final.RunID != handle.RunID {
		t.Fatalf("terminal run id = %q, want %q", final.RunID, handle.RunID)
	}
	waitForControlJobsDrained(t, runtime)
}
