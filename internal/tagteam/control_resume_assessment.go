package tagteam

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// PrepareResume inspects the exact preconditions Resume would require without
// changing repository or run artifacts. It is intentionally conservative about
// live and stale ownership, which remains a prerequisite for a future MCP
// resume operation.
func (s ControlService) PrepareResume(ctx context.Context, request ControlResumeRequest) (ControlRecoveryAssessment, error) {
	digest, err := ControlResumeActionDigest(request)
	if err != nil {
		return ControlRecoveryAssessment{}, err
	}
	repository, err := resolveControlRepository(request.Repository.CanonicalRoot)
	if err != nil {
		return ControlRecoveryAssessment{}, err
	}
	if err := s.requireRepository(repository); err != nil {
		return ControlRecoveryAssessment{}, err
	}
	assessment := ControlRecoveryAssessment{SchemaVersion: ControlContractVersion, RunID: request.RunID, ActionDigest: digest}
	runDir, err := s.runDir(request.RunID)
	if err != nil {
		return controlResumeUnavailable(assessment, "run_not_found", err.Error()), nil
	}
	locator, err := resolveStateLocator(repository.CanonicalRoot, s.StateRoot)
	if err != nil {
		return controlResumeUnavailable(assessment, "state_root_unavailable", err.Error()), nil
	}
	if active, activeErr := readActiveAt(filepath.Join(locator.RepoRoot, "active.json")); activeErr == nil && active.Status == string(RunStatusRunning) {
		return controlResumeUnavailable(assessment, "active_run", fmt.Sprintf("run %q is active in this worktree", active.RunID)), nil
	} else if activeErr != nil && !os.IsNotExist(activeErr) {
		return controlResumeUnavailable(assessment, "active_state_invalid", activeErr.Error()), nil
	}
	if code, reason := controlResumeLockState(runDir); code != "" {
		return controlResumeUnavailable(assessment, code, reason), nil
	}
	state, err := readControlRunState(runDir)
	if err != nil {
		return controlResumeUnavailable(assessment, "state_invalid", fmt.Sprintf("read resumable state: %v", err)), nil
	}
	if state.SchemaVersion < runStateSchemaVersion {
		return controlResumeUnavailable(assessment, "state_schema_unsupported", fmt.Sprintf("legacy run state schema %d is readable but not resumable", state.SchemaVersion)), nil
	}
	if state.RunID != request.RunID {
		return controlResumeUnavailable(assessment, "run_identity_mismatch", "persisted state run_id does not match the requested run"), nil
	}
	if err := controlRunWorkdirMatches(repository.CanonicalRoot, state.Workdir, "state"); err != nil {
		return controlResumeUnavailable(assessment, "run_identity_mismatch", err.Error()), nil
	}
	meta, err := readControlMeta(runDir)
	if err != nil {
		return controlResumeUnavailable(assessment, "metadata_invalid", fmt.Sprintf("read run metadata: %v", err)), nil
	}
	if meta.RunID != request.RunID || meta.Baseline == "" {
		return controlResumeUnavailable(assessment, "metadata_identity_mismatch", "persisted metadata does not match the requested run"), nil
	}
	if err := controlRunWorkdirMatches(repository.CanonicalRoot, meta.Workdir, "metadata"); err != nil {
		return controlResumeUnavailable(assessment, "metadata_identity_mismatch", err.Error()), nil
	}
	if state.BaselineSHA != "" && state.BaselineSHA != meta.Baseline {
		return controlResumeUnavailable(assessment, "baseline_metadata_mismatch", "persisted state baseline does not match metadata"), nil
	}
	if final, present, finalErr := readControlFinalOptional(runDir); finalErr != nil {
		return controlResumeUnavailable(assessment, "final_invalid", fmt.Sprintf("read persisted final result: %v", finalErr)), nil
	} else if present {
		if final.RunID != "" && final.RunID != request.RunID {
			return controlResumeUnavailable(assessment, "final_identity_mismatch", "persisted final result does not match the requested run"), nil
		}
		if err := controlRunWorkdirMatches(repository.CanonicalRoot, final.Workdir, "final result"); err != nil {
			return controlResumeUnavailable(assessment, "final_identity_mismatch", err.Error()), nil
		}
		if final.Status == RunStatusPassed || final.Status == RunStatusDegraded {
			return controlResumeUnavailable(assessment, "already_terminal", "run has already reached a terminal successful state"), nil
		}
	}
	currentHead, err := ensureGitRepo(repository.CanonicalRoot)
	if err != nil {
		return controlResumeUnavailable(assessment, "git_unavailable", err.Error()), nil
	}
	if currentHead != meta.Baseline {
		return controlResumeUnavailable(assessment, "baseline_mismatch", fmt.Sprintf("current HEAD %s does not match run baseline %s", currentHead, meta.Baseline)), nil
	}
	patch, err := readOnlyDeterministicDiffPatch(ctx, repository.CanonicalRoot, meta.Baseline)
	if err != nil {
		return controlResumeUnavailable(assessment, "diff_capture_failed", err.Error()), nil
	}
	phase := normalizeRunPhase(state.Phase)
	currentDiffHash := sha256Sum(patch)
	if state.DiffHash != "" && state.DiffHash != currentDiffHash && phase != PhaseImplementing && phase != PhaseRepairing {
		return controlResumeUnavailable(assessment, "diff_mismatch", fmt.Sprintf("worktree diff hash changed after completed %s phase", phase)), nil
	}
	if err := verifyResumeArtifacts(runDir, state, true); err != nil {
		return controlResumeUnavailable(assessment, "artifacts_invalid", err.Error()), nil
	}
	if state.DiffHash != "" && state.DiffHash != currentDiffHash {
		assessment.Resumable = true
		assessment.ReasonCode = "recovery_required"
		assessment.Reason = "partial diff is unchanged only for an implementation or repair phase; resume will enter recovery"
		return assessment, nil
	}
	assessment.Resumable = true
	assessment.ReasonCode = "resumable"
	assessment.Reason = "resume preconditions verified without changing persisted state"
	return assessment, nil
}

func controlResumeUnavailable(assessment ControlRecoveryAssessment, reasonCode, reason string) ControlRecoveryAssessment {
	assessment.Resumable = false
	assessment.ReasonCode = reasonCode
	assessment.Reason = boundControlText(reason)
	return assessment
}

func controlResumeLockState(runDir string) (string, string) {
	data, present, err := readControlOptionalArtifactBytes(runDir, "run.lock")
	if err != nil {
		return "run_lock_invalid", fmt.Sprintf("read run lock: %v", err)
	}
	if !present {
		return "", ""
	}
	var record runLockRecord
	if err := json.Unmarshal(data, &record); err != nil || record.PID <= 0 {
		return "run_lock_invalid", "run lock does not contain a valid owner"
	}
	if processAlive(record.PID) {
		return "run_locked", fmt.Sprintf("run is locked by pid %d", record.PID)
	}
	return "stale_run_lock", fmt.Sprintf("stale run lock exists for pid %d", record.PID)
}

func controlRunWorkdirMatches(expected, candidate, source string) error {
	if candidate == "" {
		return fmt.Errorf("persisted %s workdir is empty", source)
	}
	canonical, err := canonicalPath(candidate, true)
	if err != nil || canonical != expected {
		return fmt.Errorf("persisted %s workdir does not match the MCP server worktree", source)
	}
	return nil
}

func readOnlyDeterministicDiffPatch(ctx context.Context, workdir, baseline string) ([]byte, error) {
	tempDir, err := os.MkdirTemp("", "tagteam-resume-assessment-")
	if err != nil {
		return nil, fmt.Errorf("create ephemeral resume index: %w", err)
	}
	defer os.RemoveAll(tempDir)
	return deterministicDiffPatch(ctx, workdir, baseline, filepath.Join(tempDir, "index"))
}
