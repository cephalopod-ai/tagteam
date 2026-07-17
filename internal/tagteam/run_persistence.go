package tagteam

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

func runStateForFinal(final FinalRun, mode Mode, phase, recoveryStatus string) RunState {
	return RunState{
		RunID:            final.RunID,
		Mode:             mode,
		Status:           string(final.Status),
		Phase:            phase,
		Degraded:         final.Degraded,
		DegradedReason:   final.DegradedReason,
		BlockingReason:   final.BlockingReason,
		RoleStatuses:     final.RoleStatuses,
		CurrentRound:     final.RoundsCompleted,
		Workdir:          final.Workdir,
		BaselineSHA:      final.Baseline,
		LatestDiffPath:   final.LatestDiffPath,
		DiffHash:         final.LatestDiffSHA256,
		LatestReviewPath: final.LatestReviewPath,
		ExitCode:         final.ExitCode,
		RecoveryStatus:   recoveryStatus,
	}
}

func mandatoryPersistenceError(artifact string, err error) error {
	if err == nil {
		return nil
	}
	return &ExitError{
		Code: ExitAdapterFailure,
		Err:  fmt.Errorf("persist mandatory %s: %w", artifact, err),
	}
}

// persistTerminalRun writes the canonical state and terminal projection as one
// logical operation. If either initial write fails, it converts the in-memory
// result to a persistence failure and makes a best-effort second pass so any
// readable artifact cannot continue to claim clean success. The original write
// error is always returned even if the failure record succeeds on retry.
func (a *App) persistTerminalRun(workdir string, final *FinalRun, state RunState) error {
	return persistTerminalRunWith(
		final,
		state,
		func(value RunState) error { return writeRunState(final.RunDir, value) },
		func(value FinalRun) error { return a.persistFinal(workdir, value) },
	)
}

func persistTerminalRunWith(
	final *FinalRun,
	state RunState,
	writeState func(RunState) error,
	writeFinal func(FinalRun) error,
) error {
	stateErr := writeState(state)
	var finalErr error
	if stateErr == nil {
		finalErr = writeFinal(*final)
	}
	if stateErr == nil && finalErr == nil {
		return nil
	}

	initialErr := errors.Join(
		mandatoryPersistenceError("run state", stateErr),
		mandatoryPersistenceError("terminal artifacts", finalErr),
	)
	markTerminalPersistenceFailure(final, initialErr)
	failureState := runStateForFinal(*final, state.Mode, state.Phase, state.RecoveryStatus)
	retryFinalErr := writeFinal(*final)
	retryStateErr := writeState(failureState)
	return errors.Join(
		initialErr,
		mandatoryPersistenceError("failure terminal artifacts", retryFinalErr),
		mandatoryPersistenceError("failure run state", retryStateErr),
	)
}

func markTerminalPersistenceFailure(final *FinalRun, cause error) {
	message := "mandatory run evidence could not be persisted"
	if cause != nil {
		message += ": " + cause.Error()
	}
	final.ExitCode = ExitAdapterFailure
	final.Status = RunStatusFailed
	final.Verdict = "error"
	final.BlockingReason = string(ReasonPersistenceFailed)
	final.FinishedAt = time.Now().UTC()
	if strings.TrimSpace(final.Summary) == "" {
		final.Summary = message
	} else if !strings.Contains(final.Summary, message) {
		final.Summary = strings.TrimSpace(final.Summary) + "\n\n" + message
	}
}
