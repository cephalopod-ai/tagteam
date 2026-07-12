package tagteam

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestResolveOptions_EnvCanSetScoutFailurePolicy(t *testing.T) {
	cfg := DefaultConfig()
	mergeEnvConfig(&cfg, map[string]string{"TAGTEAM_SCOUT_FAILURE_POLICY": "fail"})
	opts, err := ResolveOptions(cfg, nil, FlagInputs{
		Mode:    "relay",
		Timeout: 15 * time.Minute,
	}, map[string]bool{"mode": true}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if opts.ScoutFailurePolicy != "fail" {
		t.Fatalf("scout failure policy = %q", opts.ScoutFailurePolicy)
	}
}

func TestResolveOptions_StrictScoutMapsToFail(t *testing.T) {
	cfg := DefaultConfig()
	opts, err := ResolveOptions(cfg, nil, FlagInputs{
		Mode:        "relay",
		StrictScout: true,
		Timeout:     15 * time.Minute,
	}, map[string]bool{"mode": true, "strict-scout": true}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if opts.ScoutFailurePolicy != "fail" {
		t.Fatalf("scout failure policy = %q", opts.ScoutFailurePolicy)
	}
}

func TestResolveOptions_InvalidScoutFailurePolicyRejected(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Defaults.ScoutFailurePolicy = "maybe"
	_, err := ResolveOptions(cfg, nil, FlagInputs{
		Mode:    "relay",
		Timeout: 15 * time.Minute,
	}, map[string]bool{"mode": true}, "ship it")
	if err == nil {
		t.Fatal("expected invalid scout failure policy error")
	}
	if !strings.Contains(err.Error(), "invalid scout_failure_policy") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveOptions_StrictScoutRejectedOutsideRelay(t *testing.T) {
	cfg := DefaultConfig()
	_, err := ResolveOptions(cfg, nil, FlagInputs{
		Mode:        "supervisor",
		StrictScout: true,
		Timeout:     15 * time.Minute,
	}, map[string]bool{"mode": true, "strict-scout": true}, "ship it")
	if err == nil {
		t.Fatal("expected strict-scout outside relay to fail")
	}
	if !strings.Contains(err.Error(), "--strict-scout is only valid in relay mode") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveOptions_NoScoutRetrievalRejectedOutsideRelay(t *testing.T) {
	cfg := DefaultConfig()
	_, err := ResolveOptions(cfg, nil, FlagInputs{
		Mode:             "supervisor",
		NoScoutRetrieval: true,
		Timeout:          15 * time.Minute,
	}, map[string]bool{"mode": true, "no-scout-retrieval": true}, "ship it")
	if err == nil {
		t.Fatal("expected no-scout-retrieval outside relay to fail")
	}
	if !strings.Contains(err.Error(), "--no-scout-retrieval is only valid in relay mode") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveOptions_RelayLegacyFlagsOverrideCoderAndSupervisor(t *testing.T) {
	cfg := DefaultConfig()
	opts, err := ResolveOptions(cfg, nil, FlagInputs{
		Mode:      "relay",
		Coder:     "agy:worker-model",
		Adversary: "claude:opus",
		Timeout:   15 * time.Minute,
	}, map[string]bool{"mode": true, "mc": true, "ma": true}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if opts.Coder.Adapter != "agy" || opts.Coder.Model != "worker-model" {
		t.Fatalf("-mc should override relay coder: %#v", opts.Coder)
	}
	if opts.Adversary.Adapter != "claude" || opts.Adversary.Model != "opus" {
		t.Fatalf("-ma should override relay supervisor: %#v", opts.Adversary)
	}
}

func TestResolveOptions_RelayRoleFlagsOverrideRoles(t *testing.T) {
	cfg := DefaultConfig()
	opts, err := ResolveOptions(cfg, nil, FlagInputs{
		Mode:       "relay",
		Scout:      "agy:scout-model",
		CoderRole:  "codex:coder-model",
		Supervisor: "claude:supervisor-model",
		Timeout:    15 * time.Minute,
	}, map[string]bool{"mode": true, "scout": true, "coder": true, "supervisor": true}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if opts.Scout.Adapter != "agy" || opts.Scout.Model != "scout-model" {
		t.Fatalf("--scout target = %#v", opts.Scout)
	}
	if opts.Coder.Adapter != "codex" || opts.Coder.Model != "coder-model" {
		t.Fatalf("--coder target = %#v", opts.Coder)
	}
	if opts.Adversary.Adapter != "claude" || opts.Adversary.Model != "supervisor-model" {
		t.Fatalf("--supervisor target = %#v", opts.Adversary)
	}
}

func TestResolveOptions_RelayScoutModeFlags(t *testing.T) {
	cfg := DefaultConfig()
	opts, err := ResolveOptions(cfg, nil, FlagInputs{
		Mode:          "relay",
		ScoutMode:     "tests",
		PostScoutMode: "risk",
		Timeout:       15 * time.Minute,
	}, map[string]bool{"mode": true, "scout-mode": true, "post-scout-mode": true}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if opts.ScoutMode != "tests" || opts.PostScoutMode != "risk" {
		t.Fatalf("scout modes = %q/%q", opts.ScoutMode, opts.PostScoutMode)
	}
}

func TestResolveOptions_InvalidRelayScoutModeRejected(t *testing.T) {
	cfg := DefaultConfig()
	_, err := ResolveOptions(cfg, nil, FlagInputs{
		Mode:      "relay",
		ScoutMode: "chaos",
		Timeout:   15 * time.Minute,
	}, map[string]bool{"mode": true, "scout-mode": true}, "ship it")
	if err == nil {
		t.Fatal("expected invalid scout mode error")
	}
	if !strings.Contains(err.Error(), "invalid scout-mode") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveOptions_ReviewerFlagRejectedInSupervisorMode(t *testing.T) {
	cfg := DefaultConfig()
	_, err := ResolveOptions(cfg, nil, FlagInputs{
		Reviewer: "claude:opus",
		Timeout:  15 * time.Minute,
	}, map[string]bool{"reviewer": true}, "ship it")
	if err == nil {
		t.Fatal("expected error using --reviewer in supervisor mode")
	}
	if !strings.Contains(err.Error(), "--reviewer is only valid in adversarial mode") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveOptions_ProfileWorkerSupervisor(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Profiles["custom"] = ProfileConfig{
		Worker:     "gosling:flash",
		Supervisor: "claude:haiku",
		Rounds:     5,
	}
	opts, err := ResolveOptions(cfg, nil, FlagInputs{
		Profile: "custom",
		Timeout: 15 * time.Minute,
	}, map[string]bool{}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if opts.Mode != ModeSupervisor {
		t.Fatalf("mode = %q", opts.Mode)
	}
	if opts.Coder.Adapter != "gosling" || opts.Coder.Model != "flash" {
		t.Fatalf("worker target = %#v", opts.Coder)
	}
	if opts.Adversary.Adapter != "claude" || opts.Adversary.Model != "haiku" {
		t.Fatalf("supervisor target = %#v", opts.Adversary)
	}
	if opts.Rounds != 5 {
		t.Fatalf("rounds = %d", opts.Rounds)
	}
}

func TestResolveOptions_ProfileWorkerSupervisorForcesSupervisorMode(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Defaults.Mode = string(ModeAdversarial)
	cfg.Profiles["supervised"] = ProfileConfig{
		Worker:     "agy:Gemini 3.5 Flash (High)",
		Supervisor: "claude:opus",
	}
	opts, err := ResolveOptions(cfg, nil, FlagInputs{
		Profile: "supervised",
		Timeout: 15 * time.Minute,
	}, map[string]bool{}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if opts.Mode != ModeSupervisor {
		t.Fatalf("mode = %q, want supervisor", opts.Mode)
	}
	if opts.Coder.Adapter != "agy" {
		t.Fatalf("worker target = %#v", opts.Coder)
	}
	if opts.Adversary.Adapter != "claude" || opts.Adversary.Model != "opus" {
		t.Fatalf("supervisor target = %#v", opts.Adversary)
	}
	if !opts.ModeExplicit || !opts.CoderExplicit || !opts.AdversaryExplicit {
		t.Fatalf("expected profile worker/supervisor to mark mode/targets explicit: mode=%t coder=%t adversary=%t", opts.ModeExplicit, opts.CoderExplicit, opts.AdversaryExplicit)
	}
}

func TestResolveOptions_ProfileSetsMode(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Profiles["legacy"] = ProfileConfig{
		Mode:      "adversarial",
		Coder:     "codex:gpt-5",
		Adversary: "codex:gpt-5.6-sol",
	}
	opts, err := ResolveOptions(cfg, nil, FlagInputs{
		Profile: "legacy",
		Timeout: 15 * time.Minute,
	}, map[string]bool{}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if opts.Mode != ModeAdversarial {
		t.Fatalf("mode = %q", opts.Mode)
	}
	if opts.Coder.Adapter != "codex" || opts.Coder.Model != "gpt-5" {
		t.Fatalf("coder target = %#v", opts.Coder)
	}
}

func TestResolveOptions_ProfileWithLegacyKeysOnlyResolvesAdversarial(t *testing.T) {
	cfg := DefaultConfig()
	// A profile written before `mode` existed: it only sets the legacy
	// coder/adversary keys. It must still resolve as an adversarial-mode
	// profile instead of being silently ignored under the supervisor
	// default.
	cfg.Profiles["oldschool"] = ProfileConfig{
		Coder:     "codex:gpt-5",
		Adversary: "codex:gpt-5.6-sol",
		Rounds:    3,
	}
	opts, err := ResolveOptions(cfg, nil, FlagInputs{
		Profile: "oldschool",
		Timeout: 15 * time.Minute,
	}, map[string]bool{}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if opts.Mode != ModeAdversarial {
		t.Fatalf("mode = %q, want adversarial", opts.Mode)
	}
	if opts.Coder.Adapter != "codex" || opts.Coder.Model != "gpt-5" {
		t.Fatalf("coder target = %#v", opts.Coder)
	}
	if opts.Adversary.Adapter != "codex" || opts.Adversary.Model != "gpt-5.6-sol" {
		t.Fatalf("adversary target = %#v", opts.Adversary)
	}
	if opts.Rounds != 3 {
		t.Fatalf("rounds = %d", opts.Rounds)
	}
	if !opts.ModeExplicit || !opts.CoderExplicit || !opts.AdversaryExplicit {
		t.Fatalf("expected profile selection to mark mode/targets explicit: mode=%t coder=%t adversary=%t", opts.ModeExplicit, opts.CoderExplicit, opts.AdversaryExplicit)
	}
}

func TestResolveOptions_SupervisorCanEdit(t *testing.T) {
	cfg := DefaultConfig()
	opts, err := ResolveOptions(cfg, nil, FlagInputs{
		SupervisorCanEdit: true,
		Supervisor:        "codex:gpt-5.6-sol",
		Timeout:           15 * time.Minute,
	}, map[string]bool{"supervisor": true}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if !opts.SupervisorCanEdit {
		t.Fatal("expected SupervisorCanEdit = true")
	}
}

func TestResolveOptions_SupervisorSlicingFlags(t *testing.T) {
	cfg := DefaultConfig()
	opts, err := ResolveOptions(cfg, nil, FlagInputs{
		Slice:           true,
		MaxPackages:     3,
		Package:         "P2",
		AutoNextPackage: true,
		Timeout:         15 * time.Minute,
	}, map[string]bool{"slice": true, "max-packages": true, "package": true, "auto-next-package": true}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if !opts.SupervisorSlicing || !opts.SupervisorSlicingExplicit {
		t.Fatalf("slicing = %t explicit=%t", opts.SupervisorSlicing, opts.SupervisorSlicingExplicit)
	}
	if opts.MaxPackages != 3 {
		t.Fatalf("max packages = %d", opts.MaxPackages)
	}
	if opts.Package != "P2" {
		t.Fatalf("package = %q", opts.Package)
	}
	if !opts.AutoNextPackage {
		t.Fatal("expected auto-next-package")
	}

	opts, err = ResolveOptions(cfg, nil, FlagInputs{
		NoSlice: true,
		Timeout: 15 * time.Minute,
	}, map[string]bool{"no-slice": true}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if opts.SupervisorSlicing {
		t.Fatal("expected --no-slice to disable supervisor slicing")
	}
}

func TestResolveOptions_ProfileWithOnlyRoundsDoesNotMarkModeOrTargetsExplicit(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Profiles["quick"] = ProfileConfig{Rounds: 5}
	opts, err := ResolveOptions(cfg, nil, FlagInputs{
		Profile: "quick",
		Timeout: 15 * time.Minute,
	}, map[string]bool{}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if opts.Rounds != 5 {
		t.Fatalf("rounds = %d", opts.Rounds)
	}
	// The profile only overrides rounds; it must not pin mode/targets as
	// explicit, or Fix() would refuse to resume a saved run's mode/adapters.
	if opts.ModeExplicit || opts.CoderExplicit || opts.AdversaryExplicit {
		t.Fatalf("expected a rounds-only profile to leave mode/targets non-explicit: mode=%t coder=%t adversary=%t", opts.ModeExplicit, opts.CoderExplicit, opts.AdversaryExplicit)
	}
}

func TestMergeEnvConfig_ModeWorkerSupervisor(t *testing.T) {
	t.Setenv("TAGTEAM_MODE", "adversarial")
	t.Setenv("TAGTEAM_WORKER", "gosling:flash")
	t.Setenv("TAGTEAM_SUPERVISOR", "claude:opus")

	cfg := DefaultConfig()
	mergeEnvConfig(&cfg, nil)

	if cfg.Defaults.Mode != "adversarial" {
		t.Fatalf("mode = %q", cfg.Defaults.Mode)
	}
	if cfg.Defaults.Worker != "gosling:flash" {
		t.Fatalf("worker = %q", cfg.Defaults.Worker)
	}
	if cfg.Defaults.Supervisor != "claude:opus" {
		t.Fatalf("supervisor = %q", cfg.Defaults.Supervisor)
	}
}

func TestMergeEnvConfig_RelayCoderAndEffort(t *testing.T) {
	t.Setenv("TAGTEAM_RELAY_CODER", "claude:custom-relay")
	t.Setenv("TAGTEAM_CODEX_REASONING_EFFORT", "xhigh")
	t.Setenv("TAGTEAM_CLAUDE_EFFORT", "medium")

	cfg := DefaultConfig()
	mergeEnvConfig(&cfg, nil)
	if cfg.Defaults.RelayCoder != "claude:custom-relay" {
		t.Fatalf("relay coder = %q", cfg.Defaults.RelayCoder)
	}
	if cfg.Adapters.Codex.ReasoningEffort != "xhigh" || cfg.Adapters.Claude.Effort != "medium" {
		t.Fatalf("efforts = codex:%q claude:%q", cfg.Adapters.Codex.ReasoningEffort, cfg.Adapters.Claude.Effort)
	}
	if err := validateConfig(cfg); err != nil {
		t.Fatalf("validateConfig() error = %v", err)
	}
}

func TestMergeEnvConfig_LegacyRelayCoder(t *testing.T) {
	t.Setenv("TAGTEAM_MODE", "relay")
	t.Setenv("TAGTEAM_CODER", "codex:legacy-relay-model")

	cfg := DefaultConfig()
	mergeEnvConfig(&cfg, nil)
	if cfg.Defaults.RelayCoder != "codex:legacy-relay-model" {
		t.Fatalf("relay coder = %q", cfg.Defaults.RelayCoder)
	}
}

func TestValidateConfig_RejectsInvalidEffort(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Adapters.Claude.Effort = "ultra"
	if err := validateConfig(cfg); err == nil || !strings.Contains(err.Error(), "adapters.claude.effort") {
		t.Fatalf("validateConfig() error = %v", err)
	}
}

func TestMergeEnvConfig_LegacyCoderAdversaryImpliesAdversarialMode(t *testing.T) {
	t.Setenv("TAGTEAM_CODER", "codex:gpt-5")
	t.Setenv("TAGTEAM_ADVERSARY", "codex:gpt-5.6-sol")

	cfg := DefaultConfig()
	mergeEnvConfig(&cfg, nil)

	if cfg.Defaults.Mode != string(ModeAdversarial) {
		t.Fatalf("mode = %q, want adversarial", cfg.Defaults.Mode)
	}
	if cfg.Defaults.Coder != "codex:gpt-5" || cfg.Defaults.Adversary != "codex:gpt-5.6-sol" {
		t.Fatalf("coder/adversary = %q/%q", cfg.Defaults.Coder, cfg.Defaults.Adversary)
	}
}

func TestMergeEnvConfig_ExplicitModeWinsOverLegacyCoderAdversary(t *testing.T) {
	t.Setenv("TAGTEAM_MODE", "supervisor")
	t.Setenv("TAGTEAM_CODER", "codex:gpt-5")

	cfg := DefaultConfig()
	mergeEnvConfig(&cfg, nil)

	if cfg.Defaults.Mode != string(ModeSupervisor) {
		t.Fatalf("mode = %q, want supervisor", cfg.Defaults.Mode)
	}
	if cfg.Defaults.Coder != "codex:gpt-5" {
		t.Fatalf("coder = %q", cfg.Defaults.Coder)
	}
}

func TestResolveOptions_LegacyEnvRoleSelectionStillWorksUnderSupervisorDefault(t *testing.T) {
	t.Setenv("TAGTEAM_CODER", "codex:gpt-5")
	t.Setenv("TAGTEAM_ADVERSARY", "claude:haiku")

	cfg := DefaultConfig()
	mergeEnvConfig(&cfg, nil)

	opts, err := ResolveOptions(cfg, nil, FlagInputs{Timeout: 15 * time.Minute}, map[string]bool{}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if opts.Mode != ModeAdversarial {
		t.Fatalf("mode = %q, want adversarial", opts.Mode)
	}
	if opts.Coder.Adapter != "codex" || opts.Coder.Model != "gpt-5" {
		t.Fatalf("coder target = %#v", opts.Coder)
	}
	if opts.Adversary.Adapter != "claude" || opts.Adversary.Model != "haiku" {
		t.Fatalf("adversary target = %#v", opts.Adversary)
	}
}

func TestHasTagteamEnv_RecognizesNewModeVars(t *testing.T) {
	if hasTagteamEnv(map[string]string{}) {
		t.Fatal("expected empty overlay to have no TAGTEAM_* env vars")
	}
	t.Setenv("TAGTEAM_MODE", "adversarial")
	if !hasTagteamEnv(nil) {
		t.Fatal("expected TAGTEAM_MODE to be recognized")
	}
	if !hasTagteamEnv(map[string]string{"TAGTEAM_MODE": "relay"}) {
		t.Fatal("expected overlay TAGTEAM_MODE to be recognized")
	}
}

func TestLoadConfig_RepoOverridesUser(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	t.Setenv("XDG_CONFIG_HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "tagteam"), 0o755); err != nil {
		t.Fatal(err)
	}
	userConfig := []byte("[defaults]\ncoder = \"codex:gpt-5\"\n")
	if err := os.WriteFile(filepath.Join(home, "tagteam", "config.toml"), userConfig, 0o644); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	repoConfig := []byte("[defaults]\ncoder = \"claude:opus\"\n")
	if err := os.WriteFile(filepath.Join(repo, ".tagteam.toml"), repoConfig, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := LoadConfig(repo)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.Defaults.Coder != "claude:opus" {
		t.Fatalf("coder = %q", cfg.Defaults.Coder)
	}
}

func TestLoadConfig_LegacyDefaultCoderAdversaryImpliesAdversarialMode(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	t.Setenv("XDG_CONFIG_HOME", home)
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	repoConfig := []byte("[defaults]\ncoder = \"codex:gpt-5\"\nadversary = \"claude:sonnet\"\n")
	if err := os.WriteFile(filepath.Join(repo, ".tagteam.toml"), repoConfig, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := LoadConfig(repo)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	opts, err := ResolveOptions(cfg, nil, FlagInputs{Timeout: 15 * time.Minute}, map[string]bool{}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if opts.Mode != ModeAdversarial {
		t.Fatalf("mode = %q, want adversarial", opts.Mode)
	}
	if opts.Coder.Adapter != "codex" || opts.Coder.Model != "gpt-5" {
		t.Fatalf("coder target = %#v", opts.Coder)
	}
	if opts.Adversary.Adapter != "claude" || opts.Adversary.Model != "sonnet" {
		t.Fatalf("adversary target = %#v", opts.Adversary)
	}
}

func TestLoadConfig_ExplicitSupervisorModeWinsOverLegacyDefaultRoles(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	t.Setenv("XDG_CONFIG_HOME", home)
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	repoConfig := []byte("[defaults]\nmode = \"supervisor\"\ncoder = \"codex:gpt-5\"\nadversary = \"claude:sonnet\"\nworker = \"agy:Gemini 3.5 Flash (High)\"\nsupervisor = \"claude:opus\"\n")
	if err := os.WriteFile(filepath.Join(repo, ".tagteam.toml"), repoConfig, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := LoadConfig(repo)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	opts, err := ResolveOptions(cfg, nil, FlagInputs{Timeout: 15 * time.Minute}, map[string]bool{}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if opts.Mode != ModeSupervisor {
		t.Fatalf("mode = %q, want supervisor", opts.Mode)
	}
	if opts.Coder.Adapter != "agy" {
		t.Fatalf("worker target = %#v", opts.Coder)
	}
	if opts.Adversary.Adapter != "claude" || opts.Adversary.Model != "opus" {
		t.Fatalf("supervisor target = %#v", opts.Adversary)
	}
}

func TestResolveOptions_RejectsInvalidGitSafety(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Defaults.GitSafety = "bad-mode"
	_, err := ResolveOptions(cfg, []string{"defaults"}, FlagInputs{Timeout: 15 * time.Minute}, map[string]bool{}, "ship it")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestResolveOptions_AgyPassthrough(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Adapters.Agy.ExtraArgs = []string{"--project", "repo"}
	opts, err := ResolveOptions(cfg, []string{"defaults"}, FlagInputs{
		AgyArgsRaw: "--new-project",
		Timeout:    15 * time.Minute,
	}, map[string]bool{}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	want := []string{"--project", "repo", "--new-project"}
	if len(opts.AgyArgs) != len(want) {
		t.Fatalf("agy args length = %d, want %d: %#v", len(opts.AgyArgs), len(want), opts.AgyArgs)
	}
	for i := range want {
		if opts.AgyArgs[i] != want[i] {
			t.Fatalf("agy args[%d] = %q, want %q", i, opts.AgyArgs[i], want[i])
		}
	}
}

func TestResolveOptions_GoslingPassthrough(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Adapters.Gosling.ExtraArgs = []string{"--provider", "google"}
	opts, err := ResolveOptions(cfg, []string{"defaults"}, FlagInputs{
		GoslingArgsRaw: "--model gemini-2.5-flash",
		Timeout:        15 * time.Minute,
	}, map[string]bool{}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	want := []string{"--provider", "google", "--model", "gemini-2.5-flash"}
	if len(opts.GoslingArgs) != len(want) {
		t.Fatalf("gosling args length = %d, want %d: %#v", len(opts.GoslingArgs), len(want), opts.GoslingArgs)
	}
	for i := range want {
		if opts.GoslingArgs[i] != want[i] {
			t.Fatalf("gosling args[%d] = %q, want %q", i, opts.GoslingArgs[i], want[i])
		}
	}
}

func TestDefaultConfig_Grok(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Adapters.Grok.DefaultModel != "grok-4.5" || cfg.Adapters.Grok.ReasoningEffort != "high" {
		t.Fatalf("grok defaults = %#v", cfg.Adapters.Grok)
	}
}

func TestMergeEnvConfig_Grok(t *testing.T) {
	t.Setenv("TAGTEAM_GROK_MODEL", "grok-test")
	t.Setenv("TAGTEAM_GROK_REASONING_EFFORT", "low")
	t.Setenv("TAGTEAM_GROK_ARGS", `--output-format json --no-memory`)
	cfg := DefaultConfig()
	mergeEnvConfig(&cfg, nil)
	if cfg.Adapters.Grok.DefaultModel != "grok-test" || cfg.Adapters.Grok.ReasoningEffort != "low" {
		t.Fatalf("grok env config = %#v", cfg.Adapters.Grok)
	}
	want := []string{"--output-format", "json", "--no-memory"}
	if !reflect.DeepEqual(cfg.Adapters.Grok.ExtraArgs, want) {
		t.Fatalf("grok args = %#v, want %#v", cfg.Adapters.Grok.ExtraArgs, want)
	}
}

func TestResolveOptions_GrokPassthrough(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Adapters.Grok.ExtraArgs = []string{"--no-memory"}
	opts, err := ResolveOptions(cfg, []string{"defaults"}, FlagInputs{
		GrokArgsRaw: "--output-format json",
		Timeout:     15 * time.Minute,
	}, map[string]bool{}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	want := []string{"--no-memory", "--output-format", "json"}
	if !reflect.DeepEqual(opts.GrokArgs, want) {
		t.Fatalf("grok args = %#v, want %#v", opts.GrokArgs, want)
	}
}

func TestMergeConfig_GrokPrecedence(t *testing.T) {
	cfg := DefaultConfig()
	mergeConfig(&cfg, Config{Adapters: AdapterConfigSet{Grok: GrokConfig{
		DefaultModel:    "grok-config",
		ReasoningEffort: "medium",
		ExtraArgs:       []string{"--no-memory"},
	}}})
	if cfg.Adapters.Grok.DefaultModel != "grok-config" || cfg.Adapters.Grok.ReasoningEffort != "medium" || !reflect.DeepEqual(cfg.Adapters.Grok.ExtraArgs, []string{"--no-memory"}) {
		t.Fatalf("merged grok config = %#v", cfg.Adapters.Grok)
	}
}
