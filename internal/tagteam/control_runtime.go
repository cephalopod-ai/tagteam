package tagteam

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	controlApprovalLedgerName = "control-approvals.json"
	controlMaxApprovalRecords = 1024
	controlCancelRequestName  = "cancel-request.json"
)

type ControlRuntime struct {
	service ControlService
	config  Config
	sources []string

	lifecycleMu sync.RWMutex
	closeOnce   sync.Once
	workers     sync.WaitGroup

	mu             sync.Mutex
	closed         bool
	jobs           map[string]context.CancelFunc
	stewards       map[string]Steward
	terminalErrors map[string]error
	watcherCancel  context.CancelFunc
	watcherDone    chan struct{}
}

type controlApprovalLedger struct {
	SchemaVersion int                   `json:"schema_version"`
	Starts        []controlStartRecord  `json:"starts"`
	Resumes       []controlResumeRecord `json:"resumes,omitempty"`
	Cancels       []controlCancelRecord `json:"cancels,omitempty"`
}

type controlCancelRecord struct {
	ActionDigest string    `json:"action_digest"`
	Nonce        string    `json:"nonce"`
	RunID        string    `json:"run_id"`
	OwnerPID     int       `json:"owner_pid,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type controlCancelRequest struct {
	SchemaVersion int       `json:"schema_version"`
	ActionDigest  string    `json:"action_digest"`
	Nonce         string    `json:"nonce"`
	RunID         string    `json:"run_id"`
	OwnerPID      int       `json:"owner_pid,omitempty"`
	RequestedAt   time.Time `json:"requested_at"`
}

// ControlCancelError is a bounded, recoverable error returned by the MCP
// cancel operation. Its reason code is stable enough for a host to decide
// whether to request another approval or report a persisted run problem.
type ControlCancelError struct {
	ReasonCode  string
	Reason      string
	Recoverable bool
	Err         error
}

func (e *ControlCancelError) Error() string {
	if e == nil {
		return ""
	}
	if e.Reason == "" {
		return "cancel " + e.ReasonCode
	}
	return "cancel " + e.ReasonCode + ": " + e.Reason
}

func (e *ControlCancelError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func newControlCancelError(reasonCode, reason string, cause error) error {
	if reasonCode == "" {
		reasonCode = "cancel_unavailable"
	}
	if reason == "" && cause != nil {
		reason = cause.Error()
	}
	return &ControlCancelError{ReasonCode: reasonCode, Reason: boundControlText(reason), Recoverable: true, Err: cause}
}

type controlStartRecord struct {
	IdempotencyKey string    `json:"idempotency_key"`
	ActionDigest   string    `json:"action_digest"`
	Nonce          string    `json:"nonce"`
	RunID          string    `json:"run_id"`
	CreatedAt      time.Time `json:"created_at"`
	ExpiresAt      time.Time `json:"expires_at"`
}

func NewControlRuntime(service ControlService, cfg Config, sources []string) *ControlRuntime {
	return &ControlRuntime{
		service:        service,
		config:         cfg,
		sources:        append([]string(nil), sources...),
		jobs:           map[string]context.CancelFunc{},
		stewards:       map[string]Steward{},
		terminalErrors: map[string]error{},
	}
}

func (r *ControlRuntime) Capabilities() ControlCapabilitySet {
	capabilities := r.service.Capabilities()
	capabilities.Capabilities = append(capabilities.Capabilities, "start", "resume", "cancel", "advise")
	return capabilities
}

func (r *ControlRuntime) Status(runID string) (ControlStatus, error) {
	status, err := r.service.Status(runID)
	if err == nil {
		return status, nil
	}
	if terminalErr := r.terminalError(runID); terminalErr != nil {
		return ControlStatus{}, terminalErr
	}
	locator, locatorErr := resolveStateLocator(r.service.RepositoryRoot, r.service.StateRoot)
	if locatorErr != nil {
		return ControlStatus{}, err
	}
	ledger, ledgerErr := readControlApprovalLedger(filepath.Join(locator.RepoRoot, controlApprovalLedgerName))
	if ledgerErr != nil {
		return ControlStatus{}, ledgerErr
	}
	for _, record := range ledger.Starts {
		if record.RunID != runID {
			continue
		}
		runDir, runDirErr := locator.RunDir(runID)
		if runDirErr != nil {
			return ControlStatus{}, runDirErr
		}
		snapshot := RunSnapshot{SchemaVersion: ArtifactSchemaVersion, RunID: runID, RunDir: runDir, Status: string(RunStatusRunning), Phase: "preflight", UpdatedAt: record.CreatedAt}
		payload, marshalErr := json.Marshal(snapshot)
		if marshalErr != nil {
			return ControlStatus{}, marshalErr
		}
		return ControlStatus{SchemaVersion: ControlContractVersion, SnapshotID: sha256Hex(payload), Completeness: ControlPartial, Run: snapshot}, nil
	}
	return ControlStatus{}, err
}

func (r *ControlRuntime) Start(ctx context.Context, request ControlStartRequest) (ControlRunHandle, error) {
	r.lifecycleMu.RLock()
	defer r.lifecycleMu.RUnlock()
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return ControlRunHandle{}, newControlStartError("runtime_closed", "control runtime is shutting down", nil)
	}
	if request.SchemaVersion != ControlContractVersion {
		return ControlRunHandle{}, newControlStartError("invalid_request", fmt.Sprintf("unsupported control schema_version %d (want %d)", request.SchemaVersion, ControlContractVersion), nil)
	}
	normalized, err := NormalizeControlLaunch(request.Launch)
	if err != nil {
		return ControlRunHandle{}, newControlStartError("invalid_launch", err.Error(), err)
	}
	if err := r.service.requireRepository(normalized.Repository); err != nil {
		return ControlRunHandle{}, newControlStartError("repository_mismatch", err.Error(), err)
	}
	request.Launch = normalized
	digest, err := ControlStartActionDigest(request)
	if err != nil {
		return ControlRunHandle{}, newControlStartError("invalid_request", err.Error(), err)
	}
	if err := validateControlApproval(request.Approval, digest); err != nil {
		return ControlRunHandle{}, err
	}
	opts, err := r.optionsForLaunch(normalized)
	if err != nil {
		return ControlRunHandle{}, newControlStartError("start_configuration_invalid", err.Error(), err)
	}
	locator, err := resolveStateLocator(opts.Workdir, opts.StateRoot)
	if err != nil {
		return ControlRunHandle{}, newControlStartError("state_root_unavailable", fmt.Sprintf("resolve start state root: %v", err), err)
	}
	// Reject escaping state-tree symlinks before creating or opening runs.
	if _, err := ensureCanonicalRunsRoot(locator); err != nil {
		return ControlRunHandle{}, newControlStartError("runs_root_invalid", fmt.Sprintf("resolve start runs root: %v", err), err)
	}
	if err := locator.Prepare(); err != nil {
		return ControlRunHandle{}, newControlStartError("state_root_unavailable", fmt.Sprintf("prepare start state root: %v", err), err)
	}
	if _, err := ensureCanonicalRunsRoot(locator); err != nil {
		return ControlRunHandle{}, newControlStartError("runs_root_invalid", fmt.Sprintf("resolve start runs root: %v", err), err)
	}
	// Fail closed if the capability surface drifted outside the approved baseline.
	if err := r.verifyCapabilityBaseline(locator.RepoRoot); err != nil {
		return ControlRunHandle{}, newControlStartError("capability_quarantined", err.Error(), err)
	}
	lock, err := acquireRunLock(locator.RepoRoot, false)
	if err != nil {
		return ControlRunHandle{}, newControlStartError("approval_ledger_locked", fmt.Sprintf("control approval ledger is locked: %v", err), err)
	}
	defer lock.Release()
	ledgerPath := filepath.Join(locator.RepoRoot, controlApprovalLedgerName)
	ledger, err := readControlApprovalLedger(ledgerPath)
	if err != nil {
		return ControlRunHandle{}, newControlStartError("approval_ledger_invalid", err.Error(), err)
	}
	ledger.Starts = pruneControlStartRecords(ledger.Starts, time.Now().UTC())
	for _, record := range ledger.Starts {
		if record.IdempotencyKey != request.IdempotencyKey {
			continue
		}
		if record.ActionDigest != digest {
			return ControlRunHandle{}, newControlStartError("idempotency_conflict", "idempotency_key is already bound to a different start action", nil)
		}
		return ControlRunHandle{SchemaVersion: ControlContractVersion, RunID: record.RunID, ProducerVersion: normalizedProducerVersion(r.service.ProducerVersion)}, nil
	}
	if active, activeErr := readActiveAt(filepath.Join(locator.RepoRoot, "active.json")); activeErr == nil && active.Status == string(RunStatusRunning) {
		return ControlRunHandle{}, newControlStartError("run_already_active", fmt.Sprintf("run %q is already active for this worktree", active.RunID), nil)
	} else if activeErr != nil && !os.IsNotExist(activeErr) {
		return ControlRunHandle{}, newControlStartError("active_state_invalid", fmt.Sprintf("read active run: %v", activeErr), activeErr)
	}
	// Approval nonces are single-use across every action; reject a nonce already
	// consumed by any start, resume, or cancel. The idempotent start-replay path
	// above returns before this check, so any match here is a genuine replay.
	if controlLedgerHasNonce(ledger, request.Approval.Nonce) {
		return ControlRunHandle{}, newControlStartError("approval_nonce_replayed", "approval nonce has already been consumed", nil)
	}
	for _, record := range ledger.Starts {
		active, activeErr := controlStartRecordActive(locator, record, time.Now().UTC())
		if activeErr != nil {
			return ControlRunHandle{}, newControlStartError("start_reservation_invalid", activeErr.Error(), activeErr)
		}
		if active {
			return ControlRunHandle{}, newControlStartError("run_already_active", fmt.Sprintf("run %q is already pending or active for this worktree", record.RunID), nil)
		}
	}
	runID, err := nextControlRunID(locator)
	if err != nil {
		return ControlRunHandle{}, newControlStartError("run_id_unavailable", err.Error(), err)
	}
	ledger.Starts = append(ledger.Starts, controlStartRecord{
		IdempotencyKey: request.IdempotencyKey,
		ActionDigest:   digest,
		Nonce:          request.Approval.Nonce,
		RunID:          runID,
		CreatedAt:      time.Now().UTC(),
		ExpiresAt:      request.Approval.ExpiresAt,
	})
	if len(ledger.Starts) > controlMaxApprovalRecords {
		return ControlRunHandle{}, newControlStartError("approval_ledger_full", "control approval ledger reached its maximum retained records", nil)
	}
	if err := writeJSONDurable(ledgerPath, ledger, false, true); err != nil {
		return ControlRunHandle{}, newControlStartError("approval_ledger_write_failed", fmt.Sprintf("persist consumed control approval: %v", err), err)
	}

	opts.RunID = runID
	runContext, cancel := context.WithCancel(ctx)
	r.startJob(runID, cancel, func() { r.runStart(runContext, opts, runID) })
	return ControlRunHandle{SchemaVersion: ControlContractVersion, RunID: runID, ProducerVersion: normalizedProducerVersion(r.service.ProducerVersion)}, nil
}

// Cancel records an approved cancellation request next to the run and asks a
// locally-owned job to stop. A live run owned by another runtime is rejected;
// a fresh ControlRuntime can only cancel a run whose recorded owner is stale.
func (r *ControlRuntime) Cancel(ctx context.Context, request ControlCancelRequest) (ControlRunHandle, error) {
	if request.SchemaVersion != ControlContractVersion {
		return ControlRunHandle{}, newControlCancelError("invalid_request", fmt.Sprintf("unsupported control schema_version %d (want %d)", request.SchemaVersion, ControlContractVersion), nil)
	}
	digest, err := ControlCancelActionDigest(request)
	if err != nil {
		return ControlRunHandle{}, newControlCancelError("invalid_request", err.Error(), err)
	}
	repository, err := resolveControlRepository(request.Repository.CanonicalRoot)
	if err != nil {
		return ControlRunHandle{}, newControlCancelError("repository_unavailable", err.Error(), err)
	}
	if err := r.service.requireRepository(repository); err != nil {
		return ControlRunHandle{}, newControlCancelError("repository_mismatch", err.Error(), err)
	}
	request.Repository = repository
	if err := validateControlCancelApproval(request.Approval, digest); err != nil {
		return ControlRunHandle{}, err
	}
	select {
	case <-ctx.Done():
		return ControlRunHandle{}, newControlCancelError("request_cancelled", ctx.Err().Error(), ctx.Err())
	default:
	}

	locator, err := resolveStateLocator(repository.CanonicalRoot, r.service.StateRoot)
	if err != nil {
		return ControlRunHandle{}, newControlCancelError("state_root_unavailable", err.Error(), err)
	}
	if err := r.verifyCapabilityBaseline(locator.RepoRoot); err != nil {
		return ControlRunHandle{}, newControlCancelError("capability_quarantined", err.Error(), err)
	}
	runDir, ownerPID, terminal, err := controlCancelTarget(locator, repository.CanonicalRoot, request.RunID)
	if err != nil {
		return ControlRunHandle{}, newControlCancelError("cancel_unavailable", err.Error(), err)
	}
	if terminal {
		return controlRunHandle(r.service.ProducerVersion, request.RunID), nil
	}
	jobCancel := r.localJobCancel(request.RunID)
	if jobCancel == nil && ownerPID > 0 && processAlive(ownerPID) {
		return ControlRunHandle{}, newControlCancelError("run_not_owned", fmt.Sprintf("live run %q is not owned by this MCP runtime", request.RunID), nil)
	}

	lock, err := acquireRunLock(locator.RepoRoot, false)
	if err != nil {
		return ControlRunHandle{}, newControlCancelError("approval_ledger_locked", err.Error(), err)
	}
	ledgerPath := filepath.Join(locator.RepoRoot, controlApprovalLedgerName)
	ledger, err := readControlApprovalLedger(ledgerPath)
	if err != nil {
		_ = lock.Release()
		return ControlRunHandle{}, newControlCancelError("approval_ledger_invalid", err.Error(), err)
	}
	if handle, cancelErr := controlCancelLedgerResult(ledger, request, digest); handle.RunID != "" || cancelErr != nil {
		handle.ProducerVersion = normalizedProducerVersion(r.service.ProducerVersion)
		_ = lock.Release()
		return handle, cancelErr
	}
	if len(ledger.Cancels) >= controlMaxApprovalRecords {
		_ = lock.Release()
		return ControlRunHandle{}, newControlCancelError("approval_ledger_full", "cancel approval ledger reached its maximum retained records", nil)
	}
	now := time.Now().UTC()
	ledger.Cancels = append(ledger.Cancels, controlCancelRecord{ActionDigest: digest, Nonce: request.Approval.Nonce, RunID: request.RunID, OwnerPID: ownerPID, CreatedAt: now, ExpiresAt: request.Approval.ExpiresAt})
	if err := writeJSONDurable(ledgerPath, ledger, false, true); err != nil {
		_ = lock.Release()
		return ControlRunHandle{}, newControlCancelError("approval_ledger_write_failed", fmt.Sprintf("persist consumed cancel approval: %v", err), err)
	}
	if err := lock.Release(); err != nil {
		return ControlRunHandle{}, newControlCancelError("approval_ledger_unlock_failed", err.Error(), err)
	}

	requestRecord := controlCancelRequest{SchemaVersion: ControlContractVersion, ActionDigest: digest, Nonce: request.Approval.Nonce, RunID: request.RunID, OwnerPID: ownerPID, RequestedAt: now}
	// Re-resolve under the runs-root boundary immediately before mutation so a
	// run-directory replacement after controlCancelTarget cannot redirect I/O.
	var cancelPath string
	runDir, cancelPath, _, err = resolveControlCancelIO(repository.CanonicalRoot, r.service.StateRoot, request.RunID)
	if err != nil {
		return ControlRunHandle{}, newControlCancelError("cancel_request_path_invalid", err.Error(), err)
	}
	if err := writeJSONDurable(cancelPath, requestRecord, true, true); err != nil {
		return ControlRunHandle{}, newControlCancelError("cancel_request_write_failed", fmt.Sprintf("persist cancellation request: %v", err), err)
	}
	if jobCancel != nil {
		jobCancel()
	} else if ownerPID <= 0 || !processAlive(ownerPID) {
		if err := r.persistControlCancellation(repository.CanonicalRoot, runDir, request.RunID); err != nil {
			return ControlRunHandle{}, newControlCancelError("cancel_status_write_failed", err.Error(), err)
		}
	}
	return controlRunHandle(r.service.ProducerVersion, request.RunID), nil
}

func (r *ControlRuntime) localJobCancel(runID string) context.CancelFunc {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.jobs[runID]
}

func validateControlCancelApproval(approval ControlApproval, expectedDigest string) error {
	if strings.TrimSpace(approval.Nonce) == "" {
		return newControlCancelError("approval_missing", "approval nonce is required", nil)
	}
	if approval.ActionDigest != expectedDigest {
		return newControlCancelError("approval_action_mismatch", "approval does not match the normalized cancel action", nil)
	}
	if strings.TrimSpace(approval.Nonce) != approval.Nonce || len(approval.Nonce) > controlMaxRoleBytes || containsControl(approval.Nonce) {
		return newControlCancelError("approval_invalid", fmt.Sprintf("approval nonce must be a normalized identifier no longer than %d bytes", controlMaxRoleBytes), nil)
	}
	now := time.Now().UTC()
	if approval.ApprovedAt.IsZero() || approval.ExpiresAt.IsZero() || approval.ApprovedAt.After(now) {
		return newControlCancelError("approval_invalid", "approval timestamps are invalid", nil)
	}
	if approval.ExpiresAt.Sub(approval.ApprovedAt) > ControlApprovalMaxLifetime {
		return newControlCancelError("approval_lifetime_exceeded", fmt.Sprintf("approval must expire within %s", ControlApprovalMaxLifetime), nil)
	}
	if !approval.ExpiresAt.After(now) {
		return newControlCancelError("approval_expired", "approval has expired", nil)
	}
	return nil
}

func controlCancelLedgerResult(ledger controlApprovalLedger, request ControlCancelRequest, digest string) (ControlRunHandle, error) {
	for _, record := range ledger.Starts {
		if record.Nonce == request.Approval.Nonce {
			return ControlRunHandle{}, newControlCancelError("approval_nonce_replayed", "approval nonce has already been consumed for another action", nil)
		}
	}
	for _, record := range ledger.Resumes {
		if record.Nonce == request.Approval.Nonce {
			return ControlRunHandle{}, newControlCancelError("approval_nonce_replayed", "approval nonce has already been consumed for another action", nil)
		}
	}
	for _, record := range ledger.Cancels {
		if record.Nonce == request.Approval.Nonce {
			if record.RunID == request.RunID && record.ActionDigest == digest {
				return controlRunHandle("", record.RunID), nil
			}
			return ControlRunHandle{}, newControlCancelError("approval_nonce_replayed", "approval nonce has already been consumed for another cancel action", nil)
		}
		if record.RunID == request.RunID {
			return ControlRunHandle{}, newControlCancelError("cancel_already_consumed", "a cancel approval has already been consumed for this run", nil)
		}
	}
	return ControlRunHandle{}, nil
}

func controlCancelTarget(locator StateLocator, repository, runID string) (string, int, bool, error) {
	runDir, _, err := resolveControlRunDirectory(locator.Workdir, locator.StateRoot, runID)
	if err != nil {
		return "", 0, false, err
	}
	if final, present, finalErr := readControlFinalOptional(runDir); finalErr != nil {
		return "", 0, false, fmt.Errorf("read persisted final result: %v", finalErr)
	} else if present && final.RunID != "" {
		if final.RunID != runID {
			return "", 0, false, fmt.Errorf("persisted final result does not match the requested run")
		}
		if err := controlRunWorkdirMatches(repository, final.Workdir, "final result"); err != nil {
			return "", 0, false, err
		}
		if final.Status == RunStatusCancelled {
			return runDir, 0, true, nil
		}
		return "", 0, false, fmt.Errorf("run %q is already terminal", runID)
	}
	if data, present, stateErr := readControlOptionalArtifactBytes(runDir, "state.json"); stateErr != nil {
		return "", 0, false, fmt.Errorf("read run state: %v", stateErr)
	} else if present {
		var state RunState
		if err := json.Unmarshal(data, &state); err != nil {
			return "", 0, false, fmt.Errorf("read run state: %v", err)
		}
		if state.RunID != "" && state.RunID != runID {
			return "", 0, false, fmt.Errorf("persisted state run_id does not match the requested run")
		}
		if state.Workdir != "" {
			if err := controlRunWorkdirMatches(repository, state.Workdir, "state"); err != nil {
				return "", 0, false, err
			}
		}
		if state.Status != "" && state.Status != string(RunStatusRunning) {
			return "", 0, false, fmt.Errorf("run %q is already terminal", runID)
		}
	}
	ownerPID := 0
	if data, present, lockErr := readControlOptionalArtifactBytes(runDir, "run.lock"); lockErr != nil {
		return "", 0, false, fmt.Errorf("read run owner: %v", lockErr)
	} else if present {
		var record runLockRecord
		if err := json.Unmarshal(data, &record); err != nil || record.PID <= 0 {
			return "", 0, false, fmt.Errorf("run lock does not contain a valid owner")
		}
		ownerPID = record.PID
	}
	return runDir, ownerPID, false, nil
}

func (r *ControlRuntime) persistControlCancellation(repository, runDir, runID string) error {
	// Re-resolve immediately before mutation so a replaced run directory or
	// artifact symlink cannot redirect cancel status outside the runs root.
	resolved, _, err := resolveControlRunDirectory(r.service.RepositoryRoot, r.service.StateRoot, runID)
	if err != nil {
		return err
	}
	runDir = resolved
	if err := ensureControlWritableArtifacts(runDir, "state.json", "final.json", "meta.json"); err != nil {
		return err
	}
	if final, present, err := readControlFinalOptional(runDir); err != nil {
		return err
	} else if present && final.Status == RunStatusCancelled {
		return nil
	}
	state, err := readControlRunState(runDir)
	if err != nil {
		return fmt.Errorf("read cancelled run state: %w", err)
	}
	meta, err := readControlMeta(runDir)
	if err != nil {
		return fmt.Errorf("read cancelled run metadata: %w", err)
	}
	workdir := repository
	baseline := state.BaselineSHA
	if meta.Workdir != "" {
		workdir = meta.Workdir
	}
	if baseline == "" {
		baseline = meta.Baseline
	}
	final := FinalRun{SchemaVersion: ArtifactSchemaVersion, RunID: runID, RunDir: runDir, Workdir: workdir, Baseline: baseline, Mode: state.Mode, Verdict: "cancelled", Summary: "run cancelled by MCP control request", Status: RunStatusCancelled, BlockingReason: string(ReasonCancelled), ExitCode: ExitPreflightFailed, StartedAt: meta.StartedAt, FinishedAt: time.Now().UTC()}
	terminalState := RunState{RunID: runID, Mode: state.Mode, Status: string(RunStatusCancelled), Phase: state.Phase, Workdir: workdir, BaselineSHA: baseline, BlockingReason: string(ReasonCancelled), ExitCode: final.ExitCode}
	if err := NewApp(r.config).persistTerminalRun(workdir, &final, terminalState); err != nil {
		return err
	}
	if err := finalizeActiveRun(workdir, runID, string(RunStatusCancelled)); err != nil {
		return mandatoryPersistenceError("cancelled active run", err)
	}
	return nil
}

// watchControlCancellation is intentionally file-backed. The runtime that
// owns a job keeps the actual context and child-process cancellation authority,
// while the request remains durable for diagnostics and stale-owner recovery.
func (r *ControlRuntime) watchControlCancellation(ctx context.Context, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		r.mu.Lock()
		jobs := make(map[string]context.CancelFunc, len(r.jobs))
		for runID, cancel := range r.jobs {
			jobs[runID] = cancel
		}
		r.mu.Unlock()
		for runID, cancel := range jobs {
			// Re-resolve under the runs-root boundary each tick; do not trust a
			// previously resolved run path as the validation root.
			runDir, _, _, err := resolveControlCancelIO(r.service.RepositoryRoot, r.service.StateRoot, runID)
			if err != nil {
				continue
			}
			data, err := readControlArtifactBytes(runDir, controlCancelRequestName)
			if err != nil {
				continue
			}
			var request controlCancelRequest
			if json.Unmarshal(data, &request) == nil && request.RunID == runID && request.ActionDigest != "" && request.Nonce != "" && (request.OwnerPID == 0 || request.OwnerPID == os.Getpid()) {
				cancel()
			}
		}
	}
}

func (r *ControlRuntime) runStart(ctx context.Context, opts RunOptions, runID string) {
	final, err := NewApp(r.config).Run(ctx, opts)
	if err == nil || final.RunID != "" {
		return
	}
	// The normal runner creates its directory after preflight. Persist an
	// observable terminal artifact when preflight itself fails.
	if persistErr := r.persistStartFailure(opts, runID, err); persistErr != nil {
		r.recordTerminalError(runID, persistErr)
	}
}

func (r *ControlRuntime) persistStartFailure(opts RunOptions, runID string, cause error) error {
	runDir, err := createRunDir(opts.Workdir, opts.StateRoot, runID)
	if err != nil {
		return mandatoryPersistenceError("MCP start failure directory", err)
	}
	message := redactSecretsWithOverlay(cause.Error(), opts.EnvOverlay)
	final := FinalRun{
		SchemaVersion:  ArtifactSchemaVersion,
		RunID:          runID,
		RunDir:         runDir,
		Workdir:        opts.Workdir,
		Mode:           opts.Mode,
		Verdict:        "error",
		Summary:        message,
		Status:         RunStatusFailed,
		BlockingReason: string(reasonForExit(ExitCode(cause))),
		ExitCode:       ExitCode(cause),
		StartedAt:      time.Now().UTC(),
		FinishedAt:     time.Now().UTC(),
	}
	if final.BlockingReason == "" {
		final.BlockingReason = string(ReasonWorkerUnavailable)
	}
	state := runStateForFinal(final, opts.Mode, "preflight", "start_failed")
	return NewApp(r.config).persistTerminalRun(opts.Workdir, &final, state)
}

func (r *ControlRuntime) optionsForLaunch(spec ControlLaunchSpec) (RunOptions, error) {
	// Re-resolve repository identity and scopes immediately before mutation so
	// approval digests cannot race against symlink replacement under the tree.
	// Only host-derived Workdir and AllowedPaths enter RunOptions.
	repository, err := resolveControlRepository(spec.Repository.CanonicalRoot)
	if err != nil {
		return RunOptions{}, err
	}
	if err := r.service.requireRepository(repository); err != nil {
		return RunOptions{}, err
	}
	// Revalidate the approved canonical list itself. Re-canonicalizing first
	// and revalidating the new result would accept an in-repo scope symlink
	// retarget (approved internal/ -> other/) after preparation.
	if err := revalidateControlAllowedPaths(repository.CanonicalRoot, spec.AllowedPaths); err != nil {
		return RunOptions{}, err
	}
	flags := FlagInputs{
		Mode:            string(spec.Team.Mode),
		Workdir:         repository.CanonicalRoot,
		StateRoot:       r.service.StateRoot,
		AllowedPaths:    append([]string(nil), spec.AllowedPaths...),
		Rounds:          spec.Rounds,
		Timeout:         time.Duration(spec.TimeBudget.InvocationTimeoutSeconds) * time.Second,
		WatchdogTimeout: time.Duration(spec.TimeBudget.WatchdogTimeoutSeconds) * time.Second,
		MaxWallTime:     time.Duration(spec.TimeBudget.WallTimeoutSeconds) * time.Second,
	}
	changed := map[string]bool{
		"mode":             true,
		"allow-path":       true,
		"rounds":           true,
		"watchdog-timeout": true,
		"max-wall-time":    true,
	}
	if strings.TrimSpace(r.service.StateRoot) != "" {
		changed["state-root"] = true
	}
	switch spec.Team.Mode {
	case ModeSupervisor:
		flags.Worker = controlRoleTargetString(*spec.Team.Worker)
		flags.Supervisor = controlRoleTargetString(*spec.Team.Supervisor)
		changed["worker"] = true
		changed["supervisor"] = true
	case ModeRelay:
		flags.CoderRole = controlRoleTargetString(*spec.Team.Coder)
		flags.Supervisor = controlRoleTargetString(*spec.Team.Supervisor)
		flags.Scout = controlRoleTargetString(*spec.Team.Scout)
		changed["coder"] = true
		changed["supervisor"] = true
		changed["scout"] = true
	case ModeAdversarial:
		flags.CoderRole = controlRoleTargetString(*spec.Team.Coder)
		flags.Reviewer = controlRoleTargetString(*spec.Team.Reviewer)
		changed["coder"] = true
		changed["reviewer"] = true
	case ModeSolo:
		flags.Solo = controlRoleTargetString(*spec.Team.Worker)
		changed["solo"] = true
	default:
		return RunOptions{}, fmt.Errorf("unsupported control mode %q", spec.Team.Mode)
	}
	if spec.TestPreset != "" {
		// Name is already bound into the start action digest; resolve the
		// command solely from the host-trusted registry (never raw caller input).
		preset, ok := r.config.TestPresets[spec.TestPreset]
		if !ok {
			return RunOptions{}, fmt.Errorf("unknown test_preset %q", spec.TestPreset)
		}
		flags.Test = preset.Command
		changed["test"] = true
		if preset.IdentityRegex != "" {
			flags.TestIdentityRegex = preset.IdentityRegex
			changed["test-identity-regex"] = true
		}
	}
	opts, err := ResolveOptions(r.config, r.sources, flags, changed, spec.Prompt)
	if err != nil {
		return RunOptions{}, err
	}
	return opts, nil
}

func controlRoleTargetString(target ControlRoleTarget) string {
	if target.Model == "" {
		return target.Adapter
	}
	return target.Adapter + ":" + target.Model
}

func readControlApprovalLedger(path string) (controlApprovalLedger, error) {
	ledger := controlApprovalLedger{SchemaVersion: ControlContractVersion, Starts: []controlStartRecord{}, Resumes: []controlResumeRecord{}, Cancels: []controlCancelRecord{}}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ledger, nil
	}
	if err != nil {
		return controlApprovalLedger{}, fmt.Errorf("read control approval ledger: %w", err)
	}
	if err := json.Unmarshal(data, &ledger); err != nil {
		return controlApprovalLedger{}, fmt.Errorf("decode control approval ledger: %w", err)
	}
	if ledger.SchemaVersion != ControlContractVersion {
		return controlApprovalLedger{}, fmt.Errorf("unsupported control approval ledger schema_version %d", ledger.SchemaVersion)
	}
	return ledger, nil
}

func pruneControlStartRecords(records []controlStartRecord, now time.Time) []controlStartRecord {
	result := make([]controlStartRecord, 0, len(records))
	for _, record := range records {
		if record.ExpiresAt.After(now) {
			result = append(result, record)
		}
	}
	return result
}

// controlStartRecordActive closes the short gap between a durable approval
// reservation and the runner creating active.json. A terminal final artifact
// releases the worktree; an expired reservation may be retried with new input.
func controlStartRecordActive(locator StateLocator, record controlStartRecord, now time.Time) (bool, error) {
	if !record.ExpiresAt.After(now) {
		return false, nil
	}
	runDir, err := locator.RunDir(record.RunID)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(filepath.Join(runDir, "final.json"))
	if err == nil {
		return false, nil
	}
	if os.IsNotExist(err) {
		return true, nil
	}
	return false, fmt.Errorf("check control run %q terminal record: %w", record.RunID, err)
}

func nextControlRunID(locator StateLocator) (string, error) {
	for range 8 {
		entropy := make([]byte, 4)
		if _, err := rand.Read(entropy); err != nil {
			return "", fmt.Errorf("generate control run identifier: %w", err)
		}
		runID := newRunID() + "-mcp-" + hex.EncodeToString(entropy)
		runDir, err := locator.RunDir(runID)
		if err != nil {
			return "", err
		}
		if _, err := os.Lstat(runDir); os.IsNotExist(err) {
			return runID, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("unable to reserve a unique control run identifier")
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
