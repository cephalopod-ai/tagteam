package tagteam

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/google/shlex"
)

func DefaultConfig() Config {
	supervisorSlicing := true
	autoNextPackage := false
	respectRepoInstructions := true
	decisionMemory := false
	scoutRetrieval := true
	return Config{
		Defaults: DefaultsConfig{
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
			LossPolicy:              RoleLossPolicies{Reviewer: LossPolicyBlock, Supervisor: LossPolicyBlock, Scout: LossPolicyDegrade},
			ScoutRetrieval:          &scoutRetrieval,
			ScoutContextPolicy:      "warn",
			SupervisorSlicing:       &supervisorSlicing,
			MaxPackages:             5,
			AutoNextPackage:         &autoNextPackage,
			RespectRepoInstructions: &respectRepoInstructions,
			DecisionMemory:          &decisionMemory,
			MaxFindings:             50,
			MaxOutputBytes:          2 * 1024 * 1024,
			MaxWallTime:             "0s",
			MaxRoleInvocations:      0,
			JSONRepair:              "off",
			Rounds:                  2,
			GitSafety:               "clean",
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
				LossPolicy:         RoleLossPolicies{Reviewer: LossPolicyBlock, Supervisor: LossPolicyBlock, Scout: LossPolicyDegrade},
				ScoutRetrieval:     &scoutRetrieval,
				ScoutContextPolicy: "warn",
				Rounds:             2,
			},
			"claude-failover": {
				LossPolicy: RoleLossPolicies{
					Reviewer:   LossPolicyReplaceThenBlock,
					Supervisor: LossPolicyReplaceThenBlock,
				},
				FallbacksByTarget: TargetFallbacks{
					"claude:claude-opus-4-8": []string{defaultSupervisorTarget},
					"claude:claude-sonnet-5": []string{defaultAdversarialCoderTarget},
					"claude:opus":            []string{defaultSupervisorTarget},
					"claude:opus-4.8":        []string{defaultSupervisorTarget},
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
				DefaultModel: "Gemini 3.5 Flash (Medium)",
				ExtraArgs:    []string{},
			},
			Gosling: GoslingConfig{
				DefaultModel: "",
				ExtraArgs:    []string{},
			},
			OpenAICompatible: OpenAICompatibleConfig{
				ExtraHeaders: map[string]string{},
				ExtraArgs:    []string{},
			},
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

	mergeEnvConfig(&cfg, cfg.EnvOverlay)
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
	src.Defaults.Test = ""
	src.Defaults.GitSafety = ""
	src.Defaults.MaxOutputBytes = 0
	src.Defaults.MaxWallTime = ""
	src.Defaults.MaxRoleInvocations = 0
	src.Defaults.JSONRepair = ""
	src.Defaults.LossPolicy = RoleLossPolicies{}
	src.Defaults.Fallbacks = RoleFallbacks{}
	src.Defaults.FallbacksByTarget = nil
	src.Defaults.ScoutContextPolicy = ""
	for name, profile := range src.Profiles {
		profile.Test = ""
		profile.MaxOutputBytes = 0
		profile.MaxWallTime = ""
		profile.MaxRoleInvocations = 0
		profile.JSONRepair = ""
		profile.LossPolicy = RoleLossPolicies{}
		profile.Fallbacks = RoleFallbacks{}
		profile.FallbacksByTarget = nil
		profile.ScoutContextPolicy = ""
		src.Profiles[name] = profile
	}
	src.Adapters.Codex.ExtraArgs = nil
	src.Adapters.Claude.CoderAllowedTools = nil
	src.Adapters.Claude.ExtraArgs = nil
	src.Adapters.Claude.Bare = false
	src.Adapters.CodexOSS.ExtraArgs = nil
	src.Adapters.Agy.ExtraArgs = nil
	src.Adapters.Gosling.ExtraArgs = nil
	src.Adapters.OpenAICompatible.BaseURL = ""
	src.Adapters.OpenAICompatible.APIKeyEnv = ""
	src.Adapters.OpenAICompatible.ExtraHeaders = nil
	src.Adapters.OpenAICompatible.ExtraArgs = nil
	return src
}

func mergeConfig(dst *Config, src Config) {
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
	if src.Defaults.Test != "" {
		dst.Defaults.Test = src.Defaults.Test
	}
	if src.Defaults.GitSafety != "" {
		dst.Defaults.GitSafety = src.Defaults.GitSafety
	}
	if src.Profiles != nil {
		if dst.Profiles == nil {
			dst.Profiles = map[string]ProfileConfig{}
		}
		for key, profile := range src.Profiles {
			current := dst.Profiles[key]
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
			if profile.Test != "" {
				current.Test = profile.Test
			}
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
}

func mergeContextBudget(dstMax, dstReserved **int, srcMax, srcReserved *int) {
	if srcMax != nil {
		*dstMax = cloneIntPtr(srcMax)
	}
	if srcReserved != nil {
		*dstReserved = cloneIntPtr(srcReserved)
	}
}

func mergeRoleLossPolicies(dst *RoleLossPolicies, src RoleLossPolicies) {
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
		"TAGTEAM_GIT_SAFETY",
		"TAGTEAM_CODEX_ARGS",
		"TAGTEAM_CODEX_REASONING_EFFORT",
		"TAGTEAM_CLAUDE_ARGS",
		"TAGTEAM_CLAUDE_EFFORT",
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
		{"adapters.openai_compatible", cfg.Adapters.OpenAICompatible.MaxContextTokens, cfg.Adapters.OpenAICompatible.ReservedOutputTokens},
	} {
		if err := check(item.name, item.max, item.reserved); err != nil {
			return err
		}
	}
	if err := validateChoice("adapters.codex.reasoning_effort", cfg.Adapters.Codex.ReasoningEffort, "none", "minimal", "low", "medium", "high", "xhigh"); err != nil {
		return err
	}
	if err := validateChoice("adapters.claude.effort", cfg.Adapters.Claude.Effort, "low", "medium", "high", "xhigh", "max"); err != nil {
		return err
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

func ResolveOptions(cfg Config, sources []string, flags FlagInputs, changed map[string]bool, prompt string) (RunOptions, error) {
	if err := validateConfig(cfg); err != nil {
		return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: err}
	}
	modeRaw := cfg.Defaults.Mode
	rounds := cfg.Defaults.Rounds
	testCmd := cfg.Defaults.Test
	gitSafety := cfg.Defaults.GitSafety
	scoutMode := cfg.Defaults.ScoutMode
	postScoutMode := cfg.Defaults.PostScoutMode
	scoutFailurePolicy := cfg.Defaults.ScoutFailurePolicy
	lossPolicy := cfg.Defaults.LossPolicy
	fallbacks := cfg.Defaults.Fallbacks
	fallbacksByTarget := cloneTargetFallbacks(cfg.Defaults.FallbacksByTarget)
	scoutRetrieval := true
	if cfg.Defaults.ScoutRetrieval != nil {
		scoutRetrieval = *cfg.Defaults.ScoutRetrieval
	}
	scoutContextPolicy := cfg.Defaults.ScoutContextPolicy
	supervisorSlicing := true
	if cfg.Defaults.SupervisorSlicing != nil {
		supervisorSlicing = *cfg.Defaults.SupervisorSlicing
	}
	maxPackages := cfg.Defaults.MaxPackages
	packageID := cfg.Defaults.Package
	autoNextPackage := false
	if cfg.Defaults.AutoNextPackage != nil {
		autoNextPackage = *cfg.Defaults.AutoNextPackage
	}
	respectRepoInstructions := true
	if cfg.Defaults.RespectRepoInstructions != nil {
		respectRepoInstructions = *cfg.Defaults.RespectRepoInstructions
	}
	decisionMemory := false
	if cfg.Defaults.DecisionMemory != nil {
		decisionMemory = *cfg.Defaults.DecisionMemory
	}
	maxFindings := cfg.Defaults.MaxFindings
	maxOutputBytes := cfg.Defaults.MaxOutputBytes
	maxRoleInvocations := cfg.Defaults.MaxRoleInvocations
	jsonRepair := cfg.Defaults.JSONRepair
	var maxWallTime time.Duration
	if strings.TrimSpace(cfg.Defaults.MaxWallTime) != "" {
		parsed, err := time.ParseDuration(cfg.Defaults.MaxWallTime)
		if err != nil {
			return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("parse max_wall_time: %w", err)}
		}
		maxWallTime = parsed
	}

	var profile ProfileConfig
	hasProfile := false
	profileSetsMode := false
	if flags.Profile != "" {
		p, ok := cfg.Profiles[flags.Profile]
		if !ok {
			return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("unknown profile %q", flags.Profile)}
		}
		profile = p
		hasProfile = true
		switch {
		case profile.Mode != "":
			modeRaw = profile.Mode
			profileSetsMode = true
		case profile.Mode == "" && profile.Scout != "":
			modeRaw = string(ModeRelay)
			profileSetsMode = true
		case profile.Coder != "" || profile.Adversary != "":
			// Legacy profiles that only set coder/adversary predate the
			// mode field; keep them resolving as adversarial-mode profiles
			// instead of being silently ignored under the supervisor default.
			modeRaw = string(ModeAdversarial)
			profileSetsMode = true
		case profile.Worker != "" || profile.Supervisor != "":
			modeRaw = string(ModeSupervisor)
			profileSetsMode = true
		}
		if profile.Rounds != 0 {
			rounds = profile.Rounds
		}
		if profile.Test != "" {
			testCmd = profile.Test
		}
		if profile.ScoutMode != "" {
			scoutMode = profile.ScoutMode
		}
		if profile.PostScoutMode != "" {
			postScoutMode = profile.PostScoutMode
		}
		if profile.ScoutFailurePolicy != "" {
			scoutFailurePolicy = profile.ScoutFailurePolicy
		}
		mergeRoleLossPolicies(&lossPolicy, profile.LossPolicy)
		mergeRoleFallbacks(&fallbacks, profile.Fallbacks)
		mergeTargetFallbacks(&fallbacksByTarget, profile.FallbacksByTarget)
		if profile.ScoutRetrieval != nil {
			scoutRetrieval = *profile.ScoutRetrieval
		}
		if profile.ScoutContextPolicy != "" {
			scoutContextPolicy = profile.ScoutContextPolicy
		}
		if profile.SupervisorSlicing != nil {
			supervisorSlicing = *profile.SupervisorSlicing
		}
		if profile.MaxPackages != 0 {
			maxPackages = profile.MaxPackages
		}
		if profile.Package != "" {
			packageID = profile.Package
		}
		if profile.AutoNextPackage != nil {
			autoNextPackage = *profile.AutoNextPackage
		}
		if profile.RespectRepoInstructions != nil {
			respectRepoInstructions = *profile.RespectRepoInstructions
		}
		if profile.DecisionMemory != nil {
			decisionMemory = *profile.DecisionMemory
		}
		if profile.MaxFindings != 0 {
			maxFindings = profile.MaxFindings
		}
		if profile.MaxOutputBytes != 0 {
			maxOutputBytes = profile.MaxOutputBytes
		}
		if profile.MaxRoleInvocations != 0 {
			maxRoleInvocations = profile.MaxRoleInvocations
		}
		if profile.JSONRepair != "" {
			jsonRepair = profile.JSONRepair
		}
		if strings.TrimSpace(profile.MaxWallTime) != "" {
			parsed, err := time.ParseDuration(profile.MaxWallTime)
			if err != nil {
				return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("parse profile max_wall_time: %w", err)}
			}
			maxWallTime = parsed
		}
	}
	if changed["mode"] {
		modeRaw = flags.Mode
	}
	if changed["solo"] {
		modeRaw = string(ModeSolo)
	}
	// modeExplicit only reflects choices that actually pin the mode for
	// this invocation (a --mode flag, or a profile that sets `mode` or the
	// legacy coder/adversary keys). A profile that only overrides
	// rounds/test must not block Fix() from resuming a saved run's mode.
	modeExplicit := profileSetsMode || changed["mode"]
	mode, err := ParseMode(modeRaw)
	if err != nil {
		return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: err}
	}

	// editor is the "coder" (adversarial mode) / "worker" (supervisor mode)
	// role; reviewer is "adversary" / "supervisor" respectively.
	//
	// editorExplicit/reviewerExplicit mirror modeExplicit: they're only set
	// when the profile actually supplies the role key for the resolved
	// mode, not merely because a profile was selected. Otherwise
	// `tagteam fix --profile ...` would have its profile-derived mode and
	// targets silently overwritten by Fix's saved-run resume logic below
	// even when the profile left those targets untouched.
	if changed["relay"] && flags.Relay {
		modeRaw = string(ModeRelay)
		modeExplicit = true
		mode = ModeRelay
	}
	if changed["solo"] {
		modeExplicit = true
		mode = ModeSolo
	}

	targets := configuredTargetsForMode(cfg.Defaults, mode)
	editorRaw, reviewerRaw, scoutRaw := targets.Editor, targets.Reviewer, targets.Scout
	editorExplicit := false
	reviewerExplicit := false
	scoutExplicit := false
	editorExplicitMode := Mode("")
	reviewerExplicitMode := Mode("")
	scoutExplicitMode := Mode("")
	if mode == ModeSolo {
		if hasProfile && profile.Worker != "" {
			editorRaw = profile.Worker
			editorExplicit = true
			editorExplicitMode = ModeSolo
		}
	} else if mode == ModeAdversarial {
		if hasProfile {
			if profile.Coder != "" {
				editorRaw = profile.Coder
				editorExplicit = true
				editorExplicitMode = ModeAdversarial
			}
			if profile.Adversary != "" {
				reviewerRaw = profile.Adversary
				reviewerExplicit = true
				reviewerExplicitMode = ModeAdversarial
			}
		}
	} else if mode == ModeSupervisor {
		if hasProfile {
			if profile.Worker != "" {
				editorRaw = profile.Worker
				editorExplicit = true
				editorExplicitMode = ModeSupervisor
			}
			if profile.Supervisor != "" {
				reviewerRaw = profile.Supervisor
				reviewerExplicit = true
				reviewerExplicitMode = ModeSupervisor
			}
		}
	} else {
		if hasProfile {
			if profile.Coder != "" {
				editorRaw = profile.Coder
				editorExplicit = true
				editorExplicitMode = ModeRelay
			}
			if profile.Worker != "" {
				editorRaw = profile.Worker
				editorExplicit = true
				editorExplicitMode = ModeRelay
			}
			if profile.Supervisor != "" {
				reviewerRaw = profile.Supervisor
				reviewerExplicit = true
				reviewerExplicitMode = ModeRelay
			}
			if profile.Scout != "" {
				scoutRaw = profile.Scout
				scoutExplicit = true
				scoutExplicitMode = ModeRelay
			}
		}
	}

	// Legacy -mc/-ma flags always win over defaults/profile and are valid in
	// either mode. The newer --worker/--supervisor flags are the supervisor-
	// mode names for the same two slots, and --reviewer is the adversarial-
	// mode name for the reviewer slot; using one of these three outside its
	// matching mode is rejected rather than silently ignored or misapplied.
	if changed["mc"] {
		editorRaw = flags.Coder
		editorExplicit = true
		editorExplicitMode = ""
	}
	if changed["solo"] {
		editorRaw = flags.Solo
		editorExplicit = true
		editorExplicitMode = ModeSolo
	}
	if changed["worker"] {
		if mode != ModeSolo && mode != ModeSupervisor && mode != ModeRelay {
			return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("--worker is only valid in solo, supervisor, or relay mode (current mode %q); use -mc/--mc in adversarial mode", mode)}
		}
		editorRaw = flags.Worker
		editorExplicit = true
		editorExplicitMode = mode
	}
	if changed["coder"] {
		if mode != ModeAdversarial && mode != ModeRelay {
			return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("--coder is only valid in adversarial or relay mode (current mode %q); use --worker in supervisor mode", mode)}
		}
		editorRaw = flags.CoderRole
		editorExplicit = true
		editorExplicitMode = mode
	}
	if changed["ma"] {
		if mode == ModeSolo {
			return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("-ma/--ma is not valid in solo mode")}
		}
		reviewerRaw = flags.Adversary
		reviewerExplicit = true
		reviewerExplicitMode = ""
	}
	if changed["reviewer"] {
		if mode == ModeSolo {
			return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("--reviewer is not valid in solo mode")}
		}
		if mode != ModeAdversarial {
			return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("--reviewer is only valid in adversarial mode (current mode %q); use --supervisor in supervisor mode", mode)}
		}
		reviewerRaw = flags.Reviewer
		reviewerExplicit = true
		reviewerExplicitMode = ModeAdversarial
	}
	if changed["supervisor"] {
		if mode == ModeSolo {
			return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("--supervisor is not valid in solo mode")}
		}
		if mode != ModeSupervisor && mode != ModeRelay {
			return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("--supervisor is only valid in supervisor or relay mode (current mode %q); use --reviewer or -ma in adversarial mode", mode)}
		}
		reviewerRaw = flags.Supervisor
		reviewerExplicit = true
		reviewerExplicitMode = mode
	}
	if changed["scout"] {
		if mode == ModeSolo {
			return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("--scout is not valid in solo mode")}
		}
		if mode != ModeRelay {
			return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("--scout is only valid in relay mode (current mode %q)", mode)}
		}
		scoutRaw = flags.Scout
		scoutExplicit = true
		scoutExplicitMode = ModeRelay
	}
	if changed["scout-mode"] {
		scoutMode = flags.ScoutMode
	}
	if changed["post-scout-mode"] {
		postScoutMode = flags.PostScoutMode
	}
	if changed["strict-scout"] {
		scoutFailurePolicy = "fail"
		lossPolicy.Scout = LossPolicyBlock
	}
	if changed["no-scout-retrieval"] {
		scoutRetrieval = false
	}
	if changed["scout-context-policy"] {
		scoutContextPolicy = flags.ScoutContextPolicy
	}
	if changed["slice"] {
		supervisorSlicing = flags.Slice
	}
	if changed["no-slice"] {
		supervisorSlicing = false
	}
	if changed["max-packages"] {
		maxPackages = flags.MaxPackages
	}
	if changed["package"] {
		packageID = flags.Package
	}
	if changed["auto-next-package"] {
		autoNextPackage = flags.AutoNextPackage
	}
	if changed["respect-repo-instructions"] {
		respectRepoInstructions = flags.RespectRepoInstructions
	}
	if changed["no-repo-instructions"] {
		respectRepoInstructions = false
	}
	if changed["decision-memory"] {
		decisionMemory = flags.DecisionMemory
	}
	if changed["max-findings"] {
		maxFindings = flags.MaxFindings
	}
	if changed["max-output-bytes"] {
		maxOutputBytes = flags.MaxOutputBytes
	}
	if changed["max-wall-time"] {
		maxWallTime = flags.MaxWallTime
	}
	if changed["max-role-invocations"] {
		maxRoleInvocations = flags.MaxRoleInvocations
	}
	if changed["repair-json-with-worker"] {
		jsonRepair = "worker"
	}

	if changed["rounds"] {
		rounds = flags.Rounds
	}
	if changed["test"] {
		testCmd = flags.Test
	}
	if changed["allow-dirty"] {
		gitSafety = "allow-dirty"
	}
	if changed["autostash"] {
		gitSafety = "autostash"
	}
	if gitSafety != "clean" && gitSafety != "autostash" && gitSafety != "branch" && gitSafety != "allow-dirty" {
		return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("invalid git_safety %q", gitSafety)}
	}
	if scoutMode == "" {
		scoutMode = "recon"
	}
	if postScoutMode == "" {
		postScoutMode = "polish"
	}
	if scoutFailurePolicy == "" {
		scoutFailurePolicy = "continue"
	}
	if scoutFailurePolicy != "continue" && scoutFailurePolicy != "fail" {
		return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("invalid scout_failure_policy %q", scoutFailurePolicy)}
	}
	// Preserve the legacy scout_failure_policy knob by translating it to the
	// generic per-role loss policy unless a dedicated scout policy was set.
	if lossPolicy.Scout == "" {
		if scoutFailurePolicy == "fail" {
			lossPolicy.Scout = LossPolicyBlock
		} else {
			lossPolicy.Scout = LossPolicyDegrade
		}
	}
	if lossPolicy.Reviewer == "" {
		lossPolicy.Reviewer = LossPolicyBlock
	}
	if lossPolicy.Supervisor == "" {
		lossPolicy.Supervisor = LossPolicyBlock
	}
	if err := validateRoleLossPolicies(lossPolicy); err != nil {
		return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: err}
	}
	if scoutContextPolicy == "" {
		scoutContextPolicy = "warn"
	}
	if scoutContextPolicy != "warn" && scoutContextPolicy != "skip" && scoutContextPolicy != "block" {
		return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("invalid scout_context_policy %q", scoutContextPolicy)}
	}
	if mode == ModeRelay {
		if err := validateScoutMode("scout-mode", scoutMode); err != nil {
			return RunOptions{}, err
		}
		if err := validateScoutMode("post-scout-mode", postScoutMode); err != nil {
			return RunOptions{}, err
		}
	} else if changed["strict-scout"] {
		return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("--strict-scout is only valid in relay mode")}
	} else if changed["no-scout-retrieval"] {
		return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("--no-scout-retrieval is only valid in relay mode")}
	}
	if maxPackages <= 0 {
		return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("max-packages must be > 0")}
	}
	if maxPackages > 20 {
		return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("max-packages must be <= 20")}
	}
	if maxFindings <= 0 {
		return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("max-findings must be > 0")}
	}
	if maxOutputBytes <= 0 {
		return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("max-output-bytes must be > 0")}
	}
	if maxRoleInvocations < 0 {
		return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("max-role-invocations must be >= 0")}
	}
	if jsonRepair == "" {
		jsonRepair = "off"
	}
	if jsonRepair != "off" && jsonRepair != "worker" {
		return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("invalid json_repair %q (want off or worker)", jsonRepair)}
	}

	editorLabel, reviewerLabel := roleLabels(mode)
	editorTarget, err := ParseRoleTarget(editorRaw)
	if err != nil {
		return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("resolve %s target: %w", editorLabel, err)}
	}
	var reviewerTarget RoleTarget
	if mode != ModeSolo {
		reviewerTarget, err = ParseRoleTarget(reviewerRaw)
		if err != nil {
			return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("resolve %s target: %w", reviewerLabel, err)}
		}
	}
	var scoutTarget RoleTarget
	if mode == ModeRelay {
		scoutTarget, err = ParseRoleTarget(scoutRaw)
		if err != nil {
			return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("resolve scout target: %w", err)}
		}
	}

	codexArgs, err := mergePassthrough(cfg.Adapters.Codex.ExtraArgs, flags.CodexArgsRaw)
	if err != nil {
		return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("parse --codex-args: %w", err)}
	}
	claudeArgs, err := mergePassthrough(cfg.Adapters.Claude.ExtraArgs, flags.ClaudeArgsRaw)
	if err != nil {
		return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("parse --claude-args: %w", err)}
	}
	agyArgs, err := mergePassthrough(cfg.Adapters.Agy.ExtraArgs, flags.AgyArgsRaw)
	if err != nil {
		return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("parse --agy-args: %w", err)}
	}
	goslingArgs, err := mergePassthrough(cfg.Adapters.Gosling.ExtraArgs, flags.GoslingArgsRaw)
	if err != nil {
		return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("parse --gosling-args: %w", err)}
	}
	openAICompatibleArgs, err := mergePassthrough(cfg.Adapters.OpenAICompatible.ExtraArgs, flags.OpenAICompatibleArgsRaw)
	if err != nil {
		return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("parse --openai-compatible-args: %w", err)}
	}

	workdir := flags.Workdir
	if workdir == "" {
		workdir = "."
	}
	workdir, err = filepath.Abs(workdir)
	if err != nil {
		return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("resolve workdir: %w", err)}
	}
	if rounds <= 0 {
		return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("rounds must be > 0")}
	}

	return RunOptions{
		Prompt:                    strings.TrimSpace(prompt),
		Workdir:                   workdir,
		Mode:                      mode,
		ModeExplicit:              modeExplicit,
		Coder:                     editorTarget,
		Adversary:                 reviewerTarget,
		Scout:                     scoutTarget,
		CoderExplicit:             editorExplicit,
		AdversaryExplicit:         reviewerExplicit,
		ScoutExplicit:             scoutExplicit,
		CoderExplicitMode:         editorExplicitMode,
		AdversaryExplicitMode:     reviewerExplicitMode,
		ScoutExplicitMode:         scoutExplicitMode,
		ScoutMode:                 scoutMode,
		PostScoutMode:             postScoutMode,
		ScoutFailurePolicy:        scoutFailurePolicy,
		LossPolicy:                lossPolicy,
		Fallbacks:                 normalizeRoleFallbacks(fallbacks, editorTarget, reviewerTarget, scoutTarget),
		FallbacksByTarget:         normalizeTargetFallbacks(fallbacksByTarget),
		ScoutRetrieval:            scoutRetrieval,
		ScoutContextPolicy:        scoutContextPolicy,
		TrustRepoConfig:           flags.TrustRepoConfig && changed["trust-repo-config"],
		SupervisorCanEdit:         flags.SupervisorCanEdit,
		SupervisorCanEditExplicit: changed["supervisor-can-edit"],
		SupervisorSlicing:         supervisorSlicing,
		SupervisorSlicingExplicit: changed["slice"] || changed["no-slice"],
		MaxPackages:               maxPackages,
		Package:                   strings.TrimSpace(packageID),
		AutoNextPackage:           autoNextPackage,
		RespectRepoInstructions:   respectRepoInstructions,
		DecisionMemory:            decisionMemory,
		MaxFindings:               maxFindings,
		MaxOutputBytes:            maxOutputBytes,
		MaxWallTime:               maxWallTime,
		MaxRoleInvocations:        maxRoleInvocations,
		JSONRepair:                jsonRepair,
		Rounds:                    rounds,
		TestCmd:                   testCmd,
		NoTest:                    flags.NoTest,
		JSON:                      flags.JSON,
		DryRun:                    flags.DryRun,
		ShowReview:                flags.ShowReview,
		FailOnReview:              flags.FailOnReview,
		AllowDirty:                flags.AllowDirty,
		Autostash:                 flags.Autostash,
		Timeout:                   flags.Timeout,
		Quiet:                     flags.Quiet,
		Verbose:                   flags.Verbose,
		GitSafety:                 gitSafety,
		CodexArgs:                 codexArgs,
		ClaudeArgs:                claudeArgs,
		AgyArgs:                   agyArgs,
		GoslingArgs:               goslingArgs,
		OpenAICompatibleArgs:      openAICompatibleArgs,
		EnvOverlay:                cloneStringMap(cfg.EnvOverlay),
		ConfigSources:             sources,
	}, nil
}

func cloneStringMap(src map[string]string) map[string]string {
	dst := make(map[string]string, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func parseHeaderPairs(raw string) map[string]string {
	headers := map[string]string{}
	for _, part := range strings.Split(raw, ",") {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		headers[key] = strings.TrimSpace(value)
	}
	return headers
}

func mergePassthrough(base []string, raw string) ([]string, error) {
	merged := append([]string{}, base...)
	if strings.TrimSpace(raw) == "" {
		return merged, nil
	}
	parts, err := shlex.Split(raw)
	if err != nil {
		return nil, err
	}
	return append(merged, parts...), nil
}

func validateScoutMode(label, value string) error {
	switch strings.TrimSpace(value) {
	case "recon", "lint", "polish", "tests", "risk":
		return nil
	default:
		return &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("invalid %s %q (want recon, lint, polish, tests, or risk)", label, value)}
	}
}

func validateRoleLossPolicies(p RoleLossPolicies) error {
	for _, item := range []struct {
		name  string
		value LossPolicy
	}{
		{"loss_policy.reviewer", p.Reviewer},
		{"loss_policy.supervisor", p.Supervisor},
		{"loss_policy.scout", p.Scout},
	} {
		switch item.value {
		case "", LossPolicyBlock, LossPolicyDegrade, LossPolicyReplaceThenBlock, LossPolicyReplaceThenDegrade:
		default:
			return fmt.Errorf("invalid %s %q (want block, degrade, replace_then_block, or replace_then_degrade)", item.name, item.value)
		}
	}
	return nil
}

func normalizeRoleFallbacks(f RoleFallbacks, editor, reviewer, scout RoleTarget) RoleFallbacks {
	_ = editor
	return RoleFallbacks{
		Reviewer:   normalizeFallbackTargets(f.Reviewer, reviewer),
		Supervisor: normalizeFallbackTargets(f.Supervisor, reviewer),
		Scout:      normalizeFallbackTargets(f.Scout, scout),
	}
}

func normalizeTargetFallbacks(f TargetFallbacks) TargetFallbacks {
	if len(f) == 0 {
		return nil
	}
	out := TargetFallbacks{}
	for rawPrimary, rawFallbacks := range f {
		primary, err := ParseRoleTarget(rawPrimary)
		if err != nil || primary.Adapter == "" {
			continue
		}
		normalized := normalizeFallbackTargets(rawFallbacks, primary)
		if len(normalized) > 0 {
			out[roleTargetString(primary)] = normalized
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeFallbackTargets(raw []string, primary RoleTarget) []string {
	const maxFallbackTargets = 5
	primaryRaw := roleTargetString(primary)
	seen := map[string]bool{}
	if primaryRaw != "" {
		seen[primaryRaw] = true
	}
	out := []string{}
	for _, item := range raw {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		if _, err := ParseRoleTarget(item); err != nil {
			continue
		}
		seen[item] = true
		out = append(out, item)
		if len(out) == maxFallbackTargets {
			break
		}
	}
	return out
}

func cloneTargetFallbacks(src TargetFallbacks) TargetFallbacks {
	if len(src) == 0 {
		return nil
	}
	out := TargetFallbacks{}
	for primary, fallbacks := range src {
		out[primary] = append([]string{}, fallbacks...)
	}
	return out
}

func roleTargetString(target RoleTarget) string {
	if target.Adapter == "" {
		return ""
	}
	if target.Model == "" {
		return target.Adapter
	}
	return target.Adapter + ":" + target.Model
}

func EncodeConfig(cfg Config) ([]byte, error) {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
