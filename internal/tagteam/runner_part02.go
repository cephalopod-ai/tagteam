package tagteam

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"
)

func (a *App) Review(ctx context.Context, opts RunOptions, prompt string) (final FinalRun, err error) {
	normalizeDefaultMode(&opts)
	if opts.MaxWallTime > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.MaxWallTime)
		defer cancel()
	}
	if err = validateReviewRoles(opts); err != nil {
		return FinalRun{}, err
	}
	if err = a.validateReviewTargets(opts); err != nil {
		return FinalRun{}, err
	}
	_, reviewerLabel := roleLabels(opts.Mode)
	registry := Registry(a.Config, opts)
	reviewer, ok := registry[opts.Adversary.Adapter]
	if !ok {
		return FinalRun{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("unknown %s adapter %q", reviewerLabel, opts.Adversary.Adapter)}
	}
	baseline, err := ensureGitRepo(opts.Workdir)
	if err != nil {
		return FinalRun{}, err
	}
	if err = checkAdapters(ctx, reviewer); err != nil {
		return FinalRun{}, err
	}
	runID := newRunID()
	runDir, err := createRunDir(opts.Workdir, opts.StateRoot, runID)
	if err != nil {
		return FinalRun{}, &ExitError{Code: ExitAdapterFailure, Err: err}
	}
	lock, err := acquireRunLock(runDir, false)
	if err != nil {
		return FinalRun{}, &ExitError{Code: ExitAdapterFailure, Err: err}
	}
	defer lock.Release()
	activateRun(opts.Workdir, runID, runDir, opts.Mode)
	runCompleted := false
	defer func() { deactivateRun(opts.Workdir, runID, runCompleted) }()
	if prompt == "" {
		if latestPrompt, _ := readLatestPrompt(opts.Workdir); latestPrompt != "" {
			prompt = latestPrompt
		} else {
			prompt = "Review the current working tree diff."
		}
	}
	budget := &InvocationBudget{Max: opts.MaxRoleInvocations}
	opts.InvocationBudget = budget
	savedCoder := RoleTarget{}
	if opts.CoderExplicit {
		savedCoder = opts.Coder
	}
	final = FinalRun{
		SchemaVersion:   ArtifactSchemaVersion,
		RunID:           runID,
		RunDir:          runDir,
		Workdir:         opts.Workdir,
		Baseline:        baseline,
		Mode:            opts.Mode,
		Coder:           savedCoder,
		Adversary:       opts.Adversary,
		RoundsRequested: 1,
		Caps:            runCaps(opts),
		Costs:           map[string]float64{},
		Adapters:        map[string]string{reviewerLabel: opts.Adversary.Adapter},
		Models:          map[string]string{reviewerLabel: opts.Adversary.Model},
		StartedAt:       time.Now().UTC(),
	}
	initFinalState(&final, opts)
	defer func() {
		if err == nil || final.RunID == "" || !final.FinishedAt.IsZero() {
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
		final.FinishedAt = time.Now().UTC()
		if IsIntegrityViolation(err) {
			final.Status = RunStatusQuarantined
			final.BlockingReason = string(ReasonQuarantined)
		}
		setRoleStatus(&final, reviewerLabel, opts.Adversary, "failed", classifyRoleFailure(reviewerLabel, err), err.Error())
		setFinalBlocking(&final, classifyRoleFailure(reviewerLabel, err), err.Error())
		applyInvocationBudget(&final, budget)
		finalizeRunState(&final)
		state := runStateForFinal(final, opts.Mode, "review", "")
		state.CurrentRound = 1
		if persistErr := a.persistTerminalRun(opts.Workdir, &final, state); persistErr != nil {
			err = errors.Join(err, persistErr)
		}
	}()
	logJSONRepairPolicy(opts)
	if err = writeRedactedBytes(filepath.Join(runDir, "input.md"), []byte(prompt), opts.EnvOverlay); err != nil {
		return final, err
	}
	repoInstructions, err := loadAndPersistRepoInstructions(ctx, opts, runDir)
	if err != nil {
		return final, err
	}
	meta := Meta{
		SchemaVersion: ArtifactSchemaVersion,
		RunID:         runID,
		Workdir:       opts.Workdir,
		Baseline:      baseline,
		Command:       "review",
		Prompt:        redactSecretsWithOverlay(prompt, opts.EnvOverlay),
		StartedAt:     final.StartedAt,
		Adapters:      final.Adapters,
		Models:        final.Models,
		ConfigSources: opts.ConfigSources,
	}
	if err = writeJSON(filepath.Join(runDir, "meta.json"), meta); err != nil {
		return final, err
	}
	setRoleStatus(&final, reviewerLabel, opts.Adversary, "running", "", "")
	final.Phase = "review"
	if stateErr := writeRunState(runDir, RunState{
		RunID:            runID,
		Mode:             opts.Mode,
		Status:           "running",
		Phase:            "review",
		RoleStatuses:     final.RoleStatuses,
		CurrentRound:     1,
		LatestDiffPath:   final.LatestDiffPath,
		LatestReviewPath: final.LatestReviewPath,
	}); stateErr != nil {
		return final, mandatoryPersistenceError("running review state", stateErr)
	}
	schemaPath := filepath.Join(runDir, "review-schema.json")
	if err = writeFileDurable(schemaPath, []byte(ReviewSchema), 0o644, true); err != nil {
		return final, err
	}
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
	gateResult := evaluateQualityGates(ctx, opts, baseline, 1, diffArtifact, allowedScopeForRound(opts, nil))
	final.QualityGates = append(final.QualityGates, gateResult)
	if err = writeJSONWithNewline(filepath.Join(runDir, "quality-gates-round-1.json"), gateResult); err != nil {
		return final, err
	}
	if final.Findings, err = updateFindingsLedger(runDir, 1, nil, &gateResult); err != nil {
		return final, &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("update findings ledger: %w", err)}
	}
	testOutput := ""
	if hasConfiguredTests(opts) && !opts.NoTest {
		testPath := filepath.Join(runDir, "test-round-1.txt")
		logProgress(opts, "review tests started command=%q", testCommandDescription(opts))
		testRun, testErr := runConfiguredTestCommands(ctx, opts, testPath)
		if testErr != nil {
			return final, testErr
		}
		final.Tests = append(final.Tests, testRun)
		testOutput = reviewTestEvidence(testRun)
		logProgress(opts, "review tests completed passed=%t output=%s", testRun.Passed, testPath)
	}
	review, cost, outputPath, err := a.runAdversary(ctx, opts, 1, runDir, schemaPath, prompt, baseline, diffArtifact.Patch, diffArtifact.PatchPath, testOutput, "", nil, RelayContext{}, repoInstructions, &final)
	if err != nil {
		return final, err
	}
	final.Verdict = review.Verdict
	final.Summary = review.Summary
	final.ExitCode = ExitSuccess
	final.RoundsCompleted = 1
	final.LatestReviewPath = outputPath
	final.Review = review
	if final.Findings, err = updateFindingsLedger(runDir, 1, review, nil); err != nil {
		return final, &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("update findings ledger: %w", err)}
	}
	final.Costs = map[string]float64{reviewerLabel: cost}
	final.FinishedAt = time.Now().UTC()
	if opts.FailOnReview || final.Findings.OpenBlockerOrMajor > 0 || gateResult.Blocking {
		final.ExitCode = a.computeExitCode(final)
	}
	if final.ExitCode == ExitSuccess && review.OnlyMinorOrNit() && len(review.Findings) > 0 {
		final.DegradedReason = "review_passed_with_nonblocking_findings"
		final.Degraded = true
	}
	setRoleStatus(&final, reviewerLabel, opts.Adversary, "completed", "", "")
	applyInvocationBudget(&final, budget)
	finalizeRunState(&final)
	state := runStateForFinal(final, opts.Mode, "review", "")
	state.CurrentRound = 1
	if err := a.persistTerminalRun(opts.Workdir, &final, state); err != nil {
		return final, err
	}
	// runCompleted is set only once every artifact this function is
	// responsible for persisting has actually been written -- a persistFinal
	// failure above must leave the active-run pointer marked failed (not
	// cleared), since latest.json/final.json may not reflect this run either.
	runCompleted = true
	if final.ExitCode != ExitSuccess {
		return final, &ExitError{Code: final.ExitCode, Err: fmt.Errorf("review found blocking issues")}
	}
	return final, nil
}

func (a *App) validateReviewTargets(opts RunOptions) error {
	editorLabel, reviewerLabel := roleLabels(opts.Mode)
	registry := Registry(a.Config, opts)
	if opts.CoderExplicit && opts.Coder.Adapter != "" {
		if _, ok := registry[opts.Coder.Adapter]; !ok {
			return &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("unknown %s adapter %q", editorLabel, opts.Coder.Adapter)}
		}
	}
	if opts.Mode == ModeRelay && opts.ScoutExplicit && opts.Scout.Adapter != "" {
		if _, ok := registry[opts.Scout.Adapter]; !ok {
			return &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("unknown scout adapter %q", opts.Scout.Adapter)}
		}
	}
	if opts.Adversary.Adapter != "" {
		if _, ok := registry[opts.Adversary.Adapter]; !ok {
			return &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("unknown %s adapter %q", reviewerLabel, opts.Adversary.Adapter)}
		}
	}
	return nil
}

func (a *App) Fix(ctx context.Context, opts RunOptions) (FinalRun, error) {
	normalizeDefaultMode(&opts)
	latest, err := readLatest(opts.Workdir)
	if err != nil {
		return FinalRun{}, &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	final, err := readFinal(latest.FinalPath)
	if err != nil {
		return FinalRun{}, err
	}
	if final.Review == nil {
		return FinalRun{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("latest run has no saved review")}
	}
	prompt, err := readRunPrompt(final.RunDir, "")
	if err != nil {
		return FinalRun{}, &ExitError{Code: ExitAdapterFailure, Err: err}
	}
	opts.Prompt = prompt
	opts.Baseline = final.Baseline
	opts.SkipDirtyCheck = true

	// Resume with the saved run's mode and role targets unless the caller
	// explicitly requested different ones for this fix invocation (including
	// via --profile). Without this, a run started with e.g. --mode
	// adversarial would be resumed under whatever mode/adapters are
	// currently the default (which may differ, since the default mode and
	// role targets can change between invocations), handing the saved
	// review to the wrong prompts/adapters.
	//
	// final.json files saved before supervisor/worker mode was added have
	// no Mode/Coder/Adversary fields at all; they always ran the
	// coder/adversary loop, so fall back to adversarial mode and reconstruct
	// the coder/adversary targets from the legacy Adapters/Models maps
	// (which have always been populated with "coder"/"adversary" keys).
	legacyFinal := final.Mode == "" && final.Coder.Adapter == "" && final.Adversary.Adapter == "" && final.Scout.Adapter == ""
	savedMode := final.Mode
	if savedMode == "" && legacyFinal {
		savedMode = ModeAdversarial
	}
	if !opts.ModeExplicit {
		switch {
		case final.Mode != "":
			opts.Mode = final.Mode
		case legacyFinal:
			opts.Mode = ModeAdversarial
		}
	}
	if err := validateExplicitTargetModes(opts); err != nil {
		return FinalRun{}, err
	}
	canRestoreSavedTargets := !opts.ModeExplicit || savedMode == opts.Mode
	if canRestoreSavedTargets && !opts.CoderExplicit {
		switch {
		case final.Coder.Adapter != "":
			opts.Coder = final.Coder
		case legacyFinal && final.Adapters["coder"] != "":
			opts.Coder = RoleTarget{Adapter: final.Adapters["coder"], Model: final.Models["coder"]}
		}
	}
	if canRestoreSavedTargets && !opts.AdversaryExplicit {
		switch {
		case final.Adversary.Adapter != "":
			opts.Adversary = final.Adversary
		case legacyFinal && final.Adapters["adversary"] != "":
			opts.Adversary = RoleTarget{Adapter: final.Adapters["adversary"], Model: final.Models["adversary"]}
		}
	}
	if canRestoreSavedTargets && opts.Mode == ModeRelay && !opts.ScoutExplicit {
		switch {
		case final.Scout.Adapter != "":
			opts.Scout = final.Scout
		case final.Adapters["scout"] != "":
			opts.Scout = RoleTarget{Adapter: final.Adapters["scout"], Model: final.Models["scout"]}
		}
	}
	if !opts.SupervisorCanEditExplicit {
		opts.SupervisorCanEdit = final.SupervisorCanEdit
	}

	if err := validateRunRoles(opts); err != nil {
		return FinalRun{}, err
	}
	return a.runLoop(ctx, opts, final.Review)
}

func normalizeDefaultMode(opts *RunOptions) {
	if opts.Mode == "" {
		opts.Mode = ModeSupervisor
	}
	if opts.Mode == ModeRelay {
		if opts.ScoutMode == "" {
			opts.ScoutMode = "recon"
		}
		if opts.PostScoutMode == "" {
			opts.PostScoutMode = "polish"
		}
		if opts.ScoutFailurePolicy == "" {
			opts.ScoutFailurePolicy = "continue"
		}
		if opts.LossPolicy.Scout == "" {
			if opts.ScoutFailurePolicy == "fail" {
				opts.LossPolicy.Scout = LossPolicyBlock
			} else {
				opts.LossPolicy.Scout = LossPolicyDegrade
			}
		}
		if opts.ScoutContextPolicy == "" {
			opts.ScoutContextPolicy = "warn"
		}
	}
	if opts.LossPolicy.Reviewer == "" {
		opts.LossPolicy.Reviewer = LossPolicyBlock
	}
	if opts.LossPolicy.Supervisor == "" {
		opts.LossPolicy.Supervisor = LossPolicyBlock
	}
}

func validateExplicitTargetModes(opts RunOptions) error {
	if opts.CoderExplicitMode != "" && opts.CoderExplicitMode != opts.Mode {
		return &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("worker/coder target was selected for %s mode but latest run resumes as %s; pass --mode %s or use -mc for mode-neutral override", opts.CoderExplicitMode, opts.Mode, opts.CoderExplicitMode)}
	}
	if opts.AdversaryExplicitMode != "" && opts.AdversaryExplicitMode != opts.Mode {
		return &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("reviewer/supervisor target was selected for %s mode but latest run resumes as %s; pass --mode %s or use -ma for mode-neutral override", opts.AdversaryExplicitMode, opts.Mode, opts.AdversaryExplicitMode)}
	}
	if opts.ScoutExplicitMode != "" && opts.ScoutExplicitMode != opts.Mode {
		return &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("scout target was selected for %s mode but latest run resumes as %s; pass --mode %s or omit --scout", opts.ScoutExplicitMode, opts.Mode, opts.ScoutExplicitMode)}
	}
	return nil
}

// roleLabels returns the display names for the editor and reviewer roles
// used in progress output, run metadata, and transcript filenames.
func roleLabels(mode Mode) (editor string, reviewer string) {
	if mode == ModeSolo {
		return "solo", ""
	}
	if mode == ModeSupervisor {
		return "worker", "supervisor"
	}
	if mode == ModeRelay {
		return "coder", "supervisor"
	}
	return "coder", "adversary"
}

// editorSystemPromptForMode returns the editor-role system prompt for the
// active mode: the supervisor-worker-flavored prompt in ModeSupervisor, and
// the original adversarial-flavored prompt in ModeAdversarial.
func editorSystemPromptForMode(mode Mode) string {
	if mode == ModeSolo {
		return soloSystemPrompt
	}
	if mode == ModeSupervisor || mode == ModeRelay {
		return workerSystemPrompt
	}
	return coderSystemPrompt
}

// supervisorBriefRole picks the adapter role used to build the supervisor's
// implementation brief. Supervisors are read-only by default; --supervisor-can-edit
// grants them the same edit permissions as the editor role.
func supervisorBriefRole(canEdit bool) Role {
	if canEdit {
		return RoleCoder
	}
	return RoleSupervisor
}

func validateRunRoles(opts RunOptions) error {
	if err := validateRoleTarget(RoleCoder, opts.Coder); err != nil {
		return err
	}
	if opts.Mode == ModeSolo {
		return nil
	}
	if err := validateRoleTarget(RoleAdversary, opts.Adversary); err != nil {
		return err
	}
	if opts.Mode == ModeRelay {
		if err := validateScoutMode("scout-mode", opts.ScoutMode); err != nil {
			return err
		}
		if err := validateScoutMode("post-scout-mode", opts.PostScoutMode); err != nil {
			return err
		}
		return validateRoleTarget(RoleScout, opts.Scout)
	}
	return nil
}

func validateReviewRoles(opts RunOptions) error {
	return validateRoleTarget(RoleAdversary, opts.Adversary)
}

func validateClaudeRoleAssignments(opts RunOptions) error {
	editorLabel, _ := roleLabels(opts.Mode)
	if opts.Coder.Adapter == "claude" {
		return unsupportedClaudeRoleError(editorLabel)
	}
	if opts.Mode != ModeSolo && opts.Adversary.Adapter == "claude" {
		if opts.Mode != ModeAdversarial && opts.SupervisorCanEdit {
			return &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("claude supervisors are read-only; --supervisor-can-edit is not supported")}
		}
	}
	if opts.Mode == ModeRelay && opts.Scout.Adapter == "claude" {
		return unsupportedClaudeRoleError("scout")
	}
	return nil
}

func unsupportedClaudeRoleError(role string) error {
	return &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("claude is supported only as a read-only supervisor or adversary; %s role is not supported", role)}
}

func validateRoleTarget(role Role, target RoleTarget) error {
	if role != RoleAdversary && role != RoleScout && (target.Adapter == "openai-compatible" || target.Adapter == "oai") {
		return &ExitError{Code: ExitInvalidArguments, Err: unsupportedOpenAICompatibleRoleError()}
	}
	if role == RoleAdversary && target.Adapter == "gosling" {
		return &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("gosling is not supported as an adversary adapter")}
	}
	if role == RoleScout && target.Adapter == "gosling" {
		return &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("gosling is not supported as a scout adapter")}
	}
	return nil
}

func (a *App) Doctor(ctx context.Context, opts RunOptions) (map[string]VersionInfo, error) {
	registry := Registry(a.Config, opts)
	status := map[string]VersionInfo{}
	for _, key := range []string{"codex", "codex-oss", "claude", "agy", "gosling", "grok", "openai-compatible"} {
		info, err := registry[key].Detect(ctx)
		if err != nil {
			return nil, err
		}
		status[key] = info
	}
	status["oai"] = status["openai-compatible"]
	if err := validateRunRoles(opts); err != nil {
		return status, err
	}
	targets := []RoleTarget{opts.Coder}
	if opts.Mode != ModeSolo {
		targets = append(targets, opts.Adversary)
	}
	if opts.Mode == ModeRelay {
		targets = append(targets, opts.Scout)
	}
	for _, target := range targets {
		if target.Adapter == "" {
			continue
		}
		info, ok := status[target.Adapter]
		if !ok || !info.Found || !info.Runnable {
			hint := ""
			if ok {
				hint = info.Hint
			}
			return status, &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("%s not runnable; try %s", target.Adapter, hint)}
		}
	}
	return status, nil
}
