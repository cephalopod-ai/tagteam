package tagteam

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
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

func TestControlRuntimeAdviseKeepsOneBudgetPerRun(t *testing.T) {
	service, runID, runDir := controlServiceFixture(t)
	writeRunningStateFixture(t, runDir, runID)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": `{"action":"inspect","reason":"model result"}`}}},
		})
	}))
	t.Cleanup(server.Close)
	enabled := true
	cfg := DefaultConfig()
	cfg.Steward = StewardConfig{
		Enabled:        &enabled,
		BaseURL:        server.URL,
		Model:          "test-model",
		TimeoutSeconds: 1,
		MaxCallsPerRun: 1,
	}
	runtime := NewControlRuntime(service, cfg, nil)
	first, err := runtime.Advise(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Source != "model" {
		t.Fatalf("first advisory source = %q, want model", first.Source)
	}
	second, err := runtime.Advise(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if second.Source != "deterministic" {
		t.Fatalf("budget-exhausted advisory source = %q, want deterministic", second.Source)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("model calls = %d, want one per-run budgeted call", got)
	}
}

func TestControlRuntimeStewardCacheIsBounded(t *testing.T) {
	runtime := NewControlRuntime(ControlService{}, DefaultConfig(), nil)
	for index := 0; index < controlMaxStewardStates; index++ {
		runtime.stewards[fmt.Sprintf("run-%d", index)] = DeterministicSteward{}
	}

	steward := runtime.stewardForRun("overflow-run")
	if _, ok := steward.(DeterministicSteward); !ok {
		t.Fatalf("overflow steward = %T, want deterministic fallback", steward)
	}
	if got := len(runtime.stewards); got != controlMaxStewardStates {
		t.Fatalf("steward cache size = %d, want %d", got, controlMaxStewardStates)
	}
	if _, cached := runtime.stewards["overflow-run"]; cached {
		t.Fatal("overflow steward was cached")
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
