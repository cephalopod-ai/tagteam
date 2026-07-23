package tagteam

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// CurrentRunSnapshot resolves the active run before falling back to the most
// recently completed run. latest.json is only written at finalization, so a
// status reader that starts there cannot observe work in progress.
func CurrentRunSnapshot(workdir string) (RunSnapshot, error) {
	if active, err := readActiveRun(workdir); err == nil && active.RunDir != "" {
		return BuildRunSnapshot(workdir, active.RunDir)
	}

	latest, err := readLatest(workdir)
	if err != nil {
		return RunSnapshot{}, err
	}
	return BuildRunSnapshot(workdir, latest.RunDir)
}

// BuildRunSnapshot assembles a RunSnapshot for runDir by reading whichever of
// active.json (in workdir), state.json, final.json, and plan.json exist. Only
// runDir itself must exist; every artifact inside it is optional, and a
// missing/unreadable one simply leaves the corresponding fields at their zero
// value instead of failing snapshot creation.
func BuildRunSnapshot(workdir, runDir string) (RunSnapshot, error) {
	info, err := os.Stat(runDir)
	if err != nil {
		return RunSnapshot{}, err
	}
	if !info.IsDir() {
		return RunSnapshot{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("%s is not a run directory", runDir)}
	}

	snapshot := RunSnapshot{
		SchemaVersion: ArtifactSchemaVersion,
		RunID:         filepath.Base(runDir),
		RunDir:        runDir,
	}

	if state, err := readRunState(runDir); err == nil {
		snapshot.Mode = state.Mode
		snapshot.Status = state.Status
		snapshot.Phase = state.Phase
		snapshot.CompletedPhase = state.CompletedPhase
		snapshot.RepoID = state.RepoID
		snapshot.InvocationID = state.InvocationID
		snapshot.DiffHash = state.DiffHash
		snapshot.RecoveryStatus = state.RecoveryStatus
		snapshot.Degraded = state.Degraded
		snapshot.DegradedReason = state.DegradedReason
		snapshot.BlockingReason = state.BlockingReason
		snapshot.RoleStatuses = state.RoleStatuses
		snapshot.CurrentRound = state.CurrentRound
		snapshot.LatestDiffPath = state.LatestDiffPath
		snapshot.LatestReviewPath = state.LatestReviewPath
		snapshot.ExitCode = state.ExitCode
		snapshot.UpdatedAt = state.UpdatedAt
	}
	if locator, err := locatorFromPointer(workdir); err == nil {
		snapshot.StateRoot = locator.StateRoot
		if snapshot.RepoID == "" {
			snapshot.RepoID = locator.RepoID
		}
	}
	if data, err := os.ReadFile(filepath.Join(runDir, liveProgressArtifact)); err == nil {
		var live LiveProgress
		if json.Unmarshal(data, &live) == nil {
			snapshot.LiveProgress = &live
			if live.DiffHash != "" {
				snapshot.DiffHash = live.DiffHash
			}
			if live.UpdatedAt.After(snapshot.UpdatedAt) {
				snapshot.UpdatedAt = live.UpdatedAt
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(runDir, hostActivityArtifact)); err == nil {
		var activity HostActivity
		if json.Unmarshal(data, &activity) == nil {
			snapshot.HostActivity = &activity
			if activity.UpdatedAt.After(snapshot.UpdatedAt) {
				snapshot.UpdatedAt = activity.UpdatedAt
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(runDir, preexistingWorktreeArtifact)); err == nil {
		var preexisting PreexistingWorktree
		if json.Unmarshal(data, &preexisting) == nil {
			snapshot.PreexistingFiles = preexisting.Files
		}
	}

	// active.json is kept current by the same run-lifecycle defer on every
	// entrypoint, including ones (like solo runs) that do not rewrite
	// state.json on every error path. A matching running pointer also means a
	// prior failed final.json belongs to an earlier attempt and must not mask
	// the resumed run's live state.
	activeRunning := false
	if active, err := readActiveRun(workdir); err == nil && active.RunID == snapshot.RunID {
		if active.Mode != "" {
			snapshot.Mode = active.Mode
		}
		snapshot.Status = active.Status
		activeRunning = active.Status == "running"
		if active.UpdatedAt.After(snapshot.UpdatedAt) {
			snapshot.UpdatedAt = active.UpdatedAt
		}
	}

	var finalReview *Review
	if final, err := readFinal(filepath.Join(runDir, "final.json")); err == nil && final.RunID != "" && !activeRunning {
		if snapshot.Mode == "" {
			snapshot.Mode = final.Mode
		}
		if final.Status != "" {
			snapshot.Status = string(final.Status)
		}
		if final.Phase != "" {
			snapshot.Phase = final.Phase
		}
		if final.Verdict != "" {
			snapshot.Verdict = final.Verdict
		}
		snapshot.ExitCode = final.ExitCode
		snapshot.Degraded = final.Degraded
		snapshot.DegradedReason = final.DegradedReason
		snapshot.BlockingReason = final.BlockingReason
		if len(final.RoleStatuses) > 0 {
			snapshot.RoleStatuses = final.RoleStatuses
		}
		snapshot.RoundsCompleted = final.RoundsCompleted
		snapshot.RoundsRequested = final.RoundsRequested
		if len(final.ChangedFiles) > 0 {
			snapshot.ChangedFiles = final.ChangedFiles
		}
		if final.LatestDiffPath != "" {
			snapshot.LatestDiffPath = final.LatestDiffPath
		}
		if final.LatestReviewPath != "" {
			snapshot.LatestReviewPath = final.LatestReviewPath
		}
		finalReview = final.Review
		snapshot.OpenMajorCount = final.Findings.OpenBlockerOrMajor
		if !final.FinishedAt.IsZero() && final.FinishedAt.After(snapshot.UpdatedAt) {
			snapshot.UpdatedAt = final.FinishedAt
		}
	}

	if plan, err := readExecutionPlan(runDir); err == nil && plan.RunID != "" {
		snapshot.PlanSummary = summarizeExecutionPlan(runDir, &plan)
	}
	if ledger, err := loadFindingsLedger(runDir); err == nil {
		snapshot.OpenMajorCount = summarizeFindings(filepath.Join(runDir, findingsLedgerFilename), ledger).OpenBlockerOrMajor
	}

	if snapshot.LatestDiffPath != "" && !snapshotArtifactPathAllowed(runDir, snapshot.LatestDiffPath) {
		snapshot.LatestDiffPath = ""
	}
	if snapshot.LatestReviewPath != "" && !snapshotArtifactPathAllowed(runDir, snapshot.LatestReviewPath) {
		snapshot.LatestReviewPath = ""
	}
	if len(snapshot.ChangedFiles) == 0 && snapshot.LatestDiffPath != "" {
		snapshot.ChangedFiles = readChangedFilesFromDiffPath(snapshot.LatestDiffPath)
	}

	if finalReview != nil {
		snapshot.FindingsCount = len(finalReview.Findings)
	} else if snapshot.LatestReviewPath != "" {
		if review, err := readReviewArtifact(snapshot.LatestReviewPath); err == nil {
			snapshot.FindingsCount = len(review.Findings)
		}
	}

	if testPath := latestTestOutputPath(runDir); testPath != "" {
		snapshot.LatestTestPath = testPath
	}

	return snapshot, nil
}

func snapshotArtifactPathAllowed(runDir, path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	root, err := canonicalPath(runDir, true)
	if err != nil {
		return false
	}
	artifact, err := canonicalPath(path, false)
	return err == nil && pathWithin(root, artifact)
}

func readRunState(runDir string) (RunState, error) {
	var state RunState
	data, err := os.ReadFile(filepath.Join(runDir, "state.json"))
	if err != nil {
		return RunState{}, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return RunState{}, err
	}
	return state, nil
}

func readReviewArtifact(path string) (Review, error) {
	var review Review
	data, err := os.ReadFile(path)
	if err != nil {
		return Review{}, err
	}
	if err := json.Unmarshal(data, &review); err != nil {
		return Review{}, err
	}
	return review, nil
}

// readChangedFilesFromDiffPath derives the sibling "<round>.files.json"
// metadata path from a "diff-round-N.patch" path (see captureDiffArtifact)
// and returns the changed file list it records, if the file exists and
// parses.
func readChangedFilesFromDiffPath(diffPath string) []string {
	if !strings.HasSuffix(diffPath, ".patch") {
		return nil
	}
	filesPath := strings.TrimSuffix(diffPath, ".patch") + ".files.json"
	data, err := os.ReadFile(filesPath)
	if err != nil {
		return nil
	}
	var metadata DiffFilesMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil
	}
	files := make([]string, 0, len(metadata.Files))
	for _, file := range metadata.Files {
		files = append(files, file.Path)
	}
	return files
}

// latestTestOutputPath returns the highest-round "test-round-N.txt" artifact
// in runDir, if any (see runTestCommand's output naming).
func latestTestOutputPath(runDir string) string {
	entries, err := os.ReadDir(runDir)
	if err != nil {
		return ""
	}
	best := ""
	bestRound := -1
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "test-round-") || !strings.HasSuffix(name, ".txt") {
			continue
		}
		roundStr := strings.TrimSuffix(strings.TrimPrefix(name, "test-round-"), ".txt")
		round, err := strconv.Atoi(roundStr)
		if err != nil {
			continue
		}
		if round > bestRound {
			bestRound = round
			best = filepath.Join(runDir, name)
		}
	}
	return best
}
