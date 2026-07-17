package tagteam

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestControlRuntimeCapabilitiesIncludeResumeOnlyWhenEnabled(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	service := ControlService{RepositoryRoot: repo, StateRoot: t.TempDir(), ProducerVersion: "test"}
	base := service.Capabilities()
	if controlContainsString(base.Capabilities, "resume") {
		t.Fatalf("base capabilities unexpectedly include resume: %#v", base.Capabilities)
	}
	runtime := NewControlRuntime(service, DefaultConfig(), nil)
	if !controlContainsString(runtime.Capabilities().Capabilities, "resume") {
		t.Fatalf("runtime capabilities do not include resume: %#v", runtime.Capabilities())
	}
}

func TestControlRuntimeResumeRejectsTypedApprovalFailures(t *testing.T) {
	repo, baseline := createResumeFixtureRepo(t)
	stateRoot := t.TempDir()
	runID := "control-resume-approval"
	runDir, err := createRunDir(repo, stateRoot, runID)
	if err != nil {
		t.Fatal(err)
	}
	writeResumeFixture(t, runDir, runID, repo, baseline, RunStatusRunning)
	repository, err := resolveControlRepository(repo)
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewControlRuntime(ControlService{RepositoryRoot: repo, StateRoot: stateRoot, ProducerVersion: "test"}, DefaultConfig(), nil)

	tests := []struct {
		name string
		edit func(*ControlResumeRequest)
		code string
	}{
		{name: "missing", edit: func(request *ControlResumeRequest) {}, code: "approval_missing"},
		{name: "action mismatch", edit: func(request *ControlResumeRequest) {
			request.Approval = validResumeApproval(t, *request, "wrong-nonce")
			request.Approval.ActionDigest = "wrong"
		}, code: "approval_action_mismatch"},
		{name: "expired", edit: func(request *ControlResumeRequest) {
			request.Approval = validResumeApproval(t, *request, "expired-nonce")
			request.Approval.ApprovedAt = time.Now().UTC().Add(-10 * time.Minute)
			request.Approval.ExpiresAt = time.Now().UTC().Add(-time.Minute)
		}, code: "approval_expired"},
		{name: "too long", edit: func(request *ControlResumeRequest) {
			request.Approval = validResumeApproval(t, *request, "long-nonce")
			request.Approval.ExpiresAt = request.Approval.ApprovedAt.Add(ControlApprovalMaxLifetime + time.Second)
		}, code: "approval_lifetime_exceeded"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := ControlResumeRequest{SchemaVersion: ControlContractVersion, Repository: repository, RunID: runID}
			test.edit(&request)
			_, err := runtime.Resume(context.Background(), request)
			assertControlResumeError(t, err, test.code)
		})
	}
}

func TestControlRuntimeResumeRejectsUnresumableRunWithBoundedReason(t *testing.T) {
	repo, baseline := createResumeFixtureRepo(t)
	stateRoot := t.TempDir()
	runID := "control-resume-terminal"
	runDir, err := createRunDir(repo, stateRoot, runID)
	if err != nil {
		t.Fatal(err)
	}
	writeResumeFixture(t, runDir, runID, repo, baseline, RunStatusPassed)
	repository, err := resolveControlRepository(repo)
	if err != nil {
		t.Fatal(err)
	}
	request := ControlResumeRequest{SchemaVersion: ControlContractVersion, Repository: repository, RunID: runID}
	request.Approval = validResumeApproval(t, request, "terminal-nonce")
	runtime := NewControlRuntime(ControlService{RepositoryRoot: repo, StateRoot: stateRoot, ProducerVersion: "test"}, DefaultConfig(), nil)
	if _, err := runtime.Resume(context.Background(), request); err == nil {
		t.Fatal("terminal run was resumed")
	} else {
		assertControlResumeError(t, err, "already_terminal")
	}
	locator, err := resolveStateLocator(repo, stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := readControlApprovalLedger(filepath.Join(locator.RepoRoot, controlApprovalLedgerName))
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger.Resumes) != 0 {
		t.Fatalf("unresumable request consumed approval: %#v", ledger.Resumes)
	}
}

func TestControlRuntimeResumeIsIdempotentAndPersistsNonceAcrossRuntimeRestart(t *testing.T) {
	repo, baseline := createResumeFixtureRepo(t)
	stateRoot := t.TempDir()
	runID := "control-resume-idempotent"
	runDir, err := createRunDir(repo, stateRoot, runID)
	if err != nil {
		t.Fatal(err)
	}
	writeResumeFixture(t, runDir, runID, repo, baseline, RunStatusRunning)
	repository, err := resolveControlRepository(repo)
	if err != nil {
		t.Fatal(err)
	}
	request := ControlResumeRequest{SchemaVersion: ControlContractVersion, Repository: repository, RunID: runID}
	request.Approval = validResumeApproval(t, request, "resume-once")
	service := ControlService{RepositoryRoot: repo, StateRoot: stateRoot, ProducerVersion: "test"}
	ctx, cancel := context.WithCancel(context.Background())
	firstRuntime := NewControlRuntime(service, DefaultConfig(), nil)
	first, err := firstRuntime.Resume(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	waitForControlResumeJob(t, firstRuntime, runID)

	secondRuntime := NewControlRuntime(service, DefaultConfig(), nil)
	second, err := secondRuntime.Resume(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("reissued resume handle = %#v, first = %#v", second, first)
	}
	locator, err := resolveStateLocator(repo, stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := readControlApprovalLedger(filepath.Join(locator.RepoRoot, controlApprovalLedgerName))
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger.Resumes) != 1 || ledger.Resumes[0].Nonce != request.Approval.Nonce {
		t.Fatalf("resume ledger = %#v", ledger)
	}

	replay := request
	replay.RunID = "control-resume-other"
	replay.Approval = validResumeApproval(t, replay, request.Approval.Nonce)
	if _, err := secondRuntime.Resume(context.Background(), replay); err == nil {
		t.Fatal("resume nonce replay was accepted for another run")
	} else {
		assertControlResumeError(t, err, "approval_nonce_replayed")
	}
}

func TestControlRuntimeResumePersistsDiagnosticAfterConsumedApprovalPreflightFailure(t *testing.T) {
	repo, baseline := createResumeFixtureRepo(t)
	stateRoot := t.TempDir()
	runID := "control-resume-consumed-preflight"
	runDir, err := createRunDir(repo, stateRoot, runID)
	if err != nil {
		t.Fatal(err)
	}
	writeResumeFixture(t, runDir, runID, repo, baseline, RunStatusRunning)
	repository, err := resolveControlRepository(repo)
	if err != nil {
		t.Fatal(err)
	}
	request := ControlResumeRequest{SchemaVersion: ControlContractVersion, Repository: repository, RunID: runID}
	request.Approval = validResumeApproval(t, request, "resume-preflight-diagnostic")

	controlResumePostLockHook = func() {
		if err := os.Remove(filepath.Join(runDir, "state.json")); err != nil {
			t.Errorf("remove state after approval consumption: %v", err)
		}
	}
	t.Cleanup(func() { controlResumePostLockHook = nil })
	runtime := NewControlRuntime(ControlService{RepositoryRoot: repo, StateRoot: stateRoot, ProducerVersion: "test"}, DefaultConfig(), nil)
	if _, err := runtime.Resume(context.Background(), request); err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	waitForControlResumeJob(t, runtime, runID)

	final, present, err := readControlFinalOptional(runDir)
	if err != nil || !present {
		t.Fatalf("terminal diagnostic present=%v err=%v", present, err)
	}
	if final.RunID != runID || final.Status != RunStatusFailed || final.ExitCode != ExitPreflightFailed || final.Summary == "" {
		t.Fatalf("terminal diagnostic = %#v", final)
	}
	state, err := readControlRunState(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != string(RunStatusFailed) || state.RecoveryStatus != "resume_failed" {
		t.Fatalf("terminal resume state = %#v", state)
	}
	locator, err := resolveStateLocator(repo, stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := readControlApprovalLedger(filepath.Join(locator.RepoRoot, controlApprovalLedgerName))
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger.Resumes) != 1 || ledger.Resumes[0].Nonce != request.Approval.Nonce {
		t.Fatalf("consumed resume approval = %#v", ledger.Resumes)
	}
}

func TestControlRuntimeResumeFailsAfterClose(t *testing.T) {
	runtime := NewControlRuntime(ControlService{}, DefaultConfig(), nil)
	runtime.Close()

	_, err := runtime.Resume(context.Background(), ControlResumeRequest{})
	resumeErr, ok := err.(*ControlResumeError)
	if !ok || resumeErr.ReasonCode != "runtime_closed" {
		t.Fatalf("Resume after Close error = %#v, want runtime_closed", err)
	}
}

func validResumeApproval(t *testing.T, request ControlResumeRequest, nonce string) ControlApproval {
	t.Helper()
	digest, err := ControlResumeActionDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	return ControlApproval{ActionDigest: digest, ApprovedAt: now.Add(-time.Second), ExpiresAt: now.Add(5 * time.Minute), Nonce: nonce}
}

func assertControlResumeError(t *testing.T, err error, wantCode string) {
	t.Helper()
	var resumeErr *ControlResumeError
	if err == nil || !errors.As(err, &resumeErr) {
		t.Fatalf("error = %v, want ControlResumeError/%s", err, wantCode)
	}
	if resumeErr.ReasonCode != wantCode || resumeErr.Reason == "" {
		t.Fatalf("resume error = %#v, want code %q and bounded reason", resumeErr, wantCode)
	}
}

func waitForControlResumeJob(t *testing.T, runtime *ControlRuntime, runID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runtime.mu.Lock()
		_, active := runtime.jobs[runID]
		runtime.mu.Unlock()
		if !active {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("resume job %q did not finish", runID)
}

func controlContainsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
