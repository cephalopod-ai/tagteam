package tagteam

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

func (a *App) runSolo(ctx context.Context, opts RunOptions) (final FinalRun, err error) {
	if opts.MaxWallTime > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.MaxWallTime)
		defer cancel()
	}
	editorLabel, _ := roleLabels(opts.Mode)
	runID, err := runIDForOptions(opts)
	if err != nil {
		return FinalRun{}, err
	}
	logProgress(opts, "run %s preflight started workdir=%s", runID, opts.Workdir)
	baseline, cleanup, err := preflight(opts, runID)
	if err != nil {
		return FinalRun{}, err
	}
	var runDir string
	runCompleted := false
	runActivated := false
	defer func() {
		a.finishPreflightCleanup(opts, runDir, cleanup, &final, &err)
		if runActivated {
			deactivateRun(opts.Workdir, runID, runCompleted && err == nil)
		}
	}()
	runDir, err = createRunDir(opts.Workdir, opts.StateRoot, runID)
	if err != nil {
		return FinalRun{}, &ExitError{Code: ExitAdapterFailure, Err: err}
	}
	if opts.AllowDirty || opts.GitSafety == "allow-dirty" {
		logProgress(opts, "warning: allow-dirty reviews the cumulative worktree diff against HEAD")
		if err := writePreexistingWorktree(ctx, opts.Workdir, runDir, baseline); err != nil {
			return FinalRun{}, &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("capture pre-existing worktree: %w", err)}
		}
	}
	lock, err := acquireRunLock(runDir, false)
	if err != nil {
		return FinalRun{}, &ExitError{Code: ExitAdapterFailure, Err: err}
	}
	defer lock.Release()
	activateRun(opts.Workdir, runID, runDir, opts.Mode)
	runActivated = true
	workerSchemaPath := filepath.Join(runDir, "worker-result-schema.json")
	if err := writeFileDurable(workerSchemaPath, []byte(WorkerResultSchema+"\n"), 0o644, false); err != nil {
		return FinalRun{}, err
	}
	if err := writeRedactedBytes(filepath.Join(runDir, "input.md"), []byte(opts.Prompt), opts.EnvOverlay); err != nil {
		return FinalRun{}, err
	}
	repoInstructions, err := loadAndPersistRepoInstructions(ctx, opts, runDir)
	if err != nil {
		return FinalRun{}, err
	}
	meta := Meta{
		SchemaVersion: ArtifactSchemaVersion,
		RunID:         runID,
		Workdir:       opts.Workdir,
		Baseline:      baseline,
		Command:       "run",
		Prompt:        redactSecretsWithOverlay(opts.Prompt, opts.EnvOverlay),
		StartedAt:     time.Now().UTC(),
		Adapters:      map[string]string{editorLabel: opts.Coder.Adapter},
		Models:        map[string]string{editorLabel: opts.Coder.Model},
		ConfigSources: opts.ConfigSources,
	}
	if err := writeJSON(filepath.Join(runDir, "meta.json"), meta); err != nil {
		return FinalRun{}, err
	}
	logProgress(opts, "run %s started mode=%s baseline=%s run-dir=%s", runID, opts.Mode, baseline, runDir)

	registry := Registry(a.Config, opts)
	selectionState := FinalRun{}
	initFinalState(&selectionState, opts)
	selectedCoder, editor, err := selectRunnableRoleAdapter(ctx, registry, editorLabel, opts.Coder, fallbackTargetsForRole(opts, editorLabel, opts.Coder), lossPolicyForRole(opts, editorLabel), &selectionState)
	if err != nil {
		return FinalRun{}, err
	}
	opts.Coder = selectedCoder
	meta.Adapters[editorLabel] = opts.Coder.Adapter
	meta.Models[editorLabel] = opts.Coder.Model
	if err := writeJSON(filepath.Join(runDir, "meta.json"), meta); err != nil {
		return FinalRun{}, err
	}

	final = FinalRun{
		SchemaVersion:   ArtifactSchemaVersion,
		RunID:           runID,
		ResumedFrom:     opts.ResumedFrom,
		RunDir:          runDir,
		Workdir:         opts.Workdir,
		Baseline:        baseline,
		Mode:            opts.Mode,
		Coder:           opts.Coder,
		Verdict:         "done",
		RoundsRequested: 1,
		Caps:            runCaps(opts),
		Costs:           map[string]float64{},
		Adapters:        meta.Adapters,
		Models:          meta.Models,
		StartedAt:       meta.StartedAt,
	}
	initFinalState(&final, opts)
	final.Phase = "solo"
	final.Budgets.MaxRounds = 1
	final.Degraded = selectionState.Degraded
	final.DegradedReason = selectionState.DegradedReason
	final.RoleStatuses = selectionState.RoleStatuses
	final.RoleLosses = selectionState.RoleLosses
	budget := &InvocationBudget{Max: opts.MaxRoleInvocations}
	opts.InvocationBudget = budget
	defer func() {
		if err == nil || final.RunID == "" || !final.FinishedAt.IsZero() {
			return
		}
		final.ExitCode = ExitCode(err)
		final.Verdict = "error"
		final.Summary = redactSecretsWithOverlay(err.Error(), opts.EnvOverlay)
		final.FinishedAt = time.Now().UTC()
		cancelled := errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled)
		if cancelled {
			final.Status = RunStatusCancelled
			final.BlockingReason = string(ReasonCancelled)
			final.Verdict = "cancelled"
		}
		if IsIntegrityViolation(err) {
			final.Status = RunStatusQuarantined
			final.BlockingReason = string(ReasonQuarantined)
		}
		roleStatus, roleReason := "failed", classifyRoleFailure(editorLabel, err)
		if cancelled {
			roleStatus, roleReason = string(RunStatusCancelled), ReasonCancelled
		}
		setRoleStatus(&final, editorLabel, opts.Coder, roleStatus, roleReason, err.Error())
		setFinalBlocking(&final, roleReason, err.Error())
		applyInvocationBudget(&final, budget)
		finalizeRunState(&final)
		state := runStateForFinal(final, opts.Mode, "solo", "")
		state.CurrentRound = max(1, final.RoundsCompleted)
		if persistErr := a.persistTerminalRun(opts.Workdir, &final, state); persistErr != nil {
			err = errors.Join(err, persistErr)
		}
	}()
	baselineTest, err := runBaselineTest(ctx, opts, runDir)
	if err != nil {
		return final, err
	}
	final.BaselineTest = baselineTest
	setRoleStatus(&final, editorLabel, opts.Coder, "running", "", "")
	if stateErr := writeRunState(runDir, RunState{
		RunID:        runID,
		Mode:         opts.Mode,
		Status:       "running",
		Phase:        "solo",
		RoleStatuses: final.RoleStatuses,
		CurrentRound: 1,
	}); stateErr != nil {
		return final, mandatoryPersistenceError("running solo state", stateErr)
	}

	logProgress(opts, "solo implementation started adapter=%s", editor.ID())
	outputPath := filepath.Join(runDir, "solo-round-1.md")
	beforeEditor, snapshotErr := captureWorktreeSnapshot(ctx, opts.Workdir)
	if snapshotErr != nil {
		return final, &ExitError{Code: ExitAdapterFailure, Err: snapshotErr}
	}
	editorRequest := Request{
		Context:               ctx,
		Prompt:                workerContractPrompt(withAdapterRepoInstructions(editor, appendHostBaselineEvidence(BuildSoloPrompt(opts.Workdir, opts.Prompt), final.BaselineTest), repoInstructions)),
		SystemPrompt:          "",
		EnvOverlay:            opts.EnvOverlay,
		Model:                 opts.Coder.Model,
		Workdir:               opts.Workdir,
		RunDir:                runDir,
		OutputPath:            outputPath,
		SchemaPath:            workerSchemaPath,
		Timeout:               opts.Timeout,
		WatchdogTimeout:       opts.WatchdogTimeout,
		Phase:                 fmt.Sprintf("solo %s", editor.ID()),
		ProgressRole:          Role(editorLabel),
		Quiet:                 opts.Quiet,
		Verbose:               opts.Verbose,
		Budget:                opts.InvocationBudget,
		RequireWorkerContract: true,
		AllowedScope:          allowedScopeForRound(opts, nil),
	}
	editorResult, err := a.runEditorWithContractRetry(ctx, opts, editor, editorRequest, beforeEditor)
	if err != nil {
		if cancelErr := ctx.Err(); cancelErr != nil {
			return final, cancelErr
		}
		reason := classifyRoleFailure(editorLabel, err)
		setRoleStatus(&final, editorLabel, opts.Coder, "failed", reason, err.Error())
		if IsIntegrityViolation(err) {
			final.Status = RunStatusQuarantined
			final.BlockingReason = string(ReasonQuarantined)
		} else {
			originalTarget := opts.Coder
			recovered, selectedTarget, selectedAdapter, recoveryErr := a.recoverEditorFailure(ctx, opts, 1, runDir, baseline, workerSchemaPath, "", editorRequest, opts.Coder, editor, nil, registry, err, beforeEditor, &final)
			if recoveryErr == nil {
				editorResult = recovered
				if roleTargetString(selectedTarget) != roleTargetString(originalTarget) {
					setFinalDegraded(&final, ReasonFallbackUsed, fmt.Sprintf("%s runtime fallback selected", editorLabel))
					appendRoleLoss(&final, editorLabel, opts.LossPolicy.Worker, "runtime_replace", "fallback_selected", ReasonFallbackUsed, fmt.Sprintf("%s -> %s", roleTargetString(originalTarget), roleTargetString(selectedTarget)))
				}
				opts.Coder = selectedTarget
				editor = selectedAdapter
				final.Coder = selectedTarget
				meta.Adapters[editorLabel] = selectedTarget.Adapter
				meta.Models[editorLabel] = selectedTarget.Model
				setFinalRoleTarget(&final, editorLabel, selectedTarget)
				if writeErr := writeJSON(filepath.Join(runDir, "meta.json"), meta); writeErr != nil {
					return final, writeErr
				}
				err = nil
			} else {
				err = recoveryErr
			}
		}
		if err != nil {
			status := "failed"
			if final.Status == RunStatusQuarantined {
				status = string(RunStatusQuarantined)
			}
			final.ExitCode = ExitCode(err)
			final.Verdict = status
			final.Summary = redactSecretsWithOverlay(err.Error(), opts.EnvOverlay)
			final.FinishedAt = time.Now().UTC()
			setFinalBlocking(&final, reason, err.Error())
			applyInvocationBudget(&final, budget)
			finalizeRunState(&final)
			state := RunState{
				RunID:          runID,
				Mode:           opts.Mode,
				Status:         status,
				Phase:          "solo",
				BlockingReason: final.BlockingReason,
				RoleStatuses:   final.RoleStatuses,
				CurrentRound:   1,
				ExitCode:       final.ExitCode,
			}
			if persistErr := a.persistTerminalRun(opts.Workdir, &final, state); persistErr != nil {
				err = errors.Join(err, persistErr)
			}
			return final, err
		}
	}
	setRoleStatus(&final, editorLabel, opts.Coder, "completed", "", "")
	final.Costs[editorLabel] += editorResult.CostUSD
	final.Summary = strings.TrimSpace(editorResult.Text)
	final.RoundsCompleted = 1
	if stateErr := writeRunState(runDir, RunState{
		RunID:        runID,
		Mode:         opts.Mode,
		Status:       "running",
		Phase:        "diff",
		RoleStatuses: final.RoleStatuses,
		CurrentRound: 1,
	}); stateErr != nil {
		return final, mandatoryPersistenceError("solo diff state", stateErr)
	}
	logProgress(opts, "solo implementation completed output=%s", outputPath)

	diffArtifact, err := captureDiffArtifact(ctx, opts.Workdir, baseline, runDir, 1)
	if err != nil {
		return final, err
	}
	final.LatestDiffPath = diffArtifact.PatchPath
	final.LatestNumstatPath = diffArtifact.NumstatPath
	final.LatestFilesPath = diffArtifact.FilesPath
	final.LatestSHA256Path = diffArtifact.SHA256Path
	final.LatestDiffSHA256 = diffArtifact.Metadata.DiffSHA256
	final.ChangedFiles = diffArtifact.ChangedFiles()
	logProgress(opts, "solo diff captured bytes=%d path=%s", len(diffArtifact.Patch), diffArtifact.PatchPath)
	gateResult := evaluateQualityGates(ctx, opts, baseline, 1, diffArtifact, allowedScopeForRound(opts, nil))
	final.QualityGates = append(final.QualityGates, gateResult)
	if err := writeJSONWithNewline(filepath.Join(runDir, "quality-gates-round-1.json"), gateResult); err != nil {
		return final, mandatoryPersistenceError("quality gate", err)
	}
	if summary, ledgerErr := updateFindingsLedger(runDir, 1, nil, &gateResult); ledgerErr != nil {
		return final, &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("update findings ledger: %w", ledgerErr)}
	} else {
		final.Findings = summary
	}

	if hasConfiguredTests(opts) && !opts.NoTest {
		testPath := filepath.Join(runDir, "test-round-1.txt")
		logProgress(opts, "solo tests started command=%q", testCommandDescription(opts))
		testRun, err := runConfiguredTestCommands(ctx, opts, testPath)
		if err != nil {
			return final, err
		}
		final.Tests = append(final.Tests, testRun)
		if final.BaselineTest != nil {
			comparison := compareRegression(*final.BaselineTest, testRun)
			final.Regression = &comparison
		}
		if testRun.Passed {
			logProgress(opts, "solo tests passed output=%s", testPath)
		} else {
			logProgress(opts, "solo tests failed output=%s", testPath)
		}
	}

	if final.Summary == "" {
		final.Summary = "Solo implementation completed; review was not run."
	} else {
		final.Summary = strings.TrimSpace(final.Summary + "\n\nReview was not run in solo mode.")
	}
	final.FinishedAt = time.Now().UTC()
	final.ExitCode = a.computeExitCode(final)
	applyInvocationBudget(&final, budget)
	finalizeRunState(&final)
	logProgress(opts, "run %s finished mode=solo exit=%d", runID, final.ExitCode)
	state := runStateForFinal(final, opts.Mode, "solo", "")
	state.CurrentRound = 1
	if err := a.persistTerminalRun(opts.Workdir, &final, state); err != nil {
		return final, err
	}
	// See the equivalent comment in Review: only mark the active-run pointer
	// completed once persistFinal has actually succeeded.
	runCompleted = true
	if final.ExitCode != ExitSuccess {
		return final, &ExitError{Code: final.ExitCode, Err: fmt.Errorf("run completed with exit code %d", final.ExitCode)}
	}
	return final, nil
}

func (a *App) resolveOrchestrationDecision(ctx context.Context, opts RunOptions, runDir string, editor, reviewer Adapter) (OrchestrationDecision, Mode) {
	decision := newOrchestrationDecision(filepath.Base(runDir), opts.Mode)
	if opts.DryRun {
		decision.HostReason = "dry-run keeps initial mode"
		return decision, opts.Mode
	}
	switch opts.Mode {
	case ModeRelay:
		if constraint := relaySimplificationConstraint(opts); constraint != "" {
			decision.HostReason = constraint
			return decision, ModeRelay
		}
		advisory, err := a.collectOrchestrationAdvisory(ctx, opts, reviewer, "supervisor", opts.Mode, "", runDir)
		if err != nil {
			markOrchestrationDecisionDegraded(&decision, err.Error())
			return decision, opts.Mode
		}
		return decision, applyRelaySimplificationPolicy(&decision, advisory)
	case ModeSupervisor:
		worker, err := a.collectOrchestrationAdvisory(ctx, opts, editor, "worker", opts.Mode, "", runDir)
		if err != nil {
			markOrchestrationDecisionDegraded(&decision, err.Error())
			return decision, opts.Mode
		}
		if worker.Recommendation != "escalate" || worker.TargetMode != ModeRelay {
			decision.Advisories = append(decision.Advisories, worker)
			decision.FinalMode = ModeSupervisor
			decision.Status = "kept"
			decision.HostReason = "worker did not request relay escalation"
			return decision, ModeSupervisor
		}
		supervisor, err := a.collectOrchestrationAdvisory(ctx, opts, reviewer, "supervisor", opts.Mode, worker.Reason, runDir)
		if err != nil {
			decision.Advisories = append(decision.Advisories, worker)
			markOrchestrationDecisionDegraded(&decision, err.Error())
			return decision, opts.Mode
		}
		return decision, applySupervisorEscalationPolicy(&decision, worker, supervisor)
	default:
		decision.HostReason = "mode does not support advisory transition"
		return decision, opts.Mode
	}
}

func (a *App) collectOrchestrationAdvisory(ctx context.Context, opts RunOptions, adapter Adapter, source string, current Mode, brief string, runDir string) (OrchestrationAdvisory, error) {
	schemaPath := filepath.Join(runDir, "orchestration-advisory-schema.json")
	if err := writeFileDurable(schemaPath, []byte(OrchestrationAdvisorySchema), 0o644, true); err != nil {
		return OrchestrationAdvisory{}, err
	}
	outputPath := filepath.Join(runDir, fmt.Sprintf("orchestration-%s-advisory.json", sanitizeArtifactName(source)))
	var prompt string
	if source == "worker" {
		prompt = BuildWorkerOrchestrationAdvisoryPrompt(opts.Workdir, opts.Prompt, current, brief)
	} else {
		prompt = BuildSupervisorOrchestrationAdvisoryPrompt(opts.Workdir, opts.Prompt, current)
	}
	if !adapter.Capabilities().SupportsSchema {
		prompt += "\n\nJSON schema:\n" + OrchestrationAdvisorySchema
	}
	result, err := a.runAdapter(ctx, adapter, RoleSupervisor, Request{
		Context:         ctx,
		Prompt:          prompt,
		EnvOverlay:      opts.EnvOverlay,
		Model:           advisoryModel(opts, source),
		Workdir:         opts.Workdir,
		RunDir:          runDir,
		OutputPath:      outputPath,
		SchemaPath:      schemaPath,
		Timeout:         opts.Timeout,
		WatchdogTimeout: opts.WatchdogTimeout,
		Phase:           fmt.Sprintf("orchestration advisory %s %s", source, adapter.ID()),
		Quiet:           opts.Quiet,
		Verbose:         opts.Verbose,
		Budget:          opts.InvocationBudget,
	}, false)
	if err != nil {
		return OrchestrationAdvisory{}, err
	}
	advisory, err := parseOrchestrationAdvisory([]byte(result.Text), source)
	if err != nil {
		if repaired, _, attempted, repairErr := a.repairJSONWithWorker(ctx, opts, Registry(a.Config, opts), runDir, outputPath, "orchestration advisory", OrchestrationAdvisorySchema, []byte(result.Text), err); repairErr != nil {
			if noteErr := noteJSONRepairFailure(ctx, runDir, outputPath, repairErr, opts.EnvOverlay); noteErr != nil {
				return OrchestrationAdvisory{}, noteErr
			}
			return OrchestrationAdvisory{}, err
		} else if attempted {
			repairedAdvisory, parseErr := parseOrchestrationAdvisory(repaired, source)
			if parseErr != nil {
				if werr := writeRepairSideBytes(ctx, runDir, outputPath, ".repair-validation-error.txt", []byte(parseErr.Error()+"\n"), opts.EnvOverlay); isControlResumePathGateError(werr) {
					return OrchestrationAdvisory{}, werr
				}
				return OrchestrationAdvisory{}, err
			}
			advisory = repairedAdvisory
		} else {
			return OrchestrationAdvisory{}, err
		}
	}
	if gate := controlResumeGateFrom(ctx); gate != nil {
		prevRunDir := runDir
		writeDir, rebindErr := rebindControlResumeFromContext(ctx, runDir, nil)
		if rebindErr != nil {
			return OrchestrationAdvisory{}, &ExitError{Code: ExitPreflightFailed, Err: rebindErr}
		}
		runDir = writeDir
		outputPath = rebuildControlResumeArtifactPath(prevRunDir, runDir, outputPath)
		if err := guardControlResumeWritePath(gate, outputPath); err != nil {
			return OrchestrationAdvisory{}, &ExitError{Code: ExitPreflightFailed, Err: err}
		}
	}
	if err := writeJSONWithNewline(outputPath, advisory); err != nil {
		return OrchestrationAdvisory{}, err
	}
	return advisory, nil
}

func advisoryModel(opts RunOptions, source string) string {
	if source == "worker" {
		return opts.Coder.Model
	}
	return opts.Adversary.Model
}
