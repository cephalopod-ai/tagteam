package tagteam

// The built-in roster and job catalog are editorial priors for the maintained
// operator roster, not measurements. They exist so `--job` works out of the
// box; operators are expected to retune levels and add cards (for example a
// Mistral or Qwen endpoint behind `openai-compatible`) in user config.
//
// Slot restrictions here are deliberately at least as strict as the adapter
// role boundary enforced by ValidateRoleTarget/validateClaudeRoleAssignments:
// claude is review-only, agy/gemini is scout-only, and the local
// openai-compatible endpoint is kept to scout work.

func defaultAgentRosterConfig() map[string]AgentCardConfig {
	return map[string]AgentCardConfig{
		"fable": {
			Target:        "claude:" + claudeFable51,
			Family:        "anthropic",
			Roles:         []string{"reviewer"},
			ContextTokens: 1000000,
			Notes:         "read-only highest-capability review tier; slower and premium-priced",
			Capabilities: map[string]string{
				"coding": "max", "reasoning": "max", "research": "max",
				"planning": "max", "audit": "max", "context": "max",
				"tool_use": "high", "autonomy": "high", "speed": "low",
				"reliability": "high", "cost": "low",
			},
		},
		"opus": {
			Target:        defaultSupervisorTarget,
			Family:        "anthropic",
			Roles:         []string{"reviewer"},
			ContextTokens: 200000,
			Notes:         "read-only supervisor/adversary; strongest review and arbitration tier",
			Capabilities: map[string]string{
				"coding": "high", "reasoning": "max", "research": "high",
				"planning": "max", "audit": "max", "context": "high",
				"tool_use": "high", "autonomy": "medium", "speed": "low",
				"reliability": "high", "cost": "low",
			},
		},
		"sonnet": {
			Target:        "claude:claude-sonnet-5",
			Family:        "anthropic",
			Roles:         []string{"reviewer"},
			ContextTokens: 200000,
			Notes:         "read-only reviewer; cheaper review tier than opus",
			Capabilities: map[string]string{
				"coding": "high", "reasoning": "high", "research": "medium",
				"planning": "high", "audit": "high", "context": "high",
				"tool_use": "high", "autonomy": "medium", "speed": "medium",
				"reliability": "high", "cost": "medium",
			},
		},
		"gpt-terra": {
			Target:        defaultWorkerTarget,
			Family:        "openai",
			Roles:         []string{"editor", "reviewer"},
			ContextTokens: 256000,
			Notes:         "default implementation tier",
			Capabilities: map[string]string{
				"coding": "max", "reasoning": "high", "research": "medium",
				"planning": "high", "audit": "medium", "context": "high",
				"tool_use": "high", "autonomy": "high", "speed": "medium",
				"reliability": "high", "cost": "medium",
			},
		},
		"gpt-astra": {
			Target:        "codex:" + openAIGPT6Astra,
			Family:        "openai",
			Roles:         []string{"editor", "reviewer"},
			ContextTokens: 1050000,
			Notes:         "frontier long-context coding and reasoning tier",
			Capabilities: map[string]string{
				"coding": "max", "reasoning": "max", "research": "max",
				"planning": "max", "audit": "max", "context": "max",
				"tool_use": "max", "autonomy": "high", "speed": "low",
				"reliability": "high", "cost": "low",
			},
		},
		"gpt-sol": {
			Target:        defaultSupervisorFallback,
			Family:        "openai",
			Roles:         []string{"editor", "reviewer"},
			ContextTokens: 400000,
			Notes:         "long-horizon planning and research tier; also the maintained editor fallback",
			Capabilities: map[string]string{
				"coding": "high", "reasoning": "max", "research": "high",
				"planning": "max", "audit": "high", "context": "max",
				"tool_use": "high", "autonomy": "medium", "speed": "low",
				"reliability": "high", "cost": "low",
			},
		},
		"grok": {
			Target:        "grok:" + grok46,
			Family:        "xai",
			Roles:         []string{"editor", "reviewer"},
			ContextTokens: 256000,
			Notes:         "fast scoped-patch tier",
			Capabilities: map[string]string{
				"coding": "high", "reasoning": "high", "research": "medium",
				"planning": "medium", "audit": "medium", "context": "high",
				"tool_use": "medium", "autonomy": "medium", "speed": "high",
				"reliability": "medium", "cost": "medium",
			},
		},
		"gemini-scout": {
			Target:        "agy:" + agyGemini38FlashMedium,
			Family:        "google",
			Roles:         []string{"scout"},
			ContextTokens: 1000000,
			Notes:         "scout-only in the maintained roster; largest context window",
			Capabilities: map[string]string{
				"coding": "low", "reasoning": "medium", "research": "high",
				"planning": "medium", "audit": "medium", "context": "max",
				"tool_use": "medium", "autonomy": "low", "speed": "high",
				"reliability": "medium", "cost": "high",
			},
		},
		"gemma-local": {
			Target:        defaultRelayScoutTarget,
			Family:        "local",
			Roles:         []string{"scout"},
			ContextTokens: 128000,
			Notes:         "local Ollama scout; free but least reliable",
			Capabilities: map[string]string{
				"coding": "low", "reasoning": "low", "research": "low",
				"planning": "low", "audit": "low", "context": "low",
				"tool_use": "low", "autonomy": "low", "speed": "high",
				"reliability": "low", "cost": "max",
			},
		},
	}
}

func defaultJobCatalogConfig() map[string]JobConfig {
	diverse := true
	return map[string]JobConfig{
		"scoped_patch": {
			Description:     "Small, well-scoped change with an independent review pass",
			Mode:            string(ModeSupervisor),
			Rounds:          2,
			DiverseReviewer: &diverse,
			Roles: map[string]JobRoleConfig{
				"editor": {
					Require: map[string]string{"coding": "high", "autonomy": "medium"},
					Prefer:  []string{"coding", "reliability", "speed"},
				},
				"reviewer": {
					Require: map[string]string{"audit": "medium", "reasoning": "high"},
					Prefer:  []string{"audit", "reasoning", "cost"},
				},
			},
		},
		"problem_solving": {
			Description: "Hard change where the diagnosis matters as much as the patch",
			Mode:        string(ModeSupervisor),
			Rounds:      3,
			Roles: map[string]JobRoleConfig{
				"editor": {
					Require: map[string]string{"coding": "high", "reasoning": "high"},
					Prefer:  []string{"reasoning", "coding", "autonomy"},
				},
				"reviewer": {
					Require: map[string]string{"reasoning": "high", "planning": "high"},
					Prefer:  []string{"reasoning", "planning", "audit"},
				},
			},
		},
		"audit": {
			Description:     "Independent audit of an existing change by a different model family",
			Mode:            string(ModeAdversarial),
			Rounds:          1,
			DiverseReviewer: &diverse,
			Roles: map[string]JobRoleConfig{
				"editor": {
					Require: map[string]string{"coding": "medium"},
					Prefer:  []string{"coding", "reliability"},
				},
				"reviewer": {
					Require: map[string]string{"audit": "high", "reasoning": "high"},
					Prefer:  []string{"audit", "reasoning"},
				},
			},
		},
		"research": {
			Description: "Recon-first change: scout the repository, then implement and arbitrate",
			Mode:        string(ModeRelay),
			Rounds:      2,
			Roles: map[string]JobRoleConfig{
				"editor": {
					Require: map[string]string{"coding": "high"},
					Prefer:  []string{"coding", "reliability"},
				},
				"reviewer": {
					Require: map[string]string{"reasoning": "high", "planning": "high"},
					Prefer:  []string{"planning", "research", "reasoning"},
				},
				"scout": {
					Require: map[string]string{"research": "low", "context": "low"},
					Prefer:  []string{"cost", "speed", "research"},
				},
			},
		},
		"deep_scan": {
			Description:     "Wide repository sweep before implementation; needs a large-context scout",
			Mode:            string(ModeRelay),
			Rounds:          2,
			DiverseReviewer: &diverse,
			Roles: map[string]JobRoleConfig{
				"editor": {
					Require: map[string]string{"coding": "high"},
					Prefer:  []string{"coding", "context"},
				},
				"reviewer": {
					Require: map[string]string{"reasoning": "high", "planning": "high"},
					Prefer:  []string{"planning", "reasoning", "context"},
				},
				"scout": {
					Require:          map[string]string{"research": "high", "context": "max"},
					Prefer:           []string{"context", "research", "speed"},
					MinContextTokens: 200000,
				},
			},
		},
	}
}
