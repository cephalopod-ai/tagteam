package tagteam

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestNewRunObservationSanitizesAndBounds(t *testing.T) {
	snapshot := RunSnapshot{
		SchemaVersion:   ArtifactSchemaVersion,
		RunID:           "run-1",
		Mode:            ModeSupervisor,
		Status:          string(RunStatusRunning),
		Phase:           strings.Repeat("p", stewardMaxReasonBytes+50),
		Verdict:         "needs_changes",
		FindingsCount:   3,
		OpenMajorCount:  1,
		CurrentRound:    2,
		RoundsRequested: 3,
		ChangedFiles:    []string{"internal/a.go", "internal/b.go", "README.md"},
		LiveProgress:    &LiveProgress{WaitingFor: "worker", NoProgressFor: "3m"},
		UpdatedAt:       time.Now().UTC(),
	}
	observation := NewRunObservation(snapshot)
	if observation.SchemaVersion != StewardContractVersion {
		t.Fatalf("schema version = %d", observation.SchemaVersion)
	}
	if observation.ChangedFileCount != 3 {
		t.Fatalf("changed file count = %d, want 3", observation.ChangedFileCount)
	}
	if observation.WaitingFor != "worker" || observation.NoProgressFor != "3m" {
		t.Fatalf("live progress projection = %#v", observation)
	}
	// The changed-file list itself must never cross the boundary; only a count.
	if !strings.HasSuffix(observation.Phase, "...[truncated]") || len(observation.Phase) > stewardMaxReasonBytes+len("...[truncated]") {
		t.Fatalf("phase not bounded: len=%d", len(observation.Phase))
	}
	if observation.FindingsCount != 3 || observation.OpenMajorCount != 1 {
		t.Fatalf("counts = %#v", observation)
	}
}

func TestDeterministicAdvisoryCoversEveryStatus(t *testing.T) {
	cases := []struct {
		name   string
		obs    RunObservation
		action StewardAction
	}{
		{"passed", RunObservation{Status: string(RunStatusPassed)}, StewardNotify},
		{"degraded", RunObservation{Status: string(RunStatusDegraded), DegradedReason: "reviewer_unavailable"}, StewardNotify},
		{"cancelled", RunObservation{Status: string(RunStatusCancelled)}, StewardNotify},
		{"failed", RunObservation{Status: string(RunStatusFailed), BlockingReason: "test_failed"}, StewardReportIssue},
		{"recovery", RunObservation{Status: string(RunStatusRunning), RecoveryStatus: "recovery_required"}, StewardPrepareResume},
		{"stalled", RunObservation{Status: string(RunStatusRunning), NoProgressFor: "5m"}, StewardInspect},
		{"blocked-running", RunObservation{Status: string(RunStatusRunning), BlockingReason: "worker_unavailable"}, StewardReportIssue},
		{"running", RunObservation{Status: string(RunStatusRunning)}, StewardWait},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			advisory := DeterministicAdvisory(tc.obs)
			if advisory.Action != tc.action {
				t.Fatalf("action = %q, want %q", advisory.Action, tc.action)
			}
			if advisory.Source != "deterministic" {
				t.Fatalf("source = %q, want deterministic", advisory.Source)
			}
			if err := ValidateStewardAdvisory(advisory); err != nil {
				t.Fatalf("deterministic advisory is invalid: %v", err)
			}
		})
	}
}

func TestValidateStewardAdvisory(t *testing.T) {
	valid := StewardAdvisory{SchemaVersion: StewardContractVersion, RunID: "r", Action: StewardWait, Reason: "ok"}
	if err := ValidateStewardAdvisory(valid); err != nil {
		t.Fatalf("valid advisory rejected: %v", err)
	}
	bad := []StewardAdvisory{
		{SchemaVersion: 99, Action: StewardWait, Reason: "x"},
		{SchemaVersion: StewardContractVersion, Action: "delete_repo", Reason: "x"},
		{SchemaVersion: StewardContractVersion, Action: StewardWait, Reason: "   "},
		{SchemaVersion: StewardContractVersion, Action: StewardWait, Reason: strings.Repeat("x", stewardMaxReasonBytes+1)},
	}
	for i, advisory := range bad {
		if err := ValidateStewardAdvisory(advisory); err == nil {
			t.Fatalf("invalid advisory %d was accepted", i)
		}
	}
}

type fakeSteward struct {
	advisory StewardAdvisory
	err      error
	block    time.Duration
}

func (s fakeSteward) Advise(ctx context.Context, _ RunObservation) (StewardAdvisory, error) {
	if s.block > 0 {
		select {
		case <-ctx.Done():
			return StewardAdvisory{}, ctx.Err()
		case <-time.After(s.block):
		}
	}
	return s.advisory, s.err
}

func TestAdviseWithFallbackUsesDeterministicOnErrorOrNil(t *testing.T) {
	obs := RunObservation{SchemaVersion: StewardContractVersion, RunID: "run-x", Status: string(RunStatusRunning)}
	if got := AdviseWithFallback(context.Background(), nil, obs, time.Second); got.Source != "deterministic" {
		t.Fatalf("nil steward advisory source = %q", got.Source)
	}
	failing := fakeSteward{err: context.DeadlineExceeded}
	got := AdviseWithFallback(context.Background(), failing, obs, time.Second)
	if got.Source != "deterministic" || got.Action != StewardWait {
		t.Fatalf("erroring steward did not fall back: %#v", got)
	}
}

func TestAdviseWithFallbackFallsBackOnTimeoutWithoutBlocking(t *testing.T) {
	obs := RunObservation{SchemaVersion: StewardContractVersion, RunID: "run-x", Status: string(RunStatusRunning)}
	slow := fakeSteward{block: 5 * time.Second, advisory: StewardAdvisory{SchemaVersion: StewardContractVersion, Action: StewardInspect, Reason: "slow"}}
	start := time.Now()
	got := AdviseWithFallback(context.Background(), slow, obs, 100*time.Millisecond)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("fallback blocked on the slow steward for %s", elapsed)
	}
	if got.Source != "deterministic" {
		t.Fatalf("slow steward did not fall back: %#v", got)
	}
}

func TestAdviseWithFallbackRejectsInvalidModelAdvisory(t *testing.T) {
	obs := RunObservation{SchemaVersion: StewardContractVersion, RunID: "run-x", Status: string(RunStatusRunning)}
	invalid := fakeSteward{advisory: StewardAdvisory{SchemaVersion: StewardContractVersion, Action: "escalate_to_root", Reason: "nope"}}
	got := AdviseWithFallback(context.Background(), invalid, obs, time.Second)
	if got.Source != "deterministic" {
		t.Fatalf("invalid model advisory was not rejected: %#v", got)
	}
}

func TestAdviseWithFallbackAcceptsValidModelAdvisoryAndPinsRunID(t *testing.T) {
	obs := RunObservation{SchemaVersion: StewardContractVersion, RunID: "run-x", Status: string(RunStatusRunning)}
	// A model that returns a valid action but tries to reassign the run id and
	// omits provenance: the wrapper pins the run id and stamps a source.
	model := fakeSteward{advisory: StewardAdvisory{Action: StewardNotify, Reason: "change is ready", RunID: "other-run"}}
	got := AdviseWithFallback(context.Background(), model, obs, time.Second)
	if got.Action != StewardNotify || got.RunID != "run-x" {
		t.Fatalf("accepted advisory = %#v, want notify pinned to run-x", got)
	}
	if got.Source != "steward" {
		t.Fatalf("advisory source = %q, want steward", got.Source)
	}
	if err := ValidateStewardAdvisory(got); err != nil {
		t.Fatalf("accepted advisory is invalid: %v", err)
	}
}
