package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cephalopod-ai/tagteam/internal/tagteam"
)

func runModelsCommand(t *testing.T, asJSON bool) (string, error) {
	t.Helper()
	oldDiscover := discoverModelCatalogs
	discoverModelCatalogs = func(context.Context, tagteam.Config, string) []tagteam.ModelCatalogEntry {
		return []tagteam.ModelCatalogEntry{
			{Adapter: "codex", Source: "maintained", Default: "gpt-6-astra", Models: []string{"gpt-6-astra"}},
			{Adapter: "grok", Source: "cli", Default: "grok-4.6", Models: []string{"grok-4.6", "grok-4.5"}},
			{Adapter: "mistral-acp", Source: "config", Error: "provider unavailable"},
		}
	}
	defer func() { discoverModelCatalogs = oldDiscover }()

	cmd := NewRootCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	args := []string{"models", "-C", isolatedWorkdir(t)}
	if asJSON {
		args = append(args, "--json")
	}
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestModelsCommandRendersSourcesDefaultsAndWarnings(t *testing.T) {
	out, err := runModelsCommand(t, false)
	if err != nil {
		t.Fatalf("models: %v\n%s", err, out)
	}
	for _, want := range []string{"codex\tsource=maintained default=gpt-6-astra", "* gpt-6-astra", "grok-4.5", "warning: provider unavailable"} {
		if !strings.Contains(out, want) {
			t.Fatalf("models output is missing %q:\n%s", want, out)
		}
	}
}

func TestModelsCommandJSON(t *testing.T) {
	out, err := runModelsCommand(t, true)
	if err != nil {
		t.Fatalf("models --json: %v\n%s", err, out)
	}
	var catalog []tagteam.ModelCatalogEntry
	if err := json.Unmarshal([]byte(out), &catalog); err != nil {
		t.Fatalf("models --json emitted invalid JSON: %v\n%s", err, out)
	}
	if len(catalog) != 3 || catalog[1].Source != "cli" || catalog[2].Error == "" {
		t.Fatalf("catalog = %#v", catalog)
	}
}
