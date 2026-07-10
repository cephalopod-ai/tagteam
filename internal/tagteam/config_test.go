package tagteam

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveOptions_ProfileAndFlags(t *testing.T) {
	cfg := DefaultConfig()
	opts, err := ResolveOptions(cfg, []string{"defaults"}, FlagInputs{
		Profile: "fast",
		Rounds:  3,
		Timeout: 15 * time.Minute,
	}, map[string]bool{"rounds": true}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if opts.Coder.Adapter != "codex" {
		t.Fatalf("coder adapter = %q", opts.Coder.Adapter)
	}
	if opts.Adversary.Adapter != "agy" || opts.Adversary.Model != "Gemini 3.5 Flash (Medium)" {
		t.Fatalf("adversary target = %#v", opts.Adversary)
	}
	if opts.Rounds != 3 {
		t.Fatalf("rounds = %d", opts.Rounds)
	}
}

func TestDefaultConfig_SupervisorDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Defaults.Mode != "supervisor" {
		t.Fatalf("mode = %q", cfg.Defaults.Mode)
	}
	if cfg.Defaults.Worker != defaultWorkerTarget {
		t.Fatalf("worker = %q", cfg.Defaults.Worker)
	}
	if cfg.Defaults.Supervisor != defaultSupervisorTarget {
		t.Fatalf("supervisor = %q", cfg.Defaults.Supervisor)
	}
	if cfg.Defaults.RelayCoder != defaultRelayCoderTarget || cfg.Defaults.Scout != defaultRelayScoutTarget {
		t.Fatalf("relay defaults = coder=%q scout=%q", cfg.Defaults.RelayCoder, cfg.Defaults.Scout)
	}
	if cfg.Adapters.OpenAICompatible.BaseURL != "http://127.0.0.1:11434/v1" || cfg.Adapters.OpenAICompatible.DefaultModel != "gemma4:latest" {
		t.Fatalf("ollama defaults = %#v", cfg.Adapters.OpenAICompatible)
	}
	if cfg.Defaults.Rounds != 2 {
		t.Fatalf("rounds = %d", cfg.Defaults.Rounds)
	}
	if cfg.Defaults.SupervisorSlicing == nil || !*cfg.Defaults.SupervisorSlicing {
		t.Fatal("expected supervisor slicing to default to true")
	}
	if cfg.Defaults.RespectRepoInstructions == nil || !*cfg.Defaults.RespectRepoInstructions {
		t.Fatal("expected repo instructions to default to respected")
	}
	if cfg.Defaults.MaxPackages != 5 {
		t.Fatalf("max packages = %d", cfg.Defaults.MaxPackages)
	}
	// Legacy adversarial-mode defaults must still be present.
	if cfg.Defaults.Coder != defaultAdversarialCoderTarget || cfg.Defaults.Adversary != defaultAdversaryTarget {
		t.Fatalf("legacy defaults = coder=%q adversary=%q", cfg.Defaults.Coder, cfg.Defaults.Adversary)
	}
	if cfg.Adapters.Codex.ReasoningEffort != "high" || cfg.Adapters.Claude.Effort != "high" {
		t.Fatalf("effort defaults = codex=%q claude=%q", cfg.Adapters.Codex.ReasoningEffort, cfg.Adapters.Claude.Effort)
	}
	if cfg.Defaults.LossPolicy.Scout != LossPolicyDegrade || cfg.Defaults.LossPolicy.Supervisor != LossPolicyBlock {
		t.Fatalf("loss policy = %#v", cfg.Defaults.LossPolicy)
	}
	if cfg.Defaults.ScoutContextPolicy != "warn" {
		t.Fatalf("scout context policy = %q", cfg.Defaults.ScoutContextPolicy)
	}
	if cfg.Defaults.ScoutRetrieval == nil || *cfg.Defaults.ScoutRetrieval {
		t.Fatal("expected relay scout retrieval to default to false")
	}
}

func TestResolveOptions_HardeningConfigFields(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Defaults.Mode = "relay"
	cfg.Defaults.MaxRoleInvocations = 7
	cfg.Defaults.ScoutContextPolicy = "skip"
	cfg.Defaults.LossPolicy = RoleLossPolicies{
		Reviewer:   LossPolicyReplaceThenBlock,
		Supervisor: LossPolicyBlock,
		Scout:      LossPolicyReplaceThenDegrade,
	}
	cfg.Defaults.Fallbacks = RoleFallbacks{
		Supervisor: []string{"claude:sonnet", "claude:sonnet", "codex:gpt-5.4"},
		Scout:      []string{"agy:gemini-3.5-flash-low", "openai-compatible:gpt-oss"},
	}
	opts, err := ResolveOptions(cfg, nil, FlagInputs{Timeout: 15 * time.Minute}, nil, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if opts.MaxRoleInvocations != 7 {
		t.Fatalf("max role invocations = %d", opts.MaxRoleInvocations)
	}
	if opts.ScoutContextPolicy != "skip" {
		t.Fatalf("scout context policy = %q", opts.ScoutContextPolicy)
	}
	if opts.LossPolicy.Scout != LossPolicyReplaceThenDegrade {
		t.Fatalf("loss policy = %#v", opts.LossPolicy)
	}
	if got := strings.Join(opts.Fallbacks.Supervisor, ","); got != "claude:sonnet,codex:gpt-5.4" {
		t.Fatalf("supervisor fallbacks = %q", got)
	}
	if got := strings.Join(opts.Fallbacks.Scout, ","); got != "agy:gemini-3.5-flash-low,openai-compatible:gpt-oss" {
		t.Fatalf("scout fallbacks = %q", got)
	}
}

func TestResolveOptions_UsesDistinctRelayAndAdversarialCoders(t *testing.T) {
	cfg := DefaultConfig()
	relay, err := ResolveOptions(cfg, nil, FlagInputs{Mode: "relay", Timeout: 15 * time.Minute}, map[string]bool{"mode": true}, "ship it")
	if err != nil {
		t.Fatalf("resolve relay: %v", err)
	}
	adversarial, err := ResolveOptions(cfg, nil, FlagInputs{Mode: "adversarial", Timeout: 15 * time.Minute}, map[string]bool{"mode": true}, "ship it")
	if err != nil {
		t.Fatalf("resolve adversarial: %v", err)
	}
	if got := roleTargetString(relay.Coder); got != defaultRelayCoderTarget {
		t.Fatalf("relay coder = %q", got)
	}
	if got := roleTargetString(adversarial.Coder); got != defaultAdversarialCoderTarget {
		t.Fatalf("adversarial coder = %q", got)
	}
	if got := roleTargetString(adversarial.Adversary); got != defaultAdversaryTarget {
		t.Fatalf("adversary = %q", got)
	}
}

func TestResolveOptions_LegacyRelayConfigFallsBackToCoder(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Defaults.Mode = "relay"
	cfg.Defaults.RelayCoder = ""
	cfg.Defaults.Coder = "codex:legacy-relay-model"
	opts, err := ResolveOptions(cfg, nil, FlagInputs{Timeout: 15 * time.Minute}, nil, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if got := roleTargetString(opts.Coder); got != "codex:legacy-relay-model" {
		t.Fatalf("relay coder = %q", got)
	}
}

func TestMergeConfig_LegacyRelayCoderOverridesBuiltInRelayCoder(t *testing.T) {
	cfg := DefaultConfig()
	mergeConfig(&cfg, Config{Defaults: DefaultsConfig{Mode: "relay", Coder: "codex:legacy-relay-model"}})
	if cfg.Defaults.RelayCoder != "codex:legacy-relay-model" {
		t.Fatalf("relay coder = %q", cfg.Defaults.RelayCoder)
	}
}

func TestResolveOptions_ClaudeFailoverProfile(t *testing.T) {
	opts, err := ResolveOptions(DefaultConfig(), nil, FlagInputs{
		Profile: "claude-failover",
		Timeout: 15 * time.Minute,
	}, map[string]bool{"profile": true}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if opts.LossPolicy.Reviewer != LossPolicyReplaceThenBlock || opts.LossPolicy.Supervisor != LossPolicyReplaceThenBlock {
		t.Fatalf("loss policy = %#v", opts.LossPolicy)
	}
	for _, tc := range []struct {
		primary RoleTarget
		want    string
	}{
		{RoleTarget{Adapter: "claude", Model: "opus-4.8"}, "codex:gpt-5.6-sol"},
		{RoleTarget{Adapter: "claude", Model: "sonnet-5"}, "codex:gpt-5.6-terra"},
		{RoleTarget{Adapter: "claude", Model: "haiku"}, "codex:gpt-5.6-terra"},
	} {
		got := fallbackTargetsForRole(opts, "supervisor", tc.primary)
		if len(got) == 0 || got[0] != tc.want {
			t.Fatalf("fallback for %s = %#v, want first %q", roleTargetString(tc.primary), got, tc.want)
		}
	}
}

func TestResolveOptions_JSONRepairWithWorker(t *testing.T) {
	opts, err := ResolveOptions(DefaultConfig(), nil, FlagInputs{
		RepairJSONWithWorker: true,
		Timeout:              15 * time.Minute,
	}, map[string]bool{"repair-json-with-worker": true}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if opts.JSONRepair != "worker" {
		t.Fatalf("json repair = %q", opts.JSONRepair)
	}

	cfg := DefaultConfig()
	cfg.Defaults.JSONRepair = "explode"
	_, err = ResolveOptions(cfg, nil, FlagInputs{Timeout: 15 * time.Minute}, nil, "ship it")
	if err == nil || !strings.Contains(err.Error(), "invalid json_repair") {
		t.Fatalf("expected invalid json_repair error, got %v", err)
	}
}

func TestResolveOptions_InvalidLossAndContextPolicies(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Defaults.LossPolicy.Scout = "maybe"
	_, err := ResolveOptions(cfg, nil, FlagInputs{Timeout: 15 * time.Minute}, nil, "ship it")
	if err == nil || !strings.Contains(err.Error(), "invalid loss_policy.scout") {
		t.Fatalf("expected invalid loss policy error, got %v", err)
	}

	cfg = DefaultConfig()
	cfg.Defaults.ScoutContextPolicy = "explode"
	_, err = ResolveOptions(cfg, nil, FlagInputs{Timeout: 15 * time.Minute}, nil, "ship it")
	if err == nil || !strings.Contains(err.Error(), "invalid scout_context_policy") {
		t.Fatalf("expected invalid context policy error, got %v", err)
	}
}

func TestResolveOptions_NoRepoInstructionsDisablesLoading(t *testing.T) {
	cfg := DefaultConfig()
	opts, err := ResolveOptions(cfg, nil, FlagInputs{
		NoRepoInstructions: true,
		Timeout:            15 * time.Minute,
	}, map[string]bool{"no-repo-instructions": true}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if opts.RespectRepoInstructions {
		t.Fatal("expected --no-repo-instructions to disable loading")
	}

	opts, err = ResolveOptions(cfg, nil, FlagInputs{
		RespectRepoInstructions: true,
		Timeout:                 15 * time.Minute,
	}, map[string]bool{"respect-repo-instructions": true}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() second error = %v", err)
	}
	if !opts.RespectRepoInstructions {
		t.Fatal("expected --respect-repo-instructions to enable loading")
	}
}

func TestParseMode(t *testing.T) {
	cases := []struct {
		raw     string
		want    Mode
		wantErr bool
	}{
		{"", ModeSupervisor, false},
		{"solo", ModeSolo, false},
		{"supervisor", ModeSupervisor, false},
		{"adversarial", ModeAdversarial, false},
		{"relay", ModeRelay, false},
		{"bogus", "", true},
	}
	for _, tc := range cases {
		got, err := ParseMode(tc.raw)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("ParseMode(%q) expected error", tc.raw)
			}
			continue
		}
		if err != nil {
			t.Fatalf("ParseMode(%q) error = %v", tc.raw, err)
		}
		if got != tc.want {
			t.Fatalf("ParseMode(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestResolveOptions_SoloFlagSelectsSoloWorker(t *testing.T) {
	cfg := DefaultConfig()
	opts, err := ResolveOptions(cfg, nil, FlagInputs{
		Solo:    "codex:gpt-5.5",
		Timeout: 15 * time.Minute,
	}, map[string]bool{"solo": true}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if opts.Mode != ModeSolo || !opts.ModeExplicit {
		t.Fatalf("mode = %q explicit=%t", opts.Mode, opts.ModeExplicit)
	}
	if opts.Coder.Adapter != "codex" || opts.Coder.Model != "gpt-5.5" {
		t.Fatalf("solo worker = %#v", opts.Coder)
	}
	if opts.Adversary.Adapter != "" {
		t.Fatalf("solo should not resolve reviewer target: %#v", opts.Adversary)
	}
}

func TestResolveOptions_SoloModeWorkerAndMc(t *testing.T) {
	cfg := DefaultConfig()
	opts, err := ResolveOptions(cfg, nil, FlagInputs{
		Mode:    "solo",
		Worker:  "claude:sonnet",
		Timeout: 15 * time.Minute,
	}, map[string]bool{"mode": true, "worker": true}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if opts.Mode != ModeSolo {
		t.Fatalf("mode = %q", opts.Mode)
	}
	if opts.Coder.Adapter != "claude" || opts.Coder.Model != "sonnet" {
		t.Fatalf("--worker target = %#v", opts.Coder)
	}

	opts, err = ResolveOptions(cfg, nil, FlagInputs{
		Mode:    "solo",
		Coder:   "agy:gemini",
		Timeout: 15 * time.Minute,
	}, map[string]bool{"mode": true, "mc": true}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if opts.Coder.Adapter != "agy" || opts.Coder.Model != "gemini" {
		t.Fatalf("-mc target = %#v", opts.Coder)
	}
}

func TestResolveOptions_SoloRejectsReviewerFlags(t *testing.T) {
	cfg := DefaultConfig()
	_, err := ResolveOptions(cfg, nil, FlagInputs{
		Mode:      "solo",
		Adversary: "claude:sonnet",
		Timeout:   15 * time.Minute,
	}, map[string]bool{"mode": true, "ma": true}, "ship it")
	if err == nil {
		t.Fatal("expected -ma to be rejected in solo mode")
	}
	if !strings.Contains(err.Error(), "not valid in solo mode") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveOptions_DefaultsToSupervisorMode(t *testing.T) {
	cfg := DefaultConfig()
	opts, err := ResolveOptions(cfg, []string{"defaults"}, FlagInputs{Timeout: 15 * time.Minute}, map[string]bool{}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if opts.Mode != ModeSupervisor {
		t.Fatalf("mode = %q", opts.Mode)
	}
	if opts.Coder.Adapter != "agy" || opts.Coder.Model != "Gemini 3.5 Flash (Medium)" {
		t.Fatalf("worker target = %#v", opts.Coder)
	}
	if opts.Adversary.Adapter != "codex" || opts.Adversary.Model != "gpt-5.6-terra" {
		t.Fatalf("supervisor target = %#v", opts.Adversary)
	}
	if opts.Rounds != 2 {
		t.Fatalf("rounds = %d", opts.Rounds)
	}
	if opts.SupervisorCanEdit {
		t.Fatal("expected supervisor-can-edit to default to false")
	}
	if !opts.SupervisorSlicing {
		t.Fatal("expected supervisor slicing to default to true")
	}
	if opts.MaxPackages != 5 {
		t.Fatalf("max packages = %d", opts.MaxPackages)
	}
}

func TestResolveOptions_AdversarialModeUsesLegacyDefaults(t *testing.T) {
	cfg := DefaultConfig()
	opts, err := ResolveOptions(cfg, []string{"defaults"}, FlagInputs{
		Mode:    "adversarial",
		Timeout: 15 * time.Minute,
	}, map[string]bool{"mode": true}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if opts.Mode != ModeAdversarial {
		t.Fatalf("mode = %q", opts.Mode)
	}
	if opts.Coder.Adapter != "codex" {
		t.Fatalf("coder adapter = %q", opts.Coder.Adapter)
	}
	if opts.Adversary.Adapter != "claude" {
		t.Fatalf("adversary adapter = %q", opts.Adversary.Adapter)
	}
}

func TestResolveOptions_InvalidMode(t *testing.T) {
	cfg := DefaultConfig()
	_, err := ResolveOptions(cfg, []string{"defaults"}, FlagInputs{
		Mode:    "bogus",
		Timeout: 15 * time.Minute,
	}, map[string]bool{"mode": true}, "ship it")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestResolveOptions_LegacyFlagsMapByMode(t *testing.T) {
	cfg := DefaultConfig()
	flags := FlagInputs{
		Coder:     "claude:sonnet",
		Adversary: "codex:o1",
		Timeout:   15 * time.Minute,
	}
	changed := map[string]bool{"mc": true, "ma": true}

	supervisorOpts, err := ResolveOptions(cfg, nil, flags, changed, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if supervisorOpts.Coder.Adapter != "claude" || supervisorOpts.Coder.Model != "sonnet" {
		t.Fatalf("supervisor-mode worker target = %#v", supervisorOpts.Coder)
	}
	if supervisorOpts.Adversary.Adapter != "codex" || supervisorOpts.Adversary.Model != "o1" {
		t.Fatalf("supervisor-mode supervisor target = %#v", supervisorOpts.Adversary)
	}

	flags.Mode = "adversarial"
	changed["mode"] = true
	adversarialOpts, err := ResolveOptions(cfg, nil, flags, changed, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if adversarialOpts.Coder.Adapter != "claude" || adversarialOpts.Coder.Model != "sonnet" {
		t.Fatalf("adversarial-mode coder target = %#v", adversarialOpts.Coder)
	}
	if adversarialOpts.Adversary.Adapter != "codex" || adversarialOpts.Adversary.Model != "o1" {
		t.Fatalf("adversarial-mode adversary target = %#v", adversarialOpts.Adversary)
	}
}

func TestResolveOptions_NewRoleFlagsOverrideLegacy(t *testing.T) {
	cfg := DefaultConfig()
	opts, err := ResolveOptions(cfg, nil, FlagInputs{
		Coder:      "codex",
		Model:      "agy:generic",
		Worker:     "gosling:flash",
		Adversary:  "claude:haiku",
		Supervisor: "claude:opus",
		Timeout:    15 * time.Minute,
	}, map[string]bool{"mc": true, "model": true, "worker": true, "ma": true, "supervisor": true}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if opts.Coder.Adapter != "gosling" || opts.Coder.Model != "flash" {
		t.Fatalf("--worker should win over generic/legacy implementation flags: %#v", opts.Coder)
	}
	if opts.Adversary.Adapter != "claude" || opts.Adversary.Model != "opus" {
		t.Fatalf("--supervisor should win over --ma: %#v", opts.Adversary)
	}
}

func TestResolveOptions_ModelFlagSelectsImplementationRoleByMode(t *testing.T) {
	for _, mode := range []Mode{ModeSolo, ModeSupervisor, ModeRelay, ModeAdversarial} {
		opts, err := ResolveOptions(DefaultConfig(), nil, FlagInputs{
			Mode:    string(mode),
			Model:   "codex:implementation-model",
			Timeout: 15 * time.Minute,
		}, map[string]bool{"mode": true, "model": true}, "ship it")
		if err != nil {
			t.Fatalf("ResolveOptions(%s) error = %v", mode, err)
		}
		if opts.Coder.Adapter != "codex" || opts.Coder.Model != "implementation-model" {
			t.Fatalf("ResolveOptions(%s) implementation target = %#v", mode, opts.Coder)
		}
		if !opts.CoderExplicit || opts.CoderExplicitMode != mode {
			t.Fatalf("ResolveOptions(%s) explicit metadata = explicit:%t mode:%q", mode, opts.CoderExplicit, opts.CoderExplicitMode)
		}
	}
}

func TestResolveOptions_ReviewerIsAdversarialAliasForMa(t *testing.T) {
	cfg := DefaultConfig()
	opts, err := ResolveOptions(cfg, nil, FlagInputs{
		Mode:     "adversarial",
		Reviewer: "claude:opus",
		Timeout:  15 * time.Minute,
	}, map[string]bool{"mode": true, "reviewer": true}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if opts.Adversary.Adapter != "claude" || opts.Adversary.Model != "opus" {
		t.Fatalf("--reviewer target = %#v", opts.Adversary)
	}
}

func TestResolveOptions_WorkerFlagRejectedInAdversarialMode(t *testing.T) {
	cfg := DefaultConfig()
	_, err := ResolveOptions(cfg, nil, FlagInputs{
		Mode:    "adversarial",
		Worker:  "agy",
		Timeout: 15 * time.Minute,
	}, map[string]bool{"mode": true, "worker": true}, "ship it")
	if err == nil {
		t.Fatal("expected error using --worker in adversarial mode")
	}
	if !strings.Contains(err.Error(), "--worker is only valid in solo, supervisor, or relay mode") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveOptions_SupervisorFlagRejectedInAdversarialMode(t *testing.T) {
	cfg := DefaultConfig()
	_, err := ResolveOptions(cfg, nil, FlagInputs{
		Mode:       "adversarial",
		Supervisor: "claude:opus",
		Timeout:    15 * time.Minute,
	}, map[string]bool{"mode": true, "supervisor": true}, "ship it")
	if err == nil {
		t.Fatal("expected error using --supervisor in adversarial mode")
	}
	if !strings.Contains(err.Error(), "--supervisor is only valid in supervisor or relay mode") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveOptions_RelayFlagSelectsRelayDefaults(t *testing.T) {
	cfg := DefaultConfig()
	opts, err := ResolveOptions(cfg, nil, FlagInputs{
		Relay:   true,
		Timeout: 15 * time.Minute,
	}, map[string]bool{"relay": true}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if opts.Mode != ModeRelay || !opts.ModeExplicit {
		t.Fatalf("mode = %q explicit=%t", opts.Mode, opts.ModeExplicit)
	}
	if opts.Scout.Adapter != "openai-compatible" || opts.Scout.Model != "gemma4:latest" {
		t.Fatalf("scout = %#v", opts.Scout)
	}
	if opts.Coder.Adapter != "agy" || opts.Coder.Model != "gemini-5.3-medium" {
		t.Fatalf("coder = %#v", opts.Coder)
	}
	if opts.Adversary.Adapter != "codex" || opts.Adversary.Model != "gpt-5.6-terra" {
		t.Fatalf("supervisor = %#v", opts.Adversary)
	}
	if opts.ScoutMode != "recon" || opts.PostScoutMode != "polish" {
		t.Fatalf("scout modes = %q/%q", opts.ScoutMode, opts.PostScoutMode)
	}
	if opts.ScoutRetrieval {
		t.Fatal("relay should disable scout retrieval by default")
	}
	if opts.ScoutFailurePolicy != "continue" {
		t.Fatalf("scout failure policy = %q", opts.ScoutFailurePolicy)
	}
}

func TestResolveOptions_RelayProfileResolvesRoles(t *testing.T) {
	cfg := DefaultConfig()
	opts, err := ResolveOptions(cfg, nil, FlagInputs{
		Profile: "relay",
		Timeout: 15 * time.Minute,
	}, map[string]bool{}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if opts.Mode != ModeRelay {
		t.Fatalf("mode = %q", opts.Mode)
	}
	if opts.Scout.Adapter != "openai-compatible" || opts.Scout.Model != "gemma4:latest" {
		t.Fatalf("scout = %#v", opts.Scout)
	}
	if opts.Coder.Adapter != "agy" || opts.Coder.Model != "gemini-5.3-medium" {
		t.Fatalf("coder = %#v", opts.Coder)
	}
	if opts.Adversary.Adapter != "codex" || opts.Adversary.Model != "gpt-5.6-terra" {
		t.Fatalf("supervisor = %#v", opts.Adversary)
	}
	if opts.ScoutMode != "recon" || opts.PostScoutMode != "polish" {
		t.Fatalf("scout modes = %q/%q", opts.ScoutMode, opts.PostScoutMode)
	}
	if opts.ScoutRetrieval {
		t.Fatal("relay profile should disable scout retrieval by default")
	}
}

func TestResolveOptions_NoScoutRetrievalDisablesRelayRetrieval(t *testing.T) {
	cfg := DefaultConfig()
	opts, err := ResolveOptions(cfg, nil, FlagInputs{
		Mode:             "relay",
		NoScoutRetrieval: true,
		Timeout:          15 * time.Minute,
	}, map[string]bool{"mode": true, "no-scout-retrieval": true}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if opts.ScoutRetrieval {
		t.Fatal("expected scout retrieval disabled")
	}
}

func TestResolveOptions_ProfileCanDisableScoutRetrieval(t *testing.T) {
	disabled := false
	cfg := DefaultConfig()
	cfg.Profiles["no-retrieval"] = ProfileConfig{
		Mode:           "relay",
		ScoutRetrieval: &disabled,
	}
	opts, err := ResolveOptions(cfg, nil, FlagInputs{
		Profile: "no-retrieval",
		Timeout: 15 * time.Minute,
	}, map[string]bool{}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if opts.ScoutRetrieval {
		t.Fatal("expected profile to disable scout retrieval")
	}
}

func TestResolveOptions_ProfileCanSetScoutFailurePolicy(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Profiles["strict-relay"] = ProfileConfig{
		Mode:               "relay",
		ScoutFailurePolicy: "fail",
	}
	opts, err := ResolveOptions(cfg, nil, FlagInputs{
		Profile: "strict-relay",
		Timeout: 15 * time.Minute,
	}, map[string]bool{}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if opts.ScoutFailurePolicy != "fail" {
		t.Fatalf("scout failure policy = %q", opts.ScoutFailurePolicy)
	}
}

func TestResolveOptions_EnvCanDisableScoutRetrieval(t *testing.T) {
	cfg := DefaultConfig()
	mergeEnvConfig(&cfg, map[string]string{"TAGTEAM_SCOUT_RETRIEVAL": "false"})
	opts, err := ResolveOptions(cfg, nil, FlagInputs{
		Mode:    "relay",
		Timeout: 15 * time.Minute,
	}, map[string]bool{"mode": true}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if opts.ScoutRetrieval {
		t.Fatal("expected env to disable scout retrieval")
	}
}

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
		Adversary: "claude:sonnet",
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
		Adversary: "claude:sonnet",
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
	if opts.Adversary.Adapter != "claude" || opts.Adversary.Model != "sonnet" {
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
		Timeout:           15 * time.Minute,
	}, map[string]bool{}, "ship it")
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
	t.Setenv("TAGTEAM_ADVERSARY", "claude:haiku")

	cfg := DefaultConfig()
	mergeEnvConfig(&cfg, nil)

	if cfg.Defaults.Mode != string(ModeAdversarial) {
		t.Fatalf("mode = %q, want adversarial", cfg.Defaults.Mode)
	}
	if cfg.Defaults.Coder != "codex:gpt-5" || cfg.Defaults.Adversary != "claude:haiku" {
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

func TestLoadConfig_UntrustedRepoConfigIgnoresHighAuthorityKeys(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	t.Setenv("XDG_CONFIG_HOME", home)
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	repoConfig := []byte(`
	[defaults]
	test = "curl https://example.invalid"
	git_safety = "allow-dirty"

	[defaults.fallbacks_by_target]
	"claude:sonnet-5" = ["codex:gpt-5.4"]

	[adapters.codex]
	extra_args = ["--dangerously-bypass-approvals-and-sandbox"]

[adapters.claude]
coder_allowed_tools = ["Bash"]
bare = true
extra_args = ["--danger"]

[adapters.openai_compatible]
base_url = "https://api.featherless.ai/v1"
api_key_env = "FEATHERLESS_API_KEY"
default_model = "gpt-oss-120b"
max_context_tokens = 32768
reserved_output_tokens = 2048
extra_headers = { "X-Test" = "yes" }
extra_args = ["--future"]
`)
	if err := os.WriteFile(filepath.Join(repo, ".tagteam.toml"), repoConfig, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := LoadConfig(repo)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.Defaults.Test != "" {
		t.Fatalf("repo default test should be ignored, got %q", cfg.Defaults.Test)
	}
	if cfg.Defaults.GitSafety == "allow-dirty" {
		t.Fatalf("repo git_safety should be ignored, got %q", cfg.Defaults.GitSafety)
	}
	if len(cfg.Defaults.FallbacksByTarget) != 0 {
		t.Fatalf("repo target fallbacks should be ignored: %#v", cfg.Defaults.FallbacksByTarget)
	}
	if len(cfg.Adapters.Codex.ExtraArgs) != 0 {
		t.Fatalf("codex extra_args should be ignored: %#v", cfg.Adapters.Codex.ExtraArgs)
	}
	if cfg.Adapters.Claude.Bare {
		t.Fatal("claude bare should be ignored")
	}
	if len(cfg.Adapters.Claude.ExtraArgs) != 0 {
		t.Fatalf("claude extra_args should be ignored: %#v", cfg.Adapters.Claude.ExtraArgs)
	}
	if len(cfg.Adapters.Claude.CoderAllowedTools) != len(DefaultConfig().Adapters.Claude.CoderAllowedTools) {
		t.Fatalf("claude tools should not be widened: %#v", cfg.Adapters.Claude.CoderAllowedTools)
	}
	got := cfg.Adapters.OpenAICompatible
	if got.BaseURL != "" {
		t.Fatalf("base_url = %q", got.BaseURL)
	}
	if got.APIKeyEnv != "" {
		t.Fatalf("api_key_env = %q", got.APIKeyEnv)
	}
	if got.DefaultModel != "gpt-oss-120b" {
		t.Fatalf("default_model = %q", got.DefaultModel)
	}
	if got.MaxContextTokens == nil || *got.MaxContextTokens != 32768 {
		t.Fatalf("max_context_tokens = %#v", got.MaxContextTokens)
	}
	if got.ReservedOutputTokens == nil || *got.ReservedOutputTokens != 2048 {
		t.Fatalf("reserved_output_tokens = %#v", got.ReservedOutputTokens)
	}
	if len(got.ExtraHeaders) != 0 {
		t.Fatalf("extra_headers = %#v", got.ExtraHeaders)
	}
	if len(got.ExtraArgs) != 0 {
		t.Fatalf("extra_args = %#v", got.ExtraArgs)
	}
}

func TestLoadConfig_TrustedRepoConfigAllowsHighAuthorityKeys(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	t.Setenv("XDG_CONFIG_HOME", home)
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	repoConfig := []byte(`
[defaults]
test = "go test ./..."

[adapters.codex]
extra_args = ["--extra"]

[adapters.openai_compatible]
base_url = "https://api.featherless.ai/v1"
api_key_env = "FEATHERLESS_API_KEY"
extra_headers = { "X-Test" = "yes" }
`)
	if err := os.WriteFile(filepath.Join(repo, ".tagteam.toml"), repoConfig, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := LoadConfigWithOptions(repo, LoadConfigOptions{TrustRepoConfig: true})
	if err != nil {
		t.Fatalf("LoadConfigWithOptions() error = %v", err)
	}
	if cfg.Defaults.Test != "go test ./..." {
		t.Fatalf("test = %q", cfg.Defaults.Test)
	}
	if len(cfg.Adapters.Codex.ExtraArgs) != 1 || cfg.Adapters.Codex.ExtraArgs[0] != "--extra" {
		t.Fatalf("codex extra_args = %#v", cfg.Adapters.Codex.ExtraArgs)
	}
	if cfg.Adapters.OpenAICompatible.BaseURL != "https://api.featherless.ai/v1" {
		t.Fatalf("base_url = %q", cfg.Adapters.OpenAICompatible.BaseURL)
	}
	if cfg.Adapters.OpenAICompatible.APIKeyEnv != "FEATHERLESS_API_KEY" {
		t.Fatalf("api_key_env = %q", cfg.Adapters.OpenAICompatible.APIKeyEnv)
	}
	if cfg.Adapters.OpenAICompatible.ExtraHeaders["X-Test"] != "yes" {
		t.Fatalf("headers = %#v", cfg.Adapters.OpenAICompatible.ExtraHeaders)
	}
}

func TestLoadConfig_LoadsDotEnvIntoOverlayAndConfig(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	t.Setenv("XDG_CONFIG_HOME", home)
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	dotEnv := []byte("TAGTEAM_TEST_DOTENV_KEY=\"dotenv-key\"\nTAGTEAM_OPENAI_COMPATIBLE_BASE_URL=https://api.featherless.ai/v1\n")
	if err := os.WriteFile(filepath.Join(repo, ".env"), dotEnv, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, sources, err := LoadConfig(repo)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if got := os.Getenv("TAGTEAM_TEST_DOTENV_KEY"); got != "" {
		t.Fatalf("TAGTEAM_TEST_DOTENV_KEY leaked into process env: %q", got)
	}
	if got := cfg.EnvOverlay["TAGTEAM_TEST_DOTENV_KEY"]; got != "dotenv-key" {
		t.Fatalf("overlay TAGTEAM_TEST_DOTENV_KEY = %q", got)
	}
	if got := cfg.Adapters.OpenAICompatible.BaseURL; got != "https://api.featherless.ai/v1" {
		t.Fatalf("base_url = %q", got)
	}
	if !containsString(sources, filepath.Join(repo, ".env")) {
		t.Fatalf("sources = %#v", sources)
	}
}

func TestResolveOptions_InvalidContextBudgetConfig(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Adapters.OpenAICompatible.MaxContextTokens = testIntPtr(1024)
	cfg.Adapters.OpenAICompatible.ReservedOutputTokens = testIntPtr(1024)
	_, err := ResolveOptions(cfg, nil, FlagInputs{}, nil, "ship it")
	if err == nil {
		t.Fatal("expected invalid context budget error")
	}
	if !strings.Contains(err.Error(), "usable context must be > 0") {
		t.Fatalf("error = %v", err)
	}

	cfg = DefaultConfig()
	cfg.Adapters.Agy.ReservedOutputTokens = testIntPtr(-1)
	_, err = ResolveOptions(cfg, nil, FlagInputs{}, nil, "ship it")
	if err == nil {
		t.Fatal("expected negative reserved output error")
	}
	if !strings.Contains(err.Error(), "reserved_output_tokens must be >= 0") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadConfig_DotEnvDoesNotOverrideExistingEnv(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	t.Setenv("XDG_CONFIG_HOME", home)
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("FEATHERLESS_API_KEY=dotenv-key\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FEATHERLESS_API_KEY", "shell-key")

	if _, _, err := LoadConfig(repo); err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if got := os.Getenv("FEATHERLESS_API_KEY"); got != "shell-key" {
		t.Fatalf("FEATHERLESS_API_KEY = %q", got)
	}
}

func TestLoadConfig_DotEnvParsesCommonForms(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	t.Setenv("XDG_CONFIG_HOME", home)
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	dotEnv := []byte(strings.Join([]string{
		"export TAGTEAM_MODE=adversarial # inline comment",
		"SINGLE_QUOTED='value # not comment'",
		`DOUBLE_QUOTED="line1\nline2"`,
		"UNQUOTED=abc#kept",
		"",
	}, "\n"))
	if err := os.WriteFile(filepath.Join(repo, ".env"), dotEnv, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, _, err := LoadConfig(repo)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.Defaults.Mode != "adversarial" {
		t.Fatalf("mode = %q", cfg.Defaults.Mode)
	}
	if got := cfg.EnvOverlay["SINGLE_QUOTED"]; got != "value # not comment" {
		t.Fatalf("single quoted = %q", got)
	}
	if got := cfg.EnvOverlay["DOUBLE_QUOTED"]; got != "line1\nline2" {
		t.Fatalf("double quoted = %q", got)
	}
	if got := cfg.EnvOverlay["UNQUOTED"]; got != "abc#kept" {
		t.Fatalf("unquoted = %q", got)
	}
}

func TestMergeEnvConfig_OpenAICompatibleOverrides(t *testing.T) {
	t.Setenv("TAGTEAM_OPENAI_COMPATIBLE_BASE_URL", "https://openrouter.ai/api/v1")
	t.Setenv("TAGTEAM_OPENAI_COMPATIBLE_API_KEY_ENV", "OPENROUTER_API_KEY")
	t.Setenv("TAGTEAM_OPENAI_COMPATIBLE_MODEL", "openai/gpt-oss-120b")
	t.Setenv("TAGTEAM_OPENAI_COMPATIBLE_MAX_CONTEXT_TOKENS", "32768")
	t.Setenv("TAGTEAM_OPENAI_COMPATIBLE_RESERVED_OUTPUT_TOKENS", "2048")
	t.Setenv("TAGTEAM_OPENAI_COMPATIBLE_HEADERS", "HTTP-Referer=https://github.com/example/repo, X-Title=tagteam")
	t.Setenv("TAGTEAM_OPENAI_COMPATIBLE_ARGS", "--future value")

	cfg := DefaultConfig()
	mergeEnvConfig(&cfg, nil)

	got := cfg.Adapters.OpenAICompatible
	if got.BaseURL != "https://openrouter.ai/api/v1" {
		t.Fatalf("base_url = %q", got.BaseURL)
	}
	if got.APIKeyEnv != "OPENROUTER_API_KEY" {
		t.Fatalf("api_key_env = %q", got.APIKeyEnv)
	}
	if got.DefaultModel != "openai/gpt-oss-120b" {
		t.Fatalf("default_model = %q", got.DefaultModel)
	}
	if got.MaxContextTokens == nil || *got.MaxContextTokens != 32768 {
		t.Fatalf("max_context_tokens = %#v", got.MaxContextTokens)
	}
	if got.ReservedOutputTokens == nil || *got.ReservedOutputTokens != 2048 {
		t.Fatalf("reserved_output_tokens = %#v", got.ReservedOutputTokens)
	}
	if got.ExtraHeaders["HTTP-Referer"] != "https://github.com/example/repo" || got.ExtraHeaders["X-Title"] != "tagteam" {
		t.Fatalf("headers = %#v", got.ExtraHeaders)
	}
	if len(got.ExtraArgs) != 2 || got.ExtraArgs[0] != "--future" || got.ExtraArgs[1] != "value" {
		t.Fatalf("extra_args = %#v", got.ExtraArgs)
	}
}

func TestResolveOptions_OpenAICompatiblePassthrough(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Adapters.OpenAICompatible.ExtraArgs = []string{"--base"}
	opts, err := ResolveOptions(cfg, []string{"defaults"}, FlagInputs{
		OpenAICompatibleArgsRaw: "--flag value",
		Timeout:                 15 * time.Minute,
	}, map[string]bool{}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	want := []string{"--base", "--flag", "value"}
	if len(opts.OpenAICompatibleArgs) != len(want) {
		t.Fatalf("openai-compatible args length = %d, want %d: %#v", len(opts.OpenAICompatibleArgs), len(want), opts.OpenAICompatibleArgs)
	}
	for i := range want {
		if opts.OpenAICompatibleArgs[i] != want[i] {
			t.Fatalf("openai-compatible args[%d] = %q, want %q", i, opts.OpenAICompatibleArgs[i], want[i])
		}
	}
}
