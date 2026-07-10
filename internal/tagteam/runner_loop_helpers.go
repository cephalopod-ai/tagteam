package tagteam

import (
	"context"
	"fmt"
	"path/filepath"
	"time"
)

func buildRoundEditorPrompt(ctx context.Context, opts RunOptions, round int, runDir, baseline, latestDiff string, latestReview Review, initialReview *Review, relay RelayContext, selectedPackage *WorkPackage, workPlan *WorkPlan, brief string, implementSelectedPackage bool) (string, error) {
	switch {
	case round == 1 && initialReview == nil && opts.Mode == ModeRelay:
		return BuildRelayCoderPrompt(opts.Workdir, opts.Prompt, relay.Brief, relay.Instructions, relay.Scout), nil
	case implementSelectedPackage && opts.Mode == ModeSupervisor && selectedPackage != nil && workPlan != nil:
		return BuildWorkerPackageImplementPrompt(opts.Workdir, opts.Prompt, *workPlan, *selectedPackage), nil
	case round == 1 && initialReview == nil && opts.Mode == ModeSupervisor:
		return BuildWorkerImplementPrompt(opts.Workdir, opts.Prompt, brief), nil
	case round == 1 && initialReview == nil:
		return BuildCoderPrompt(opts.Workdir, opts.Prompt), nil
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
		return BuildRelayFixPrompt(round, opts.Prompt, diff, relay.Brief, relay.Instructions, relay.Scout, relay.PostScout, review), nil
	case opts.Mode == ModeSupervisor && selectedPackage != nil:
		return BuildWorkerPackageFixPrompt(round, opts.Prompt, diff, *selectedPackage, review), nil
	case opts.Mode == ModeSupervisor:
		return BuildWorkerFixPrompt(round, opts.Prompt, diff, review), nil
	default:
		return BuildFixPrompt(round, opts.Prompt, diff, review), nil
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
	_ = writeRunState(runDir, RunState{RunID: runID, Mode: opts.Mode, Status: "running", Phase: string(PhaseImplementing), RoleStatuses: final.RoleStatuses, CurrentRound: round, LatestDiffPath: final.LatestDiffPath, LatestReviewPath: final.LatestReviewPath})
	logProgress(opts, "round %d diff captured bytes=%d path=%s", round, len(diffArtifact.Patch), diffArtifact.PatchPath)
	gateResult := evaluateQualityGates(ctx, opts, baseline, round, diffArtifact, allowedScopeForRound(opts, selectedPackage))
	final.QualityGates = append(final.QualityGates, gateResult)
	_ = writeJSONWithNewline(filepath.Join(runDir, fmt.Sprintf("quality-gates-round-%d.json", round)), gateResult)
	if summary, ledgerErr := updateFindingsLedger(runDir, round, nil, &gateResult); ledgerErr != nil {
		return DiffArtifact{}, "", &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("update findings ledger: %w", ledgerErr)}
	} else {
		final.Findings = summary
	}
	testOutput := ""
	if opts.TestCmd == "" || opts.NoTest {
		return diffArtifact, testOutput, nil
	}
	_ = writeRunState(runDir, RunState{RunID: runID, Mode: opts.Mode, Status: "running", Phase: string(PhaseTesting), RoleStatuses: final.RoleStatuses, CurrentRound: round, LatestDiffPath: final.LatestDiffPath, LatestReviewPath: final.LatestReviewPath})
	testPath := filepath.Join(runDir, fmt.Sprintf("test-round-%d.txt", round))
	logProgress(opts, "round %d tests started command=%q", round, opts.TestCmd)
	testRun, err := runTestCommand(ctx, opts.Workdir, opts.TestCmd, opts.Timeout, testPath, opts.DryRun, opts.EnvOverlay, opts.MaxOutputBytes, opts.TestIdentityRegex)
	if err != nil {
		return DiffArtifact{}, "", err
	}
	final.Tests = append(final.Tests, testRun)
	if final.BaselineTest != nil {
		comparison := compareRegression(*final.BaselineTest, testRun)
		final.Regression = &comparison
	}
	testOutput = testRun.Output
	if testRun.Passed {
		logProgress(opts, "round %d tests passed output=%s", round, testPath)
	} else {
		logProgress(opts, "round %d tests failed output=%s", round, testPath)
	}
	return diffArtifact, testOutput, nil
}

func (a *App) runPostScout(ctx context.Context, opts RunOptions, round int, runDir, diff, testOutput, repoInstructions string, scout Adapter, registry map[string]Adapter, relay *RelayContext, final *FinalRun) error {
	postScoutPath := filepath.Join(runDir, fmt.Sprintf("post-scout-round-%d.json", round))
	postScoutStatusPath := filepath.Join(runDir, fmt.Sprintf("post-scout-execution-round-%d.json", round))
	postScoutStatus := newScoutExecutionArtifact(opts.PostScoutMode, opts.ScoutFailurePolicy, false)
	logProgress(opts, "round %d post-scout %s started adapter=%s", round, opts.PostScoutMode, scout.ID())
	postScoutStatus.ScoutRan = true
	postScoutResult, err := a.runAdapter(ctx, scout, RoleScout, Request{
		Context: ctx, Prompt: withRepoInstructions(BuildScoutPrompt(opts.Workdir, opts.Prompt, relay.Brief, opts.PostScoutMode, "post", diff, safeTestOutput(testOutput), ""), repoInstructions), EnvOverlay: opts.EnvOverlay,
		Model: opts.Scout.Model, Workdir: opts.Workdir, RunDir: runDir, OutputPath: postScoutPath, Timeout: opts.Timeout, WatchdogTimeout: opts.WatchdogTimeout,
		Phase: fmt.Sprintf("round %d post-scout %s %s", round, opts.PostScoutMode, scout.ID()), Quiet: opts.Quiet, Verbose: opts.Verbose, Budget: opts.InvocationBudget,
	}, opts.DryRun)
	if err != nil && IsOutputContractError(err) {
		if repaired, repairCost, attempted, repairErr := a.repairJSONWithWorker(ctx, opts, registry, runDir, postScoutPath, "post-scout", ScoutSchemaForRepair(), readRepairSource(postScoutPath, nil), err); repairErr != nil {
			_ = writeRedactedBytes(postScoutPath+".repair-failed.txt", []byte(repairErr.Error()+"\n"), opts.EnvOverlay)
		} else if attempted {
			if repairedScout, parseErr := parseScout(repaired); parseErr == nil {
				setFinalDegraded(final, ReasonJSONRepairUsed, "post-scout JSON repaired by worker")
				appendRoleLoss(final, "scout", opts.LossPolicy.Scout, "json-repair", "repaired", ReasonJSONRepairUsed, "worker repaired invalid post-scout JSON")
				postScoutResult = Result{Scout: repairedScout, Text: repairedScout.Summary, CostUSD: repairCost}
				err = nil
			} else {
				_ = writeRedactedBytes(postScoutPath+".repair-validation-error.txt", []byte(parseErr.Error()+"\n"), opts.EnvOverlay)
			}
		}
	}
	if err != nil {
		postScoutStatus.FailureClass = classifyScoutFailure(err)
		postScoutStatus.Failure = err.Error()
		if policyBlocks(opts.LossPolicy.Scout) {
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
	if err := writeJSONWithNewline(postScoutStatusPath, postScoutStatus); err != nil {
		return &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("write post-scout execution artifact: %w", err)}
	}
	return nil
}

func (a *App) runRoundReview(ctx context.Context, opts RunOptions, round int, runDir, schemaPath, baseline, diff, diffPath, testOutput, editorOutputPath, repoInstructions, reviewerLabel string, reviewer Adapter, relay RelayContext, latestReview Review, executionPlan *ExecutionPlan, final *FinalRun) (*Review, error) {
	logProgress(opts, "round %d %s started adapter=%s", round, reviewerLabel, reviewer.ID())
	final.Phase = reviewerLabel
	setRoleStatus(final, reviewerLabel, opts.Adversary, "running", "", "")
	_ = writeRunState(runDir, RunState{RunID: final.RunID, Mode: opts.Mode, Status: "running", Phase: string(PhaseReviewing), RoleStatuses: final.RoleStatuses, CurrentRound: round, LatestDiffPath: final.LatestDiffPath, LatestReviewPath: final.LatestReviewPath})
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
	if summary, ledgerErr := updateFindingsLedger(runDir, round, review, nil); ledgerErr != nil {
		return nil, &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("update findings ledger: %w", ledgerErr)}
	} else {
		final.Findings = summary
	}
	_ = writeRunState(runDir, RunState{RunID: final.RunID, Mode: opts.Mode, Status: "running", Phase: string(PhaseReviewing), RoleStatuses: final.RoleStatuses, CurrentRound: round, LatestDiffPath: final.LatestDiffPath, LatestReviewPath: final.LatestReviewPath})
	return review, nil
}

func (a *App) finalizeReviewedRun(opts RunOptions, runDir string, budget *InvocationBudget, latestDiff DiffArtifact, executionPlan *ExecutionPlan, selectedPackage *WorkPackage, final *FinalRun) error {
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
		if err := persistExecutionPlan(runDir, executionPlan); err != nil {
			return err
		}
		final.Plan = summarizeExecutionPlan(runDir, executionPlan)
	}
	logProgress(opts, "run %s finished verdict=%s exit=%d rounds=%d/%d", final.RunID, final.Verdict, final.ExitCode, final.RoundsCompleted, final.RoundsRequested)
	_ = writeRunState(runDir, RunState{RunID: final.RunID, Mode: opts.Mode, Status: string(final.Status), Phase: string(PhaseReviewing), Degraded: final.Degraded, DegradedReason: final.DegradedReason, BlockingReason: final.BlockingReason, RoleStatuses: final.RoleStatuses, CurrentRound: final.RoundsCompleted, LatestDiffPath: final.LatestDiffPath, LatestReviewPath: final.LatestReviewPath, ExitCode: final.ExitCode})
	return a.persistFinal(opts.Workdir, *final)
}
