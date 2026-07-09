package tagteam

const (
	defaultSupervisorTarget       = "codex:gpt-5.6-sol"
	defaultWorkerTarget           = "claude:claude-sonnet-5"
	defaultRelayCoderTarget       = "claude:claude-sonnet-5"
	defaultRelayScoutTarget       = "agy:Gemini 3.5 Flash (Medium)"
	defaultAdversarialCoderTarget = "codex:gpt-5.6-terra"
	defaultAdversaryTarget        = "claude:claude-opus-4-8"
)

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
