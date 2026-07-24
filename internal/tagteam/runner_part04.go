package tagteam

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

func (a *App) runLoop(ctx context.Context, opts RunOptions, initialReview *Review) (final FinalRun, err error) {
	if opts.MaxWallTime > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.MaxWallTime)
		defer cancel()
	}
	editorLabel, reviewerLabel := roleLabels(opts.Mode)
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
	schemaPath := filepath.Join(runDir, "review-schema.json")
	if err := writeFileDurable(schemaPath, []byte(ReviewSchema), 0o644, true); err != nil {
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
		Adapters:      map[string]string{editorLabel: opts.Coder.Adapter, reviewerLabel: opts.Adversary.Adapter},
		Models:        map[string]string{editorLabel: opts.Coder.Model, reviewerLabel: opts.Adversary.Model},
		ConfigSources: opts.ConfigSources,
	}
	if opts.Mode == ModeRelay {
		meta.Adapters["scout"] = opts.Scout.Adapter
		meta.Models["scout"] = opts.Scout.Model
	}
	if err := writeJSON(filepath.Join(runDir, "meta.json"), meta); err != nil {
		return FinalRun{}, err
	}
	final = FinalRun{
		SchemaVersion:     ArtifactSchemaVersion,
		RunID:             runID,
		ResumedFrom:       opts.ResumedFrom,
		RunDir:            runDir,
		Workdir:           opts.Workdir,
		Baseline:          baseline,
		Mode:              opts.Mode,
		Coder:             opts.Coder,
		Adversary:         opts.Adversary,
		Scout:             opts.Scout,
		SupervisorCanEdit: opts.SupervisorCanEdit,
		RoundsRequested:   opts.Rounds,
		Caps:              runCaps(opts),
		Costs:             map[string]float64{},
		Adapters:          meta.Adapters,
		Models:            meta.Models,
		StartedAt:         meta.StartedAt,
	}
	initFinalState(&final, opts)
	budget := &InvocationBudget{Max: opts.MaxRoleInvocations}
	opts.InvocationBudget = budget
	// currentRole tracks the role whose work is in flight so the deferred
	// failure handler can classify the blocking reason correctly. final.Phase is
	// a human-facing serialized field and stays "preflight" until the round loop
	// sets it, so it is not a reliable classifier key for early failures.
	currentRole := editorLabel
	defer func() {
		if err == nil || final.RunID == "" {
			return
		}
		if !final.FinishedAt.IsZero() {
			return
		}
		if final.ExitCode == ExitSuccess {
			final.ExitCode = ExitCode(err)
		}
		if final.Verdict == "" {
			final.Verdict = "error"
		}
		if final.Summary == "" {
			final.Summary = redactSecretsWithOverlay(err.Error(), opts.EnvOverlay)
		}
		if final.FinishedAt.IsZero() {
			final.FinishedAt = time.Now().UTC()
		}
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			final.Status = RunStatusCancelled
			final.BlockingReason = string(ReasonCancelled)
			if artifact, captureErr := captureDiffArtifact(context.Background(), opts.Workdir, baseline, runDir, max(1, final.RoundsCompleted+1)); captureErr == nil {
				final.LatestDiffPath = artifact.PatchPath
				final.LatestDiffSHA256 = artifact.Metadata.DiffSHA256
				final.ChangedFiles = artifact.ChangedFiles()
			}
		}
		if IsIntegrityViolation(err) {
			final.Status = RunStatusQuarantined
			final.BlockingReason = string(ReasonQuarantined)
		}
		setFinalBlocking(&final, classifyRoleFailure(currentRole, err), err.Error())
		applyInvocationBudget(&final, budget)
		finalizeRunState(&final)
		state := runStateForFinal(final, opts.Mode, terminalPhaseForFailure(runDir, final.Phase), "")
		if persistErr := a.persistTerminalRun(opts.Workdir, &final, state); persistErr != nil {
			err = errors.Join(err, persistErr)
		}
	}()
	if stateErr := writeRunState(runDir, RunState{RunID: runID, Mode: opts.Mode, Status: "running", Phase: "preflight"}); stateErr != nil {
		return final, mandatoryPersistenceError("preflight run state", stateErr)
	}
	logProgress(opts, "run %s started mode=%s baseline=%s run-dir=%s", runID, opts.Mode, baseline, runDir)
	logJSONRepairPolicy(opts)

	registry := Registry(a.Config, opts)
	editor, ok := registry[opts.Coder.Adapter]
	if !ok {
		return final, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("unknown %s adapter %q", editorLabel, opts.Coder.Adapter)}
	}
	reviewer, ok := registry[opts.Adversary.Adapter]
	if !ok {
		return final, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("unknown %s adapter %q", reviewerLabel, opts.Adversary.Adapter)}
	}
	var scout Adapter
	scoutAvailable := true
	if opts.Mode == ModeRelay {
		var scoutOK bool
		scout, scoutOK = registry[opts.Scout.Adapter]
		if !scoutOK {
			return final, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("unknown scout adapter %q", opts.Scout.Adapter)}
		}
	}
	opts.Coder, editor, err = selectRunnableRoleAdapter(ctx, registry, editorLabel, opts.Coder, fallbackTargetsForRole(opts, editorLabel, opts.Coder), lossPolicyForRole(opts, editorLabel), &final)
	if err != nil {
		return final, err
	}
	meta.Adapters[editorLabel] = opts.Coder.Adapter
	meta.Models[editorLabel] = opts.Coder.Model
	final.Coder = opts.Coder
	reviewerRoleForPolicy := reviewerLabel
	currentRole = reviewerRoleForPolicy
	opts.Adversary, reviewer, err = selectRunnableRoleAdapter(ctx, registry, reviewerRoleForPolicy, opts.Adversary, fallbackTargetsForRole(opts, reviewerRoleForPolicy, opts.Adversary), lossPolicyForRole(opts, reviewerRoleForPolicy), &final)
	if err != nil {
		setFinalBlocking(&final, classifyRoleFailure(reviewerRoleForPolicy, err), err.Error())
		return final, err
	}
	meta.Adapters[reviewerLabel] = opts.Adversary.Adapter
	meta.Models[reviewerLabel] = opts.Adversary.Model
	final.Adversary = opts.Adversary
	final.Adapters = meta.Adapters
	final.Models = meta.Models
	if err := writeJSON(filepath.Join(runDir, "meta.json"), meta); err != nil {
		return final, err
	}
	baselineTest, err := runBaselineTest(ctx, opts, runDir)
	if err != nil {
		return final, err
	}
	final.BaselineTest = baselineTest

	var brief string
	var relay RelayContext
	var workPlan *WorkPlan
	var selectedPackage *WorkPackage
	var executionPlan *ExecutionPlan
	if initialReview == nil && (opts.Mode == ModeSupervisor || opts.Mode == ModeRelay) {
		decision, effectiveMode := a.resolveOrchestrationDecision(ctx, opts, runDir, editor, reviewer)
		normalizeOrchestrationDecision(&decision)
		if err := writeJSONWithNewline(filepath.Join(runDir, orchestrationDecisionArtifact), decision); err != nil {
			return final, &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("write orchestration decision artifact: %w", err)}
		}
		if effectiveMode != opts.Mode {
			logProgress(opts, "orchestration transition applied %s -> %s reason=%q", opts.Mode, effectiveMode, decision.HostReason)
			previousEditorLabel := editorLabel
			opts.Mode = effectiveMode
			editorLabel, reviewerLabel = roleLabels(opts.Mode)
			if opts.Mode == ModeRelay && opts.Scout.Adapter == "" {
				relayTargets := configuredTargetsForMode(a.Config.Defaults, ModeRelay)
				opts.Scout, err = ParseRoleTarget(relayTargets.Scout)
				if err != nil {
					return final, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("resolve escalated relay scout target: %w", err)}
				}
				opts.Fallbacks.Scout = normalizeFallbackTargets(opts.Fallbacks.Scout, opts.Scout)
			}
			renameRoleStatus(&final, previousEditorLabel, editorLabel)
			meta.Adapters = map[string]string{editorLabel: opts.Coder.Adapter, reviewerLabel: opts.Adversary.Adapter}
			meta.Models = map[string]string{editorLabel: opts.Coder.Model, reviewerLabel: opts.Adversary.Model}
			if opts.Mode == ModeRelay {
				meta.Adapters["scout"] = opts.Scout.Adapter
				meta.Models["scout"] = opts.Scout.Model
			}
			if err := writeJSON(filepath.Join(runDir, "meta.json"), meta); err != nil {
				return final, err
			}
			final.Mode = opts.Mode
			final.Adapters = meta.Adapters
			final.Models = meta.Models
		}
		if opts.Mode == ModeRelay {
			var scoutOK bool
			scout, scoutOK = registry[opts.Scout.Adapter]
			if !scoutOK {
				return final, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("unknown scout adapter %q", opts.Scout.Adapter)}
			}
			var scoutErr error
			opts.Scout, scout, scoutErr = selectRunnableRoleAdapter(ctx, registry, "scout", opts.Scout, fallbackTargetsForRole(opts, "scout", opts.Scout), opts.LossPolicy.Scout, &final)
			if scoutErr != nil {
				setRoleStatus(&final, "scout", opts.Scout, "failed", ReasonScoutUnavailable, scoutErr.Error())
				appendRoleLoss(&final, "scout", opts.LossPolicy.Scout, "preflight", "degraded", ReasonScoutUnavailable, scoutErr.Error())
				if policyBlocks(opts.LossPolicy.Scout) {
					setFinalBlocking(&final, ReasonScoutUnavailable, scoutErr.Error())
					return final, scoutErr
				}
				setFinalDegraded(&final, ReasonScoutUnavailable, "scout unavailable; continuing without scout context")
				scoutAvailable = false
			} else {
				meta.Adapters["scout"] = opts.Scout.Adapter
				meta.Models["scout"] = opts.Scout.Model
				final.Scout = opts.Scout
				final.Adapters = meta.Adapters
				final.Models = meta.Models
			}
		}
	} else if opts.Mode == ModeRelay {
		var scoutErr error
		opts.Scout, scout, scoutErr = selectRunnableRoleAdapter(ctx, registry, "scout", opts.Scout, fallbackTargetsForRole(opts, "scout", opts.Scout), opts.LossPolicy.Scout, &final)
		if scoutErr != nil {
			setRoleStatus(&final, "scout", opts.Scout, "failed", ReasonScoutUnavailable, scoutErr.Error())
			appendRoleLoss(&final, "scout", opts.LossPolicy.Scout, "preflight", "degraded", ReasonScoutUnavailable, scoutErr.Error())
			if policyBlocks(opts.LossPolicy.Scout) {
				setFinalBlocking(&final, ReasonScoutUnavailable, scoutErr.Error())
				return final, scoutErr
			}
			setFinalDegraded(&final, ReasonScoutUnavailable, "scout unavailable; continuing without scout context")
			scoutAvailable = false
		} else {
			meta.Adapters["scout"] = opts.Scout.Adapter
			meta.Models["scout"] = opts.Scout.Model
			final.Scout = opts.Scout
			final.Adapters = meta.Adapters
			final.Models = meta.Models
		}
	}
	if opts.Mode == ModeSupervisor && initialReview == nil {
		if opts.SupervisorSlicing {
			logProgress(opts, "supervisor slicing started adapter=%s max-packages=%d", reviewer.ID(), opts.MaxPackages)
			planOutputPath := filepath.Join(runDir, "supervisor-work-plan.json")
			planSchemaPath := filepath.Join(runDir, "work-plan-schema.json")
			var plan WorkPlan
			var planCost float64
			planParsed := false
			if opts.DryRun {
				plan = syntheticWorkPlan(opts.Prompt, opts.Package)
				planParsed = true
			} else {
				if err := writeFileDurable(planSchemaPath, []byte(WorkPlanSchema), 0o644, true); err != nil {
					return final, err
				}
				planPrompt := withRepoInstructions(BuildSupervisorWorkPlanPrompt(opts.Workdir, opts.Prompt, opts.MaxPackages, opts.Package, workPlanBudgetSeconds(opts.Timeout)), repoInstructions)
				if !reviewer.Capabilities().SupportsSchema {
					planPrompt += "\n\nJSON schema:\n" + WorkPlanSchema
				}
				planResult, err := a.runAdapter(ctx, reviewer, RoleSupervisor, Request{
					Context:         ctx,
					Prompt:          planPrompt,
					EnvOverlay:      opts.EnvOverlay,
					Model:           opts.Adversary.Model,
					Workdir:         opts.Workdir,
					RunDir:          runDir,
					OutputPath:      planOutputPath,
					SchemaPath:      planSchemaPath,
					Timeout:         opts.Timeout,
					WatchdogTimeout: opts.WatchdogTimeout,
					Phase:           fmt.Sprintf("supervisor slicing %s", reviewer.ID()),
					Quiet:           opts.Quiet,
					Verbose:         opts.Verbose,
					Budget:          opts.InvocationBudget,
				}, false)
				if err != nil {
					if repairedPlan, repairCost, ok, rerr := a.tryWorkPlanRepair(ctx, opts, registry, runDir, planOutputPath, nil, true, err, &final, reviewerLabel); rerr != nil {
						return final, rerr
					} else if ok {
						plan = repairedPlan
						planCost += repairCost
						planParsed = true
					}
					return final, err
				}
				if !planParsed {
					parsed, err := parseWorkPlan([]byte(planResult.Text), opts.Package, opts.MaxPackages)
					if err != nil {
						if repairedPlan, repairCost, ok, rerr := a.tryWorkPlanRepair(ctx, opts, registry, runDir, planOutputPath, []byte(planResult.Text), false, err, &final, reviewerLabel); rerr != nil {
							return final, rerr
						} else if ok {
							parsed = repairedPlan
							planCost += repairCost
						} else {
							return final, err
						}
					}
					plan = parsed
					planCost += planResult.CostUSD
				}
			}
			if err := validateWorkPlanBudget(plan, workPlanBudgetSeconds(opts.Timeout), opts.AutoNextPackage); err != nil {
				return final, &ExitError{Code: ExitAdapterFailure, Err: err}
			}
			pkg, ok := plan.Selected()
			if !ok {
				return final, &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("supervisor work plan has no selected package")}
			}
			if err := writeJSONWithNewline(planOutputPath, plan); err != nil {
				return final, err
			}
			final.Costs[reviewerLabel] += planCost
			workPlan = &plan
			selectedPackage = &pkg
			relay.WorkPlan = workPlan
			relay.WorkPackage = selectedPackage
			final.WorkPlan = workPlan
			final.SelectedPackage = selectedPackage
			final.RemainingPackages = plan.RemainingPackageTitles()
			executionPlan = newExecutionPlanFromWorkPlan(runID, opts.Mode, plan, "supervisor-initial")
			if err := persistExecutionPlan(ctx, runDir, executionPlan); err != nil {
				return final, err
			}
			final.Plan = summarizeExecutionPlan(runDir, executionPlan)
			brief = BuildWorkPackageBrief(plan, pkg)
			briefOutputPath := filepath.Join(runDir, "supervisor-brief.md")
			if err := writeFileDurable(briefOutputPath, []byte(brief), 0o644, true); err != nil {
				return final, err
			}
			logProgress(opts, "supervisor sliced task into %d packages; executing %s: %s", len(plan.Packages), pkg.ID, pkg.Title)
		} else {
			logProgress(opts, "supervisor brief started adapter=%s", reviewer.ID())
			briefOutputPath := filepath.Join(runDir, "supervisor-brief.md")
			briefResult, err := a.runAdapter(ctx, reviewer, supervisorBriefRole(opts.SupervisorCanEdit), Request{
				Context:         ctx,
				Prompt:          withRepoInstructions(BuildSupervisorBriefPrompt(opts.Workdir, opts.Prompt, opts.SupervisorCanEdit), repoInstructions),
				EnvOverlay:      opts.EnvOverlay,
				Model:           opts.Adversary.Model,
				Workdir:         opts.Workdir,
				RunDir:          runDir,
				OutputPath:      briefOutputPath,
				Timeout:         opts.Timeout,
				WatchdogTimeout: opts.WatchdogTimeout,
				Phase:           fmt.Sprintf("supervisor brief %s", reviewer.ID()),
				Quiet:           opts.Quiet,
				Verbose:         opts.Verbose,
				Budget:          opts.InvocationBudget,
			}, opts.DryRun)
			if err != nil {
				return final, err
			}
			final.Costs[reviewerLabel] += briefResult.CostUSD
			brief = briefResult.Text
			logProgress(opts, "supervisor brief completed output=%s", briefOutputPath)
		}
	}
	if opts.Mode == ModeRelay && initialReview == nil {
		scoutOutputPath := filepath.Join(runDir, "scout-round-1.json")
		scoutStatusPath := filepath.Join(runDir, "scout-execution-round-1.json")
		scoutStatus := newScoutExecutionArtifact(opts.ScoutMode, opts.ScoutFailurePolicy, opts.ScoutRetrieval && opts.ScoutMode == "recon")
		skipScout := !scoutAvailable
		retrievalContext := ""
		codeIntelContext := ""
		var retrieval RetrievalArtifact
		var codeIntel CodeIntelArtifact
		if (opts.CodeIntelCommand != "" || len(opts.CodeIntel.Providers) > 0) && !opts.DryRun {
			codeIntel, _ = runConfiguredCodeIntel(ctx, opts, runDir)
			codeIntelContext = CompactCodeIntelForPrompt(codeIntel)
		}
		symlinkTopology := collectScopeSymlinkTopology(opts.Workdir, allowedScopeForRound(opts, nil))
		if opts.ScoutRetrieval && opts.ScoutMode == "recon" && scoutAvailable {
			logProgress(opts, "scout retrieval started")
			var err error
			if retrieval, err = runScoutRetrieval(ctx, opts.Workdir, opts.Prompt, runDir, true); err != nil {
				return final, &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("write retrieval artifact: %w", err)}
			}
			retrievalContext = CompactRetrievalForPrompt(retrieval)
			scoutStatus.RetrievalRan = true
			scoutStatus.RetrievalStatus = retrieval.Status
			scoutStatus.RetrievalDegraded = retrievalStatusIsDegraded(retrieval.Status)
			logProgress(opts, "scout retrieval completed status=%s evidence=%d", retrieval.Status, len(retrieval.Evidence))
		}
		if sc := CompactSymlinkTopologyForPrompt(symlinkTopology); sc != "" {
			retrievalContext = strings.TrimSpace(sc + "\n" + retrievalContext)
		}
		scoutPrompt := withRepoInstructions(BuildScoutPromptWithCodeIntel(opts.Workdir, opts.Prompt, "", opts.ScoutMode, "pre", "", "", retrievalContext, codeIntelContext), repoInstructions)
		if opts.ScoutMode == "recon" {
			contextBudgetPath := filepath.Join(runDir, "scout-context-round-1.json")
			limit := scoutContextLimitForAdapter(a.Config, opts.Scout.Adapter)
			contextBudget := estimateScoutPromptBudget(scoutPrompt, limit)
			contextBudget.Adapter = opts.Scout.Adapter
			contextBudget.Model = opts.Scout.Model
			if contextBudget.Status == scoutContextStatusNearLimit && (retrievalContext != "" || codeIntelContext != "") {
				logProgress(opts, "scout context near configured limit; compacting derived context estimated=%d usable=%d", contextBudget.EstimatedInputTokens, contextBudget.UsableContextTokens)
				compactedRetrieval := CompactRetrievalForPromptAggressive(retrieval)
				compactedCodeIntel := CompactCodeIntelForPromptAggressive(codeIntel)
				if (compactedRetrieval != "" && len(compactedRetrieval) < len(retrievalContext)) || (compactedCodeIntel != "" && len(compactedCodeIntel) < len(codeIntelContext)) {
					retrievalContext = compactedRetrieval
					codeIntelContext = compactedCodeIntel
					scoutPrompt = withRepoInstructions(BuildScoutPromptWithCodeIntel(opts.Workdir, opts.Prompt, "", opts.ScoutMode, "pre", "", "", retrievalContext, codeIntelContext), repoInstructions)
					contextBudget = estimateScoutPromptBudget(scoutPrompt, limit)
					contextBudget.Adapter = opts.Scout.Adapter
					contextBudget.Model = opts.Scout.Model
					contextBudget.RetrievalCompacted = true
				}
			}
			if contextBudget.Status == scoutContextStatusExceeds && (retrievalContext != "" || codeIntelContext != "") {
				logProgress(opts, "scout context exceeds configured limit; disabling derived context estimated=%d usable=%d", contextBudget.EstimatedInputTokens, contextBudget.UsableContextTokens)
				retrievalContext = ""
				codeIntelContext = ""
				scoutPrompt = withRepoInstructions(BuildScoutPromptWithCodeIntel(opts.Workdir, opts.Prompt, "", opts.ScoutMode, "pre", "", "", "", ""), repoInstructions)
				contextBudget = estimateScoutPromptBudget(scoutPrompt, limit)
				contextBudget.Adapter = opts.Scout.Adapter
				contextBudget.Model = opts.Scout.Model
				contextBudget.RetrievalDisabledDueBudget = true
				scoutStatus.RetrievalDisabledByBudget = true
			}
			if contextBudget.Status == scoutContextStatusNearLimit {
				logProgress(opts, "scout context near configured limit estimated=%d usable=%d", contextBudget.EstimatedInputTokens, contextBudget.UsableContextTokens)
				if opts.ScoutContextPolicy == "skip" {
					setFinalDegraded(&final, ReasonScoutContextTooSmall, "scout context near configured limit; skipping scout")
					appendRoleLoss(&final, "scout", opts.LossPolicy.Scout, "context-budget", "degraded", ReasonScoutContextTooSmall, "near configured scout context limit")
					scoutStatus.ContinuedWithoutScoutContext = true
					skipScout = true
				}
				if opts.ScoutContextPolicy == "block" {
					err := &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("scout context near configured limit and scout_context_policy=block")}
					setFinalBlocking(&final, ReasonScoutContextTooSmall, err.Error())
					_ = writeJSONWithNewline(contextBudgetPath, contextBudget)
					_ = writeJSONWithNewline(scoutStatusPath, scoutStatus)
					return final, err
				}
			}
			if err := writeJSONWithNewline(contextBudgetPath, contextBudget); err != nil {
				return final, &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("write scout context artifact: %w", err)}
			}
			if contextBudget.Status == scoutContextStatusExceeds {
				budgetErr := invalidScoutContextBudgetError(contextBudget)
				scoutStatus.FailureClass = scoutFailureClassContextBudget
				scoutStatus.Failure = budgetErr.Error()
				if opts.ScoutContextPolicy == "block" || policyBlocks(opts.LossPolicy.Scout) {
					setFinalBlocking(&final, ReasonScoutContextTooSmall, budgetErr.Error())
					_ = writeJSONWithNewline(scoutStatusPath, scoutStatus)
					return final, &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("scout failed and scout_failure_policy=fail; aborting relay run: %w", budgetErr)}
				}
				setFinalDegraded(&final, ReasonScoutContextTooSmall, "scout context too small; continuing without scout context")
				appendRoleLoss(&final, "scout", opts.LossPolicy.Scout, "context-budget", "degraded", ReasonScoutContextTooSmall, budgetErr.Error())
				scoutStatus.ContinuedWithoutScoutContext = true
				skipScout = true
				logProgress(opts, "scout prompt exceeds configured budget; continuing without scout context")
			}
		}
		var scoutResult Result
		if !skipScout {
			logProgress(opts, "scout %s started adapter=%s", opts.ScoutMode, scout.ID())
			scoutStatus.ScoutRan = true
			var err error
			scoutResult, err = a.runAdapter(ctx, scout, RoleScout, Request{
				Context:         ctx,
				Prompt:          scoutPrompt,
				EnvOverlay:      opts.EnvOverlay,
				Model:           opts.Scout.Model,
				Workdir:         opts.Workdir,
				RunDir:          runDir,
				OutputPath:      scoutOutputPath,
				Timeout:         opts.Timeout,
				WatchdogTimeout: opts.WatchdogTimeout,
				Phase:           fmt.Sprintf("scout %s %s", opts.ScoutMode, scout.ID()),
				Quiet:           opts.Quiet,
				Verbose:         opts.Verbose,
				Budget:          opts.InvocationBudget,
			}, opts.DryRun)
			if err != nil && IsOutputContractError(err) {
				if repaired, ok, rerr := a.tryScoutContractRepair(ctx, opts, registry, runDir, scoutOutputPath, "scout", "scout JSON repaired by worker", err, &final); rerr != nil {
					return final, rerr
				} else if ok {
					scoutResult = repaired
					err = nil
				}
			}
			if err != nil {
				scoutStatus.FailureClass = classifyScoutFailure(err)
				scoutStatus.Failure = err.Error()
				if shouldBlockScoutFailure(opts.LossPolicy.Scout, err) {
					_ = writeJSONWithNewline(scoutStatusPath, scoutStatus)
					return final, &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("scout failed and scout_failure_policy=fail; aborting relay run: %w", err)}
				}
				setFinalDegraded(&final, ReasonScoutUnavailable, "scout failed; continuing without scout context")
				appendRoleLoss(&final, "scout", opts.LossPolicy.Scout, "invoke", "degraded", classifyRoleFailure("scout", err), err.Error())
				scoutStatus.ContinuedWithoutScoutContext = true
				logProgress(opts, "scout failed; continuing without scout context error=%q", err.Error())
			} else {
				scoutStatus.ScoutSucceeded = true
				setRoleStatus(&final, "scout", opts.Scout, "completed", "", "")
				if scoutResult.Scout != nil {
					if retrieval.Status != "" && scoutResult.Scout.RetrievalStatus == "" {
						scoutResult.Scout.RetrievalQueries = append([]string{}, retrieval.Queries...)
						scoutResult.Scout.Evidence = retrievalScoutEvidence(retrieval.Evidence)
						scoutResult.Scout.RetrievalStatus = retrieval.Status
						scoutResult.Scout.RetrievalTruncated = retrieval.Truncated
					}
					scoutResult.Scout.Evidence = mergeSymlinkTopologyEvidence(scoutResult.Scout.Evidence, symlinkTopology)
					relay.Scout = *scoutResult.Scout
				}
				final.Costs["scout"] += scoutResult.CostUSD
				logProgress(opts, "scout %s completed output=%s", opts.ScoutMode, scoutOutputPath)
			}
		}
		if err := writeJSONWithNewline(scoutStatusPath, scoutStatus); err != nil {
			return final, &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("write scout execution artifact: %w", err)}
		}

		logProgress(opts, "supervisor brief started adapter=%s", reviewer.ID())
		briefOutputPath := filepath.Join(runDir, "supervisor-brief.md")
		briefResult, err := a.runAdapter(ctx, reviewer, supervisorBriefRole(opts.SupervisorCanEdit), Request{
			Context:         ctx,
			Prompt:          withRepoInstructions(BuildSupervisorBriefPrompt(opts.Workdir, opts.Prompt, opts.SupervisorCanEdit), repoInstructions),
			EnvOverlay:      opts.EnvOverlay,
			Model:           opts.Adversary.Model,
			Workdir:         opts.Workdir,
			RunDir:          runDir,
			OutputPath:      briefOutputPath,
			Timeout:         opts.Timeout,
			WatchdogTimeout: opts.WatchdogTimeout,
			Phase:           fmt.Sprintf("supervisor brief %s", reviewer.ID()),
			Quiet:           opts.Quiet,
			Verbose:         opts.Verbose,
			Budget:          opts.InvocationBudget,
		}, opts.DryRun)
		if err != nil {
			return final, err
		}
		final.Costs[reviewerLabel] += briefResult.CostUSD
		brief = briefResult.Text
		relay.Brief = brief
		logProgress(opts, "supervisor brief completed output=%s", briefOutputPath)

		instructionsPath := filepath.Join(runDir, "supervisor-instructions.md")
		logProgress(opts, "supervisor relay instructions started adapter=%s", reviewer.ID())
		instructionsResult, err := a.runAdapter(ctx, reviewer, RoleSupervisor, Request{
			Context:         ctx,
			Prompt:          withRepoInstructions(BuildRelaySupervisorInstructionsPrompt(opts.Prompt, brief, relay.Scout), repoInstructions),
			EnvOverlay:      opts.EnvOverlay,
			Model:           opts.Adversary.Model,
			Workdir:         opts.Workdir,
			RunDir:          runDir,
			OutputPath:      instructionsPath,
			Timeout:         opts.Timeout,
			WatchdogTimeout: opts.WatchdogTimeout,
			Phase:           fmt.Sprintf("relay supervisor instructions %s", reviewer.ID()),
			Quiet:           opts.Quiet,
			Verbose:         opts.Verbose,
			Budget:          opts.InvocationBudget,
		}, opts.DryRun)
		if err != nil {
			return final, err
		}
		relay.Instructions = instructionsResult.Text
		final.Costs[reviewerLabel] += instructionsResult.CostUSD
		logProgress(opts, "supervisor relay instructions completed output=%s", instructionsPath)
	}

	editorSystemPrompt := editorSystemPromptForMode(opts.Mode)

	var sessionID string
	var latestReview Review
	var latestDiff string
	var latestDiffArtifact DiffArtifact
	implementSelectedPackage := initialReview == nil
	for round := 1; round <= opts.Rounds; round++ {
		logProgress(opts, "round %d/%d %s started adapter=%s", round, opts.Rounds, editorLabel, editor.ID())
		final.Phase = editorLabel
		currentRole = editorLabel
		setRoleStatus(&final, editorLabel, opts.Coder, "running", "", "")
		if stateErr := writeRunState(runDir, RunState{
			RunID:            runID,
			Mode:             opts.Mode,
			Status:           "running",
			Phase:            editorLabel,
			RoleStatuses:     final.RoleStatuses,
			CurrentRound:     round,
			LatestDiffPath:   final.LatestDiffPath,
			LatestReviewPath: final.LatestReviewPath,
		}); stateErr != nil {
			return final, mandatoryPersistenceError("round implementation state", stateErr)
		}
		editorPrompt, err := buildRoundEditorPrompt(ctx, opts, round, runDir, baseline, latestDiff, latestReview, initialReview, relay, selectedPackage, workPlan, brief, implementSelectedPackage)
		if err != nil {
			return final, err
		}
		editorPrompt = workerContractPrompt(withRepoInstructions(editorPrompt, repoInstructions))
		implementSelectedPackage = false
		editorOutputPath := filepath.Join(runDir, fmt.Sprintf("%s-round-%d.md", editorLabel, round))
		if selectedPackage != nil {
			if setPlanItemStatus(executionPlan, selectedPackage.ID, PlanStatusInProgress, "runner", fmt.Sprintf("round %d %s started", round, editorLabel)) {
				if err := persistExecutionPlan(ctx, runDir, executionPlan); err != nil {
					return final, err
				}
			}
		}
		beforeEditor, snapshotErr := captureWorktreeSnapshot(ctx, opts.Workdir)
		if snapshotErr != nil {
			return final, &ExitError{Code: ExitAdapterFailure, Err: snapshotErr}
		}
		editorRequest := Request{
			Context:               ctx,
			Prompt:                editorPrompt,
			SystemPrompt:          editorSystemPrompt,
			EnvOverlay:            opts.EnvOverlay,
			Model:                 opts.Coder.Model,
			Workdir:               opts.Workdir,
			RunDir:                runDir,
			OutputPath:            editorOutputPath,
			SchemaPath:            workerSchemaPath,
			ResumeID:              sessionID,
			Timeout:               opts.Timeout,
			WatchdogTimeout:       opts.WatchdogTimeout,
			Phase:                 fmt.Sprintf("round %d %s %s", round, editorLabel, editor.ID()),
			ProgressRole:          Role(editorLabel),
			Quiet:                 opts.Quiet,
			Verbose:               opts.Verbose,
			Budget:                opts.InvocationBudget,
			RequireWorkerContract: true,
			AllowedScope:          allowedScopeForRound(opts, selectedPackage),
		}
		editorResult, err := a.runEditorWithContractRetry(ctx, opts, editor, editorRequest, beforeEditor)
		if err != nil {
			editorResult, opts.Coder, editor, err = a.recoverRoundEditorFailure(ctx, opts, round, runDir, baseline, workerSchemaPath, sessionID, editorLabel, editorRequest, beforeEditor, editor, reviewer, registry, &meta, &final, err)
			if err != nil {
				return final, err
			}
		}
		setRoleStatus(&final, editorLabel, opts.Coder, "completed", "", "")
		logProgress(opts, "round %d %s completed output=%s", round, editorLabel, editorOutputPath)
		final.Costs[editorLabel] += editorResult.CostUSD
		if editorResult.SessionID != "" {
			sessionID = editorResult.SessionID
		}

		diffArtifact, testOutput, err := captureAndTestRound(ctx, opts, baseline, runDir, runID, round, selectedPackage, &final)
		if err != nil {
			return final, err
		}
		diff := diffArtifact.Patch
		latestDiff = diff
		latestDiffArtifact = diffArtifact

		if opts.Mode == ModeRelay && scoutAvailable {
			if err := a.runPostScout(ctx, opts, round, runDir, diff, testOutput, repoInstructions, scout, registry, &relay, &final); err != nil {
				return final, err
			}
		}

		currentRole = reviewerLabel
		review, err := a.runRoundReview(ctx, opts, round, runDir, schemaPath, baseline, diff, diffArtifact.PatchPath, testOutput, editorOutputPath, repoInstructions, reviewerLabel, reviewer, relay, latestReview, executionPlan, &final)
		if err != nil {
			return final, err
		}
		latestReview = *review

		if review.Verdict == "pass" {
			if selectedPackage != nil {
				setPlanItemStatus(executionPlan, selectedPackage.ID, PlanStatusPassed, reviewerLabel, fmt.Sprintf("round %d review passed", round))
			}
			if opts.Mode == ModeSupervisor && opts.AutoNextPackage && workPlan != nil && selectedPackage != nil {
				nextPackage, ok := nextWorkPackage(*workPlan, selectedPackage.ID)
				if ok && round < opts.Rounds {
					workPlan.SelectedPackage = nextPackage.ID
					selectedPackage = &nextPackage
					relay.WorkPlan = workPlan
					relay.WorkPackage = selectedPackage
					final.WorkPlan = workPlan
					final.SelectedPackage = selectedPackage
					final.RemainingPackages = workPlan.RemainingPackageTitles()
					brief = BuildWorkPackageBrief(*workPlan, nextPackage)
					implementSelectedPackage = true
					if err := persistExecutionPlan(ctx, runDir, executionPlan); err != nil {
						return final, err
					}
					logProgress(opts, "package %s passed; continuing to %s: %s", reviewPackageID(final.SelectedPackage), nextPackage.ID, nextPackage.Title)
					continue
				}
			}
			if selectedPackage != nil && !opts.AutoNextPackage {
				deferRemainingPlanItems(executionPlan, selectedPackage.ID, "runner", "remaining packages not run without --auto-next-package")
			}
			if err := persistExecutionPlan(ctx, runDir, executionPlan); err != nil {
				return final, err
			}
			final.Verdict = "pass"
			final.Summary = review.Summary
			if len(final.RemainingPackages) > 0 {
				final.Summary = appendRemainingPackagesSummary(final.Summary, final.RemainingPackages)
			}
			break
		}
		if review.OnlyMinorOrNit() {
			if selectedPackage != nil {
				setPlanItemStatus(executionPlan, selectedPackage.ID, PlanStatusPassed, reviewerLabel, fmt.Sprintf("round %d review had only minor/nit findings", round))
			}
			if selectedPackage != nil && !opts.AutoNextPackage {
				deferRemainingPlanItems(executionPlan, selectedPackage.ID, "runner", "remaining packages not run without --auto-next-package")
			}
			if err := persistExecutionPlan(ctx, runDir, executionPlan); err != nil {
				return final, err
			}
			final.Verdict = review.Verdict
			final.Summary = review.Summary
			if len(final.RemainingPackages) > 0 {
				final.Summary = appendRemainingPackagesSummary(final.Summary, final.RemainingPackages)
			}
			break
		}
		if round == opts.Rounds {
			if selectedPackage != nil {
				setPlanItemStatus(executionPlan, selectedPackage.ID, PlanStatusFailed, reviewerLabel, fmt.Sprintf("round limit reached with %s review", review.Verdict))
			}
			if err := persistExecutionPlan(ctx, runDir, executionPlan); err != nil {
				return final, err
			}
			final.Verdict = review.Verdict
			final.Summary = review.Summary
			final.RoundLimitReached = true
			logProgress(opts, "round limit reached after %d rounds; collecting final reports", opts.Rounds)
			reports, reportCosts := a.collectRoundLimitReports(ctx, opts, runDir, baseline, diff, *review, final.Tests, repoInstructions)
			final.RoundLimitReports = reports
			for role, cost := range reportCosts {
				final.Costs[role] += cost
			}
			final.Summary = strings.TrimSpace(final.Summary + "\n\nRound limit reached; no more edits were requested. Final reports were collected from both agents when available.")
		}
	}

	if err := a.finalizeReviewedRun(opts, runDir, budget, latestDiffArtifact, executionPlan, selectedPackage, &final); err != nil {
		return final, err
	}
	runCompleted = true
	if final.ExitCode != ExitSuccess {
		return final, &ExitError{Code: final.ExitCode, Err: fmt.Errorf("run completed with exit code %d", final.ExitCode)}
	}
	return final, nil
}
