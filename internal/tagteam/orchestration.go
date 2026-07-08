package tagteam

import (
	"encoding/json"
	"fmt"
	"strings"
)

const orchestrationDecisionArtifact = "orchestration-decision.json"

func newOrchestrationDecision(runID string, initial Mode) OrchestrationDecision {
	return OrchestrationDecision{
		SchemaVersion: ArtifactSchemaVersion,
		RunID:         runID,
		InitialMode:   initial,
		FinalMode:     initial,
		Status:        "kept",
		Advisories:    []OrchestrationAdvisory{},
		HostReason:    "no transition requested",
	}
}

func parseOrchestrationAdvisory(raw []byte, source string) (OrchestrationAdvisory, error) {
	var advisory OrchestrationAdvisory
	if err := json.Unmarshal(raw, &advisory); err != nil {
		extracted, extractErr := extractJSONObject(raw)
		if extractErr != nil {
			return OrchestrationAdvisory{}, &OutputContractError{Err: fmt.Errorf("decode orchestration advisory JSON: %w", err)}
		}
		if err := json.Unmarshal(extracted, &advisory); err != nil {
			return OrchestrationAdvisory{}, &OutputContractError{Err: fmt.Errorf("decode orchestration advisory JSON: %w", err)}
		}
	}
	advisory.Source = source
	if advisory.SchemaVersion == 0 {
		advisory.SchemaVersion = ArtifactSchemaVersion
	}
	if err := advisory.Validate(); err != nil {
		return OrchestrationAdvisory{}, &OutputContractError{Err: err}
	}
	advisory.Reason = strings.TrimSpace(advisory.Reason)
	return advisory, nil
}

func (a OrchestrationAdvisory) Validate() error {
	if a.SchemaVersion != ArtifactSchemaVersion {
		return fmt.Errorf("unsupported orchestration advisory schema_version %d", a.SchemaVersion)
	}
	switch a.Recommendation {
	case "keep", "simplify", "escalate":
	default:
		return fmt.Errorf("invalid orchestration recommendation %q", a.Recommendation)
	}
	switch a.TargetMode {
	case ModeSupervisor, ModeRelay:
	default:
		return fmt.Errorf("invalid orchestration target_mode %q", a.TargetMode)
	}
	if strings.TrimSpace(a.Reason) == "" {
		return fmt.Errorf("orchestration advisory missing reason")
	}
	switch a.Confidence {
	case "low", "medium", "high":
	default:
		return fmt.Errorf("invalid orchestration confidence %q", a.Confidence)
	}
	return nil
}

func applyRelaySimplificationPolicy(decision *OrchestrationDecision, advisory OrchestrationAdvisory) Mode {
	decision.Advisories = append(decision.Advisories, advisory)
	if advisory.Recommendation == "simplify" && advisory.TargetMode == ModeSupervisor {
		decision.FinalMode = ModeSupervisor
		decision.Status = "transitioned"
		decision.AppliedTransition = &OrchestrationTransition{From: ModeRelay, To: ModeSupervisor, Reason: advisory.Reason}
		decision.TransitionLimitConsumed = true
		decision.HostReason = "supervisor recommended simplifying relay to supervisor mode"
		return ModeSupervisor
	}
	decision.FinalMode = ModeRelay
	decision.Status = "kept"
	decision.HostReason = "relay advisory did not request an allowed simplification"
	return ModeRelay
}

func applySupervisorEscalationPolicy(decision *OrchestrationDecision, worker, supervisor OrchestrationAdvisory) Mode {
	decision.Advisories = append(decision.Advisories, worker, supervisor)
	if worker.Recommendation == "escalate" &&
		worker.TargetMode == ModeRelay &&
		supervisor.Recommendation == "escalate" &&
		supervisor.TargetMode == ModeRelay {
		decision.FinalMode = ModeRelay
		decision.Status = "transitioned"
		decision.AppliedTransition = &OrchestrationTransition{From: ModeSupervisor, To: ModeRelay, Reason: "worker requested more context and supervisor agreed relay/scout is needed"}
		decision.TransitionLimitConsumed = true
		decision.HostReason = "worker and supervisor agreed current mode lacks context"
		return ModeRelay
	}
	decision.FinalMode = ModeSupervisor
	decision.Status = "kept"
	decision.HostReason = "escalation requires both worker and supervisor to request relay; ambiguous or conflicting signals keep the simpler mode"
	return ModeSupervisor
}

func markOrchestrationDecisionDegraded(decision *OrchestrationDecision, reason string) {
	decision.Degraded = true
	decision.DegradedReason = strings.TrimSpace(reason)
	if decision.DegradedReason == "" {
		decision.DegradedReason = "orchestration advisory unavailable"
	}
	decision.Status = "degraded"
	decision.FinalMode = decision.InitialMode
	decision.HostReason = "advisory failed; continuing with initial mode"
}

func normalizeOrchestrationDecision(decision *OrchestrationDecision) {
	if decision.SchemaVersion == 0 {
		decision.SchemaVersion = ArtifactSchemaVersion
	}
	if decision.FinalMode == "" {
		decision.FinalMode = decision.InitialMode
	}
	if decision.Advisories == nil {
		decision.Advisories = []OrchestrationAdvisory{}
	}
	if decision.Status == "" {
		decision.Status = "kept"
	}
	if decision.HostReason == "" {
		decision.HostReason = "no transition requested"
	}
}
