package tagteam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type resumeRuntime struct {
	editorLabel      string
	reviewerLabel    string
	editor           Adapter
	reviewer         Adapter
	scout            Adapter
	registry         map[string]Adapter
	relay            RelayContext
	workPlan         *WorkPlan
	selectedPackage  *WorkPackage
	executionPlan    *ExecutionPlan
	repoInstructions string
	pathGate         *controlResumePathGate
}

func (a *App) resumeExistingRun(ctx context.Context, opts RunOptions, runDir string, meta Meta, state RunState, saved FinalRun, prior *Review, currentDiffHash string, gate *controlResumePathGate) (final FinalRun, err error) {
	if opts.Rounds <= 0 {
		opts.Rounds = saved.RoundsRequested
	}
	if opts.Rounds <= 0 {
		opts.Rounds = 1
	}
	if state.CurrentRound > opts.Rounds {
		opts.Rounds = state.CurrentRound
	}
	if opts.TestCmd != "" && !opts.NoTest {
		if err := validateTestCommand(opts.Workdir, opts.TestCmd); err != nil {
			return FinalRun{}, err
		}
	}

	if current, rebindErr := rebindControlResumeRunDir(gate, runDir, nil); rebindErr != nil {
		return FinalRun{}, &ExitError{Code: ExitPreflightFailed, Err: rebindErr}
	} else {
		runDir = current
	}
	final = prepareResumedFinal(saved, opts, meta, state, runDir)
	budget := &InvocationBudget{Max: opts.MaxRoleInvocations}
	opts.InvocationBudget = budget
	activateRun(opts.Workdir, state.RunID, runDir, opts.Mode)
	runCompleted := false
	defer func() { deactivateRun(opts.Workdir, state.RunID, runCompleted && err == nil) }()
	defer func() {
		if err == nil || !final.FinishedAt.IsZero() {
			return
		}
		final.ExitCode = ExitCode(err)
		final.Verdict = "error"
		final.Summary = redactSecretsWithOverlay(err.Error(), opts.EnvOverlay)
		final.FinishedAt = time.Now().UTC()
		if final.Status != RunStatusQuarantined && final.Status != RunStatusCancelled {
			final.Status = RunStatusFailed
		}
		applyInvocationBudget(&final, budget)
		// Deferred failure persistence must not write through a replaced run dir.
		if current, rebindErr := rebindControlResumeRunDir(gate, runDir, &final, "state.json", "final.json"); rebindErr != nil {
			err = &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("run directory path changed during failure persistence: %v (original: %v)", rebindErr, err)}
			return
		} else {
			runDir = current
		}
		terminalState := runStateForFinal(final, opts.Mode, final.Phase, "resume_failed")
		terminalState.CurrentRound = max(1, state.CurrentRound)
		if persistErr := a.persistTerminalRun(opts.Workdir, &final, terminalState); persistErr != nil {
			err = errors.Join(err, persistErr)
		}
	}()

	rt, err := a.prepareResumeRuntime(ctx, opts, runDir, &final, gate)
	if err != nil {
		return final, err
	}
	opts.Coder = final.Coder
	opts.Adversary = final.Adversary
	opts.Scout = final.Scout
	phase := normalizeRunPhase(state.Phase)
	if final.BaselineTest == nil {
		if phase != PhasePlanning {
			return quarantineResumedExecution(final, "baseline test evidence is missing after planning")
		}
		if runDir, err = rebindControlResumeRunDir(gate, runDir, &final); err != nil {
			return final, &ExitError{Code: ExitPreflightFailed, Err: err}
		}
		baselineTest, baselineErr := runBaselineTest(ctx, opts, runDir)
		if baselineErr != nil {
			return final, baselineErr
		}
		final.BaselineTest = baselineTest
	}

	round := max(1, state.CurrentRound)
	if runDir, err = rebindControlResumeRunDir(gate, runDir, &final, "state.json"); err != nil {
		return final, &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	if stateErr := writeRunState(runDir, RunState{RunID: state.RunID, Mode: opts.Mode, Status: "running", Phase: string(phase), CurrentRound: round, LatestDiffPath: state.LatestDiffPath, LatestReviewPath: state.LatestReviewPath, RecoveryStatus: "resuming"}); stateErr != nil {
		return final, mandatoryPersistenceError("resume state", stateErr)
	}
	if phase == PhasePlanning {
		if err := a.resumePlanning(ctx, opts, runDir, &rt, &final); err != nil {
			return final, err
		}
		phase = PhaseImplementing
	}

	if opts.Mode == ModeSolo {
		final, err = a.resumeSoloRun(ctx, opts, state, phase, round, currentDiffHash, rt, final, budget)
	} else {
		final, err = a.resumeReviewedRun(ctx, opts, state, phase, round, currentDiffHash, prior, rt, final, budget)
	}
	if final.FinishedAt.IsZero() {
		return final, err
	}
	runCompleted = true
	return final, err
}

func prepareResumedFinal(saved FinalRun, opts RunOptions, meta Meta, state RunState, runDir string) FinalRun {
	final := saved
	if final.SchemaVersion == 0 {
		final.SchemaVersion = ArtifactSchemaVersion
	}
	final.RunID = state.RunID
	final.ResumedFrom = ""
	final.RunDir = runDir
	final.Workdir = opts.Workdir
	final.Baseline = meta.Baseline
	final.Mode = opts.Mode
	final.Coder = opts.Coder
	final.Adversary = opts.Adversary
	final.Scout = opts.Scout
	final.Status = RunStatusRunning
	final.Phase = state.Phase
	final.Verdict = ""
	final.Summary = ""
	final.BlockingReason = ""
	final.ExitCode = ExitSuccess
	final.FinishedAt = time.Time{}
	final.RoundLimitReached = false
	final.envOverlay = opts.EnvOverlay
	if final.StartedAt.IsZero() {
		final.StartedAt = meta.StartedAt
	}
	if final.Costs == nil {
		final.Costs = map[string]float64{}
	}
	if final.Adapters == nil {
		final.Adapters = meta.Adapters
	}
	if final.Models == nil {
		final.Models = meta.Models
	}
	if final.RoleStatuses == nil {
		final.RoleStatuses = map[string]RoleStatus{}
	}
	final.RoundsRequested = max(final.RoundsRequested, opts.Rounds)
	return final
}

func (a *App) prepareResumeRuntime(ctx context.Context, opts RunOptions, runDir string, final *FinalRun, gate *controlResumePathGate) (resumeRuntime, error) {
	controlSafe := gate != nil
	editorLabel, reviewerLabel := roleLabels(opts.Mode)
	runtime := resumeRuntime{editorLabel: editorLabel, reviewerLabel: reviewerLabel, pathGate: gate}
	registry := Registry(a.Config, opts)
	runtime.registry = registry
	var err error
	opts.Coder, runtime.editor, err = selectRunnableRoleAdapter(ctx, registry, editorLabel, opts.Coder, fallbackTargetsForRole(opts, editorLabel, opts.Coder), lossPolicyForRole(opts, editorLabel), final)
	if err != nil {
		return runtime, err
	}
	final.Coder = opts.Coder
	if opts.Mode != ModeSolo {
		opts.Adversary, runtime.reviewer, err = selectRunnableRoleAdapter(ctx, registry, reviewerLabel, opts.Adversary, fallbackTargetsForRole(opts, reviewerLabel, opts.Adversary), lossPolicyForRole(opts, reviewerLabel), final)
		if err != nil {
			return runtime, err
		}
		final.Adversary = opts.Adversary
	}
	if opts.Mode == ModeRelay {
		opts.Scout, runtime.scout, err = selectRunnableRoleAdapter(ctx, registry, "scout", opts.Scout, fallbackTargetsForRole(opts, "scout", opts.Scout), opts.LossPolicy.Scout, final)
		if err != nil {
			return runtime, err
		}
		final.Scout = opts.Scout
	}
	if runDir, err = rebindControlResumeRunDir(gate, runDir, final, "repo-instructions.md", "repo-instructions.json"); err != nil {
		return runtime, &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	runtime.repoInstructions, err = loadAndPersistRepoInstructions(ctx, opts, runDir)
	if err != nil {
		return runtime, err
	}
	if controlSafe {
		if runDir, err = rebindControlResumeRunDir(gate, runDir, final); err != nil {
			return runtime, &ExitError{Code: ExitPreflightFailed, Err: err}
		}
		runtime.relay, err = loadResumeRelayContextControl(ctx, runDir)
		if err != nil {
			return runtime, err
		}
	} else {
		runtime.relay = loadResumeRelayContext(runDir)
	}
	if runtime.relay.WorkPlan != nil {
		runtime.workPlan = runtime.relay.WorkPlan
		if pkg, ok := runtime.workPlan.Selected(); ok {
			runtime.selectedPackage = &pkg
			runtime.relay.WorkPackage = &pkg
		}
	}
	if controlSafe {
		if runDir, err = rebindControlResumeRunDir(gate, runDir, final); err != nil {
			return runtime, &ExitError{Code: ExitPreflightFailed, Err: err}
		}
		if plan, planErr := readControlExecutionPlanOptional(ctx, runDir); planErr != nil {
			return runtime, planErr
		} else if plan != nil {
			runtime.executionPlan = plan
		}
	} else if plan, planErr := readExecutionPlan(runDir); planErr == nil {
		runtime.executionPlan = &plan
	}
	return runtime, nil
}

func loadResumeRelayContext(runDir string) RelayContext {
	relay := RelayContext{}
	relay.Brief = readOptionalText(filepath.Join(runDir, "supervisor-brief.md"))
	relay.Instructions = readOptionalText(filepath.Join(runDir, "supervisor-instructions.md"))
	_ = readOptionalJSON(filepath.Join(runDir, "scout-round-1.json"), &relay.Scout)
	_ = readOptionalJSON(filepath.Join(runDir, "post-scout-round-1.json"), &relay.PostScout)
	var plan WorkPlan
	if readOptionalJSON(filepath.Join(runDir, "supervisor-work-plan.json"), &plan) == nil && len(plan.Packages) > 0 {
		relay.WorkPlan = &plan
		if selected, ok := plan.Selected(); ok {
			relay.WorkPackage = &selected
		}
	}
	return relay
}

// loadResumeRelayContextControl loads relay resume artifacts through the
// control-safe readers. Escaping or broken symlinks fail closed without
// consuming external content; missing optional files are ignored.
// When a control-resume gate is present on ctx, re-resolve before each optional read.
func loadResumeRelayContextControl(ctx context.Context, runDir string) (RelayContext, error) {
	relay := RelayContext{}
	for _, step := range []struct {
		name  string
		apply func([]byte) error
	}{
		{"supervisor-brief.md", func(data []byte) error {
			relay.Brief = string(data)
			return nil
		}},
		{"supervisor-instructions.md", func(data []byte) error {
			relay.Instructions = string(data)
			return nil
		}},
		{"scout-round-1.json", func(data []byte) error {
			if err := json.Unmarshal(data, &relay.Scout); err != nil {
				return fmt.Errorf("control artifact scout-round-1.json: %w", err)
			}
			return nil
		}},
		{"post-scout-round-1.json", func(data []byte) error {
			if err := json.Unmarshal(data, &relay.PostScout); err != nil {
				return fmt.Errorf("control artifact post-scout-round-1.json: %w", err)
			}
			return nil
		}},
		{"supervisor-work-plan.json", func(data []byte) error {
			var plan WorkPlan
			if err := json.Unmarshal(data, &plan); err != nil {
				return fmt.Errorf("control artifact supervisor-work-plan.json: %w", err)
			}
			if len(plan.Packages) > 0 {
				relay.WorkPlan = &plan
				if selected, ok := plan.Selected(); ok {
					relay.WorkPackage = &selected
				}
			}
			return nil
		}},
	} {
		current, err := rebindControlResumeFromContext(ctx, runDir, nil)
		if err != nil {
			return RelayContext{}, &ExitError{Code: ExitPreflightFailed, Err: err}
		}
		runDir = current
		data, present, err := readControlOptionalArtifactBytes(runDir, step.name)
		if err != nil {
			// Under an MCP resume gate, artifact escape/read failures are
			// preflight path-boundary errors, not adapter failures.
			if controlResumeGateFrom(ctx) != nil {
				return RelayContext{}, &ExitError{Code: ExitPreflightFailed, Err: err}
			}
			return RelayContext{}, err
		}
		if present {
			if err := step.apply(data); err != nil {
				return RelayContext{}, err
			}
		}
	}
	return relay, nil
}

// readControlExecutionPlanOptional loads plan.json via the control-safe reader.
// Missing plan is (nil, nil); escaping symlinks are errors.
// When a control-resume gate is present on ctx, re-resolve immediately before the read.
func readControlExecutionPlanOptional(ctx context.Context, runDir string) (*ExecutionPlan, error) {
	current, err := rebindControlResumeFromContext(ctx, runDir, nil)
	if err != nil {
		return nil, &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	runDir = current
	data, present, err := readControlOptionalArtifactBytes(runDir, "plan.json")
	if err != nil {
		if controlResumeGateFrom(ctx) != nil {
			return nil, &ExitError{Code: ExitPreflightFailed, Err: err}
		}
		return nil, err
	}
	if !present {
		return nil, nil
	}
	var plan ExecutionPlan
	if err := json.Unmarshal(data, &plan); err != nil {
		return nil, fmt.Errorf("control artifact plan.json: %w", err)
	}
	return &plan, nil
}

func readOptionalText(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func readOptionalJSON(path string, out any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func (a *App) resumePlanning(ctx context.Context, opts RunOptions, runDir string, runtime *resumeRuntime, final *FinalRun) error {
	gate := runtime.pathGate
	if opts.Mode == ModeAdversarial || opts.Mode == ModeSolo {
		return nil
	}
	if opts.Mode == ModeRelay && runtime.relay.Scout.Summary == "" {
		var err error
		if runDir, err = rebindControlResumeRunDir(gate, runDir, final, "scout-round-1.json"); err != nil {
			return &ExitError{Code: ExitPreflightFailed, Err: err}
		}
		path := filepath.Join(runDir, "scout-round-1.json")
		result, err := a.runAdapter(ctx, runtime.scout, RoleScout, Request{Context: ctx, Prompt: withRepoInstructions(BuildScoutPrompt(opts.Workdir, opts.Prompt, "", opts.ScoutMode, "pre", "", "", ""), runtime.repoInstructions), EnvOverlay: opts.EnvOverlay, Model: opts.Scout.Model, Workdir: opts.Workdir, RunDir: runDir, OutputPath: path, Timeout: opts.Timeout, WatchdogTimeout: opts.WatchdogTimeout, Phase: "planning resumed scout", Budget: opts.InvocationBudget, MaxOutputBytes: opts.MaxOutputBytes, controlResumeGate: gate}, opts.DryRun)
		if err != nil {
			return err
		}
		if result.Scout != nil {
			runtime.relay.Scout = *result.Scout
		}
	}
	if opts.Mode == ModeSupervisor && opts.SupervisorSlicing && runtime.workPlan == nil {
		plan, err := a.resumeWorkPlan(ctx, opts, runDir, *runtime)
		if err != nil {
			return err
		}
		runtime.workPlan = &plan
		runtime.relay.WorkPlan = &plan
		selected, _ := plan.Selected()
		runtime.selectedPackage = &selected
		runtime.relay.WorkPackage = &selected
		final.WorkPlan = &plan
		final.SelectedPackage = &selected
		final.RemainingPackages = plan.RemainingPackageTitles()
		if runtime.executionPlan == nil {
			var planErr error
			if runDir, planErr = rebindControlResumeRunDir(gate, runDir, final, "plan.json"); planErr != nil {
				return &ExitError{Code: ExitPreflightFailed, Err: planErr}
			}
			runtime.executionPlan = newExecutionPlanFromWorkPlan(final.RunID, opts.Mode, plan, "supervisor-resume")
			if err := persistExecutionPlan(ctx, runDir, runtime.executionPlan); err != nil {
				return err
			}
		}
	}
	if strings.TrimSpace(runtime.relay.Brief) == "" {
		if runtime.workPlan != nil && runtime.selectedPackage != nil {
			var err error
			if runDir, err = rebindControlResumeRunDir(gate, runDir, final, "supervisor-brief.md"); err != nil {
				return &ExitError{Code: ExitPreflightFailed, Err: err}
			}
			runtime.relay.Brief = BuildWorkPackageBrief(*runtime.workPlan, *runtime.selectedPackage)
			if err := writeFileDurable(filepath.Join(runDir, "supervisor-brief.md"), []byte(runtime.relay.Brief), 0o644, true); err != nil {
				return err
			}
		} else {
			var err error
			if runDir, err = rebindControlResumeRunDir(gate, runDir, final, "supervisor-brief.md"); err != nil {
				return &ExitError{Code: ExitPreflightFailed, Err: err}
			}
			result, err := a.runAdapter(ctx, runtime.reviewer, supervisorBriefRole(opts.SupervisorCanEdit), Request{Context: ctx, Prompt: withRepoInstructions(BuildSupervisorBriefPrompt(opts.Workdir, opts.Prompt, opts.SupervisorCanEdit), runtime.repoInstructions), EnvOverlay: opts.EnvOverlay, Model: opts.Adversary.Model, Workdir: opts.Workdir, RunDir: runDir, OutputPath: filepath.Join(runDir, "supervisor-brief.md"), Timeout: opts.Timeout, WatchdogTimeout: opts.WatchdogTimeout, Phase: "planning resumed supervisor brief", Budget: opts.InvocationBudget, MaxOutputBytes: opts.MaxOutputBytes, controlResumeGate: gate}, opts.DryRun)
			if err != nil {
				return err
			}
			runtime.relay.Brief = result.Text
		}
	}
	if opts.Mode == ModeRelay && strings.TrimSpace(runtime.relay.Instructions) == "" {
		var err error
		if runDir, err = rebindControlResumeRunDir(gate, runDir, final, "supervisor-instructions.md"); err != nil {
			return &ExitError{Code: ExitPreflightFailed, Err: err}
		}
		result, err := a.runAdapter(ctx, runtime.reviewer, RoleSupervisor, Request{Context: ctx, Prompt: withRepoInstructions(BuildRelaySupervisorInstructionsPrompt(opts.Prompt, runtime.relay.Brief, runtime.relay.Scout), runtime.repoInstructions), EnvOverlay: opts.EnvOverlay, Model: opts.Adversary.Model, Workdir: opts.Workdir, RunDir: runDir, OutputPath: filepath.Join(runDir, "supervisor-instructions.md"), Timeout: opts.Timeout, WatchdogTimeout: opts.WatchdogTimeout, Phase: "planning resumed relay instructions", Budget: opts.InvocationBudget, MaxOutputBytes: opts.MaxOutputBytes, controlResumeGate: gate}, opts.DryRun)
		if err != nil {
			return err
		}
		runtime.relay.Instructions = result.Text
	}
	return nil
}

func (a *App) resumeWorkPlan(ctx context.Context, opts RunOptions, runDir string, runtime resumeRuntime) (WorkPlan, error) {
	gate := runtime.pathGate
	var err error
	if runDir, err = rebindControlResumeRunDir(gate, runDir, nil, "supervisor-work-plan.json", "work-plan-schema.json"); err != nil {
		return WorkPlan{}, &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	path := filepath.Join(runDir, "supervisor-work-plan.json")
	schemaPath := filepath.Join(runDir, "work-plan-schema.json")
	if err := writeFileDurable(schemaPath, []byte(WorkPlanSchema), 0o644, true); err != nil {
		return WorkPlan{}, err
	}
	prompt := withRepoInstructions(BuildSupervisorWorkPlanPrompt(opts.Workdir, opts.Prompt, opts.MaxPackages, opts.Package), runtime.repoInstructions)
	result, err := a.runAdapter(ctx, runtime.reviewer, RoleSupervisor, Request{Context: ctx, Prompt: prompt, EnvOverlay: opts.EnvOverlay, Model: opts.Adversary.Model, Workdir: opts.Workdir, RunDir: runDir, OutputPath: path, SchemaPath: schemaPath, Timeout: opts.Timeout, WatchdogTimeout: opts.WatchdogTimeout, Phase: "planning resumed work plan", Budget: opts.InvocationBudget, MaxOutputBytes: opts.MaxOutputBytes, controlResumeGate: gate}, opts.DryRun)
	if err != nil {
		return WorkPlan{}, err
	}
	plan, err := parseWorkPlan([]byte(result.Text), opts.Package, opts.MaxPackages)
	if err != nil {
		return WorkPlan{}, err
	}
	if err := validateWorkPlanBudget(plan, int64(opts.Timeout.Seconds()*0.8)); err != nil {
		return WorkPlan{}, err
	}
	if runDir, err = rebindControlResumeRunDir(gate, runDir, nil, "supervisor-work-plan.json"); err != nil {
		return WorkPlan{}, &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	path = filepath.Join(runDir, "supervisor-work-plan.json")
	if err := writeJSONWithNewline(path, plan); err != nil {
		return WorkPlan{}, err
	}
	return plan, nil
}
