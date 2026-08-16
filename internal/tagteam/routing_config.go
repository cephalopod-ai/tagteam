package tagteam

import (
	"fmt"
	"sort"
	"strings"
)

// mergeRoutingConfig layers `[agents.*]` and `[jobs.*]` tables the same way the
// rest of the config merges: a later layer overrides the fields it sets and
// leaves the rest of an existing entry intact.
func mergeRoutingConfig(dst *Config, src Config) {
	if len(src.Agents) > 0 {
		if dst.Agents == nil {
			dst.Agents = map[string]AgentCardConfig{}
		}
		for key, card := range src.Agents {
			dst.Agents[key] = overlayAgentCardConfig(dst.Agents[key], card)
		}
	}
	if len(src.Jobs) > 0 {
		if dst.Jobs == nil {
			dst.Jobs = map[string]JobConfig{}
		}
		for key, job := range src.Jobs {
			dst.Jobs[key] = overlayJobConfig(dst.Jobs[key], job)
		}
	}
}

func overlayAgentCardConfig(base, src AgentCardConfig) AgentCardConfig {
	out := base
	if strings.TrimSpace(src.Target) != "" {
		out.Target = src.Target
	}
	if strings.TrimSpace(src.Family) != "" {
		out.Family = src.Family
	}
	if src.Roles != nil {
		out.Roles = append([]string(nil), src.Roles...)
	}
	if src.ContextTokens != 0 {
		out.ContextTokens = src.ContextTokens
	}
	if strings.TrimSpace(src.Notes) != "" {
		out.Notes = src.Notes
	}
	if src.Disabled != nil {
		disabled := *src.Disabled
		out.Disabled = &disabled
	}
	if len(src.Capabilities) > 0 {
		merged := map[string]string{}
		for key, value := range out.Capabilities {
			merged[key] = value
		}
		for key, value := range src.Capabilities {
			merged[key] = value
		}
		out.Capabilities = merged
	}
	return out
}

func overlayJobConfig(base, src JobConfig) JobConfig {
	out := base
	if strings.TrimSpace(src.Description) != "" {
		out.Description = src.Description
	}
	if strings.TrimSpace(src.Mode) != "" {
		out.Mode = src.Mode
	}
	if src.Rounds != 0 {
		out.Rounds = src.Rounds
	}
	if src.DiverseReviewer != nil {
		diverse := *src.DiverseReviewer
		out.DiverseReviewer = &diverse
	}
	if len(src.Roles) > 0 {
		merged := map[string]JobRoleConfig{}
		for key, value := range out.Roles {
			merged[key] = value
		}
		for key, value := range src.Roles {
			merged[key] = overlayJobRoleConfig(merged[key], value)
		}
		out.Roles = merged
	}
	return out
}

func overlayJobRoleConfig(base, src JobRoleConfig) JobRoleConfig {
	out := base
	if len(src.Require) > 0 {
		merged := map[string]string{}
		for key, value := range out.Require {
			merged[key] = value
		}
		for key, value := range src.Require {
			merged[key] = value
		}
		out.Require = merged
	}
	if src.Prefer != nil {
		out.Prefer = append([]string(nil), src.Prefer...)
	}
	if src.MinContextTokens != 0 {
		out.MinContextTokens = src.MinContextTokens
	}
	if src.Candidates != nil {
		out.Candidates = append([]string(nil), src.Candidates...)
	}
	return out
}

// agentRosterConfig returns the built-in roster overlaid with configured cards.
func agentRosterConfig(cfg Config) map[string]AgentCardConfig {
	roster := map[string]AgentCardConfig{}
	for key, card := range defaultAgentRosterConfig() {
		roster[key] = card
	}
	for key, card := range cfg.Agents {
		roster[key] = overlayAgentCardConfig(roster[key], card)
	}
	return roster
}

// jobCatalogConfig returns the built-in job catalog overlaid with configured jobs.
func jobCatalogConfig(cfg Config) map[string]JobConfig {
	catalog := map[string]JobConfig{}
	for key, job := range defaultJobCatalogConfig() {
		catalog[key] = job
	}
	for key, job := range cfg.Jobs {
		catalog[key] = overlayJobConfig(catalog[key], job)
	}
	return catalog
}

// ResolveAgentRoster returns every configured agent card in a stable order.
func ResolveAgentRoster(cfg Config) ([]AgentCard, error) {
	raw := agentRosterConfig(cfg)
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	cards := make([]AgentCard, 0, len(keys))
	for _, key := range keys {
		card, err := resolveAgentCard(key, raw[key])
		if err != nil {
			return nil, err
		}
		cards = append(cards, card)
	}
	return cards, nil
}

func resolveAgentCard(key string, cfg AgentCardConfig) (AgentCard, error) {
	target, err := ParseRoleTarget(cfg.Target)
	if err != nil {
		return AgentCard{}, fmt.Errorf("agents.%s.target: %w", key, err)
	}
	card := AgentCard{
		Key:           key,
		Target:        target,
		Family:        strings.TrimSpace(cfg.Family),
		ContextTokens: cfg.ContextTokens,
		Capabilities:  map[Capability]int{},
		Notes:         strings.TrimSpace(cfg.Notes),
	}
	if card.Family == "" {
		// Without an explicit family, the adapter id is the coarsest honest
		// proxy for "independent model family".
		card.Family = target.Adapter
	}
	if cfg.ContextTokens < 0 {
		return AgentCard{}, fmt.Errorf("agents.%s.context_tokens must be >= 0", key)
	}
	if cfg.Disabled != nil {
		card.Disabled = *cfg.Disabled
	}
	for _, rawSlot := range cfg.Roles {
		slot, err := ParseRoleSlot(rawSlot)
		if err != nil {
			return AgentCard{}, fmt.Errorf("agents.%s.roles: %w", key, err)
		}
		card.Slots = append(card.Slots, slot)
	}
	for rawName, rawLevel := range cfg.Capabilities {
		capability, err := ParseCapability(rawName)
		if err != nil {
			return AgentCard{}, fmt.Errorf("agents.%s.capabilities: %w", key, err)
		}
		level, err := ParseCapabilityLevel(rawLevel)
		if err != nil {
			return AgentCard{}, fmt.Errorf("agents.%s.capabilities.%s: %w", key, capability, err)
		}
		card.Capabilities[capability] = level
	}
	return card, nil
}

// JobNames lists every configured job name in a stable order.
func JobNames(cfg Config) []string {
	catalog := jobCatalogConfig(cfg)
	names := make([]string, 0, len(catalog))
	for name := range catalog {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ResolveJob resolves one named job into its validated specification.
func ResolveJob(cfg Config, name string) (JobSpec, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return JobSpec{}, fmt.Errorf("job name is empty")
	}
	catalog := jobCatalogConfig(cfg)
	raw, ok := catalog[trimmed]
	if !ok {
		return JobSpec{}, fmt.Errorf("unknown job %q (configured jobs: %s)", trimmed, strings.Join(JobNames(cfg), ", "))
	}
	return resolveJobSpec(trimmed, raw)
}

func resolveJobSpec(name string, cfg JobConfig) (JobSpec, error) {
	mode, err := ParseMode(cfg.Mode)
	if err != nil {
		return JobSpec{}, fmt.Errorf("jobs.%s.mode: %w", name, err)
	}
	if cfg.Rounds < 0 {
		return JobSpec{}, fmt.Errorf("jobs.%s.rounds must be >= 0", name)
	}
	spec := JobSpec{
		Name:        name,
		Description: strings.TrimSpace(cfg.Description),
		Mode:        mode,
		Rounds:      cfg.Rounds,
		Roles:       map[RoleSlot]RoleRequirement{},
	}
	if cfg.DiverseReviewer != nil {
		spec.DiverseReviewer = *cfg.DiverseReviewer
	}
	for rawSlot, roleCfg := range cfg.Roles {
		slot, err := ParseRoleSlot(rawSlot)
		if err != nil {
			return JobSpec{}, fmt.Errorf("jobs.%s.roles: %w", name, err)
		}
		requirement := RoleRequirement{
			Slot:             slot,
			Require:          map[Capability]int{},
			MinContextTokens: roleCfg.MinContextTokens,
			Candidates:       append([]string(nil), roleCfg.Candidates...),
		}
		if roleCfg.MinContextTokens < 0 {
			return JobSpec{}, fmt.Errorf("jobs.%s.roles.%s.min_context_tokens must be >= 0", name, slot)
		}
		for rawName, rawLevel := range roleCfg.Require {
			capability, err := ParseCapability(rawName)
			if err != nil {
				return JobSpec{}, fmt.Errorf("jobs.%s.roles.%s.require: %w", name, slot, err)
			}
			level, err := ParseCapabilityLevel(rawLevel)
			if err != nil {
				return JobSpec{}, fmt.Errorf("jobs.%s.roles.%s.require.%s: %w", name, slot, capability, err)
			}
			requirement.Require[capability] = level
		}
		for _, rawName := range roleCfg.Prefer {
			capability, err := ParseCapability(rawName)
			if err != nil {
				return JobSpec{}, fmt.Errorf("jobs.%s.roles.%s.prefer: %w", name, slot, err)
			}
			requirement.Prefer = append(requirement.Prefer, capability)
		}
		spec.Roles[slot] = requirement
	}
	for _, slot := range SlotsForMode(spec.Mode) {
		if _, ok := spec.Roles[slot]; !ok {
			// An unstated slot is still staffed by the router; it simply has
			// no requirements beyond the adapter role boundary.
			spec.Roles[slot] = RoleRequirement{Slot: slot, Require: map[Capability]int{}}
		}
	}
	return spec, nil
}

// validateRoutingConfig fails config load when a card or job cannot resolve,
// so a typo surfaces at load time rather than at selection time.
func validateRoutingConfig(cfg Config) error {
	if _, err := ResolveAgentRoster(cfg); err != nil {
		return err
	}
	for _, name := range JobNames(cfg) {
		if _, err := ResolveJob(cfg, name); err != nil {
			return err
		}
	}
	return nil
}
