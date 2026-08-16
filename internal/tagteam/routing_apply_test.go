package tagteam

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func resolveWithJob(t *testing.T, flags FlagInputs, changed map[string]bool) RunOptions {
	t.Helper()
	if flags.Timeout == 0 {
		flags.Timeout = 15 * time.Minute
	}
	opts, err := ResolveOptions(DefaultConfig(), nil, flags, changed, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	return opts
}

func TestResolveOptionsJobSelectsWorkflowAndTeam(t *testing.T) {
	opts := resolveWithJob(t, FlagInputs{Job: "audit"}, map[string]bool{"job": true})
	if opts.Mode != ModeAdversarial {
		t.Fatalf("mode = %q, want %q", opts.Mode, ModeAdversarial)
	}
	if opts.Rounds != 1 {
		t.Fatalf("rounds = %d, want the job default 1", opts.Rounds)
	}
	if opts.Job != "audit" {
		t.Fatalf("job = %q, want audit", opts.Job)
	}
	if opts.Routing == nil {
		t.Fatal("routing decision was not attached to the resolved run")
	}
	if opts.Coder.Adapter == "" || opts.Adversary.Adapter == "" {
		t.Fatalf("routing left a slot unstaffed: coder=%+v adversary=%+v", opts.Coder, opts.Adversary)
	}
	if opts.Coder.Adapter == opts.Adversary.Adapter {
		t.Fatalf("audit should route review to an independent adapter, got %q for both", opts.Coder.Adapter)
	}
	if err := validateRunRoles(opts); err != nil {
		t.Fatalf("routed team fails role validation: %v", err)
	}
	if err := validateClaudeRoleAssignments(opts); err != nil {
		t.Fatalf("routed team fails claude role validation: %v", err)
	}
}

func TestResolveOptionsJobStaffsRelayScout(t *testing.T) {
	opts := resolveWithJob(t, FlagInputs{Job: "deep_scan"}, map[string]bool{"job": true})
	if opts.Mode != ModeRelay {
		t.Fatalf("mode = %q, want %q", opts.Mode, ModeRelay)
	}
	if opts.Scout.Adapter != "agy" {
		t.Fatalf("scout = %+v, want the large-context agy scout", opts.Scout)
	}
	if err := validateRunRoles(opts); err != nil {
		t.Fatalf("routed relay team fails role validation: %v", err)
	}
}

func TestResolveOptionsExplicitFlagsWinOverJob(t *testing.T) {
	flags := FlagInputs{
		Job:        "audit",
		Mode:       string(ModeSupervisor),
		Worker:     "codex:gpt-5.6-sol",
		Supervisor: "claude:claude-sonnet-5",
	}
	changed := map[string]bool{"job": true, "mode": true, "worker": true, "supervisor": true}
	opts := resolveWithJob(t, flags, changed)
	if opts.Mode != ModeSupervisor {
		t.Fatalf("mode = %q, want the explicit %q", opts.Mode, ModeSupervisor)
	}
	if roleTargetString(opts.Coder) != "codex:gpt-5.6-sol" {
		t.Fatalf("worker = %q, want the explicit target", roleTargetString(opts.Coder))
	}
	if roleTargetString(opts.Adversary) != "claude:claude-sonnet-5" {
		t.Fatalf("supervisor = %q, want the explicit target", roleTargetString(opts.Adversary))
	}
	if opts.Routing == nil {
		t.Fatal("routing decision should still be recorded for pinned slots")
	}
	for _, role := range opts.Routing.Roles {
		if role.Source != RoutingSourceOperator {
			t.Fatalf("slot %s source = %q, want %q", role.Slot, role.Source, RoutingSourceOperator)
		}
	}
	if opts.Routing.WorkflowSource != RoutingSourceOperator {
		t.Fatalf("workflow source = %q, want %q", opts.Routing.WorkflowSource, RoutingSourceOperator)
	}
}

func TestResolveOptionsExplicitRoundsWinOverJob(t *testing.T) {
	opts := resolveWithJob(t, FlagInputs{Job: "problem_solving", Rounds: 5}, map[string]bool{"job": true, "rounds": true})
	if opts.Rounds != 5 {
		t.Fatalf("rounds = %d, want the explicit 5", opts.Rounds)
	}
	if opts.Routing.RoundsSource != RoutingSourceOperator {
		t.Fatalf("rounds source = %q, want %q", opts.Routing.RoundsSource, RoutingSourceOperator)
	}
}

func TestResolveOptionsRoutingAppendsFallbacksWithoutDroppingConfigured(t *testing.T) {
	opts := resolveWithJob(t, FlagInputs{Job: "scoped_patch"}, map[string]bool{"job": true})
	if len(opts.Fallbacks.Worker) == 0 {
		t.Fatal("expected worker fallbacks after routing")
	}
	configured := defaultWorkerFallback
	found := false
	for _, fallback := range opts.Fallbacks.Worker {
		if fallback == configured {
			found = true
		}
		if fallback == roleTargetString(opts.Coder) {
			t.Fatalf("fallback list repeats the primary worker target: %s", fallback)
		}
	}
	if !found {
		t.Fatalf("routing dropped the configured worker fallback %q: %v", configured, opts.Fallbacks.Worker)
	}
}

func TestResolveOptionsUnknownJobIsAnArgumentError(t *testing.T) {
	_, err := ResolveOptions(DefaultConfig(), nil, FlagInputs{Job: "nope", Timeout: time.Minute}, map[string]bool{"job": true}, "ship it")
	if err == nil {
		t.Fatal("expected an error for an unknown job")
	}
	if ExitCode(err) != ExitInvalidArguments {
		t.Fatalf("exit code = %d, want %d", ExitCode(err), ExitInvalidArguments)
	}
}

func TestResolveOptionsWithoutJobLeavesRoutingEmpty(t *testing.T) {
	opts := resolveWithJob(t, FlagInputs{}, map[string]bool{})
	if opts.Routing != nil || opts.Job != "" {
		t.Fatalf("routing should be empty without --job, got job=%q routing=%+v", opts.Job, opts.Routing)
	}
	if opts.Mode != ModeSupervisor {
		t.Fatalf("mode = %q, want the configured default", opts.Mode)
	}
}

func TestPersistRoutingDecisionWritesArtifact(t *testing.T) {
	runDir := t.TempDir()
	opts := resolveWithJob(t, FlagInputs{Job: "audit"}, map[string]bool{"job": true})
	if err := persistRoutingDecision(context.Background(), opts, runDir); err != nil {
		t.Fatalf("persistRoutingDecision() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(runDir, routingArtifactName))
	if err != nil {
		t.Fatalf("read routing artifact: %v", err)
	}
	var decision RoutingDecision
	if err := json.Unmarshal(data, &decision); err != nil {
		t.Fatalf("routing artifact is not valid JSON: %v", err)
	}
	if decision.SchemaVersion != RoutingSchemaVersion {
		t.Fatalf("schema version = %d, want %d", decision.SchemaVersion, RoutingSchemaVersion)
	}
	if decision.Job != "audit" || len(decision.Roles) != 2 {
		t.Fatalf("persisted decision is incomplete: %+v", decision)
	}
}

func TestPersistRoutingDecisionSkipsUnroutedRuns(t *testing.T) {
	runDir := t.TempDir()
	if err := persistRoutingDecision(context.Background(), RunOptions{}, runDir); err != nil {
		t.Fatalf("persistRoutingDecision() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(runDir, routingArtifactName)); !os.IsNotExist(err) {
		t.Fatalf("routing artifact should not exist for an unrouted run (err = %v)", err)
	}
}

func TestInitFinalStateCarriesRoutingIntoFinalRun(t *testing.T) {
	opts := resolveWithJob(t, FlagInputs{Job: "scoped_patch"}, map[string]bool{"job": true})
	final := FinalRun{}
	initFinalState(&final, opts)
	if final.Job != "scoped_patch" || final.Routing == nil {
		t.Fatalf("final run is missing routing provenance: job=%q routing=%+v", final.Job, final.Routing)
	}
	payload, err := json.Marshal(final)
	if err != nil {
		t.Fatalf("marshal final: %v", err)
	}
	if !strings.Contains(string(payload), `"routing"`) {
		t.Fatal("final.json should serialize the routing decision")
	}
}

func TestFormatRoutingDecisionMentionsEveryStaffedSlot(t *testing.T) {
	opts := resolveWithJob(t, FlagInputs{Job: "deep_scan"}, map[string]bool{"job": true})
	rendered := FormatRoutingDecision(*opts.Routing)
	for _, wanted := range []string{"deep_scan", "relay", "coder", "supervisor", "scout"} {
		if !strings.Contains(rendered, wanted) {
			t.Fatalf("rendered decision is missing %q:\n%s", wanted, rendered)
		}
	}
}
