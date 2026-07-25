package tagteam

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

func (a *App) runRelayScoutPhase(ctx context.Context, opts RunOptions, runDir string, registry map[string]Adapter, scout, reviewer Adapter, meta *Meta, reviewerLabel string, repoInstructions string, scoutAvailable bool, relay *RelayContext, briefOut *string, final *FinalRun) (RunOptions, error) {
	var brief string
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
			return opts, &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("write retrieval artifact: %w", err)}
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
	scoutPrompt := withAdapterRepoInstructions(scout, BuildScoutPromptWithCodeIntel(opts.Workdir, opts.Prompt, "", opts.ScoutMode, "pre", "", "", retrievalContext, codeIntelContext, final.BaselineTest), repoInstructions)
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
				scoutPrompt = withAdapterRepoInstructions(scout, BuildScoutPromptWithCodeIntel(opts.Workdir, opts.Prompt, "", opts.ScoutMode, "pre", "", "", retrievalContext, codeIntelContext, final.BaselineTest), repoInstructions)
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
			scoutPrompt = withAdapterRepoInstructions(scout, BuildScoutPromptWithCodeIntel(opts.Workdir, opts.Prompt, "", opts.ScoutMode, "pre", "", "", "", "", final.BaselineTest), repoInstructions)
			contextBudget = estimateScoutPromptBudget(scoutPrompt, limit)
			contextBudget.Adapter = opts.Scout.Adapter
			contextBudget.Model = opts.Scout.Model
			contextBudget.RetrievalDisabledDueBudget = true
			scoutStatus.RetrievalDisabledByBudget = true
		}
		if contextBudget.Status == scoutContextStatusNearLimit {
			logProgress(opts, "scout context near configured limit estimated=%d usable=%d", contextBudget.EstimatedInputTokens, contextBudget.UsableContextTokens)
			if opts.ScoutContextPolicy == "skip" {
				setFinalDegraded(final, ReasonScoutContextTooSmall, "scout context near configured limit; skipping scout")
				appendRoleLoss(final, "scout", opts.LossPolicy.Scout, "context-budget", "degraded", ReasonScoutContextTooSmall, "near configured scout context limit")
				scoutStatus.ContinuedWithoutScoutContext = true
				skipScout = true
			}
			if opts.ScoutContextPolicy == "block" {
				err := &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("scout context near configured limit and scout_context_policy=block")}
				setFinalBlocking(final, ReasonScoutContextTooSmall, err.Error())
				_ = writeJSONWithNewline(contextBudgetPath, contextBudget)
				_ = writeJSONWithNewline(scoutStatusPath, scoutStatus)
				return opts, err
			}
		}
		if err := writeJSONWithNewline(contextBudgetPath, contextBudget); err != nil {
			return opts, &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("write scout context artifact: %w", err)}
		}
		if contextBudget.Status == scoutContextStatusExceeds {
			budgetErr := invalidScoutContextBudgetError(contextBudget)
			scoutStatus.FailureClass = scoutFailureClassContextBudget
			scoutStatus.Failure = budgetErr.Error()
			if opts.ScoutContextPolicy == "block" || policyBlocks(opts.LossPolicy.Scout) {
				setFinalBlocking(final, ReasonScoutContextTooSmall, budgetErr.Error())
				_ = writeJSONWithNewline(scoutStatusPath, scoutStatus)
				return opts, &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("scout failed and scout_failure_policy=fail; aborting relay run: %w", budgetErr)}
			}
			setFinalDegraded(final, ReasonScoutContextTooSmall, "scout context too small; continuing without scout context")
			appendRoleLoss(final, "scout", opts.LossPolicy.Scout, "context-budget", "degraded", ReasonScoutContextTooSmall, budgetErr.Error())
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
			if repaired, ok, rerr := a.tryScoutContractRepair(ctx, opts, registry, runDir, scoutOutputPath, "scout", "scout JSON repaired by worker", err, final); rerr != nil {
				return opts, rerr
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
				return opts, &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("scout failed and scout_failure_policy=fail; aborting relay run: %w", err)}
			}
			setFinalDegraded(final, ReasonScoutUnavailable, "scout failed; continuing without scout context")
			appendRoleLoss(final, "scout", opts.LossPolicy.Scout, "invoke", "degraded", classifyRoleFailure("scout", err), err.Error())
			scoutStatus.ContinuedWithoutScoutContext = true
			logProgress(opts, "scout failed; continuing without scout context error=%q", err.Error())
		} else {
			scoutStatus.ScoutSucceeded = true
			setRoleStatus(final, "scout", opts.Scout, "completed", "", "")
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
		return opts, &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("write scout execution artifact: %w", err)}
	}

	logProgress(opts, "supervisor brief started adapter=%s", reviewer.ID())
	briefOutputPath := filepath.Join(runDir, "supervisor-brief.md")
	briefResult, err := a.runSupervisorWithFallback(ctx, &opts, registry, runDir, reviewerLabel, &reviewer, meta, final, func(target RoleTarget, adapter Adapter) (Result, error) {
		return a.runAdapter(ctx, adapter, supervisorBriefRole(opts.SupervisorCanEdit), Request{
			Context:         ctx,
			Prompt:          withAdapterRepoInstructions(adapter, BuildSupervisorBriefPrompt(opts.Workdir, opts.Prompt, opts.SupervisorCanEdit, final.BaselineTest), repoInstructions),
			EnvOverlay:      opts.EnvOverlay,
			Model:           target.Model,
			Workdir:         opts.Workdir,
			RunDir:          runDir,
			OutputPath:      briefOutputPath,
			Timeout:         opts.Timeout,
			WatchdogTimeout: opts.WatchdogTimeout,
			Phase:           fmt.Sprintf("supervisor brief %s", adapter.ID()),
			Quiet:           opts.Quiet,
			Verbose:         opts.Verbose,
			Budget:          opts.InvocationBudget,
		}, opts.DryRun)
	})
	if err != nil {
		return opts, err
	}
	final.Costs[reviewerLabel] += briefResult.CostUSD
	brief = briefResult.Text
	relay.Brief = brief
	logProgress(opts, "supervisor brief completed output=%s", briefOutputPath)

	instructionsPath := filepath.Join(runDir, "supervisor-instructions.md")
	logProgress(opts, "supervisor relay instructions started adapter=%s", reviewer.ID())
	instructionsResult, err := a.runSupervisorWithFallback(ctx, &opts, registry, runDir, reviewerLabel, &reviewer, meta, final, func(target RoleTarget, adapter Adapter) (Result, error) {
		return a.runAdapter(ctx, adapter, RoleSupervisor, Request{
			Context:         ctx,
			Prompt:          withAdapterRepoInstructions(adapter, BuildRelaySupervisorInstructionsPrompt(opts.Prompt, brief, relay.Scout, final.BaselineTest), repoInstructions),
			EnvOverlay:      opts.EnvOverlay,
			Model:           target.Model,
			Workdir:         opts.Workdir,
			RunDir:          runDir,
			OutputPath:      instructionsPath,
			Timeout:         opts.Timeout,
			WatchdogTimeout: opts.WatchdogTimeout,
			Phase:           fmt.Sprintf("relay supervisor instructions %s", adapter.ID()),
			Quiet:           opts.Quiet,
			Verbose:         opts.Verbose,
			Budget:          opts.InvocationBudget,
		}, opts.DryRun)
	})
	if err != nil {
		return opts, err
	}
	relay.Instructions = instructionsResult.Text
	final.Costs[reviewerLabel] += instructionsResult.CostUSD
	logProgress(opts, "supervisor relay instructions completed output=%s", instructionsPath)
	*briefOut = brief
	return opts, nil
}
