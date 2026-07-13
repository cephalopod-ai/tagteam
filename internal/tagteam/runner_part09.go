package tagteam

import (
	"bytes"
	"context"
	"os"
	"strings"
)

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
func logJSONRepairPolicy(opts RunOptions) {
	_, reviewerLabel := roleLabels(opts.Mode)
	if opts.JSONRepair == "worker" {
		logProgress(opts, "worker JSON repair enabled explicitly; invalid JSON contract output may be parsed by worker=%s", roleTargetString(opts.Coder))
		return
	}
	if reviewerLabel == "supervisor" && opts.Adversary.Adapter == "claude" {
		logProgress(opts, "warning: Claude supervisor has a known JSON-output rough edge; rerun with --repair-json-with-worker to explicitly allow the worker parser workaround")
	}
}
func readRepairSource(ctx context.Context, runDir, outputPath string, fallback []byte) ([]byte, error) {
	if strings.TrimSpace(outputPath) == "" {
		return fallback, nil
	}
	prevRunDir := runDir
	current, err := rebindControlResumeFromContext(ctx, runDir, nil)
	if err != nil {
		return nil, &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	outputPath = rebuildControlResumeArtifactPath(prevRunDir, current, outputPath)
	if gate := controlResumeGateFrom(ctx); gate != nil {
		if err := guardControlResumeWritePath(gate, outputPath); err != nil {
			return nil, &ExitError{Code: ExitPreflightFailed, Err: err}
		}
	}
	if fileExists(outputPath) {
		if raw, err := os.ReadFile(outputPath); err == nil && len(bytes.TrimSpace(raw)) > 0 {
			return raw, nil
		}
	}
	return fallback, nil
}
func (a *App) tryRepairJSONFromArtifact(ctx context.Context, opts RunOptions, registry map[string]Adapter, runDir, artifactBase, contractName, schema string, fallback []byte, validationErr error) ([]byte, float64, bool, error) {
	source, err := readRepairSource(ctx, runDir, artifactBase, fallback)
	if err != nil {
		return nil, 0, false, err
	}
	return a.repairJSONWithWorker(ctx, opts, registry, runDir, artifactBase, contractName, schema, source, validationErr)
}

// isControlResumePathGateError treats typed ExitPreflightFailed as authoritative.
func isControlResumePathGateError(err error) bool {
	return err != nil && ExitCode(err) == ExitPreflightFailed
}

// rebindRepairArtifactBase re-resolves runDir via the context gate and rebuilds
// artifactBase from the validated directory. Nil gate leaves paths unchanged.
func rebindRepairArtifactBase(ctx context.Context, runDir, artifactBase string) (string, string, error) {
	prev := runDir
	current, err := rebindControlResumeFromContext(ctx, runDir, nil)
	if err != nil {
		return "", "", &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	return current, rebuildControlResumeArtifactPath(prev, current, artifactBase), nil
}

func resolveRepairSidePath(ctx context.Context, runDir, artifactBase, suffix string) (string, error) {
	if strings.TrimSpace(artifactBase) == "" {
		return "", nil
	}
	_, base, err := rebindRepairArtifactBase(ctx, runDir, artifactBase)
	if err != nil {
		return "", err
	}
	target := base + suffix
	if gate := controlResumeGateFrom(ctx); gate != nil {
		if err := guardControlResumeWritePath(gate, target); err != nil {
			return "", &ExitError{Code: ExitPreflightFailed, Err: err}
		}
	}
	return target, nil
}
func writeRepairSideBytes(ctx context.Context, runDir, artifactBase, suffix string, data []byte, envOverlay map[string]string) error {
	target, err := resolveRepairSidePath(ctx, runDir, artifactBase, suffix)
	if err != nil || target == "" {
		return err
	}
	return writeRedactedBytes(target, data, envOverlay)
}
func writeRepairSideJSON(ctx context.Context, runDir, artifactBase, suffix string, value any) error {
	target, err := resolveRepairSidePath(ctx, runDir, artifactBase, suffix)
	if err != nil || target == "" {
		return err
	}
	return writeJSONWithNewline(target, value)
}

// noteJSONRepairFailure records non-gate repair failures as a side artifact.
// Path-gate/preflight repair errors propagate immediately. Any non-nil side-
// artifact write error also propagates so durable-write failures are not hidden.
func noteJSONRepairFailure(ctx context.Context, runDir, artifactBase string, repairErr error, envOverlay map[string]string) error {
	if repairErr == nil {
		return nil
	}
	if isControlResumePathGateError(repairErr) {
		return repairErr
	}
	if writeErr := writeRepairSideBytes(ctx, runDir, artifactBase, ".repair-failed.txt", []byte(repairErr.Error()+"\n"), envOverlay); writeErr != nil {
		return writeErr
	}
	return nil
}
func (a *App) tryWorkPlanRepair(ctx context.Context, opts RunOptions, registry map[string]Adapter, runDir, planOutputPath string, raw []byte, useArtifact bool, validationErr error, final *FinalRun, reviewerLabel string) (WorkPlan, float64, bool, error) {
	var repaired []byte
	var repairCost float64
	var attempted bool
	var repairErr error
	if useArtifact {
		repaired, repairCost, attempted, repairErr = a.tryRepairJSONFromArtifact(ctx, opts, registry, runDir, planOutputPath, "supervisor work plan", WorkPlanSchema, nil, validationErr)
	} else {
		repaired, repairCost, attempted, repairErr = a.repairJSONWithWorker(ctx, opts, registry, runDir, planOutputPath, "supervisor work plan", WorkPlanSchema, raw, validationErr)
	}
	if repairErr != nil {
		return WorkPlan{}, 0, false, noteJSONRepairFailure(ctx, runDir, planOutputPath, repairErr, opts.EnvOverlay)
	}
	if !attempted {
		return WorkPlan{}, 0, false, nil
	}
	repairedPlan, parseErr := parseWorkPlan(repaired, opts.Package, opts.MaxPackages)
	if parseErr != nil {
		if werr := writeRepairSideBytes(ctx, runDir, planOutputPath, ".repair-validation-error.txt", []byte(parseErr.Error()+"\n"), opts.EnvOverlay); isControlResumePathGateError(werr) {
			return WorkPlan{}, 0, false, werr
		}
		return WorkPlan{}, 0, false, nil
	}
	if final != nil {
		setFinalDegraded(final, ReasonJSONRepairUsed, "supervisor work-plan JSON repaired by worker")
		appendRoleLoss(final, reviewerLabel, lossPolicyForRole(opts, reviewerLabel), "json-repair", "repaired", ReasonJSONRepairUsed, "worker repaired invalid work-plan JSON")
	}
	return repairedPlan, repairCost, true, nil
}
func (a *App) tryScoutContractRepair(ctx context.Context, opts RunOptions, registry map[string]Adapter, runDir, artifactBase, contractName, degradeMsg string, validationErr error, final *FinalRun) (Result, bool, error) {
	repaired, repairCost, attempted, repairErr := a.tryRepairJSONFromArtifact(ctx, opts, registry, runDir, artifactBase, contractName, ScoutSchemaForRepair(), nil, validationErr)
	if repairErr != nil {
		return Result{}, false, noteJSONRepairFailure(ctx, runDir, artifactBase, repairErr, opts.EnvOverlay)
	}
	if !attempted {
		return Result{}, false, nil
	}
	repairedScout, parseErr := parseScout(repaired)
	if parseErr != nil {
		if werr := writeRepairSideBytes(ctx, runDir, artifactBase, ".repair-validation-error.txt", []byte(parseErr.Error()+"\n"), opts.EnvOverlay); isControlResumePathGateError(werr) {
			return Result{}, false, werr
		}
		return Result{}, false, nil
	}
	if final != nil {
		setFinalDegraded(final, ReasonJSONRepairUsed, degradeMsg)
		appendRoleLoss(final, "scout", opts.LossPolicy.Scout, "json-repair", "repaired", ReasonJSONRepairUsed, "worker repaired invalid "+contractName+" JSON")
	}
	return Result{Scout: repairedScout, Text: repairedScout.Summary, CostUSD: repairCost}, true, nil
}
