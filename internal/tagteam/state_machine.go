package tagteam

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const runStateSchemaVersion = 2

type RunPhase string

const (
	PhasePlanning     RunPhase = "planning"
	PhaseImplementing RunPhase = "implementing"
	PhaseTesting      RunPhase = "testing"
	PhaseReviewing    RunPhase = "reviewing"
	PhaseRepairing    RunPhase = "repairing"
)

type StateEvent struct {
	SchemaVersion int       `json:"schema_version"`
	RunID         string    `json:"run_id"`
	FromPhase     RunPhase  `json:"from_phase,omitempty"`
	ToPhase       RunPhase  `json:"to_phase"`
	Status        string    `json:"status"`
	Round         int       `json:"round,omitempty"`
	InvocationID  string    `json:"invocation_id,omitempty"`
	DiffHash      string    `json:"diff_hash,omitempty"`
	At            time.Time `json:"at"`
}

func normalizeRunPhase(raw string) RunPhase {
	raw = strings.ToLower(strings.TrimSpace(raw))
	switch {
	case raw == "testing" || strings.Contains(raw, "test"):
		return PhaseTesting
	case raw == "repairing" || strings.Contains(raw, "repair") || strings.Contains(raw, "fix"):
		return PhaseRepairing
	case raw == "reviewing" || strings.Contains(raw, "review") || strings.Contains(raw, "adversary"):
		return PhaseReviewing
	case raw == "implementing" || raw == "diff" || raw == "solo" || strings.Contains(raw, "worker") || strings.Contains(raw, "coder"):
		return PhaseImplementing
	default:
		return PhasePlanning
	}
}

func completedPhaseForTransition(previous RunState, next RunState) RunPhase {
	if next.Status == string(RunStatusPassed) || next.Status == string(RunStatusDegraded) || next.Status == string(RunStatusBlocked) {
		current := normalizeRunPhase(next.Phase)
		if phaseOrder(current) > phaseOrder(previous.CompletedPhase) {
			return current
		}
	}
	if previous.Phase == "" || previous.Phase == next.Phase {
		return previous.CompletedPhase
	}
	previousPhase := normalizeRunPhase(previous.Phase)
	if phaseOrder(previousPhase) > phaseOrder(previous.CompletedPhase) {
		return previousPhase
	}
	return previous.CompletedPhase
}

func phaseOrder(phase RunPhase) int {
	switch phase {
	case PhasePlanning:
		return 1
	case PhaseImplementing:
		return 2
	case PhaseTesting:
		return 3
	case PhaseReviewing:
		return 4
	case PhaseRepairing:
		return 5
	default:
		return 0
	}
}

func persistRunState(runDir string, state RunState) error {
	if runDir == "" {
		return nil
	}
	previous, _ := readRunState(runDir)
	state.SchemaVersion = runStateSchemaVersion
	state.Phase = string(normalizeRunPhase(state.Phase))
	state.CompletedPhase = completedPhaseForTransition(previous, state)
	state.UpdatedAt = time.Now().UTC()
	if state.BaselineSHA == "" {
		state.BaselineSHA = previous.BaselineSHA
	}
	if state.Workdir == "" {
		state.Workdir = previous.Workdir
	}
	if state.RepoID == "" {
		state.RepoID = previous.RepoID
		if state.RepoID == "" {
			state.RepoID = filepath.Base(filepath.Dir(filepath.Dir(runDir)))
		}
	}
	if state.BaselineSHA == "" || state.Workdir == "" {
		if meta, err := readMeta(filepath.Join(runDir, "meta.json")); err == nil {
			if state.BaselineSHA == "" {
				state.BaselineSHA = meta.Baseline
			}
			if state.Workdir == "" {
				state.Workdir = meta.Workdir
			}
		}
	}
	if state.DiffHash == "" {
		state.DiffHash = diffHashForState(state.LatestDiffPath)
	}
	if err := writeJSONWithNewline(filepath.Join(runDir, "state.json"), state); err != nil {
		return err
	}
	if previous.Phase == state.Phase && previous.Status == state.Status && previous.DiffHash == state.DiffHash && previous.CurrentRound == state.CurrentRound {
		return nil
	}
	return appendStateEvent(filepath.Join(runDir, "events.jsonl"), StateEvent{
		SchemaVersion: runStateSchemaVersion,
		RunID:         state.RunID,
		FromPhase:     normalizeRunPhase(previous.Phase),
		ToPhase:       normalizeRunPhase(state.Phase),
		Status:        state.Status,
		Round:         state.CurrentRound,
		InvocationID:  state.InvocationID,
		DiffHash:      state.DiffHash,
		At:            state.UpdatedAt,
	})
}

func appendStateEvent(path string, event StateEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	return file.Sync()
}

func diffHashForState(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	hash, err := hashBytes(data)
	if err != nil {
		return ""
	}
	return hash
}

func recordInvocationState(req *Request, record DeliveryRecord) {
	if err := rebindRequestControlResume(req, "state.json"); err != nil {
		return
	}
	if req.RunDir == "" {
		return
	}
	state, err := readRunState(req.RunDir)
	if err != nil {
		return
	}
	state.Status = "running"
	state.Phase = string(invocationPhase(record.Role, req.Phase))
	state.Role = string(record.Role)
	state.Adapter = record.Adapter
	state.Model = record.Model
	state.InvocationID = record.InvocationID
	if state.BaselineSHA != "" && req.Workdir != "" {
		indexPath := filepath.Join(req.RunDir, "invocation-state.index")
		if patch, diffErr := deterministicDiffPatch(contextOrBackground(req.Context), req.Workdir, state.BaselineSHA, indexPath); diffErr == nil {
			state.DiffHash = sha256Sum(patch)
		}
	}
	if err := guardControlResumeWritePath(req.controlResumeGate, filepath.Join(req.RunDir, "state.json")); err != nil {
		return
	}
	_ = persistRunState(req.RunDir, state)
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx != nil {
		return ctx
	}
	return context.Background()
}

func invocationPhase(role Role, description string) RunPhase {
	if strings.Contains(strings.ToLower(description), "repair") {
		return PhaseRepairing
	}
	switch role {
	case RoleCoder:
		return PhaseImplementing
	case RoleAdversary, RoleReporter:
		return PhaseReviewing
	default:
		return PhasePlanning
	}
}

func hashBytes(data []byte) (string, error) {
	if data == nil {
		return "", fmt.Errorf("cannot hash nil data")
	}
	sum := sha256Sum(data)
	return sum, nil
}
