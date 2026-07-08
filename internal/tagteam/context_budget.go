package tagteam

import (
	"errors"
	"fmt"
	"time"
)

const (
	scoutContextStatusUnknown    = "unknown"
	scoutContextStatusOK         = "ok"
	scoutContextStatusNearLimit  = "near_limit"
	scoutContextStatusExceeds    = "exceeds_limit"
	scoutContextEstimator        = "ceil(prompt_bytes/3)"
	scoutContextNearLimitPercent = 70
)

type ScoutContextLimit struct {
	MaxContextTokens     int
	ReservedOutputTokens int
}

type ScoutContextBudgetArtifact struct {
	SchemaVersion              int       `json:"schema_version"`
	Adapter                    string    `json:"adapter,omitempty"`
	Model                      string    `json:"model,omitempty"`
	ConfiguredMaxContextTokens int       `json:"configured_max_context_tokens,omitempty"`
	ReservedOutputTokens       int       `json:"reserved_output_tokens,omitempty"`
	UsableContextTokens        int       `json:"usable_context_tokens,omitempty"`
	EstimatedInputTokens       int       `json:"estimated_input_tokens"`
	EstimatedInputBytes        int       `json:"estimated_input_bytes"`
	Status                     string    `json:"status"`
	Estimator                  string    `json:"estimator"`
	NoConfiguredLimit          bool      `json:"no_configured_limit"`
	RetrievalCompacted         bool      `json:"retrieval_compacted"`
	RetrievalDisabledDueBudget bool      `json:"retrieval_disabled_due_budget"`
	GeneratedAt                time.Time `json:"generated_at"`
}

func scoutContextLimitForAdapter(cfg Config, adapter string) ScoutContextLimit {
	switch adapter {
	case "codex":
		return contextLimitFromConfig(cfg.Adapters.Codex.MaxContextTokens, cfg.Adapters.Codex.ReservedOutputTokens)
	case "codex-oss":
		return contextLimitFromConfig(cfg.Adapters.CodexOSS.MaxContextTokens, cfg.Adapters.CodexOSS.ReservedOutputTokens)
	case "claude":
		return contextLimitFromConfig(cfg.Adapters.Claude.MaxContextTokens, cfg.Adapters.Claude.ReservedOutputTokens)
	case "agy":
		return contextLimitFromConfig(cfg.Adapters.Agy.MaxContextTokens, cfg.Adapters.Agy.ReservedOutputTokens)
	case "gosling":
		return contextLimitFromConfig(cfg.Adapters.Gosling.MaxContextTokens, cfg.Adapters.Gosling.ReservedOutputTokens)
	case "openai-compatible", "oai":
		return contextLimitFromConfig(cfg.Adapters.OpenAICompatible.MaxContextTokens, cfg.Adapters.OpenAICompatible.ReservedOutputTokens)
	default:
		return ScoutContextLimit{}
	}
}

func contextLimitFromConfig(maxContextTokens, reservedOutputTokens *int) ScoutContextLimit {
	limit := ScoutContextLimit{}
	if maxContextTokens != nil {
		limit.MaxContextTokens = *maxContextTokens
	}
	if reservedOutputTokens != nil {
		limit.ReservedOutputTokens = *reservedOutputTokens
	}
	return limit
}

func estimateScoutPromptBudget(prompt string, limit ScoutContextLimit) ScoutContextBudgetArtifact {
	bytes := len([]byte(prompt))
	estimated := estimatePromptTokens(prompt)
	artifact := ScoutContextBudgetArtifact{
		SchemaVersion:        ArtifactSchemaVersion,
		ReservedOutputTokens: limit.ReservedOutputTokens,
		EstimatedInputTokens: estimated,
		EstimatedInputBytes:  bytes,
		Status:               scoutContextStatusUnknown,
		Estimator:            scoutContextEstimator,
		NoConfiguredLimit:    limit.MaxContextTokens == 0,
		GeneratedAt:          time.Now().UTC(),
	}
	if limit.MaxContextTokens <= 0 {
		return artifact
	}
	artifact.ConfiguredMaxContextTokens = limit.MaxContextTokens
	artifact.UsableContextTokens = limit.MaxContextTokens - limit.ReservedOutputTokens
	switch {
	case estimated <= artifact.UsableContextTokens*scoutContextNearLimitPercent/100:
		artifact.Status = scoutContextStatusOK
	case estimated <= artifact.UsableContextTokens:
		artifact.Status = scoutContextStatusNearLimit
	default:
		artifact.Status = scoutContextStatusExceeds
	}
	return artifact
}

func estimatePromptTokens(prompt string) int {
	bytes := len([]byte(prompt))
	if bytes == 0 {
		return 0
	}
	// Deterministic, intentionally conservative approximation. This avoids
	// provider-specific tokenizers while still scaling with actual prompt size.
	return (bytes + 2) / 3
}

// errScoutContextTooSmall is the sentinel wrapped by invalidScoutContextBudgetError.
// classifyRoleFailure and classifyScoutFailure both detect the scout-context case
// via errors.Is on this sentinel rather than substring-matching the message.
var errScoutContextTooSmall = errors.New("scout context exceeds configured limit")

func invalidScoutContextBudgetError(artifact ScoutContextBudgetArtifact) error {
	return fmt.Errorf("%w even without retrieval: estimated_input_tokens=%d usable_context_tokens=%d; configure a larger-context scout or reduce prompt size", errScoutContextTooSmall, artifact.EstimatedInputTokens, artifact.UsableContextTokens)
}
