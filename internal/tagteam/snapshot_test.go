package tagteam

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newRunDirForSnapshotTest(t *testing.T) (workdir, runDir, runID string) {
	t.Helper()
	workdir = t.TempDir()
	runID = "2026-01-01T000000.000000000Z"
	runDir = filepath.Join(workdir, ".tagteam", "runs", runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return workdir, runDir, runID
}

func TestCurrentRunSnapshotPrefersActiveRunBeforeLatestExists(t *testing.T) {
	workdir, runDir, runID := newRunDirForSnapshotTest(t)
	state := RunState{
		RunID:        runID,
		Mode:         ModeAdversarial,
		Status:       "running",
		Phase:        string(PhaseReviewing),
		CurrentRound: 1,
	}
	if err := writeJSONWithNewline(filepath.Join(runDir, "state.json"), state); err != nil {
		t.Fatal(err)
	}
	activateRun(workdir, runID, runDir, ModeAdversarial)

	snapshot, err := CurrentRunSnapshot(workdir)
	if err != nil {
		t.Fatalf("CurrentRunSnapshot() error = %v", err)
	}
	if snapshot.RunID != runID || snapshot.Status != "running" {
		t.Fatalf("active snapshot = %#v", snapshot)
	}
	if snapshot.Phase != string(PhaseReviewing) {
		t.Fatalf("phase = %q, want %q", snapshot.Phase, PhaseReviewing)
	}
}

func TestCurrentRunSnapshotFallsBackToLatestCompletedRun(t *testing.T) {
	workdir, runDir, runID := newRunDirForSnapshotTest(t)
	final := FinalRun{
		SchemaVersion: ArtifactSchemaVersion,
		RunID:         runID,
		RunDir:        runDir,
		Mode:          ModeSupervisor,
		Status:        RunStatusPassed,
		Verdict:       "pass",
	}
	finalPath := filepath.Join(runDir, "final.json")
	if err := writeJSONWithNewline(finalPath, final); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONWithNewline(statePathForWorkdir(workdir, "latest.json"), LatestRun{
		RunID:     runID,
		RunDir:    runDir,
		FinalPath: finalPath,
	}); err != nil {
		t.Fatal(err)
	}

	snapshot, err := CurrentRunSnapshot(workdir)
	if err != nil {
		t.Fatalf("CurrentRunSnapshot() error = %v", err)
	}
	if snapshot.RunID != runID || snapshot.Status != string(RunStatusPassed) || snapshot.Verdict != "pass" {
		t.Fatalf("completed snapshot = %#v", snapshot)
	}
}

func TestBuildRunSnapshot_RunningStateOnly(t *testing.T) {
	workdir, runDir, runID := newRunDirForSnapshotTest(t)
	state := RunState{
		RunID:            runID,
		Mode:             ModeSupervisor,
		Status:           "running",
		Phase:            "round-1",
		CurrentRound:     1,
		LatestDiffPath:   filepath.Join(runDir, "diff-round-1.patch"),
		LatestReviewPath: filepath.Join(runDir, "supervisor-round-1.json"),
	}
	if err := writeJSONWithNewline(filepath.Join(runDir, "state.json"), state); err != nil {
		t.Fatal(err)
	}
	live := LiveProgress{SchemaVersion: ArtifactSchemaVersion, Role: RoleSupervisor, Status: "running", FilesChanged: 2, Additions: 8, Deletions: 1, DiffHash: "live-hash", UpdatedAt: time.Now().UTC()}
	if err := writeJSONWithNewline(filepath.Join(runDir, liveProgressArtifact), live); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONWithNewline(filepath.Join(runDir, preexistingWorktreeArtifact), PreexistingWorktree{SchemaVersion: ArtifactSchemaVersion, Files: []string{"already-dirty.go"}}); err != nil {
		t.Fatal(err)
	}

	snapshot, err := BuildRunSnapshot(workdir, runDir)
	if err != nil {
		t.Fatalf("BuildRunSnapshot() error = %v", err)
	}
	if snapshot.RunID != runID || snapshot.RunDir != runDir {
		t.Fatalf("run_id/run_dir = %q/%q", snapshot.RunID, snapshot.RunDir)
	}
	if snapshot.Mode != ModeSupervisor {
		t.Fatalf("mode = %q, want %q", snapshot.Mode, ModeSupervisor)
	}
	if snapshot.Status != "running" {
		t.Fatalf("status = %q, want running", snapshot.Status)
	}
	if snapshot.Phase != "round-1" || snapshot.CurrentRound != 1 {
		t.Fatalf("phase/round = %q/%d", snapshot.Phase, snapshot.CurrentRound)
	}
	if snapshot.LatestDiffPath == "" || snapshot.LatestReviewPath == "" {
		t.Fatalf("expected latest diff/review paths to be populated: %#v", snapshot)
	}
	if snapshot.SchemaVersion != ArtifactSchemaVersion {
		t.Fatalf("schema_version = %d, want %d", snapshot.SchemaVersion, ArtifactSchemaVersion)
	}
	if snapshot.LiveProgress == nil || snapshot.LiveProgress.Role != RoleSupervisor || snapshot.DiffHash != "live-hash" {
		t.Fatalf("live progress not projected into snapshot: %#v", snapshot)
	}
	if len(snapshot.PreexistingFiles) != 1 || snapshot.PreexistingFiles[0] != "already-dirty.go" {
		t.Fatalf("pre-existing files not projected into snapshot: %#v", snapshot.PreexistingFiles)
	}
}

func TestBuildRunSnapshotProjectsHostActivity(t *testing.T) {
	workdir, runDir, _ := newRunDirForSnapshotTest(t)
	activity := HostActivity{
		SchemaVersion: ArtifactSchemaVersion,
		Actor:         "tagteam-host",
		Phase:         "baseline-test",
		Status:        "integrity_violation",
		Command:       "go test ./...",
		OutputPath:    filepath.Join(runDir, "baseline-test.txt"),
		ChangedFiles:  []string{"registry.yaml"},
		StartedAt:     time.Now().Add(-time.Second),
		UpdatedAt:     time.Now(),
	}
	if err := writeJSONWithNewline(filepath.Join(runDir, hostActivityArtifact), activity); err != nil {
		t.Fatal(err)
	}

	snapshot, err := BuildRunSnapshot(workdir, runDir)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.HostActivity == nil || snapshot.HostActivity.Status != "integrity_violation" || len(snapshot.HostActivity.ChangedFiles) != 1 {
		t.Fatalf("host activity not projected: %#v", snapshot.HostActivity)
	}
}

func TestBuildRunSnapshot_CompletedFinalRun(t *testing.T) {
	workdir, runDir, runID := newRunDirForSnapshotTest(t)
	state := RunState{RunID: runID, Mode: ModeSupervisor, Status: "running", Phase: "round-1", CurrentRound: 1}
	if err := writeJSONWithNewline(filepath.Join(runDir, "state.json"), state); err != nil {
		t.Fatal(err)
	}
	final := FinalRun{
		SchemaVersion:   ArtifactSchemaVersion,
		RunID:           runID,
		RunDir:          runDir,
		Mode:            ModeSupervisor,
		Verdict:         "pass",
		Status:          RunStatusPassed,
		Phase:           "final",
		ExitCode:        ExitSuccess,
		RoundsCompleted: 2,
		RoundsRequested: 2,
		ChangedFiles:    []string{"main.go", "README.md"},
		Review:          &Review{Verdict: "pass", Findings: []Finding{{Severity: "minor", File: "main.go", Issue: "nit"}}},
		FinishedAt:      time.Now().UTC(),
	}
	if err := writeJSONWithNewline(filepath.Join(runDir, "final.json"), final); err != nil {
		t.Fatal(err)
	}

	snapshot, err := BuildRunSnapshot(workdir, runDir)
	if err != nil {
		t.Fatalf("BuildRunSnapshot() error = %v", err)
	}
	if snapshot.Status != string(RunStatusPassed) {
		t.Fatalf("status = %q, want %q (final.json is authoritative once present)", snapshot.Status, RunStatusPassed)
	}
	if snapshot.Verdict != "pass" || snapshot.ExitCode != ExitSuccess {
		t.Fatalf("verdict/exit = %q/%d", snapshot.Verdict, snapshot.ExitCode)
	}
	if snapshot.RoundsCompleted != 2 || snapshot.RoundsRequested != 2 {
		t.Fatalf("rounds = %d/%d", snapshot.RoundsCompleted, snapshot.RoundsRequested)
	}
	if len(snapshot.ChangedFiles) != 2 {
		t.Fatalf("changed_files = %#v", snapshot.ChangedFiles)
	}
	if snapshot.FindingsCount != 1 {
		t.Fatalf("findings_count = %d, want 1", snapshot.FindingsCount)
	}
}

func TestBuildRunSnapshot_PreservesStateStatusWhenOlderFinalOmitsIt(t *testing.T) {
	workdir, runDir, runID := newRunDirForSnapshotTest(t)
	state := RunState{RunID: runID, Mode: ModeSupervisor, Status: "finished", Phase: "final", CurrentRound: 1}
	if err := writeJSONWithNewline(filepath.Join(runDir, "state.json"), state); err != nil {
		t.Fatal(err)
	}
	final := FinalRun{
		SchemaVersion:   ArtifactSchemaVersion,
		RunID:           runID,
		RunDir:          runDir,
		Mode:            ModeSupervisor,
		Verdict:         "needs_changes",
		ExitCode:        ExitBlockingFindings,
		RoundsCompleted: 1,
		RoundsRequested: 1,
		FinishedAt:      time.Now().UTC(),
	}
	if err := writeJSONWithNewline(filepath.Join(runDir, "final.json"), final); err != nil {
		t.Fatal(err)
	}

	snapshot, err := BuildRunSnapshot(workdir, runDir)
	if err != nil {
		t.Fatalf("BuildRunSnapshot() error = %v", err)
	}
	if snapshot.Status != "finished" {
		t.Fatalf("status = %q, want state.json fallback when final.json omits status", snapshot.Status)
	}
	if snapshot.Phase != "final" {
		t.Fatalf("phase = %q, want state.json fallback when final.json omits phase", snapshot.Phase)
	}
	if snapshot.Verdict != "needs_changes" {
		t.Fatalf("verdict = %q, want final.json verdict", snapshot.Verdict)
	}
}
func TestBuildRunSnapshot_IncludesPlanSummaryWhenPresent(t *testing.T) {
	workdir, runDir, runID := newRunDirForSnapshotTest(t)
	now := time.Now().UTC()
	plan := ExecutionPlan{
		SchemaVersion: 1,
		RunID:         runID,
		Mode:          ModeSupervisor,
		Status:        "running",
		Items: []PlanItem{
			{ID: "P1", Title: "one", Status: PlanStatusPassed, CreatedAt: now, UpdatedAt: now},
			{ID: "P2", Title: "two", Status: PlanStatusPending, CreatedAt: now, UpdatedAt: now},
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := writeJSONWithNewline(filepath.Join(runDir, "plan.json"), plan); err != nil {
		t.Fatal(err)
	}

	snapshot, err := BuildRunSnapshot(workdir, runDir)
	if err != nil {
		t.Fatalf("BuildRunSnapshot() error = %v", err)
	}
	if snapshot.PlanSummary == nil {
		t.Fatal("expected plan summary to be populated when plan.json exists")
	}
	if snapshot.PlanSummary.Total != 2 || snapshot.PlanSummary.Passed != 1 || snapshot.PlanSummary.Pending != 1 {
		t.Fatalf("plan summary = %#v", snapshot.PlanSummary)
	}
}

func TestBuildRunSnapshot_ToleratesMissingOptionalArtifacts(t *testing.T) {
	workdir, runDir, runID := newRunDirForSnapshotTest(t)

	snapshot, err := BuildRunSnapshot(workdir, runDir)
	if err != nil {
		t.Fatalf("BuildRunSnapshot() with no artifacts should not fail: %v", err)
	}
	if snapshot.RunID != runID {
		t.Fatalf("run_id = %q, want %q", snapshot.RunID, runID)
	}
	if snapshot.PlanSummary != nil {
		t.Fatalf("expected nil plan summary when plan.json is absent, got %#v", snapshot.PlanSummary)
	}
	if snapshot.Status != "" {
		t.Fatalf("expected empty status with no state/final/active artifacts, got %q", snapshot.Status)
	}
}

func TestBuildRunSnapshot_MissingRunDirReturnsError(t *testing.T) {
	workdir := t.TempDir()
	if _, err := BuildRunSnapshot(workdir, filepath.Join(workdir, ".tagteam", "runs", "does-not-exist")); err == nil {
		t.Fatal("expected an error for a missing run directory")
	}
}

func TestBuildRunSnapshotDoesNotFollowArtifactReferencesOutsideRun(t *testing.T) {
	workdir, runDir, runID := newRunDirForSnapshotTest(t)
	outside := filepath.Join(workdir, "outside-review.json")
	if err := writeJSONWithNewline(outside, Review{SchemaVersion: ArtifactSchemaVersion, Verdict: "needs_changes", Findings: []Finding{{Severity: "major", File: "secret", Issue: "outside", Fix: "none"}}}); err != nil {
		t.Fatal(err)
	}
	state := RunState{RunID: runID, Status: "running", LatestReviewPath: outside, LatestDiffPath: filepath.Join(workdir, "outside.patch")}
	if err := writeJSONWithNewline(filepath.Join(runDir, "state.json"), state); err != nil {
		t.Fatal(err)
	}

	snapshot, err := BuildRunSnapshot(workdir, runDir)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.LatestReviewPath != "" || snapshot.LatestDiffPath != "" || snapshot.FindingsCount != 0 {
		t.Fatalf("outside artifacts were projected: %#v", snapshot)
	}
}

func TestBuildRunSnapshot_ExposesDegradedAndBlockingReasonConsistently(t *testing.T) {
	workdir, runDir, runID := newRunDirForSnapshotTest(t)
	final := FinalRun{
		SchemaVersion:  ArtifactSchemaVersion,
		RunID:          runID,
		RunDir:         runDir,
		Mode:           ModeRelay,
		Status:         RunStatusDegraded,
		ExitCode:       ExitSuccess,
		Degraded:       true,
		DegradedReason: "scout_unavailable",
		BlockingReason: "",
		FinishedAt:     time.Now().UTC(),
	}
	if err := writeJSONWithNewline(filepath.Join(runDir, "final.json"), final); err != nil {
		t.Fatal(err)
	}

	snapshot, err := BuildRunSnapshot(workdir, runDir)
	if err != nil {
		t.Fatalf("BuildRunSnapshot() error = %v", err)
	}
	if !snapshot.Degraded || snapshot.DegradedReason != "scout_unavailable" {
		t.Fatalf("degraded fields = %#v", snapshot)
	}
	if snapshot.BlockingReason != "" {
		t.Fatalf("blocking_reason = %q, want empty", snapshot.BlockingReason)
	}
}

// TestBuildRunSnapshot_ActiveJSONFailedOverridesStaleRunningState is a
// regression test for a bug where BuildRunSnapshot let a stale state.json
// "running" status clobber a correctly-marked "failed" active.json. This is
// exactly what happens for runSolo, which (unlike runLoop/Review) has no
// recovery defer to rewrite state.json on a mid-run error: only active.json
// gets updated when the run aborts.
func TestBuildRunSnapshot_ActiveJSONFailedOverridesStaleRunningState(t *testing.T) {
	workdir, runDir, runID := newRunDirForSnapshotTest(t)
	// state.json is left at its initial "running" value, as it would be if
	// the process errored out before ever rewriting it.
	state := RunState{RunID: runID, Mode: ModeSolo, Status: "running", Phase: "solo"}
	if err := writeJSONWithNewline(filepath.Join(runDir, "state.json"), state); err != nil {
		t.Fatal(err)
	}
	activateRun(workdir, runID, runDir, ModeSolo)
	deactivateRun(workdir, runID, false)

	snapshot, err := BuildRunSnapshot(workdir, runDir)
	if err != nil {
		t.Fatalf("BuildRunSnapshot() error = %v", err)
	}
	if snapshot.Status != "failed" {
		t.Fatalf("status = %q, want failed (active.json must win over stale state.json)", snapshot.Status)
	}
}

func TestBuildRunSnapshot_RunningActiveRunIgnoresStaleFinal(t *testing.T) {
	workdir, runDir, runID := newRunDirForSnapshotTest(t)
	state := RunState{RunID: runID, Mode: ModeSupervisor, Status: "running", Phase: "round-2", CurrentRound: 2}
	if err := writeJSONWithNewline(filepath.Join(runDir, "state.json"), state); err != nil {
		t.Fatal(err)
	}
	staleFinal := FinalRun{
		SchemaVersion:  ArtifactSchemaVersion,
		RunID:          runID,
		RunDir:         runDir,
		Mode:           ModeSupervisor,
		Status:         RunStatusFailed,
		Phase:          "final",
		Verdict:        "error",
		ExitCode:       ExitAdapterFailure,
		BlockingReason: "old failure",
		FinishedAt:     time.Now().Add(-time.Minute),
	}
	if err := writeJSONWithNewline(filepath.Join(runDir, "final.json"), staleFinal); err != nil {
		t.Fatal(err)
	}
	activateRun(workdir, runID, runDir, ModeSupervisor)

	snapshot, err := BuildRunSnapshot(workdir, runDir)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != "running" || snapshot.Phase != "round-2" {
		t.Fatalf("live snapshot = %#v", snapshot)
	}
	if snapshot.Verdict != "" || snapshot.ExitCode != 0 || snapshot.BlockingReason != "" {
		t.Fatalf("stale terminal state leaked into live snapshot: %#v", snapshot)
	}
}

func TestBuildRunSnapshot_MatchesCompletedSoloRun(t *testing.T) {
	installFakeClaudeBinary(t)

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "hello\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	app := NewApp(DefaultConfig())
	final, err := app.Run(context.Background(), RunOptions{
		Prompt:  "add a feature",
		Workdir: repo,
		Mode:    ModeSolo,
		Coder:   RoleTarget{Adapter: "claude"},
		Rounds:  1,
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	snapshot, err := BuildRunSnapshot(repo, final.RunDir)
	if err != nil {
		t.Fatalf("BuildRunSnapshot() error = %v", err)
	}
	if snapshot.RunID != final.RunID {
		t.Fatalf("run_id = %q, want %q", snapshot.RunID, final.RunID)
	}
	if snapshot.Status != string(final.Status) {
		t.Fatalf("status = %q, want %q", snapshot.Status, final.Status)
	}
	if snapshot.Verdict != final.Verdict || snapshot.ExitCode != final.ExitCode {
		t.Fatalf("verdict/exit = %q/%d, want %q/%d", snapshot.Verdict, snapshot.ExitCode, final.Verdict, final.ExitCode)
	}
}
