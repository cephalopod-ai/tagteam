package tagteam

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

type controlResumeRecord struct {
	ActionDigest string    `json:"action_digest"`
	Nonce        string    `json:"nonce"`
	RunID        string    `json:"run_id"`
	CreatedAt    time.Time `json:"created_at"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// ControlResumeError is a bounded, recoverable error returned by the MCP
// resume operation. ReasonCode is stable enough for a host to choose whether
// to request a new approval, repair the persisted run, or report the issue.
type ControlResumeError struct {
	ReasonCode  string
	Reason      string
	Recoverable bool
	Err         error
}

func (e *ControlResumeError) Error() string {
	if e == nil {
		return ""
	}
	if e.Reason == "" {
		return "resume " + e.ReasonCode
	}
	return "resume " + e.ReasonCode + ": " + e.Reason
}

func (e *ControlResumeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func newControlResumeError(reasonCode, reason string, cause error) error {
	if reasonCode == "" {
		reasonCode = "resume_unavailable"
	}
	if reason == "" && cause != nil {
		reason = cause.Error()
	}
	return &ControlResumeError{ReasonCode: reasonCode, Reason: boundControlText(reason), Recoverable: true, Err: cause}
}

// Resume consumes one approved resume action and starts App.Resume with the
// persisted run identity. The approval ledger is separate from run artifacts
// so replay protection survives a fresh ControlRuntime instance or process.
func (r *ControlRuntime) Resume(ctx context.Context, request ControlResumeRequest) (ControlRunHandle, error) {
	if request.SchemaVersion != ControlContractVersion {
		return ControlRunHandle{}, newControlResumeError("invalid_request", fmt.Sprintf("unsupported control schema_version %d (want %d)", request.SchemaVersion, ControlContractVersion), nil)
	}
	digest, err := ControlResumeActionDigest(request)
	if err != nil {
		return ControlRunHandle{}, newControlResumeError("invalid_request", err.Error(), err)
	}
	repository, err := resolveControlRepository(request.Repository.CanonicalRoot)
	if err != nil {
		return ControlRunHandle{}, newControlResumeError("repository_unavailable", err.Error(), err)
	}
	if err := r.service.requireRepository(repository); err != nil {
		return ControlRunHandle{}, newControlResumeError("repository_mismatch", err.Error(), err)
	}
	request.Repository = repository
	if err := validateControlResumeApproval(request.Approval, digest); err != nil {
		return ControlRunHandle{}, err
	}

	locator, err := resolveStateLocator(repository.CanonicalRoot, r.service.StateRoot)
	if err != nil {
		return ControlRunHandle{}, newControlResumeError("state_root_unavailable", err.Error(), err)
	}
	if err := r.verifyCapabilityBaseline(locator.RepoRoot); err != nil {
		return ControlRunHandle{}, newControlResumeError("capability_quarantined", err.Error(), err)
	}
	if handle, resumeErr := r.lookupResumeLedger(locator, request, digest); handle.RunID != "" || resumeErr != nil {
		handle.ProducerVersion = normalizedProducerVersion(r.service.ProducerVersion)
		return handle, resumeErr
	}

	assessment, err := r.service.PrepareResume(ctx, request)
	if err != nil {
		return ControlRunHandle{}, newControlResumeError("resume_preparation_failed", err.Error(), err)
	}
	if !assessment.Resumable {
		return ControlRunHandle{}, newControlResumeError(assessment.ReasonCode, assessment.Reason, nil)
	}

	opts, err := r.resumeOptions(repository.CanonicalRoot)
	if err != nil {
		return ControlRunHandle{}, newControlResumeError("resume_configuration_invalid", err.Error(), err)
	}
	lock, err := acquireRunLock(locator.RepoRoot, false)
	if err != nil {
		return ControlRunHandle{}, newControlResumeError("approval_ledger_locked", err.Error(), err)
	}
	ledgerPath := filepath.Join(locator.RepoRoot, controlApprovalLedgerName)
	ledger, err := readControlApprovalLedger(ledgerPath)
	if err != nil {
		_ = lock.Release()
		return ControlRunHandle{}, newControlResumeError("approval_ledger_invalid", err.Error(), err)
	}
	if handle, resumeErr := controlResumeLedgerResult(ledger, request, digest); handle.RunID != "" || resumeErr != nil {
		handle.ProducerVersion = normalizedProducerVersion(r.service.ProducerVersion)
		_ = lock.Release()
		return handle, resumeErr
	}
	if len(ledger.Resumes) >= controlMaxApprovalRecords {
		_ = lock.Release()
		return ControlRunHandle{}, newControlResumeError("approval_ledger_full", "resume approval ledger reached its maximum retained records", nil)
	}
	now := time.Now().UTC()
	ledger.Resumes = append(ledger.Resumes, controlResumeRecord{ActionDigest: digest, Nonce: request.Approval.Nonce, RunID: request.RunID, CreatedAt: now, ExpiresAt: request.Approval.ExpiresAt})
	if err := writeJSONDurable(ledgerPath, ledger, false, true); err != nil {
		_ = lock.Release()
		return ControlRunHandle{}, newControlResumeError("approval_ledger_write_failed", fmt.Sprintf("persist consumed resume approval: %v", err), err)
	}
	if err := lock.Release(); err != nil {
		return ControlRunHandle{}, newControlResumeError("approval_ledger_unlock_failed", err.Error(), err)
	}

	runContext, cancel := context.WithCancel(ctx)
	r.registerJob(request.RunID, cancel)
	go r.runResume(runContext, opts, request.RunID)
	return controlRunHandle(r.service.ProducerVersion, request.RunID), nil
}

func (r *ControlRuntime) lookupResumeLedger(locator StateLocator, request ControlResumeRequest, digest string) (ControlRunHandle, error) {
	lock, err := acquireRunLock(locator.RepoRoot, false)
	if err != nil {
		return ControlRunHandle{}, newControlResumeError("approval_ledger_locked", err.Error(), err)
	}
	defer lock.Release()
	ledger, err := readControlApprovalLedger(filepath.Join(locator.RepoRoot, controlApprovalLedgerName))
	if err != nil {
		return ControlRunHandle{}, newControlResumeError("approval_ledger_invalid", err.Error(), err)
	}
	return controlResumeLedgerResult(ledger, request, digest)
}

func controlResumeLedgerResult(ledger controlApprovalLedger, request ControlResumeRequest, digest string) (ControlRunHandle, error) {
	// Approval nonces are single-use across every action. A nonce previously
	// consumed by a start or cancel can never be reused for a resume, so scan
	// those sections before the idempotent resume-replay handling below.
	for _, record := range ledger.Starts {
		if record.Nonce == request.Approval.Nonce {
			return ControlRunHandle{}, newControlResumeError("approval_nonce_replayed", "approval nonce has already been consumed for another action", nil)
		}
	}
	for _, record := range ledger.Cancels {
		if record.Nonce == request.Approval.Nonce {
			return ControlRunHandle{}, newControlResumeError("approval_nonce_replayed", "approval nonce has already been consumed for another action", nil)
		}
	}
	for _, record := range ledger.Resumes {
		if record.Nonce == request.Approval.Nonce {
			if record.RunID == request.RunID && record.ActionDigest == digest {
				return controlRunHandle("", record.RunID), nil
			}
			return ControlRunHandle{}, newControlResumeError("approval_nonce_replayed", "approval nonce has already been consumed for another resume action", nil)
		}
		if record.RunID == request.RunID {
			return ControlRunHandle{}, newControlResumeError("resume_already_consumed", "a resume approval has already been consumed for this run", nil)
		}
	}
	return ControlRunHandle{}, nil
}

func controlRunHandle(producerVersion, runID string) ControlRunHandle {
	return ControlRunHandle{SchemaVersion: ControlContractVersion, RunID: runID, ProducerVersion: normalizedProducerVersion(producerVersion)}
}

func validateControlResumeApproval(approval ControlApproval, expectedDigest string) error {
	if strings.TrimSpace(approval.Nonce) == "" {
		return newControlResumeError("approval_missing", "approval nonce is required", nil)
	}
	if approval.ActionDigest != expectedDigest {
		return newControlResumeError("approval_action_mismatch", "approval does not match the normalized resume action", nil)
	}
	if strings.TrimSpace(approval.Nonce) != approval.Nonce || len(approval.Nonce) > controlMaxRoleBytes || containsControl(approval.Nonce) {
		return newControlResumeError("approval_invalid", fmt.Sprintf("approval nonce must be a normalized identifier no longer than %d bytes", controlMaxRoleBytes), nil)
	}
	now := time.Now().UTC()
	if approval.ApprovedAt.IsZero() || approval.ExpiresAt.IsZero() || approval.ApprovedAt.After(now) {
		return newControlResumeError("approval_invalid", "approval timestamps are invalid", nil)
	}
	if approval.ExpiresAt.Sub(approval.ApprovedAt) > ControlApprovalMaxLifetime {
		return newControlResumeError("approval_lifetime_exceeded", fmt.Sprintf("approval must expire within %s", ControlApprovalMaxLifetime), nil)
	}
	if !approval.ExpiresAt.After(now) {
		return newControlResumeError("approval_expired", "approval has expired", nil)
	}
	return nil
}

func (r *ControlRuntime) resumeOptions(repository string) (RunOptions, error) {
	flags := FlagInputs{Workdir: repository, StateRoot: r.service.StateRoot, Timeout: 15 * time.Minute}
	changed := map[string]bool{"state-root": true}
	return ResolveOptions(r.config, r.sources, flags, changed, "")
}

func (r *ControlRuntime) runResume(ctx context.Context, opts RunOptions, runID string) {
	defer r.unregisterJob(runID)
	// MCP-owned resume never uses the generic raw locator path alone: re-resolve
	// the run directory and artifacts immediately before lock/mutation.
	_, _ = NewApp(r.config).ResumeControl(ctx, opts, runID)
}
