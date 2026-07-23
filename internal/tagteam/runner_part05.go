package tagteam

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func (a *App) collectRoundLimitReports(ctx context.Context, opts RunOptions, runDir, baseline, diff string, review Review, tests []TestRun, repoInstructions string) ([]RoundLimitReport, map[string]float64) {
	editorLabel, reviewerLabel := roleLabels(opts.Mode)
	registry := Registry(a.Config, opts)
	costs := map[string]float64{}
	reports := make([]RoundLimitReport, 0, 2)

	targets := []struct {
		label       string
		counterpart string
		target      RoleTarget
		model       string
	}{
		{label: editorLabel, counterpart: reviewerLabel, target: opts.Coder, model: opts.Coder.Model},
		{label: reviewerLabel, counterpart: editorLabel, target: opts.Adversary, model: opts.Adversary.Model},
	}

	for _, target := range targets {
		adapter, ok := registry[target.target.Adapter]
		reportPath := filepath.Join(runDir, fmt.Sprintf("%s-final-report.md", target.label))
		report := RoundLimitReport{
			Role:    target.label,
			Adapter: target.target.Adapter,
			Path:    reportPath,
		}
		if !ok {
			report.Text = fmt.Sprintf("final report unavailable: unknown %s adapter %q", target.label, target.target.Adapter)
			writeRoundLimitReportArtifact(reportPath, report.Text)
			reports = append(reports, report)
			continue
		}
		if err := checkAdapters(ctx, adapter); err != nil {
			report.Text = fmt.Sprintf("final report unavailable: %v", err)
			writeRoundLimitReportArtifact(reportPath, report.Text)
			reports = append(reports, report)
			continue
		}
		prompt := withRepoInstructions(BuildRoundLimitReportPrompt(target.label, target.counterpart, opts.Mode, opts.Prompt, diffWithBaselineHeader(baseline, diff), review, tests), repoInstructions)
		result, err := a.runAdapter(ctx, adapter, RoleReporter, Request{
			Context:         ctx,
			Prompt:          prompt,
			EnvOverlay:      opts.EnvOverlay,
			Model:           target.model,
			Workdir:         opts.Workdir,
			RunDir:          runDir,
			OutputPath:      reportPath,
			Timeout:         opts.Timeout,
			WatchdogTimeout: opts.WatchdogTimeout,
			Phase:           fmt.Sprintf("round-limit %s %s", target.label, adapter.ID()),
			Quiet:           opts.Quiet,
			Verbose:         opts.Verbose,
			Budget:          opts.InvocationBudget,
		}, opts.DryRun)
		if err != nil {
			report.Text = fmt.Sprintf("final report failed: %v", err)
			writeRoundLimitReportArtifact(reportPath, report.Text)
			logProgress(opts, "round-limit %s report failed error=%q", target.label, err.Error())
			reports = append(reports, report)
			continue
		}
		report.Text = result.Text
		costs[target.label] += result.CostUSD
		logProgress(opts, "round-limit %s report completed output=%s", target.label, reportPath)
		reports = append(reports, report)
	}
	return reports, costs
}

func writeRoundLimitReportArtifact(path, text string) {
	if path == "" {
		return
	}
	_ = writeFileDurable(path, []byte(text), 0o644, true)
}

func diffWithBaselineHeader(baseline, diff string) string {
	if strings.TrimSpace(baseline) == "" {
		return diff
	}
	return fmt.Sprintf("Baseline: %s\n\n%s", baseline, diff)
}

func parseWorkPlan(raw []byte, requestedPackage string, maxPackages int) (WorkPlan, error) {
	var plan WorkPlan
	err := decodeEmbeddedJSON(raw, func(candidate []byte) error {
		var parsed WorkPlan
		if err := json.Unmarshal(candidate, &parsed); err != nil {
			return fmt.Errorf("decode work plan JSON: %w", err)
		}
		if err := validateWorkPlan(&parsed, requestedPackage, maxPackages); err != nil {
			return err
		}
		plan = parsed
		return nil
	})
	if err != nil {
		return WorkPlan{}, &OutputContractError{Err: err}
	}
	return plan, nil
}

func syntheticWorkPlan(prompt, requestedPackage string) WorkPlan {
	selected := strings.TrimSpace(requestedPackage)
	if selected == "" {
		selected = "P1"
	}
	return WorkPlan{
		SchemaVersion: ArtifactSchemaVersion,
		Summary:       "dry-run work package",
		Packages: []WorkPackage{
			{
				ID:               selected,
				Title:            "Dry-run package",
				Goal:             strings.TrimSpace(prompt),
				EstimatedSeconds: 60,
				AllowedScope:     []string{"."},
				Acceptance:       []string{"dry-run"},
				Validation:       []string{"dry-run"},
			},
		},
		SelectedPackage: selected,
		Defer:           []string{},
	}
}

func validateWorkPlan(plan *WorkPlan, requestedPackage string, maxPackages int) error {
	if plan.SchemaVersion == 0 {
		plan.SchemaVersion = ArtifactSchemaVersion
	}
	if plan.SchemaVersion != ArtifactSchemaVersion {
		return fmt.Errorf("unsupported work plan schema_version %d", plan.SchemaVersion)
	}
	if strings.TrimSpace(plan.Summary) == "" {
		return fmt.Errorf("work plan missing summary")
	}
	if len(plan.Packages) == 0 {
		return fmt.Errorf("work plan has no packages")
	}
	if maxPackages > 0 && len(plan.Packages) > maxPackages {
		return fmt.Errorf("work plan has %d packages, max is %d", len(plan.Packages), maxPackages)
	}
	seen := map[string]bool{}
	for i := range plan.Packages {
		pkg := &plan.Packages[i]
		pkg.ID = strings.TrimSpace(pkg.ID)
		if pkg.ID == "" {
			return fmt.Errorf("package %d missing id", i)
		}
		if seen[pkg.ID] {
			return fmt.Errorf("duplicate package id %q", pkg.ID)
		}
		seen[pkg.ID] = true
		if strings.TrimSpace(pkg.Title) == "" {
			return fmt.Errorf("package %s missing title", pkg.ID)
		}
		if strings.TrimSpace(pkg.Goal) == "" {
			return fmt.Errorf("package %s missing goal", pkg.ID)
		}
		if pkg.EstimatedSeconds <= 0 {
			return fmt.Errorf("package %s missing or invalid estimated_seconds", pkg.ID)
		}
		if len(pkg.AllowedScope) == 0 {
			return fmt.Errorf("package %s missing allowed_scope", pkg.ID)
		}
		if len(pkg.Acceptance) == 0 {
			return fmt.Errorf("package %s missing acceptance", pkg.ID)
		}
		if len(pkg.Validation) == 0 {
			return fmt.Errorf("package %s missing validation", pkg.ID)
		}
	}
	if strings.TrimSpace(plan.SelectedPackage) == "" {
		plan.SelectedPackage = plan.Packages[0].ID
	}
	if requested := strings.TrimSpace(requestedPackage); requested != "" {
		if !seen[requested] {
			return fmt.Errorf("requested package %q not found in work plan", requested)
		}
		plan.SelectedPackage = requested
	}
	if !seen[plan.SelectedPackage] {
		return fmt.Errorf("selected package %q not found in work plan", plan.SelectedPackage)
	}
	return nil
}

func validateWorkPlanBudget(plan WorkPlan, budgetSeconds int64) error {
	if budgetSeconds <= 0 {
		return nil
	}
	for _, pkg := range plan.Packages {
		if int64(pkg.EstimatedSeconds) > budgetSeconds {
			return fmt.Errorf("package %s estimated_seconds=%d exceeds calibrated package budget=%d", pkg.ID, pkg.EstimatedSeconds, budgetSeconds)
		}
	}
	return nil
}

func nextWorkPackage(plan WorkPlan, currentID string) (WorkPackage, bool) {
	for i, pkg := range plan.Packages {
		if strings.TrimSpace(pkg.ID) == strings.TrimSpace(currentID) && i+1 < len(plan.Packages) {
			return plan.Packages[i+1], true
		}
	}
	return WorkPackage{}, false
}

func reviewPackageID(pkg *WorkPackage) string {
	if pkg == nil {
		return ""
	}
	return pkg.ID
}

func appendRemainingPackagesSummary(summary string, remaining []string) string {
	if len(remaining) == 0 {
		return summary
	}
	if strings.TrimSpace(summary) == "" {
		summary = "Selected package completed."
	}
	return strings.TrimSpace(summary) + "\n\nRemaining packages not run: " + strings.Join(remaining, "; ")
}

func (a *App) repairJSONWithWorker(ctx context.Context, opts RunOptions, registry map[string]Adapter, runDir, artifactBase, contractName, schema string, raw []byte, validationErr error) ([]byte, float64, bool, error) {
	if opts.JSONRepair != "worker" {
		return nil, 0, false, nil
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, 0, false, nil
	}
	worker, ok := registry[opts.Coder.Adapter]
	if !ok {
		return nil, 0, true, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("unknown worker adapter %q for JSON repair", opts.Coder.Adapter)}
	}
	if err := checkAdapters(ctx, worker); err != nil {
		return nil, 0, true, err
	}
	var err error
	if runDir, artifactBase, err = rebindRepairArtifactBase(ctx, runDir, artifactBase); err != nil {
		return nil, 0, true, err
	}
	gate := controlResumeGateFrom(ctx)
	if gate != nil {
		if !pathWithin(runDir, artifactBase) {
			return nil, 0, true, &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("JSON repair artifact base escapes the validated run directory")}
		}
		if err := guardControlResumeWritePath(gate, artifactBase); err != nil {
			return nil, 0, true, &ExitError{Code: ExitPreflightFailed, Err: err}
		}
	}
	promptPath := artifactBase + ".repair-prompt.md"
	outputPath := artifactBase + ".repaired.json"
	if err := guardControlResumeWritePath(gate, promptPath); err != nil {
		return nil, 0, true, &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	if err := os.MkdirAll(filepath.Dir(artifactBase), 0o755); err != nil {
		return nil, 0, true, err
	}
	// Rebind after MkdirAll so a replacement in that window cannot redirect the
	// repair-prompt write through a stale path.
	if controlResumeAfterRepairMkdirHook != nil && controlResumeGateFrom(ctx) != nil {
		controlResumeAfterRepairMkdirHook()
	}
	if runDir, artifactBase, err = rebindRepairArtifactBase(ctx, runDir, artifactBase); err != nil {
		return nil, 0, true, err
	}
	promptPath = artifactBase + ".repair-prompt.md"
	outputPath = artifactBase + ".repaired.json"
	if err := guardControlResumeWritePath(gate, promptPath); err != nil {
		return nil, 0, true, &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	prompt := BuildWorkerJSONRepairPrompt(contractName, schema, errString(validationErr), raw)
	if err := writeRedactedBytes(promptPath, []byte(prompt), opts.EnvOverlay); err != nil {
		return nil, 0, true, err
	}
	if runDir, artifactBase, err = rebindRepairArtifactBase(ctx, runDir, artifactBase); err != nil {
		return nil, 0, true, err
	}
	promptPath = artifactBase + ".repair-prompt.md"
	outputPath = artifactBase + ".repaired.json"
	if err := guardControlResumeWritePath(gate, outputPath); err != nil {
		return nil, 0, true, &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	logProgress(opts, "worker JSON repair started contract=%s adapter=%s output=%s", contractName, worker.ID(), outputPath)
	result, err := a.runAdapter(ctx, worker, RoleReporter, Request{
		Context:           ctx,
		Prompt:            prompt,
		EnvOverlay:        opts.EnvOverlay,
		Model:             opts.Coder.Model,
		Workdir:           opts.Workdir,
		RunDir:            runDir,
		OutputPath:        outputPath,
		Timeout:           opts.Timeout,
		WatchdogTimeout:   opts.WatchdogTimeout,
		MaxOutputBytes:    opts.MaxOutputBytes,
		Phase:             fmt.Sprintf("worker JSON repair %s %s", contractName, worker.ID()),
		Quiet:             opts.Quiet,
		Verbose:           opts.Verbose,
		Budget:            opts.InvocationBudget,
		controlResumeGate: gate,
	}, opts.DryRun)
	if err != nil {
		if writeErr := writeRepairSideBytes(ctx, runDir, artifactBase, ".repair-failed.txt", []byte(err.Error()+"\n"), opts.EnvOverlay); writeErr != nil {
			return nil, 0, true, writeErr
		}
		return nil, 0, true, err
	}
	repaired := bytes.TrimSpace(result.Raw)
	if len(repaired) == 0 {
		repaired = []byte(strings.TrimSpace(result.Text))
	}
	if runDir, artifactBase, err = rebindRepairArtifactBase(ctx, runDir, artifactBase); err != nil {
		return nil, result.CostUSD, true, err
	}
	outputPath = artifactBase + ".repaired.json"
	if len(repaired) == 0 && fileExists(outputPath) {
		if err := guardControlResumeWritePath(gate, outputPath); err != nil {
			return nil, result.CostUSD, true, &ExitError{Code: ExitPreflightFailed, Err: err}
		}
		fileBytes, readErr := os.ReadFile(outputPath)
		if readErr != nil {
			return nil, result.CostUSD, true, readErr
		}
		repaired = bytes.TrimSpace(fileBytes)
	}
	if err := guardControlResumeWritePath(gate, outputPath); err != nil {
		return nil, result.CostUSD, true, &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	if err := writeRedactedBytes(outputPath, repaired, opts.EnvOverlay); err != nil {
		return nil, result.CostUSD, true, err
	}
	logProgress(opts, "worker JSON repair completed contract=%s output=%s", contractName, outputPath)
	return repaired, result.CostUSD, true, nil
}

func (a *App) runAdversary(ctx context.Context, opts RunOptions, round int, runDir, schemaPath, prompt, baseline, diff, diffPath, testOutput, coderOutputPath string, priorReview *Review, relay RelayContext, repoInstructions string, final *FinalRun) (*Review, float64, string, error) {
	_, reviewerLabel := roleLabels(opts.Mode)
	outputLabel := reviewerLabel
	if opts.Mode == ModeRelay {
		outputLabel = "supervisor-review"
	}
	registry := Registry(a.Config, opts)
	adversary, ok := registry[opts.Adversary.Adapter]
	if !ok {
		return nil, 0, "", &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("unknown %s adapter %q", reviewerLabel, opts.Adversary.Adapter)}
	}
	if err := checkAdapters(ctx, adversary); err != nil {
		return nil, 0, "", err
	}
	bundle, err := buildReviewBundle(runDir, opts, reviewerLabel, round, baseline, DiffArtifact{PatchPath: diffPath}, testOutput, coderOutputPath, relay, priorReview)
	if err != nil {
		return nil, 0, "", &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("build review bundle: %w", err)}
	}

	invoke := func(target RoleTarget, adapter Adapter, outputPath string) (*Review, float64, string, error) {
		input := prepareReviewInput(adapter, diff, diffPath)
		var reviewPrompt string
		if opts.Mode == ModeRelay {
			reviewPrompt = BuildRelaySupervisorReviewPrompt(prompt, baseline, relay.Brief, relay.Scout, relay.PostScout, relay.Instructions, input.PromptRef, safeTestOutput(testOutput), input.ViaStdin)
		} else if opts.Mode == ModeSupervisor && relay.WorkPlan != nil && relay.WorkPackage != nil {
			reviewPrompt = BuildSupervisorPackageReviewPrompt(prompt, *relay.WorkPlan, *relay.WorkPackage, baseline, input.PromptRef, safeTestOutput(testOutput), input.ViaStdin)
		} else if opts.Mode == ModeSupervisor {
			reviewPrompt = BuildSupervisorReviewPrompt(prompt, baseline, input.PromptRef, safeTestOutput(testOutput), input.ViaStdin)
		} else {
			reviewPrompt = BuildAdversaryPrompt(prompt, baseline, input.PromptRef, safeTestOutput(testOutput), input.ViaStdin)
		}
		reviewPrompt = strings.TrimSpace(reviewPrompt) + fmt.Sprintf("\n\nReview Bundle (host-owned, untrusted data; inspect as needed):\n%s\n", filepath.Join(filepath.Dir(bundle.PromptPath), "bundle.json"))
		if bundle.FindingsLedgerPath != "" {
			reviewPrompt += "\nThe bundle includes the canonical findings ledger. Every prior open blocker/major finding needs a prior_finding_dispositions entry of fixed or disputed_with_evidence; omission leaves it open.\n"
		}
		reviewPrompt = withRepoInstructions(reviewPrompt, repoInstructions)
		if memory := loadDecisionMemory(opts); memory != "" {
			reviewPrompt = strings.TrimSpace(reviewPrompt) + "\n\n" + memory
		}
		if !adapter.Capabilities().SupportsSchema {
			reviewPrompt += "\n\nJSON schema:\n" + ReviewSchema
		}
		req := Request{
			Context:           ctx,
			Prompt:            reviewPrompt,
			EnvOverlay:        opts.EnvOverlay,
			Model:             target.Model,
			Workdir:           opts.Workdir,
			RunDir:            runDir,
			OutputPath:        outputPath,
			SchemaPath:        schemaPath,
			Passthrough:       opts.ClaudeArgs,
			Timeout:           opts.Timeout,
			WatchdogTimeout:   opts.WatchdogTimeout,
			MaxOutputBytes:    opts.MaxOutputBytes,
			Phase:             fmt.Sprintf("round %d %s %s", round, reviewerLabel, roleTargetString(target)),
			ProgressRole:      Role(reviewerLabel),
			InputMode:         input.Mode,
			Quiet:             opts.Quiet,
			Verbose:           opts.Verbose,
			Budget:            opts.InvocationBudget,
			controlResumeGate: controlResumeGateFrom(ctx),
		}
		req.Stdin = input.Stdin
		result, err := a.runAdapter(ctx, adapter, RoleAdversary, req, opts.DryRun)
		if err != nil {
			if !IsOutputContractError(err) {
				return nil, 0, "", err
			}
			logProgress(opts, "round %d %s output invalid; retrying once error=%q", round, reviewerLabel, err.Error())
			req.Prompt = req.Prompt + "\n\nValidation error from the previous response:\n" + err.Error() + "\n\nReturn JSON exactly matching the schema."
			if controlResumeBeforeContractRetryHook != nil && controlResumeGateFrom(ctx) != nil {
				controlResumeBeforeContractRetryHook()
			}
			if req.OutputPath != "" {
				if rebindErr := bindControlResumeRequest(ctx, &req); rebindErr != nil {
					return nil, 0, "", &ExitError{Code: ExitPreflightFailed, Err: rebindErr}
				}
				retryPromptPath := req.OutputPath + ".retry-prompt.md"
				if gate := req.controlResumeGate; gate != nil {
					if guardErr := guardControlResumeWritePath(gate, retryPromptPath); guardErr != nil {
						return nil, 0, "", &ExitError{Code: ExitPreflightFailed, Err: guardErr}
					}
				}
				if writeErr := writeRedactedBytes(retryPromptPath, []byte(req.Prompt), req.EnvOverlay); writeErr != nil {
					return nil, 0, "", writeErr
				}
			}
			result, err = a.runAdapter(ctx, adapter, RoleAdversary, req, opts.DryRun)
			if err != nil {
				if IsOutputContractError(err) {
					if repaired, repairCost, attempted, repairErr := a.tryRepairJSONFromArtifact(ctx, opts, Registry(a.Config, opts), runDir, req.OutputPath, "review", ReviewSchema, result.Raw, err); repairErr != nil {
						if noteErr := noteJSONRepairFailure(ctx, runDir, req.OutputPath, repairErr, req.EnvOverlay); noteErr != nil {
							return nil, 0, "", noteErr
						}
					} else if attempted {
						review, parseErr := parseReviewPayload(repaired)
						if parseErr == nil {
							if final != nil {
								setFinalDegraded(final, ReasonJSONRepairUsed, "review JSON repaired by worker")
								appendRoleLoss(final, reviewerLabel, lossPolicyForRole(opts, reviewerLabel), "json-repair", "repaired", ReasonJSONRepairUsed, "worker repaired invalid review JSON")
							}
							if werr := writeRepairSideJSON(ctx, runDir, req.OutputPath, ".parsed.json", review); isControlResumePathGateError(werr) {
								return nil, 0, "", werr
							}
							return review, repairCost, outputPath, nil
						}
						if werr := writeRepairSideBytes(ctx, runDir, req.OutputPath, ".repair-validation-error.txt", []byte(parseErr.Error()+"\n"), req.EnvOverlay); isControlResumePathGateError(werr) {
							return nil, 0, "", werr
						}
					}
					if werr := writeRepairSideJSON(ctx, runDir, req.OutputPath, ".invalid.json", map[string]any{
						"schema_version": ArtifactSchemaVersion,
						"role":           string(RoleAdversary),
						"adapter":        adapter.ID(),
						"model":          target.Model,
						"error":          err.Error(),
						"recorded_at":    time.Now().UTC(),
					}); isControlResumePathGateError(werr) {
						return nil, 0, "", werr
					}
					return nil, 0, "", &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("%s output failed validation after retry: %w", reviewerLabel, err)}
				}
				return nil, 0, "", err
			}
		}
		normalizeReview(result.Review)
		applyReviewCaps(result.Review, opts.MaxFindings)
		return result.Review, result.CostUSD, outputPath, nil
	}

	outputPath := filepath.Join(runDir, fmt.Sprintf("%s-round-%d.json", outputLabel, round))
	review, cost, path, err := invoke(opts.Adversary, adversary, outputPath)
	if err == nil {
		return review, cost, path, nil
	}
	policy := lossPolicyForRole(opts, reviewerLabel)
	if !policyAttemptsReplacement(policy) {
		return nil, 0, "", err
	}
	attempts := []string{roleTargetString(opts.Adversary)}
	for _, raw := range fallbackTargetsForRole(opts, reviewerLabel, opts.Adversary) {
		target, parseErr := ParseRoleTarget(raw)
		if parseErr != nil {
			continue
		}
		attempts = append(attempts, roleTargetString(target))
		candidate, ok := registry[target.Adapter]
		if !ok {
			continue
		}
		if detectErr := checkAdapters(ctx, candidate); detectErr != nil {
			continue
		}
		logProgress(opts, "round %d %s failed; trying fallback target=%s error=%q", round, reviewerLabel, roleTargetString(target), err.Error())
		fallbackPath := filepath.Join(runDir, fmt.Sprintf("%s-round-%d-fallback-%s.json", outputLabel, round, sanitizeArtifactName(roleTargetString(target))))
		review, cost, path, fallbackErr := invoke(target, candidate, fallbackPath)
		if fallbackErr != nil {
			err = fallbackErr
			continue
		}
		if final != nil {
			if final.Adapters == nil {
				final.Adapters = map[string]string{}
			}
			if final.Models == nil {
				final.Models = map[string]string{}
			}
			final.Adapters[reviewerLabel] = target.Adapter
			final.Models[reviewerLabel] = target.Model
			setFinalDegraded(final, ReasonFallbackUsed, fmt.Sprintf("%s fallback selected after primary failure", reviewerLabel))
			appendRoleLoss(final, reviewerLabel, policy, "replace", "fallback_selected_after_failure", ReasonFallbackUsed, fmt.Sprintf("%s -> %s", roleTargetString(opts.Adversary), roleTargetString(target)))
			setRoleStatus(final, reviewerLabel, target, "completed", ReasonFallbackUsed, "fallback selected after primary failure")
			status := final.RoleStatuses[reviewerLabel]
			status.Attempts = attempts
			status.Selected = roleTargetString(target)
			final.RoleStatuses[reviewerLabel] = status
		}
		return review, cost, path, nil
	}
	return nil, 0, "", err
}

func normalizeReview(review *Review) {
	if review == nil {
		return
	}
	review.SchemaVersion = ReviewSchemaVersion
	if review.Findings == nil {
		review.Findings = []Finding{}
	}
	if review.TestSuggestions == nil {
		review.TestSuggestions = []string{}
	}
	if review.PriorFindingDispositions == nil {
		review.PriorFindingDispositions = []FindingDisposition{}
	}
	if review.DataLossChecks == nil {
		review.DataLossChecks = notApplicableDataLossChecks("host-generated review record")
	}
	for i := range review.Findings {
		if review.Findings[i].ID == "" {
			review.Findings[i].ID = stableFindingID(review.Findings[i])
		}
	}
}

func normalizeScout(scout *Scout) {
	if scout == nil {
		return
	}
	if scout.SchemaVersion == 0 {
		scout.SchemaVersion = ArtifactSchemaVersion
	}
	if scout.RelevantFiles == nil {
		scout.RelevantFiles = []string{}
	}
	if scout.LikelyEntryPoints == nil {
		scout.LikelyEntryPoints = []string{}
	}
	if scout.ExistingPatterns == nil {
		scout.ExistingPatterns = []string{}
	}
	if scout.Risks == nil {
		scout.Risks = []string{}
	}
	if scout.SuggestedTests == nil {
		scout.SuggestedTests = []string{}
	}
	if scout.RetrievalQueries == nil {
		scout.RetrievalQueries = []string{}
	}
	if scout.Evidence == nil {
		scout.Evidence = []ScoutEvidence{}
	}
}

func applyReviewCaps(review *Review, maxFindings int) {
	if review == nil || maxFindings <= 0 || len(review.Findings) <= maxFindings {
		return
	}
	severityRank := func(severity string) int {
		switch severity {
		case "blocker":
			return 0
		case "major":
			return 1
		case "minor":
			return 2
		case "nit":
			return 3
		default:
			return 4
		}
	}
	sort.SliceStable(review.Findings, func(i, j int) bool {
		return severityRank(review.Findings[i].Severity) < severityRank(review.Findings[j].Severity)
	})
	review.Findings = append([]Finding{}, review.Findings[:maxFindings]...)
	review.Summary = strings.TrimSpace(review.Summary + fmt.Sprintf("\n\nReview findings were truncated to the configured max_findings=%d.", maxFindings))
}

func maxOutputBytes(req Request) int64 {
	if req.MaxOutputBytes > 0 {
		return req.MaxOutputBytes
	}
	if req.Context != nil {
		if value, ok := req.Context.Value(maxOutputBytesContextKey{}).(int64); ok && value > 0 {
			return value
		}
	}
	return 2 * 1024 * 1024
}

func startDeliveryRecord(adapter Adapter, role Role, req *Request, dryRun bool, spec *CommandSpec) (DeliveryRecord, string, error) {
	if err := rebindRequestControlResume(req, "deliveries"); err != nil {
		return DeliveryRecord{}, "", &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	started := time.Now().UTC()
	name := fmt.Sprintf("%s-%s-%s", started.Format("20060102T150405.000000000Z"), sanitizeArtifactName(string(role)), sanitizeArtifactName(adapter.ID()))
	record := DeliveryRecord{
		SchemaVersion: ArtifactSchemaVersion,
		InvocationID:  name,
		Role:          role,
		Adapter:       adapter.ID(),
		Phase:         req.Phase,
		SchemaPath:    req.SchemaPath,
		OutputPath:    req.OutputPath,
		Model:         req.Model,
		Timeout:       req.Timeout.String(),
		InputMode:     req.InputMode,
		PromptInlined: promptInArgv(req.Prompt, spec),
		DryRun:        dryRun,
		StartedAt:     started,
		Status:        "started",
	}
	if spec != nil {
		record.Argv = redactStringSlice(spec.Argv, req.EnvOverlay)
	}
	if spec != nil && len(spec.Stdin) > 0 {
		sum := sha256.Sum256(spec.Stdin)
		record.StdinBytes = len(spec.Stdin)
		record.StdinSHA256 = hex.EncodeToString(sum[:])
	}
	if req.RunDir == "" {
		return record, "", nil
	}
	dir := filepath.Join(req.RunDir, "deliveries")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return record, "", err
	}
	promptPath := filepath.Join(dir, name+".prompt.md")
	if err := guardControlResumeWritePath(req.controlResumeGate, promptPath); err != nil {
		return record, "", &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	if err := writeRedactedBytes(promptPath, []byte(req.Prompt), req.EnvOverlay); err != nil {
		return record, "", err
	}
	record.PromptPath = promptPath
	record.StdoutPath = filepath.Join(dir, name+".stdout.txt")
	record.StderrPath = filepath.Join(dir, name+".stderr.txt")
	recordPath := filepath.Join(dir, name+".json")
	if err := guardControlResumeWritePath(req.controlResumeGate, recordPath); err != nil {
		return record, "", &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	if err := writeJSONWithNewline(recordPath, record); err != nil {
		return record, "", err
	}
	return record, recordPath, nil
}

func writeOutputContractArtifacts(req Request, role Role, result Result, raw []byte) (string, error) {
	if err := rebindRequestControlResume(&req); err != nil {
		return "", &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	if req.OutputPath == "" {
		return "", nil
	}
	if err := guardControlResumeWritePath(req.controlResumeGate, req.OutputPath); err != nil {
		return "", &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	rawPath := req.OutputPath + ".raw"
	if err := guardControlResumeWritePath(req.controlResumeGate, rawPath); err != nil {
		return rawPath, &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	if err := writeRedactedBytes(rawPath, raw, req.EnvOverlay); err != nil {
		return rawPath, err
	}
	switch role {
	case RoleCoder:
		if result.Worker != nil {
			parsedPath := req.OutputPath + ".parsed.json"
			if err := guardControlResumeWritePath(req.controlResumeGate, parsedPath); err != nil {
				return rawPath, &ExitError{Code: ExitPreflightFailed, Err: err}
			}
			if err := writeJSONWithNewline(parsedPath, result.Worker); err != nil {
				return rawPath, err
			}
		}
	case RoleAdversary:
		if result.Review != nil {
			normalizeReview(result.Review)
			parsedPath := req.OutputPath + ".parsed.json"
			if err := guardControlResumeWritePath(req.controlResumeGate, parsedPath); err != nil {
				return rawPath, &ExitError{Code: ExitPreflightFailed, Err: err}
			}
			if err := writeJSONWithNewline(parsedPath, result.Review); err != nil {
				return rawPath, err
			}
		}
	case RoleScout:
		if result.Scout != nil {
			normalizeScout(result.Scout)
			parsedPath := req.OutputPath + ".parsed.json"
			if err := guardControlResumeWritePath(req.controlResumeGate, parsedPath); err != nil {
				return rawPath, &ExitError{Code: ExitPreflightFailed, Err: err}
			}
			if err := writeJSONWithNewline(parsedPath, result.Scout); err != nil {
				return rawPath, err
			}
		}
	}
	return rawPath, nil
}

func writeValidationErrorArtifact(req Request, cause error) (string, error) {
	if cause == nil || req.OutputPath == "" {
		return "", nil
	}
	if err := rebindRequestControlResume(&req); err != nil {
		return "", &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	if err := guardControlResumeWritePath(req.controlResumeGate, req.OutputPath); err != nil {
		return "", &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	path := req.OutputPath + ".validation-error.txt"
	if err := guardControlResumeWritePath(req.controlResumeGate, path); err != nil {
		return path, &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	if err := writeFileDurable(path, []byte(redactSecretsWithOverlay(cause.Error(), req.EnvOverlay)+"\n"), 0o644, true); err != nil {
		return path, err
	}
	return path, nil
}

func promptInArgv(prompt string, spec *CommandSpec) bool {
	if spec == nil || prompt == "" {
		return false
	}
	for _, arg := range spec.Argv {
		if arg == prompt {
			return true
		}
	}
	return false
}

func finishDeliveryRecord(req Request, path string, record DeliveryRecord, status string, err error) {
	if path == "" {
		return
	}
	if guardErr := guardControlResumeWritePath(req.controlResumeGate, path); guardErr != nil {
		return
	}
	record.FinishedAt = time.Now().UTC()
	record.Status = status
	if err != nil {
		record.Error = redactSecrets(err.Error())
	}
	_ = writeJSONWithNewline(path, record)
}

func finishInvocationStreams(record *DeliveryRecord, stdout, stderr *invocationStream) {
	if stdout != nil {
		_ = stdout.Close()
		record.StdoutBytes = stdout.Received()
		record.StdoutTruncated = stdout.Exceeded()
	}
	if stderr != nil {
		_ = stderr.Close()
		record.StderrBytes = stderr.Received()
		record.StderrTruncated = stderr.Exceeded()
	}
}
