package tagteam

import (
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
	if opts.Adversary.Adapter != "codex" || opts.Adversary.Model != "gpt-5.6-terra" {
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
	// Adversarial-mode defaults remain independently configurable.
	if cfg.Defaults.Coder != defaultAdversarialCoderTarget || cfg.Defaults.Adversary != defaultAdversaryTarget {
		t.Fatalf("legacy defaults = coder=%q adversary=%q", cfg.Defaults.Coder, cfg.Defaults.Adversary)
	}
	if cfg.Adapters.Codex.ReasoningEffort != "high" || cfg.Adapters.Claude.Effort != "high" {
		t.Fatalf("effort defaults = codex=%q claude=%q", cfg.Adapters.Codex.ReasoningEffort, cfg.Adapters.Claude.Effort)
	}
	if cfg.Adapters.Agy.DefaultModel != agyGemini36FlashMedium {
		t.Fatalf("agy default model = %q", cfg.Adapters.Agy.DefaultModel)
	}
	if cfg.Defaults.LossPolicy.Scout != LossPolicyDegrade || cfg.Defaults.LossPolicy.Supervisor != LossPolicyBlock {
		t.Fatalf("loss policy = %#v", cfg.Defaults.LossPolicy)
	}
	if cfg.Defaults.LossPolicy.Worker != LossPolicyReplaceThenBlock || len(cfg.Defaults.Fallbacks.Worker) != 1 || cfg.Defaults.Fallbacks.Worker[0] != defaultWorkerFallback {
		t.Fatalf("worker fallback policy = policy:%q fallbacks:%#v", cfg.Defaults.LossPolicy.Worker, cfg.Defaults.Fallbacks.Worker)
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
		Scout:      []string{"agy:gemini-3.6-flash-low", "openai-compatible:gpt-oss"},
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
	if got := strings.Join(opts.Fallbacks.Scout, ","); got != "agy:gemini-3.6-flash-low,openai-compatible:gpt-oss" {
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
		{RoleTarget{Adapter: "claude", Model: "opus-4.8"}, defaultSupervisorFallback},
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
		Worker:  "codex:gpt-5.6-terra",
		Timeout: 15 * time.Minute,
	}, map[string]bool{"mode": true, "worker": true}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if opts.Mode != ModeSolo {
		t.Fatalf("mode = %q", opts.Mode)
	}
	if opts.Coder.Adapter != "codex" || opts.Coder.Model != "gpt-5.6-terra" {
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
	if opts.Coder.Adapter != "codex" || opts.Coder.Model != "gpt-5.6-terra" {
		t.Fatalf("worker target = %#v", opts.Coder)
	}
	if opts.Adversary.Adapter != "claude" || opts.Adversary.Model != "claude-opus-4-8" {
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

func TestResolveOptions_AdversarialModeUsesDefaults(t *testing.T) {
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

func TestValidateClaudeRoleAssignmentsRejectsImplementationAndScout(t *testing.T) {
	tests := []struct {
		name string
		opts RunOptions
	}{
		{name: "solo worker", opts: RunOptions{Mode: ModeSolo, Coder: RoleTarget{Adapter: "claude"}}},
		{name: "supervisor worker", opts: RunOptions{Mode: ModeSupervisor, Coder: RoleTarget{Adapter: "claude"}, Adversary: RoleTarget{Adapter: "codex"}}},
		{name: "relay scout", opts: RunOptions{Mode: ModeRelay, Coder: RoleTarget{Adapter: "agy"}, Adversary: RoleTarget{Adapter: "codex"}, Scout: RoleTarget{Adapter: "claude"}}},
		{name: "editing supervisor", opts: RunOptions{Mode: ModeSupervisor, Coder: RoleTarget{Adapter: "agy"}, Adversary: RoleTarget{Adapter: "claude"}, SupervisorCanEdit: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateClaudeRoleAssignments(tt.opts)
			if err == nil || !strings.Contains(err.Error(), "claude") {
				t.Fatalf("validateClaudeRoleAssignments() error = %v, want Claude role rejection", err)
			}
		})
	}
}

func TestValidateRunRolesAllowsReadOnlyClaudeSupervisor(t *testing.T) {
	opts := RunOptions{
		Mode:      ModeSupervisor,
		Coder:     RoleTarget{Adapter: "agy"},
		Adversary: RoleTarget{Adapter: "claude"},
	}
	if err := validateClaudeRoleAssignments(opts); err != nil {
		t.Fatalf("validateClaudeRoleAssignments() error = %v", err)
	}
}

func TestValidateClaudeRoleAssignmentsAllowsClaudeAdversary(t *testing.T) {
	err := validateClaudeRoleAssignments(RunOptions{Mode: ModeAdversarial, Coder: RoleTarget{Adapter: "codex"}, Adversary: RoleTarget{Adapter: "claude"}})
	if err != nil {
		t.Fatalf("validateClaudeRoleAssignments() error = %v", err)
	}
}

func TestResolveOptions_LegacyFlagsMapByMode(t *testing.T) {
	cfg := DefaultConfig()
	flags := FlagInputs{
		Coder:     "agy:gemini",
		Adversary: "codex:o1",
		Timeout:   15 * time.Minute,
	}
	changed := map[string]bool{"mc": true, "ma": true}

	supervisorOpts, err := ResolveOptions(cfg, nil, flags, changed, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if supervisorOpts.Coder.Adapter != "agy" || supervisorOpts.Coder.Model != "gemini" {
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
	if adversarialOpts.Coder.Adapter != "agy" || adversarialOpts.Coder.Model != "gemini" {
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
		Reviewer: "codex:gpt-5.6-sol",
		Timeout:  15 * time.Minute,
	}, map[string]bool{"mode": true, "reviewer": true}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if opts.Adversary.Adapter != "codex" || opts.Adversary.Model != "gpt-5.6-sol" {
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
	if opts.Coder.Adapter != "codex" || opts.Coder.Model != "gpt-5.6-terra" {
		t.Fatalf("coder = %#v", opts.Coder)
	}
	if opts.Adversary.Adapter != "claude" || opts.Adversary.Model != "claude-opus-4-8" {
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
	if opts.Coder.Adapter != "codex" || opts.Coder.Model != "gpt-5.6-terra" {
		t.Fatalf("coder = %#v", opts.Coder)
	}
	if opts.Adversary.Adapter != "claude" || opts.Adversary.Model != "claude-opus-4-8" {
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

func TestResolveOptionsRejectsInvalidAllowedPaths(t *testing.T) {
	_, err := ResolveOptions(DefaultConfig(), nil, FlagInputs{
		AllowedPaths: []string{"../outside"},
		Timeout:      15 * time.Minute,
	}, map[string]bool{"allow-path": true}, "ship it")
	if err == nil || !strings.Contains(err.Error(), `invalid --allow-path "../outside": parent traversal (..) is forbidden`) {
		t.Fatalf("ResolveOptions() error = %v, want invalid allow-path error", err)
	}
}

func TestValidateAllowedPathsReturnsDeterministicActionableErrors(t *testing.T) {
	tests := []struct {
		name  string
		paths []string
		want  string
	}{
		{name: "missing", paths: nil, want: "at least one --allow-path is required"},
		{name: "absolute", paths: []string{"/internal"}, want: `invalid --allow-path "/internal": path must be repo-relative, not absolute; use "internal" for an exact path or "internal/" for a directory`},
		{name: "parent", paths: []string{"docs/../secrets"}, want: `invalid --allow-path "docs/../secrets": parent traversal (..) is forbidden`},
		{name: "glob", paths: []string{"docs/*.md"}, want: `invalid --allow-path "docs/*.md": glob syntax is not supported`},
		{name: "duplicate", paths: []string{"./docs/", "docs/"}, want: `invalid --allow-path "docs/": duplicates "./docs/" after normalization to "docs/"`},
		{name: "first error wins", paths: []string{"/first", "../second"}, want: `invalid --allow-path "/first": path must be repo-relative`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateAllowedPaths(test.paths)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateAllowedPaths(%q) error = %v, want %q", test.paths, err, test.want)
			}
		})
	}
}

func TestValidateAllowedPathsAcceptsDocumentedForms(t *testing.T) {
	if err := ValidateAllowedPaths([]string{"README.md", "docs/", "./internal/", "."}); err != nil {
		t.Fatalf("documented allow paths rejected: %v", err)
	}
}
