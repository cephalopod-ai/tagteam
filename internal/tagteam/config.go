package tagteam

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/google/shlex"
)

func DefaultConfig() Config {
	supervisorSlicing := true
	autoNextPackage := false
	respectRepoInstructions := true
	return Config{
		Defaults: DefaultsConfig{
			Mode:                    "supervisor",
			Coder:                   "codex",
			Adversary:               "claude",
			Worker:                  "agy:Gemini 3.5 Flash (High)",
			Scout:                   "agy:gemini-3.5-flash-low",
			Supervisor:              "claude:opus",
			ScoutMode:               "recon",
			PostScoutMode:           "polish",
			SupervisorSlicing:       &supervisorSlicing,
			MaxPackages:             5,
			AutoNextPackage:         &autoNextPackage,
			RespectRepoInstructions: &respectRepoInstructions,
			Rounds:                  2,
			GitSafety:               "clean",
		},
		Profiles: map[string]ProfileConfig{
			"fast": {
				Mode:      "adversarial",
				Coder:     "codex:gpt-5-codex-mini",
				Adversary: "claude:haiku",
				Rounds:    1,
			},
			"paranoid": {
				Mode:      "adversarial",
				Adversary: "claude:opus",
				Rounds:    4,
				Test:      "make check",
			},
			"relay": {
				Mode:          "relay",
				Scout:         "agy:gemini-3.5-flash-low",
				Coder:         "codex:gpt-5.4-mini",
				Supervisor:    "claude:sonnet",
				ScoutMode:     "recon",
				PostScoutMode: "polish",
				Rounds:        2,
			},
		},
		Adapters: AdapterConfigSet{
			Codex: CodexConfig{},
			Claude: ClaudeConfig{
				DefaultModel:      "sonnet",
				CoderAllowedTools: []string{"Edit", "Write", "Read", "Glob", "Grep", "Bash"},
				ExtraArgs:         []string{},
			},
			CodexOSS: CodexConfig{
				DefaultModel: "qwen3-coder",
			},
			Agy: AgyConfig{
				DefaultModel: "gemini-3.5-flash",
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

func LoadConfig(workdir string) (Config, []string, error) {
	cfg := DefaultConfig()
	sources := []string{"built-in defaults"}

	loadedDotEnv, err := loadDotEnv(workdir)
	if err != nil {
		return Config{}, nil, err
	}
	if loadedDotEnv {
		sources = append(sources, filepath.Join(workdir, ".env"))
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
	if err := mergeConfigFile(&cfg, repoPath); err != nil {
		return Config{}, nil, err
	}
	if fileExists(repoPath) {
		sources = append(sources, repoPath)
	}

	mergeEnvConfig(&cfg)
	if hasTagteamEnv() {
		sources = append(sources, "TAGTEAM_* env")
	}

	return cfg, sources, nil
}

func loadDotEnv(workdir string) (bool, error) {
	path := filepath.Join(workdir, ".env")
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineNo := 0
	loaded := false
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return false, fmt.Errorf("parse %s:%d: expected KEY=VALUE", path, lineNo)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return false, fmt.Errorf("parse %s:%d: empty environment variable name", path, lineNo)
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		value = strings.TrimSpace(value)
		if unquoted, err := strconv.Unquote(value); err == nil {
			value = unquoted
		}
		if err := os.Setenv(key, value); err != nil {
			return false, fmt.Errorf("set %s from %s:%d: %w", key, path, lineNo, err)
		}
		loaded = true
	}
	if err := scanner.Err(); err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	return loaded, nil
}

func userConfigPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config dir: %w", err)
	}
	return filepath.Join(base, "tagteam", "config.toml"), nil
}

func mergeConfigFile(dst *Config, path string) error {
	if !fileExists(path) {
		return nil
	}
	var src Config
	if _, err := toml.DecodeFile(path, &src); err != nil {
		return fmt.Errorf("decode config %s: %w", path, err)
	}
	mergeConfig(dst, src)
	return nil
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
	if len(src.Adapters.Codex.ExtraArgs) > 0 {
		dst.Adapters.Codex.ExtraArgs = append([]string{}, src.Adapters.Codex.ExtraArgs...)
	}
	if src.Adapters.Claude.DefaultModel != "" {
		dst.Adapters.Claude.DefaultModel = src.Adapters.Claude.DefaultModel
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
	if src.Adapters.CodexOSS.DefaultModel != "" {
		dst.Adapters.CodexOSS.DefaultModel = src.Adapters.CodexOSS.DefaultModel
	}
	if len(src.Adapters.CodexOSS.ExtraArgs) > 0 {
		dst.Adapters.CodexOSS.ExtraArgs = append([]string{}, src.Adapters.CodexOSS.ExtraArgs...)
	}
	if src.Adapters.Agy.DefaultModel != "" {
		dst.Adapters.Agy.DefaultModel = src.Adapters.Agy.DefaultModel
	}
	if len(src.Adapters.Agy.ExtraArgs) > 0 {
		dst.Adapters.Agy.ExtraArgs = append([]string{}, src.Adapters.Agy.ExtraArgs...)
	}
	if src.Adapters.Gosling.DefaultModel != "" {
		dst.Adapters.Gosling.DefaultModel = src.Adapters.Gosling.DefaultModel
	}
	if len(src.Adapters.Gosling.ExtraArgs) > 0 {
		dst.Adapters.Gosling.ExtraArgs = append([]string{}, src.Adapters.Gosling.ExtraArgs...)
	}
	if src.Adapters.OpenAICompatible.BaseURL != "" {
		dst.Adapters.OpenAICompatible.BaseURL = src.Adapters.OpenAICompatible.BaseURL
	}
	if src.Adapters.OpenAICompatible.APIKeyEnv != "" {
		dst.Adapters.OpenAICompatible.APIKeyEnv = src.Adapters.OpenAICompatible.APIKeyEnv
	}
	if src.Adapters.OpenAICompatible.DefaultModel != "" {
		dst.Adapters.OpenAICompatible.DefaultModel = src.Adapters.OpenAICompatible.DefaultModel
	}
	if len(src.Adapters.OpenAICompatible.ExtraHeaders) > 0 {
		dst.Adapters.OpenAICompatible.ExtraHeaders = cloneStringMap(src.Adapters.OpenAICompatible.ExtraHeaders)
	}
	if len(src.Adapters.OpenAICompatible.ExtraArgs) > 0 {
		dst.Adapters.OpenAICompatible.ExtraArgs = append([]string{}, src.Adapters.OpenAICompatible.ExtraArgs...)
	}
}

func hasTagteamEnv() bool {
	for _, key := range []string{
		"TAGTEAM_MODE",
		"TAGTEAM_CODER",
		"TAGTEAM_ADVERSARY",
		"TAGTEAM_WORKER",
		"TAGTEAM_SCOUT",
		"TAGTEAM_SCOUT_MODE",
		"TAGTEAM_POST_SCOUT_MODE",
		"TAGTEAM_SUPERVISOR",
		"TAGTEAM_SUPERVISOR_SLICING",
		"TAGTEAM_MAX_PACKAGES",
		"TAGTEAM_PACKAGE",
		"TAGTEAM_AUTO_NEXT_PACKAGE",
		"TAGTEAM_RESPECT_REPO_INSTRUCTIONS",
		"TAGTEAM_ROUNDS",
		"TAGTEAM_TEST",
		"TAGTEAM_GIT_SAFETY",
		"TAGTEAM_CODEX_ARGS",
		"TAGTEAM_CLAUDE_ARGS",
		"TAGTEAM_AGY_ARGS",
		"TAGTEAM_GOSLING_ARGS",
		"TAGTEAM_OPENAI_COMPATIBLE_BASE_URL",
		"TAGTEAM_OPENAI_COMPATIBLE_API_KEY_ENV",
		"TAGTEAM_OPENAI_COMPATIBLE_MODEL",
		"TAGTEAM_OPENAI_COMPATIBLE_HEADERS",
		"TAGTEAM_OPENAI_COMPATIBLE_ARGS",
	} {
		if _, ok := os.LookupEnv(key); ok {
			return true
		}
	}
	return false
}

func mergeEnvConfig(cfg *Config) {
	legacyRoleEnvSet := false
	if value := os.Getenv("TAGTEAM_CODER"); value != "" {
		cfg.Defaults.Coder = value
		legacyRoleEnvSet = true
	}
	if value := os.Getenv("TAGTEAM_ADVERSARY"); value != "" {
		cfg.Defaults.Adversary = value
		legacyRoleEnvSet = true
	}
	if value := os.Getenv("TAGTEAM_WORKER"); value != "" {
		cfg.Defaults.Worker = value
	}
	if value := os.Getenv("TAGTEAM_SCOUT"); value != "" {
		cfg.Defaults.Scout = value
	}
	if value := os.Getenv("TAGTEAM_SCOUT_MODE"); value != "" {
		cfg.Defaults.ScoutMode = value
	}
	if value := os.Getenv("TAGTEAM_POST_SCOUT_MODE"); value != "" {
		cfg.Defaults.PostScoutMode = value
	}
	if value := os.Getenv("TAGTEAM_SUPERVISOR"); value != "" {
		cfg.Defaults.Supervisor = value
	}
	if value := os.Getenv("TAGTEAM_SUPERVISOR_SLICING"); value != "" {
		if parsed, err := strconv.ParseBool(value); err == nil {
			cfg.Defaults.SupervisorSlicing = &parsed
		}
	}
	if value := os.Getenv("TAGTEAM_MAX_PACKAGES"); value != "" {
		if maxPackages, err := strconv.Atoi(value); err == nil && maxPackages > 0 {
			cfg.Defaults.MaxPackages = maxPackages
		}
	}
	if value := os.Getenv("TAGTEAM_PACKAGE"); value != "" {
		cfg.Defaults.Package = value
	}
	if value := os.Getenv("TAGTEAM_AUTO_NEXT_PACKAGE"); value != "" {
		if parsed, err := strconv.ParseBool(value); err == nil {
			cfg.Defaults.AutoNextPackage = &parsed
		}
	}
	if value := os.Getenv("TAGTEAM_RESPECT_REPO_INSTRUCTIONS"); value != "" {
		if parsed, err := strconv.ParseBool(value); err == nil {
			cfg.Defaults.RespectRepoInstructions = &parsed
		}
	}
	if value := os.Getenv("TAGTEAM_MODE"); value != "" {
		cfg.Defaults.Mode = value
	} else if legacyRoleEnvSet {
		// TAGTEAM_CODER/TAGTEAM_ADVERSARY predate TAGTEAM_MODE and the
		// supervisor default; keep them selecting adversarial mode instead
		// of being silently ignored now that default resolution reads
		// Defaults.Worker/Defaults.Supervisor.
		cfg.Defaults.Mode = string(ModeAdversarial)
	}
	if value := os.Getenv("TAGTEAM_ROUNDS"); value != "" {
		if rounds, err := strconv.Atoi(value); err == nil && rounds > 0 {
			cfg.Defaults.Rounds = rounds
		}
	}
	if value := os.Getenv("TAGTEAM_TEST"); value != "" {
		cfg.Defaults.Test = value
	}
	if value := os.Getenv("TAGTEAM_GIT_SAFETY"); value != "" {
		cfg.Defaults.GitSafety = value
	}
	if value := os.Getenv("TAGTEAM_CODEX_ARGS"); value != "" {
		if parsed, err := shlex.Split(value); err == nil {
			cfg.Adapters.Codex.ExtraArgs = parsed
		}
	}
	if value := os.Getenv("TAGTEAM_CLAUDE_ARGS"); value != "" {
		if parsed, err := shlex.Split(value); err == nil {
			cfg.Adapters.Claude.ExtraArgs = parsed
		}
	}
	if value := os.Getenv("TAGTEAM_AGY_ARGS"); value != "" {
		if parsed, err := shlex.Split(value); err == nil {
			cfg.Adapters.Agy.ExtraArgs = parsed
		}
	}
	if value := os.Getenv("TAGTEAM_GOSLING_ARGS"); value != "" {
		if parsed, err := shlex.Split(value); err == nil {
			cfg.Adapters.Gosling.ExtraArgs = parsed
		}
	}
	if value := os.Getenv("TAGTEAM_OPENAI_COMPATIBLE_BASE_URL"); value != "" {
		cfg.Adapters.OpenAICompatible.BaseURL = value
	}
	if value := os.Getenv("TAGTEAM_OPENAI_COMPATIBLE_API_KEY_ENV"); value != "" {
		cfg.Adapters.OpenAICompatible.APIKeyEnv = value
	}
	if value := os.Getenv("TAGTEAM_OPENAI_COMPATIBLE_MODEL"); value != "" {
		cfg.Adapters.OpenAICompatible.DefaultModel = value
	}
	if value := os.Getenv("TAGTEAM_OPENAI_COMPATIBLE_HEADERS"); value != "" {
		cfg.Adapters.OpenAICompatible.ExtraHeaders = parseHeaderPairs(value)
	}
	if value := os.Getenv("TAGTEAM_OPENAI_COMPATIBLE_ARGS"); value != "" {
		if parsed, err := shlex.Split(value); err == nil {
			cfg.Adapters.OpenAICompatible.ExtraArgs = parsed
		}
	}
}

func ResolveOptions(cfg Config, sources []string, flags FlagInputs, changed map[string]bool, prompt string) (RunOptions, error) {
	modeRaw := cfg.Defaults.Mode
	rounds := cfg.Defaults.Rounds
	testCmd := cfg.Defaults.Test
	gitSafety := cfg.Defaults.GitSafety
	scoutMode := cfg.Defaults.ScoutMode
	postScoutMode := cfg.Defaults.PostScoutMode
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

	var editorRaw, reviewerRaw, scoutRaw string
	editorExplicit := false
	reviewerExplicit := false
	scoutExplicit := false
	editorExplicitMode := Mode("")
	reviewerExplicitMode := Mode("")
	scoutExplicitMode := Mode("")
	if mode == ModeSolo {
		editorRaw = cfg.Defaults.Worker
		if hasProfile && profile.Worker != "" {
			editorRaw = profile.Worker
			editorExplicit = true
			editorExplicitMode = ModeSolo
		}
	} else if mode == ModeAdversarial {
		editorRaw = cfg.Defaults.Coder
		reviewerRaw = cfg.Defaults.Adversary
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
		editorRaw = cfg.Defaults.Worker
		reviewerRaw = cfg.Defaults.Supervisor
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
		editorRaw = "codex:gpt-5.4-mini"
		reviewerRaw = "claude:sonnet"
		scoutRaw = "agy:gemini-3.5-flash-low"
		if cfg.Defaults.Mode == string(ModeRelay) && cfg.Defaults.Coder != "" {
			editorRaw = cfg.Defaults.Coder
		}
		if cfg.Defaults.Mode == string(ModeRelay) && cfg.Defaults.Supervisor != "" {
			reviewerRaw = cfg.Defaults.Supervisor
		}
		if cfg.Defaults.Mode == string(ModeRelay) && cfg.Defaults.Scout != "" {
			scoutRaw = cfg.Defaults.Scout
		}
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
	if mode == ModeRelay {
		if err := validateScoutMode("scout-mode", scoutMode); err != nil {
			return RunOptions{}, err
		}
		if err := validateScoutMode("post-scout-mode", postScoutMode); err != nil {
			return RunOptions{}, err
		}
	}
	if maxPackages <= 0 {
		return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("max-packages must be > 0")}
	}
	if maxPackages > 20 {
		return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("max-packages must be <= 20")}
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
		SupervisorCanEdit:         flags.SupervisorCanEdit,
		SupervisorCanEditExplicit: changed["supervisor-can-edit"],
		SupervisorSlicing:         supervisorSlicing,
		SupervisorSlicingExplicit: changed["slice"] || changed["no-slice"],
		MaxPackages:               maxPackages,
		Package:                   strings.TrimSpace(packageID),
		AutoNextPackage:           autoNextPackage,
		RespectRepoInstructions:   respectRepoInstructions,
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

func EncodeConfig(cfg Config) ([]byte, error) {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
