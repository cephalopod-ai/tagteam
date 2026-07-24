package tagteam

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func sanitizeArtifactName(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	var b strings.Builder
	for _, r := range raw {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('-')
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "call"
	}
	return out
}

func (a *App) runAdapter(ctx context.Context, adapter Adapter, role Role, req Request, dryRun bool) (result Result, runErr error) {
	if err := bindControlResumeRequest(ctx, &req); err != nil {
		return Result{}, &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	if controlResumeBeforeAdapterHook != nil && req.controlResumeGate != nil {
		controlResumeBeforeAdapterHook()
		if err := bindControlResumeRequest(ctx, &req); err != nil {
			return Result{}, &ExitError{Code: ExitPreflightFailed, Err: err}
		}
	}
	if err := req.Budget.Before(string(role), req.Phase); err != nil {
		return Result{}, &ExitError{Code: ExitAdapterFailure, Err: err}
	}
	if err := validateRequestArtifactPaths(req); err != nil {
		return Result{}, &ExitError{Code: ExitInvalidArguments, Err: err}
	}
	var before worktreeSnapshot
	var hostBefore integritySnapshot
	if !dryRun && req.Workdir != "" {
		var snapshotErr error
		before, snapshotErr = captureWorktreeSnapshot(ctx, req.Workdir)
		if snapshotErr != nil {
			return Result{}, &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("capture pre-invocation Git state: %w", snapshotErr)}
		}
		hostBefore, snapshotErr = captureIntegritySnapshot(req)
		if snapshotErr != nil {
			return Result{}, &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("capture protected artifact state: %w", snapshotErr)}
		}
		defer func() {
			if integrityErr := validateInvocationIntegrity(context.Background(), req, role, before, hostBefore); integrityErr != nil {
				result = Result{}
				runErr = &ExitError{Code: ExitAdapterFailure, Err: integrityErr}
			}
		}()
	}
	calibration, effectiveTimeout := calibrateTimeout(ctx, adapter, role, req)
	req.Timeout = effectiveTimeout
	if calibration.Warning != "" {
		logRequestProgress(req, "timeout calibration warning: %s", calibration.Warning)
	}
	if direct, ok := adapter.(DirectAdapter); ok {
		spec, err := adapter.BuildCmd(role, req)
		if err != nil {
			return Result{}, &ExitError{Code: ExitInvalidArguments, Err: err}
		}
		record, recordPath, err := startDeliveryRecord(adapter, role, &req, dryRun, spec)
		if err != nil {
			return Result{}, err
		}
		if err := recordInvocationState(&req, record); err != nil {
			return Result{}, &ExitError{Code: ExitAdapterFailure, Err: err}
		}
		if !dryRun && req.Workdir != "" {
			refreshed, snapshotErr := captureIntegritySnapshot(req)
			if snapshotErr != nil {
				return Result{}, &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("refresh protected artifact state: %w", snapshotErr)}
			}
			hostBefore = refreshed
		}
		if dryRun {
			payload, _ := json.MarshalIndent(spec, "", "  ")
			result := Result{Text: redactSecretsWithOverlay(string(payload), req.EnvOverlay), Command: spec.Argv}
			if role == RoleAdversary {
				result.Review = &Review{
					SchemaVersion:   ArtifactSchemaVersion,
					Verdict:         "pass",
					Summary:         "dry-run",
					Findings:        []Finding{},
					TestSuggestions: []string{},
				}
			}
			if role == RoleScout {
				result.Scout = &Scout{
					SchemaVersion:     ArtifactSchemaVersion,
					Mode:              "recon",
					Summary:           "dry-run",
					RelevantFiles:     []string{},
					LikelyEntryPoints: []string{},
					ExistingPatterns:  []string{},
					Risks:             []string{},
					SuggestedTests:    []string{},
					Items:             []ScoutItem{},
					DoNotBlock:        true,
				}
			}
			finishDeliveryRecord(req, recordPath, record, "dry-run", nil)
			return result, nil
		}
		req.InvocationID = record.InvocationID
		phase := req.Phase
		if phase == "" {
			phase = fmt.Sprintf("%s %s", role, adapter.ID())
		}
		progressRole := role
		if req.ProgressRole != "" {
			progressRole = req.ProgressRole
		}
		started := time.Now()
		lastActivity := started
		req.ProgressLastActivity = &lastActivity
		directCtx := req.Context
		if directCtx == nil {
			directCtx = ctx
		}
		scopeCtx, scopeCancel := context.WithCancel(directCtx)
		defer scopeCancel()
		directCtx = scopeCtx
		directTimeout := req.Timeout
		directCancel := func() {}
		if directTimeout > 0 {
			directCtx, directCancel = context.WithTimeout(directCtx, directTimeout)
		}
		defer directCancel()
		req.Context = directCtx
		// The host-owned context above is authoritative for both invocation and
		// watchdog deadlines; avoid adding a second nested timer in the adapter.
		req.Timeout = 0
		directProgressStatus := "completed"
		defer func() {
			status := directProgressStatus
			if runErr != nil {
				status = "failed"
			}
			_, _ = writeLiveProgress(context.Background(), req, progressRole, phase, started, status)
		}()
		stopProgress := startSoftProgressMonitor(directCtx, req, progressRole, phase, started, nil)
		defer stopProgress()
		stopScopeGuard := startLiveScopeGuard(directCtx, req, before, scopeCancel)
		defer stopScopeGuard()
		if err := rebindRequestControlResume(&req); err != nil {
			return Result{}, &ExitError{Code: ExitPreflightFailed, Err: err}
		}
		result, err := direct.RunDirect(role, req)
		if scopeErr := stopScopeGuard(); scopeErr != nil {
			err = scopeErr
		}
		if err != nil {
			var validationErr *directValidationError
			if errors.As(err, &validationErr) {
				safeRaw := sanitizeAdapterArtifact(adapter.ID(), validationErr.raw)
				if artifactErr := writeRecoveryOutput(req, safeRaw); artifactErr != nil {
					finishDeliveryRecord(req, recordPath, record, "failed", artifactErr)
					return Result{}, artifactErr
				}
				rawPath, artifactErr := writeOutputContractArtifacts(req, role, Result{}, safeRaw)
				if artifactErr != nil {
					finishDeliveryRecord(req, recordPath, record, "failed", artifactErr)
					return Result{}, artifactErr
				}
				record.RawOutputPath = rawPath
				validationPath, artifactErr := writeValidationErrorArtifact(req, validationErr)
				if artifactErr != nil {
					finishDeliveryRecord(req, recordPath, record, "failed", artifactErr)
					return Result{}, artifactErr
				}
				record.ValidationErrorPath = validationPath
				err = conciseAdapterResultError(adapter.ID(), err, validationPath, req.EnvOverlay)
			}
			if directCtx.Err() != nil {
				record.CancellationCause = directCtx.Err().Error()
			}
			if record.StderrPath != "" {
				if err := guardControlResumeWritePath(req.controlResumeGate, record.StderrPath); err != nil {
					finishDeliveryRecord(req, recordPath, record, "failed", err)
					return Result{}, &ExitError{Code: ExitPreflightFailed, Err: err}
				}
				_ = writeRedactedBytes(record.StderrPath, []byte(err.Error()+"\n"), req.EnvOverlay)
				record.StderrBytes = int64(len(err.Error()) + 1)
			}
			finishDeliveryRecord(req, recordPath, record, "failed", err)
			return result, err
		}
		raw := result.Raw
		if len(raw) == 0 {
			raw = []byte(result.Text)
		}
		artifactRaw := sanitizeAdapterArtifact(adapter.ID(), raw)
		if record.StdoutPath != "" {
			if err := guardControlResumeWritePath(req.controlResumeGate, record.StdoutPath); err != nil {
				finishDeliveryRecord(req, recordPath, record, "failed", err)
				return Result{}, &ExitError{Code: ExitPreflightFailed, Err: err}
			}
			_ = writeRedactedBytes(record.StdoutPath, artifactRaw, req.EnvOverlay)
			record.StdoutBytes = int64(len(artifactRaw))
		}
		if int64(len(raw)) > maxOutputBytes(req) {
			err := &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("%s output exceeded max_output_bytes=%d", adapter.ID(), maxOutputBytes(req))}
			finishDeliveryRecord(req, recordPath, record, "failed", err)
			return Result{}, err
		}
		if err := validateWorkerResultForRequest(ctx, req, &result, before); err != nil {
			validationPath, artifactErr := writeValidationErrorArtifact(req, err)
			if artifactErr != nil {
				finishDeliveryRecord(req, recordPath, record, "failed", artifactErr)
				return Result{}, artifactErr
			}
			record.ValidationErrorPath = validationPath
			_, _ = writeOutputContractArtifacts(req, role, result, artifactRaw)
			resultErr := conciseAdapterResultError(adapter.ID(), err, validationPath, req.EnvOverlay)
			finishDeliveryRecord(req, recordPath, record, "failed", resultErr)
			return result, resultErr
		}
		if role == RoleAdversary && result.Review != nil {
			if err := result.Review.ValidateCurrent(); err != nil {
				contractErr := &OutputContractError{Err: err}
				validationPath, artifactErr := writeValidationErrorArtifact(req, contractErr)
				if artifactErr != nil {
					finishDeliveryRecord(req, recordPath, record, "failed", artifactErr)
					return Result{}, artifactErr
				}
				record.ValidationErrorPath = validationPath
				resultErr := conciseAdapterResultError(adapter.ID(), contractErr, validationPath, req.EnvOverlay)
				finishDeliveryRecord(req, recordPath, record, "failed", resultErr)
				return result, resultErr
			}
		}
		normalizeReview(result.Review)
		normalizeScout(result.Scout)
		if err := writeNormalizedOutput(req, role, result); err != nil {
			finishDeliveryRecord(req, recordPath, record, "failed", err)
			return Result{}, err
		}
		rawPath, artifactErr := writeOutputContractArtifacts(req, role, result, artifactRaw)
		if artifactErr != nil {
			finishDeliveryRecord(req, recordPath, record, "failed", artifactErr)
			return Result{}, artifactErr
		}
		record.RawOutputPath = rawPath
		if req.OutputPath != "" && (role == RoleCoder || role == RoleAdversary || role == RoleScout) {
			record.ParsedPath = req.OutputPath + ".parsed.json"
		}
		finishDeliveryRecord(req, recordPath, record, "completed", nil)
		return result, nil
	}
	spec, err := adapter.BuildCmd(role, req)
	if err != nil {
		return Result{}, &ExitError{Code: ExitInvalidArguments, Err: err}
	}
	record, recordPath, err := startDeliveryRecord(adapter, role, &req, dryRun, spec)
	if err != nil {
		return Result{}, err
	}
	if err := recordInvocationState(&req, record); err != nil {
		return Result{}, &ExitError{Code: ExitAdapterFailure, Err: err}
	}
	if !dryRun && req.Workdir != "" {
		refreshed, snapshotErr := captureIntegritySnapshot(req)
		if snapshotErr != nil {
			return Result{}, &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("refresh protected artifact state: %w", snapshotErr)}
		}
		hostBefore = refreshed
	}
	if dryRun {
		payload, _ := json.MarshalIndent(spec, "", "  ")
		result := Result{Text: redactSecretsWithOverlay(string(payload), req.EnvOverlay), Command: spec.Argv}
		if role == RoleAdversary {
			result.Review = &Review{
				SchemaVersion:   ArtifactSchemaVersion,
				Verdict:         "pass",
				Summary:         "dry-run",
				Findings:        []Finding{},
				TestSuggestions: []string{},
			}
		}
		if role == RoleScout {
			result.Scout = &Scout{
				SchemaVersion:     ArtifactSchemaVersion,
				Mode:              "recon",
				Summary:           "dry-run",
				RelevantFiles:     []string{},
				LikelyEntryPoints: []string{},
				ExistingPatterns:  []string{},
				Risks:             []string{},
				SuggestedTests:    []string{},
				Items:             []ScoutItem{},
				DoNotBlock:        true,
			}
		}
		finishDeliveryRecord(req, recordPath, record, "dry-run", nil)
		return result, nil
	}
	req.InvocationID = record.InvocationID
	phase := req.Phase
	if phase == "" {
		phase = fmt.Sprintf("%s %s", role, adapter.ID())
	}
	runCtx := req.Context
	if runCtx == nil {
		runCtx = ctx
	}
	scopeCtx, scopeCancel := context.WithCancel(runCtx)
	defer scopeCancel()
	runCtx = scopeCtx
	progressRole := role
	if req.ProgressRole != "" {
		progressRole = req.ProgressRole
	}
	if adapter.Capabilities().SerializeInvocations {
		// Serialize before starting the invocation timer so lock wait does
		// not consume the subprocess budget. Lock failures are fail-closed
		// adapter failures, so role fallback policies can engage. A
		// contended wait publishes live progress as "waiting" so the TUI
		// and status views show the run is queued, not hung.
		waitStarted := time.Now()
		release, lockErr := acquireInvocationSlot(runCtx, adapter.ID(), req, req.Timeout, func(holderPID int) {
			_ = writeWaitingProgress(req, progressRole, phase, waitStarted, adapter.ID(), holderPID)
		})
		if lockErr != nil {
			_, _ = writeLiveProgress(context.Background(), req, progressRole, phase, waitStarted, "failed")
			finishDeliveryRecord(req, recordPath, record, "failed", lockErr)
			return Result{}, lockErr
		}
		defer release()
	}
	cancel := func() {}
	if req.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(runCtx, req.Timeout)
	}
	defer cancel()
	cmd := exec.CommandContext(runCtx, spec.Argv[0], spec.Argv[1:]...)
	prepareProcessTree(cmd)
	cmd.Dir = spec.Dir
	cmd.Env, err = commandEnvForInvocation(role, req, spec.Env)
	if err != nil {
		finishDeliveryRecord(req, recordPath, record, "failed", err)
		return Result{}, &ExitError{Code: ExitAdapterFailure, Err: err}
	}
	if len(spec.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(spec.Stdin)
	}
	if err := rebindRequestControlResume(&req); err != nil {
		return Result{}, &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	stdout, err := newInvocationStream(record.StdoutPath, maxOutputBytes(req), req.EnvOverlay)
	if err != nil {
		finishDeliveryRecord(req, recordPath, record, "failed", err)
		return Result{}, err
	}
	stderr, err := newInvocationStream(record.StderrPath, maxOutputBytes(req), req.EnvOverlay)
	if err != nil {
		_ = stdout.Close()
		finishDeliveryRecord(req, recordPath, record, "failed", err)
		return Result{}, err
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	started := time.Now()
	lastActivity := started
	req.ProgressStdout = stdout
	req.ProgressStderr = stderr
	req.ProgressLastActivity = &lastActivity
	logRequestProgress(req, "%s process starting output=%s", phase, spec.Output)
	stopProgress := startSoftProgressMonitor(runCtx, req, progressRole, phase, started, func() {
		_ = stdout.Sync()
		_ = stderr.Sync()
	})
	defer stopProgress()
	stopScopeGuard := startLiveScopeGuard(runCtx, req, before, scopeCancel)
	defer stopScopeGuard()
	if err := rebindRequestControlResume(&req); err != nil {
		return Result{}, &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	if err := cmd.Run(); err != nil {
		stopProgress()
		finishInvocationStreams(&record, stdout, stderr)
		if scopeErr := stopScopeGuard(); scopeErr != nil {
			finishDeliveryRecord(req, recordPath, record, "failed", scopeErr)
			return Result{}, scopeErr
		}
		if stdout.Exceeded() || stderr.Exceeded() {
			limitErr := &ExitError{Code: ExitAdapterFailure, Err: outputLimitError(adapter.ID(), maxOutputBytes(req))}
			finishDeliveryRecord(req, recordPath, record, "failed", limitErr)
			return Result{}, limitErr
		}
		_, _ = writeLiveProgress(context.Background(), req, progressRole, phase, started, "failed")
		var reportedEnvelopeErr error
		if adapter.ID() == "claude" && len(stdout.Bytes()) > 0 {
			if _, parseErr := adapter.ParseResult(role, stdout.Bytes()); parseErr != nil && strings.Contains(parseErr.Error(), "claude reported ") {
				reportedEnvelopeErr = parseErr
			}
		}
		msg := redactSecretsWithOverlay(strings.TrimSpace(stderr.String()), req.EnvOverlay)
		if reportedEnvelopeErr != nil {
			msg = redactSecretsWithOverlay(reportedEnvelopeErr.Error(), req.EnvOverlay)
		}
		if msg == "" {
			msg = redactSecretsWithOverlay(err.Error(), req.EnvOverlay)
		}
		msg = conciseAdapterError(msg, record.StderrPath)
		logRequestProgress(req, "%s failed elapsed=%s", phase, shortDuration(time.Since(started)))
		if exitErr, ok := err.(*exec.ExitError); ok {
			record.ProcessExitCode = exitErr.ExitCode()
		}
		if runCtx.Err() != nil {
			record.CancellationCause = runCtx.Err().Error()
			if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
				recordTimeoutObservation(req.RunDir, calibration, time.Since(started))
			}
		}
		runErr := &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("%s failed: %s", adapter.ID(), msg)}
		finishDeliveryRecord(req, recordPath, record, "failed", runErr)
		return Result{}, runErr
	}
	stopProgress()
	finishInvocationStreams(&record, stdout, stderr)
	if scopeErr := stopScopeGuard(); scopeErr != nil {
		finishDeliveryRecord(req, recordPath, record, "failed", scopeErr)
		return Result{}, scopeErr
	}
	_, _ = writeLiveProgress(context.Background(), req, progressRole, phase, started, "completed")
	logRequestProgress(req, "%s process completed elapsed=%s", phase, shortDuration(time.Since(started)))
	if stdout.Exceeded() || stderr.Exceeded() {
		limitErr := &ExitError{Code: ExitAdapterFailure, Err: outputLimitError(adapter.ID(), maxOutputBytes(req))}
		finishDeliveryRecord(req, recordPath, record, "failed", limitErr)
		return Result{}, limitErr
	}
	raw := stdout.Bytes()
	if req.OutputPath != "" && fileExists(req.OutputPath) {
		if info, statErr := os.Stat(req.OutputPath); statErr == nil && info.Size() > maxOutputBytes(req) {
			err := &ExitError{Code: ExitAdapterFailure, Err: outputLimitError(adapter.ID(), maxOutputBytes(req))}
			finishDeliveryRecord(req, recordPath, record, "failed", err)
			return Result{}, err
		}
		var readErr error
		raw, readErr = os.ReadFile(req.OutputPath)
		if readErr != nil {
			return Result{}, readErr
		}
	}
	if len(raw) == 0 {
		raw = stdout.Bytes()
	}
	if int64(len(raw)) > maxOutputBytes(req) {
		err := &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("%s output exceeded max_output_bytes=%d", adapter.ID(), maxOutputBytes(req))}
		finishDeliveryRecord(req, recordPath, record, "failed", err)
		return Result{}, err
	}
	artifactRaw := sanitizeAdapterArtifact(adapter.ID(), raw)
	if record.StdoutPath != "" {
		if err := guardControlResumeWritePath(req.controlResumeGate, record.StdoutPath); err != nil {
			finishDeliveryRecord(req, recordPath, record, "failed", err)
			return Result{}, &ExitError{Code: ExitPreflightFailed, Err: err}
		}
		_ = writeRedactedBytes(record.StdoutPath, artifactRaw, req.EnvOverlay)
	}
	result, err = adapter.ParseResult(role, raw)
	if err != nil {
		if artifactErr := writeRecoveryOutput(req, artifactRaw); artifactErr != nil {
			finishDeliveryRecord(req, recordPath, record, "failed", artifactErr)
			return Result{}, artifactErr
		}
		validationPath, artifactErr := writeValidationErrorArtifact(req, err)
		if artifactErr != nil {
			finishDeliveryRecord(req, recordPath, record, "failed", artifactErr)
			return Result{}, artifactErr
		}
		record.ValidationErrorPath = validationPath
		resultErr := conciseAdapterResultError(adapter.ID(), err, validationPath, req.EnvOverlay)
		finishDeliveryRecord(req, recordPath, record, "failed", resultErr)
		return Result{Raw: raw, Text: strings.TrimSpace(string(raw))}, resultErr
	}
	if err := validateWorkerResultForRequest(ctx, req, &result, before); err != nil {
		validationPath, artifactErr := writeValidationErrorArtifact(req, err)
		if artifactErr != nil {
			finishDeliveryRecord(req, recordPath, record, "failed", artifactErr)
			return Result{}, artifactErr
		}
		record.ValidationErrorPath = validationPath
		_, _ = writeOutputContractArtifacts(req, role, result, artifactRaw)
		resultErr := conciseAdapterResultError(adapter.ID(), err, validationPath, req.EnvOverlay)
		finishDeliveryRecord(req, recordPath, record, "failed", resultErr)
		return result, resultErr
	}
	if role == RoleAdversary && result.Review != nil {
		if err := result.Review.ValidateCurrent(); err != nil {
			contractErr := &OutputContractError{Err: err}
			validationPath, artifactErr := writeValidationErrorArtifact(req, contractErr)
			if artifactErr != nil {
				finishDeliveryRecord(req, recordPath, record, "failed", artifactErr)
				return Result{}, artifactErr
			}
			record.ValidationErrorPath = validationPath
			resultErr := conciseAdapterResultError(adapter.ID(), contractErr, validationPath, req.EnvOverlay)
			finishDeliveryRecord(req, recordPath, record, "failed", resultErr)
			return result, resultErr
		}
	}
	normalizeReview(result.Review)
	normalizeScout(result.Scout)
	if err := writeNormalizedOutput(req, role, result); err != nil {
		finishDeliveryRecord(req, recordPath, record, "failed", err)
		return Result{}, err
	}
	rawPath, artifactErr := writeOutputContractArtifacts(req, role, result, artifactRaw)
	if artifactErr != nil {
		finishDeliveryRecord(req, recordPath, record, "failed", artifactErr)
		return Result{}, artifactErr
	}
	record.RawOutputPath = rawPath
	if req.OutputPath != "" && (role == RoleCoder || role == RoleAdversary || role == RoleScout) {
		record.ParsedPath = req.OutputPath + ".parsed.json"
	}
	result.Command = spec.Argv
	finishDeliveryRecord(req, recordPath, record, "completed", nil)
	return result, nil
}

func conciseAdapterError(message, artifactPath string) string {
	message = strings.TrimSpace(message)
	if len(message) <= 512 && !strings.Contains(message, "\n") {
		return message
	}
	firstLine := firstAdapterDiagnosticLine(message, "adapter process failed")
	summary := firstLine
	if artifactPath != "" {
		summary += "\nfull stderr: " + artifactPath
	}
	return summary
}

func outputArtifactFingerprint(path string) string {
	if path == "" {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil {
		return "missing"
	}
	return fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
}

func (a *App) computeExitCode(final FinalRun) int {
	if final.Findings.OpenBlockerOrMajor > 0 {
		return ExitBlockingFindings
	}
	if len(final.QualityGates) > 0 && final.QualityGates[len(final.QualityGates)-1].Blocking {
		return ExitBlockingFindings
	}
	if final.Regression != nil {
		if final.Regression.Status == "new_failures" || final.Regression.Status == "unknown" {
			return ExitTestsFailed
		}
	} else if len(final.Tests) > 0 && !final.Tests[len(final.Tests)-1].Passed {
		return ExitTestsFailed
	}
	if final.Review != nil && final.Review.HasBlockingFindings() {
		return ExitBlockingFindings
	}
	return ExitSuccess
}

func (a *App) persistFinal(workdir string, final FinalRun) error {
	if final.SchemaVersion == 0 {
		final.SchemaVersion = ArtifactSchemaVersion
	}
	if final.Caps == (RunCaps{}) {
		final.Caps = RunCaps{}
	}
	normalizeReview(final.Review)
	finalPath := filepath.Join(final.RunDir, "final.json")
	if err := writeJSON(finalPath, final); err != nil {
		return err
	}
	latest := LatestRun{
		RunID:     final.RunID,
		RunDir:    final.RunDir,
		FinalPath: finalPath,
		Verdict:   final.Verdict,
		ExitCode:  final.ExitCode,
		UpdatedAt: time.Now().UTC(),
	}
	return writeJSON(statePathForWorkdir(workdir, "latest.json"), latest)
}

type preflightCleanup func(runDir string) error

func preflight(opts RunOptions, runID string) (string, preflightCleanup, error) {
	baseline := opts.Baseline
	if baseline == "" {
		var err error
		baseline, err = ensureGitRepo(opts.Workdir)
		if err != nil {
			return "", nil, err
		}
	}
	// A dry run may inspect a dirty worktree, but it must not create a branch,
	// stage files, commit, or stash in order to do so.
	if opts.DryRun {
		return baseline, nil, nil
	}
	if err := ensureRepositoryRuntimeIgnored(opts.Workdir); err != nil {
		return "", nil, &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	if hasConfiguredTests(opts) && !opts.NoTest {
		if err := validateConfiguredTestCommands(opts); err != nil {
			return "", nil, err
		}
	}
	if opts.AllowDirty || opts.GitSafety == "allow-dirty" {
		checkpoint, err := gitCreateCheckpointBranch(opts.Workdir, "tagteam/"+runID, runID)
		if err != nil {
			return "", nil, err
		}
		return checkpoint, nil, nil
	}
	if opts.SkipDirtyCheck {
		return baseline, nil, nil
	}
	if opts.GitSafety == "integrate" {
		logProgress(opts, "committing selected worktree state, synchronizing its tracked branch, and creating isolated run branch")
		baseline, err := gitPrepareIntegratedRun(opts.Workdir, runID)
		if err != nil {
			return "", nil, err
		}
		return baseline, nil, nil
	}
	if opts.GitSafety == "sync" {
		logProgress(opts, "syncing current branch and creating isolated run branch")
		baseline, err := gitPrepareSyncedRun(opts.Workdir, runID)
		if err != nil {
			return "", nil, err
		}
		return baseline, nil, nil
	}
	if opts.GitSafety == "branch" {
		if err := gitCreateBranch(opts.Workdir, "tagteam/"+runID); err != nil {
			return "", nil, err
		}
		return baseline, nil, nil
	}
	dirty, err := gitDirty(opts.Workdir)
	if err != nil {
		return "", nil, err
	}
	if !dirty {
		return baseline, nil, nil
	}
	if opts.Autostash || opts.GitSafety == "autostash" {
		stashRef, err := gitAutostash(opts.Workdir, runID)
		if err != nil {
			return "", nil, err
		}
		return baseline, func(runDir string) error {
			return restoreAutostash(opts.Workdir, runDir, stashRef)
		}, nil
	}
	return "", nil, &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("worktree is dirty; use --allow-dirty or --autostash")}
}

func ensureGitRepo(workdir string) (string, error) {
	out, err := runCommand(context.Background(), workdir, "git", "rev-parse", "--verify", "HEAD")
	if err != nil {
		return "", &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("workdir is not a git repo or has no HEAD: %w", err)}
	}
	return strings.TrimSpace(out), nil
}

func checkAdapters(ctx context.Context, adapters ...Adapter) error {
	for _, adapter := range adapters {
		if adapter == nil {
			return &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("adapter is not configured")}
		}
		info, err := adapter.Detect(ctx)
		if err != nil {
			return &ExitError{Code: ExitPreflightFailed, Err: err}
		}
		if !info.Found || !info.Runnable {
			return &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("%s not runnable; try %s", adapter.ID(), info.Hint)}
		}
	}
	return nil
}

func selectRunnableRoleAdapter(ctx context.Context, registry map[string]Adapter, role string, primary RoleTarget, fallbackRaw []string, policy LossPolicy, final *FinalRun) (RoleTarget, Adapter, error) {
	if err := ValidateRoleTarget(roleForSelectionLabel(role), primary); err != nil {
		return primary, nil, err
	}
	adapter, ok := registry[primary.Adapter]
	if !ok {
		return primary, nil, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("unknown %s adapter %q", role, primary.Adapter)}
	}
	attempts := []string{roleTargetString(primary)}
	if err := checkAdapters(ctx, adapter); err == nil {
		setRoleStatus(final, role, primary, "ready", "", "")
		return primary, adapter, nil
	} else if !policyAttemptsReplacement(policy) {
		reason := classifyRoleFailure(role, err)
		setRoleStatus(final, role, primary, "failed", reason, err.Error())
		return primary, adapter, err
	} else {
		for _, raw := range fallbackRaw {
			target, parseErr := ParseRoleTarget(raw)
			if parseErr != nil {
				continue
			}
			if err := ValidateRoleTarget(roleForSelectionLabel(role), target); err != nil {
				return primary, nil, err
			}
			attempts = append(attempts, roleTargetString(target))
			candidate, ok := registry[target.Adapter]
			if !ok {
				continue
			}
			if err := checkAdapters(ctx, candidate); err != nil {
				continue
			}
			setFinalDegraded(final, ReasonFallbackUsed, fmt.Sprintf("%s fallback selected", role))
			appendRoleLoss(final, role, policy, "replace", "fallback_selected", ReasonFallbackUsed, fmt.Sprintf("%s -> %s", roleTargetString(primary), roleTargetString(target)))
			setRoleStatus(final, role, target, "ready", ReasonFallbackUsed, "fallback selected")
			status := final.RoleStatuses[role]
			status.Attempts = attempts
			status.Selected = roleTargetString(target)
			final.RoleStatuses[role] = status
			return target, candidate, nil
		}
		err := &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("%s not runnable and no fallback target was runnable", role)}
		reason := classifyRoleFailure(role, err)
		setRoleStatus(final, role, primary, "failed", reason, err.Error())
		status := final.RoleStatuses[role]
		status.Attempts = attempts
		final.RoleStatuses[role] = status
		return primary, adapter, err
	}
}

func roleForSelectionLabel(label string) Role {
	switch label {
	case "worker", "coder", "solo":
		return RoleCoder
	case "supervisor":
		return RoleSupervisor
	case "scout":
		return RoleScout
	default:
		return RoleAdversary
	}
}
