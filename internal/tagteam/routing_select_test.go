package tagteam

import (
	"strings"
	"testing"
	"time"
)

func testRoutingConfig() Config {
	cfg := DefaultConfig()
	return cfg
}

func routeOrFatal(t *testing.T, cfg Config, req RoutingRequest) RoutingDecision {
	t.Helper()
	decision, err := Route(cfg, req)
	if err != nil {
		t.Fatalf("Route() error = %v", err)
	}
	return decision
}

func roleFor(t *testing.T, decision RoutingDecision, slot RoleSlot) RoleRouting {
	t.Helper()
	for _, role := range decision.Roles {
		if role.Slot == string(slot) {
			return role
		}
	}
	t.Fatalf("decision has no %s slot: %+v", slot, decision.Roles)
	return RoleRouting{}
}

func jobOrFatal(t *testing.T, cfg Config, name string) JobSpec {
	t.Helper()
	spec, err := ResolveJob(cfg, name)
	if err != nil {
		t.Fatalf("ResolveJob(%q) error = %v", name, err)
	}
	return spec
}

// Every built-in card must be legal for every slot it advertises; otherwise the
// router could propose a team that ResolveOptions immediately rejects.
func TestDefaultRosterRespectsRoleBoundaries(t *testing.T) {
	roster, err := ResolveAgentRoster(testRoutingConfig())
	if err != nil {
		t.Fatalf("ResolveAgentRoster() error = %v", err)
	}
	if len(roster) == 0 {
		t.Fatal("default roster is empty")
	}
	modes := []Mode{ModeSupervisor, ModeAdversarial, ModeRelay}
	for _, card := range roster {
		for _, slot := range card.Slots {
			for _, mode := range modes {
				role := SlotRole(slot, mode)
				if err := ValidateRoleTarget(role, card.Target); err != nil {
					t.Fatalf("card %s advertises slot %s but %s role is rejected: %v", card.Key, slot, role, err)
				}
				if err := validateRoutedTargetRole(role, card.Target); err != nil {
					t.Fatalf("card %s advertises slot %s but %s role is rejected: %v", card.Key, slot, role, err)
				}
			}
		}
	}
}

func TestDefaultJobsResolveAndStaffEverySlot(t *testing.T) {
	cfg := testRoutingConfig()
	names := JobNames(cfg)
	if len(names) == 0 {
		t.Fatal("default job catalog is empty")
	}
	for _, name := range names {
		spec := jobOrFatal(t, cfg, name)
		decision := routeOrFatal(t, cfg, RoutingRequest{Job: spec, Mode: spec.Mode, Now: time.Unix(0, 0)})
		wanted := SlotsForMode(spec.Mode)
		if len(decision.Roles) != len(wanted) {
			t.Fatalf("job %s staffed %d slots, want %d", name, len(decision.Roles), len(wanted))
		}
		for _, slot := range wanted {
			role := roleFor(t, decision, slot)
			if role.Selected == "" {
				t.Fatalf("job %s left slot %s unstaffed", name, slot)
			}
			if _, err := ParseRoleTarget(role.Selected); err != nil {
				t.Fatalf("job %s slot %s selected an unparsable target %q: %v", name, slot, role.Selected, err)
			}
		}
	}
}

func TestRouteSelectionIsDeterministic(t *testing.T) {
	cfg := testRoutingConfig()
	spec := jobOrFatal(t, cfg, "scoped_patch")
	first := routeOrFatal(t, cfg, RoutingRequest{Job: spec, Mode: spec.Mode, Now: time.Unix(0, 0)})
	for i := 0; i < 5; i++ {
		next := routeOrFatal(t, cfg, RoutingRequest{Job: spec, Mode: spec.Mode, Now: time.Unix(0, 0)})
		for index := range first.Roles {
			if first.Roles[index].Selected != next.Roles[index].Selected {
				t.Fatalf("routing is not deterministic: %s != %s", first.Roles[index].Selected, next.Roles[index].Selected)
			}
		}
	}
}

func TestRouteDiverseReviewerRejectsEditorFamily(t *testing.T) {
	cfg := testRoutingConfig()
	spec := jobOrFatal(t, cfg, "audit")
	decision := routeOrFatal(t, cfg, RoutingRequest{Job: spec, Mode: spec.Mode})
	editor := roleFor(t, decision, SlotEditor)
	reviewer := roleFor(t, decision, SlotReviewer)
	if editor.Family == "" || reviewer.Family == "" {
		t.Fatalf("expected both slots to record a family, got editor=%q reviewer=%q", editor.Family, reviewer.Family)
	}
	if editor.Family == reviewer.Family {
		t.Fatalf("audit selected the same family for both slots: %s", editor.Family)
	}
	rejectedSameFamily := false
	for _, rejected := range reviewer.Rejected {
		if strings.Contains(rejected.Reason, "outside the") {
			rejectedSameFamily = true
		}
	}
	if !rejectedSameFamily {
		t.Fatalf("expected a recorded family-diversity rejection, got %+v", reviewer.Rejected)
	}
}

func TestRouteHonorsOperatorPinAndDiversityAgainstIt(t *testing.T) {
	cfg := testRoutingConfig()
	spec := jobOrFatal(t, cfg, "audit")
	decision := routeOrFatal(t, cfg, RoutingRequest{
		Job:      spec,
		Mode:     spec.Mode,
		Operator: map[RoleSlot]RoleTarget{SlotEditor: {Adapter: "grok", Model: "grok-4.5"}},
	})
	editor := roleFor(t, decision, SlotEditor)
	if editor.Source != RoutingSourceOperator {
		t.Fatalf("pinned editor source = %q, want %q", editor.Source, RoutingSourceOperator)
	}
	if editor.Selected != "grok:grok-4.5" {
		t.Fatalf("pinned editor = %q, want grok:grok-4.5", editor.Selected)
	}
	reviewer := roleFor(t, decision, SlotReviewer)
	if reviewer.Family == "xai" {
		t.Fatalf("reviewer must avoid the pinned editor family, got %q", reviewer.Family)
	}
}

func TestRouteExclusionRemovesCandidates(t *testing.T) {
	cfg := testRoutingConfig()
	spec := jobOrFatal(t, cfg, "audit")
	decision := routeOrFatal(t, cfg, RoutingRequest{Job: spec, Mode: spec.Mode, Excluded: []string{"opus"}})
	reviewer := roleFor(t, decision, SlotReviewer)
	if reviewer.Agent == "opus" {
		t.Fatalf("excluded agent was still selected: %s", reviewer.Selected)
	}
	found := false
	for _, rejected := range reviewer.Rejected {
		if strings.Contains(rejected.Reason, `excluded by "opus"`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an exclusion rejection reason, got %+v", reviewer.Rejected)
	}
}

func TestRouteUnavailableAdapterIsRejectedWithReason(t *testing.T) {
	cfg := testRoutingConfig()
	spec := jobOrFatal(t, cfg, "scoped_patch")
	decision := routeOrFatal(t, cfg, RoutingRequest{
		Job:  spec,
		Mode: spec.Mode,
		Availability: map[string]AdapterAvailability{
			"codex":  {Adapter: "codex", Available: false, Reason: "not installed"},
			"claude": {Adapter: "claude", Available: true},
			"grok":   {Adapter: "grok", Available: true},
		},
		AvailabilityProbed: true,
	})
	editor := roleFor(t, decision, SlotEditor)
	if strings.HasPrefix(editor.Selected, "codex:") {
		t.Fatalf("unavailable adapter was selected: %s", editor.Selected)
	}
	if decision.AvailabilityMode != AvailabilityModeProbed {
		t.Fatalf("availability mode = %q, want %q", decision.AvailabilityMode, AvailabilityModeProbed)
	}
	if len(decision.Availability) == 0 {
		t.Fatal("probed decision should record availability facts")
	}
	found := false
	for _, rejected := range editor.Rejected {
		if strings.Contains(rejected.Reason, "not installed") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an availability rejection reason, got %+v", editor.Rejected)
	}
}

func TestRouteFailsLoudlyWhenASlotCannotBeStaffed(t *testing.T) {
	cfg := testRoutingConfig()
	spec := jobOrFatal(t, cfg, "scoped_patch")
	_, err := Route(cfg, RoutingRequest{
		Job:      spec,
		Mode:     spec.Mode,
		Excluded: []string{"codex", "grok", "claude", "agy", "openai-compatible"},
	})
	if err == nil {
		t.Fatal("expected an error when every candidate is excluded")
	}
	if ExitCode(err) != ExitInvalidArguments {
		t.Fatalf("exit code = %d, want %d", ExitCode(err), ExitInvalidArguments)
	}
	if !strings.Contains(err.Error(), "cannot staff the editor slot") {
		t.Fatalf("error should name the unstaffable slot, got %v", err)
	}
}

func TestRouteMinContextTokensFiltersSmallWindows(t *testing.T) {
	cfg := testRoutingConfig()
	spec := jobOrFatal(t, cfg, "deep_scan")
	decision := routeOrFatal(t, cfg, RoutingRequest{Job: spec, Mode: spec.Mode})
	scout := roleFor(t, decision, SlotScout)
	if scout.Selected != "agy:"+agyGemini36FlashMedium {
		t.Fatalf("deep_scan scout = %q, want the large-context scout", scout.Selected)
	}
	found := false
	for _, rejected := range scout.Rejected {
		if rejected.Agent == "gemma-local" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the small-window scout to be rejected, got %+v", scout.Rejected)
	}
}

func TestRouteRecordsRankedFallbacks(t *testing.T) {
	cfg := testRoutingConfig()
	spec := jobOrFatal(t, cfg, "scoped_patch")
	decision := routeOrFatal(t, cfg, RoutingRequest{Job: spec, Mode: spec.Mode})
	editor := roleFor(t, decision, SlotEditor)
	if len(editor.Fallbacks) == 0 {
		t.Fatal("expected ranked fallbacks for the editor slot")
	}
	if len(editor.Fallbacks) > maxRoutingFallbacks {
		t.Fatalf("fallbacks = %d, want at most %d", len(editor.Fallbacks), maxRoutingFallbacks)
	}
	for _, fallback := range editor.Fallbacks {
		if fallback == editor.Selected {
			t.Fatalf("fallback list repeats the primary selection: %s", fallback)
		}
	}
}

func TestRouteNotesOperatorWorkflowOverride(t *testing.T) {
	cfg := testRoutingConfig()
	spec := jobOrFatal(t, cfg, "audit")
	decision := routeOrFatal(t, cfg, RoutingRequest{Job: spec, Mode: ModeSupervisor})
	if decision.Workflow != ModeSupervisor {
		t.Fatalf("workflow = %q, want %q", decision.Workflow, ModeSupervisor)
	}
	if decision.WorkflowSource != RoutingSourceOperator {
		t.Fatalf("workflow source = %q, want %q", decision.WorkflowSource, RoutingSourceOperator)
	}
	if len(decision.Notes) == 0 {
		t.Fatal("expected a note recording the workflow override")
	}
}

func TestCapabilityParsing(t *testing.T) {
	if _, err := ParseCapability("coding"); err != nil {
		t.Fatalf("ParseCapability(coding) error = %v", err)
	}
	if _, err := ParseCapability("vibes"); err == nil {
		t.Fatal("expected an error for an unknown capability")
	}
	level, err := ParseCapabilityLevel("high")
	if err != nil || level != CapabilityLevelHigh {
		t.Fatalf("ParseCapabilityLevel(high) = %d, %v", level, err)
	}
	if level, err := ParseCapabilityLevel("3"); err != nil || level != CapabilityLevelHigh {
		t.Fatalf("ParseCapabilityLevel(3) = %d, %v", level, err)
	}
	if _, err := ParseCapabilityLevel("9"); err == nil {
		t.Fatal("expected an out-of-range error")
	}
}

// Family diversity is a stated job requirement, so an unstaffable reviewer
// fails with an actionable message instead of quietly reviewing in-family.
func TestRouteDiversityFailureExplainsHowToRelaxIt(t *testing.T) {
	cfg := testRoutingConfig()
	spec := jobOrFatal(t, cfg, "audit")
	_, err := Route(cfg, RoutingRequest{Job: spec, Mode: spec.Mode, Excluded: []string{"claude"}})
	if err == nil {
		t.Fatal("expected an error when only same-family reviewers remain")
	}
	if !strings.Contains(err.Error(), "diverse_reviewer = false") {
		t.Fatalf("error should explain how to relax the constraint, got %v", err)
	}
}
