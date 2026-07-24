package tagteam

const (
	agyGemini36FlashLow    = "gemini-3.6-flash-low"
	agyGemini36FlashMedium = "gemini-3.6-flash-medium"
	agyGemini36FlashHigh   = "gemini-3.6-flash-high"

	defaultSupervisorTarget   = "claude:claude-opus-4-8"
	defaultSupervisorFallback = "codex:gpt-5.6-sol"
	defaultWorkerTarget       = "codex:gpt-5.6-terra"
	// Keep automatic implementation fallback on a model permitted to edit.
	// Gemini is reserved for the scout role in the maintained operator roster.
	defaultWorkerFallback         = "codex:gpt-5.6-sol"
	defaultRelayCoderTarget       = defaultWorkerTarget
	defaultRelayScoutTarget       = "openai-compatible:gemma4:latest"
	defaultAdversarialCoderTarget = "codex:gpt-5.6-terra"
	defaultAdversaryTarget        = defaultSupervisorTarget
)

// AgyGemini36FlashModelChoices returns the Agy Gemini 3.6 Flash tiers that
// Tagteam exposes in interactive model selection. Target parsing remains
// open-ended so user-configured or newer Agy models continue to work.
func AgyGemini36FlashModelChoices() []string {
	return []string{
		agyGemini36FlashLow,
		agyGemini36FlashMedium,
		agyGemini36FlashHigh,
	}
}

type modeRoleTargets struct {
	Editor   string
	Reviewer string
	Scout    string
}

func configuredTargetsForMode(defaults DefaultsConfig, mode Mode) modeRoleTargets {
	switch mode {
	case ModeSolo:
		return modeRoleTargets{Editor: defaults.Worker}
	case ModeAdversarial:
		return modeRoleTargets{Editor: defaults.Coder, Reviewer: defaults.Adversary}
	case ModeSupervisor:
		return modeRoleTargets{Editor: defaults.Worker, Reviewer: defaults.Supervisor}
	case ModeRelay:
		editor := defaults.RelayCoder
		if editor == "" {
			// Preserve configurations written before relay_coder existed.
			editor = defaults.Coder
		}
		return modeRoleTargets{Editor: editor, Reviewer: defaults.Supervisor, Scout: defaults.Scout}
	default:
		return modeRoleTargets{}
	}
}
