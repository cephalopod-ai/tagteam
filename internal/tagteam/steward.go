package tagteam

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	// StewardContractVersion versions the RunObservation fed to, and the
	// StewardAdvisory returned by, an advisory Run Steward.
	StewardContractVersion = 1
	stewardMaxReasonBytes  = 2048
	stewardDefaultTimeout  = 10 * time.Second
)

// StewardAction is the bounded advisory enum a Run Steward may recommend. The
// steward is strictly advisory: it can suggest one of these actions but cannot
// edit the repository, broaden scope, change roles, dismiss findings, or approve
// its own recovery. The controller never branches execution on the advisory.
type StewardAction string

const (
	StewardWait          StewardAction = "wait"
	StewardInspect       StewardAction = "inspect"
	StewardNotify        StewardAction = "notify"
	StewardPrepareResume StewardAction = "prepare_resume"
	StewardAskUser       StewardAction = "ask_user"
	StewardReportIssue   StewardAction = "report_issue"
)

func validStewardAction(action StewardAction) bool {
	switch action {
	case StewardWait, StewardInspect, StewardNotify, StewardPrepareResume, StewardAskUser, StewardReportIssue:
		return true
	}
	return false
}

// RunObservation is the bounded, sanitized projection of a run's authoritative
// snapshot that a Run Steward is allowed to see. It deliberately excludes raw
// prompts, diffs, file paths, changed-file lists, and model reasoning: only
// status, phase, reason codes, counts, and progress timing cross the boundary.
type RunObservation struct {
	SchemaVersion    int       `json:"schema_version"`
	RunID            string    `json:"run_id"`
	Mode             string    `json:"mode,omitempty"`
	Status           string    `json:"status,omitempty"`
	Phase            string    `json:"phase,omitempty"`
	Verdict          string    `json:"verdict,omitempty"`
	RecoveryStatus   string    `json:"recovery_status,omitempty"`
	Degraded         bool      `json:"degraded"`
	DegradedReason   string    `json:"degraded_reason,omitempty"`
	BlockingReason   string    `json:"blocking_reason,omitempty"`
	CurrentRound     int       `json:"current_round,omitempty"`
	RoundsCompleted  int       `json:"rounds_completed,omitempty"`
	RoundsRequested  int       `json:"rounds_requested,omitempty"`
	FindingsCount    int       `json:"findings_count"`
	OpenMajorCount   int       `json:"open_major_count"`
	ChangedFileCount int       `json:"changed_file_count"`
	WaitingFor       string    `json:"waiting_for,omitempty"`
	NoProgressFor    string    `json:"no_progress_for,omitempty"`
	ObservedAt       time.Time `json:"observed_at"`
}

// NewRunObservation projects a bounded, sanitized RunObservation from the
// authoritative RunSnapshot. Every free-text field is re-bounded defensively and
// only counts (never the changed-file list itself) cross the steward boundary.
func NewRunObservation(snapshot RunSnapshot) RunObservation {
	observation := RunObservation{
		SchemaVersion:    StewardContractVersion,
		RunID:            boundStewardText(snapshot.RunID),
		Mode:             boundStewardText(string(snapshot.Mode)),
		Status:           boundStewardText(snapshot.Status),
		Phase:            boundStewardText(snapshot.Phase),
		Verdict:          boundStewardText(snapshot.Verdict),
		RecoveryStatus:   boundStewardText(snapshot.RecoveryStatus),
		Degraded:         snapshot.Degraded,
		DegradedReason:   boundStewardText(snapshot.DegradedReason),
		BlockingReason:   boundStewardText(snapshot.BlockingReason),
		CurrentRound:     snapshot.CurrentRound,
		RoundsCompleted:  snapshot.RoundsCompleted,
		RoundsRequested:  snapshot.RoundsRequested,
		FindingsCount:    snapshot.FindingsCount,
		OpenMajorCount:   snapshot.OpenMajorCount,
		ChangedFileCount: len(snapshot.ChangedFiles),
		ObservedAt:       snapshot.UpdatedAt,
	}
	if snapshot.LiveProgress != nil {
		observation.WaitingFor = boundStewardText(snapshot.LiveProgress.WaitingFor)
		observation.NoProgressFor = boundStewardText(snapshot.LiveProgress.NoProgressFor)
	}
	return observation
}

// StewardAdvisory is the schema-validated advisory a Run Steward returns. It is
// recorded for the user or host but never gates or alters the Tagteam run.
type StewardAdvisory struct {
	SchemaVersion int           `json:"schema_version"`
	RunID         string        `json:"run_id"`
	Action        StewardAction `json:"action"`
	Reason        string        `json:"reason"`
	Source        string        `json:"source"`
	ObservedAt    time.Time     `json:"observed_at,omitempty"`
}

// ValidateStewardAdvisory enforces the advisory schema on any steward result,
// including a model-authored one. An invalid advisory is rejected so the caller
// falls back to the deterministic template instead of acting on garbage.
func ValidateStewardAdvisory(advisory StewardAdvisory) error {
	if advisory.SchemaVersion != StewardContractVersion {
		return fmt.Errorf("unsupported steward schema_version %d (want %d)", advisory.SchemaVersion, StewardContractVersion)
	}
	if !validStewardAction(advisory.Action) {
		return fmt.Errorf("invalid steward action %q", advisory.Action)
	}
	if strings.TrimSpace(advisory.Reason) == "" {
		return fmt.Errorf("steward advisory reason is required")
	}
	if len(advisory.Reason) > stewardMaxReasonBytes {
		return fmt.Errorf("steward advisory reason exceeds %d bytes", stewardMaxReasonBytes)
	}
	return nil
}

// Steward is the advisory interface. Advise reads a bounded observation and
// returns an advisory; it must not influence execution.
type Steward interface {
	Advise(ctx context.Context, observation RunObservation) (StewardAdvisory, error)
}

// DeterministicSteward is the guaranteed final fallback. It maps an observation
// to a safe advisory using fixed templates and never errors, so a missing,
// invalid, slow, or rate-limited model steward cannot delay or alter a run.
type DeterministicSteward struct{}

// Advise implements Steward.
func (DeterministicSteward) Advise(_ context.Context, observation RunObservation) (StewardAdvisory, error) {
	return DeterministicAdvisory(observation), nil
}

// DeterministicAdvisory is the pure mapping from an observation to a safe
// advisory. It always returns a schema-valid advisory sourced as deterministic.
func DeterministicAdvisory(observation RunObservation) StewardAdvisory {
	action, reason := deterministicStewardDecision(observation)
	return StewardAdvisory{
		SchemaVersion: StewardContractVersion,
		RunID:         observation.RunID,
		Action:        action,
		Reason:        boundStewardText(reason),
		Source:        "deterministic",
		ObservedAt:    observation.ObservedAt,
	}
}

func deterministicStewardDecision(observation RunObservation) (StewardAction, string) {
	switch RunStatus(observation.Status) {
	case RunStatusPassed:
		return StewardNotify, "run passed; notify the operator that the change is ready to review"
	case RunStatusDegraded:
		return StewardNotify, fmt.Sprintf("run finished degraded (%s); notify the operator to review reduced guarantees", stewardReasonOrDefault(observation.DegradedReason, "degraded"))
	case RunStatusCancelled:
		return StewardNotify, "run was cancelled; notify the operator that no further work is pending"
	case RunStatusFailed:
		return StewardReportIssue, fmt.Sprintf("run failed (%s); report the blocking issue for operator review", stewardReasonOrDefault(observation.BlockingReason, "failed"))
	}
	// Non-terminal / running states.
	if strings.Contains(observation.RecoveryStatus, "recover") {
		return StewardPrepareResume, "run is in a recoverable interrupted state; prepare a resume assessment"
	}
	if observation.NoProgressFor != "" {
		return StewardInspect, fmt.Sprintf("run has shown no progress for %s; inspect the active role for a stall", observation.NoProgressFor)
	}
	if observation.BlockingReason != "" {
		return StewardReportIssue, fmt.Sprintf("run is blocked (%s); report the blocking issue", observation.BlockingReason)
	}
	return StewardWait, "run is progressing normally; wait for the next checkpoint"
}

// AdviseWithFallback runs a primary steward under a strict timeout and validates
// its advisory. Any error, timeout, or invalid result falls back to the
// deterministic template. It never returns an error and never blocks the run
// beyond the timeout, so a model steward can neither delay nor alter a run.
func AdviseWithFallback(ctx context.Context, primary Steward, observation RunObservation, timeout time.Duration) StewardAdvisory {
	fallback := DeterministicAdvisory(observation)
	if primary == nil {
		return fallback
	}
	if timeout <= 0 {
		timeout = stewardDefaultTimeout
	}
	adviseCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type outcome struct {
		advisory StewardAdvisory
		err      error
	}
	// Buffered so a slow steward that ignores cancellation cannot block the send
	// after we have already returned the fallback.
	resultCh := make(chan outcome, 1)
	go func() {
		advisory, err := primary.Advise(adviseCtx, observation)
		resultCh <- outcome{advisory, err}
	}()
	select {
	case <-adviseCtx.Done():
		return fallback
	case result := <-resultCh:
		if result.err != nil {
			return fallback
		}
		advisory := result.advisory
		// Pin identity and normalize source so a model cannot reassign the
		// advisory to a different run or omit provenance.
		advisory.RunID = observation.RunID
		if advisory.SchemaVersion == 0 {
			advisory.SchemaVersion = StewardContractVersion
		}
		if strings.TrimSpace(advisory.Source) == "" {
			advisory.Source = "steward"
		}
		advisory.Reason = boundStewardText(advisory.Reason)
		if err := ValidateStewardAdvisory(advisory); err != nil {
			return fallback
		}
		return advisory
	}
}

func stewardReasonOrDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func boundStewardText(value string) string {
	if len(value) <= stewardMaxReasonBytes {
		return value
	}
	end := stewardMaxReasonBytes
	for end > 0 && !utf8Boundary(value, end) {
		end--
	}
	return value[:end] + "...[truncated]"
}
