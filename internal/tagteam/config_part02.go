package tagteam

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/shlex"
)

func mergeRoleLossPolicies(dst *RoleLossPolicies, src RoleLossPolicies) {
	if src.Worker != "" {
		dst.Worker = src.Worker
	}
	if src.Reviewer != "" {
		dst.Reviewer = src.Reviewer
	}
	if src.Supervisor != "" {
		dst.Supervisor = src.Supervisor
	}
	if src.Scout != "" {
		dst.Scout = src.Scout
	}
}

func mergeRoleFallbacks(dst *RoleFallbacks, src RoleFallbacks) {
	if len(src.Worker) > 0 {
		dst.Worker = append([]string{}, src.Worker...)
	}
	if len(src.Reviewer) > 0 {
		dst.Reviewer = append([]string{}, src.Reviewer...)
	}
	if len(src.Supervisor) > 0 {
		dst.Supervisor = append([]string{}, src.Supervisor...)
	}
	if len(src.Scout) > 0 {
		dst.Scout = append([]string{}, src.Scout...)
	}
}

func mergeTargetFallbacks(dst *TargetFallbacks, src TargetFallbacks) {
	if len(src) == 0 {
		return
	}
	if *dst == nil {
		*dst = TargetFallbacks{}
	}
	for primary, fallbacks := range src {
		(*dst)[primary] = append([]string{}, fallbacks...)
	}
}

func cloneIntPtr(src *int) *int {
	if src == nil {
		return nil
	}
	value := *src
	return &value
}

func hasTagteamEnv(overlay map[string]string) bool {
	for _, key := range []string{
		"TAGTEAM_MODE",
		"TAGTEAM_CODER",
		"TAGTEAM_RELAY_CODER",
		"TAGTEAM_ADVERSARY",
		"TAGTEAM_WORKER",
		"TAGTEAM_SCOUT",
		"TAGTEAM_SCOUT_MODE",
		"TAGTEAM_POST_SCOUT_MODE",
		"TAGTEAM_SCOUT_FAILURE_POLICY",
		"TAGTEAM_SCOUT_LOSS_POLICY",
		"TAGTEAM_REVIEWER_LOSS_POLICY",
		"TAGTEAM_SUPERVISOR_LOSS_POLICY",
		"TAGTEAM_SCOUT_RETRIEVAL",
		"TAGTEAM_CODE_INTEL_COMMAND",
		"TAGTEAM_CODE_INTEL_COMMAND_CODEBASE_MEMORY",
		"TAGTEAM_CODE_INTEL_COMMAND_GITNEXUS",
		"TAGTEAM_CODE_INTEL_TIMEOUT",
		"TAGTEAM_SCOUT_CONTEXT_POLICY",
		"TAGTEAM_SUPERVISOR",
		"TAGTEAM_SUPERVISOR_SLICING",
		"TAGTEAM_MAX_PACKAGES",
		"TAGTEAM_PACKAGE",
		"TAGTEAM_AUTO_NEXT_PACKAGE",
		"TAGTEAM_RESPECT_REPO_INSTRUCTIONS",
		"TAGTEAM_DECISION_MEMORY",
		"TAGTEAM_MAX_FINDINGS",
		"TAGTEAM_MAX_OUTPUT_BYTES",
		"TAGTEAM_MAX_WALL_TIME",
		"TAGTEAM_MAX_ROLE_INVOCATIONS",
		"TAGTEAM_JSON_REPAIR",
		"TAGTEAM_ROUNDS",
		"TAGTEAM_TEST",
		"TAGTEAM_LINT",
		"TAGTEAM_TEST_IDENTITY_REGEX",
		"TAGTEAM_GIT_SAFETY",
		"TAGTEAM_CODEX_ARGS",
		"TAGTEAM_CODEX_REASONING_EFFORT",
		"TAGTEAM_CLAUDE_ARGS",
		"TAGTEAM_CLAUDE_EFFORT",
		"TAGTEAM_CLAUDE_SERIALIZE",
		"TAGTEAM_AGY_ARGS",
		"TAGTEAM_GOSLING_ARGS",
		"TAGTEAM_OPENAI_COMPATIBLE_BASE_URL",
		"TAGTEAM_OPENAI_COMPATIBLE_API_KEY_ENV",
		"TAGTEAM_OPENAI_COMPATIBLE_MODEL",
		"TAGTEAM_OPENAI_COMPATIBLE_MAX_CONTEXT_TOKENS",
		"TAGTEAM_OPENAI_COMPATIBLE_RESERVED_OUTPUT_TOKENS",
		"TAGTEAM_OPENAI_COMPATIBLE_HEADERS",
		"TAGTEAM_OPENAI_COMPATIBLE_ARGS",
	} {
		if overlay != nil {
			if _, ok := overlay[key]; ok {
				return true
			}
		} else {
			if _, ok := os.LookupEnv(key); ok {
				return true
			}
		}
	}
	return false
}

func mergeEnvConfig(cfg *Config, overlay map[string]string) {
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_STATE_ROOT"); ok {
		cfg.Defaults.StateRoot = value
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_WATCHDOG_TIMEOUT"); ok {
		cfg.Defaults.WatchdogTimeout = value
	}
	legacyRoleEnvSet := false
	coderEnvSet := false
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_CODER"); ok {
		cfg.Defaults.Coder = value
		legacyRoleEnvSet = true
		coderEnvSet = true
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_RELAY_CODER"); ok {
		cfg.Defaults.RelayCoder = value
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_ADVERSARY"); ok {
		cfg.Defaults.Adversary = value
		legacyRoleEnvSet = true
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_WORKER"); ok {
		cfg.Defaults.Worker = value
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_SCOUT"); ok {
		cfg.Defaults.Scout = value
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_SCOUT_MODE"); ok {
		cfg.Defaults.ScoutMode = value
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_POST_SCOUT_MODE"); ok {
		cfg.Defaults.PostScoutMode = value
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_SCOUT_FAILURE_POLICY"); ok {
		cfg.Defaults.ScoutFailurePolicy = value
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_SCOUT_LOSS_POLICY"); ok {
		cfg.Defaults.LossPolicy.Scout = LossPolicy(value)
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_WORKER_LOSS_POLICY"); ok {
		cfg.Defaults.LossPolicy.Worker = LossPolicy(value)
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_REVIEWER_LOSS_POLICY"); ok {
		cfg.Defaults.LossPolicy.Reviewer = LossPolicy(value)
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_SUPERVISOR_LOSS_POLICY"); ok {
		cfg.Defaults.LossPolicy.Supervisor = LossPolicy(value)
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_SCOUT_RETRIEVAL"); ok {
		if parsed, err := strconv.ParseBool(value); err == nil {
			cfg.Defaults.ScoutRetrieval = &parsed
		}
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_CODE_INTEL_COMMAND"); ok {
		cfg.Defaults.CodeIntelCommand = value
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_CODE_INTEL_COMMAND_CODEBASE_MEMORY"); ok {
		if cfg.CodeIntel.Providers == nil {
			cfg.CodeIntel.Providers = map[string]CodeIntelProviderConfig{}
		}
		cfg.CodeIntel.Providers["codebase-memory"] = CodeIntelProviderConfig{Command: value}
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_CODE_INTEL_COMMAND_GITNEXUS"); ok {
		if cfg.CodeIntel.Providers == nil {
			cfg.CodeIntel.Providers = map[string]CodeIntelProviderConfig{}
		}
		cfg.CodeIntel.Providers["gitnexus"] = CodeIntelProviderConfig{Command: value}
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_CODE_INTEL_TIMEOUT"); ok {
		cfg.CodeIntel.Timeout = value
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_SCOUT_CONTEXT_POLICY"); ok {
		cfg.Defaults.ScoutContextPolicy = value
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_SUPERVISOR"); ok {
		cfg.Defaults.Supervisor = value
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_SUPERVISOR_SLICING"); ok {
		if parsed, err := strconv.ParseBool(value); err == nil {
			cfg.Defaults.SupervisorSlicing = &parsed
		}
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_MAX_PACKAGES"); ok {
		if maxPackages, err := strconv.Atoi(value); err == nil && maxPackages > 0 {
			cfg.Defaults.MaxPackages = maxPackages
		}
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_PACKAGE"); ok {
		cfg.Defaults.Package = value
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_AUTO_NEXT_PACKAGE"); ok {
		if parsed, err := strconv.ParseBool(value); err == nil {
			cfg.Defaults.AutoNextPackage = &parsed
		}
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_RESPECT_REPO_INSTRUCTIONS"); ok {
		if parsed, err := strconv.ParseBool(value); err == nil {
			cfg.Defaults.RespectRepoInstructions = &parsed
		}
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_DECISION_MEMORY"); ok {
		if parsed, err := strconv.ParseBool(value); err == nil {
			cfg.Defaults.DecisionMemory = &parsed
		}
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_MAX_FINDINGS"); ok {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			cfg.Defaults.MaxFindings = parsed
		}
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_MAX_OUTPUT_BYTES"); ok {
		if parsed, err := strconv.ParseInt(value, 10, 64); err == nil && parsed > 0 {
			cfg.Defaults.MaxOutputBytes = parsed
		}
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_MAX_WALL_TIME"); ok {
		cfg.Defaults.MaxWallTime = value
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_MAX_ROLE_INVOCATIONS"); ok {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			cfg.Defaults.MaxRoleInvocations = parsed
		}
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_JSON_REPAIR"); ok {
		cfg.Defaults.JSONRepair = value
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_MODE"); ok {
		cfg.Defaults.Mode = value
		if value == string(ModeRelay) && coderEnvSet {
			if _, relayCoderSet := envLookupNonEmpty(overlay, "TAGTEAM_RELAY_CODER"); !relayCoderSet {
				cfg.Defaults.RelayCoder = cfg.Defaults.Coder
			}
		}
	} else if legacyRoleEnvSet {
		// TAGTEAM_CODER/TAGTEAM_ADVERSARY predate TAGTEAM_MODE and the
		// supervisor default; keep them selecting adversarial mode instead
		// of being silently ignored now that default resolution reads
		// Defaults.Worker/Defaults.Supervisor.
		cfg.Defaults.Mode = string(ModeAdversarial)
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_ROUNDS"); ok {
		if rounds, err := strconv.Atoi(value); err == nil && rounds > 0 {
			cfg.Defaults.Rounds = rounds
		}
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_TEST"); ok {
		cfg.Defaults.Test = value
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_LINT"); ok {
		cfg.Defaults.Lint = value
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_TEST_IDENTITY_REGEX"); ok {
		cfg.Defaults.TestIdentityRegex = value
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_GIT_SAFETY"); ok {
		cfg.Defaults.GitSafety = value
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_CODEX_ARGS"); ok {
		if parsed, err := shlex.Split(value); err == nil {
			cfg.Adapters.Codex.ExtraArgs = parsed
		}
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_CODEX_REASONING_EFFORT"); ok {
		cfg.Adapters.Codex.ReasoningEffort = value
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_CLAUDE_ARGS"); ok {
		if parsed, err := shlex.Split(value); err == nil {
			cfg.Adapters.Claude.ExtraArgs = parsed
		}
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_CLAUDE_EFFORT"); ok {
		cfg.Adapters.Claude.Effort = value
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_CLAUDE_SERIALIZE"); ok {
		if parsed, err := strconv.ParseBool(value); err == nil {
			cfg.Adapters.Claude.Serialize = &parsed
		}
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_AGY_ARGS"); ok {
		if parsed, err := shlex.Split(value); err == nil {
			cfg.Adapters.Agy.ExtraArgs = parsed
		}
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_GOSLING_ARGS"); ok {
		if parsed, err := shlex.Split(value); err == nil {
			cfg.Adapters.Gosling.ExtraArgs = parsed
		}
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_GROK_ARGS"); ok {
		if parsed, err := shlex.Split(value); err == nil {
			cfg.Adapters.Grok.ExtraArgs = parsed
		}
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_GROK_MODEL"); ok {
		cfg.Adapters.Grok.DefaultModel = value
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_GROK_REASONING_EFFORT"); ok {
		cfg.Adapters.Grok.ReasoningEffort = value
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_OPENAI_COMPATIBLE_BASE_URL"); ok {
		cfg.Adapters.OpenAICompatible.BaseURL = value
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_OPENAI_COMPATIBLE_API_KEY_ENV"); ok {
		cfg.Adapters.OpenAICompatible.APIKeyEnv = value
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_OPENAI_COMPATIBLE_MODEL"); ok {
		cfg.Adapters.OpenAICompatible.DefaultModel = value
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_OPENAI_COMPATIBLE_MAX_CONTEXT_TOKENS"); ok {
		if parsed, err := strconv.Atoi(value); err == nil {
			cfg.Adapters.OpenAICompatible.MaxContextTokens = &parsed
		}
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_OPENAI_COMPATIBLE_RESERVED_OUTPUT_TOKENS"); ok {
		if parsed, err := strconv.Atoi(value); err == nil {
			cfg.Adapters.OpenAICompatible.ReservedOutputTokens = &parsed
		}
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_OPENAI_COMPATIBLE_HEADERS"); ok {
		cfg.Adapters.OpenAICompatible.ExtraHeaders = parseHeaderPairs(value)
	}
	if value, ok := envLookupNonEmpty(overlay, "TAGTEAM_OPENAI_COMPATIBLE_ARGS"); ok {
		if parsed, err := shlex.Split(value); err == nil {
			cfg.Adapters.OpenAICompatible.ExtraArgs = parsed
		}
	}
}

func validateConfig(cfg Config) error {
	check := func(name string, maxContextTokens, reservedOutputTokens *int) error {
		maxSet := maxContextTokens != nil
		reserved := 0
		if reservedOutputTokens != nil {
			reserved = *reservedOutputTokens
		}
		if maxSet && *maxContextTokens <= 0 {
			return fmt.Errorf("%s.max_context_tokens must be > 0 when set", name)
		}
		if reserved < 0 {
			return fmt.Errorf("%s.reserved_output_tokens must be >= 0", name)
		}
		if maxSet && *maxContextTokens-reserved <= 0 {
			return fmt.Errorf("%s usable context must be > 0", name)
		}
		return nil
	}
	for _, item := range []struct {
		name     string
		max      *int
		reserved *int
	}{
		{"adapters.codex", cfg.Adapters.Codex.MaxContextTokens, cfg.Adapters.Codex.ReservedOutputTokens},
		{"adapters.claude", cfg.Adapters.Claude.MaxContextTokens, cfg.Adapters.Claude.ReservedOutputTokens},
		{"adapters.codex-oss", cfg.Adapters.CodexOSS.MaxContextTokens, cfg.Adapters.CodexOSS.ReservedOutputTokens},
		{"adapters.agy", cfg.Adapters.Agy.MaxContextTokens, cfg.Adapters.Agy.ReservedOutputTokens},
		{"adapters.gosling", cfg.Adapters.Gosling.MaxContextTokens, cfg.Adapters.Gosling.ReservedOutputTokens},
		{"adapters.grok", cfg.Adapters.Grok.MaxContextTokens, cfg.Adapters.Grok.ReservedOutputTokens},
		{"adapters.openai_compatible", cfg.Adapters.OpenAICompatible.MaxContextTokens, cfg.Adapters.OpenAICompatible.ReservedOutputTokens},
	} {
		if err := check(item.name, item.max, item.reserved); err != nil {
			return err
		}
	}
	if err := validateChoice("adapters.codex.reasoning_effort", cfg.Adapters.Codex.ReasoningEffort, "none", "minimal", "low", "medium", "high", "xhigh"); err != nil {
		return err
	}
	if err := validateChoice("adapters.grok.reasoning_effort", cfg.Adapters.Grok.ReasoningEffort, "low", "medium", "high", "xhigh"); err != nil {
		return err
	}
	if err := validateChoice("adapters.claude.effort", cfg.Adapters.Claude.Effort, "low", "medium", "high", "xhigh", "max"); err != nil {
		return err
	}
	if err := validateTestPresets(cfg.TestPresets); err != nil {
		return err
	}
	return nil
}

// normalizeTestPresets trims keys/commands from trusted config and rewrites the
// registry under exact-match normalized names. Fail closed on empty commands,
// control characters, over-length keys, or collisions after trim.
func normalizeTestPresets(cfg *Config) error {
	if cfg == nil || len(cfg.TestPresets) == 0 {
		return nil
	}
	out := make(map[string]TestPresetConfig, len(cfg.TestPresets))
	for key, preset := range cfg.TestPresets {
		name := strings.TrimSpace(key)
		if name == "" || len(name) > controlMaxRoleBytes || containsControl(name) {
			return fmt.Errorf("test_presets key %q must be a non-empty identifier no longer than %d bytes", key, controlMaxRoleBytes)
		}
		if _, exists := out[name]; exists {
			return fmt.Errorf("test_presets: duplicate name %q after normalization", name)
		}
		command := strings.TrimSpace(preset.Command)
		if command == "" {
			return fmt.Errorf("test_presets.%s.command is required", name)
		}
		if containsControl(command) {
			return fmt.Errorf("test_presets.%s.command must not contain control characters", name)
		}
		identity := strings.TrimSpace(preset.IdentityRegex)
		if identity != "" {
			compiled, err := regexp.Compile(identity)
			if err != nil || compiled.NumSubexp() < 1 {
				return fmt.Errorf("test_presets.%s.identity_regex must compile and contain a capture group", name)
			}
		}
		out[name] = TestPresetConfig{Command: command, IdentityRegex: identity}
	}
	cfg.TestPresets = out
	return nil
}

// validateTestPresets requires registry entries already be normalized (exact-match
// keys, no case folding) so in-memory configs fail closed the same way as loaded ones.
func validateTestPresets(presets map[string]TestPresetConfig) error {
	for key, preset := range presets {
		if key == "" || strings.TrimSpace(key) != key || len(key) > controlMaxRoleBytes || containsControl(key) {
			return fmt.Errorf("test_presets key %q must be a normalized identifier no longer than %d bytes", key, controlMaxRoleBytes)
		}
		if preset.Command == "" || strings.TrimSpace(preset.Command) != preset.Command || containsControl(preset.Command) {
			return fmt.Errorf("test_presets.%s.command must be a non-empty normalized command", key)
		}
		if preset.IdentityRegex != "" {
			if strings.TrimSpace(preset.IdentityRegex) != preset.IdentityRegex {
				return fmt.Errorf("test_presets.%s.identity_regex must be normalized", key)
			}
			compiled, err := regexp.Compile(preset.IdentityRegex)
			if err != nil || compiled.NumSubexp() < 1 {
				return fmt.Errorf("test_presets.%s.identity_regex must compile and contain a capture group", key)
			}
		}
	}
	return nil
}

func validateChoice(name, value string, choices ...string) error {
	if value == "" {
		return nil
	}
	for _, choice := range choices {
		if value == choice {
			return nil
		}
	}
	return fmt.Errorf("invalid %s %q (want %s)", name, value, strings.Join(choices, ", "))
}

func envLookup(overlay map[string]string, key string) (string, bool) {
	if value, ok := os.LookupEnv(key); ok {
		return value, true
	}
	if overlay != nil {
		value, ok := overlay[key]
		return value, ok
	}
	return "", false
}

func envLookupNonEmpty(overlay map[string]string, key string) (string, bool) {
	value, ok := envLookup(overlay, key)
	if !ok || value == "" {
		return "", false
	}
	return value, true
}
