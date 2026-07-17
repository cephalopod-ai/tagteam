package tagteam

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPersistRunStateJournalFailureDoesNotCommitCanonicalState(t *testing.T) {
	runDir := t.TempDir()
	stateWrites := 0
	err := persistRunStateWithIO(
		runDir,
		RunState{RunID: "run-1", Status: "running", Phase: "testing"},
		func(string, any) error {
			stateWrites++
			return nil
		},
		func(string, StateEvent) error { return errors.New("journal full") },
	)
	if err == nil || !strings.Contains(err.Error(), "journal") {
		t.Fatalf("persistRunStateWithIO error = %v", err)
	}
	if stateWrites != 0 {
		t.Fatalf("canonical state writes = %d, want zero", stateWrites)
	}
}

func TestPersistRunStateSnapshotFailureOccursAfterJournal(t *testing.T) {
	runDir := t.TempDir()
	journalWrites := 0
	err := persistRunStateWithIO(
		runDir,
		RunState{RunID: "run-1", Status: "running", Phase: "testing"},
		func(string, any) error { return errors.New("state full") },
		func(string, StateEvent) error {
			journalWrites++
			return nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), "canonical run state") {
		t.Fatalf("persistRunStateWithIO error = %v", err)
	}
	if journalWrites != 1 {
		t.Fatalf("journal writes = %d, want one", journalWrites)
	}
}

func TestPersistTerminalRunRewritesSuccessAsPersistenceFailure(t *testing.T) {
	final := FinalRun{RunID: "run-1", RunDir: "/tmp/run-1", Status: RunStatusPassed, Verdict: "done"}
	finalWrites := []FinalRun{}
	stateWrites := []RunState{}
	failFirstFinal := true
	err := persistTerminalRunWith(
		&final,
		RunState{RunID: final.RunID, Status: string(RunStatusPassed), Phase: "reviewing"},
		func(value RunState) error {
			stateWrites = append(stateWrites, value)
			return nil
		},
		func(value FinalRun) error {
			finalWrites = append(finalWrites, value)
			if failFirstFinal {
				failFirstFinal = false
				return errors.New("final disk full")
			}
			return nil
		},
	)
	if err == nil {
		t.Fatal("terminal persistence failure was suppressed")
	}
	if final.Status != RunStatusFailed || final.ExitCode != ExitAdapterFailure || final.BlockingReason != string(ReasonPersistenceFailed) {
		t.Fatalf("final = %#v", final)
	}
	if len(finalWrites) != 2 || finalWrites[1].Status != RunStatusFailed {
		t.Fatalf("final writes = %#v", finalWrites)
	}
	if len(stateWrites) != 2 || stateWrites[1].Status != string(RunStatusFailed) {
		t.Fatalf("state writes = %#v", stateWrites)
	}
}

func TestPersistTerminalRunPropagatesErrorFinalizationStateFailure(t *testing.T) {
	final := FinalRun{
		RunID:          "run-1",
		RunDir:         "/tmp/run-1",
		Status:         RunStatusFailed,
		Verdict:        "error",
		ExitCode:       ExitAdapterFailure,
		BlockingReason: string(ReasonWorkerUnavailable),
	}
	stateAttempts := 0
	finalWrites := []FinalRun{}
	err := persistTerminalRunWith(
		&final,
		RunState{RunID: final.RunID, Status: string(RunStatusFailed), Phase: "implementing"},
		func(RunState) error {
			stateAttempts++
			if stateAttempts == 1 {
				return errors.New("state write failed")
			}
			return nil
		},
		func(value FinalRun) error {
			finalWrites = append(finalWrites, value)
			return nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), "state write failed") {
		t.Fatalf("error-finalization persistence error = %v", err)
	}
	if stateAttempts != 2 || len(finalWrites) != 1 {
		t.Fatalf("state attempts=%d final writes=%d", stateAttempts, len(finalWrites))
	}
	if finalWrites[0].BlockingReason != string(ReasonPersistenceFailed) || finalWrites[0].Status != RunStatusFailed {
		t.Fatalf("failure terminal = %#v", finalWrites[0])
	}
}

func TestCaptureAndTestRoundPropagatesQualityGateWriteFailure(t *testing.T) {
	repo := autostashFixture(t)
	baseline := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	runDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(runDir, "quality-gates-round-1.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	final := FinalRun{RunID: "run-1", RunDir: runDir, Workdir: repo, Mode: ModeSolo}
	_, _, err := captureAndTestRound(
		context.Background(),
		RunOptions{Workdir: repo, Mode: ModeSolo},
		baseline,
		runDir,
		final.RunID,
		1,
		nil,
		&final,
	)
	if err == nil || !strings.Contains(err.Error(), "mandatory quality gate") {
		t.Fatalf("captureAndTestRound error = %v", err)
	}
}
