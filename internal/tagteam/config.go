package tagteam

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

func mergeChurnThresholds(dst *ChurnThresholds, src ChurnThresholds) {
	if src.MaxFiles > 0 {
		dst.MaxFiles = src.MaxFiles
	}
	if src.MaxChangedLines > 0 {
		dst.MaxChangedLines = src.MaxChangedLines
	}
	if src.MaxFixtureFiles > 0 {
		dst.MaxFixtureFiles = src.MaxFixtureFiles
	}
	if src.WhitespaceRatio > 0 {
		dst.WhitespaceRatio = src.WhitespaceRatio
	}
	if src.MinimumSemanticRatio > 0 {
		dst.MinimumSemanticRatio = src.MinimumSemanticRatio
	}
}

func DefaultConfig() Config {
	supervisorSlicing := true
	autoNextPackage := false
	respectRepoInstructions := true
	decisionMemory := false
	scoutRetrieval := false
	stewardEnabled := false
	return Config{
		Defaults: DefaultsConfig{
			WatchdogTimeout:         "5m",
			Mode:                    "supervisor",
			Coder:                   defaultAdversarialCoderTarget,
			RelayCoder:              defaultRelayCoderTarget,
			Adversary:               defaultAdversaryTarget,
			Worker:                  defaultWorkerTarget,
			Scout:                   defaultRelayScoutTarget,
			Supervisor:              defaultSupervisorTarget,
			ScoutMode:               "recon",
			PostScoutMode:           "polish",
			ScoutFailurePolicy:      "continue",
			LossPolicy:              RoleLossPolicies{Worker: LossPolicyReplaceThenBlock, Reviewer: LossPolicyBlock, Supervisor: LossPolicyBlock, Scout: LossPolicyDegrade},
			Fallbacks:               RoleFallbacks{Worker: []string{defaultWorkerFallback}},
			ScoutRetrieval:          &scoutRetrieval,
			CodeIntelCommand:        "",
			ScoutContextPolicy:      "warn",
			SupervisorSlicing:       &supervisorSlicing,
			MaxPackages:             5,
			AutoNextPackage:         &autoNextPackage,
			RespectRepoInstructions: &respectRepoInstructions,
			DecisionMemory:          &decisionMemory,
			MaxFindings:             50,
			MaxOutputBytes:          2 * 1024 * 1024,
			// Keep a bounded total run lifetime even when each individual model
			// invocation is making observable progress.
			MaxWallTime:        "60m",
			MaxRoleInvocations: 0,
			JSONRepair:         "off",
			Rounds:             2,
			Churn: ChurnThresholds{
				MaxFiles:             25,
				MaxChangedLines:      1000,
				MaxFixtureFiles:      10,
				WhitespaceRatio:      0.60,
				MinimumSemanticRatio: 0.20,
			},
			GitSafety: "sync",
		},
		Profiles: map[string]ProfileConfig{
			"fast": {
				Mode:      "adversarial",
				Coder:     defaultAdversarialCoderTarget,
				Adversary: defaultWorkerTarget,
				Rounds:    1,
			},
			"paranoid": {
				Mode:      "adversarial",
				Adversary: defaultAdversaryTarget,
				Rounds:    4,
				Test:      "make check",
			},
			"relay": {
				Mode:               "relay",
				Scout:              defaultRelayScoutTarget,
				Coder:              defaultRelayCoderTarget,
				Supervisor:         defaultSupervisorTarget,
				ScoutMode:          "recon",
				PostScoutMode:      "polish",
				ScoutFailurePolicy: "continue",
				LossPolicy:         RoleLossPolicies{Worker: LossPolicyReplaceThenBlock, Reviewer: LossPolicyBlock, Supervisor: LossPolicyBlock, Scout: LossPolicyDegrade},
				ScoutRetrieval:     &scoutRetrieval,
				CodeIntelCommand:   "",
				ScoutContextPolicy: "warn",
				Rounds:             2,
			},
			"claude-failover": {
				LossPolicy: RoleLossPolicies{
					Reviewer:   LossPolicyReplaceThenBlock,
					Supervisor: LossPolicyReplaceThenBlock,
				},
				FallbacksByTarget: TargetFallbacks{
					"claude:claude-opus-4-8": []string{defaultSupervisorFallback},
					"claude:claude-sonnet-5": []string{defaultAdversarialCoderTarget},
					"claude:opus":            []string{defaultSupervisorFallback},
					"claude:opus-4.8":        []string{defaultSupervisorFallback},
					"claude:sonnet":          []string{defaultAdversarialCoderTarget},
					"claude:sonnet-5":        []string{defaultAdversarialCoderTarget},
					"claude:haiku":           []string{defaultAdversarialCoderTarget},
					"claude:haiku-5":         []string{defaultAdversarialCoderTarget},
					"claude:haiku-4.8":       []string{defaultAdversarialCoderTarget},
					"claude:sonnet-4.5":      []string{defaultAdversarialCoderTarget},
				},
			},
		},
		Adapters: AdapterConfigSet{
			Codex: CodexConfig{
				DefaultModel:    "gpt-5.6-sol",
				ReasoningEffort: "high",
			},
			Claude: ClaudeConfig{
				DefaultModel:      "claude-sonnet-5",
				Effort:            "high",
				CoderAllowedTools: []string{"Edit", "Write", "Read", "Glob", "Grep", "Bash"},
				ExtraArgs:         []string{},
			},
			CodexOSS: CodexConfig{
				DefaultModel: "qwen3-coder",
			},
			Agy: AgyConfig{
				DefaultModel: agyGemini36FlashMedium,
				ExtraArgs:    []string{},
			},
			Gosling: GoslingConfig{
				DefaultModel: "",
				ExtraArgs:    []string{},
			},
			Grok: GrokConfig{
				DefaultModel:    "grok-4.5",
				ReasoningEffort: "high",
				ExtraArgs:       []string{},
			},
			OpenAICompatible: OpenAICompatibleConfig{
				BaseURL:      "http://127.0.0.1:11434/v1",
				DefaultModel: "gemma4:latest",
				ExtraHeaders: map[string]string{},
				ExtraArgs:    []string{},
			},
		},
		// The advisory Run Steward is opt-in. Disabled, the deterministic
		// template steward is the only tier, so a run never depends on a model.
		// The default tier points at a local OpenAI-compatible endpoint (Ollama)
		// with conservative per-run budgets that keep it below worker priority.
		Steward: StewardConfig{
			Enabled:            &stewardEnabled,
			BaseURL:            "http://127.0.0.1:11434/v1",
			Model:              "",
			TimeoutSeconds:     10,
			MaxCallsPerRun:     20,
			MinIntervalSeconds: 5,
		},
	}
}

type LoadConfigOptions struct {
	TrustRepoConfig bool
}

func LoadConfig(workdir string) (Config, []string, error) {
	return LoadConfigWithOptions(workdir, LoadConfigOptions{})
}

func LoadConfigWithOptions(workdir string, opts LoadConfigOptions) (Config, []string, error) {
	cfg := DefaultConfig()
	sources := []string{"built-in defaults"}

	dotEnv, loadedDotEnv, err := loadDotEnv(workdir)
	if err != nil {
		return Config{}, nil, err
	}
	if loadedDotEnv {
		cfg.EnvOverlay = dotEnv
	}

	userPath, err := userConfigPath()
	if err != nil {
		return Config{}, nil, err
	}
	if err := mergeConfigFile(&cfg, userPath); err != nil {
		return Config{}, nil, err
	}
	if fileExists(userPath) {
		sources = append(sources, userPath)
	}

	repoPath := filepath.Join(workdir, ".tagteam.toml")
	if err := mergeConfigFileWithOptions(&cfg, repoPath, mergeConfigFileOptions{RepoLocal: true, Trusted: opts.TrustRepoConfig}); err != nil {
		return Config{}, nil, err
	}
	if fileExists(repoPath) {
		source := repoPath
		if !opts.TrustRepoConfig {
			source += " (untrusted: high-authority keys ignored)"
		}
		sources = append(sources, source)
	}

	if err := mergeEnvConfig(&cfg, cfg.EnvOverlay); err != nil {
		return Config{}, nil, err
	}
	if err := normalizeConfigTestCommands(&cfg); err != nil {
		return Config{}, nil, err
	}
	if err := normalizeTestPresets(&cfg); err != nil {
		return Config{}, nil, err
	}
	if err := validateConfig(cfg); err != nil {
		return Config{}, nil, err
	}
	if loadedDotEnv {
		sources = append(sources, filepath.Join(workdir, ".env"))
	}
	if hasTagteamEnv(nil) {
		sources = append(sources, "TAGTEAM_* env")
	}

	return cfg, sources, nil
}

func loadDotEnv(workdir string) (map[string]string, bool, error) {
	path := filepath.Join(workdir, ".env")
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	values := map[string]string{}
	scanner := bufio.NewScanner(file)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		key, value, ok, err := parseDotEnvLine(path, lineNo, scanner.Text())
		if err != nil {
			return nil, false, err
		}
		if !ok {
			continue
		}
		// Shell exports are explicit user state and always win over the
		// repo-local convenience overlay.
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}
	return values, len(values) > 0, nil
}

func parseDotEnvLine(path string, lineNo int, raw string) (string, string, bool, error) {
	line := strings.TrimSpace(raw)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false, nil
	}
	if strings.HasPrefix(line, "export ") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
	}
	key, value, ok := strings.Cut(line, "=")
	if !ok {
		return "", "", false, fmt.Errorf("parse %s:%d: expected KEY=VALUE", path, lineNo)
	}
	key = strings.TrimSpace(key)
	if !validEnvName(key) {
		return "", "", false, fmt.Errorf("parse %s:%d: invalid environment variable name %q", path, lineNo, key)
	}
	value = strings.TrimSpace(stripDotEnvInlineComment(value))
	if value == "" {
		return key, "", true, nil
	}
	switch {
	case strings.HasPrefix(value, "'"):
		if !strings.HasSuffix(value, "'") || len(value) == 1 {
			return "", "", false, fmt.Errorf("parse %s:%d: unterminated single-quoted value", path, lineNo)
		}
		return key, value[1 : len(value)-1], true, nil
	case strings.HasPrefix(value, `"`):
		unquoted, err := strconv.Unquote(value)
		if err != nil {
			return "", "", false, fmt.Errorf("parse %s:%d: invalid double-quoted value: %w", path, lineNo, err)
		}
		return key, unquoted, true, nil
	default:
		return key, strings.TrimSpace(value), true, nil
	}
}

func validEnvName(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		if r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

func stripDotEnvInlineComment(value string) string {
	inSingle := false
	inDouble := false
	escaped := false
	for i, r := range value {
		if escaped {
			escaped = false
			continue
		}
		if inDouble && r == '\\' {
			escaped = true
			continue
		}
		switch r {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if !inSingle && !inDouble && (i == 0 || value[i-1] == ' ' || value[i-1] == '\t') {
				return value[:i]
			}
		}
	}
	return value
}

func userConfigPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config dir: %w", err)
	}
	return filepath.Join(base, "tagteam", "config.toml"), nil
}

func mergeConfigFile(dst *Config, path string) error {
	return mergeConfigFileWithOptions(dst, path, mergeConfigFileOptions{Trusted: true})
}

type mergeConfigFileOptions struct {
	RepoLocal bool
	Trusted   bool
}

func mergeConfigFileWithOptions(dst *Config, path string, opts mergeConfigFileOptions) error {
	if !fileExists(path) {
		return nil
	}
	var src Config
	if _, err := toml.DecodeFile(path, &src); err != nil {
		return fmt.Errorf("decode config %s: %w", path, err)
	}
	if opts.RepoLocal && !opts.Trusted {
		src = sanitizeUntrustedRepoConfig(src)
	}
	mergeConfig(dst, src)
	return nil
}

func sanitizeUntrustedRepoConfig(src Config) Config {
	src.Defaults.StateRoot = ""
	src.Defaults.WatchdogTimeout = ""
	src.Defaults.Test = ""
	src.Defaults.Tests = nil
	src.Defaults.Lint = ""
	src.Defaults.TestIdentityRegex = ""
	src.Defaults.GitSafety = ""
	src.Defaults.MaxOutputBytes = 0
	src.Defaults.MaxWallTime = ""
	src.Defaults.MaxRoleInvocations = 0
	src.Defaults.JSONRepair = ""
	src.Defaults.LossPolicy = RoleLossPolicies{}
	src.Defaults.Fallbacks = RoleFallbacks{}
	src.Defaults.FallbacksByTarget = nil
	src.Defaults.ScoutContextPolicy = ""
	src.Defaults.CodeIntelCommand = ""
	for name, profile := range src.Profiles {
		profile.StateRoot = ""
		profile.WatchdogTimeout = ""
		profile.Test = ""
		profile.Tests = nil
		profile.Lint = ""
		profile.TestIdentityRegex = ""
		profile.GitSafety = ""
		profile.MaxOutputBytes = 0
		profile.MaxWallTime = ""
		profile.MaxRoleInvocations = 0
		profile.JSONRepair = ""
		profile.LossPolicy = RoleLossPolicies{}
		profile.Fallbacks = RoleFallbacks{}
		profile.FallbacksByTarget = nil
		profile.ScoutContextPolicy = ""
		profile.CodeIntelCommand = ""
		src.Profiles[name] = profile
	}
	src.Adapters.Codex.ExtraArgs = nil
	src.Adapters.Claude.CoderAllowedTools = nil
	src.Adapters.Claude.ExtraArgs = nil
	src.Adapters.Claude.Bare = false
	src.Adapters.Claude.Serialize = nil
	src.Adapters.CodexOSS.ExtraArgs = nil
	src.Adapters.Agy.ExtraArgs = nil
	src.Adapters.Gosling.ExtraArgs = nil
	src.Adapters.Grok.ExtraArgs = nil
	src.Adapters.OpenAICompatible.BaseURL = ""
	src.Adapters.OpenAICompatible.APIKeyEnv = ""
	src.Adapters.OpenAICompatible.ExtraHeaders = nil
	src.Adapters.OpenAICompatible.ExtraArgs = nil
	// A steward endpoint and API-key selector cross a network/auth boundary.
	// Repo-local config may set them only after explicit trust.
	src.Steward = StewardConfig{}
	// Repo-local subprocesses and external bridge paths are authority-bearing.
	// They are accepted only with --trust-repo-config.
	src.CodeIntel = CodeIntelConfig{}
	// Named test presets resolve shell commands; never accept them from
	// untrusted repo config even when the name matches a trusted preset.
	src.TestPresets = nil
	return src
}

func mergeConfig(dst *Config, src Config) {
	if src.Defaults.StateRoot != "" {
		dst.Defaults.StateRoot = src.Defaults.StateRoot
	}
	if src.Defaults.WatchdogTimeout != "" {
		dst.Defaults.WatchdogTimeout = src.Defaults.WatchdogTimeout
	}
	legacyDefaultsOnly := src.Defaults.Mode == "" &&
		(src.Defaults.Coder != "" || src.Defaults.Adversary != "") &&
		src.Defaults.Worker == "" &&
		src.Defaults.Supervisor == ""
	if legacyDefaultsOnly {
		dst.Defaults.Mode = string(ModeAdversarial)
	}
	if src.Defaults.Mode != "" {
		dst.Defaults.Mode = src.Defaults.Mode
	}
	if src.Defaults.Coder != "" {
		dst.Defaults.Coder = src.Defaults.Coder
	}
	if src.Defaults.RelayCoder != "" {
		dst.Defaults.RelayCoder = src.Defaults.RelayCoder
	} else if src.Defaults.Mode == string(ModeRelay) && src.Defaults.Coder != "" {
		// Before relay_coder existed, relay configurations used coder for this
		// slot. Preserve that meaning when loading an older config file.
		dst.Defaults.RelayCoder = src.Defaults.Coder
	}
	if src.Defaults.Adversary != "" {
		dst.Defaults.Adversary = src.Defaults.Adversary
	}
	if src.Defaults.Worker != "" {
		dst.Defaults.Worker = src.Defaults.Worker
	}
	if src.Defaults.Scout != "" {
		dst.Defaults.Scout = src.Defaults.Scout
	}
	if src.Defaults.Supervisor != "" {
		dst.Defaults.Supervisor = src.Defaults.Supervisor
	}
	if src.Defaults.ScoutMode != "" {
		dst.Defaults.ScoutMode = src.Defaults.ScoutMode
	}
	if src.Defaults.PostScoutMode != "" {
		dst.Defaults.PostScoutMode = src.Defaults.PostScoutMode
	}
	if src.Defaults.ScoutFailurePolicy != "" {
		dst.Defaults.ScoutFailurePolicy = src.Defaults.ScoutFailurePolicy
	}
	mergeRoleLossPolicies(&dst.Defaults.LossPolicy, src.Defaults.LossPolicy)
	mergeRoleFallbacks(&dst.Defaults.Fallbacks, src.Defaults.Fallbacks)
	mergeTargetFallbacks(&dst.Defaults.FallbacksByTarget, src.Defaults.FallbacksByTarget)
	if src.Defaults.ScoutRetrieval != nil {
		dst.Defaults.ScoutRetrieval = src.Defaults.ScoutRetrieval
	}
	if src.Defaults.CodeIntelCommand != "" {
		dst.Defaults.CodeIntelCommand = src.Defaults.CodeIntelCommand
	}
	mergeCodeIntelConfig(&dst.CodeIntel, src.CodeIntel)
	if src.Defaults.ScoutContextPolicy != "" {
		dst.Defaults.ScoutContextPolicy = src.Defaults.ScoutContextPolicy
	}
	if src.Defaults.SupervisorSlicing != nil {
		dst.Defaults.SupervisorSlicing = src.Defaults.SupervisorSlicing
	}
	if src.Defaults.MaxPackages != 0 {
		dst.Defaults.MaxPackages = src.Defaults.MaxPackages
	}
	if src.Defaults.Package != "" {
		dst.Defaults.Package = src.Defaults.Package
	}
	if src.Defaults.AutoNextPackage != nil {
		dst.Defaults.AutoNextPackage = src.Defaults.AutoNextPackage
	}
	if src.Defaults.RespectRepoInstructions != nil {
		dst.Defaults.RespectRepoInstructions = src.Defaults.RespectRepoInstructions
	}
	if src.Defaults.DecisionMemory != nil {
		dst.Defaults.DecisionMemory = src.Defaults.DecisionMemory
	}
	if src.Defaults.MaxFindings != 0 {
		dst.Defaults.MaxFindings = src.Defaults.MaxFindings
	}
	if src.Defaults.MaxOutputBytes != 0 {
		dst.Defaults.MaxOutputBytes = src.Defaults.MaxOutputBytes
	}
	if src.Defaults.MaxWallTime != "" {
		dst.Defaults.MaxWallTime = src.Defaults.MaxWallTime
	}
	if src.Defaults.MaxRoleInvocations != 0 {
		dst.Defaults.MaxRoleInvocations = src.Defaults.MaxRoleInvocations
	}
	if src.Defaults.JSONRepair != "" {
		dst.Defaults.JSONRepair = src.Defaults.JSONRepair
	}
	if src.Defaults.Rounds != 0 {
		dst.Defaults.Rounds = src.Defaults.Rounds
	}
	if src.Defaults.Tests != nil {
		dst.Defaults.Tests = append([]string(nil), src.Defaults.Tests...)
		dst.Defaults.Test = ""
	} else if src.Defaults.Test != "" {
		dst.Defaults.Test = src.Defaults.Test
		dst.Defaults.Tests = nil
	}
	if src.Defaults.Lint != "" {
		dst.Defaults.Lint = src.Defaults.Lint
	}
	if src.Defaults.TestIdentityRegex != "" {
		dst.Defaults.TestIdentityRegex = src.Defaults.TestIdentityRegex
	}
	mergeChurnThresholds(&dst.Defaults.Churn, src.Defaults.Churn)
	if src.Defaults.GitSafety != "" {
		dst.Defaults.GitSafety = src.Defaults.GitSafety
	}
	if src.Profiles != nil {
		if dst.Profiles == nil {
			dst.Profiles = map[string]ProfileConfig{}
		}
		for key, profile := range src.Profiles {
			current := dst.Profiles[key]
			if profile.StateRoot != "" {
				current.StateRoot = profile.StateRoot
			}
			if profile.WatchdogTimeout != "" {
				current.WatchdogTimeout = profile.WatchdogTimeout
			}
			if profile.Mode != "" {
				current.Mode = profile.Mode
			}
			if profile.Coder != "" {
				current.Coder = profile.Coder
			}
			if profile.Adversary != "" {
				current.Adversary = profile.Adversary
			}
			if profile.Worker != "" {
				current.Worker = profile.Worker
			}
			if profile.Scout != "" {
				current.Scout = profile.Scout
			}
			if profile.Supervisor != "" {
				current.Supervisor = profile.Supervisor
			}
			if profile.ScoutMode != "" {
				current.ScoutMode = profile.ScoutMode
			}
			if profile.PostScoutMode != "" {
				current.PostScoutMode = profile.PostScoutMode
			}
			if profile.ScoutFailurePolicy != "" {
				current.ScoutFailurePolicy = profile.ScoutFailurePolicy
			}
			mergeRoleLossPolicies(&current.LossPolicy, profile.LossPolicy)
			mergeRoleFallbacks(&current.Fallbacks, profile.Fallbacks)
			mergeTargetFallbacks(&current.FallbacksByTarget, profile.FallbacksByTarget)
			if profile.ScoutRetrieval != nil {
				current.ScoutRetrieval = profile.ScoutRetrieval
			}
			if profile.CodeIntelCommand != "" {
				current.CodeIntelCommand = profile.CodeIntelCommand
			}
			if profile.ScoutContextPolicy != "" {
				current.ScoutContextPolicy = profile.ScoutContextPolicy
			}
			if profile.SupervisorSlicing != nil {
				current.SupervisorSlicing = profile.SupervisorSlicing
			}
			if profile.MaxPackages != 0 {
				current.MaxPackages = profile.MaxPackages
			}
			if profile.Package != "" {
				current.Package = profile.Package
			}
			if profile.AutoNextPackage != nil {
				current.AutoNextPackage = profile.AutoNextPackage
			}
			if profile.RespectRepoInstructions != nil {
				current.RespectRepoInstructions = profile.RespectRepoInstructions
			}
			if profile.DecisionMemory != nil {
				current.DecisionMemory = profile.DecisionMemory
			}
			if profile.MaxFindings != 0 {
				current.MaxFindings = profile.MaxFindings
			}
			if profile.MaxOutputBytes != 0 {
				current.MaxOutputBytes = profile.MaxOutputBytes
			}
			if profile.MaxWallTime != "" {
				current.MaxWallTime = profile.MaxWallTime
			}
			if profile.MaxRoleInvocations != 0 {
				current.MaxRoleInvocations = profile.MaxRoleInvocations
			}
			if profile.JSONRepair != "" {
				current.JSONRepair = profile.JSONRepair
			}
			if profile.Rounds != 0 {
				current.Rounds = profile.Rounds
			}
			if profile.Tests != nil {
				current.Tests = append([]string(nil), profile.Tests...)
				current.Test = ""
			} else if profile.Test != "" {
				current.Test = profile.Test
				current.Tests = nil
			}
			if profile.Lint != "" {
				current.Lint = profile.Lint
			}
			if profile.TestIdentityRegex != "" {
				current.TestIdentityRegex = profile.TestIdentityRegex
			}
			if profile.GitSafety != "" {
				current.GitSafety = profile.GitSafety
			}
			mergeChurnThresholds(&current.Churn, profile.Churn)
			dst.Profiles[key] = current
		}
	}
	if src.Adapters.Codex.DefaultModel != "" {
		dst.Adapters.Codex.DefaultModel = src.Adapters.Codex.DefaultModel
	}
	if src.Adapters.Codex.ReasoningEffort != "" {
		dst.Adapters.Codex.ReasoningEffort = src.Adapters.Codex.ReasoningEffort
	}
	if len(src.Adapters.Codex.ExtraArgs) > 0 {
		dst.Adapters.Codex.ExtraArgs = append([]string{}, src.Adapters.Codex.ExtraArgs...)
	}
	mergeContextBudget(&dst.Adapters.Codex.MaxContextTokens, &dst.Adapters.Codex.ReservedOutputTokens, src.Adapters.Codex.MaxContextTokens, src.Adapters.Codex.ReservedOutputTokens)
	if src.Adapters.Claude.DefaultModel != "" {
		dst.Adapters.Claude.DefaultModel = src.Adapters.Claude.DefaultModel
	}
	if src.Adapters.Claude.Effort != "" {
		dst.Adapters.Claude.Effort = src.Adapters.Claude.Effort
	}
	if len(src.Adapters.Claude.CoderAllowedTools) > 0 {
		dst.Adapters.Claude.CoderAllowedTools = append([]string{}, src.Adapters.Claude.CoderAllowedTools...)
	}
	if len(src.Adapters.Claude.ExtraArgs) > 0 {
		dst.Adapters.Claude.ExtraArgs = append([]string{}, src.Adapters.Claude.ExtraArgs...)
	}
	if src.Adapters.Claude.Bare {
		dst.Adapters.Claude.Bare = true
	}
	if src.Adapters.Claude.Serialize != nil {
		value := *src.Adapters.Claude.Serialize
		dst.Adapters.Claude.Serialize = &value
	}
	mergeContextBudget(&dst.Adapters.Claude.MaxContextTokens, &dst.Adapters.Claude.ReservedOutputTokens, src.Adapters.Claude.MaxContextTokens, src.Adapters.Claude.ReservedOutputTokens)
	if src.Adapters.CodexOSS.DefaultModel != "" {
		dst.Adapters.CodexOSS.DefaultModel = src.Adapters.CodexOSS.DefaultModel
	}
	if len(src.Adapters.CodexOSS.ExtraArgs) > 0 {
		dst.Adapters.CodexOSS.ExtraArgs = append([]string{}, src.Adapters.CodexOSS.ExtraArgs...)
	}
	mergeContextBudget(&dst.Adapters.CodexOSS.MaxContextTokens, &dst.Adapters.CodexOSS.ReservedOutputTokens, src.Adapters.CodexOSS.MaxContextTokens, src.Adapters.CodexOSS.ReservedOutputTokens)
	if src.Adapters.Agy.DefaultModel != "" {
		dst.Adapters.Agy.DefaultModel = src.Adapters.Agy.DefaultModel
	}
	if len(src.Adapters.Agy.ExtraArgs) > 0 {
		dst.Adapters.Agy.ExtraArgs = append([]string{}, src.Adapters.Agy.ExtraArgs...)
	}
	mergeContextBudget(&dst.Adapters.Agy.MaxContextTokens, &dst.Adapters.Agy.ReservedOutputTokens, src.Adapters.Agy.MaxContextTokens, src.Adapters.Agy.ReservedOutputTokens)
	if src.Adapters.Gosling.DefaultModel != "" {
		dst.Adapters.Gosling.DefaultModel = src.Adapters.Gosling.DefaultModel
	}
	if len(src.Adapters.Gosling.ExtraArgs) > 0 {
		dst.Adapters.Gosling.ExtraArgs = append([]string{}, src.Adapters.Gosling.ExtraArgs...)
	}
	mergeContextBudget(&dst.Adapters.Gosling.MaxContextTokens, &dst.Adapters.Gosling.ReservedOutputTokens, src.Adapters.Gosling.MaxContextTokens, src.Adapters.Gosling.ReservedOutputTokens)
	if src.Adapters.Grok.DefaultModel != "" {
		dst.Adapters.Grok.DefaultModel = src.Adapters.Grok.DefaultModel
	}
	if src.Adapters.Grok.ReasoningEffort != "" {
		dst.Adapters.Grok.ReasoningEffort = src.Adapters.Grok.ReasoningEffort
	}
	if len(src.Adapters.Grok.ExtraArgs) > 0 {
		dst.Adapters.Grok.ExtraArgs = append([]string{}, src.Adapters.Grok.ExtraArgs...)
	}
	mergeContextBudget(&dst.Adapters.Grok.MaxContextTokens, &dst.Adapters.Grok.ReservedOutputTokens, src.Adapters.Grok.MaxContextTokens, src.Adapters.Grok.ReservedOutputTokens)
	if src.Adapters.OpenAICompatible.BaseURL != "" {
		dst.Adapters.OpenAICompatible.BaseURL = src.Adapters.OpenAICompatible.BaseURL
	}
	if src.Adapters.OpenAICompatible.APIKeyEnv != "" {
		dst.Adapters.OpenAICompatible.APIKeyEnv = src.Adapters.OpenAICompatible.APIKeyEnv
	}
	if src.Adapters.OpenAICompatible.DefaultModel != "" {
		dst.Adapters.OpenAICompatible.DefaultModel = src.Adapters.OpenAICompatible.DefaultModel
	}
	mergeContextBudget(&dst.Adapters.OpenAICompatible.MaxContextTokens, &dst.Adapters.OpenAICompatible.ReservedOutputTokens, src.Adapters.OpenAICompatible.MaxContextTokens, src.Adapters.OpenAICompatible.ReservedOutputTokens)
	if len(src.Adapters.OpenAICompatible.ExtraHeaders) > 0 {
		dst.Adapters.OpenAICompatible.ExtraHeaders = cloneStringMap(src.Adapters.OpenAICompatible.ExtraHeaders)
	}
	if len(src.Adapters.OpenAICompatible.ExtraArgs) > 0 {
		dst.Adapters.OpenAICompatible.ExtraArgs = append([]string{}, src.Adapters.OpenAICompatible.ExtraArgs...)
	}
	mergeStewardConfig(&dst.Steward, src.Steward)
	if src.TestPresets != nil {
		if dst.TestPresets == nil {
			dst.TestPresets = map[string]TestPresetConfig{}
		}
		for key, preset := range src.TestPresets {
			// Later trusted layers fully replace an earlier entry with the same key.
			dst.TestPresets[key] = preset
		}
	}
}

func mergeStewardConfig(dst *StewardConfig, src StewardConfig) {
	if src.Enabled != nil {
		enabled := *src.Enabled
		dst.Enabled = &enabled
	}
	if src.BaseURL != "" {
		dst.BaseURL = src.BaseURL
	}
	if src.APIKeyEnv != "" {
		dst.APIKeyEnv = src.APIKeyEnv
	}
	if src.Model != "" {
		dst.Model = src.Model
	}
	if src.TimeoutSeconds != 0 {
		dst.TimeoutSeconds = src.TimeoutSeconds
	}
	if src.MaxCallsPerRun != 0 {
		dst.MaxCallsPerRun = src.MaxCallsPerRun
	}
	if src.MinIntervalSeconds != 0 {
		dst.MinIntervalSeconds = src.MinIntervalSeconds
	}
}

func mergeContextBudget(dstMax, dstReserved **int, srcMax, srcReserved *int) {
	if srcMax != nil {
		*dstMax = cloneIntPtr(srcMax)
	}
	if srcReserved != nil {
		*dstReserved = cloneIntPtr(srcReserved)
	}
}
