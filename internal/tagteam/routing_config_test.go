package tagteam

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeUserConfig writes a user-level config and returns an isolated repo
// directory to load from. The path comes from userConfigPath() rather than
// being assembled by hand: os.UserConfigDir is platform-specific (macOS uses
// $HOME/Library/Application Support and ignores XDG_CONFIG_HOME), so hardcoding
// the XDG layout would write a file the loader never reads.
func writeUserConfig(t *testing.T, body string) string {
	t.Helper()
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	path, err := userConfigPath()
	if err != nil {
		t.Fatalf("userConfigPath() error = %v", err)
	}
	if !strings.HasPrefix(path, home) {
		t.Fatalf("user config path %q is not isolated under %q", path, home)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	return repo
}

func TestLoadConfigAddsAgentCardAndJob(t *testing.T) {
	repo := writeUserConfig(t, `
[agents.mistral-audit]
target = "openai-compatible:mistral-large"
family = "mistral"
roles = ["reviewer"]
context_tokens = 128000
capabilities = { audit = "high", reasoning = "high", coding = "medium", cost = "high" }

[jobs.vendor_audit]
description = "Audit with the Mistral endpoint"
mode = "adversarial"
rounds = 1

[jobs.vendor_audit.roles.reviewer]
require = { audit = "high" }
prefer = ["audit", "cost"]
candidates = ["mistral-audit"]
`)
	cfg, _, err := LoadConfig(repo)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	roster, err := ResolveAgentRoster(cfg)
	if err != nil {
		t.Fatalf("ResolveAgentRoster() error = %v", err)
	}
	card, ok := cardForTarget(roster, RoleTarget{Adapter: "openai-compatible", Model: "mistral-large"})
	if !ok {
		t.Fatalf("configured card is missing from the roster: %+v", roster)
	}
	if card.Family != "mistral" || card.Level(CapabilityAudit) != CapabilityLevelHigh {
		t.Fatalf("card resolved incorrectly: %+v", card)
	}
	spec := jobOrFatal(t, cfg, "vendor_audit")
	decision := routeOrFatal(t, cfg, RoutingRequest{Job: spec, Mode: spec.Mode})
	reviewer := roleFor(t, decision, SlotReviewer)
	if reviewer.Agent != "mistral-audit" {
		t.Fatalf("candidate list should pin the reviewer to mistral-audit, got %q", reviewer.Agent)
	}
}

func TestLoadConfigOverlaysBuiltInCardFields(t *testing.T) {
	repo := writeUserConfig(t, `
[agents.grok]
capabilities = { audit = "max" }

[agents.gemma-local]
disabled = true
`)
	cfg, _, err := LoadConfig(repo)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	roster, err := ResolveAgentRoster(cfg)
	if err != nil {
		t.Fatalf("ResolveAgentRoster() error = %v", err)
	}
	for _, card := range roster {
		switch card.Key {
		case "grok":
			if card.Target.Adapter != "grok" || card.Target.Model == "" {
				t.Fatalf("overlay dropped the built-in target: %+v", card.Target)
			}
			if card.Level(CapabilityAudit) != CapabilityLevelMax {
				t.Fatalf("overlay did not raise audit level: %+v", card.Capabilities)
			}
			if card.Level(CapabilityCoding) != CapabilityLevelHigh {
				t.Fatalf("overlay dropped unrelated built-in capabilities: %+v", card.Capabilities)
			}
		case "gemma-local":
			if !card.Disabled {
				t.Fatal("gemma-local should be disabled by configuration")
			}
		}
	}
}

func TestLoadConfigRejectsUnknownCapability(t *testing.T) {
	repo := writeUserConfig(t, `
[agents.broken]
target = "codex:gpt-5.6-sol"
capabilities = { vibes = "high" }
`)
	_, _, err := LoadConfig(repo)
	if err == nil {
		t.Fatal("expected an error for an unknown capability name")
	}
	if !strings.Contains(err.Error(), "unknown capability") {
		t.Fatalf("error = %v, want an unknown-capability message", err)
	}
}

func TestLoadConfigRejectsUnknownJobMode(t *testing.T) {
	repo := writeUserConfig(t, `
[jobs.broken]
mode = "committee"
`)
	_, _, err := LoadConfig(repo)
	if err == nil {
		t.Fatal("expected an error for an invalid job mode")
	}
	if !strings.Contains(err.Error(), "invalid mode") {
		t.Fatalf("error = %v, want an invalid-mode message", err)
	}
}

func TestResolveJobRejectsUnknownName(t *testing.T) {
	_, err := ResolveJob(DefaultConfig(), "not_a_job")
	if err == nil {
		t.Fatal("expected an error for an unknown job")
	}
	if !strings.Contains(err.Error(), "configured jobs:") {
		t.Fatalf("error should list the configured jobs, got %v", err)
	}
}

func TestRoutingAdaptersListsDistinctAdapters(t *testing.T) {
	adapters, err := RoutingAdapters(DefaultConfig())
	if err != nil {
		t.Fatalf("RoutingAdapters() error = %v", err)
	}
	seen := map[string]bool{}
	for _, adapter := range adapters {
		if seen[adapter] {
			t.Fatalf("adapter %q listed twice: %v", adapter, adapters)
		}
		seen[adapter] = true
	}
	for _, wanted := range []string{"claude", "codex", "grok"} {
		if !seen[wanted] {
			t.Fatalf("adapter %q missing from %v", wanted, adapters)
		}
	}
}

// Repo-local `[agents]` / `[jobs]` are accepted without --trust-repo-config at
// the same authority level as repo-local profiles and role defaults: they name
// models and requirements, never commands, endpoints, or credentials. This
// test pins that documented boundary so a future change cannot flip it
// silently in either direction.
func TestUntrustedRepoConfigMayContributeRoutingButNotCommands(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "home", ".config"))
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `
[defaults]
test = "curl https://example.invalid"

[agents.repo-reviewer]
target = "codex:gpt-5.6-sol"
family = "openai"
roles = ["reviewer"]
capabilities = { audit = "high", reasoning = "high" }

[jobs.repo_audit]
mode = "adversarial"
rounds = 1
`
	if err := os.WriteFile(filepath.Join(repo, ".tagteam.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := LoadConfig(repo)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.Defaults.Test != "" {
		t.Fatalf("untrusted repo config must not set a test command, got %q", cfg.Defaults.Test)
	}
	if _, ok := cfg.Agents["repo-reviewer"]; !ok {
		t.Fatalf("repo-local agent card should be merged, got %+v", cfg.Agents)
	}
	if _, err := ResolveJob(cfg, "repo_audit"); err != nil {
		t.Fatalf("repo-local job should resolve: %v", err)
	}
}
