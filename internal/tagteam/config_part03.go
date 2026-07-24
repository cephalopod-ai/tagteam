package tagteam

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/shlex"
)

func ResolveOptions(cfg Config, sources []string, flags FlagInputs, changed map[string]bool, prompt string) (RunOptions, error) {
	if err := validateConfig(cfg); err != nil {
		return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: err}
	}
	if len(flags.AllowedPaths) > 0 {
		if err := validateExplicitAllowedScope(flags.AllowedPaths); err != nil {
			return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: err}
		}
	}
	stateRoot := cfg.Defaults.StateRoot
	watchdogTimeout, err := time.ParseDuration(cfg.Defaults.WatchdogTimeout)
	if err != nil {
		return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("parse watchdog_timeout: %w", err)}
	}
	modeRaw := cfg.Defaults.Mode
	rounds := cfg.Defaults.Rounds
	testCmd := cfg.Defaults.Test
	testCommands := configuredConfigTestCommands(cfg.Defaults.Test, cfg.Defaults.Tests)
	if len(testCommands) > 0 {
		testCmd = testCommands[0]
	}
	lintCmd := cfg.Defaults.Lint
	testIdentityRegex := cfg.Defaults.TestIdentityRegex
	churn := cfg.Defaults.Churn
	gitSafety := cfg.Defaults.GitSafety
	scoutMode := cfg.Defaults.ScoutMode
	postScoutMode := cfg.Defaults.PostScoutMode
	scoutFailurePolicy := cfg.Defaults.ScoutFailurePolicy
	lossPolicy := cfg.Defaults.LossPolicy
	fallbacks := cfg.Defaults.Fallbacks
	fallbacksByTarget := cloneTargetFallbacks(cfg.Defaults.FallbacksByTarget)
	scoutRetrieval := false
	if cfg.Defaults.ScoutRetrieval != nil {
		scoutRetrieval = *cfg.Defaults.ScoutRetrieval
	}
	codeIntelCommand := cfg.Defaults.CodeIntelCommand
	codeIntel := cfg.CodeIntel
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
	if strings.TrimSpace(codeIntel.Timeout) != "" {
		if _, err := time.ParseDuration(codeIntel.Timeout); err != nil {
			return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("parse code_intel.timeout: %w", err)}
		}
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
		if profile.StateRoot != "" {
			stateRoot = profile.StateRoot
		}
		if profile.WatchdogTimeout != "" {
			parsed, parseErr := time.ParseDuration(profile.WatchdogTimeout)
			if parseErr != nil {
				return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("parse profile watchdog_timeout: %w", parseErr)}
			}
			watchdogTimeout = parsed
		}
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
		if profile.Tests != nil {
			testCommands = append([]string(nil), profile.Tests...)
			testCmd = testCommands[0]
		} else if profile.Test != "" {
			testCmd = profile.Test
			testCommands = []string{testCmd}
		}
		if profile.Lint != "" {
			lintCmd = profile.Lint
		}
		if profile.TestIdentityRegex != "" {
			testIdentityRegex = profile.TestIdentityRegex
		}
		if profile.GitSafety != "" {
			gitSafety = profile.GitSafety
		}
		mergeChurnThresholds(&churn, profile.Churn)
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
		if profile.CodeIntelCommand != "" {
			codeIntelCommand = profile.CodeIntelCommand
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
	if changed["state-root"] {
		stateRoot = flags.StateRoot
	}
	if changed["watchdog-timeout"] {
		watchdogTimeout = flags.WatchdogTimeout
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
	// --model is the conventional, mode-neutral spelling for the same
	// implementation slot. Keep reviewer and scout selection role-specific.
	if changed["model"] {
		editorRaw = flags.Model
		editorExplicit = true
		editorExplicitMode = mode
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
		if flags.Tests != nil {
			normalized, normalizeErr := normalizeTestCommandList(flags.Tests, "--test")
			if normalizeErr != nil {
				return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: normalizeErr}
			}
			testCommands = normalized
			testCmd = testCommands[0]
		} else {
			testCmd = flags.Test
			testCommands = configuredConfigTestCommands(testCmd, nil)
		}
	}
	if changed["test-identity-regex"] {
		testIdentityRegex = flags.TestIdentityRegex
	}
	if changed["lint"] {
		lintCmd = flags.Lint
	}
	if changed["allow-dirty"] {
		gitSafety = "allow-dirty"
	}
	if changed["autostash"] {
		gitSafety = "autostash"
	}
	if gitSafety != "clean" && gitSafety != "autostash" && gitSafety != "branch" && gitSafety != "allow-dirty" && gitSafety != "sync" {
		return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("invalid git_safety %q", gitSafety)}
	}
	if testIdentityRegex != "" {
		compiled, compileErr := regexp.Compile(testIdentityRegex)
		if compileErr != nil || compiled.NumSubexp() < 1 {
			return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("test_identity_regex must compile and contain a capture group")}
		}
	}
	if churn.MaxFiles <= 0 || churn.MaxChangedLines <= 0 || churn.MaxFixtureFiles <= 0 || churn.WhitespaceRatio <= 0 || churn.WhitespaceRatio > 1 || churn.MinimumSemanticRatio <= 0 || churn.MinimumSemanticRatio > 1 {
		return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("invalid churn thresholds")}
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
	grokArgs, err := mergePassthrough(cfg.Adapters.Grok.ExtraArgs, flags.GrokArgsRaw)
	if err != nil {
		return RunOptions{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("parse --grok-args: %w", err)}
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

	opts := RunOptions{
		Prompt:                    strings.TrimSpace(prompt),
		Workdir:                   workdir,
		StateRoot:                 strings.TrimSpace(stateRoot),
		AllowedPaths:              append([]string(nil), flags.AllowedPaths...),
		WatchdogTimeout:           watchdogTimeout,
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
		CodeIntelCommand:          strings.TrimSpace(codeIntelCommand),
		CodeIntel:                 codeIntel,
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
		TestCommands:              append([]string(nil), testCommands...),
		LintCmd:                   lintCmd,
		TestIdentityRegex:         testIdentityRegex,
		Churn:                     churn,
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
		GrokArgs:                  grokArgs,
		OpenAICompatibleArgs:      openAICompatibleArgs,
		EnvOverlay:                cloneStringMap(cfg.EnvOverlay),
		ConfigSources:             sources,
	}
	if err := validateRunRoles(opts); err != nil {
		return RunOptions{}, err
	}
	if err := validateRunFallbacks(opts); err != nil {
		return RunOptions{}, err
	}
	if err := validateClaudeRoleAssignments(opts); err != nil {
		return RunOptions{}, err
	}
	return opts, nil
}

func cloneStringMap(src map[string]string) map[string]string {
	dst := make(map[string]string, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func parseHeaderPairs(raw string) (map[string]string, error) {
	headers := map[string]string{}
	for _, part := range strings.Split(raw, ",") {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("header entry must use name=value syntax")
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, fmt.Errorf("header name must not be empty")
		}
		headers[key] = strings.TrimSpace(value)
	}
	return headers, nil
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
