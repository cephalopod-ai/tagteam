package tagteam

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func writeRunningStateFixture(t *testing.T, runDir, runID string) {
	t.Helper()
	state := RunState{
		SchemaVersion: runStateSchemaVersion,
		RunID:         runID,
		Mode:          ModeSupervisor,
		Status:        string(RunStatusRunning),
		Phase:         string(PhaseImplementing),
		UpdatedAt:     time.Now().UTC(),
	}
	if err := writeJSONWithNewline(filepath.Join(runDir, "state.json"), state); err != nil {
		t.Fatal(err)
	}
}

func TestControlRuntimeAdviseProducesDeterministicAdvisory(t *testing.T) {
	service, runID, runDir := controlServiceFixture(t)
	writeRunningStateFixture(t, runDir, runID)
	runtime := NewControlRuntime(service, DefaultConfig(), nil)

	advisory, err := runtime.Advise(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	// With the steward disabled by default, a healthy running run yields a
	// deterministic wait advisory pinned to the run.
	if advisory.Action != StewardWait || advisory.Source != "deterministic" || advisory.RunID != runID {
		t.Fatalf("advisory = %#v", advisory)
	}
	if err := ValidateStewardAdvisory(advisory); err != nil {
		t.Fatalf("advise produced an invalid advisory: %v", err)
	}
}

func TestControlRuntimeAdviseRejectsUnknownRun(t *testing.T) {
	service, _, _ := controlServiceFixture(t)
	runtime := NewControlRuntime(service, DefaultConfig(), nil)
	if _, err := runtime.Advise(context.Background(), "no-such-run"); err == nil {
		t.Fatal("advise on an unknown run did not error")
	}
}

func TestMCPStdioServerExposesAdviseTool(t *testing.T) {
	service, runID, runDir := controlServiceFixture(t)
	writeRunningStateFixture(t, runDir, runID)
	runtime := NewControlRuntime(service, DefaultConfig(), nil)
	responses := runMCPStdioWithRuntime(t, service, runtime,
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "tagteam_advise", "arguments": map[string]any{"run_id": runID}}},
	)
	result := responses[1]["result"].(map[string]any)
	if result["isError"] != false {
		t.Fatalf("advise result = %#v", result)
	}
	structured := result["structuredContent"].(map[string]any)
	if structured["action"] != string(StewardWait) || structured["source"] != "deterministic" {
		t.Fatalf("advise structured content = %#v", structured)
	}
	if structured["run_id"] != runID {
		t.Fatalf("advise run_id = %v, want %q", structured["run_id"], runID)
	}
}
