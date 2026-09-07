package tagteam

const (
	openAIGPT6Astra        = "gpt-6-astra"
	claudeFable51          = "claude-fable-5-1"
	grok46                 = "grok-4.6"
	agyGemini38FlashLow    = "gemini-3.8-flash-low"
	agyGemini38FlashMedium = "gemini-3.8-flash-medium"
	agyGemini38FlashHigh   = "gemini-3.8-flash-high"
	agyGemini36FlashLow    = "gemini-3.6-flash-low"
	agyGemini36FlashMedium = "gemini-3.6-flash-medium"
	agyGemini36FlashHigh   = "gemini-3.6-flash-high"

	defaultSupervisorTarget   = "claude:claude-opus-5"
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

// MaintainedModelTargets returns current first-party picker choices plus the
// pinned compatibility models Tagteam ships by default. Provider model-list
// commands remain the source of truth when live discovery is available.
func MaintainedModelTargets() []string {
	return []string{
		"codex:" + openAIGPT6Astra,
		"codex:gpt-5.6-sol",
		"codex:gpt-5.6-terra",
		"claude:" + claudeFable51,
		"claude:claude-opus-5",
		"claude:claude-sonnet-5",
		"grok:" + grok46,
		"grok:grok-4.5",
		"agy:" + agyGemini38FlashHigh,
		"agy:" + agyGemini38FlashMedium,
		"agy:" + agyGemini38FlashLow,
		"agy:" + agyGemini36FlashHigh,
		"agy:" + agyGemini36FlashMedium,
		"agy:" + agyGemini36FlashLow,
	}
}

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

func mergeContextBudget(dstMax, dstReserved **int, srcMax, srcReserved *int) {
	if srcMax != nil {
		*dstMax = cloneIntPtr(srcMax)
	}
	if srcReserved != nil {
		*dstReserved = cloneIntPtr(srcReserved)
	}
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
