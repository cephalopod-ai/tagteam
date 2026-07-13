package tagteam

import (
	"fmt"
	"strings"
	"time"
)

// ControlStartError is a bounded, recoverable error returned by the MCP start
// operation. ReasonCode is stable enough for a host to decide whether to
// re-request approval, choose a new idempotency key, wait for an active run,
// or report a persisted configuration problem — without parsing prose. It
// mirrors ControlResumeError and ControlCancelError so every MCP lifecycle
// mutation surfaces a typed, machine-recoverable failure.
type ControlStartError struct {
	ReasonCode  string
	Reason      string
	Recoverable bool
	Err         error
}

func (e *ControlStartError) Error() string {
	if e == nil {
		return ""
	}
	if e.Reason == "" {
		return "start " + e.ReasonCode
	}
	return "start " + e.ReasonCode + ": " + e.Reason
}

func (e *ControlStartError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func newControlStartError(reasonCode, reason string, cause error) error {
	if reasonCode == "" {
		reasonCode = "start_unavailable"
	}
	if reason == "" && cause != nil {
		reason = cause.Error()
	}
	return &ControlStartError{ReasonCode: reasonCode, Reason: boundControlText(reason), Recoverable: true, Err: cause}
}

// validateControlApproval enforces the start approval binding: the record must
// carry a normalized nonce, match the expected start action digest, and be
// currently valid within the bounded approval lifetime. Reason codes match the
// resume and cancel validators so a host recovers uniformly across operations.
func validateControlApproval(approval ControlApproval, expectedDigest string) error {
	if strings.TrimSpace(approval.Nonce) == "" {
		return newControlStartError("approval_missing", "approval nonce is required", nil)
	}
	if approval.ActionDigest != expectedDigest {
		return newControlStartError("approval_action_mismatch", "approval does not match the normalized start action", nil)
	}
	if strings.TrimSpace(approval.Nonce) != approval.Nonce || len(approval.Nonce) > controlMaxRoleBytes || containsControl(approval.Nonce) {
		return newControlStartError("approval_invalid", fmt.Sprintf("approval nonce must be a normalized identifier no longer than %d bytes", controlMaxRoleBytes), nil)
	}
	now := time.Now().UTC()
	if approval.ApprovedAt.IsZero() || approval.ExpiresAt.IsZero() || approval.ApprovedAt.After(now) {
		return newControlStartError("approval_invalid", "approval timestamps are invalid", nil)
	}
	if approval.ExpiresAt.Sub(approval.ApprovedAt) > ControlApprovalMaxLifetime {
		return newControlStartError("approval_lifetime_exceeded", fmt.Sprintf("approval must expire within %s", ControlApprovalMaxLifetime), nil)
	}
	if !approval.ExpiresAt.After(now) {
		return newControlStartError("approval_expired", "approval has expired", nil)
	}
	return nil
}

// controlLedgerHasNonce reports whether a nonce already appears in any ledger
// section. Approval nonces are single-use across the whole ledger, so a start
// (whose idempotent replay is keyed separately by idempotency_key) rejects any
// nonce previously consumed by a start, resume, or cancel action.
func controlLedgerHasNonce(ledger controlApprovalLedger, nonce string) bool {
	for _, record := range ledger.Starts {
		if record.Nonce == nonce {
			return true
		}
	}
	for _, record := range ledger.Resumes {
		if record.Nonce == nonce {
			return true
		}
	}
	for _, record := range ledger.Cancels {
		if record.Nonce == nonce {
			return true
		}
	}
	return false
}
