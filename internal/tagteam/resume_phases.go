package tagteam

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (a *App) resumeReviewedRun(ctx context.Context, opts RunOptions, state RunState, phase RunPhase, round int, currentDiffHash string, prior *Review, runtime resumeRuntime, final FinalRun, budget *InvocationBudget) (FinalRun, error) {
	workerSchemaPath := filepath.Join(final.RunDir, "worker-result-schema.json")
	schemaPath := filepath.Join(final.RunDir, "review-schema.json")
	if err := writeFileDurable(workerSchemaPath, []byte(WorkerResultSchema+"\n"), 0o644, true); err != nil {
		return final, err
	}
	if err := writeFileDurable(schemaPath, []byte(ReviewSchema), 0o644, true); err != nil {
		return final, err
	}
	latestReview := Review{}
	if prior != nil {
		latestReview = *prior
	}
	firstPhase := true
	var latestDiff DiffArtifact
	for ; round <= opts.Rounds; round++ {
		skipEditor := firstPhase && (phase == PhaseTesting || phase == PhaseReviewing)
		skipTests := firstPhase && phase == PhaseReviewing
		editorOutputPath := resumeEditorOutputPath(final.RunDir, runtime.editorLabel, round)
		if !skipEditor {
			partial := firstPhase && (phase == PhaseImplementing || phase == PhaseRepairing) && state.DiffHash != currentDiffHash
			result, outputPath, err := a.resumeEditor(ctx, opts, state, round, partial, workerSchemaPath, latestReview, &runtime, &final)
			if err != nil {
				return final, err
			}
			editorOutputPath = outputPath
			final.Costs[runtime.editorLabel] += result.CostUSD
			setRoleStatus(&final, runtime.editorLabel, final.Coder, "completed", "", "")
		}

		diff, testOutput, err := resumeCaptureRound(ctx, opts, final.Baseline, final.RunDir, final.RunID, round, runtime.selectedPackage, !skipTests, &final)
		if err != nil {
			return final, err
		}
		latestDiff = diff
		if skipTests {
			testOutput = readOptionalText(filepath.Join(final.RunDir, fmt.Sprintf("test-round-%d.txt", round)))
		}
		if opts.Mode == ModeRelay && runtime.scout != nil {
			if err := a.runPostScout(ctx, opts, round, final.RunDir, diff.Patch, testOutput, runtime.repoInstructions, runtime.scout, runtime.registry, &runtime.relay, &final); err != nil {
				return final, err
			}
		}
		review, err := a.runRoundReview(ctx, opts, round, final.RunDir, schemaPath, final.Baseline, diff.Patch, diff.PatchPath, testOutput, editorOutputPath, runtime.repoInstructions, runtime.reviewerLabel, runtime.reviewer, runtime.relay, latestReview, runtime.executionPlan, &final)
		if err != nil {
			return final, err
		}
		latestReview = *review
		final.Review = review
		final.Verdict = review.Verdict
		final.Summary = review.Summary
		if review.Verdict == "pass" || review.OnlyMinorOrNit() {
			break
		}
		if round == opts.Rounds {
			final.RoundLimitReached = true
			break
		}
		phase = PhaseRepairing
		firstPhase = false
		_ = writeRunState(final.RunDir, RunState{RunID: final.RunID, Mode: opts.Mode, Status: "running", Phase: string(PhaseRepairing), CurrentRound: round + 1, LatestDiffPath: final.LatestDiffPath, LatestReviewPath: final.LatestReviewPath, RecoveryStatus: "review_requested_repairs"})
	}
	if err := a.finalizeReviewedRun(opts, final.RunDir, budget, latestDiff, runtime.executionPlan, runtime.selectedPackage, &final); err != nil {
		return final, err
	}
	if final.ExitCode != ExitSuccess {
		return final, &ExitError{Code: final.ExitCode, Err: fmt.Errorf("resumed run completed with exit code %d", final.ExitCode)}
	}
	return final, nil
}

func (a *App) resumeSoloRun(ctx context.Context, opts RunOptions, state RunState, phase RunPhase, round int, currentDiffHash string, runtime resumeRuntime, final FinalRun, budget *InvocationBudget) (FinalRun, error) {
	workerSchemaPath := filepath.Join(final.RunDir, "worker-result-schema.json")
	if err := writeFileDurable(workerSchemaPath, []byte(WorkerResultSchema+"\n"), 0o644, true); err != nil {
		return final, err
	}
	if phase == PhaseReviewing {
		return quarantineResumedExecution(final, "solo run cannot resume a reviewing phase")
	}
	if phase != PhaseTesting {
		partial := (phase == PhaseImplementing || phase == PhaseRepairing) && state.DiffHash != currentDiffHash
		result, _, err := a.resumeEditor(ctx, opts, state, round, partial, workerSchemaPath, Review{}, &runtime, &final)
		if err != nil {
			return final, err
		}
		final.Costs[runtime.editorLabel] += result.CostUSD
		final.Summary = result.Text
		setRoleStatus(&final, runtime.editorLabel, final.Coder, "completed", "", "")
	}
	diff, _, err := resumeCaptureRound(ctx, opts, final.Baseline, final.RunDir, final.RunID, round, nil, true, &final)
	if err != nil {
		return final, err
	}
	if strings.TrimSpace(final.Summary) == "" {
		final.Summary = "Solo implementation resumed and host validation completed; review was not run."
	} else {
		final.Summary = strings.TrimSpace(final.Summary + "\n\nReview was not run in solo mode.")
	}
	final.Verdict = "done"
	final.RoundsCompleted = round
	final.FinishedAt = time.Now().UTC()
	final.ExitCode = a.computeExitCode(final)
	applyInvocationBudget(&final, budget)
	finalizeRunState(&final)
	_ = writeRunState(final.RunDir, RunState{RunID: final.RunID, Mode: opts.Mode, Status: string(final.Status), Phase: string(PhaseTesting), CurrentRound: round, LatestDiffPath: diff.PatchPath, DiffHash: diff.Metadata.DiffSHA256, ExitCode: final.ExitCode, RecoveryStatus: "resumed"})
	if err := a.persistFinal(opts.Workdir, final); err != nil {
		return final, err
	}
	if final.ExitCode != ExitSuccess {
		return final, &ExitError{Code: final.ExitCode, Err: fmt.Errorf("resumed solo run completed with exit code %d", final.ExitCode)}
	}
	return final, nil
}

func (a *App) resumeEditor(ctx context.Context, opts RunOptions, state RunState, round int, partial bool, workerSchemaPath string, latestReview Review, runtime *resumeRuntime, final *FinalRun) (Result, string, error) {
	if priorRecoveryExists(final.RunDir, round) && partial {
		return Result{}, "", quarantineResumedExecutionError(final, fmt.Sprintf("recovery decision already exists for round %d", round))
	}
	before, err := captureWorktreeSnapshot(ctx, opts.Workdir)
	if err != nil {
		return Result{}, "", err
	}
	prompt, err := buildRoundEditorPrompt(ctx, opts, round, final.RunDir, final.Baseline, "", latestReview, nil, runtime.relay, runtime.selectedPackage, runtime.workPlan, runtime.relay.Brief, round == 1 && latestReview.Verdict == "")
	if err != nil {
		return Result{}, "", err
	}
	prompt = workerContractPrompt(withRepoInstructions(prompt, runtime.repoInstructions))
	outputPath := resumeEditorOutputPath(final.RunDir, runtime.editorLabel, round)
	setRoleStatus(final, runtime.editorLabel, final.Coder, "running", "", "")
	editorRequest := Request{Context: ctx, Prompt: prompt, SystemPrompt: editorSystemPromptForMode(opts.Mode), EnvOverlay: opts.EnvOverlay, Model: final.Coder.Model, Workdir: opts.Workdir, RunDir: final.RunDir, OutputPath: outputPath, SchemaPath: workerSchemaPath, Timeout: opts.Timeout, WatchdogTimeout: opts.WatchdogTimeout, Phase: fmt.Sprintf("resumed round %d %s", round, runtime.editorLabel), ProgressRole: Role(runtime.editorLabel), Budget: opts.InvocationBudget, MaxOutputBytes: opts.MaxOutputBytes, RequireWorkerContract: true}
	if partial {
		before = worktreeSnapshot{}
		recovered, selectedTarget, selectedAdapter, recoveryErr := a.recoverEditorFailure(ctx, opts, round, final.RunDir, final.Baseline, workerSchemaPath, "", editorRequest, final.Coder, runtime.editor, runtime.reviewer, runtime.registry, fmt.Errorf("invocation %s was interrupted with an uncheckpointed diff", state.InvocationID), before, final)
		if recoveryErr != nil {
			return Result{}, "", recoveryErr
		}
		final.Coder = selectedTarget
		setFinalRoleTarget(final, runtime.editorLabel, selectedTarget)
		runtime.editor = selectedAdapter
		return recovered, filepath.Join(final.RunDir, fmt.Sprintf("worker-recovery-round-%d.json", round)), nil
	}
	result, err := a.runEditorWithContractRetry(ctx, opts, runtime.editor, editorRequest, before)
	if err != nil {
		recovered, selectedTarget, selectedAdapter, recoveryErr := a.recoverEditorFailure(ctx, opts, round, final.RunDir, final.Baseline, workerSchemaPath, result.SessionID, editorRequest, final.Coder, runtime.editor, runtime.reviewer, runtime.registry, err, before, final)
		if recoveryErr != nil {
			return Result{}, "", recoveryErr
		}
		final.Coder = selectedTarget
		setFinalRoleTarget(final, runtime.editorLabel, selectedTarget)
		runtime.editor = selectedAdapter
		return recovered, filepath.Join(final.RunDir, fmt.Sprintf("worker-recovery-round-%d.json", round)), nil
	}
	return result, outputPath, nil
}

func resumeCaptureRound(ctx context.Context, opts RunOptions, baseline, runDir, runID string, round int, selectedPackage *WorkPackage, runTests bool, final *FinalRun) (DiffArtifact, string, error) {
	diff, err := captureDiffArtifact(ctx, opts.Workdir, baseline, runDir, round)
	if err != nil {
		return DiffArtifact{}, "", err
	}
	final.LatestDiffPath = diff.PatchPath
	final.LatestNumstatPath = diff.NumstatPath
	final.LatestFilesPath = diff.FilesPath
	final.LatestSHA256Path = diff.SHA256Path
	final.LatestDiffSHA256 = diff.Metadata.DiffSHA256
	final.ChangedFiles = diff.ChangedFiles()
	gate := evaluateQualityGates(ctx, opts, baseline, round, diff, allowedScopeForRound(opts, selectedPackage))
	final.QualityGates = append(final.QualityGates, gate)
	if err := writeJSONWithNewline(filepath.Join(runDir, fmt.Sprintf("quality-gates-round-%d.json", round)), gate); err != nil {
		return DiffArtifact{}, "", err
	}
	if summary, ledgerErr := updateFindingsLedger(runDir, round, nil, &gate); ledgerErr != nil {
		return DiffArtifact{}, "", ledgerErr
	} else {
		final.Findings = summary
	}
	_ = writeRunState(runDir, RunState{RunID: runID, Mode: opts.Mode, Status: "running", Phase: string(PhaseTesting), CurrentRound: round, LatestDiffPath: diff.PatchPath, DiffHash: diff.Metadata.DiffSHA256, LatestReviewPath: final.LatestReviewPath, RecoveryStatus: "resuming"})
	if !runTests || opts.TestCmd == "" || opts.NoTest {
		return diff, "", nil
	}
	path := filepath.Join(runDir, fmt.Sprintf("test-round-%d.txt", round))
	test, err := runTestCommand(ctx, opts.Workdir, opts.TestCmd, opts.Timeout, path, opts.DryRun, opts.EnvOverlay, opts.MaxOutputBytes, opts.TestIdentityRegex)
	if err != nil {
		return DiffArtifact{}, "", err
	}
	final.Tests = append(final.Tests, test)
	if final.BaselineTest != nil {
		comparison := compareRegression(*final.BaselineTest, test)
		final.Regression = &comparison
	}
	return diff, test.Output, nil
}

func resumeEditorOutputPath(runDir, editorLabel string, round int) string {
	return filepath.Join(runDir, fmt.Sprintf("%s-round-%d.md", editorLabel, round))
}

func priorRecoveryExists(runDir string, round int) bool {
	_, err := os.Stat(filepath.Join(runDir, fmt.Sprintf("recovery-round-%d.json", round)))
	return err == nil
}

func quarantineResumedExecution(final FinalRun, message string) (FinalRun, error) {
	err := quarantineResumedExecutionError(&final, message)
	return final, err
}

func quarantineResumedExecutionError(final *FinalRun, message string) error {
	final.Status = RunStatusQuarantined
	final.Verdict = "quarantined"
	final.BlockingReason = string(ReasonQuarantined)
	return &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("resume quarantined: %s", message)}
}
