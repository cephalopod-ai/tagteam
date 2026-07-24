package tagteam

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func assertControlStartError(t *testing.T, err error, wantCode string) {
	t.Helper()
	var startErr *ControlStartError
	if err == nil || !errors.As(err, &startErr) {
		t.Fatalf("error = %v, want ControlStartError/%s", err, wantCode)
	}
	if startErr.ReasonCode != wantCode || startErr.Reason == "" {
		t.Fatalf("start error = %#v, want code %q and bounded reason", startErr, wantCode)
	}
	if !startErr.Recoverable {
		t.Fatalf("start error %#v is not marked recoverable", startErr)
	}
}

// TestControlStartDigestBindsLaunchIdentityFields proves the start approval
// digest binds the selected roles, scope, prompt, rounds, and time budget, so a
// changed action can never reuse an approval issued for a different launch.
func TestControlStartDigestBindsLaunchIdentityFields(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	build := func() ControlStartRequest {
		request := ControlStartRequest{SchemaVersion: ControlContractVersion, Launch: controlLaunchFixture(t, repo), IdempotencyKey: "session-1-generation-1"}
		request.Launch.TestPreset = ""
		return request
	}
	baseDigest, err := ControlStartActionDigest(build())
	if err != nil {
		t.Fatal(err)
	}
	mutations := []struct {
		name   string
		mutate func(*ControlStartRequest)
	}{
		{"worker adapter", func(r *ControlStartRequest) { r.Launch.Team.Worker.Adapter = "grok" }},
		{"worker model", func(r *ControlStartRequest) { r.Launch.Team.Worker.Model = "different-model" }},
		{"supervisor model", func(r *ControlStartRequest) { r.Launch.Team.Supervisor.Model = "different-model" }},
		{"allowed paths", func(r *ControlStartRequest) { r.Launch.AllowedPaths = []string{"README.md"} }},
		{"prompt", func(r *ControlStartRequest) { r.Launch.Prompt = "a completely different task" }},
		{"rounds", func(r *ControlStartRequest) { r.Launch.Rounds = 5 }},
		{"invocation budget", func(r *ControlStartRequest) { r.Launch.TimeBudget.InvocationTimeoutSeconds = 123 }},
		{"idempotency key", func(r *ControlStartRequest) { r.IdempotencyKey = "session-1-generation-2" }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			request := build()
			mutation.mutate(&request)
			digest, err := ControlStartActionDigest(request)
			if err != nil {
				t.Fatal(err)
			}
			if digest == baseDigest {
				t.Fatalf("start digest did not bind %s", mutation.name)
			}
		})
	}
}

// TestControlRunActionDigestsBindRepositoryAndRun proves resume and cancel
// digests bind the canonical repository identity and run identity, and that a
// resume approval can never be replayed as a cancel (or vice versa).
func TestControlRunActionDigestsBindRepositoryAndRun(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	other, _ := createResumeFixtureRepo(t)
	repository, err := resolveControlRepository(repo)
	if err != nil {
		t.Fatal(err)
	}
	otherRepository, err := resolveControlRepository(other)
	if err != nil {
		t.Fatal(err)
	}
	resume, err := ControlResumeActionDigest(ControlResumeRequest{SchemaVersion: ControlContractVersion, Repository: repository, RunID: "run-1"})
	if err != nil {
		t.Fatal(err)
	}
	cancel, err := ControlCancelActionDigest(ControlCancelRequest{SchemaVersion: ControlContractVersion, Repository: repository, RunID: "run-1"})
	if err != nil {
		t.Fatal(err)
	}
	otherRun, err := ControlResumeActionDigest(ControlResumeRequest{SchemaVersion: ControlContractVersion, Repository: repository, RunID: "run-2"})
	if err != nil {
		t.Fatal(err)
	}
	otherRepo, err := ControlResumeActionDigest(ControlResumeRequest{SchemaVersion: ControlContractVersion, Repository: otherRepository, RunID: "run-1"})
	if err != nil {
		t.Fatal(err)
	}
	if resume == cancel {
		t.Fatal("resume and cancel digests collide for the same run")
	}
	if resume == otherRun {
		t.Fatal("resume digest did not bind run identity")
	}
	if resume == otherRepo {
		t.Fatal("resume digest did not bind repository identity")
	}
}

// writeControlLedgerWithNonce persists a control approval ledger that has
// already consumed nonce for the named action section, simulating a prior
// approved operation whose nonce a replayed request tries to reuse.
func writeControlLedgerWithNonce(t *testing.T, repo, stateRoot, section, nonce string) {
	t.Helper()
	locator, err := resolveStateLocator(repo, stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := locator.Prepare(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	ledger := controlApprovalLedger{SchemaVersion: ControlContractVersion}
	switch section {
	case "starts":
		ledger.Starts = []controlStartRecord{{IdempotencyKey: "prior-key", ActionDigest: "prior-digest", Nonce: nonce, RunID: "prior-run", CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute)}}
	case "resumes":
		ledger.Resumes = []controlResumeRecord{{ActionDigest: "prior-digest", Nonce: nonce, RunID: "prior-run", CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute)}}
	case "cancels":
		ledger.Cancels = []controlCancelRecord{{ActionDigest: "prior-digest", Nonce: nonce, RunID: "prior-run", CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute)}}
	default:
		t.Fatalf("unknown ledger section %q", section)
	}
	if err := writeJSONDurable(filepath.Join(locator.RepoRoot, controlApprovalLedgerName), ledger, false, true); err != nil {
		t.Fatal(err)
	}
}

// cancellableRunFixture writes a running, unfinalized run owned by a stale PID
// so Cancel reaches the approval ledger rather than short-circuiting on a live
// owner or an already-terminal run.
func cancellableRunFixture(t *testing.T, repo, stateRoot, runID, baseline string) {
	t.Helper()
	runDir, err := createRunDir(repo, stateRoot, runID)
	if err != nil {
		t.Fatal(err)
	}
	writeResumeFixture(t, runDir, runID, repo, baseline, RunStatusRunning)
	if err := os.Remove(filepath.Join(runDir, "final.json")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	// A PID far above this process is not alive, so Cancel treats the recorded
	// owner as stale and proceeds to the durable cancellation path.
	if err := writeJSONDurable(filepath.Join(runDir, "run.lock"), runLockRecord{PID: os.Getpid() + 100000, CreatedAt: time.Now().UTC()}, true, true); err != nil {
		t.Fatal(err)
	}
}

func TestControlRuntimeStartRejectsNonceConsumedByAnotherAction(t *testing.T) {
	for _, section := range []string{"resumes", "cancels"} {
		t.Run(section, func(t *testing.T) {
			repo, _ := createResumeFixtureRepo(t)
			stateRoot := t.TempDir()
			const shared = "cross-action-nonce"
			writeControlLedgerWithNonce(t, repo, stateRoot, section, shared)
			runtime := NewControlRuntime(ControlService{RepositoryRoot: repo, StateRoot: stateRoot, ProducerVersion: "test"}, DefaultConfig(), nil)
			request := controlStartFixture(t, repo)
			request.Approval.Nonce = shared
			_, err := runtime.Start(context.Background(), request)
			assertControlStartError(t, err, "approval_nonce_replayed")
		})
	}
}

func TestControlRuntimeResumeRejectsNonceConsumedByAnotherAction(t *testing.T) {
	for _, section := range []string{"starts", "cancels"} {
		t.Run(section, func(t *testing.T) {
			repo, baseline := createResumeFixtureRepo(t)
			stateRoot := t.TempDir()
			runID := "resume-cross-action"
			runDir, err := createRunDir(repo, stateRoot, runID)
			if err != nil {
				t.Fatal(err)
			}
			writeResumeFixture(t, runDir, runID, repo, baseline, RunStatusRunning)
			const shared = "cross-action-nonce"
			writeControlLedgerWithNonce(t, repo, stateRoot, section, shared)
			repository, err := resolveControlRepository(repo)
			if err != nil {
				t.Fatal(err)
			}
			request := ControlResumeRequest{SchemaVersion: ControlContractVersion, Repository: repository, RunID: runID}
			request.Approval = validResumeApproval(t, request, shared)
			runtime := NewControlRuntime(ControlService{RepositoryRoot: repo, StateRoot: stateRoot, ProducerVersion: "test"}, DefaultConfig(), nil)
			_, err = runtime.Resume(context.Background(), request)
			assertControlResumeError(t, err, "approval_nonce_replayed")
		})
	}
}

func TestControlRuntimeCancelRejectsNonceConsumedByAnotherAction(t *testing.T) {
	for _, section := range []string{"starts", "resumes"} {
		t.Run(section, func(t *testing.T) {
			repo, baseline := createResumeFixtureRepo(t)
			stateRoot := t.TempDir()
			runID := "cancel-cross-action"
			cancellableRunFixture(t, repo, stateRoot, runID, baseline)
			const shared = "cross-action-nonce"
			writeControlLedgerWithNonce(t, repo, stateRoot, section, shared)
			repository, err := resolveControlRepository(repo)
			if err != nil {
				t.Fatal(err)
			}
			request := ControlCancelRequest{SchemaVersion: ControlContractVersion, Repository: repository, RunID: runID}
			request.Approval = validCancelApproval(t, request, shared)
			runtime := NewControlRuntime(ControlService{RepositoryRoot: repo, StateRoot: stateRoot, ProducerVersion: "test"}, DefaultConfig(), nil)
			_, err = runtime.Cancel(context.Background(), request)
			assertControlCancelError(t, err, "approval_nonce_replayed")
		})
	}
}

func TestControlRuntimeStartRejectsExpiredApproval(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	runtime := NewControlRuntime(ControlService{RepositoryRoot: repo, StateRoot: t.TempDir(), ProducerVersion: "test"}, DefaultConfig(), nil)
	request := controlStartFixture(t, repo)
	now := time.Now().UTC()
	request.Approval.ApprovedAt = now.Add(-10 * time.Minute)
	request.Approval.ExpiresAt = now.Add(-time.Minute)
	_, err := runtime.Start(context.Background(), request)
	assertControlStartError(t, err, "approval_expired")
}

func TestControlRuntimeStartRejectsApprovalLifetimeBeyondCap(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	runtime := NewControlRuntime(ControlService{RepositoryRoot: repo, StateRoot: t.TempDir(), ProducerVersion: "test"}, DefaultConfig(), nil)
	request := controlStartFixture(t, repo)
	request.Approval.ExpiresAt = request.Approval.ApprovedAt.Add(ControlApprovalMaxLifetime + time.Minute)
	_, err := runtime.Start(context.Background(), request)
	assertControlStartError(t, err, "approval_lifetime_exceeded")
}

func TestControlRuntimeCancelRejectsExpiredApproval(t *testing.T) {
	repo, baseline := createResumeFixtureRepo(t)
	stateRoot := t.TempDir()
	runID := "cancel-expired-approval"
	cancellableRunFixture(t, repo, stateRoot, runID, baseline)
	repository, err := resolveControlRepository(repo)
	if err != nil {
		t.Fatal(err)
	}
	request := ControlCancelRequest{SchemaVersion: ControlContractVersion, Repository: repository, RunID: runID}
	request.Approval = validCancelApproval(t, request, "cancel-expired-nonce")
	now := time.Now().UTC()
	request.Approval.ApprovedAt = now.Add(-10 * time.Minute)
	request.Approval.ExpiresAt = now.Add(-time.Minute)
	runtime := NewControlRuntime(ControlService{RepositoryRoot: repo, StateRoot: stateRoot, ProducerVersion: "test"}, DefaultConfig(), nil)
	_, err = runtime.Cancel(context.Background(), request)
	assertControlCancelError(t, err, "approval_expired")
}
