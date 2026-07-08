package tagteam

import (
	"errors"
	"time"
)

const (
	scoutFailureClassInvocation    = "scout_adapter_invocation_failure"
	scoutFailureClassOutput        = "scout_output_contract_failure"
	scoutFailureClassContextBudget = "scout_context_budget_exceeded"
)

type ScoutExecutionArtifact struct {
	SchemaVersion                int       `json:"schema_version"`
	ScoutMode                    string    `json:"scout_mode"`
	FailurePolicy                string    `json:"failure_policy"`
	RetrievalEnabled             bool      `json:"retrieval_enabled"`
	RetrievalRan                 bool      `json:"retrieval_ran"`
	RetrievalStatus              string    `json:"retrieval_status,omitempty"`
	RetrievalDegraded            bool      `json:"retrieval_degraded"`
	RetrievalDisabledByBudget    bool      `json:"retrieval_disabled_by_budget"`
	ScoutRan                     bool      `json:"scout_ran"`
	ScoutSucceeded               bool      `json:"scout_succeeded"`
	FailureClass                 string    `json:"failure_class,omitempty"`
	Failure                      string    `json:"failure,omitempty"`
	ContinuedWithoutScoutContext bool      `json:"continued_without_scout_context"`
	GeneratedAt                  time.Time `json:"generated_at"`
}

func newScoutExecutionArtifact(scoutMode, failurePolicy string, retrievalEnabled bool) ScoutExecutionArtifact {
	return ScoutExecutionArtifact{
		SchemaVersion:    ArtifactSchemaVersion,
		ScoutMode:        scoutMode,
		FailurePolicy:    failurePolicy,
		RetrievalEnabled: retrievalEnabled,
		GeneratedAt:      time.Now().UTC(),
	}
}

func retrievalStatusIsDegraded(status string) bool {
	switch status {
	case "", "ok", "disabled":
		return false
	default:
		return true
	}
}

func classifyScoutFailure(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, errScoutContextTooSmall) {
		return scoutFailureClassContextBudget
	}
	if IsOutputContractError(err) {
		return scoutFailureClassOutput
	}
	var exitErr *ExitError
	if errors.As(err, &exitErr) && exitErr.Code == ExitAdapterFailure {
		return scoutFailureClassInvocation
	}
	return scoutFailureClassOutput
}
