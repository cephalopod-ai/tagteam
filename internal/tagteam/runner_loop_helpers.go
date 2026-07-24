package tagteam

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

func runIDForOptions(opts RunOptions) (string, error) {
	runID := opts.RunID
	if runID == "" {
		runID = newRunID()
	}
	if err := validateRunID(runID); err != nil {
		return "", &ExitError{Code: ExitInvalidArguments, Err: err}
	}
	return runID, nil
}

func (a *App) recoverRoundEditorFailure(
	ctx context.Context,
	opts RunOptions,
	round int,
	runDir, baseline, workerSchemaPath, sessionID, editorLabel string,
	editorRequest Request,
	beforeEditor worktreeSnapshot,
	editor, reviewer Adapter,
	registry map[string]Adapter,
	meta *Meta,
	final *FinalRun,
	cause error,
) (Result, RoleTarget, Adapter, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, opts.Coder, editor, err
	}
	setRoleStatus(final, editorLabel, opts.Coder, "failed", classifyRoleFailure(editorLabel, cause), cause.Error())
	if IsIntegrityViolation(cause) {
		final.Status = RunStatusQuarantined
		final.BlockingReason = string(ReasonQuarantined)
		return Result{}, opts.Coder, editor, cause
	}
	recovered, selectedTarget, selectedAdapter, err := a.recoverEditorFailure(ctx, opts, round, runDir, baseline, workerSchemaPath, sessionID, editorRequest, opts.Coder, editor, reviewer, registry, cause, beforeEditor, final)
	if err != nil {
		return Result{}, opts.Coder, editor, err
	}
	if roleTargetString(selectedTarget) != roleTargetString(opts.Coder) {
		setFinalDegraded(final, ReasonFallbackUsed, fmt.Sprintf("%s runtime fallback selected", editorLabel))
		appendRoleLoss(final, editorLabel, opts.LossPolicy.Worker, "runtime_replace", "fallback_selected", ReasonFallbackUsed, fmt.Sprintf("%s -> %s", roleTargetString(opts.Coder), roleTargetString(selectedTarget)))
	}
	final.Coder = selectedTarget
	meta.Adapters[editorLabel] = selectedTarget.Adapter
	meta.Models[editorLabel] = selectedTarget.Model
	setFinalRoleTarget(final, editorLabel, selectedTarget)
	if err := writeJSON(filepath.Join(runDir, "meta.json"), *meta); err != nil {
		return Result{}, opts.Coder, editor, err
	}
	return recovered, selectedTarget, selectedAdapter, nil
}

func (a *App) runEditorWithContractRetry(ctx context.Context, opts RunOptions, editor Adapter, req Request, before worktreeSnapshot) (Result, error) {
	result, err := a.runAdapter(ctx, editor, RoleCoder, req, opts.DryRun)
	if cancelErr := ctx.Err(); cancelErr != nil {
		return result, cancelErr
	}
	if err == nil || !IsOutputContractError(err) || opts.DryRun {
		return result, err
	}
	after, snapshotErr := captureWorktreeSnapshot(context.Background(), opts.Workdir)
	if snapshotErr != nil || len(worktreeDelta(before, after)) != 0 {
		return result, err
	}

	logProgress(opts, "%s output invalid with no edits; retrying once error=%q", roleLabelsEditor(opts.Mode), err.Error())
	req.Prompt += "\n\nYour previous response failed the worker-result contract and made no repository changes. Execute the requested implementation now. Do not answer with model identity or capability prose. Return only the required JSON envelope after editing and validation.\n\nValidation error:\n" + err.Error()
	if controlResumeBeforeContractRetryHook != nil && controlResumeGateFrom(ctx) != nil {
		controlResumeBeforeContractRetryHook()
	}
	if req.OutputPath != "" {
		if rebindErr := bindControlResumeRequest(ctx, &req); rebindErr != nil {
			return result, &ExitError{Code: ExitPreflightFailed, Err: rebindErr}
		}
		retryPromptPath := req.OutputPath + ".retry-prompt.md"
		ext := filepath.Ext(req.OutputPath)
		retryOutputPath := strings.TrimSuffix(req.OutputPath, ext) + ".retry" + ext
		if gate := req.controlResumeGate; gate != nil {
			if guardErr := guardControlResumeWritePath(gate, retryPromptPath); guardErr != nil {
				return result, &ExitError{Code: ExitPreflightFailed, Err: guardErr}
			}
			if guardErr := guardControlResumeWritePath(gate, retryOutputPath); guardErr != nil {
				return result, &ExitError{Code: ExitPreflightFailed, Err: guardErr}
			}
		}
		if writeErr := writeRedactedBytes(retryPromptPath, []byte(req.Prompt), req.EnvOverlay); writeErr != nil {
			return result, writeErr
		}
		req.OutputPath = retryOutputPath
	}
	req.Phase += " contract retry"
	return a.runAdapter(ctx, editor, RoleCoder, req, opts.DryRun)
}

func buildRoundEditorPrompt(ctx context.Context, opts RunOptions, round int, runDir, baseline string, baselineTest *TestRun, latestDiff string, latestReview Review, initialReview *Review, relay RelayContext, selectedPackage *WorkPackage, workPlan *WorkPlan, brief string, implementSelectedPackage bool) (string, error) {
	switch {
	case round == 1 && initialReview == nil && opts.Mode == ModeRelay:
		return appendHostBaselineEvidence(BuildRelayCoderPrompt(opts.Workdir, opts.Prompt, relay.Brief, relay.Instructions, relay.Scout), baselineTest), nil
	case implementSelectedPackage && opts.Mode == ModeSupervisor && selectedPackage != nil && workPlan != nil:
		return appendHostBaselineEvidence(BuildWorkerPackageImplementPrompt(opts.Workdir, opts.Prompt, *workPlan, *selectedPackage), baselineTest), nil
	case round == 1 && initialReview == nil && opts.Mode == ModeSupervisor:
		return appendHostBaselineEvidence(BuildWorkerImplementPrompt(opts.Workdir, opts.Prompt, brief), baselineTest), nil
	case round == 1 && initialReview == nil:
		return appendHostBaselineEvidence(BuildCoderPrompt(opts.Workdir, opts.Prompt), baselineTest), nil
	}
	diff := latestDiff
	if diff == "" {
		patch, err := deterministicDiffPatch(ctx, opts.Workdir, baseline, filepath.Join(runDir, fmt.Sprintf("tmp-prompt-round-%d.index", round)))
		if err != nil {
			return "", err
		}
		diff = string(patch)
	}
	review := latestReview
	if round == 1 && initialReview != nil {
		review = *initialReview
	}
	switch {
	case opts.Mode == ModeRelay:
		return appendHostBaselineEvidence(BuildRelayFixPrompt(round, opts.Prompt, diff, relay.Brief, relay.Instructions, relay.Scout, relay.PostScout, review), baselineTest), nil
	case opts.Mode == ModeSupervisor && selectedPackage != nil:
		return appendHostBaselineEvidence(BuildWorkerPackageFixPrompt(round, opts.Prompt, diff, *selectedPackage, review), baselineTest), nil
	case opts.Mode == ModeSupervisor:
		return appendHostBaselineEvidence(BuildWorkerFixPrompt(round, opts.Prompt, diff, review), baselineTest), nil
	default:
		return appendHostBaselineEvidence(BuildFixPrompt(round, opts.Prompt, diff, review), baselineTest), nil
	}
}

func captureAndTestRound(ctx context.Context, opts RunOptions, baseline, runDir, runID string, round int, selectedPackage *WorkPackage, final *FinalRun) (DiffArtifact, string, error) {
	diffArtifact, err := captureDiffArtifact(ctx, opts.Workdir, baseline, runDir, round)
	if err != nil {
		return DiffArtifact{}, "", err
	}
	final.LatestDiffPath = diffArtifact.PatchPath
	final.LatestNumstatPath = diffArtifact.NumstatPath
	final.LatestFilesPath = diffArtifact.FilesPath
	final.LatestSHA256Path = diffArtifact.SHA256Path
	final.LatestDiffSHA256 = diffArtifact.Metadata.DiffSHA256
	if err := writeRunState(runDir, RunState{RunID: runID, Mode: opts.Mode, Status: "running", Phase: string(PhaseImplementing), RoleStatuses: final.RoleStatuses, CurrentRound: round, LatestDiffPath: final.LatestDiffPath, LatestReviewPath: final.LatestReviewPath}); err != nil {
		return DiffArtifact{}, "", mandatoryPersistenceError("implementation run state", err)
	}
	logProgress(opts, "round %d diff captured bytes=%d path=%s", round, len(diffArtifact.Patch), diffArtifact.PatchPath)
	gateResult := evaluateQualityGates(ctx, opts, baseline, round, diffArtifact, allowedScopeForRound(opts, selectedPackage))
	final.QualityGates = append(final.QualityGates, gateResult)
	if err := writeJSONWithNewline(filepath.Join(runDir, fmt.Sprintf("quality-gates-round-%d.json", round)), gateResult); err != nil {
		return DiffArtifact{}, "", mandatoryPersistenceError("quality gate", err)
	}
	if summary, ledgerErr := updateFindingsLedger(runDir, round, nil, &gateResult); ledgerErr != nil {
		return DiffArtifact{}, "", &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("update findings ledger: %w", ledgerErr)}
	} else {
		final.Findings = summary
	}
	testOutput := ""
	if !hasConfiguredTests(opts) || opts.NoTest {
		return diffArtifact, testOutput, nil
	}
	if err := writeRunState(runDir, RunState{RunID: runID, Mode: opts.Mode, Status: "running", Phase: string(PhaseTesting), RoleStatuses: final.RoleStatuses, CurrentRound: round, LatestDiffPath: final.LatestDiffPath, LatestReviewPath: final.LatestReviewPath}); err != nil {
		return DiffArtifact{}, "", mandatoryPersistenceError("testing run state", err)
	}
	testPath := filepath.Join(runDir, fmt.Sprintf("test-round-%d.txt", round))
	logProgress(opts, "round %d tests started command=%q", round, testCommandDescription(opts))
	testRun, err := runConfiguredTestCommands(ctx, opts, testPath)
	if err != nil {
		return DiffArtifact{}, "", err
	}
	final.Tests = append(final.Tests, testRun)
	if final.BaselineTest != nil {
		comparison := compareRegression(*final.BaselineTest, testRun)
		final.Regression = &comparison
	}
	testOutput = reviewTestEvidence(testRun)
	if testRun.Passed {
		logProgress(opts, "round %d tests passed output=%s", round, testPath)
	} else {
		logProgress(opts, "round %d tests failed output=%s", round, testPath)
	}
	return diffArtifact, testOutput, nil
}

func reviewTestEvidence(testRun TestRun) string {
	result := "failed"
	if testRun.Passed {
		result = "passed"
	}
	output := strings.TrimSpace(testRun.Output)
	if output == "" {
		output = "(no output)"
	}
	return fmt.Sprintf("Command: %s\nResult: %s\nOutput:\n%s", testRun.Command, result, output)
}

func (a *App) runPostScout(ctx context.Context, opts RunOptions, round int, runDir, diff, testOutput, repoInstructions string, scout Adapter, registry map[string]Adapter, relay *RelayContext, final *FinalRun) error {
	var err error
	if runDir, err = rebindControlResumeFromContext(ctx, runDir, final); err != nil {
		return &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	postScoutPath := filepath.Join(runDir, fmt.Sprintf("post-scout-round-%d.json", round))
	postScoutStatusPath := filepath.Join(runDir, fmt.Sprintf("post-scout-execution-round-%d.json", round))
	postScoutStatus := newScoutExecutionArtifact(opts.PostScoutMode, opts.ScoutFailurePolicy, false)
	logProgress(opts, "round %d post-scout %s started adapter=%s", round, opts.PostScoutMode, scout.ID())
	postScoutStatus.ScoutRan = true
	postScoutResult, err := a.runAdapter(ctx, scout, RoleScout, Request{
		Context: ctx, Prompt: withRepoInstructions(BuildScoutPrompt(opts.Workdir, opts.Prompt, relay.Brief, opts.PostScoutMode, "post", diff, safeTestOutput(testOutput), "", final.BaselineTest), repoInstructions), EnvOverlay: opts.EnvOverlay,
		Model: opts.Scout.Model, Workdir: opts.Workdir, RunDir: runDir, OutputPath: postScoutPath, Timeout: opts.Timeout, WatchdogTimeout: opts.WatchdogTimeout,
		Phase: fmt.Sprintf("round %d post-scout %s %s", round, opts.PostScoutMode, scout.ID()), Quiet: opts.Quiet, Verbose: opts.Verbose, Budget: opts.InvocationBudget,
		controlResumeGate: controlResumeGateFrom(ctx),
	}, opts.DryRun)
	if err != nil && IsOutputContractError(err) {
		if repaired, ok, rerr := a.tryScoutContractRepair(ctx, opts, registry, runDir, postScoutPath, "post-scout", "post-scout JSON repaired by worker", err, final); rerr != nil {
			return rerr
		} else if ok {
			postScoutResult = repaired
			err = nil
		}
	}
	if err != nil {
		postScoutStatus.FailureClass = classifyScoutFailure(err)
		postScoutStatus.Failure = err.Error()
		if shouldBlockScoutFailure(opts.LossPolicy.Scout, err) {
			if writeDir, rebindErr := rebindControlResumeFromContext(ctx, runDir, final); rebindErr != nil {
				return &ExitError{Code: ExitPreflightFailed, Err: rebindErr}
			} else {
				runDir = writeDir
				postScoutStatusPath = filepath.Join(runDir, fmt.Sprintf("post-scout-execution-round-%d.json", round))
			}
			_ = writeJSONWithNewline(postScoutStatusPath, postScoutStatus)
			return &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("post-scout failed and scout_failure_policy=fail; aborting relay run: %w", err)}
		}
		setFinalDegraded(final, ReasonScoutUnavailable, "post-scout failed; continuing without post-scout context")
		appendRoleLoss(final, "scout", opts.LossPolicy.Scout, "post-scout", "degraded", classifyRoleFailure("scout", err), err.Error())
		postScoutStatus.ContinuedWithoutScoutContext = true
		logProgress(opts, "round %d post-scout failed; continuing without post-scout context error=%q", round, err.Error())
	} else {
		postScoutStatus.ScoutSucceeded = true
		if postScoutResult.Scout != nil {
			relay.PostScout = *postScoutResult.Scout
		}
		final.Costs["scout"] += postScoutResult.CostUSD
		logProgress(opts, "round %d post-scout %s completed output=%s", round, opts.PostScoutMode, postScoutPath)
	}
	if writeDir, rebindErr := rebindControlResumeFromContext(ctx, runDir, final); rebindErr != nil {
		return &ExitError{Code: ExitPreflightFailed, Err: rebindErr}
	} else {
		runDir = writeDir
		postScoutStatusPath = filepath.Join(runDir, fmt.Sprintf("post-scout-execution-round-%d.json", round))
	}
	if err := writeJSONWithNewline(postScoutStatusPath, postScoutStatus); err != nil {
		return &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("write post-scout execution artifact: %w", err)}
	}
	return nil
}

func (a *App) runRoundReview(ctx context.Context, opts RunOptions, round int, runDir, schemaPath, baseline, diff, diffPath, testOutput, editorOutputPath, repoInstructions, reviewerLabel string, reviewer Adapter, relay RelayContext, latestReview Review, executionPlan *ExecutionPlan, final *FinalRun) (*Review, error) {
	var rebindErr error
	if runDir, rebindErr = rebindControlResumeFromContext(ctx, runDir, final, "state.json"); rebindErr != nil {
		return nil, &ExitError{Code: ExitPreflightFailed, Err: rebindErr}
	}
	schemaPath = filepath.Join(runDir, filepath.Base(schemaPath))
	logProgress(opts, "round %d %s started adapter=%s", round, reviewerLabel, reviewer.ID())
	final.Phase = reviewerLabel
	setRoleStatus(final, reviewerLabel, opts.Adversary, "running", "", "")
	if err := writeRunState(runDir, RunState{RunID: final.RunID, Mode: opts.Mode, Status: "running", Phase: string(PhaseReviewing), RoleStatuses: final.RoleStatuses, CurrentRound: round, LatestDiffPath: final.LatestDiffPath, LatestReviewPath: final.LatestReviewPath}); err != nil {
		return nil, mandatoryPersistenceError("review run state", err)
	}
	var priorReview *Review
	if latestReview.Verdict != "" {
		priorReview = &latestReview
	}
	review, cost, reviewPath, err := a.runAdversary(ctx, opts, round, runDir, schemaPath, opts.Prompt, baseline, diff, diffPath, testOutput, editorOutputPath, priorReview, relay, repoInstructions, final)
	if err != nil {
		reason := classifyRoleFailure(reviewerLabel, err)
		setRoleStatus(final, reviewerLabel, opts.Adversary, "failed", reason, err.Error())
		setFinalBlocking(final, reason, err.Error())
		return nil, err
	}
	setRoleStatus(final, reviewerLabel, opts.Adversary, "completed", "", "")
	appendReviewFindingPlanItems(executionPlan, *review, round)
	logProgress(opts, "round %d %s completed verdict=%s findings=%d output=%s", round, reviewerLabel, review.Verdict, len(review.Findings), reviewPath)
	final.Costs[reviewerLabel] += cost
	final.RoundsCompleted = round
	final.Review = review
	final.LatestReviewPath = reviewPath
	if runDir, rebindErr = rebindControlResumeFromContext(ctx, runDir, final); rebindErr != nil {
		return nil, &ExitError{Code: ExitPreflightFailed, Err: rebindErr}
	}
	if summary, ledgerErr := updateFindingsLedger(runDir, round, review, nil); ledgerErr != nil {
		return nil, &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("update findings ledger: %w", ledgerErr)}
	} else {
		final.Findings = summary
	}
	if runDir, rebindErr = rebindControlResumeFromContext(ctx, runDir, final, "state.json"); rebindErr != nil {
		return nil, &ExitError{Code: ExitPreflightFailed, Err: rebindErr}
	}
	if err := writeRunState(runDir, RunState{RunID: final.RunID, Mode: opts.Mode, Status: "running", Phase: string(PhaseReviewing), RoleStatuses: final.RoleStatuses, CurrentRound: round, LatestDiffPath: final.LatestDiffPath, LatestReviewPath: final.LatestReviewPath}); err != nil {
		return nil, mandatoryPersistenceError("completed review run state", err)
	}
	return review, nil
}

func (a *App) finalizeReviewedRun(opts RunOptions, runDir string, budget *InvocationBudget, latestDiff DiffArtifact, executionPlan *ExecutionPlan, selectedPackage *WorkPackage, final *FinalRun) error {
	return a.finalizeReviewedRunWithGate(opts, runDir, budget, latestDiff, executionPlan, selectedPackage, final, nil)
}

func (a *App) finalizeReviewedRunWithGate(opts RunOptions, runDir string, budget *InvocationBudget, latestDiff DiffArtifact, executionPlan *ExecutionPlan, selectedPackage *WorkPackage, final *FinalRun, gate *controlResumePathGate) error {
	var err error
	if runDir, err = rebindControlResumeRunDir(gate, runDir, final); err != nil {
		return &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	final.ChangedFiles = latestDiff.ChangedFiles()
	final.FinishedAt = time.Now().UTC()
	final.ExitCode = a.computeExitCode(*final)
	if final.ExitCode == ExitSuccess && final.Review != nil && final.Review.OnlyMinorOrNit() && len(final.Review.Findings) > 0 {
		final.DegradedReason = "review_passed_with_nonblocking_findings"
		final.Degraded = true
	}
	if final.ExitCode == ExitTestsFailed {
		setFinalBlocking(final, ReasonTestFailed, "latest test command failed")
	}
	if final.RoundLimitReached {
		setFinalBlocking(final, ReasonRoundsExhausted, "round limit reached")
	}
	applyInvocationBudget(final, budget)
	finalizeRunState(final)
	if executionPlan != nil {
		if final.ExitCode == ExitTestsFailed && selectedPackage != nil {
			setPlanItemStatus(executionPlan, selectedPackage.ID, PlanStatusFailed, "runner", "latest test command failed")
		}
		finalizeExecutionPlan(executionPlan, final.ExitCode)
		if runDir, err = rebindControlResumeRunDir(gate, runDir, final, "plan.json"); err != nil {
			return &ExitError{Code: ExitPreflightFailed, Err: err}
		}
		if err := persistExecutionPlan(withControlResumeGate(context.Background(), gate), runDir, executionPlan); err != nil {
			return err
		}
		final.Plan = summarizeExecutionPlan(runDir, executionPlan)
	}
	logProgress(opts, "run %s finished verdict=%s exit=%d rounds=%d/%d", final.RunID, final.Verdict, final.ExitCode, final.RoundsCompleted, final.RoundsRequested)
	if runDir, err = rebindControlResumeRunDir(gate, runDir, final, "state.json", "final.json"); err != nil {
		return &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	state := runStateForFinal(*final, opts.Mode, string(PhaseReviewing), "")
	return a.persistTerminalRun(opts.Workdir, final, state)
}
