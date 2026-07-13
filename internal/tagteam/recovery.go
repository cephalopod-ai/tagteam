package tagteam

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

const RecoveryDecisionSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "additionalProperties": false,
  "required": ["schema_version", "decision", "reason", "evidence"],
  "properties": {
    "schema_version": {"type": "integer", "const": 1},
    "decision": {"type": "string", "enum": ["repair", "continue_with_fallback", "quarantine"]},
    "reason": {"type": "string", "minLength": 1},
    "evidence": {"type": "array", "items": {"type": "string"}}
  }
}`

type RecoveryDecision struct {
	SchemaVersion int      `json:"schema_version"`
	Decision      string   `json:"decision"`
	Reason        string   `json:"reason"`
	Evidence      []string `json:"evidence"`
}

type RecoveryArtifact struct {
	SchemaVersion int               `json:"schema_version"`
	Round         int               `json:"round"`
	Failure       string            `json:"failure"`
	PartialDiff   DiffFilesMetadata `json:"partial_diff"`
	DiffPath      string            `json:"diff_path"`
	Test          *TestRun          `json:"test,omitempty"`
	Decision      RecoveryDecision  `json:"decision"`
	Original      RoleTarget        `json:"original_worker"`
	Selected      RoleTarget        `json:"selected_worker,omitempty"`
	Status        string            `json:"status"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
}

func (d RecoveryDecision) Validate(allowed map[string]bool) error {
	if d.SchemaVersion != ArtifactSchemaVersion {
		return fmt.Errorf("unsupported recovery schema_version %d", d.SchemaVersion)
	}
	if !allowed[d.Decision] {
		return fmt.Errorf("recovery decision %q is not available", d.Decision)
	}
	if strings.TrimSpace(d.Reason) == "" || d.Evidence == nil {
		return fmt.Errorf("recovery decision requires reason and evidence")
	}
	return nil
}

func parseRecoveryDecision(raw []byte, allowed map[string]bool) (RecoveryDecision, error) {
	var decision RecoveryDecision
	err := decodeEmbeddedJSON(raw, func(candidate []byte) error {
		var parsed RecoveryDecision
		if err := json.Unmarshal(candidate, &parsed); err != nil {
			return fmt.Errorf("decode recovery decision: %w", err)
		}
		if err := parsed.Validate(allowed); err != nil {
			return err
		}
		decision = parsed
		return nil
	})
	if err != nil {
		return RecoveryDecision{}, &OutputContractError{Err: err}
	}
	return decision, nil
}

func recoveryPrompt(failure error, diff DiffArtifact, test *TestRun, allowed map[string]bool) string {
	actions := []string{"quarantine"}
	if allowed["repair"] {
		actions = append(actions, "repair")
	}
	if allowed["continue_with_fallback"] {
		actions = append(actions, "continue_with_fallback")
	}
	testSummary := "not configured"
	if test != nil {
		testSummary = fmt.Sprintf("passed=%t output=%s", test.Passed, safeTestOutput(test.Output))
	}
	return fmt.Sprintf(`You are the read-only recovery supervisor. An implementation adapter failed after changing the worktree.

Failure: %s
Diff path: %s
Changed files: %v
Diff hash: %s
Focused tests: %s
Allowed decisions: %s

Choose repair only when the same resumable worker should repair its partial work. Choose continue_with_fallback when another configured worker should preserve and continue the partial work. Choose quarantine when further automated edits are unsafe.

Return JSON only matching the supplied schema.`, failure, diff.PatchPath, diff.ChangedFiles(), diff.Metadata.DiffSHA256, testSummary, strings.Join(actions, ", "))
}

func (a *App) recoverEditorFailure(
	ctx context.Context,
	opts RunOptions,
	round int,
	runDir, baseline, workerSchemaPath, sessionID string,
	originalRequest Request,
	originalTarget RoleTarget,
	originalAdapter, reviewer Adapter,
	registry map[string]Adapter,
	failure error,
	before worktreeSnapshot,
	final *FinalRun,
) (Result, RoleTarget, Adapter, error) {
	gate := controlResumeGateFrom(ctx)
	var err error
	if runDir, err = rebindControlResumeRunDir(gate, runDir, final); err != nil {
		return Result{}, originalTarget, originalAdapter, &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	after, snapshotErr := captureWorktreeSnapshot(context.Background(), opts.Workdir)
	if snapshotErr != nil {
		return Result{}, originalTarget, originalAdapter, failure
	}
	if len(worktreeDelta(before, after)) == 0 {
		return a.retryZeroDeltaEditorWithFallback(ctx, opts, round, runDir, baseline, workerSchemaPath, originalRequest, originalTarget, originalAdapter, registry, failure, after, final)
	}
	if err := guardControlResumeWritePath(gate, filepath.Join(runDir, "state.json")); err != nil {
		return Result{}, originalTarget, originalAdapter, &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	_ = writeRunState(runDir, RunState{RunID: final.RunID, Mode: opts.Mode, Status: "running", Phase: string(PhaseRepairing), CurrentRound: round, RecoveryStatus: "checkpointing", RoleStatuses: final.RoleStatuses})
	diff, err := captureDiffArtifact(ctx, opts.Workdir, baseline, runDir, round)
	if err != nil {
		return Result{}, originalTarget, originalAdapter, failure
	}
	if runDir, err = rebindControlResumeRunDir(gate, runDir, final); err != nil {
		return Result{}, originalTarget, originalAdapter, &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	artifact := RecoveryArtifact{
		SchemaVersion: ArtifactSchemaVersion,
		Round:         round,
		Failure:       redactSecretsWithOverlay(failure.Error(), opts.EnvOverlay),
		PartialDiff:   diff.Metadata,
		DiffPath:      diff.PatchPath,
		Original:      originalTarget,
		Status:        "awaiting_decision",
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	if opts.TestCmd != "" && !opts.NoTest {
		if runDir, err = rebindControlResumeRunDir(gate, runDir, final, fmt.Sprintf("recovery-test-round-%d.txt", round)); err != nil {
			return Result{}, originalTarget, originalAdapter, &ExitError{Code: ExitPreflightFailed, Err: err}
		}
		testPath := filepath.Join(runDir, fmt.Sprintf("recovery-test-round-%d.txt", round))
		test, _ := runTestCommand(ctx, opts.Workdir, opts.TestCmd, opts.Timeout, testPath, opts.DryRun, opts.EnvOverlay, opts.MaxOutputBytes, opts.TestIdentityRegex)
		artifact.Test = &test
	}
	allowed := map[string]bool{"quarantine": true}
	if originalAdapter.Capabilities().SupportsResume && strings.TrimSpace(sessionID) != "" {
		allowed["repair"] = true
	}
	if policyAttemptsReplacement(opts.LossPolicy.Worker) && len(fallbackTargetsForRole(opts, roleLabelsEditor(opts.Mode), originalTarget)) > 0 {
		allowed["continue_with_fallback"] = true
	}
	decision := RecoveryDecision{SchemaVersion: ArtifactSchemaVersion, Decision: "quarantine", Reason: "recovery supervisor unavailable or invalid", Evidence: []string{failure.Error()}}
	if runDir, err = rebindControlResumeRunDir(gate, runDir, final, "recovery-decision-schema.json", fmt.Sprintf("recovery-decision-round-%d.json", round)); err != nil {
		return Result{}, originalTarget, originalAdapter, &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	decisionPath := filepath.Join(runDir, fmt.Sprintf("recovery-decision-round-%d.json", round))
	schemaPath := filepath.Join(runDir, "recovery-decision-schema.json")
	if err := guardControlResumeWritePath(gate, schemaPath); err != nil {
		return Result{}, originalTarget, originalAdapter, &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	_ = writeFileDurable(schemaPath, []byte(RecoveryDecisionSchema+"\n"), 0o644, false)
	if reviewer != nil {
		result, decisionErr := a.runAdapter(ctx, reviewer, RoleSupervisor, Request{
			Context:         ctx,
			Prompt:          recoveryPrompt(failure, diff, artifact.Test, allowed),
			EnvOverlay:      opts.EnvOverlay,
			Model:           opts.Adversary.Model,
			Workdir:         opts.Workdir,
			RunDir:          runDir,
			OutputPath:      decisionPath,
			SchemaPath:      schemaPath,
			Timeout:         opts.Timeout,
			WatchdogTimeout: opts.WatchdogTimeout,
			Phase:           "repairing recovery decision",
			Quiet:           opts.Quiet,
			Verbose:         opts.Verbose,
			Budget:          opts.InvocationBudget,
			MaxOutputBytes:  opts.MaxOutputBytes,
		}, opts.DryRun)
		if decisionErr == nil {
			if parsed, parseErr := parseRecoveryDecision([]byte(result.Text), allowed); parseErr == nil {
				decision = parsed
			}
		}
	}
	artifact.Decision = decision
	artifact.UpdatedAt = time.Now().UTC()

	selectedTarget := originalTarget
	selectedAdapter := originalAdapter
	switch decision.Decision {
	case "repair":
		artifact.Status = "repairing"
	case "continue_with_fallback":
		selectedTarget, selectedAdapter, err = selectRuntimeWorkerFallback(ctx, opts, registry, originalTarget)
		if err != nil {
			decision.Decision = "quarantine"
			decision.Reason = err.Error()
			artifact.Decision = decision
			artifact.Status = "quarantined"
		}
	default:
		artifact.Status = "quarantined"
	}
	artifact.Selected = selectedTarget
	artifact.UpdatedAt = time.Now().UTC()
	if runDir, err = rebindControlResumeRunDir(gate, runDir, final, fmt.Sprintf("recovery-round-%d.json", round)); err != nil {
		return Result{}, selectedTarget, selectedAdapter, &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	artifactPath := filepath.Join(runDir, fmt.Sprintf("recovery-round-%d.json", round))
	if err := guardControlResumeWritePath(gate, artifactPath); err != nil {
		return Result{}, selectedTarget, selectedAdapter, &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	_ = writeJSONWithNewline(artifactPath, artifact)
	if artifact.Status == "quarantined" {
		final.Status = RunStatusQuarantined
		setFinalBlocking(final, ReasonQuarantined, decision.Reason)
		return Result{}, selectedTarget, selectedAdapter, &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("partial work quarantined: %s", decision.Reason)}
	}

	recoveryContext := fmt.Sprintf("Recovery context: preserve the existing partial diff at %s. %s the requested work and address the recorded failure and focused-test evidence.", diff.PatchPath, map[bool]string{true: "Repair", false: "Continue"}[decision.Decision == "repair"])
	retryPrompt := prependRecoveryContext(originalRequest.Prompt, recoveryContext)
	if runDir, err = rebindControlResumeRunDir(gate, runDir, final, fmt.Sprintf("worker-recovery-round-%d.json", round)); err != nil {
		return Result{}, selectedTarget, selectedAdapter, &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	retryOutput := filepath.Join(runDir, fmt.Sprintf("worker-recovery-round-%d.json", round))
	retryRequest := originalRequest
	retryRequest.Context = ctx
	retryRequest.Prompt = retryPrompt
	retryRequest.EnvOverlay = opts.EnvOverlay
	retryRequest.Model = selectedTarget.Model
	retryRequest.Workdir = opts.Workdir
	retryRequest.RunDir = runDir
	retryRequest.OutputPath = retryOutput
	retryRequest.SchemaPath = filepath.Join(runDir, filepath.Base(workerSchemaPath))
	retryRequest.ResumeID = map[bool]string{true: sessionID, false: ""}[decision.Decision == "repair"]
	retryRequest.Timeout = opts.Timeout
	retryRequest.WatchdogTimeout = opts.WatchdogTimeout
	retryRequest.Phase = "repairing worker continuation"
	retryRequest.ProgressRole = Role(roleLabelsEditor(opts.Mode))
	retryRequest.Quiet = opts.Quiet
	retryRequest.Verbose = opts.Verbose
	retryRequest.Budget = opts.InvocationBudget
	retryRequest.MaxOutputBytes = opts.MaxOutputBytes
	retryRequest.RequireWorkerContract = true
	result, retryErr := a.runAdapter(ctx, selectedAdapter, RoleCoder, retryRequest, opts.DryRun)
	if retryErr != nil {
		artifact.Status = "quarantined"
		artifact.UpdatedAt = time.Now().UTC()
		if runDir, err = rebindControlResumeRunDir(gate, runDir, final, fmt.Sprintf("recovery-round-%d.json", round)); err != nil {
			return Result{}, selectedTarget, selectedAdapter, &ExitError{Code: ExitPreflightFailed, Err: err}
		}
		artifactPath = filepath.Join(runDir, fmt.Sprintf("recovery-round-%d.json", round))
		if err := guardControlResumeWritePath(gate, artifactPath); err != nil {
			return Result{}, selectedTarget, selectedAdapter, &ExitError{Code: ExitPreflightFailed, Err: err}
		}
		_ = writeJSONWithNewline(artifactPath, artifact)
		final.Status = RunStatusQuarantined
		setFinalBlocking(final, ReasonQuarantined, retryErr.Error())
		return Result{}, selectedTarget, selectedAdapter, retryErr
	}
	artifact.Status = "recovered"
	artifact.UpdatedAt = time.Now().UTC()
	if runDir, err = rebindControlResumeRunDir(gate, runDir, final, fmt.Sprintf("recovery-round-%d.json", round)); err != nil {
		return Result{}, selectedTarget, selectedAdapter, &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	artifactPath = filepath.Join(runDir, fmt.Sprintf("recovery-round-%d.json", round))
	if err := guardControlResumeWritePath(gate, artifactPath); err != nil {
		return Result{}, selectedTarget, selectedAdapter, &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	_ = writeJSONWithNewline(artifactPath, artifact)
	return result, selectedTarget, selectedAdapter, nil
}

func (a *App) retryZeroDeltaEditorWithFallback(
	ctx context.Context,
	opts RunOptions,
	round int,
	runDir, baseline, workerSchemaPath string,
	originalRequest Request,
	originalTarget RoleTarget,
	originalAdapter Adapter,
	registry map[string]Adapter,
	failure error,
	beforeFallback worktreeSnapshot,
	final *FinalRun,
) (Result, RoleTarget, Adapter, error) {
	gate := controlResumeGateFrom(ctx)
	if !policyAttemptsReplacement(opts.LossPolicy.Worker) || len(fallbackTargetsForRole(opts, roleLabelsEditor(opts.Mode), originalTarget)) == 0 {
		return Result{}, originalTarget, originalAdapter, failure
	}
	selectedTarget, selectedAdapter, err := selectRuntimeWorkerFallback(ctx, opts, registry, originalTarget)
	if err != nil {
		return Result{}, originalTarget, originalAdapter, &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("%v; worker fallback unavailable: %w", failure, err)}
	}
	now := time.Now().UTC()
	decision := RecoveryDecision{
		SchemaVersion: ArtifactSchemaVersion,
		Decision:      "continue_with_fallback",
		Reason:        "primary editor failed before changing the worktree",
		Evidence:      []string{redactSecretsWithOverlay(failure.Error(), opts.EnvOverlay)},
	}
	artifact := RecoveryArtifact{
		SchemaVersion: ArtifactSchemaVersion,
		Round:         round,
		Failure:       redactSecretsWithOverlay(failure.Error(), opts.EnvOverlay),
		PartialDiff: DiffFilesMetadata{
			SchemaVersion: ArtifactSchemaVersion,
			Baseline:      baseline,
			GeneratedAt:   now,
			Files:         []DiffFile{},
		},
		Decision:  decision,
		Original:  originalTarget,
		Selected:  selectedTarget,
		Status:    "retrying_without_partial_diff",
		CreatedAt: now,
		UpdatedAt: now,
	}
	var rebindErr error
	if runDir, rebindErr = rebindControlResumeRunDir(gate, runDir, final, fmt.Sprintf("recovery-round-%d.json", round)); rebindErr != nil {
		return Result{}, originalTarget, originalAdapter, &ExitError{Code: ExitPreflightFailed, Err: rebindErr}
	}
	artifactPath := filepath.Join(runDir, fmt.Sprintf("recovery-round-%d.json", round))
	if err := guardControlResumeWritePath(gate, artifactPath); err != nil {
		return Result{}, originalTarget, originalAdapter, &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	_ = writeJSONWithNewline(artifactPath, artifact)

	retryRequest := originalRequest
	retryRequest.Context = ctx
	retryRequest.Prompt = prependRecoveryContext(originalRequest.Prompt, "Recovery context: the primary editor failed before making repository changes. Execute the original task with the configured fallback editor.")
	retryRequest.EnvOverlay = opts.EnvOverlay
	retryRequest.Model = selectedTarget.Model
	retryRequest.Workdir = opts.Workdir
	if runDir, rebindErr = rebindControlResumeRunDir(gate, runDir, final, fmt.Sprintf("worker-fallback-round-%d.json", round)); rebindErr != nil {
		return Result{}, selectedTarget, selectedAdapter, &ExitError{Code: ExitPreflightFailed, Err: rebindErr}
	}
	retryRequest.RunDir = runDir
	retryRequest.OutputPath = filepath.Join(runDir, fmt.Sprintf("worker-fallback-round-%d.json", round))
	retryRequest.SchemaPath = filepath.Join(runDir, filepath.Base(workerSchemaPath))
	retryRequest.ResumeID = ""
	retryRequest.Timeout = opts.Timeout
	retryRequest.WatchdogTimeout = opts.WatchdogTimeout
	retryRequest.Phase = fmt.Sprintf("round %d %s fallback %s", round, roleLabelsEditor(opts.Mode), selectedAdapter.ID())
	retryRequest.ProgressRole = Role(roleLabelsEditor(opts.Mode))
	retryRequest.Quiet = opts.Quiet
	retryRequest.Verbose = opts.Verbose
	retryRequest.Budget = opts.InvocationBudget
	retryRequest.MaxOutputBytes = opts.MaxOutputBytes
	retryRequest.RequireWorkerContract = true
	result, retryErr := a.runEditorWithContractRetry(ctx, opts, selectedAdapter, retryRequest, beforeFallback)
	if retryErr != nil {
		afterFallback, snapshotErr := captureWorktreeSnapshot(context.Background(), opts.Workdir)
		if snapshotErr == nil && len(worktreeDelta(beforeFallback, afterFallback)) > 0 {
			artifact.Status = "quarantined"
			artifact.UpdatedAt = time.Now().UTC()
			if runDir, rebindErr = rebindControlResumeRunDir(gate, runDir, final, fmt.Sprintf("recovery-round-%d.json", round)); rebindErr != nil {
				return Result{}, selectedTarget, selectedAdapter, &ExitError{Code: ExitPreflightFailed, Err: rebindErr}
			}
			artifactPath = filepath.Join(runDir, fmt.Sprintf("recovery-round-%d.json", round))
			if err := guardControlResumeWritePath(gate, artifactPath); err != nil {
				return Result{}, selectedTarget, selectedAdapter, &ExitError{Code: ExitPreflightFailed, Err: err}
			}
			_ = writeJSONWithNewline(artifactPath, artifact)
			final.Status = RunStatusQuarantined
			setFinalBlocking(final, ReasonQuarantined, "fallback editor failed after changing the worktree")
			return Result{}, selectedTarget, selectedAdapter, &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("fallback partial work quarantined: %w", retryErr)}
		}
		artifact.Status = "failed"
		artifact.UpdatedAt = time.Now().UTC()
		if runDir, rebindErr = rebindControlResumeRunDir(gate, runDir, final, fmt.Sprintf("recovery-round-%d.json", round)); rebindErr != nil {
			return Result{}, selectedTarget, selectedAdapter, &ExitError{Code: ExitPreflightFailed, Err: rebindErr}
		}
		artifactPath = filepath.Join(runDir, fmt.Sprintf("recovery-round-%d.json", round))
		if err := guardControlResumeWritePath(gate, artifactPath); err != nil {
			return Result{}, selectedTarget, selectedAdapter, &ExitError{Code: ExitPreflightFailed, Err: err}
		}
		_ = writeJSONWithNewline(artifactPath, artifact)
		return Result{}, selectedTarget, selectedAdapter, retryErr
	}
	artifact.Status = "recovered"
	artifact.UpdatedAt = time.Now().UTC()
	if runDir, rebindErr = rebindControlResumeRunDir(gate, runDir, final, fmt.Sprintf("recovery-round-%d.json", round)); rebindErr != nil {
		return Result{}, selectedTarget, selectedAdapter, &ExitError{Code: ExitPreflightFailed, Err: rebindErr}
	}
	artifactPath = filepath.Join(runDir, fmt.Sprintf("recovery-round-%d.json", round))
	if err := guardControlResumeWritePath(gate, artifactPath); err != nil {
		return Result{}, selectedTarget, selectedAdapter, &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	_ = writeJSONWithNewline(artifactPath, artifact)
	return result, selectedTarget, selectedAdapter, nil
}

func prependRecoveryContext(originalPrompt, recoveryContext string) string {
	originalPrompt = strings.TrimSpace(originalPrompt)
	if originalPrompt == "" {
		return workerContractPrompt(recoveryContext)
	}
	return strings.TrimSpace(recoveryContext) + "\n\n" + originalPrompt
}

func roleLabelsEditor(mode Mode) string {
	editor, _ := roleLabels(mode)
	return editor
}

func selectRuntimeWorkerFallback(ctx context.Context, opts RunOptions, registry map[string]Adapter, primary RoleTarget) (RoleTarget, Adapter, error) {
	for _, raw := range fallbackTargetsForRole(opts, roleLabelsEditor(opts.Mode), primary) {
		target, err := ParseRoleTarget(raw)
		if err != nil || roleTargetString(target) == roleTargetString(primary) {
			continue
		}
		adapter := registry[target.Adapter]
		if adapter == nil || checkAdapters(ctx, adapter) != nil {
			continue
		}
		if _, err := adapter.BuildCmd(RoleCoder, Request{Workdir: opts.Workdir, Model: target.Model, Timeout: opts.Timeout}); err != nil {
			continue
		}
		return target, adapter, nil
	}
	return RoleTarget{}, nil, fmt.Errorf("no runnable worker fallback remains")
}
