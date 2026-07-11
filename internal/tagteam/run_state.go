package tagteam

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func initFinalState(final *FinalRun, opts RunOptions) {
	final.Status = RunStatusRunning
	final.Phase = "preflight"
	final.envOverlay = opts.EnvOverlay
	final.RoleStatuses = map[string]RoleStatus{}
	final.RoleLosses = []RoleLossRecord{}
	final.Budgets = BudgetState{
		MaxRounds:          opts.Rounds,
		MaxRoleInvocations: opts.MaxRoleInvocations,
		MaxWallClock:       opts.MaxWallTime,
	}
}

func finalizeRunState(final *FinalRun) {
	if final.Status == RunStatusCancelled || final.Status == RunStatusQuarantined {
		normalizeFinalVerdict(final)
		return
	}
	if final.ExitCode == ExitSuccess {
		if final.Degraded || final.DegradedReason != "" {
			final.Status = RunStatusDegraded
			final.Degraded = true
		} else {
			final.Status = RunStatusPassed
		}
		return
	}
	if final.ExitCode == ExitBlockingFindings || final.Review != nil && final.Review.HasBlockingFindings() || final.RoundLimitReached {
		final.Status = RunStatusBlocked
	} else {
		final.Status = RunStatusFailed
	}
	normalizeFinalVerdict(final)
	if final.BlockingReason == "" {
		final.BlockingReason = string(reasonForExit(final.ExitCode))
	}
	if final.BlockingReason == string(ReasonBudgetExceeded) {
		final.Budgets.Exhausted = true
		final.Budgets.ReasonCode = ReasonBudgetExceeded
	}
}

func normalizeFinalVerdict(final *FinalRun) {
	if final.ExitCode == ExitSuccess {
		return
	}
	switch final.Status {
	case RunStatusCancelled:
		if final.Verdict == "" || final.Verdict == "pass" || final.Verdict == "done" {
			final.Verdict = "cancelled"
		}
	case RunStatusBlocked:
		if final.Verdict == "" || final.Verdict == "pass" || final.Verdict == "done" {
			final.Verdict = "needs_changes"
		}
	default:
		if final.Verdict == "" || final.Verdict == "pass" || final.Verdict == "done" {
			final.Verdict = "error"
		}
	}
}

func reasonForExit(code int) ReasonCode {
	switch code {
	case ExitTestsFailed:
		return ReasonTestFailed
	case ExitBlockingFindings:
		return ReasonBlockingFindings
	case ExitAdapterFailure:
		return ReasonReviewerUnavailable
	case ExitPreflightFailed:
		return ReasonSupervisorUnavailable
	default:
		return ReasonNone
	}
}

func setFinalDegraded(final *FinalRun, reason ReasonCode, message string) {
	final.Degraded = true
	if final.DegradedReason == "" {
		if reason != "" {
			final.DegradedReason = string(reason)
		} else {
			final.DegradedReason = strings.TrimSpace(message)
		}
	}
}

func setFinalBlocking(final *FinalRun, reason ReasonCode, message string) {
	if final.BlockingReason == "" {
		if reason != "" {
			final.BlockingReason = string(reason)
		} else {
			final.BlockingReason = strings.TrimSpace(message)
		}
	}
}

func setRoleStatus(final *FinalRun, role string, target RoleTarget, status string, reason ReasonCode, message string) {
	if final.RoleStatuses == nil {
		final.RoleStatuses = map[string]RoleStatus{}
	}
	current := final.RoleStatuses[role]
	current.Role = role
	current.Adapter = target.Adapter
	current.Model = target.Model
	current.Status = status
	current.ReasonCode = reason
	current.Message = redactSecretsWithOverlay(message, final.envOverlay)
	current.LastUpdatedAt = time.Now().UTC()
	final.RoleStatuses[role] = current
}

func setFinalRoleTarget(final *FinalRun, role string, target RoleTarget) {
	if final.Adapters == nil {
		final.Adapters = map[string]string{}
	}
	if final.Models == nil {
		final.Models = map[string]string{}
	}
	final.Adapters[role] = target.Adapter
	final.Models[role] = target.Model
}

func renameRoleStatus(final *FinalRun, from, to string) {
	if from == to || final.RoleStatuses == nil {
		return
	}
	status, ok := final.RoleStatuses[from]
	if !ok {
		return
	}
	delete(final.RoleStatuses, from)
	status.Role = to
	status.LastUpdatedAt = time.Now().UTC()
	final.RoleStatuses[to] = status
}

func appendRoleLoss(final *FinalRun, role string, policy LossPolicy, action, outcome string, reason ReasonCode, message string) {
	final.RoleLosses = append(final.RoleLosses, RoleLossRecord{
		Role:            role,
		Policy:          policy,
		AttemptedAction: action,
		Outcome:         outcome,
		ReasonCode:      reason,
		Message:         redactSecretsWithOverlay(message, final.envOverlay),
	})
}

func policyBlocks(policy LossPolicy) bool {
	return policy == LossPolicyBlock || policy == LossPolicyReplaceThenBlock
}

func policyDegrades(policy LossPolicy) bool {
	return policy == LossPolicyDegrade || policy == LossPolicyReplaceThenDegrade
}

func policyAttemptsReplacement(policy LossPolicy) bool {
	return policy == LossPolicyReplaceThenBlock || policy == LossPolicyReplaceThenDegrade
}

func classifyRoleFailure(role string, err error) ReasonCode {
	if err == nil {
		return ReasonNone
	}
	if IsOutputContractError(err) && (role == "worker" || role == "coder" || role == "solo") {
		return ReasonWorkerOutputInvalid
	}
	if IsOutputContractError(err) {
		return ReasonReviewerJSONInvalid
	}
	if errors.Is(err, errInvocationBudgetExceeded) {
		return ReasonBudgetExceeded
	}
	if errors.Is(err, errInvocationStalled) {
		return ReasonStalled
	}
	if errors.Is(err, errScoutContextTooSmall) {
		return ReasonScoutContextTooSmall
	}
	switch role {
	case "scout":
		return ReasonScoutUnavailable
	case "worker", "coder", "solo":
		if errors.Is(err, context.DeadlineExceeded) || strings.Contains(strings.ToLower(err.Error()), "deadline") {
			return ReasonWorkerTimeout
		}
		return ReasonWorkerUnavailable
	case "supervisor":
		return ReasonSupervisorUnavailable
	default:
		return ReasonReviewerUnavailable
	}
}

var errInvocationBudgetExceeded = errors.New("role invocation budget exceeded")
var errInvocationStalled = errors.New("invocation stalled without Git or output progress")

func (b *InvocationBudget) Before(role, phase string) error {
	if b == nil {
		return nil
	}
	if b.Max > 0 && b.Used >= b.Max {
		return fmt.Errorf("%w: max_role_invocations=%d before %s (%s)", errInvocationBudgetExceeded, b.Max, role, phase)
	}
	b.Used++
	return nil
}

func applyInvocationBudget(final *FinalRun, budget *InvocationBudget) {
	if budget == nil {
		return
	}
	final.Budgets.RoleInvocations = budget.Used
	if budget.Max > 0 {
		final.Budgets.MaxRoleInvocations = budget.Max
	}
}

type ReviewBundle struct {
	SchemaVersion      int       `json:"schema_version"`
	Role               string    `json:"role"`
	Round              int       `json:"round"`
	Baseline           string    `json:"baseline"`
	PromptPath         string    `json:"prompt_path"`
	ConfigSummaryPath  string    `json:"config_summary_path"`
	DiffPath           string    `json:"diff_path,omitempty"`
	FilesPath          string    `json:"files_path,omitempty"`
	TestOutputPath     string    `json:"test_output_path,omitempty"`
	ScoutOutputPath    string    `json:"scout_output_path,omitempty"`
	CoderOutputPath    string    `json:"coder_output_path,omitempty"`
	PriorFindingsPath  string    `json:"prior_findings_path,omitempty"`
	FindingsLedgerPath string    `json:"findings_ledger_path,omitempty"`
	GeneratedAt        time.Time `json:"generated_at"`
}

func buildReviewBundle(runDir string, opts RunOptions, role string, round int, baseline string, diff DiffArtifact, testOutput, coderOutputPath string, relay RelayContext, prior *Review) (ReviewBundle, error) {
	dir := filepath.Join(runDir, fmt.Sprintf("bundle-%s-round-%d", sanitizeArtifactName(role), round))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ReviewBundle{}, err
	}
	bundle := ReviewBundle{
		SchemaVersion:     ArtifactSchemaVersion,
		Role:              role,
		Round:             round,
		Baseline:          baseline,
		PromptPath:        filepath.Join(dir, "original-prompt.md"),
		ConfigSummaryPath: filepath.Join(dir, "config-summary.json"),
		DiffPath:          diff.PatchPath,
		FilesPath:         diff.FilesPath,
		CoderOutputPath:   coderOutputPath,
		GeneratedAt:       time.Now().UTC(),
	}
	if err := writeRedactedBytes(bundle.PromptPath, []byte(opts.Prompt), opts.EnvOverlay); err != nil {
		return ReviewBundle{}, err
	}
	configSummary := map[string]any{
		"schema_version":  ArtifactSchemaVersion,
		"mode":            opts.Mode,
		"coder":           opts.Coder,
		"adversary":       opts.Adversary,
		"scout":           opts.Scout,
		"rounds":          opts.Rounds,
		"test_configured": opts.TestCmd != "",
		"loss_policy":     opts.LossPolicy,
		"fallbacks":       opts.Fallbacks,
	}
	if err := writeJSONWithNewline(bundle.ConfigSummaryPath, configSummary); err != nil {
		return ReviewBundle{}, err
	}
	if strings.TrimSpace(testOutput) != "" {
		bundle.TestOutputPath = filepath.Join(dir, "test-output.txt")
		if err := writeRedactedBytes(bundle.TestOutputPath, []byte(testOutput), opts.EnvOverlay); err != nil {
			return ReviewBundle{}, err
		}
	}
	if relay.Scout.Summary != "" || len(relay.Scout.RelevantFiles) > 0 || len(relay.Scout.Items) > 0 {
		bundle.ScoutOutputPath = filepath.Join(dir, "scout-output.json")
		if err := writeJSONWithNewline(bundle.ScoutOutputPath, relay.Scout); err != nil {
			return ReviewBundle{}, err
		}
	}
	if prior != nil {
		bundle.PriorFindingsPath = filepath.Join(dir, "prior-review.json")
		if err := writeJSONWithNewline(bundle.PriorFindingsPath, prior); err != nil {
			return ReviewBundle{}, err
		}
	}
	if ledger, err := os.ReadFile(filepath.Join(runDir, findingsLedgerFilename)); err == nil {
		bundle.FindingsLedgerPath = filepath.Join(dir, "findings-ledger.json")
		if err := writeFileDurable(bundle.FindingsLedgerPath, ledger, 0o644, true); err != nil {
			return ReviewBundle{}, err
		}
	}
	indexPath := filepath.Join(dir, "bundle.json")
	if err := writeJSONWithNewline(indexPath, bundle); err != nil {
		return ReviewBundle{}, err
	}
	return bundle, nil
}

func fallbackTargetsForRole(opts RunOptions, role string, primary RoleTarget) []string {
	var out []string
	if len(opts.FallbacksByTarget) > 0 {
		out = append(out, opts.FallbacksByTarget[roleTargetString(primary)]...)
	}
	switch role {
	case "worker", "coder", "solo":
		out = append(out, opts.Fallbacks.Worker...)
	case "scout":
		out = append(out, opts.Fallbacks.Scout...)
	case "supervisor":
		out = append(out, opts.Fallbacks.Supervisor...)
	default:
		out = append(out, opts.Fallbacks.Reviewer...)
	}
	return normalizeFallbackTargets(out, primary)
}

func lossPolicyForRole(opts RunOptions, role string) LossPolicy {
	switch role {
	case "worker", "coder", "solo":
		return opts.LossPolicy.Worker
	case "scout":
		return opts.LossPolicy.Scout
	case "supervisor":
		return opts.LossPolicy.Supervisor
	default:
		return opts.LossPolicy.Reviewer
	}
}
