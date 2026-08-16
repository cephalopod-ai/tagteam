package tagteam

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// RoutingRequest is everything the router needs to compose a team. It is a
// pure input: selection performs no I/O, so a decision is reproducible from
// config plus the availability facts handed in by the caller.
type RoutingRequest struct {
	Job JobSpec
	// Mode is the workflow actually being run. It normally equals Job.Mode
	// but an explicit operator --mode wins, and the router must staff the
	// workflow that will really execute.
	Mode Mode
	// Operator holds slots the operator pinned by flag or profile. Pinned
	// slots are recorded, never re-selected.
	Operator map[RoleSlot]RoleTarget
	// Excluded holds operator exclusions: an agent key, an adapter id, or a
	// full adapter:model target.
	Excluded []string
	// Availability is keyed by adapter id. A missing entry means "assumed
	// available"; AvailabilityProbed records which of the two happened.
	Availability       map[string]AdapterAvailability
	AvailabilityProbed bool
	// Now stamps the decision. Zero uses the wall clock.
	Now time.Time
}

// maxRoutingFallbacks caps how many ranked alternates the router records per
// slot. Two is enough to survive a provider outage without turning the
// fallback chain into an unbounded model tour.
const maxRoutingFallbacks = 2

type scoredCandidate struct {
	card  AgentCard
	score float64
}

// Route composes a team for a job. It returns an error only when a slot the
// workflow requires cannot be staffed at all; every other exclusion is
// recorded as a rejection reason on the decision.
func Route(cfg Config, req RoutingRequest) (RoutingDecision, error) {
	roster, err := ResolveAgentRoster(cfg)
	if err != nil {
		return RoutingDecision{}, err
	}
	stamp := req.Now
	if stamp.IsZero() {
		stamp = time.Now().UTC()
	}
	availabilityMode := AvailabilityModeAssumed
	if req.AvailabilityProbed {
		availabilityMode = AvailabilityModeProbed
	}
	decision := RoutingDecision{
		SchemaVersion:    RoutingSchemaVersion,
		Job:              req.Job.Name,
		JobDescription:   req.Job.Description,
		Workflow:         req.Mode,
		WorkflowSource:   RoutingSourceJob,
		Rounds:           req.Job.Rounds,
		GeneratedAt:      stamp.UTC(),
		AvailabilityMode: availabilityMode,
		Excluded:         normalizeExclusions(req.Excluded),
	}
	if req.Mode != req.Job.Mode {
		decision.WorkflowSource = RoutingSourceOperator
		decision.Notes = append(decision.Notes, fmt.Sprintf("job %q proposes %s; operator selected %s", req.Job.Name, req.Job.Mode, req.Mode))
	}
	if req.AvailabilityProbed {
		decision.Availability = availabilityList(req.Availability)
	}

	chosenFamilies := map[RoleSlot]string{}
	for _, slot := range SlotsForMode(req.Mode) {
		requirement, ok := req.Job.Roles[slot]
		if !ok {
			requirement = RoleRequirement{Slot: slot, Require: map[Capability]int{}}
		}
		routing := RoleRouting{
			Slot:  string(slot),
			Label: SlotLabel(slot, req.Mode),
			Role:  string(SlotRole(slot, req.Mode)),
		}
		if pinned, pinnedOK := req.Operator[slot]; pinnedOK && pinned.Adapter != "" {
			routing.Source = RoutingSourceOperator
			routing.Selected = roleTargetString(pinned)
			routing.Reasons = []string{"operator pinned this slot; router selection skipped"}
			if card, found := cardForTarget(roster, pinned); found {
				routing.Agent = card.Key
				routing.Family = card.Family
			}
			chosenFamilies[slot] = familyForTarget(roster, pinned)
			decision.Roles = append(decision.Roles, routing)
			continue
		}
		excludeFamily := ""
		if slot == SlotReviewer && req.Job.DiverseReviewer {
			excludeFamily = chosenFamilies[SlotEditor]
		}
		ranked, rejected := rankCandidates(roster, req, slot, requirement, excludeFamily)
		routing.Rejected = rejected
		if len(ranked) == 0 {
			// Family diversity is a stated requirement of the job, so it is
			// never relaxed silently: the operator is told exactly which
			// constraint blocked the slot and how to change it.
			hint := ""
			if excludeFamily != "" && blockedOnlyByFamily(rejected, excludeFamily) {
				hint = fmt.Sprintf(
					"; no reviewer outside the %q family is eligible — pin the review slot explicitly, exclude fewer agents, or set diverse_reviewer = false for job %q",
					excludeFamily, req.Job.Name,
				)
			}
			return RoutingDecision{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf(
				"job %q cannot staff the %s slot in %s mode: %s%s",
				req.Job.Name, slot, req.Mode, summarizeRejections(rejected), hint,
			)}
		}
		best := ranked[0]
		routing.Source = RoutingSourceRouter
		routing.Agent = best.card.Key
		routing.Family = best.card.Family
		routing.Selected = roleTargetString(best.card.Target)
		routing.Score = best.score
		routing.Reasons = selectionReasons(best.card, requirement, excludeFamily, req)
		for _, alternate := range ranked[1:] {
			if len(routing.Fallbacks) >= maxRoutingFallbacks {
				break
			}
			routing.Fallbacks = append(routing.Fallbacks, roleTargetString(alternate.card.Target))
		}
		chosenFamilies[slot] = best.card.Family
		decision.Roles = append(decision.Roles, routing)
	}
	return decision, nil
}

// rankCandidates applies the hard constraints and returns the surviving cards
// in descending utility order, plus a rejection record for everything else.
func rankCandidates(roster []AgentCard, req RoutingRequest, slot RoleSlot, requirement RoleRequirement, excludeFamily string) ([]scoredCandidate, []RoutingRejection) {
	role := SlotRole(slot, req.Mode)
	var eligible []scoredCandidate
	var rejected []RoutingRejection
	reject := func(card AgentCard, reason string) {
		rejected = append(rejected, RoutingRejection{Agent: card.Key, Target: roleTargetString(card.Target), Reason: reason})
	}
	for _, card := range roster {
		if card.Disabled {
			reject(card, "disabled in configuration")
			continue
		}
		if excluded, pattern := isExcluded(card, req.Excluded); excluded {
			reject(card, fmt.Sprintf("excluded by %q", pattern))
			continue
		}
		if len(requirement.Candidates) > 0 && !containsFold(requirement.Candidates, card.Key) {
			reject(card, "not in the job candidate list for this slot")
			continue
		}
		if !card.AllowsSlot(slot) {
			reject(card, fmt.Sprintf("card does not allow the %s slot", slot))
			continue
		}
		if err := ValidateRoleTarget(role, card.Target); err != nil {
			reject(card, err.Error())
			continue
		}
		if err := validateRoutedTargetRole(role, card.Target); err != nil {
			reject(card, err.Error())
			continue
		}
		if available, reason := adapterAvailable(req.Availability, card.Target.Adapter); !available {
			reject(card, reason)
			continue
		}
		if missing, ok := unmetRequirement(card, requirement); !ok {
			reject(card, missing)
			continue
		}
		if requirement.MinContextTokens > 0 && card.ContextTokens > 0 && card.ContextTokens < requirement.MinContextTokens {
			reject(card, fmt.Sprintf("context window %d is below the required %d tokens", card.ContextTokens, requirement.MinContextTokens))
			continue
		}
		if requirement.MinContextTokens > 0 && card.ContextTokens == 0 {
			reject(card, fmt.Sprintf("context window is unstated but the slot requires >= %d tokens", requirement.MinContextTokens))
			continue
		}
		if excludeFamily != "" && strings.EqualFold(card.Family, excludeFamily) {
			reject(card, fmt.Sprintf("job requires a reviewer outside the %q family", excludeFamily))
			continue
		}
		eligible = append(eligible, scoredCandidate{card: card, score: scoreCandidate(card, requirement)})
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		if eligible[i].score != eligible[j].score {
			return eligible[i].score > eligible[j].score
		}
		left, right := eligible[i].card, eligible[j].card
		if left.Level(CapabilityReliability) != right.Level(CapabilityReliability) {
			return left.Level(CapabilityReliability) > right.Level(CapabilityReliability)
		}
		if totalLevels(left) != totalLevels(right) {
			return totalLevels(left) > totalLevels(right)
		}
		return left.Key < right.Key
	})
	return eligible, rejected
}

// scoreCandidate computes the weighted preference utility. Preferences are an
// ordered list: the first entry carries the most weight, so a job states its
// priorities without hand-tuned numbers.
func scoreCandidate(card AgentCard, requirement RoleRequirement) float64 {
	if len(requirement.Prefer) == 0 {
		// With no stated preference, rank by overall rating. The divisor keeps
		// these scores on the same order of magnitude as weighted ones.
		return float64(totalLevels(card)) / 10
	}
	score := 0.0
	weights := len(requirement.Prefer)
	for index, capability := range requirement.Prefer {
		weight := float64(weights - index)
		score += weight * float64(card.Level(capability))
	}
	return score
}

func totalLevels(card AgentCard) int {
	total := 0
	for _, capability := range sortedCapabilityKeys(card.Capabilities) {
		total += card.Capabilities[capability]
	}
	return total
}

func unmetRequirement(card AgentCard, requirement RoleRequirement) (string, bool) {
	for _, capability := range sortedCapabilityKeys(requirement.Require) {
		needed := requirement.Require[capability]
		if card.Level(capability) < needed {
			return fmt.Sprintf("%s=%s is below the required %s", capability, CapabilityLevelName(card.Level(capability)), CapabilityLevelName(needed)), false
		}
	}
	return "", true
}

func selectionReasons(card AgentCard, requirement RoleRequirement, excludeFamily string, req RoutingRequest) []string {
	reasons := make([]string, 0, 4)
	for _, capability := range sortedCapabilityKeys(requirement.Require) {
		reasons = append(reasons, fmt.Sprintf("%s=%s meets required %s", capability, CapabilityLevelName(card.Level(capability)), CapabilityLevelName(requirement.Require[capability])))
	}
	if len(requirement.Prefer) > 0 {
		preferred := make([]string, 0, len(requirement.Prefer))
		for _, capability := range requirement.Prefer {
			preferred = append(preferred, fmt.Sprintf("%s=%s", capability, CapabilityLevelName(card.Level(capability))))
		}
		reasons = append(reasons, "highest weighted preference score ("+strings.Join(preferred, ", ")+")")
	}
	if excludeFamily != "" {
		reasons = append(reasons, fmt.Sprintf("independent model family (%s, not %s)", card.Family, excludeFamily))
	}
	if req.AvailabilityProbed {
		reasons = append(reasons, fmt.Sprintf("adapter %s probed available", card.Target.Adapter))
	}
	return reasons
}

// validateRoutedTargetRole mirrors the claude role boundary enforced for
// resolved runs so the router never proposes a team that ResolveOptions would
// immediately reject.
func validateRoutedTargetRole(role Role, target RoleTarget) error {
	if target.Adapter != "claude" {
		return nil
	}
	switch role {
	case RoleSupervisor, RoleAdversary:
		return nil
	default:
		return unsupportedClaudeRoleError(string(role))
	}
}

func adapterAvailable(availability map[string]AdapterAvailability, adapter string) (bool, string) {
	if len(availability) == 0 {
		return true, ""
	}
	entry, ok := availability[adapter]
	if !ok {
		return true, ""
	}
	if entry.Available {
		return true, ""
	}
	if strings.TrimSpace(entry.Reason) == "" {
		return false, fmt.Sprintf("adapter %s is not available", adapter)
	}
	return false, fmt.Sprintf("adapter %s is not available: %s", adapter, entry.Reason)
}

func availabilityList(availability map[string]AdapterAvailability) []AdapterAvailability {
	entries := make([]AdapterAvailability, 0, len(availability))
	for _, entry := range availability {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Adapter < entries[j].Adapter })
	return entries
}

func isExcluded(card AgentCard, excluded []string) (bool, string) {
	target := roleTargetString(card.Target)
	for _, raw := range excluded {
		pattern := strings.TrimSpace(raw)
		if pattern == "" {
			continue
		}
		if strings.EqualFold(pattern, card.Key) ||
			strings.EqualFold(pattern, target) ||
			strings.EqualFold(pattern, card.Target.Adapter) {
			return true, pattern
		}
	}
	return false, ""
}

func normalizeExclusions(excluded []string) []string {
	out := make([]string, 0, len(excluded))
	seen := map[string]bool{}
	for _, raw := range excluded {
		value := strings.TrimSpace(raw)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func cardForTarget(roster []AgentCard, target RoleTarget) (AgentCard, bool) {
	wanted := roleTargetString(target)
	for _, card := range roster {
		if strings.EqualFold(roleTargetString(card.Target), wanted) {
			return card, true
		}
	}
	return AgentCard{}, false
}

func familyForTarget(roster []AgentCard, target RoleTarget) string {
	if card, ok := cardForTarget(roster, target); ok {
		return card.Family
	}
	return target.Adapter
}

// blockedOnlyByFamily reports whether the family-diversity constraint is the
// only thing standing between the slot and an otherwise eligible candidate.
func blockedOnlyByFamily(rejected []RoutingRejection, family string) bool {
	marker := fmt.Sprintf("outside the %q family", family)
	for _, entry := range rejected {
		if strings.Contains(entry.Reason, marker) {
			return true
		}
	}
	return false
}

func summarizeRejections(rejected []RoutingRejection) string {
	if len(rejected) == 0 {
		return "no agent cards are configured for this slot"
	}
	parts := make([]string, 0, len(rejected))
	for _, entry := range rejected {
		parts = append(parts, fmt.Sprintf("%s (%s)", entry.Agent, entry.Reason))
	}
	return "every candidate was rejected: " + strings.Join(parts, "; ")
}

func containsFold(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), wanted) {
			return true
		}
	}
	return false
}
