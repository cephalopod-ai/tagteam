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
	gate := runtime.pathGate
	runDir := final.RunDir
	var err error
	if runDir, err = rebindControlResumeRunDir(gate, runDir, &final, "worker-result-schema.json", "review-schema.json"); err != nil {
		return final, &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	workerSchemaPath := filepath.Join(runDir, "worker-result-schema.json")
	schemaPath := filepath.Join(runDir, "review-schema.json")
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
		if runDir, err = rebindControlResumeRunDir(gate, runDir, &final); err != nil {
			return final, &ExitError{Code: ExitPreflightFailed, Err: err}
		}
		editorOutputPath := resumeEditorOutputPath(runDir, runtime.editorLabel, round)
		if !skipEditor {
			partial := firstPhase && (phase == PhaseImplementing || phase == PhaseRepairing) && state.DiffHash != currentDiffHash
			result, outputPath, editorErr := a.resumeEditor(ctx, opts, state, round, partial, workerSchemaPath, latestReview, &runtime, &final)
			if editorErr != nil {
				return final, editorErr
			}
			editorOutputPath = outputPath
			final.Costs[runtime.editorLabel] += result.CostUSD
			setRoleStatus(&final, runtime.editorLabel, final.Coder, "completed", "", "")
		}

		diff, testOutput, captureErr := resumeCaptureRound(ctx, opts, final.Baseline, runDir, final.RunID, round, runtime.selectedPackage, !skipTests, &final, gate)
		if captureErr != nil {
			return final, captureErr
		}
		runDir = final.RunDir
		latestDiff = diff
		if skipTests {
			if runDir, err = rebindControlResumeRunDir(gate, runDir, &final); err != nil {
				return final, &ExitError{Code: ExitPreflightFailed, Err: err}
			}
			testOutput = readOptionalText(filepath.Join(runDir, fmt.Sprintf("test-round-%d.txt", round)))
		}
		if opts.Mode == ModeRelay && runtime.scout != nil {
			if runDir, err = rebindControlResumeRunDir(gate, runDir, &final); err != nil {
				return final, &ExitError{Code: ExitPreflightFailed, Err: err}
			}
			if err := a.runPostScout(ctx, opts, round, runDir, diff.Patch, testOutput, runtime.repoInstructions, runtime.scout, runtime.registry, &runtime.relay, &final); err != nil {
				return final, err
			}
			runDir = final.RunDir
		}
		if runDir, err = rebindControlResumeRunDir(gate, runDir, &final); err != nil {
			return final, &ExitError{Code: ExitPreflightFailed, Err: err}
		}
		schemaPath = filepath.Join(runDir, "review-schema.json")
		review, reviewErr := a.runRoundReview(ctx, opts, round, runDir, schemaPath, final.Baseline, diff.Patch, diff.PatchPath, testOutput, editorOutputPath, runtime.repoInstructions, runtime.reviewerLabel, runtime.reviewer, runtime.relay, latestReview, runtime.executionPlan, &final)
		if reviewErr != nil {
			return final, reviewErr
		}
		runDir = final.RunDir
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
		if runDir, err = rebindControlResumeRunDir(gate, runDir, &final, "state.json"); err != nil {
			return final, &ExitError{Code: ExitPreflightFailed, Err: err}
		}
		if err := writeRunState(runDir, RunState{RunID: final.RunID, Mode: opts.Mode, Status: "running", Phase: string(PhaseRepairing), CurrentRound: round + 1, LatestDiffPath: final.LatestDiffPath, LatestReviewPath: final.LatestReviewPath, RecoveryStatus: "review_requested_repairs"}); err != nil {
			return final, mandatoryPersistenceError("resume repair state", err)
		}
	}
	if runDir, err = rebindControlResumeRunDir(gate, runDir, &final); err != nil {
		return final, &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	if err := a.finalizeReviewedRunWithGate(opts, runDir, budget, latestDiff, runtime.executionPlan, runtime.selectedPackage, &final, gate); err != nil {
		return final, err
	}
	if final.ExitCode != ExitSuccess {
		return final, &ExitError{Code: final.ExitCode, Err: fmt.Errorf("resumed run completed with exit code %d", final.ExitCode)}
	}
	return final, nil
}

func (a *App) resumeSoloRun(ctx context.Context, opts RunOptions, state RunState, phase RunPhase, round int, currentDiffHash string, runtime resumeRuntime, final FinalRun, budget *InvocationBudget) (FinalRun, error) {
	gate := runtime.pathGate
	runDir := final.RunDir
	var err error
	if runDir, err = rebindControlResumeRunDir(gate, runDir, &final, "worker-result-schema.json"); err != nil {
		return final, &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	workerSchemaPath := filepath.Join(runDir, "worker-result-schema.json")
	if err := writeFileDurable(workerSchemaPath, []byte(WorkerResultSchema+"\n"), 0o644, true); err != nil {
		return final, err
	}
	if phase == PhaseReviewing {
		return quarantineResumedExecution(final, "solo run cannot resume a reviewing phase")
	}
	if phase != PhaseTesting {
		partial := (phase == PhaseImplementing || phase == PhaseRepairing) && state.DiffHash != currentDiffHash
		result, _, editorErr := a.resumeEditor(ctx, opts, state, round, partial, workerSchemaPath, Review{}, &runtime, &final)
		if editorErr != nil {
			return final, editorErr
		}
		final.Costs[runtime.editorLabel] += result.CostUSD
		final.Summary = result.Text
		setRoleStatus(&final, runtime.editorLabel, final.Coder, "completed", "", "")
	}
	runDir = final.RunDir
	diff, _, captureErr := resumeCaptureRound(ctx, opts, final.Baseline, runDir, final.RunID, round, nil, true, &final, gate)
	if captureErr != nil {
		return final, captureErr
	}
	runDir = final.RunDir
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
	if runDir, err = rebindControlResumeRunDir(gate, runDir, &final, "state.json", "final.json"); err != nil {
		return final, &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	terminalState := runStateForFinal(final, opts.Mode, string(PhaseTesting), "resumed")
	terminalState.CurrentRound = round
	terminalState.LatestDiffPath = diff.PatchPath
	terminalState.DiffHash = diff.Metadata.DiffSHA256
	if err := a.persistTerminalRun(opts.Workdir, &final, terminalState); err != nil {
		return final, err
	}
	if final.ExitCode != ExitSuccess {
		return final, &ExitError{Code: final.ExitCode, Err: fmt.Errorf("resumed solo run completed with exit code %d", final.ExitCode)}
	}
	return final, nil
}

func (a *App) resumeEditor(ctx context.Context, opts RunOptions, state RunState, round int, partial bool, workerSchemaPath string, latestReview Review, runtime *resumeRuntime, final *FinalRun) (Result, string, error) {
	gate := runtime.pathGate
	runDir := final.RunDir
	var err error
	if runDir, err = rebindControlResumeRunDir(gate, runDir, final); err != nil {
		return Result{}, "", &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	if priorRecoveryExists(runDir, round) && partial {
		return Result{}, "", quarantineResumedExecutionError(final, fmt.Sprintf("recovery decision already exists for round %d", round))
	}
	before, err := captureWorktreeSnapshot(ctx, opts.Workdir)
	if err != nil {
		return Result{}, "", err
	}
	prompt, err := buildRoundEditorPrompt(ctx, opts, round, runDir, final.Baseline, "", latestReview, nil, runtime.relay, runtime.selectedPackage, runtime.workPlan, runtime.relay.Brief, round == 1 && latestReview.Verdict == "")
	if err != nil {
		return Result{}, "", err
	}
	prompt = workerContractPrompt(withRepoInstructions(prompt, runtime.repoInstructions))
	// Re-resolve immediately before adapter request construction/dispatch.
	if runDir, err = rebindControlResumeRunDir(gate, runDir, final, filepath.Base(resumeEditorOutputPath(runDir, runtime.editorLabel, round))); err != nil {
		return Result{}, "", &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	outputPath := resumeEditorOutputPath(runDir, runtime.editorLabel, round)
	workerSchemaPath = filepath.Join(runDir, "worker-result-schema.json")
	setRoleStatus(final, runtime.editorLabel, final.Coder, "running", "", "")
	editorRequest := Request{Context: ctx, Prompt: prompt, SystemPrompt: editorSystemPromptForMode(opts.Mode), EnvOverlay: opts.EnvOverlay, Model: final.Coder.Model, Workdir: opts.Workdir, RunDir: runDir, OutputPath: outputPath, SchemaPath: workerSchemaPath, Timeout: opts.Timeout, WatchdogTimeout: opts.WatchdogTimeout, Phase: fmt.Sprintf("resumed round %d %s", round, runtime.editorLabel), ProgressRole: Role(runtime.editorLabel), Budget: opts.InvocationBudget, MaxOutputBytes: opts.MaxOutputBytes, RequireWorkerContract: true, controlResumeGate: gate}
	if partial {
		before = worktreeSnapshot{}
		if runDir, err = rebindControlResumeRunDir(gate, runDir, final); err != nil {
			return Result{}, "", &ExitError{Code: ExitPreflightFailed, Err: err}
		}
		editorRequest.RunDir = runDir
		editorRequest.OutputPath = resumeEditorOutputPath(runDir, runtime.editorLabel, round)
		editorRequest.SchemaPath = filepath.Join(runDir, "worker-result-schema.json")
		recovered, selectedTarget, selectedAdapter, recoveryErr := a.recoverEditorFailure(ctx, opts, round, runDir, final.Baseline, editorRequest.SchemaPath, "", editorRequest, final.Coder, runtime.editor, runtime.reviewer, runtime.registry, fmt.Errorf("invocation %s was interrupted with an uncheckpointed diff", state.InvocationID), before, final)
		if recoveryErr != nil {
			return Result{}, "", recoveryErr
		}
		final.Coder = selectedTarget
		setFinalRoleTarget(final, runtime.editorLabel, selectedTarget)
		runtime.editor = selectedAdapter
		if runDir, err = rebindControlResumeRunDir(gate, runDir, final); err != nil {
			return Result{}, "", &ExitError{Code: ExitPreflightFailed, Err: err}
		}
		return recovered, filepath.Join(runDir, fmt.Sprintf("worker-recovery-round-%d.json", round)), nil
	}
	result, err := a.runEditorWithContractRetry(ctx, opts, runtime.editor, editorRequest, before)
	if err != nil {
		runDir, rebindErr := rebindControlResumeRunDir(gate, runDir, final)
		if rebindErr != nil {
			return Result{}, "", &ExitError{Code: ExitPreflightFailed, Err: rebindErr}
		}
		editorRequest.RunDir = runDir
		editorRequest.OutputPath = resumeEditorOutputPath(runDir, runtime.editorLabel, round)
		editorRequest.SchemaPath = filepath.Join(runDir, "worker-result-schema.json")
		recovered, selectedTarget, selectedAdapter, recoveryErr := a.recoverEditorFailure(ctx, opts, round, runDir, final.Baseline, editorRequest.SchemaPath, result.SessionID, editorRequest, final.Coder, runtime.editor, runtime.reviewer, runtime.registry, err, before, final)
		if recoveryErr != nil {
			return Result{}, "", recoveryErr
		}
		final.Coder = selectedTarget
		setFinalRoleTarget(final, runtime.editorLabel, selectedTarget)
		runtime.editor = selectedAdapter
		if runDir, rebindErr = rebindControlResumeRunDir(gate, runDir, final); rebindErr != nil {
			return Result{}, "", &ExitError{Code: ExitPreflightFailed, Err: rebindErr}
		}
		return recovered, filepath.Join(runDir, fmt.Sprintf("worker-recovery-round-%d.json", round)), nil
	}
	return result, outputPath, nil
}

func resumeCaptureRound(ctx context.Context, opts RunOptions, baseline, runDir, runID string, round int, selectedPackage *WorkPackage, runTests bool, final *FinalRun, gate *controlResumePathGate) (DiffArtifact, string, error) {
	var err error
	if runDir, err = rebindControlResumeRunDir(gate, runDir, final); err != nil {
		return DiffArtifact{}, "", &ExitError{Code: ExitPreflightFailed, Err: err}
	}
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
	gateResult := evaluateQualityGates(ctx, opts, baseline, round, diff, allowedScopeForRound(opts, selectedPackage))
	final.QualityGates = replaceQualityGateForRound(final.QualityGates, gateResult)
	if runDir, err = rebindControlResumeRunDir(gate, runDir, final, fmt.Sprintf("quality-gates-round-%d.json", round), "state.json"); err != nil {
		return DiffArtifact{}, "", &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	if err := writeJSONWithNewline(filepath.Join(runDir, fmt.Sprintf("quality-gates-round-%d.json", round)), gateResult); err != nil {
		return DiffArtifact{}, "", err
	}
	if summary, ledgerErr := updateFindingsLedger(runDir, round, nil, &gateResult); ledgerErr != nil {
		return DiffArtifact{}, "", ledgerErr
	} else {
		final.Findings = summary
	}
	if err := writeRunState(runDir, RunState{RunID: runID, Mode: opts.Mode, Status: "running", Phase: string(PhaseTesting), CurrentRound: round, LatestDiffPath: diff.PatchPath, DiffHash: diff.Metadata.DiffSHA256, LatestReviewPath: final.LatestReviewPath, RecoveryStatus: "resuming"}); err != nil {
		return DiffArtifact{}, "", mandatoryPersistenceError("resumed testing state", err)
	}
	if !runTests || !hasConfiguredTests(opts) || opts.NoTest {
		return diff, "", nil
	}
	if runDir, err = rebindControlResumeRunDir(gate, runDir, final, fmt.Sprintf("test-round-%d.txt", round)); err != nil {
		return DiffArtifact{}, "", &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	path := filepath.Join(runDir, fmt.Sprintf("test-round-%d.txt", round))
	test, err := runConfiguredTestCommands(ctx, opts, path)
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
