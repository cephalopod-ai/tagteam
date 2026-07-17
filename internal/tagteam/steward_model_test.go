package tagteam

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func stewardChatServer(t *testing.T, content string, captured *string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if captured != nil {
			mu.Lock()
			*captured = string(body)
			mu.Unlock()
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": content}}},
		})
	}))
	t.Cleanup(server.Close)
	return server
}

func TestModelStewardParsesAdvisoryFromLocalModel(t *testing.T) {
	server := stewardChatServer(t, `{"action":"notify","reason":"change is ready to review"}`, nil)
	steward := &ModelSteward{BaseURL: server.URL, Model: "test-model"}
	observation := RunObservation{SchemaVersion: StewardContractVersion, RunID: "run-1", Status: string(RunStatusPassed)}
	advisory, err := steward.Advise(context.Background(), observation)
	if err != nil {
		t.Fatal(err)
	}
	if advisory.Action != StewardNotify || advisory.Source != "model" {
		t.Fatalf("advisory = %#v", advisory)
	}
	// The wrapper validates and pins identity; the raw model advisory omits it.
	final := AdviseWithFallback(context.Background(), steward, observation, time.Second)
	if final.Action != StewardNotify || final.RunID != "run-1" || final.Source != "model" {
		t.Fatalf("final advisory = %#v", final)
	}
}

func TestModelStewardSendsNoToolsAndOnlySanitizedObservation(t *testing.T) {
	var captured string
	server := stewardChatServer(t, `{"action":"wait","reason":"ok"}`, &captured)
	steward := &ModelSteward{BaseURL: server.URL, Model: "test-model"}
	observation := RunObservation{SchemaVersion: StewardContractVersion, RunID: "run-1", Status: string(RunStatusRunning), Phase: "implementing"}
	if _, err := steward.Advise(context.Background(), observation); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(captured), &payload); err != nil {
		t.Fatalf("captured payload not JSON: %v", err)
	}
	// Recursion prevention: no tool/function surface is ever advertised.
	for _, forbidden := range []string{"tools", "functions", "tool_choice", "function_call"} {
		if _, ok := payload[forbidden]; ok {
			t.Fatalf("steward request advertised forbidden field %q: %s", forbidden, captured)
		}
	}
	// The sanitized observation crosses the boundary; repo contents never do.
	if !strings.Contains(captured, "implementing") || !strings.Contains(captured, string(RunStatusRunning)) {
		t.Fatalf("observation not forwarded: %s", captured)
	}
	if strings.Contains(captured, "/etc/") || strings.Contains(captured, ".go\"") {
		t.Fatalf("request leaked repository-shaped content: %s", captured)
	}
}

func TestModelStewardRejectsNonJSONContent(t *testing.T) {
	server := stewardChatServer(t, "I think you should probably wait a bit.", nil)
	steward := &ModelSteward{BaseURL: server.URL, Model: "test-model"}
	observation := RunObservation{SchemaVersion: StewardContractVersion, RunID: "run-1", Status: string(RunStatusRunning)}
	if _, err := steward.Advise(context.Background(), observation); err == nil {
		t.Fatal("non-JSON steward content was accepted")
	}
	// End to end, the wrapper falls back to the deterministic template.
	final := AdviseWithFallback(context.Background(), steward, observation, time.Second)
	if final.Source != "deterministic" {
		t.Fatalf("non-JSON model did not fall back: %#v", final)
	}
}

type countingSteward struct {
	mu    sync.Mutex
	calls int
}

func (c *countingSteward) Advise(_ context.Context, observation RunObservation) (StewardAdvisory, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return StewardAdvisory{SchemaVersion: StewardContractVersion, RunID: observation.RunID, Action: StewardWait, Reason: "ok", Source: "model"}, nil
}

func TestBudgetedStewardEnforcesCallCap(t *testing.T) {
	counter := &countingSteward{}
	budgeted := NewBudgetedSteward(counter, 2, 0)
	observations := []RunObservation{
		{RunID: "r", Status: "a"}, {RunID: "r", Status: "b"}, {RunID: "r", Status: "c"},
	}
	results := make([]error, len(observations))
	for i, observation := range observations {
		_, results[i] = budgeted.Advise(context.Background(), observation)
	}
	if results[0] != nil || results[1] != nil {
		t.Fatalf("first two calls errored: %v %v", results[0], results[1])
	}
	if results[2] == nil {
		t.Fatal("third call was not rejected by the call budget")
	}
	if counter.calls != 2 {
		t.Fatalf("primary called %d times, want 2", counter.calls)
	}
}

func TestBudgetedStewardDedupsWithinInterval(t *testing.T) {
	counter := &countingSteward{}
	budgeted := NewBudgetedSteward(counter, 0, 30*time.Second)
	base := time.Unix(1_700_000_000, 0).UTC()
	budgeted.now = func() time.Time { return base }
	observation := RunObservation{RunID: "r", Status: string(RunStatusRunning), Phase: "implementing"}

	if _, err := budgeted.Advise(context.Background(), observation); err != nil {
		t.Fatal(err)
	}
	if _, err := budgeted.Advise(context.Background(), observation); err != nil {
		t.Fatal(err)
	}
	if counter.calls != 1 {
		t.Fatalf("identical observation within interval called primary %d times, want 1", counter.calls)
	}
	// Advancing past the interval re-queries the primary.
	budgeted.now = func() time.Time { return base.Add(time.Minute) }
	if _, err := budgeted.Advise(context.Background(), observation); err != nil {
		t.Fatal(err)
	}
	if counter.calls != 2 {
		t.Fatalf("observation after interval called primary %d times, want 2", counter.calls)
	}
}

func TestStewardLeaseIsExclusivePerRun(t *testing.T) {
	runDir := t.TempDir()
	lease, err := acquireStewardLease(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireStewardLease(runDir); err == nil {
		t.Fatal("second steward observer acquired the lease")
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	again, err := acquireStewardLease(runDir)
	if err != nil {
		t.Fatalf("lease not reacquirable after release: %v", err)
	}
	_ = again.Release()
}

func TestBuildStewardDisabledReturnsDeterministic(t *testing.T) {
	disabled := false
	enabled := true
	steward := BuildSteward(StewardConfig{Enabled: &disabled, BaseURL: "http://127.0.0.1:11434/v1", Model: "x"}, nil)
	if _, ok := steward.(DeterministicSteward); !ok {
		t.Fatalf("disabled steward = %T, want DeterministicSteward", steward)
	}
	steward = BuildSteward(StewardConfig{Enabled: &enabled, BaseURL: "", Model: "x"}, nil)
	if _, ok := steward.(DeterministicSteward); !ok {
		t.Fatalf("unconfigured steward = %T, want DeterministicSteward", steward)
	}
}

func TestBuildStewardEnabledBuildsModelTier(t *testing.T) {
	server := stewardChatServer(t, `{"action":"inspect","reason":"stalled worker"}`, nil)
	enabled := true
	cfg := StewardConfig{Enabled: &enabled, BaseURL: server.URL, Model: "test-model", MaxCallsPerRun: 5}
	steward := BuildSteward(cfg, nil)
	if _, ok := steward.(*BudgetedSteward); !ok {
		t.Fatalf("enabled steward = %T, want *BudgetedSteward", steward)
	}
	observation := RunObservation{SchemaVersion: StewardContractVersion, RunID: "run-1", Status: string(RunStatusRunning), NoProgressFor: "5m"}
	final := AdviseWithFallback(context.Background(), steward, observation, time.Second)
	if final.Action != StewardInspect || final.Source != "model" {
		t.Fatalf("model-tier advisory = %#v", final)
	}
}
