package tagteam

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// jobRouting is the applied result of one routing pass: the decision record
// plus the raw targets and fallback chains it contributes to the resolved run.
type jobRouting struct {
	Spec      JobSpec
	Decision  RoutingDecision
	Targets   map[RoleSlot]string
	Fallbacks map[RoleSlot][]string
}

// resolveJobForFlags resolves --job into a validated specification. It returns
// enabled=false when the operator did not select a job, which keeps every
// existing resolution path byte-for-byte unchanged.
func resolveJobForFlags(cfg Config, flags FlagInputs) (JobSpec, bool, error) {
	name := strings.TrimSpace(flags.Job)
	if name == "" {
		return JobSpec{}, false, nil
	}
	spec, err := ResolveJob(cfg, name)
	if err != nil {
		return JobSpec{}, false, &ExitError{Code: ExitInvalidArguments, Err: err}
	}
	return spec, true, nil
}

// routeJob runs the selection pass for a resolved workflow. Slots the operator
// pinned by flag or profile are passed through as-is and recorded with an
// operator source rather than re-selected.
func routeJob(cfg Config, spec JobSpec, flags FlagInputs, mode Mode, pinned map[RoleSlot]RoleTarget) (*jobRouting, error) {
	decision, err := Route(cfg, RoutingRequest{
		Job:                spec,
		Mode:               mode,
		Operator:           pinned,
		Excluded:           flags.RouteExclude,
		Availability:       flags.RouteAvailability,
		AvailabilityProbed: len(flags.RouteAvailability) > 0,
	})
	if err != nil {
		return nil, err
	}
	applied := &jobRouting{
		Spec:      spec,
		Decision:  decision,
		Targets:   map[RoleSlot]string{},
		Fallbacks: map[RoleSlot][]string{},
	}
	for _, routing := range decision.Roles {
		if routing.Source != RoutingSourceRouter {
			continue
		}
		slot, err := ParseRoleSlot(routing.Slot)
		if err != nil {
			return nil, &ExitError{Code: ExitInvalidArguments, Err: err}
		}
		applied.Targets[slot] = routing.Selected
		if len(routing.Fallbacks) > 0 {
			applied.Fallbacks[slot] = append([]string(nil), routing.Fallbacks...)
		}
	}
	return applied, nil
}

// pinnedRoutingSlots reports which slots the operator already fixed for this
// invocation. A pinned slot is authoritative: routing never overrides an
// explicit --worker/--supervisor/--scout flag or a profile role key.
func pinnedRoutingSlots(mode Mode, editorRaw, reviewerRaw, scoutRaw string, editorPinned, reviewerPinned, scoutPinned bool) map[RoleSlot]RoleTarget {
	pinned := map[RoleSlot]RoleTarget{}
	add := func(slot RoleSlot, raw string, isPinned bool) {
		if !isPinned {
			return
		}
		target, err := ParseRoleTarget(raw)
		if err != nil {
			// A malformed explicit target is reported by the normal target
			// parsing below; treating it as unpinned here would silently
			// replace it with a routed agent instead.
			return
		}
		pinned[slot] = target
	}
	add(SlotEditor, editorRaw, editorPinned)
	if mode != ModeSolo {
		add(SlotReviewer, reviewerRaw, reviewerPinned)
	}
	if mode == ModeRelay {
		add(SlotScout, scoutRaw, scoutPinned)
	}
	return pinned
}

// mergeRoutingFallbacks appends the router's ranked alternates behind whatever
// fallbacks the operator or config already configured. Routing only ever adds
// options; it never removes a configured fallback.
func mergeRoutingFallbacks(dst *RoleFallbacks, mode Mode, routed map[RoleSlot][]string) {
	if dst == nil || len(routed) == 0 {
		return
	}
	if values, ok := routed[SlotEditor]; ok {
		dst.Worker = append(append([]string(nil), dst.Worker...), values...)
	}
	if values, ok := routed[SlotReviewer]; ok {
		if mode == ModeAdversarial {
			dst.Reviewer = append(append([]string(nil), dst.Reviewer...), values...)
		} else {
			dst.Supervisor = append(append([]string(nil), dst.Supervisor...), values...)
		}
	}
	if values, ok := routed[SlotScout]; ok {
		dst.Scout = append(append([]string(nil), dst.Scout...), values...)
	}
}

// applyRoutedTargets rewrites the raw role targets for every slot the router
// staffed. Operator-pinned slots keep the value they already resolved to.
func applyRoutedTargets(applied *jobRouting, editorRaw, reviewerRaw, scoutRaw *string) {
	if applied == nil {
		return
	}
	if value, ok := applied.Targets[SlotEditor]; ok && editorRaw != nil {
		*editorRaw = value
	}
	if value, ok := applied.Targets[SlotReviewer]; ok && reviewerRaw != nil {
		*reviewerRaw = value
	}
	if value, ok := applied.Targets[SlotScout]; ok && scoutRaw != nil {
		*scoutRaw = value
	}
}

// persistRoutingDecision writes routing.json before any agent runs so the
// composition of a heterogeneous team is auditable independently of the
// outcome recorded in final.json.
func persistRoutingDecision(ctx context.Context, opts RunOptions, runDir string) error {
	if opts.Routing == nil {
		return nil
	}
	current, err := rebindControlResumeFromContext(ctx, runDir, nil, routingArtifactName)
	if err != nil {
		return &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	return writeJSON(filepath.Join(current, routingArtifactName), opts.Routing)
}

const routingArtifactName = "routing.json"

// FormatRoutingDecision renders a routing decision for terminal output.
func FormatRoutingDecision(decision RoutingDecision) string {
	var b strings.Builder
	fmt.Fprintf(&b, "job:      %s\n", decision.Job)
	if decision.JobDescription != "" {
		fmt.Fprintf(&b, "          %s\n", decision.JobDescription)
	}
	fmt.Fprintf(&b, "workflow: %s (%s)\n", decision.Workflow, decision.WorkflowSource)
	if decision.Rounds > 0 {
		source := decision.RoundsSource
		if source == "" {
			source = RoutingSourceJob
		}
		fmt.Fprintf(&b, "rounds:   %d (%s)\n", decision.Rounds, source)
	}
	fmt.Fprintf(&b, "agents:   selected with availability %s\n", decision.AvailabilityMode)
	for _, role := range decision.Roles {
		fmt.Fprintf(&b, "\n  %s (%s, %s)\n", role.Label, role.Slot, role.Source)
		fmt.Fprintf(&b, "    target:    %s", role.Selected)
		if role.Agent != "" {
			fmt.Fprintf(&b, "  [%s]", role.Agent)
		}
		b.WriteString("\n")
		for _, reason := range role.Reasons {
			fmt.Fprintf(&b, "    reason:    %s\n", reason)
		}
		if len(role.Fallbacks) > 0 {
			fmt.Fprintf(&b, "    fallbacks: %s\n", strings.Join(role.Fallbacks, ", "))
		}
		for _, rejected := range role.Rejected {
			fmt.Fprintf(&b, "    rejected:  %s (%s)\n", rejected.Agent, rejected.Reason)
		}
	}
	for _, entry := range decision.Availability {
		status := "available"
		if !entry.Available {
			status = "unavailable: " + entry.Reason
		}
		fmt.Fprintf(&b, "\n  adapter %s: %s", entry.Adapter, status)
	}
	if len(decision.Availability) > 0 {
		b.WriteString("\n")
	}
	for _, note := range decision.Notes {
		fmt.Fprintf(&b, "\nnote: %s", note)
	}
	if len(decision.Notes) > 0 {
		b.WriteString("\n")
	}
	return b.String()
}
