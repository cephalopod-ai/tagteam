package tagteam

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// RoutingSchemaVersion versions the persisted routing.json contract.
const RoutingSchemaVersion = 1

// Capability is one dimension of the agent capability vocabulary the router
// scores against. The vocabulary is closed: unknown names are configuration
// errors rather than silently ignored keys.
type Capability string

const (
	CapabilityCoding      Capability = "coding"
	CapabilityReasoning   Capability = "reasoning"
	CapabilityResearch    Capability = "research"
	CapabilityPlanning    Capability = "planning"
	CapabilityAudit       Capability = "audit"
	CapabilityContext     Capability = "context"
	CapabilityToolUse     Capability = "tool_use"
	CapabilityAutonomy    Capability = "autonomy"
	CapabilitySpeed       Capability = "speed"
	CapabilityReliability Capability = "reliability"
	// CapabilityCost rates cost efficiency, not price: a higher level means
	// the agent is cheaper to run for the same work.
	CapabilityCost Capability = "cost"
)

// KnownCapabilities returns the closed capability vocabulary in a stable order.
func KnownCapabilities() []Capability {
	return []Capability{
		CapabilityCoding,
		CapabilityReasoning,
		CapabilityResearch,
		CapabilityPlanning,
		CapabilityAudit,
		CapabilityContext,
		CapabilityToolUse,
		CapabilityAutonomy,
		CapabilitySpeed,
		CapabilityReliability,
		CapabilityCost,
	}
}

// Capability levels are a 0..4 ordinal scale. They are editorial priors, not
// measurements: operators are expected to retune them per roster.
const (
	CapabilityLevelNone   = 0
	CapabilityLevelLow    = 1
	CapabilityLevelMedium = 2
	CapabilityLevelHigh   = 3
	CapabilityLevelMax    = 4
)

// RoleSlot is the mode-independent name of a team position. Modes rename the
// same slots (worker/coder, supervisor/adversary), so jobs are written against
// slots and resolved to concrete roles once the workflow is known.
type RoleSlot string

const (
	SlotEditor   RoleSlot = "editor"
	SlotReviewer RoleSlot = "reviewer"
	SlotScout    RoleSlot = "scout"
)

// ParseCapability validates a capability name against the closed vocabulary.
func ParseCapability(raw string) (Capability, error) {
	name := strings.ToLower(strings.TrimSpace(raw))
	for _, known := range KnownCapabilities() {
		if string(known) == name {
			return known, nil
		}
	}
	return "", fmt.Errorf("unknown capability %q (want one of %s)", raw, capabilityVocabulary())
}

func capabilityVocabulary() string {
	names := make([]string, 0, len(KnownCapabilities()))
	for _, known := range KnownCapabilities() {
		names = append(names, string(known))
	}
	return strings.Join(names, ", ")
}

// ParseCapabilityLevel accepts either a level word (none/low/medium/high/max)
// or its numeric equivalent in 0..4.
func ParseCapabilityLevel(raw string) (int, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	switch value {
	case "none":
		return CapabilityLevelNone, nil
	case "low":
		return CapabilityLevelLow, nil
	case "medium", "med":
		return CapabilityLevelMedium, nil
	case "high":
		return CapabilityLevelHigh, nil
	case "max", "very_high":
		return CapabilityLevelMax, nil
	}
	level, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("invalid capability level %q (want none, low, medium, high, max, or 0-4)", raw)
	}
	if level < CapabilityLevelNone || level > CapabilityLevelMax {
		return 0, fmt.Errorf("capability level %d is out of range (want 0-4)", level)
	}
	return level, nil
}

// CapabilityLevelName renders a level back to its canonical word.
func CapabilityLevelName(level int) string {
	switch level {
	case CapabilityLevelNone:
		return "none"
	case CapabilityLevelLow:
		return "low"
	case CapabilityLevelMedium:
		return "medium"
	case CapabilityLevelHigh:
		return "high"
	case CapabilityLevelMax:
		return "max"
	default:
		return strconv.Itoa(level)
	}
}

// ParseRoleSlot validates a job role-slot key.
func ParseRoleSlot(raw string) (RoleSlot, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(SlotEditor):
		return SlotEditor, nil
	case string(SlotReviewer):
		return SlotReviewer, nil
	case string(SlotScout):
		return SlotScout, nil
	default:
		return "", fmt.Errorf("unknown job role slot %q (want editor, reviewer, or scout)", raw)
	}
}

// AgentCardConfig is one `[agents.<key>]` entry: which target it names, which
// slots it may fill, and how it rates on the capability vocabulary.
type AgentCardConfig struct {
	Target        string            `toml:"target"`
	Family        string            `toml:"family"`
	Roles         []string          `toml:"roles"`
	ContextTokens int               `toml:"context_tokens"`
	Capabilities  map[string]string `toml:"capabilities"`
	Disabled      *bool             `toml:"disabled"`
	Notes         string            `toml:"notes"`
}

// JobRoleConfig states what one slot of a job needs.
type JobRoleConfig struct {
	Require          map[string]string `toml:"require"`
	Prefer           []string          `toml:"prefer"`
	MinContextTokens int               `toml:"min_context_tokens"`
	Candidates       []string          `toml:"candidates"`
}

// JobConfig is one `[jobs.<name>]` entry: the workflow it resolves to and the
// per-slot requirements the router selects against.
type JobConfig struct {
	Description     string                   `toml:"description"`
	Mode            string                   `toml:"mode"`
	Rounds          int                      `toml:"rounds"`
	DiverseReviewer *bool                    `toml:"diverse_reviewer"`
	Roles           map[string]JobRoleConfig `toml:"roles"`
}

// AgentCard is the resolved, validated form of AgentCardConfig.
type AgentCard struct {
	Key           string
	Target        RoleTarget
	Family        string
	Slots         []RoleSlot
	ContextTokens int
	Capabilities  map[Capability]int
	Disabled      bool
	Notes         string
}

// Level returns the card's rating for one capability (0 when unrated).
func (c AgentCard) Level(capability Capability) int {
	return c.Capabilities[capability]
}

// AllowsSlot reports whether the card opts into a slot. An empty slot list
// means "any slot the adapter role boundary already permits".
func (c AgentCard) AllowsSlot(slot RoleSlot) bool {
	if len(c.Slots) == 0 {
		return true
	}
	for _, allowed := range c.Slots {
		if allowed == slot {
			return true
		}
	}
	return false
}

// RoleRequirement is the resolved, validated form of JobRoleConfig.
type RoleRequirement struct {
	Slot             RoleSlot
	Require          map[Capability]int
	Prefer           []Capability
	MinContextTokens int
	Candidates       []string
}

// JobSpec is the resolved, validated form of JobConfig.
type JobSpec struct {
	Name            string
	Description     string
	Mode            Mode
	Rounds          int
	DiverseReviewer bool
	Roles           map[RoleSlot]RoleRequirement
}

// SlotsForMode returns the slots a workflow actually staffs, in selection
// order. Reviewer selection depends on the chosen editor for family
// diversity, so editor is always resolved first.
func SlotsForMode(mode Mode) []RoleSlot {
	switch mode {
	case ModeSolo:
		return []RoleSlot{SlotEditor}
	case ModeRelay:
		return []RoleSlot{SlotEditor, SlotReviewer, SlotScout}
	default:
		return []RoleSlot{SlotEditor, SlotReviewer}
	}
}

// SlotRole maps a slot to the role used for adapter boundary validation.
func SlotRole(slot RoleSlot, mode Mode) Role {
	switch slot {
	case SlotScout:
		return RoleScout
	case SlotReviewer:
		if mode == ModeAdversarial {
			return RoleAdversary
		}
		return RoleSupervisor
	default:
		return RoleCoder
	}
}

// SlotLabel maps a slot to the operator-facing role label for a mode
// (worker/coder/solo, supervisor/adversary, scout).
func SlotLabel(slot RoleSlot, mode Mode) string {
	editorLabel, reviewerLabel := roleLabels(mode)
	switch slot {
	case SlotScout:
		return "scout"
	case SlotReviewer:
		return reviewerLabel
	default:
		return editorLabel
	}
}

// RoutingDecision is the persisted record of one routing pass. It is written
// to routing.json before any agent runs and echoed in final.json so a
// heterogeneous team composition can be audited after the fact.
type RoutingDecision struct {
	SchemaVersion    int                   `json:"schema_version"`
	Job              string                `json:"job"`
	JobDescription   string                `json:"job_description,omitempty"`
	Workflow         Mode                  `json:"workflow"`
	WorkflowSource   string                `json:"workflow_source"`
	Rounds           int                   `json:"rounds,omitempty"`
	RoundsSource     string                `json:"rounds_source,omitempty"`
	GeneratedAt      time.Time             `json:"generated_at"`
	AvailabilityMode string                `json:"availability_mode"`
	Roles            []RoleRouting         `json:"roles"`
	Availability     []AdapterAvailability `json:"availability,omitempty"`
	Excluded         []string              `json:"excluded,omitempty"`
	Notes            []string              `json:"notes,omitempty"`
}

// RoleRouting records the outcome for one staffed slot.
type RoleRouting struct {
	Slot      string             `json:"slot"`
	Label     string             `json:"label"`
	Role      string             `json:"role"`
	Source    string             `json:"source"`
	Agent     string             `json:"agent,omitempty"`
	Selected  string             `json:"selected"`
	Family    string             `json:"family,omitempty"`
	Score     float64            `json:"score,omitempty"`
	Reasons   []string           `json:"reasons,omitempty"`
	Fallbacks []string           `json:"fallbacks,omitempty"`
	Rejected  []RoutingRejection `json:"rejected,omitempty"`
}

// RoutingRejection explains why a candidate was not eligible for a slot.
type RoutingRejection struct {
	Agent  string `json:"agent"`
	Target string `json:"target,omitempty"`
	Reason string `json:"reason"`
}

// AdapterAvailability is one probed (or assumed) adapter availability fact.
type AdapterAvailability struct {
	Adapter   string `json:"adapter"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// Routing source values recorded per slot and for the workflow.
const (
	RoutingSourceRouter   = "router"
	RoutingSourceOperator = "operator"
	RoutingSourceJob      = "job"
	RoutingSourceConfig   = "config"
)

// Availability modes recorded in routing.json.
const (
	AvailabilityModeAssumed = "assumed"
	AvailabilityModeProbed  = "probed"
)

func sortedCapabilityKeys(values map[Capability]int) []Capability {
	keys := make([]Capability, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}
