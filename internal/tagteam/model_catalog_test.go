package tagteam

import (
	"context"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestModelListCommandRestrictsEnvironment(t *testing.T) {
	t.Setenv("UNRELATED_CATALOG_SECRET", "do-not-forward")
	cmd := newModelListCommand(context.Background(), "agy", t.TempDir(), map[string]string{"CATALOG_OVERLAY": "forwarded"}, "models")
	env := strings.Join(cmd.Env, "\n")
	if strings.Contains(env, "UNRELATED_CATALOG_SECRET=") || !strings.Contains(env, "CATALOG_OVERLAY=forwarded") {
		t.Fatalf("model-list environment is not restricted correctly: %q", env)
	}
}

func TestMaintainedModelTargetsIncludeRequestedFrontierModels(t *testing.T) {
	targets := MaintainedModelTargets()
	for _, want := range []string{"codex:gpt-6-astra", "claude:claude-fable-5-1", "grok:grok-4.6", "agy:gemini-3.8-flash-medium"} {
		found := false
		for _, target := range targets {
			if target == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("maintained targets are missing %q: %#v", want, targets)
		}
	}
}

func TestDiscoverModelCatalogsPreservesFallbacks(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	cfg := DefaultConfig()
	cfg.Adapters.MistralAcp.Binary = "missing-vibe-acp"
	want := map[string]string{"codex": "maintained", "codex-oss": "config", "claude": "maintained", "agy": "maintained", "gosling": "config", "grok": "maintained", "openai-compatible": "config", "mistral-acp": "config"}
	entries := DiscoverModelCatalogs(context.Background(), cfg, t.TempDir())
	if len(entries) != len(want) {
		t.Fatalf("catalog entries = %d, want %d", len(entries), len(want))
	}
	for _, entry := range entries {
		if entry.Source != want[entry.Adapter] || (entry.Source == "maintained" && len(entry.Models) == 0) {
			t.Errorf("%s fallback = source %q, models %#v", entry.Adapter, entry.Source, entry.Models)
		}
		if (entry.Adapter == "agy" || entry.Adapter == "grok" || entry.Adapter == "mistral-acp") && entry.Error == "" {
			t.Errorf("%s should retain a visible discovery warning", entry.Adapter)
		}
	}
}

func TestDiscoverModelCatalogsRedactsProviderWarnings(t *testing.T) {
	oldCommandContext := execCommandContext
	t.Cleanup(func() { execCommandContext = oldCommandContext })

	const secret = "catalog-overlay-secret"
	execCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", `printf '%s' "$CATALOG_API_KEY" >&2; exit 1`)
	}
	cfg := DefaultConfig()
	cfg.EnvOverlay = map[string]string{"CATALOG_API_KEY": secret}
	entries := DiscoverModelCatalogs(context.Background(), cfg, t.TempDir())

	foundRedactedWarning := false
	for _, entry := range entries {
		if strings.Contains(entry.Error, secret) {
			t.Fatalf("%s warning disclosed the overlay secret: %q", entry.Adapter, entry.Error)
		}
		if strings.Contains(entry.Error, redactedSecret) {
			foundRedactedWarning = true
		}
	}
	if !foundRedactedWarning {
		t.Fatal("expected at least one redacted provider warning")
	}
}

func TestParseAgyModelList(t *testing.T) {
	raw := []byte("Fetching available models...\ngemini-3.8-flash-high\tGemini 3.8 Flash (High)\ngemini-3.8-flash-low\tGemini 3.8 Flash (Low)\n")
	want := []string{"gemini-3.8-flash-high", "gemini-3.8-flash-low"}
	if got := parseAgyModelList(raw); !reflect.DeepEqual(got, want) {
		t.Fatalf("parseAgyModelList() = %#v, want %#v", got, want)
	}
}

func TestParseGrokModelList(t *testing.T) {
	raw := []byte("Default model: grok-4.6\n\nAvailable models:\n  * grok-4.6 (default)\n  - grok-4.5\n")
	models, defaultModel := parseGrokModelList(raw)
	want := []string{"grok-4.6", "grok-4.5"}
	if !reflect.DeepEqual(models, want) || defaultModel != "grok-4.6" {
		t.Fatalf("parseGrokModelList() = %#v, %q; want %#v, grok-4.6", models, defaultModel, want)
	}
}

func TestACPModelConfig(t *testing.T) {
	current, models := acpModelConfig([]acpSessionConfigOption{{
		ID:           "model",
		CurrentValue: "codestral-current",
		Options: []acpSessionConfigChoice{
			{Value: "codestral-current"},
			{Value: "mistral-large-latest"},
		},
	}})
	if current != "codestral-current" || !reflect.DeepEqual(models, []string{"codestral-current", "mistral-large-latest"}) {
		t.Fatalf("acpModelConfig() = %q, %#v", current, models)
	}
}

func TestMistralAcpDiscoverModelsFromSessionConfig(t *testing.T) {
	adapter := fakeMistralAcpAdapter(t, "discovery")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	discovery, err := adapter.DiscoverModels(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("DiscoverModels() error = %v", err)
	}
	if discovery.Source != "acp" || discovery.Default != "codestral-current" {
		t.Fatalf("discovery = %#v", discovery)
	}
	want := []string{"codestral-current", "mistral-large-latest"}
	if !reflect.DeepEqual(discovery.Models, want) {
		t.Fatalf("models = %#v, want %#v", discovery.Models, want)
	}
}

// TestMistralAcpDiscoverModelsReportsMissingModelOption exercises the path
// that reads the agent's stderr while the process is still running. Under
// -race it is also the regression test for that concurrent read.
func TestMistralAcpDiscoverModelsReportsMissingModelOption(t *testing.T) {
	adapter := fakeMistralAcpAdapter(t, "no_model_option")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := adapter.DiscoverModels(ctx, t.TempDir())
	if err == nil {
		t.Fatal("expected an error when the session advertises no model options")
	}
	if !strings.Contains(err.Error(), "advertised no model options") {
		t.Fatalf("error = %v", err)
	}
	// The agent's own diagnostic must survive into the operator-facing error;
	// it is only complete once the agent has been reaped.
	if !strings.Contains(err.Error(), "this build advertises no model options") {
		t.Fatalf("error dropped the agent diagnostic: %v", err)
	}
}
