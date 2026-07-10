package tagteam

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type App struct {
	Config Config
}

const maxReviewInputBytes = 10 * 1024 * 1024
const maxInlineReviewPromptBytes = 128 * 1024
const maxRepoInstructionBytes = 64 * 1024

var repoInstructionCandidates = []string{
	"AGENTS.md",
	"agent.md",
	filepath.Join(".tagteam", "AGENTS.md"),
	filepath.Join(".codex", "AGENTS.md"),
	filepath.Join(".claude", "AGENTS.md"),
	filepath.Join(".agy", "AGENTS.md"),
}

type reviewInput struct {
	PromptRef string
	Stdin     []byte
	ViaStdin  bool
	Mode      string
}

type DiffArtifact struct {
	PatchPath   string
	NumstatPath string
	FilesPath   string
	SHA256Path  string
	Patch       string
	Metadata    DiffFilesMetadata
}

type RelayContext struct {
	Brief        string
	Scout        Scout
	PostScout    Scout
	Instructions string
	WorkPlan     *WorkPlan
	WorkPackage  *WorkPackage
}

type RepoInstructionSource struct {
	Path          string `json:"path"`
	DisplayPath   string `json:"display_path"`
	SizeBytes     int64  `json:"size_bytes"`
	SHA256        string `json:"sha256"`
	IncludedBytes int    `json:"included_bytes"`
	Truncated     bool   `json:"truncated"`
}

type RepoInstructionsMetadata struct {
	SchemaVersion    int                     `json:"schema_version"`
	Enabled          bool                    `json:"enabled"`
	MaxBytes         int                     `json:"max_bytes"`
	GeneratedAt      time.Time               `json:"generated_at"`
	SourceCount      int                     `json:"source_count"`
	TotalSourceBytes int64                   `json:"total_source_bytes"`
	BundleBytes      int                     `json:"bundle_bytes"`
	Truncated        bool                    `json:"truncated"`
	Sources          []RepoInstructionSource `json:"sources"`
}

type repoInstructionsBundle struct {
	Text     string
	Metadata RepoInstructionsMetadata
}

func NewApp(cfg Config) *App {
	return &App{Config: cfg}
}

func loadAndPersistRepoInstructions(ctx context.Context, opts RunOptions, runDir string) (string, error) {
	if !opts.RespectRepoInstructions {
		return "", nil
	}
	bundle, err := loadRepoInstructions(ctx, opts.Workdir, maxRepoInstructionBytes)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(runDir, "repo-instructions.md"), []byte(bundle.Text), 0o644); err != nil {
		return "", err
	}
	if err := writeJSON(filepath.Join(runDir, "repo-instructions.json"), bundle.Metadata); err != nil {
		return "", err
	}
	return bundle.Text, nil
}

func loadDecisionMemory(opts RunOptions) string {
	if !opts.DecisionMemory {
		return ""
	}
	path := filepath.Join(opts.Workdir, ".tagteam", "decisions.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(data) > maxRepoInstructionBytes {
		data = data[:maxRepoInstructionBytes]
	}
	text := strings.TrimSpace(string(normalizeTextFileNewline(data)))
	if text == "" {
		return ""
	}
	return "Decision Memory (user-dismissed recurring findings; use as advisory context only):\n\n" + text
}

func loadRepoInstructions(ctx context.Context, workdir string, maxBytes int) (repoInstructionsBundle, error) {
	if maxBytes <= 0 {
		maxBytes = maxRepoInstructionBytes
	}
	generatedAt := time.Now().UTC()
	absWorkdir, err := filepath.Abs(workdir)
	if err != nil {
		return repoInstructionsBundle{}, err
	}
	bases := repoInstructionBases(ctx, absWorkdir)
	seen := map[string]bool{}
	type discovered struct {
		source RepoInstructionSource
		data   []byte
	}
	var files []discovered
	for _, base := range bases {
		for _, rel := range repoInstructionCandidates {
			path := filepath.Join(base, rel)
			absPath, err := filepath.Abs(path)
			if err != nil {
				return repoInstructionsBundle{}, err
			}
			if seen[absPath] {
				continue
			}
			seen[absPath] = true
			parentInfo, err := os.Stat(filepath.Dir(absPath))
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return repoInstructionsBundle{}, err
			}
			if !parentInfo.IsDir() {
				continue
			}
			info, err := os.Stat(absPath)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return repoInstructionsBundle{}, err
			}
			if !info.Mode().IsRegular() {
				continue
			}
			data, err := os.ReadFile(absPath)
			if err != nil {
				return repoInstructionsBundle{}, err
			}
			displayPath := rel
			if base != absWorkdir {
				if rootRel, relErr := filepath.Rel(absWorkdir, absPath); relErr == nil && !strings.HasPrefix(rootRel, ".."+string(filepath.Separator)) && rootRel != ".." {
					displayPath = filepath.ToSlash(rootRel)
				} else {
					displayPath = filepath.ToSlash(absPath)
				}
			}
			sum := sha256.Sum256(data)
			files = append(files, discovered{
				source: RepoInstructionSource{
					Path:        absPath,
					DisplayPath: filepath.ToSlash(displayPath),
					SizeBytes:   int64(len(data)),
					SHA256:      hex.EncodeToString(sum[:]),
				},
				data: data,
			})
		}
	}

	var buf strings.Builder
	sources := make([]RepoInstructionSource, len(files))
	totalSourceBytes := int64(0)
	truncated := false
	for i, file := range files {
		source := file.source
		totalSourceBytes += source.SizeBytes
		normalized := normalizeInstructionText(file.data)
		entry := fmt.Sprintf("----- BEGIN %s -----\n%s\n----- END %s -----\n\n", source.DisplayPath, normalized, source.DisplayPath)
		remaining := maxBytes - buf.Len()
		if remaining <= 0 {
			source.Truncated = true
			truncated = true
			sources[i] = source
			continue
		}
		if len(entry) > remaining {
			buf.WriteString(entry[:remaining])
			source.IncludedBytes = remaining
			source.Truncated = true
			truncated = true
			sources[i] = source
			continue
		}
		buf.WriteString(entry)
		source.IncludedBytes = len(entry)
		sources[i] = source
	}

	text := strings.TrimSpace(buf.String())
	metadata := RepoInstructionsMetadata{
		SchemaVersion:    ArtifactSchemaVersion,
		Enabled:          true,
		MaxBytes:         maxBytes,
		GeneratedAt:      generatedAt,
		SourceCount:      len(sources),
		TotalSourceBytes: totalSourceBytes,
		BundleBytes:      len([]byte(text)),
		Truncated:        truncated,
		Sources:          sources,
	}
	return repoInstructionsBundle{Text: text, Metadata: metadata}, nil
}

func repoInstructionBases(ctx context.Context, workdir string) []string {
	bases := []string{workdir}
	if out, err := runCommand(ctx, workdir, "git", "rev-parse", "--show-toplevel"); err == nil {
		root := strings.TrimSpace(out)
		if root != "" {
			if absRoot, absErr := filepath.Abs(root); absErr == nil {
				root = absRoot
			}
			if root != workdir {
				bases = append(bases, root)
			}
		}
	}
	return bases
}

func normalizeInstructionText(data []byte) string {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.TrimSpace(text)
}

func runCaps(opts RunOptions) RunCaps {
	return RunCaps{
		MaxFindings:        opts.MaxFindings,
		MaxOutputBytes:     opts.MaxOutputBytes,
		TimeoutSeconds:     int64(opts.Timeout.Seconds()),
		MaxWallTimeSeconds: int64(opts.MaxWallTime.Seconds()),
		MaxRoleInvocations: opts.MaxRoleInvocations,
	}
}

func writeRunState(runDir string, state RunState) error {
	if runDir == "" {
		return nil
	}
	state.SchemaVersion = ArtifactSchemaVersion
	state.UpdatedAt = time.Now().UTC()
	return writeJSONWithNewline(filepath.Join(runDir, "state.json"), state)
}

func newExecutionPlanFromWorkPlan(runID string, mode Mode, workPlan WorkPlan, source string) *ExecutionPlan {
	now := time.Now().UTC()
	plan := &ExecutionPlan{
		SchemaVersion: 1,
		RunID:         runID,
		Mode:          mode,
		Status:        "running",
		Summary:       workPlan.Summary,
		Items:         make([]PlanItem, 0, len(workPlan.Packages)),
		Events:        []PlanEvent{},
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if source == "" {
		source = "supervisor-initial"
	}
	for _, pkg := range workPlan.Packages {
		status := PlanStatusPending
		if pkg.ID == workPlan.SelectedPackage {
			status = PlanStatusInProgress
		}
		plan.Items = append(plan.Items, PlanItem{
			ID:           pkg.ID,
			Title:        pkg.Title,
			Status:       status,
			Owner:        "worker",
			Source:       source,
			Reason:       pkg.Goal,
			AllowedScope: append([]string{}, pkg.AllowedScope...),
			Acceptance:   append([]string{}, pkg.Acceptance...),
			Validation:   append([]string{}, pkg.Validation...),
			CreatedAt:    now,
			UpdatedAt:    now,
		})
		plan.Events = append(plan.Events, PlanEvent{
			Type:    "item_created",
			ItemID:  pkg.ID,
			By:      source,
			At:      now,
			To:      status,
			Message: pkg.Title,
		})
		if status == PlanStatusInProgress {
			plan.Events = append(plan.Events, PlanEvent{
				Type:    "item_started",
				ItemID:  pkg.ID,
				By:      "runner",
				At:      now,
				To:      PlanStatusInProgress,
				Message: "selected package started",
			})
		}
	}
	return plan
}

func setPlanItemStatus(plan *ExecutionPlan, itemID string, status PlanStatus, by, message string) bool {
	if plan == nil || itemID == "" {
		return false
	}
	now := time.Now().UTC()
	if status == PlanStatusInProgress {
		for _, item := range plan.Items {
			if item.ID != itemID && item.Status == PlanStatusInProgress {
				return false
			}
		}
	}
	for i := range plan.Items {
		if plan.Items[i].ID != itemID {
			continue
		}
		from := plan.Items[i].Status
		if from == status {
			return false
		}
		plan.Items[i].Status = status
		plan.Items[i].UpdatedAt = now
		plan.UpdatedAt = now
		plan.Events = append(plan.Events, PlanEvent{
			Type:    "item_status_changed",
			ItemID:  itemID,
			By:      by,
			At:      now,
			From:    from,
			To:      status,
			Message: message,
		})
		return true
	}
	return false
}

func deferRemainingPlanItems(plan *ExecutionPlan, selectedID, by, message string) {
	if plan == nil {
		return
	}
	for _, item := range plan.Items {
		if item.ID == selectedID || item.Status != PlanStatusPending {
			continue
		}
		setPlanItemStatus(plan, item.ID, PlanStatusDeferred, by, message)
	}
}

func appendReviewFindingPlanItems(plan *ExecutionPlan, review Review, round int) {
	if plan == nil {
		return
	}
	source := fmt.Sprintf("supervisor-review-round-%d", round)
	now := time.Now().UTC()
	for i, finding := range review.Findings {
		if finding.Severity != "blocker" && finding.Severity != "major" {
			continue
		}
		id := fmt.Sprintf("R%d-F%d", round, i+1)
		if planItemExists(plan, id) {
			continue
		}
		title := strings.TrimSpace(finding.Issue)
		if title == "" {
			title = fmt.Sprintf("%s finding", finding.Severity)
		}
		item := PlanItem{
			ID:         id,
			Title:      title,
			Status:     PlanStatusNeedsArbitration,
			Owner:      "supervisor",
			Source:     source,
			Reason:     strings.TrimSpace(finding.Fix),
			Acceptance: []string{strings.TrimSpace(finding.Fix)},
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		if finding.File != "" {
			item.AllowedScope = []string{finding.File}
		}
		plan.Items = append(plan.Items, item)
		plan.Events = append(plan.Events, PlanEvent{
			Type:    "item_added",
			ItemID:  id,
			By:      source,
			At:      now,
			To:      PlanStatusNeedsArbitration,
			Message: title,
		})
		plan.UpdatedAt = now
	}
}

func planItemExists(plan *ExecutionPlan, id string) bool {
	for _, item := range plan.Items {
		if item.ID == id {
			return true
		}
	}
	return false
}

func finalizeExecutionPlan(plan *ExecutionPlan, exitCode int) {
	if plan == nil {
		return
	}
	if exitCode == ExitSuccess {
		plan.Status = "passed"
	} else {
		plan.Status = "failed"
	}
	plan.UpdatedAt = time.Now().UTC()
	plan.Events = append(plan.Events, PlanEvent{
		Type:    "plan_finished",
		By:      "runner",
		At:      plan.UpdatedAt,
		Message: fmt.Sprintf("exit=%d", exitCode),
	})
}

func summarizeExecutionPlan(runDir string, plan *ExecutionPlan) *PlanSummary {
	if plan == nil {
		return nil
	}
	summary := &PlanSummary{
		Path:   filepath.Join(runDir, "plan.json"),
		Status: plan.Status,
		Total:  len(plan.Items),
	}
	for _, item := range plan.Items {
		switch item.Status {
		case PlanStatusPending:
			summary.Pending++
		case PlanStatusInProgress:
			summary.InProgress++
		case PlanStatusBlocked:
			summary.Blocked++
		case PlanStatusPassed:
			summary.Passed++
		case PlanStatusFailed:
			summary.Failed++
		case PlanStatusSkipped:
			summary.Skipped++
		case PlanStatusDeferred:
			summary.Deferred++
		case PlanStatusNeedsArbitration:
			summary.Arbitration++
		}
	}
	return summary
}

func persistExecutionPlan(runDir string, plan *ExecutionPlan) error {
	if plan == nil {
		return nil
	}
	if err := writeJSONWithNewline(filepath.Join(runDir, "plan.json"), plan); err != nil {
		return err
	}
	var events bytes.Buffer
	for _, event := range plan.Events {
		data, err := json.Marshal(event)
		if err != nil {
			return err
		}
		events.Write(data)
		events.WriteByte('\n')
	}
	return os.WriteFile(filepath.Join(runDir, "plan-events.jsonl"), events.Bytes(), 0o644)
}

func (a *App) Run(ctx context.Context, opts RunOptions) (FinalRun, error) {
	normalizeDefaultMode(&opts)
	if strings.TrimSpace(opts.Prompt) == "" {
		return FinalRun{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("prompt is required")}
	}
	if err := validateRunRoles(opts); err != nil {
		return FinalRun{}, err
	}
	if opts.Mode == ModeSolo {
		return a.runSolo(ctx, opts)
	}
	return a.runLoop(ctx, opts, nil)
}

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
	runDir, err := createRunDir(opts.Workdir, runID)
	if err != nil {
		return FinalRun{}, &ExitError{Code: ExitAdapterFailure, Err: err}
	}
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
		setRoleStatus(&final, reviewerLabel, opts.Adversary, "failed", classifyRoleFailure(reviewerLabel, err), err.Error())
		setFinalBlocking(&final, classifyRoleFailure(reviewerLabel, err), err.Error())
		applyInvocationBudget(&final, budget)
		finalizeRunState(&final)
		_ = writeRunState(runDir, RunState{RunID: runID, Mode: opts.Mode, Status: string(final.Status), Phase: "review", Degraded: final.Degraded, DegradedReason: final.DegradedReason, BlockingReason: final.BlockingReason, RoleStatuses: final.RoleStatuses, CurrentRound: 1, LatestDiffPath: final.LatestDiffPath, LatestReviewPath: final.LatestReviewPath, ExitCode: final.ExitCode})
		_ = a.persistFinal(opts.Workdir, final)
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
	_ = writeRunState(runDir, RunState{
		RunID:            runID,
		Mode:             opts.Mode,
		Status:           "running",
		Phase:            "review",
		RoleStatuses:     final.RoleStatuses,
		CurrentRound:     1,
		LatestDiffPath:   final.LatestDiffPath,
		LatestReviewPath: final.LatestReviewPath,
	})
	schemaPath := filepath.Join(runDir, "review-schema.json")
	if err = os.WriteFile(schemaPath, []byte(ReviewSchema), 0o644); err != nil {
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
	review, cost, outputPath, err := a.runAdversary(ctx, opts, 1, runDir, schemaPath, prompt, baseline, diffArtifact.Patch, diffArtifact.PatchPath, "", "", nil, RelayContext{}, repoInstructions, &final)
	if err != nil {
		return final, err
	}
	final.Verdict = review.Verdict
	final.Summary = review.Summary
	final.ExitCode = ExitSuccess
	final.RoundsCompleted = 1
	final.LatestReviewPath = outputPath
	final.Review = review
	final.Costs = map[string]float64{reviewerLabel: cost}
	final.FinishedAt = time.Now().UTC()
	if opts.FailOnReview && review.HasBlockingFindings() {
		final.ExitCode = ExitBlockingFindings
	}
	if final.ExitCode == ExitSuccess && review.OnlyMinorOrNit() && len(review.Findings) > 0 {
		final.DegradedReason = "review_passed_with_nonblocking_findings"
		final.Degraded = true
	}
	setRoleStatus(&final, reviewerLabel, opts.Adversary, "completed", "", "")
	applyInvocationBudget(&final, budget)
	finalizeRunState(&final)
	_ = writeRunState(runDir, RunState{RunID: runID, Mode: opts.Mode, Status: string(final.Status), Phase: "review", Degraded: final.Degraded, DegradedReason: final.DegradedReason, BlockingReason: final.BlockingReason, RoleStatuses: final.RoleStatuses, CurrentRound: 1, LatestDiffPath: final.LatestDiffPath, LatestReviewPath: final.LatestReviewPath, ExitCode: final.ExitCode})
	if err := a.persistFinal(opts.Workdir, final); err != nil {
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
	for _, key := range []string{"codex", "codex-oss", "claude", "agy", "gosling", "openai-compatible"} {
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

func (a *App) runSolo(ctx context.Context, opts RunOptions) (FinalRun, error) {
	if opts.MaxWallTime > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.MaxWallTime)
		defer cancel()
	}
	editorLabel, _ := roleLabels(opts.Mode)
	runID := newRunID()
	logProgress(opts, "run %s preflight started workdir=%s", runID, opts.Workdir)
	baseline, cleanup, err := preflight(opts, runID)
	if err != nil {
		return FinalRun{}, err
	}
	if cleanup != nil {
		defer cleanup()
	}
	runDir, err := createRunDir(opts.Workdir, runID)
	if err != nil {
		return FinalRun{}, &ExitError{Code: ExitAdapterFailure, Err: err}
	}
	activateRun(opts.Workdir, runID, runDir, opts.Mode)
	runCompleted := false
	defer func() { deactivateRun(opts.Workdir, runID, runCompleted) }()
	if err := writeRedactedBytes(filepath.Join(runDir, "input.md"), []byte(opts.Prompt), opts.EnvOverlay); err != nil {
		return FinalRun{}, err
	}
	repoInstructions, err := loadAndPersistRepoInstructions(ctx, opts, runDir)
	if err != nil {
		return FinalRun{}, err
	}
	meta := Meta{
		SchemaVersion: ArtifactSchemaVersion,
		RunID:         runID,
		Workdir:       opts.Workdir,
		Baseline:      baseline,
		Command:       "run",
		Prompt:        redactSecretsWithOverlay(opts.Prompt, opts.EnvOverlay),
		StartedAt:     time.Now().UTC(),
		Adapters:      map[string]string{editorLabel: opts.Coder.Adapter},
		Models:        map[string]string{editorLabel: opts.Coder.Model},
		ConfigSources: opts.ConfigSources,
	}
	if err := writeJSON(filepath.Join(runDir, "meta.json"), meta); err != nil {
		return FinalRun{}, err
	}
	logProgress(opts, "run %s started mode=%s baseline=%s run-dir=%s", runID, opts.Mode, baseline, runDir)

	registry := Registry(a.Config, opts)
	editor, ok := registry[opts.Coder.Adapter]
	if !ok {
		return FinalRun{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("unknown %s adapter %q", editorLabel, opts.Coder.Adapter)}
	}
	if err := checkAdapters(ctx, editor); err != nil {
		return FinalRun{}, err
	}

	final := FinalRun{
		SchemaVersion:   ArtifactSchemaVersion,
		RunID:           runID,
		RunDir:          runDir,
		Workdir:         opts.Workdir,
		Baseline:        baseline,
		Mode:            opts.Mode,
		Coder:           opts.Coder,
		Verdict:         "done",
		RoundsRequested: 1,
		Caps:            runCaps(opts),
		Costs:           map[string]float64{},
		Adapters:        meta.Adapters,
		Models:          meta.Models,
		StartedAt:       meta.StartedAt,
	}
	initFinalState(&final, opts)
	budget := &InvocationBudget{Max: opts.MaxRoleInvocations}
	opts.InvocationBudget = budget
	setRoleStatus(&final, editorLabel, opts.Coder, "running", "", "")
	_ = writeRunState(runDir, RunState{
		RunID:        runID,
		Mode:         opts.Mode,
		Status:       "running",
		Phase:        "solo",
		RoleStatuses: final.RoleStatuses,
		CurrentRound: 1,
	})

	logProgress(opts, "solo implementation started adapter=%s", editor.ID())
	outputPath := filepath.Join(runDir, "solo-round-1.md")
	editorResult, err := a.runAdapter(ctx, editor, RoleCoder, Request{
		Context:      ctx,
		Prompt:       withRepoInstructions(BuildSoloPrompt(opts.Workdir, opts.Prompt), repoInstructions),
		SystemPrompt: "",
		EnvOverlay:   opts.EnvOverlay,
		Model:        opts.Coder.Model,
		Workdir:      opts.Workdir,
		RunDir:       runDir,
		OutputPath:   outputPath,
		Timeout:      opts.Timeout,
		Phase:        fmt.Sprintf("solo %s", editor.ID()),
		Quiet:        opts.Quiet,
		Verbose:      opts.Verbose,
		Budget:       opts.InvocationBudget,
	}, opts.DryRun)
	if err != nil {
		reason := classifyRoleFailure(editorLabel, err)
		setRoleStatus(&final, editorLabel, opts.Coder, "failed", reason, err.Error())
		_ = writeRunState(runDir, RunState{
			RunID:          runID,
			Mode:           opts.Mode,
			Status:         "failed",
			Phase:          "solo",
			BlockingReason: string(reason),
			RoleStatuses:   final.RoleStatuses,
			CurrentRound:   1,
		})
		return final, err
	}
	setRoleStatus(&final, editorLabel, opts.Coder, "completed", "", "")
	final.Costs[editorLabel] += editorResult.CostUSD
	final.Summary = strings.TrimSpace(editorResult.Text)
	final.RoundsCompleted = 1
	_ = writeRunState(runDir, RunState{
		RunID:        runID,
		Mode:         opts.Mode,
		Status:       "running",
		Phase:        "diff",
		RoleStatuses: final.RoleStatuses,
		CurrentRound: 1,
	})
	logProgress(opts, "solo implementation completed output=%s", outputPath)

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
	logProgress(opts, "solo diff captured bytes=%d path=%s", len(diffArtifact.Patch), diffArtifact.PatchPath)

	if opts.TestCmd != "" && !opts.NoTest {
		testPath := filepath.Join(runDir, "test-round-1.txt")
		logProgress(opts, "solo tests started command=%q", opts.TestCmd)
		testRun, err := runTestCommand(ctx, opts.Workdir, opts.TestCmd, opts.Timeout, testPath, opts.DryRun, opts.EnvOverlay, opts.MaxOutputBytes)
		if err != nil {
			return final, err
		}
		final.Tests = append(final.Tests, testRun)
		if testRun.Passed {
			logProgress(opts, "solo tests passed output=%s", testPath)
		} else {
			logProgress(opts, "solo tests failed output=%s", testPath)
		}
	}

	if final.Summary == "" {
		final.Summary = "Solo implementation completed; review was not run."
	} else {
		final.Summary = strings.TrimSpace(final.Summary + "\n\nReview was not run in solo mode.")
	}
	final.FinishedAt = time.Now().UTC()
	final.ExitCode = a.computeExitCode(final)
	applyInvocationBudget(&final, budget)
	finalizeRunState(&final)
	logProgress(opts, "run %s finished mode=solo exit=%d", runID, final.ExitCode)
	_ = writeRunState(runDir, RunState{RunID: runID, Mode: opts.Mode, Status: string(final.Status), Phase: "solo", Degraded: final.Degraded, DegradedReason: final.DegradedReason, BlockingReason: final.BlockingReason, RoleStatuses: final.RoleStatuses, CurrentRound: 1, LatestDiffPath: final.LatestDiffPath, ExitCode: final.ExitCode})
	if err := a.persistFinal(opts.Workdir, final); err != nil {
		return final, err
	}
	// See the equivalent comment in Review: only mark the active-run pointer
	// completed once persistFinal has actually succeeded.
	runCompleted = true
	if final.ExitCode != ExitSuccess {
		return final, &ExitError{Code: final.ExitCode, Err: fmt.Errorf("run completed with exit code %d", final.ExitCode)}
	}
	return final, nil
}

func (a *App) resolveOrchestrationDecision(ctx context.Context, opts RunOptions, runDir string, editor, reviewer Adapter) (OrchestrationDecision, Mode) {
	decision := newOrchestrationDecision(filepath.Base(runDir), opts.Mode)
	if opts.DryRun {
		decision.HostReason = "dry-run keeps initial mode"
		return decision, opts.Mode
	}
	switch opts.Mode {
	case ModeRelay:
		advisory, err := a.collectOrchestrationAdvisory(ctx, opts, reviewer, "supervisor", opts.Mode, "", runDir)
		if err != nil {
			markOrchestrationDecisionDegraded(&decision, err.Error())
			return decision, opts.Mode
		}
		return decision, applyRelaySimplificationPolicy(&decision, advisory)
	case ModeSupervisor:
		worker, err := a.collectOrchestrationAdvisory(ctx, opts, editor, "worker", opts.Mode, "", runDir)
		if err != nil {
			markOrchestrationDecisionDegraded(&decision, err.Error())
			return decision, opts.Mode
		}
		if worker.Recommendation != "escalate" || worker.TargetMode != ModeRelay {
			decision.Advisories = append(decision.Advisories, worker)
			decision.FinalMode = ModeSupervisor
			decision.Status = "kept"
			decision.HostReason = "worker did not request relay escalation"
			return decision, ModeSupervisor
		}
		supervisor, err := a.collectOrchestrationAdvisory(ctx, opts, reviewer, "supervisor", opts.Mode, worker.Reason, runDir)
		if err != nil {
			decision.Advisories = append(decision.Advisories, worker)
			markOrchestrationDecisionDegraded(&decision, err.Error())
			return decision, opts.Mode
		}
		return decision, applySupervisorEscalationPolicy(&decision, worker, supervisor)
	default:
		decision.HostReason = "mode does not support advisory transition"
		return decision, opts.Mode
	}
}

func (a *App) collectOrchestrationAdvisory(ctx context.Context, opts RunOptions, adapter Adapter, source string, current Mode, brief string, runDir string) (OrchestrationAdvisory, error) {
	schemaPath := filepath.Join(runDir, "orchestration-advisory-schema.json")
	if err := os.WriteFile(schemaPath, []byte(OrchestrationAdvisorySchema), 0o644); err != nil {
		return OrchestrationAdvisory{}, err
	}
	outputPath := filepath.Join(runDir, fmt.Sprintf("orchestration-%s-advisory.json", sanitizeArtifactName(source)))
	var prompt string
	if source == "worker" {
		prompt = BuildWorkerOrchestrationAdvisoryPrompt(opts.Workdir, opts.Prompt, current, brief)
	} else {
		prompt = BuildSupervisorOrchestrationAdvisoryPrompt(opts.Workdir, opts.Prompt, current)
	}
	if !adapter.Capabilities().SupportsSchema {
		prompt += "\n\nJSON schema:\n" + OrchestrationAdvisorySchema
	}
	result, err := a.runAdapter(ctx, adapter, RoleSupervisor, Request{
		Context:    ctx,
		Prompt:     prompt,
		EnvOverlay: opts.EnvOverlay,
		Model:      advisoryModel(opts, source),
		Workdir:    opts.Workdir,
		RunDir:     runDir,
		OutputPath: outputPath,
		SchemaPath: schemaPath,
		Timeout:    opts.Timeout,
		Phase:      fmt.Sprintf("orchestration advisory %s %s", source, adapter.ID()),
		Quiet:      opts.Quiet,
		Verbose:    opts.Verbose,
		Budget:     opts.InvocationBudget,
	}, false)
	if err != nil {
		return OrchestrationAdvisory{}, err
	}
	advisory, err := parseOrchestrationAdvisory([]byte(result.Text), source)
	if err != nil {
		if repaired, _, attempted, repairErr := a.repairJSONWithWorker(ctx, opts, Registry(a.Config, opts), runDir, outputPath, "orchestration advisory", OrchestrationAdvisorySchema, []byte(result.Text), err); repairErr != nil {
			_ = writeRedactedBytes(outputPath+".repair-failed.txt", []byte(repairErr.Error()+"\n"), opts.EnvOverlay)
		} else if attempted {
			repairedAdvisory, parseErr := parseOrchestrationAdvisory(repaired, source)
			if parseErr == nil {
				advisory = repairedAdvisory
			} else {
				_ = writeRedactedBytes(outputPath+".repair-validation-error.txt", []byte(parseErr.Error()+"\n"), opts.EnvOverlay)
				return OrchestrationAdvisory{}, err
			}
		} else {
			return OrchestrationAdvisory{}, err
		}
	}
	if err := writeJSONWithNewline(outputPath, advisory); err != nil {
		return OrchestrationAdvisory{}, err
	}
	return advisory, nil
}

func advisoryModel(opts RunOptions, source string) string {
	if source == "worker" {
		return opts.Coder.Model
	}
	return opts.Adversary.Model
}

func (a *App) runLoop(ctx context.Context, opts RunOptions, initialReview *Review) (final FinalRun, err error) {
	if opts.MaxWallTime > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.MaxWallTime)
		defer cancel()
	}
	editorLabel, reviewerLabel := roleLabels(opts.Mode)
	runID := newRunID()
	logProgress(opts, "run %s preflight started workdir=%s", runID, opts.Workdir)
	baseline, cleanup, err := preflight(opts, runID)
	if err != nil {
		return FinalRun{}, err
	}
	if cleanup != nil {
		defer cleanup()
	}
	runDir, err := createRunDir(opts.Workdir, runID)
	if err != nil {
		return FinalRun{}, &ExitError{Code: ExitAdapterFailure, Err: err}
	}
	activateRun(opts.Workdir, runID, runDir, opts.Mode)
	runCompleted := false
	defer func() { deactivateRun(opts.Workdir, runID, runCompleted) }()
	if err := writeRedactedBytes(filepath.Join(runDir, "input.md"), []byte(opts.Prompt), opts.EnvOverlay); err != nil {
		return FinalRun{}, err
	}
	repoInstructions, err := loadAndPersistRepoInstructions(ctx, opts, runDir)
	if err != nil {
		return FinalRun{}, err
	}
	schemaPath := filepath.Join(runDir, "review-schema.json")
	if err := os.WriteFile(schemaPath, []byte(ReviewSchema), 0o644); err != nil {
		return FinalRun{}, err
	}
	meta := Meta{
		SchemaVersion: ArtifactSchemaVersion,
		RunID:         runID,
		Workdir:       opts.Workdir,
		Baseline:      baseline,
		Command:       "run",
		Prompt:        redactSecretsWithOverlay(opts.Prompt, opts.EnvOverlay),
		StartedAt:     time.Now().UTC(),
		Adapters:      map[string]string{editorLabel: opts.Coder.Adapter, reviewerLabel: opts.Adversary.Adapter},
		Models:        map[string]string{editorLabel: opts.Coder.Model, reviewerLabel: opts.Adversary.Model},
		ConfigSources: opts.ConfigSources,
	}
	if opts.Mode == ModeRelay {
		meta.Adapters["scout"] = opts.Scout.Adapter
		meta.Models["scout"] = opts.Scout.Model
	}
	if err := writeJSON(filepath.Join(runDir, "meta.json"), meta); err != nil {
		return FinalRun{}, err
	}
	final = FinalRun{
		SchemaVersion:     ArtifactSchemaVersion,
		RunID:             runID,
		RunDir:            runDir,
		Workdir:           opts.Workdir,
		Baseline:          baseline,
		Mode:              opts.Mode,
		Coder:             opts.Coder,
		Adversary:         opts.Adversary,
		Scout:             opts.Scout,
		SupervisorCanEdit: opts.SupervisorCanEdit,
		RoundsRequested:   opts.Rounds,
		Caps:              runCaps(opts),
		Costs:             map[string]float64{},
		Adapters:          meta.Adapters,
		Models:            meta.Models,
		StartedAt:         meta.StartedAt,
	}
	initFinalState(&final, opts)
	budget := &InvocationBudget{Max: opts.MaxRoleInvocations}
	opts.InvocationBudget = budget
	// currentRole tracks the role whose work is in flight so the deferred
	// failure handler can classify the blocking reason correctly. final.Phase is
	// a human-facing serialized field and stays "preflight" until the round loop
	// sets it, so it is not a reliable classifier key for early failures.
	currentRole := editorLabel
	defer func() {
		if err == nil || final.RunID == "" {
			return
		}
		if !final.FinishedAt.IsZero() {
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
		if final.FinishedAt.IsZero() {
			final.FinishedAt = time.Now().UTC()
		}
		setFinalBlocking(&final, classifyRoleFailure(currentRole, err), err.Error())
		applyInvocationBudget(&final, budget)
		finalizeRunState(&final)
		_ = writeRunState(runDir, RunState{RunID: runID, Mode: opts.Mode, Status: string(final.Status), Phase: "error", Degraded: final.Degraded, DegradedReason: final.DegradedReason, BlockingReason: final.BlockingReason, RoleStatuses: final.RoleStatuses, CurrentRound: final.RoundsCompleted, LatestDiffPath: final.LatestDiffPath, LatestReviewPath: final.LatestReviewPath, ExitCode: final.ExitCode})
		_ = a.persistFinal(opts.Workdir, final)
	}()
	_ = writeRunState(runDir, RunState{RunID: runID, Mode: opts.Mode, Status: "running", Phase: "preflight"})
	logProgress(opts, "run %s started mode=%s baseline=%s run-dir=%s", runID, opts.Mode, baseline, runDir)
	logJSONRepairPolicy(opts)

	registry := Registry(a.Config, opts)
	editor, ok := registry[opts.Coder.Adapter]
	if !ok {
		return final, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("unknown %s adapter %q", editorLabel, opts.Coder.Adapter)}
	}
	reviewer, ok := registry[opts.Adversary.Adapter]
	if !ok {
		return final, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("unknown %s adapter %q", reviewerLabel, opts.Adversary.Adapter)}
	}
	var scout Adapter
	scoutAvailable := true
	if opts.Mode == ModeRelay {
		var scoutOK bool
		scout, scoutOK = registry[opts.Scout.Adapter]
		if !scoutOK {
			return final, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("unknown scout adapter %q", opts.Scout.Adapter)}
		}
	}
	if err := checkAdapters(ctx, editor); err != nil {
		setRoleStatus(&final, editorLabel, opts.Coder, "failed", classifyRoleFailure(editorLabel, err), err.Error())
		return final, err
	}
	setRoleStatus(&final, editorLabel, opts.Coder, "ready", "", "")
	reviewerRoleForPolicy := reviewerLabel
	currentRole = reviewerRoleForPolicy
	opts.Adversary, reviewer, err = selectRunnableRoleAdapter(ctx, registry, reviewerRoleForPolicy, opts.Adversary, fallbackTargetsForRole(opts, reviewerRoleForPolicy, opts.Adversary), lossPolicyForRole(opts, reviewerRoleForPolicy), &final)
	if err != nil {
		setFinalBlocking(&final, classifyRoleFailure(reviewerRoleForPolicy, err), err.Error())
		return final, err
	}
	meta.Adapters[reviewerLabel] = opts.Adversary.Adapter
	meta.Models[reviewerLabel] = opts.Adversary.Model
	final.Adversary = opts.Adversary
	final.Adapters = meta.Adapters
	final.Models = meta.Models

	var brief string
	var relay RelayContext
	var workPlan *WorkPlan
	var selectedPackage *WorkPackage
	var executionPlan *ExecutionPlan
	if initialReview == nil && (opts.Mode == ModeSupervisor || opts.Mode == ModeRelay) {
		decision, effectiveMode := a.resolveOrchestrationDecision(ctx, opts, runDir, editor, reviewer)
		normalizeOrchestrationDecision(&decision)
		if err := writeJSONWithNewline(filepath.Join(runDir, orchestrationDecisionArtifact), decision); err != nil {
			return final, &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("write orchestration decision artifact: %w", err)}
		}
		if effectiveMode != opts.Mode {
			logProgress(opts, "orchestration transition applied %s -> %s reason=%q", opts.Mode, effectiveMode, decision.HostReason)
			previousEditorLabel := editorLabel
			opts.Mode = effectiveMode
			editorLabel, reviewerLabel = roleLabels(opts.Mode)
			if opts.Mode == ModeRelay && opts.Scout.Adapter == "" {
				relayTargets := configuredTargetsForMode(a.Config.Defaults, ModeRelay)
				opts.Scout, err = ParseRoleTarget(relayTargets.Scout)
				if err != nil {
					return final, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("resolve escalated relay scout target: %w", err)}
				}
				opts.Fallbacks.Scout = normalizeFallbackTargets(opts.Fallbacks.Scout, opts.Scout)
			}
			renameRoleStatus(&final, previousEditorLabel, editorLabel)
			meta.Adapters = map[string]string{editorLabel: opts.Coder.Adapter, reviewerLabel: opts.Adversary.Adapter}
			meta.Models = map[string]string{editorLabel: opts.Coder.Model, reviewerLabel: opts.Adversary.Model}
			if opts.Mode == ModeRelay {
				meta.Adapters["scout"] = opts.Scout.Adapter
				meta.Models["scout"] = opts.Scout.Model
			}
			if err := writeJSON(filepath.Join(runDir, "meta.json"), meta); err != nil {
				return final, err
			}
			final.Mode = opts.Mode
			final.Adapters = meta.Adapters
			final.Models = meta.Models
		}
		if opts.Mode == ModeRelay {
			var scoutOK bool
			scout, scoutOK = registry[opts.Scout.Adapter]
			if !scoutOK {
				return final, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("unknown scout adapter %q", opts.Scout.Adapter)}
			}
			var scoutErr error
			opts.Scout, scout, scoutErr = selectRunnableRoleAdapter(ctx, registry, "scout", opts.Scout, fallbackTargetsForRole(opts, "scout", opts.Scout), opts.LossPolicy.Scout, &final)
			if scoutErr != nil {
				setRoleStatus(&final, "scout", opts.Scout, "failed", ReasonScoutUnavailable, scoutErr.Error())
				appendRoleLoss(&final, "scout", opts.LossPolicy.Scout, "preflight", "degraded", ReasonScoutUnavailable, scoutErr.Error())
				if policyBlocks(opts.LossPolicy.Scout) {
					setFinalBlocking(&final, ReasonScoutUnavailable, scoutErr.Error())
					return final, scoutErr
				}
				setFinalDegraded(&final, ReasonScoutUnavailable, "scout unavailable; continuing without scout context")
				scoutAvailable = false
			} else {
				meta.Adapters["scout"] = opts.Scout.Adapter
				meta.Models["scout"] = opts.Scout.Model
				final.Scout = opts.Scout
				final.Adapters = meta.Adapters
				final.Models = meta.Models
			}
		}
	} else if opts.Mode == ModeRelay {
		var scoutErr error
		opts.Scout, scout, scoutErr = selectRunnableRoleAdapter(ctx, registry, "scout", opts.Scout, fallbackTargetsForRole(opts, "scout", opts.Scout), opts.LossPolicy.Scout, &final)
		if scoutErr != nil {
			setRoleStatus(&final, "scout", opts.Scout, "failed", ReasonScoutUnavailable, scoutErr.Error())
			appendRoleLoss(&final, "scout", opts.LossPolicy.Scout, "preflight", "degraded", ReasonScoutUnavailable, scoutErr.Error())
			if policyBlocks(opts.LossPolicy.Scout) {
				setFinalBlocking(&final, ReasonScoutUnavailable, scoutErr.Error())
				return final, scoutErr
			}
			setFinalDegraded(&final, ReasonScoutUnavailable, "scout unavailable; continuing without scout context")
			scoutAvailable = false
		} else {
			meta.Adapters["scout"] = opts.Scout.Adapter
			meta.Models["scout"] = opts.Scout.Model
			final.Scout = opts.Scout
			final.Adapters = meta.Adapters
			final.Models = meta.Models
		}
	}
	if opts.Mode == ModeSupervisor && initialReview == nil {
		if opts.SupervisorSlicing {
			logProgress(opts, "supervisor slicing started adapter=%s max-packages=%d", reviewer.ID(), opts.MaxPackages)
			planOutputPath := filepath.Join(runDir, "supervisor-work-plan.json")
			planSchemaPath := filepath.Join(runDir, "work-plan-schema.json")
			var plan WorkPlan
			var planCost float64
			planParsed := false
			if opts.DryRun {
				plan = syntheticWorkPlan(opts.Prompt, opts.Package)
				planParsed = true
			} else {
				if err := os.WriteFile(planSchemaPath, []byte(WorkPlanSchema), 0o644); err != nil {
					return final, err
				}
				planPrompt := withRepoInstructions(BuildSupervisorWorkPlanPrompt(opts.Workdir, opts.Prompt, opts.MaxPackages, opts.Package), repoInstructions)
				if !reviewer.Capabilities().SupportsSchema {
					planPrompt += "\n\nJSON schema:\n" + WorkPlanSchema
				}
				planResult, err := a.runAdapter(ctx, reviewer, RoleSupervisor, Request{
					Context:    ctx,
					Prompt:     planPrompt,
					EnvOverlay: opts.EnvOverlay,
					Model:      opts.Adversary.Model,
					Workdir:    opts.Workdir,
					RunDir:     runDir,
					OutputPath: planOutputPath,
					SchemaPath: planSchemaPath,
					Timeout:    opts.Timeout,
					Phase:      fmt.Sprintf("supervisor slicing %s", reviewer.ID()),
					Quiet:      opts.Quiet,
					Verbose:    opts.Verbose,
					Budget:     opts.InvocationBudget,
				}, false)
				if err != nil {
					if repaired, repairCost, attempted, repairErr := a.repairJSONWithWorker(ctx, opts, registry, runDir, planOutputPath, "supervisor work plan", WorkPlanSchema, readRepairSource(planOutputPath, nil), err); repairErr != nil {
						_ = writeRedactedBytes(planOutputPath+".repair-failed.txt", []byte(repairErr.Error()+"\n"), opts.EnvOverlay)
					} else if attempted {
						repairedPlan, parseErr := parseWorkPlan(repaired, opts.Package, opts.MaxPackages)
						if parseErr == nil {
							setFinalDegraded(&final, ReasonJSONRepairUsed, "supervisor work-plan JSON repaired by worker")
							appendRoleLoss(&final, reviewerLabel, lossPolicyForRole(opts, reviewerLabel), "json-repair", "repaired", ReasonJSONRepairUsed, "worker repaired invalid work-plan JSON")
							plan = repairedPlan
							planCost += repairCost
							planParsed = true
						}
						_ = writeRedactedBytes(planOutputPath+".repair-validation-error.txt", []byte(parseErr.Error()+"\n"), opts.EnvOverlay)
					}
					return final, err
				}
				if !planParsed {
					parsed, err := parseWorkPlan([]byte(planResult.Text), opts.Package, opts.MaxPackages)
					if err != nil {
						if repaired, repairCost, attempted, repairErr := a.repairJSONWithWorker(ctx, opts, registry, runDir, planOutputPath, "supervisor work plan", WorkPlanSchema, []byte(planResult.Text), err); repairErr != nil {
							_ = writeRedactedBytes(planOutputPath+".repair-failed.txt", []byte(repairErr.Error()+"\n"), opts.EnvOverlay)
						} else if attempted {
							repairedPlan, parseErr := parseWorkPlan(repaired, opts.Package, opts.MaxPackages)
							if parseErr == nil {
								setFinalDegraded(&final, ReasonJSONRepairUsed, "supervisor work-plan JSON repaired by worker")
								appendRoleLoss(&final, reviewerLabel, lossPolicyForRole(opts, reviewerLabel), "json-repair", "repaired", ReasonJSONRepairUsed, "worker repaired invalid work-plan JSON")
								parsed = repairedPlan
								planCost += repairCost
							} else {
								_ = writeRedactedBytes(planOutputPath+".repair-validation-error.txt", []byte(parseErr.Error()+"\n"), opts.EnvOverlay)
								return final, err
							}
						} else {
							return final, err
						}
					}
					plan = parsed
					planCost += planResult.CostUSD
				}
			}
			pkg, ok := plan.Selected()
			if !ok {
				return final, &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("supervisor work plan has no selected package")}
			}
			if err := writeJSONWithNewline(planOutputPath, plan); err != nil {
				return final, err
			}
			final.Costs[reviewerLabel] += planCost
			workPlan = &plan
			selectedPackage = &pkg
			relay.WorkPlan = workPlan
			relay.WorkPackage = selectedPackage
			final.WorkPlan = workPlan
			final.SelectedPackage = selectedPackage
			final.RemainingPackages = plan.RemainingPackageTitles()
			executionPlan = newExecutionPlanFromWorkPlan(runID, opts.Mode, plan, "supervisor-initial")
			if err := persistExecutionPlan(runDir, executionPlan); err != nil {
				return final, err
			}
			final.Plan = summarizeExecutionPlan(runDir, executionPlan)
			brief = BuildWorkPackageBrief(plan, pkg)
			briefOutputPath := filepath.Join(runDir, "supervisor-brief.md")
			if err := os.WriteFile(briefOutputPath, []byte(brief), 0o644); err != nil {
				return final, err
			}
			logProgress(opts, "supervisor sliced task into %d packages; executing %s: %s", len(plan.Packages), pkg.ID, pkg.Title)
		} else {
			logProgress(opts, "supervisor brief started adapter=%s", reviewer.ID())
			briefOutputPath := filepath.Join(runDir, "supervisor-brief.md")
			briefResult, err := a.runAdapter(ctx, reviewer, supervisorBriefRole(opts.SupervisorCanEdit), Request{
				Context:    ctx,
				Prompt:     withRepoInstructions(BuildSupervisorBriefPrompt(opts.Workdir, opts.Prompt, opts.SupervisorCanEdit), repoInstructions),
				EnvOverlay: opts.EnvOverlay,
				Model:      opts.Adversary.Model,
				Workdir:    opts.Workdir,
				RunDir:     runDir,
				OutputPath: briefOutputPath,
				Timeout:    opts.Timeout,
				Phase:      fmt.Sprintf("supervisor brief %s", reviewer.ID()),
				Quiet:      opts.Quiet,
				Verbose:    opts.Verbose,
				Budget:     opts.InvocationBudget,
			}, opts.DryRun)
			if err != nil {
				return final, err
			}
			final.Costs[reviewerLabel] += briefResult.CostUSD
			brief = briefResult.Text
			logProgress(opts, "supervisor brief completed output=%s", briefOutputPath)
		}
	}
	if opts.Mode == ModeRelay && initialReview == nil {
		scoutOutputPath := filepath.Join(runDir, "scout-round-1.json")
		scoutStatusPath := filepath.Join(runDir, "scout-execution-round-1.json")
		scoutStatus := newScoutExecutionArtifact(opts.ScoutMode, opts.ScoutFailurePolicy, opts.ScoutRetrieval && opts.ScoutMode == "recon")
		skipScout := !scoutAvailable
		retrievalContext := ""
		var retrieval RetrievalArtifact
		if opts.ScoutRetrieval && opts.ScoutMode == "recon" && scoutAvailable {
			logProgress(opts, "scout retrieval started")
			var err error
			retrieval, err = runScoutRetrieval(ctx, opts.Workdir, opts.Prompt, runDir, true)
			if err != nil {
				return final, &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("write retrieval artifact: %w", err)}
			}
			retrievalContext = CompactRetrievalForPrompt(retrieval)
			scoutStatus.RetrievalRan = true
			scoutStatus.RetrievalStatus = retrieval.Status
			scoutStatus.RetrievalDegraded = retrievalStatusIsDegraded(retrieval.Status)
			logProgress(opts, "scout retrieval completed status=%s evidence=%d", retrieval.Status, len(retrieval.Evidence))
		}
		scoutPrompt := withRepoInstructions(BuildScoutPrompt(opts.Workdir, opts.Prompt, "", opts.ScoutMode, "pre", "", "", retrievalContext), repoInstructions)
		if opts.ScoutMode == "recon" {
			contextBudgetPath := filepath.Join(runDir, "scout-context-round-1.json")
			limit := scoutContextLimitForAdapter(a.Config, opts.Scout.Adapter)
			contextBudget := estimateScoutPromptBudget(scoutPrompt, limit)
			contextBudget.Adapter = opts.Scout.Adapter
			contextBudget.Model = opts.Scout.Model
			if contextBudget.Status == scoutContextStatusNearLimit && retrievalContext != "" {
				logProgress(opts, "scout context near configured limit; compacting retrieval estimated=%d usable=%d", contextBudget.EstimatedInputTokens, contextBudget.UsableContextTokens)
				compacted := CompactRetrievalForPromptAggressive(retrieval)
				if compacted != "" && len(compacted) < len(retrievalContext) {
					retrievalContext = compacted
					scoutPrompt = withRepoInstructions(BuildScoutPrompt(opts.Workdir, opts.Prompt, "", opts.ScoutMode, "pre", "", "", retrievalContext), repoInstructions)
					contextBudget = estimateScoutPromptBudget(scoutPrompt, limit)
					contextBudget.Adapter = opts.Scout.Adapter
					contextBudget.Model = opts.Scout.Model
					contextBudget.RetrievalCompacted = true
				}
			}
			if contextBudget.Status == scoutContextStatusExceeds && retrievalContext != "" {
				logProgress(opts, "scout context exceeds configured limit; disabling retrieval estimated=%d usable=%d", contextBudget.EstimatedInputTokens, contextBudget.UsableContextTokens)
				retrievalContext = ""
				scoutPrompt = withRepoInstructions(BuildScoutPrompt(opts.Workdir, opts.Prompt, "", opts.ScoutMode, "pre", "", "", ""), repoInstructions)
				contextBudget = estimateScoutPromptBudget(scoutPrompt, limit)
				contextBudget.Adapter = opts.Scout.Adapter
				contextBudget.Model = opts.Scout.Model
				contextBudget.RetrievalDisabledDueBudget = true
				scoutStatus.RetrievalDisabledByBudget = true
			}
			if contextBudget.Status == scoutContextStatusNearLimit {
				logProgress(opts, "scout context near configured limit estimated=%d usable=%d", contextBudget.EstimatedInputTokens, contextBudget.UsableContextTokens)
				if opts.ScoutContextPolicy == "skip" {
					setFinalDegraded(&final, ReasonScoutContextTooSmall, "scout context near configured limit; skipping scout")
					appendRoleLoss(&final, "scout", opts.LossPolicy.Scout, "context-budget", "degraded", ReasonScoutContextTooSmall, "near configured scout context limit")
					scoutStatus.ContinuedWithoutScoutContext = true
					skipScout = true
				}
				if opts.ScoutContextPolicy == "block" {
					err := &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("scout context near configured limit and scout_context_policy=block")}
					setFinalBlocking(&final, ReasonScoutContextTooSmall, err.Error())
					_ = writeJSONWithNewline(contextBudgetPath, contextBudget)
					_ = writeJSONWithNewline(scoutStatusPath, scoutStatus)
					return final, err
				}
			}
			if err := writeJSONWithNewline(contextBudgetPath, contextBudget); err != nil {
				return final, &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("write scout context artifact: %w", err)}
			}
			if contextBudget.Status == scoutContextStatusExceeds {
				budgetErr := invalidScoutContextBudgetError(contextBudget)
				scoutStatus.FailureClass = scoutFailureClassContextBudget
				scoutStatus.Failure = budgetErr.Error()
				if opts.ScoutContextPolicy == "block" || policyBlocks(opts.LossPolicy.Scout) {
					setFinalBlocking(&final, ReasonScoutContextTooSmall, budgetErr.Error())
					_ = writeJSONWithNewline(scoutStatusPath, scoutStatus)
					return final, &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("scout failed and scout_failure_policy=fail; aborting relay run: %w", budgetErr)}
				}
				setFinalDegraded(&final, ReasonScoutContextTooSmall, "scout context too small; continuing without scout context")
				appendRoleLoss(&final, "scout", opts.LossPolicy.Scout, "context-budget", "degraded", ReasonScoutContextTooSmall, budgetErr.Error())
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
				Context:    ctx,
				Prompt:     scoutPrompt,
				EnvOverlay: opts.EnvOverlay,
				Model:      opts.Scout.Model,
				Workdir:    opts.Workdir,
				RunDir:     runDir,
				OutputPath: scoutOutputPath,
				Timeout:    opts.Timeout,
				Phase:      fmt.Sprintf("scout %s %s", opts.ScoutMode, scout.ID()),
				Quiet:      opts.Quiet,
				Verbose:    opts.Verbose,
				Budget:     opts.InvocationBudget,
			}, opts.DryRun)
			if err != nil && IsOutputContractError(err) {
				if repaired, repairCost, attempted, repairErr := a.repairJSONWithWorker(ctx, opts, registry, runDir, scoutOutputPath, "scout", ScoutSchemaForRepair(), readRepairSource(scoutOutputPath, nil), err); repairErr != nil {
					_ = writeRedactedBytes(scoutOutputPath+".repair-failed.txt", []byte(repairErr.Error()+"\n"), opts.EnvOverlay)
				} else if attempted {
					repairedScout, parseErr := parseScout(repaired)
					if parseErr == nil {
						setFinalDegraded(&final, ReasonJSONRepairUsed, "scout JSON repaired by worker")
						appendRoleLoss(&final, "scout", opts.LossPolicy.Scout, "json-repair", "repaired", ReasonJSONRepairUsed, "worker repaired invalid scout JSON")
						scoutResult = Result{Scout: repairedScout, Text: repairedScout.Summary, CostUSD: repairCost}
						err = nil
					} else {
						_ = writeRedactedBytes(scoutOutputPath+".repair-validation-error.txt", []byte(parseErr.Error()+"\n"), opts.EnvOverlay)
					}
				}
			}
			if err != nil {
				scoutStatus.FailureClass = classifyScoutFailure(err)
				scoutStatus.Failure = err.Error()
				if policyBlocks(opts.LossPolicy.Scout) {
					_ = writeJSONWithNewline(scoutStatusPath, scoutStatus)
					return final, &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("scout failed and scout_failure_policy=fail; aborting relay run: %w", err)}
				}
				setFinalDegraded(&final, ReasonScoutUnavailable, "scout failed; continuing without scout context")
				appendRoleLoss(&final, "scout", opts.LossPolicy.Scout, "invoke", "degraded", classifyRoleFailure("scout", err), err.Error())
				scoutStatus.ContinuedWithoutScoutContext = true
				logProgress(opts, "scout failed; continuing without scout context error=%q", err.Error())
			} else {
				scoutStatus.ScoutSucceeded = true
				setRoleStatus(&final, "scout", opts.Scout, "completed", "", "")
				if scoutResult.Scout != nil {
					if retrievalContext != "" && scoutResult.Scout.RetrievalStatus == "" {
						var retrieval RetrievalArtifact
						if err := json.Unmarshal([]byte(retrievalContext), &retrieval); err == nil {
							scoutResult.Scout.RetrievalQueries = append([]string{}, retrieval.Queries...)
							scoutResult.Scout.Evidence = retrievalScoutEvidence(retrieval.Evidence)
							scoutResult.Scout.RetrievalStatus = retrieval.Status
							scoutResult.Scout.RetrievalTruncated = retrieval.Truncated
						}
					}
					relay.Scout = *scoutResult.Scout
				}
				final.Costs["scout"] += scoutResult.CostUSD
				logProgress(opts, "scout %s completed output=%s", opts.ScoutMode, scoutOutputPath)
			}
		}
		if err := writeJSONWithNewline(scoutStatusPath, scoutStatus); err != nil {
			return final, &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("write scout execution artifact: %w", err)}
		}

		logProgress(opts, "supervisor brief started adapter=%s", reviewer.ID())
		briefOutputPath := filepath.Join(runDir, "supervisor-brief.md")
		briefResult, err := a.runAdapter(ctx, reviewer, supervisorBriefRole(opts.SupervisorCanEdit), Request{
			Context:    ctx,
			Prompt:     withRepoInstructions(BuildSupervisorBriefPrompt(opts.Workdir, opts.Prompt, opts.SupervisorCanEdit), repoInstructions),
			EnvOverlay: opts.EnvOverlay,
			Model:      opts.Adversary.Model,
			Workdir:    opts.Workdir,
			RunDir:     runDir,
			OutputPath: briefOutputPath,
			Timeout:    opts.Timeout,
			Phase:      fmt.Sprintf("supervisor brief %s", reviewer.ID()),
			Quiet:      opts.Quiet,
			Verbose:    opts.Verbose,
			Budget:     opts.InvocationBudget,
		}, opts.DryRun)
		if err != nil {
			return final, err
		}
		final.Costs[reviewerLabel] += briefResult.CostUSD
		brief = briefResult.Text
		relay.Brief = brief
		logProgress(opts, "supervisor brief completed output=%s", briefOutputPath)

		instructionsPath := filepath.Join(runDir, "supervisor-instructions.md")
		logProgress(opts, "supervisor relay instructions started adapter=%s", reviewer.ID())
		instructionsResult, err := a.runAdapter(ctx, reviewer, RoleSupervisor, Request{
			Context:    ctx,
			Prompt:     withRepoInstructions(BuildRelaySupervisorInstructionsPrompt(opts.Prompt, brief, relay.Scout), repoInstructions),
			EnvOverlay: opts.EnvOverlay,
			Model:      opts.Adversary.Model,
			Workdir:    opts.Workdir,
			RunDir:     runDir,
			OutputPath: instructionsPath,
			Timeout:    opts.Timeout,
			Phase:      fmt.Sprintf("relay supervisor instructions %s", reviewer.ID()),
			Quiet:      opts.Quiet,
			Verbose:    opts.Verbose,
			Budget:     opts.InvocationBudget,
		}, opts.DryRun)
		if err != nil {
			return final, err
		}
		relay.Instructions = instructionsResult.Text
		final.Costs[reviewerLabel] += instructionsResult.CostUSD
		logProgress(opts, "supervisor relay instructions completed output=%s", instructionsPath)
	}

	editorSystemPrompt := editorSystemPromptForMode(opts.Mode)

	var sessionID string
	var latestReview Review
	var latestDiff string
	var latestDiffArtifact DiffArtifact
	implementSelectedPackage := initialReview == nil
	for round := 1; round <= opts.Rounds; round++ {
		logProgress(opts, "round %d/%d %s started adapter=%s", round, opts.Rounds, editorLabel, editor.ID())
		final.Phase = editorLabel
		currentRole = editorLabel
		setRoleStatus(&final, editorLabel, opts.Coder, "running", "", "")
		_ = writeRunState(runDir, RunState{
			RunID:            runID,
			Mode:             opts.Mode,
			Status:           "running",
			Phase:            editorLabel,
			RoleStatuses:     final.RoleStatuses,
			CurrentRound:     round,
			LatestDiffPath:   final.LatestDiffPath,
			LatestReviewPath: final.LatestReviewPath,
		})
		var editorPrompt string
		switch {
		case round == 1 && initialReview == nil && opts.Mode == ModeRelay:
			editorPrompt = BuildRelayCoderPrompt(opts.Workdir, opts.Prompt, relay.Brief, relay.Instructions, relay.Scout)
		case implementSelectedPackage && opts.Mode == ModeSupervisor && selectedPackage != nil && workPlan != nil:
			editorPrompt = BuildWorkerPackageImplementPrompt(opts.Workdir, opts.Prompt, *workPlan, *selectedPackage)
		case round == 1 && initialReview == nil && opts.Mode == ModeSupervisor:
			editorPrompt = BuildWorkerImplementPrompt(opts.Workdir, opts.Prompt, brief)
		case round == 1 && initialReview == nil:
			editorPrompt = BuildCoderPrompt(opts.Workdir, opts.Prompt)
		default:
			diff := latestDiff
			if diff == "" {
				patch, err := deterministicDiffPatch(ctx, opts.Workdir, baseline, filepath.Join(runDir, fmt.Sprintf("tmp-prompt-round-%d.index", round)))
				if err != nil {
					return final, err
				}
				diff = string(patch)
			}
			review := latestReview
			if round == 1 && initialReview != nil {
				review = *initialReview
			}
			if opts.Mode == ModeRelay {
				editorPrompt = BuildRelayFixPrompt(round, opts.Prompt, diff, relay.Brief, relay.Instructions, relay.Scout, relay.PostScout, review)
			} else if opts.Mode == ModeSupervisor && selectedPackage != nil {
				editorPrompt = BuildWorkerPackageFixPrompt(round, opts.Prompt, diff, *selectedPackage, review)
			} else if opts.Mode == ModeSupervisor {
				editorPrompt = BuildWorkerFixPrompt(round, opts.Prompt, diff, review)
			} else {
				editorPrompt = BuildFixPrompt(round, opts.Prompt, diff, review)
			}
		}
		editorPrompt = withRepoInstructions(editorPrompt, repoInstructions)
		implementSelectedPackage = false
		editorOutputPath := filepath.Join(runDir, fmt.Sprintf("%s-round-%d.md", editorLabel, round))
		if selectedPackage != nil {
			if setPlanItemStatus(executionPlan, selectedPackage.ID, PlanStatusInProgress, "runner", fmt.Sprintf("round %d %s started", round, editorLabel)) {
				if err := persistExecutionPlan(runDir, executionPlan); err != nil {
					return final, err
				}
			}
		}
		editorResult, err := a.runAdapter(ctx, editor, RoleCoder, Request{
			Context:      ctx,
			Prompt:       editorPrompt,
			SystemPrompt: editorSystemPrompt,
			EnvOverlay:   opts.EnvOverlay,
			Model:        opts.Coder.Model,
			Workdir:      opts.Workdir,
			RunDir:       runDir,
			OutputPath:   editorOutputPath,
			ResumeID:     sessionID,
			Timeout:      opts.Timeout,
			Phase:        fmt.Sprintf("round %d %s %s", round, editorLabel, editor.ID()),
			Quiet:        opts.Quiet,
			Verbose:      opts.Verbose,
			Budget:       opts.InvocationBudget,
		}, opts.DryRun)
		if err != nil {
			setRoleStatus(&final, editorLabel, opts.Coder, "failed", classifyRoleFailure(editorLabel, err), err.Error())
			return final, err
		}
		setRoleStatus(&final, editorLabel, opts.Coder, "completed", "", "")
		logProgress(opts, "round %d %s completed output=%s", round, editorLabel, editorOutputPath)
		final.Costs[editorLabel] += editorResult.CostUSD
		if editorResult.SessionID != "" {
			sessionID = editorResult.SessionID
		}

		diffArtifact, err := captureDiffArtifact(ctx, opts.Workdir, baseline, runDir, round)
		if err != nil {
			return final, err
		}
		diff := diffArtifact.Patch
		latestDiff = diff
		latestDiffArtifact = diffArtifact
		final.LatestDiffPath = diffArtifact.PatchPath
		final.LatestNumstatPath = diffArtifact.NumstatPath
		final.LatestFilesPath = diffArtifact.FilesPath
		final.LatestSHA256Path = diffArtifact.SHA256Path
		final.LatestDiffSHA256 = diffArtifact.Metadata.DiffSHA256
		_ = writeRunState(runDir, RunState{
			RunID:            runID,
			Mode:             opts.Mode,
			Status:           "running",
			Phase:            "diff",
			RoleStatuses:     final.RoleStatuses,
			CurrentRound:     round,
			LatestDiffPath:   final.LatestDiffPath,
			LatestReviewPath: final.LatestReviewPath,
		})
		logProgress(opts, "round %d diff captured bytes=%d path=%s", round, len(diff), diffArtifact.PatchPath)

		testOutput := ""
		if opts.TestCmd != "" && !opts.NoTest {
			testPath := filepath.Join(runDir, fmt.Sprintf("test-round-%d.txt", round))
			logProgress(opts, "round %d tests started command=%q", round, opts.TestCmd)
			testRun, err := runTestCommand(ctx, opts.Workdir, opts.TestCmd, opts.Timeout, testPath, opts.DryRun, opts.EnvOverlay, opts.MaxOutputBytes)
			if err != nil {
				return final, err
			}
			final.Tests = append(final.Tests, testRun)
			testOutput = testRun.Output
			if testRun.Passed {
				logProgress(opts, "round %d tests passed output=%s", round, testPath)
			} else {
				logProgress(opts, "round %d tests failed output=%s", round, testPath)
			}
		}

		if opts.Mode == ModeRelay && scoutAvailable {
			postScoutPath := filepath.Join(runDir, fmt.Sprintf("post-scout-round-%d.json", round))
			postScoutStatusPath := filepath.Join(runDir, fmt.Sprintf("post-scout-execution-round-%d.json", round))
			postScoutStatus := newScoutExecutionArtifact(opts.PostScoutMode, opts.ScoutFailurePolicy, false)
			logProgress(opts, "round %d post-scout %s started adapter=%s", round, opts.PostScoutMode, scout.ID())
			postScoutStatus.ScoutRan = true
			postScoutResult, err := a.runAdapter(ctx, scout, RoleScout, Request{
				Context:    ctx,
				Prompt:     withRepoInstructions(BuildScoutPrompt(opts.Workdir, opts.Prompt, relay.Brief, opts.PostScoutMode, "post", diff, safeTestOutput(testOutput), ""), repoInstructions),
				EnvOverlay: opts.EnvOverlay,
				Model:      opts.Scout.Model,
				Workdir:    opts.Workdir,
				RunDir:     runDir,
				OutputPath: postScoutPath,
				Timeout:    opts.Timeout,
				Phase:      fmt.Sprintf("round %d post-scout %s %s", round, opts.PostScoutMode, scout.ID()),
				Quiet:      opts.Quiet,
				Verbose:    opts.Verbose,
				Budget:     opts.InvocationBudget,
			}, opts.DryRun)
			if err != nil && IsOutputContractError(err) {
				if repaired, repairCost, attempted, repairErr := a.repairJSONWithWorker(ctx, opts, registry, runDir, postScoutPath, "post-scout", ScoutSchemaForRepair(), readRepairSource(postScoutPath, nil), err); repairErr != nil {
					_ = writeRedactedBytes(postScoutPath+".repair-failed.txt", []byte(repairErr.Error()+"\n"), opts.EnvOverlay)
				} else if attempted {
					repairedScout, parseErr := parseScout(repaired)
					if parseErr == nil {
						setFinalDegraded(&final, ReasonJSONRepairUsed, "post-scout JSON repaired by worker")
						appendRoleLoss(&final, "scout", opts.LossPolicy.Scout, "json-repair", "repaired", ReasonJSONRepairUsed, "worker repaired invalid post-scout JSON")
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
					return final, &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("post-scout failed and scout_failure_policy=fail; aborting relay run: %w", err)}
				}
				setFinalDegraded(&final, ReasonScoutUnavailable, "post-scout failed; continuing without post-scout context")
				appendRoleLoss(&final, "scout", opts.LossPolicy.Scout, "post-scout", "degraded", classifyRoleFailure("scout", err), err.Error())
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
				return final, &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("write post-scout execution artifact: %w", err)}
			}
		}

		logProgress(opts, "round %d %s started adapter=%s", round, reviewerLabel, reviewer.ID())
		final.Phase = reviewerLabel
		currentRole = reviewerLabel
		setRoleStatus(&final, reviewerLabel, opts.Adversary, "running", "", "")
		_ = writeRunState(runDir, RunState{
			RunID:            runID,
			Mode:             opts.Mode,
			Status:           "running",
			Phase:            reviewerLabel,
			RoleStatuses:     final.RoleStatuses,
			CurrentRound:     round,
			LatestDiffPath:   final.LatestDiffPath,
			LatestReviewPath: final.LatestReviewPath,
		})
		var priorReview *Review
		if latestReview.Verdict != "" {
			priorReview = &latestReview
		}
		review, cost, reviewPath, err := a.runAdversary(ctx, opts, round, runDir, schemaPath, opts.Prompt, baseline, diff, diffArtifact.PatchPath, testOutput, editorOutputPath, priorReview, relay, repoInstructions, &final)
		if err != nil {
			reason := classifyRoleFailure(reviewerLabel, err)
			setRoleStatus(&final, reviewerLabel, opts.Adversary, "failed", reason, err.Error())
			setFinalBlocking(&final, reason, err.Error())
			return final, err
		}
		setRoleStatus(&final, reviewerLabel, opts.Adversary, "completed", "", "")
		appendReviewFindingPlanItems(executionPlan, *review, round)
		logProgress(opts, "round %d %s completed verdict=%s findings=%d output=%s", round, reviewerLabel, review.Verdict, len(review.Findings), reviewPath)
		final.Costs[reviewerLabel] += cost
		final.RoundsCompleted = round
		final.Review = review
		final.LatestReviewPath = reviewPath
		_ = writeRunState(runDir, RunState{
			RunID:            runID,
			Mode:             opts.Mode,
			Status:           "running",
			Phase:            "review-complete",
			RoleStatuses:     final.RoleStatuses,
			CurrentRound:     round,
			LatestDiffPath:   final.LatestDiffPath,
			LatestReviewPath: final.LatestReviewPath,
		})
		latestReview = *review

		if review.Verdict == "pass" {
			if selectedPackage != nil {
				setPlanItemStatus(executionPlan, selectedPackage.ID, PlanStatusPassed, reviewerLabel, fmt.Sprintf("round %d review passed", round))
			}
			if opts.Mode == ModeSupervisor && opts.AutoNextPackage && workPlan != nil && selectedPackage != nil {
				nextPackage, ok := nextWorkPackage(*workPlan, selectedPackage.ID)
				if ok && round < opts.Rounds {
					workPlan.SelectedPackage = nextPackage.ID
					selectedPackage = &nextPackage
					relay.WorkPlan = workPlan
					relay.WorkPackage = selectedPackage
					final.WorkPlan = workPlan
					final.SelectedPackage = selectedPackage
					final.RemainingPackages = workPlan.RemainingPackageTitles()
					brief = BuildWorkPackageBrief(*workPlan, nextPackage)
					implementSelectedPackage = true
					if err := persistExecutionPlan(runDir, executionPlan); err != nil {
						return final, err
					}
					logProgress(opts, "package %s passed; continuing to %s: %s", reviewPackageID(final.SelectedPackage), nextPackage.ID, nextPackage.Title)
					continue
				}
			}
			if selectedPackage != nil && !opts.AutoNextPackage {
				deferRemainingPlanItems(executionPlan, selectedPackage.ID, "runner", "remaining packages not run without --auto-next-package")
			}
			if err := persistExecutionPlan(runDir, executionPlan); err != nil {
				return final, err
			}
			final.Verdict = "pass"
			final.Summary = review.Summary
			if len(final.RemainingPackages) > 0 {
				final.Summary = appendRemainingPackagesSummary(final.Summary, final.RemainingPackages)
			}
			break
		}
		if review.OnlyMinorOrNit() {
			if selectedPackage != nil {
				setPlanItemStatus(executionPlan, selectedPackage.ID, PlanStatusPassed, reviewerLabel, fmt.Sprintf("round %d review had only minor/nit findings", round))
			}
			if selectedPackage != nil && !opts.AutoNextPackage {
				deferRemainingPlanItems(executionPlan, selectedPackage.ID, "runner", "remaining packages not run without --auto-next-package")
			}
			if err := persistExecutionPlan(runDir, executionPlan); err != nil {
				return final, err
			}
			final.Verdict = review.Verdict
			final.Summary = review.Summary
			if len(final.RemainingPackages) > 0 {
				final.Summary = appendRemainingPackagesSummary(final.Summary, final.RemainingPackages)
			}
			break
		}
		if round == opts.Rounds {
			if selectedPackage != nil {
				setPlanItemStatus(executionPlan, selectedPackage.ID, PlanStatusFailed, reviewerLabel, fmt.Sprintf("round limit reached with %s review", review.Verdict))
			}
			if err := persistExecutionPlan(runDir, executionPlan); err != nil {
				return final, err
			}
			final.Verdict = review.Verdict
			final.Summary = review.Summary
			final.RoundLimitReached = true
			logProgress(opts, "round limit reached after %d rounds; collecting final reports", opts.Rounds)
			reports, reportCosts := a.collectRoundLimitReports(ctx, opts, runDir, baseline, diff, *review, final.Tests, repoInstructions)
			final.RoundLimitReports = reports
			for role, cost := range reportCosts {
				final.Costs[role] += cost
			}
			final.Summary = strings.TrimSpace(final.Summary + "\n\nRound limit reached; no more edits were requested. Final reports were collected from both agents when available.")
		}
	}

	final.ChangedFiles = latestDiffArtifact.ChangedFiles()
	final.FinishedAt = time.Now().UTC()
	final.ExitCode = a.computeExitCode(final)
	if final.ExitCode == ExitSuccess && final.Review != nil && final.Review.OnlyMinorOrNit() && len(final.Review.Findings) > 0 {
		final.DegradedReason = "review_passed_with_nonblocking_findings"
		final.Degraded = true
	}
	if final.ExitCode == ExitTestsFailed {
		setFinalBlocking(&final, ReasonTestFailed, "latest test command failed")
	}
	if final.RoundLimitReached {
		setFinalBlocking(&final, ReasonRoundsExhausted, "round limit reached")
	}
	applyInvocationBudget(&final, budget)
	finalizeRunState(&final)
	if executionPlan != nil {
		if final.ExitCode == ExitTestsFailed && selectedPackage != nil {
			setPlanItemStatus(executionPlan, selectedPackage.ID, PlanStatusFailed, "runner", "latest test command failed")
		}
		finalizeExecutionPlan(executionPlan, final.ExitCode)
		if err := persistExecutionPlan(runDir, executionPlan); err != nil {
			return final, err
		}
		final.Plan = summarizeExecutionPlan(runDir, executionPlan)
	}
	logProgress(opts, "run %s finished verdict=%s exit=%d rounds=%d/%d", runID, final.Verdict, final.ExitCode, final.RoundsCompleted, final.RoundsRequested)
	_ = writeRunState(runDir, RunState{RunID: runID, Mode: opts.Mode, Status: string(final.Status), Phase: "final", Degraded: final.Degraded, DegradedReason: final.DegradedReason, BlockingReason: final.BlockingReason, RoleStatuses: final.RoleStatuses, CurrentRound: final.RoundsCompleted, LatestDiffPath: final.LatestDiffPath, LatestReviewPath: final.LatestReviewPath, ExitCode: final.ExitCode})
	if err := a.persistFinal(opts.Workdir, final); err != nil {
		return final, err
	}
	// See the equivalent comment in Review: only mark the active-run pointer
	// completed once every artifact this function persists (execution plan,
	// then final.json/latest.json) has actually been written.
	runCompleted = true
	if final.ExitCode != ExitSuccess {
		return final, &ExitError{Code: final.ExitCode, Err: fmt.Errorf("run completed with exit code %d", final.ExitCode)}
	}
	return final, nil
}

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
			Context:    ctx,
			Prompt:     prompt,
			EnvOverlay: opts.EnvOverlay,
			Model:      target.model,
			Workdir:    opts.Workdir,
			RunDir:     runDir,
			OutputPath: reportPath,
			Timeout:    opts.Timeout,
			Phase:      fmt.Sprintf("round-limit %s %s", target.label, adapter.ID()),
			Quiet:      opts.Quiet,
			Verbose:    opts.Verbose,
			Budget:     opts.InvocationBudget,
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
	_ = os.WriteFile(path, []byte(text), 0o644)
}

func diffWithBaselineHeader(baseline, diff string) string {
	if strings.TrimSpace(baseline) == "" {
		return diff
	}
	return fmt.Sprintf("Baseline: %s\n\n%s", baseline, diff)
}

func parseWorkPlan(raw []byte, requestedPackage string, maxPackages int) (WorkPlan, error) {
	var plan WorkPlan
	if err := json.Unmarshal(raw, &plan); err != nil {
		extracted, extractErr := extractJSONObject(raw)
		if extractErr != nil {
			return WorkPlan{}, &OutputContractError{Err: fmt.Errorf("decode work plan JSON: %w", err)}
		}
		if err := json.Unmarshal(extracted, &plan); err != nil {
			return WorkPlan{}, &OutputContractError{Err: fmt.Errorf("decode work plan JSON: %w", err)}
		}
	}
	if err := validateWorkPlan(&plan, requestedPackage, maxPackages); err != nil {
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
				ID:           selected,
				Title:        "Dry-run package",
				Goal:         strings.TrimSpace(prompt),
				AllowedScope: []string{"."},
				Acceptance:   []string{"dry-run"},
				Validation:   []string{"dry-run"},
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
	if err := os.MkdirAll(filepath.Dir(artifactBase), 0o755); err != nil {
		return nil, 0, true, err
	}
	promptPath := artifactBase + ".repair-prompt.md"
	outputPath := artifactBase + ".repaired.json"
	prompt := BuildWorkerJSONRepairPrompt(contractName, schema, errString(validationErr), raw)
	if err := writeRedactedBytes(promptPath, []byte(prompt), opts.EnvOverlay); err != nil {
		return nil, 0, true, err
	}
	logProgress(opts, "worker JSON repair started contract=%s adapter=%s output=%s", contractName, worker.ID(), outputPath)
	result, err := a.runAdapter(ctx, worker, RoleReporter, Request{
		Context:        ctx,
		Prompt:         prompt,
		EnvOverlay:     opts.EnvOverlay,
		Model:          opts.Coder.Model,
		Workdir:        opts.Workdir,
		RunDir:         runDir,
		OutputPath:     outputPath,
		Timeout:        opts.Timeout,
		MaxOutputBytes: opts.MaxOutputBytes,
		Phase:          fmt.Sprintf("worker JSON repair %s %s", contractName, worker.ID()),
		Quiet:          opts.Quiet,
		Verbose:        opts.Verbose,
		Budget:         opts.InvocationBudget,
	}, opts.DryRun)
	if err != nil {
		_ = writeRedactedBytes(artifactBase+".repair-failed.txt", []byte(err.Error()+"\n"), opts.EnvOverlay)
		return nil, 0, true, err
	}
	repaired := bytes.TrimSpace(result.Raw)
	if len(repaired) == 0 {
		repaired = []byte(strings.TrimSpace(result.Text))
	}
	if len(repaired) == 0 && fileExists(outputPath) {
		fileBytes, readErr := os.ReadFile(outputPath)
		if readErr != nil {
			return nil, result.CostUSD, true, readErr
		}
		repaired = bytes.TrimSpace(fileBytes)
	}
	if err := writeRedactedBytes(outputPath, repaired, opts.EnvOverlay); err != nil {
		return nil, result.CostUSD, true, err
	}
	logProgress(opts, "worker JSON repair completed contract=%s output=%s", contractName, outputPath)
	return repaired, result.CostUSD, true, nil
}

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

func readRepairSource(outputPath string, fallback []byte) []byte {
	if strings.TrimSpace(outputPath) != "" && fileExists(outputPath) {
		if raw, err := os.ReadFile(outputPath); err == nil && len(bytes.TrimSpace(raw)) > 0 {
			return raw
		}
	}
	return fallback
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
		reviewPrompt = withRepoInstructions(reviewPrompt, repoInstructions)
		if memory := loadDecisionMemory(opts); memory != "" {
			reviewPrompt = strings.TrimSpace(reviewPrompt) + "\n\n" + memory
		}
		if !adapter.Capabilities().SupportsSchema {
			reviewPrompt += "\n\nJSON schema:\n" + ReviewSchema
		}
		req := Request{
			Context:        ctx,
			Prompt:         reviewPrompt,
			EnvOverlay:     opts.EnvOverlay,
			Model:          target.Model,
			Workdir:        opts.Workdir,
			RunDir:         runDir,
			OutputPath:     outputPath,
			SchemaPath:     schemaPath,
			Passthrough:    opts.ClaudeArgs,
			Timeout:        opts.Timeout,
			MaxOutputBytes: opts.MaxOutputBytes,
			Phase:          fmt.Sprintf("round %d %s %s", round, reviewerLabel, roleTargetString(target)),
			InputMode:      input.Mode,
			Quiet:          opts.Quiet,
			Verbose:        opts.Verbose,
			Budget:         opts.InvocationBudget,
		}
		req.Stdin = input.Stdin
		result, err := a.runAdapter(ctx, adapter, RoleAdversary, req, opts.DryRun)
		if err != nil {
			if !IsOutputContractError(err) {
				return nil, 0, "", err
			}
			logProgress(opts, "round %d %s output invalid; retrying once error=%q", round, reviewerLabel, err.Error())
			req.Prompt = req.Prompt + "\n\nValidation error from the previous response:\n" + err.Error() + "\n\nReturn JSON exactly matching the schema."
			if req.OutputPath != "" {
				_ = writeRedactedBytes(req.OutputPath+".retry-prompt.md", []byte(req.Prompt), req.EnvOverlay)
			}
			result, err = a.runAdapter(ctx, adapter, RoleAdversary, req, opts.DryRun)
			if err != nil {
				if IsOutputContractError(err) {
					if repaired, repairCost, attempted, repairErr := a.repairJSONWithWorker(ctx, opts, Registry(a.Config, opts), runDir, req.OutputPath, "review", ReviewSchema, readRepairSource(req.OutputPath, result.Raw), err); repairErr != nil {
						_ = writeRedactedBytes(req.OutputPath+".repair-failed.txt", []byte(repairErr.Error()+"\n"), req.EnvOverlay)
					} else if attempted {
						review, parseErr := parseReviewPayload(repaired)
						if parseErr == nil {
							if final != nil {
								setFinalDegraded(final, ReasonJSONRepairUsed, "review JSON repaired by worker")
								appendRoleLoss(final, reviewerLabel, lossPolicyForRole(opts, reviewerLabel), "json-repair", "repaired", ReasonJSONRepairUsed, "worker repaired invalid review JSON")
							}
							if req.OutputPath != "" {
								_ = writeJSONWithNewline(req.OutputPath+".parsed.json", review)
							}
							return review, repairCost, outputPath, nil
						}
						_ = writeRedactedBytes(req.OutputPath+".repair-validation-error.txt", []byte(parseErr.Error()+"\n"), req.EnvOverlay)
					}
					if req.OutputPath != "" {
						_ = writeJSONWithNewline(req.OutputPath+".invalid.json", map[string]any{
							"schema_version": ArtifactSchemaVersion,
							"role":           string(RoleAdversary),
							"adapter":        adapter.ID(),
							"model":          target.Model,
							"error":          err.Error(),
							"recorded_at":    time.Now().UTC(),
						})
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
	if review.SchemaVersion == 0 {
		review.SchemaVersion = ArtifactSchemaVersion
	}
	if review.Findings == nil {
		review.Findings = []Finding{}
	}
	if review.TestSuggestions == nil {
		review.TestSuggestions = []string{}
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
	return 2 * 1024 * 1024
}

func startDeliveryRecord(adapter Adapter, role Role, req Request, dryRun bool, spec *CommandSpec) (DeliveryRecord, string, error) {
	started := time.Now().UTC()
	record := DeliveryRecord{
		SchemaVersion: ArtifactSchemaVersion,
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
	name := fmt.Sprintf("%s-%s-%s", started.Format("20060102T150405.000000000Z"), sanitizeArtifactName(string(role)), sanitizeArtifactName(adapter.ID()))
	promptPath := filepath.Join(dir, name+".prompt.md")
	if err := writeRedactedBytes(promptPath, []byte(req.Prompt), req.EnvOverlay); err != nil {
		return record, "", err
	}
	record.PromptPath = promptPath
	return record, filepath.Join(dir, name+".json"), nil
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

func finishDeliveryRecord(path string, record DeliveryRecord, status string, err error) {
	if path == "" {
		return
	}
	record.FinishedAt = time.Now().UTC()
	record.Status = status
	if err != nil {
		record.Error = redactSecrets(err.Error())
	}
	_ = writeJSONWithNewline(path, record)
}

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

func writeOutputContractArtifacts(req Request, role Role, result Result, raw []byte) (string, error) {
	if req.OutputPath == "" {
		return "", nil
	}
	rawPath := req.OutputPath + ".raw"
	if err := writeRedactedBytes(rawPath, raw, req.EnvOverlay); err != nil {
		return rawPath, err
	}
	switch role {
	case RoleAdversary:
		if result.Review != nil {
			normalizeReview(result.Review)
			if err := writeJSONWithNewline(req.OutputPath+".parsed.json", result.Review); err != nil {
				return rawPath, err
			}
		}
	case RoleScout:
		if result.Scout != nil {
			normalizeScout(result.Scout)
			if err := writeJSONWithNewline(req.OutputPath+".parsed.json", result.Scout); err != nil {
				return rawPath, err
			}
		}
	}
	return rawPath, nil
}

func writeValidationErrorArtifact(req Request, err error) string {
	if req.OutputPath == "" || err == nil {
		return ""
	}
	path := req.OutputPath + ".validation-error.txt"
	_ = os.WriteFile(path, []byte(redactSecretsWithOverlay(err.Error(), req.EnvOverlay)+"\n"), 0o644)
	return path
}

func (a *App) runAdapter(ctx context.Context, adapter Adapter, role Role, req Request, dryRun bool) (Result, error) {
	if err := req.Budget.Before(string(role), req.Phase); err != nil {
		return Result{}, &ExitError{Code: ExitAdapterFailure, Err: err}
	}
	if direct, ok := adapter.(DirectAdapter); ok {
		spec, err := adapter.BuildCmd(role, req)
		if err != nil {
			return Result{}, &ExitError{Code: ExitInvalidArguments, Err: err}
		}
		record, recordPath, err := startDeliveryRecord(adapter, role, req, dryRun, spec)
		if err != nil {
			return Result{}, err
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
			finishDeliveryRecord(recordPath, record, "dry-run", nil)
			return result, nil
		}
		result, err := direct.RunDirect(role, req)
		if err != nil {
			finishDeliveryRecord(recordPath, record, "failed", err)
			return Result{}, err
		}
		raw := result.Raw
		if len(raw) == 0 {
			raw = []byte(result.Text)
		}
		if int64(len(raw)) > maxOutputBytes(req) {
			err := &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("%s output exceeded max_output_bytes=%d", adapter.ID(), maxOutputBytes(req))}
			finishDeliveryRecord(recordPath, record, "failed", err)
			return Result{}, err
		}
		if req.OutputPath != "" && !fileExists(req.OutputPath) {
			if writeErr := writeRedactedBytes(req.OutputPath, raw, req.EnvOverlay); writeErr != nil {
				finishDeliveryRecord(recordPath, record, "failed", writeErr)
				return Result{}, writeErr
			}
		}
		rawPath, artifactErr := writeOutputContractArtifacts(req, role, result, raw)
		if artifactErr != nil {
			finishDeliveryRecord(recordPath, record, "failed", artifactErr)
			return Result{}, artifactErr
		}
		record.RawOutputPath = rawPath
		if req.OutputPath != "" && (role == RoleAdversary || role == RoleScout) {
			record.ParsedPath = req.OutputPath + ".parsed.json"
		}
		normalizeReview(result.Review)
		normalizeScout(result.Scout)
		finishDeliveryRecord(recordPath, record, "completed", nil)
		return result, nil
	}
	spec, err := adapter.BuildCmd(role, req)
	if err != nil {
		return Result{}, &ExitError{Code: ExitInvalidArguments, Err: err}
	}
	record, recordPath, err := startDeliveryRecord(adapter, role, req, dryRun, spec)
	if err != nil {
		return Result{}, err
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
		finishDeliveryRecord(recordPath, record, "dry-run", nil)
		return result, nil
	}
	phase := req.Phase
	if phase == "" {
		phase = fmt.Sprintf("%s %s", role, adapter.ID())
	}
	runCtx := req.Context
	if runCtx == nil {
		runCtx = ctx
	}
	cancel := func() {}
	if req.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(runCtx, req.Timeout)
	}
	defer cancel()
	cmd := exec.CommandContext(runCtx, spec.Argv[0], spec.Argv[1:]...)
	prepareProcessTree(cmd)
	cmd.Dir = spec.Dir
	cmd.Env = mergeCommandEnvForRole(role, req.EnvOverlay, spec.Env)
	if len(spec.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(spec.Stdin)
	}
	stdout := newBoundedBuffer(maxOutputBytes(req))
	stderr := newBoundedBuffer(maxOutputBytes(req))
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	started := time.Now()
	logRequestProgress(req, "%s process starting output=%s", phase, spec.Output)
	done := make(chan struct{})
	if !req.Quiet {
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					logRequestProgress(req, "%s still running elapsed=%s", phase, shortDuration(time.Since(started)))
				case <-done:
					return
				}
			}
		}()
	}
	if err := cmd.Run(); err != nil {
		close(done)
		if stdout.Exceeded() || stderr.Exceeded() {
			limitErr := &ExitError{Code: ExitAdapterFailure, Err: outputLimitError(adapter.ID(), maxOutputBytes(req))}
			finishDeliveryRecord(recordPath, record, "failed", limitErr)
			return Result{}, limitErr
		}
		msg := redactSecretsWithOverlay(strings.TrimSpace(stderr.String()), req.EnvOverlay)
		if msg == "" {
			msg = redactSecretsWithOverlay(err.Error(), req.EnvOverlay)
		}
		logRequestProgress(req, "%s failed elapsed=%s", phase, shortDuration(time.Since(started)))
		runErr := &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("%s failed: %s", adapter.ID(), msg)}
		finishDeliveryRecord(recordPath, record, "failed", runErr)
		return Result{}, runErr
	}
	close(done)
	logRequestProgress(req, "%s process completed elapsed=%s", phase, shortDuration(time.Since(started)))
	raw := stdout.Bytes()
	if req.OutputPath != "" && fileExists(req.OutputPath) {
		if info, statErr := os.Stat(req.OutputPath); statErr == nil && info.Size() > maxOutputBytes(req) {
			err := &ExitError{Code: ExitAdapterFailure, Err: outputLimitError(adapter.ID(), maxOutputBytes(req))}
			finishDeliveryRecord(recordPath, record, "failed", err)
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
		finishDeliveryRecord(recordPath, record, "failed", err)
		return Result{}, err
	}
	if req.OutputPath != "" && !fileExists(req.OutputPath) {
		if writeErr := writeRedactedBytes(req.OutputPath, raw, req.EnvOverlay); writeErr != nil {
			finishDeliveryRecord(recordPath, record, "failed", writeErr)
			return Result{}, writeErr
		}
	}
	result, err := adapter.ParseResult(role, raw)
	if err != nil {
		record.ValidationErrorPath = writeValidationErrorArtifact(req, err)
		finishDeliveryRecord(recordPath, record, "failed", err)
		return Result{}, err
	}
	normalizeReview(result.Review)
	normalizeScout(result.Scout)
	if role == RoleScout && req.OutputPath != "" && result.Scout != nil {
		if err := writeJSONWithNewline(req.OutputPath, result.Scout); err != nil {
			finishDeliveryRecord(recordPath, record, "failed", err)
			return Result{}, err
		}
	}
	rawPath, artifactErr := writeOutputContractArtifacts(req, role, result, raw)
	if artifactErr != nil {
		finishDeliveryRecord(recordPath, record, "failed", artifactErr)
		return Result{}, artifactErr
	}
	record.RawOutputPath = rawPath
	if req.OutputPath != "" && (role == RoleAdversary || role == RoleScout) {
		record.ParsedPath = req.OutputPath + ".parsed.json"
	}
	result.Command = spec.Argv
	finishDeliveryRecord(recordPath, record, "completed", nil)
	return result, nil
}

func (a *App) computeExitCode(final FinalRun) int {
	if len(final.Tests) > 0 && !final.Tests[len(final.Tests)-1].Passed {
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
	return writeJSON(filepath.Join(workdir, ".tagteam", "latest.json"), latest)
}

func preflight(opts RunOptions, runID string) (string, func(), error) {
	baseline := opts.Baseline
	if baseline == "" {
		var err error
		baseline, err = ensureGitRepo(opts.Workdir)
		if err != nil {
			return "", nil, err
		}
	}
	if opts.AllowDirty || opts.GitSafety == "allow-dirty" {
		return baseline, nil, nil
	}
	if opts.SkipDirtyCheck {
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
		stashRef, err := gitAutostash(opts.Workdir)
		if err != nil {
			return "", nil, err
		}
		return baseline, func() {
			_, _ = runCommand(context.Background(), opts.Workdir, "git", "stash", "pop", stashRef)
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

func runTestCommand(ctx context.Context, workdir, testCmd string, timeout time.Duration, outputPath string, dryRun bool, envOverlay map[string]string, maxBytes int64) (TestRun, error) {
	if dryRun {
		return TestRun{Command: testCmd, Passed: true, Output: "dry-run"}, nil
	}
	if maxBytes <= 0 {
		maxBytes = 2 * 1024 * 1024
	}
	runCtx := ctx
	cancel := func() {}
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()
	cmd := exec.CommandContext(runCtx, "/bin/sh", "-lc", testCmd)
	prepareProcessTree(cmd)
	cmd.Dir = workdir
	cmd.Env = mergeCommandEnv(envOverlay, nil)
	var out boundedBuffer
	out.limit = maxBytes
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	output := out.Bytes()
	if out.Exceeded() {
		err = outputLimitError("test command", maxBytes)
	}
	testRun := TestRun{
		Command: testCmd,
		Output:  redactSecretsWithOverlay(string(output), envOverlay),
		Passed:  err == nil,
	}
	_ = writeRedactedBytes(outputPath, output, envOverlay)
	return testRun, nil
}

func mergeCommandEnv(overlay map[string]string, extra []string) []string {
	env := os.Environ()
	if len(overlay) > 0 {
		existing := map[string]bool{}
		for _, item := range env {
			key, _, _ := strings.Cut(item, "=")
			existing[key] = true
		}
		keys := make([]string, 0, len(overlay))
		for key := range overlay {
			if existing[key] {
				continue
			}
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			env = append(env, key+"="+overlay[key])
		}
	}
	if len(extra) > 0 {
		env = append(env, extra...)
	}
	return env
}

func mergeCommandEnvForRole(role Role, overlay map[string]string, extra []string) []string {
	if roleAllowsParentEnv(role) {
		return mergeCommandEnv(overlay, extra)
	}
	return mergeRestrictedCommandEnv(overlay, extra)
}

func roleAllowsParentEnv(role Role) bool {
	switch role {
	case RoleCoder:
		return true
	default:
		return false
	}
}

func mergeRestrictedCommandEnv(overlay map[string]string, extra []string) []string {
	allowed := map[string]bool{
		"HOME":          true,
		"LANG":          true,
		"LC_ALL":        true,
		"LOGNAME":       true,
		"PATH":          true,
		"SHELL":         true,
		"SSH_AUTH_SOCK": true,
		"TEMP":          true,
		"TERM":          true,
		"TERM_PROGRAM":  true,
		"TMP":           true,
		"TMPDIR":        true,
		"USER":          true,
	}
	values := map[string]string{}
	for _, item := range os.Environ() {
		key, value, ok := strings.Cut(item, "=")
		if !ok || (!allowed[key] && !isForwardableAuthEnvKey(key)) {
			continue
		}
		values[key] = value
	}
	for key, value := range overlay {
		if _, exists := values[key]; exists {
			continue
		}
		values[key] = value
	}
	for _, item := range extra {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+values[key])
	}
	return env
}

// isForwardableAuthEnvKey reports whether an ambient env var is a provider auth
// credential that non-coder adapter subprocesses need to authenticate. It is
// deliberately narrower than isSensitiveEnvKey (the redaction set): we redact
// broadly but forward narrowly, so only well-known auth key shapes cross the
// restricted-env boundary. Forwarded keys are still scrubbed from artifacts by
// secretValuesFromEnv, which also matches API_KEY/AUTH_TOKEN.
func isForwardableAuthEnvKey(key string) bool {
	upper := strings.ToUpper(strings.TrimSpace(key))
	switch upper {
	case "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY", "ANTHROPIC_AUTH_TOKEN":
		return true
	}
	return strings.HasSuffix(upper, "_API_KEY") || strings.HasSuffix(upper, "_AUTH_TOKEN")
}

func createRunDir(workdir, runID string) (string, error) {
	rootDir := filepath.Join(workdir, ".tagteam")
	if err := os.MkdirAll(filepath.Join(rootDir, "runs"), 0o755); err != nil {
		return "", err
	}
	if err := ensureRunRootIgnore(rootDir); err != nil {
		return "", err
	}
	root := filepath.Join(rootDir, "runs", runID)
	if err := os.Mkdir(root, 0o755); err != nil {
		return "", err
	}
	return root, nil
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func readLatest(workdir string) (LatestRun, error) {
	var latest LatestRun
	data, err := os.ReadFile(filepath.Join(workdir, ".tagteam", "latest.json"))
	if err != nil {
		return LatestRun{}, err
	}
	if err := json.Unmarshal(data, &latest); err != nil {
		return LatestRun{}, err
	}
	return latest, nil
}

func readFinal(path string) (FinalRun, error) {
	var final FinalRun
	data, err := os.ReadFile(path)
	if err != nil {
		return FinalRun{}, err
	}
	if err := json.Unmarshal(data, &final); err != nil {
		return FinalRun{}, err
	}
	return final, nil
}

func readExecutionPlan(runDir string) (ExecutionPlan, error) {
	var plan ExecutionPlan
	data, err := os.ReadFile(filepath.Join(runDir, "plan.json"))
	if err != nil {
		return ExecutionPlan{}, err
	}
	if err := json.Unmarshal(data, &plan); err != nil {
		return ExecutionPlan{}, err
	}
	return plan, nil
}

func readMeta(path string) (Meta, error) {
	var meta Meta
	data, err := os.ReadFile(path)
	if err != nil {
		return Meta{}, err
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return Meta{}, err
	}
	return meta, nil
}

func readLatestPrompt(workdir string) (string, error) {
	latest, err := readLatest(workdir)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(latest.RunDir, "input.md"))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (a DiffArtifact) ChangedFiles() []string {
	files := make([]string, 0, len(a.Metadata.Files))
	for _, file := range a.Metadata.Files {
		files = append(files, file.Path)
	}
	return files
}

func captureDiffArtifact(ctx context.Context, workdir, baseline, runDir string, round int) (DiffArtifact, error) {
	prefix := filepath.Join(runDir, fmt.Sprintf("diff-round-%d", round))
	indexPath := filepath.Join(runDir, fmt.Sprintf("tmp-diff-round-%d.index", round))
	defer os.Remove(indexPath)
	defer os.Remove(indexPath + ".lock")

	patch, numstat, statusZ, numstatZ, err := deterministicDiffOutputs(ctx, workdir, baseline, indexPath)
	if err != nil {
		return DiffArtifact{}, err
	}
	patchPath := prefix + ".patch"
	if err := os.WriteFile(patchPath, patch, 0o644); err != nil {
		return DiffArtifact{}, err
	}
	sum := sha256.Sum256(patch)
	diffHash := hex.EncodeToString(sum[:])
	shaPath := prefix + ".sha256"
	if err := os.WriteFile(shaPath, []byte(diffHash+"\n"), 0o644); err != nil {
		return DiffArtifact{}, err
	}
	numstatPath := prefix + ".numstat"
	if err := os.WriteFile(numstatPath, normalizeTextFileNewline(numstat), 0o644); err != nil {
		return DiffArtifact{}, err
	}
	files := buildDiffFiles(statusZ, numstatZ)
	metadata := DiffFilesMetadata{
		SchemaVersion: ArtifactSchemaVersion,
		Baseline:      baseline,
		Head:          currentWorkingTreeHead(ctx, workdir),
		GeneratedAt:   time.Now().UTC(),
		DiffSHA256:    diffHash,
		Files:         files,
	}
	filesPath := prefix + ".files.json"
	if err := writeJSONWithNewline(filesPath, metadata); err != nil {
		return DiffArtifact{}, err
	}
	return DiffArtifact{
		PatchPath:   patchPath,
		NumstatPath: numstatPath,
		FilesPath:   filesPath,
		SHA256Path:  shaPath,
		Patch:       string(patch),
		Metadata:    metadata,
	}, nil
}

func deterministicDiffPatch(ctx context.Context, workdir, baseline, indexPath string) ([]byte, error) {
	patch, _, _, _, err := deterministicDiffOutputs(ctx, workdir, baseline, indexPath)
	return patch, err
}

func deterministicDiffOutputs(ctx context.Context, workdir, baseline, indexPath string) ([]byte, []byte, []byte, []byte, error) {
	defer os.Remove(indexPath)
	defer os.Remove(indexPath + ".lock")
	pathspecPath := indexPath + ".pathspec"
	defer os.Remove(pathspecPath)
	env := []string{"LC_ALL=C", "GIT_INDEX_FILE=" + indexPath}
	if _, err := runGitCommandBytes(ctx, workdir, env, "read-tree", baseline); err != nil {
		return nil, nil, nil, nil, err
	}
	if _, err := runGitCommandBytes(ctx, workdir, env, "add", "-u", "--", "."); err != nil {
		return nil, nil, nil, nil, err
	}
	pathspec, err := deterministicUntrackedPathspec(ctx, workdir)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if len(pathspec) > 0 {
		if err := os.WriteFile(pathspecPath, pathspec, 0o644); err != nil {
			return nil, nil, nil, nil, err
		}
		if _, err := runGitCommandBytes(ctx, workdir, env, "add", "--pathspec-from-file="+pathspecPath, "--pathspec-file-nul"); err != nil {
			return nil, nil, nil, nil, err
		}
	}
	patch, err := runGitCommandBytes(ctx, workdir, env, "-c", "core.quotepath=false", "diff", "--cached", "--no-ext-diff", "--no-color", "--binary", "--full-index", "--find-renames=50%", baseline, "--", ".")
	if err != nil {
		return nil, nil, nil, nil, err
	}
	numstat, err := runGitCommandBytes(ctx, workdir, env, "-c", "core.quotepath=false", "diff", "--cached", "--no-ext-diff", "--no-color", "--numstat", baseline, "--", ".")
	if err != nil {
		return nil, nil, nil, nil, err
	}
	statusZ, err := runGitCommandBytes(ctx, workdir, env, "-c", "core.quotepath=false", "diff", "--cached", "--no-ext-diff", "--no-color", "--name-status", "-z", "--find-renames=50%", baseline, "--", ".")
	if err != nil {
		return nil, nil, nil, nil, err
	}
	numstatZ, err := runGitCommandBytes(ctx, workdir, env, "-c", "core.quotepath=false", "diff", "--cached", "--no-ext-diff", "--no-color", "--numstat", "-z", baseline, "--", ".")
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return patch, numstat, statusZ, numstatZ, nil
}

func deterministicUntrackedPathspec(ctx context.Context, workdir string) ([]byte, error) {
	currentFiles, err := runGitCommandBytes(ctx, workdir, []string{"LC_ALL=C"}, "ls-files", "-z", "--others", "--exclude-standard", "--", ".")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	paths := []string{}
	for _, raw := range splitNULTokens(currentFiles) {
		path := strings.TrimPrefix(raw, "./")
		if path == "" || path == ".tagteam" || strings.HasPrefix(path, ".tagteam/") {
			continue
		}
		if seen[path] {
			continue
		}
		seen[path] = true
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var buf bytes.Buffer
	for _, path := range paths {
		buf.WriteString(path)
		buf.WriteByte(0)
	}
	return buf.Bytes(), nil
}

func buildDiffFiles(statusZ, numstatZ []byte) []DiffFile {
	stats := parseNumstatZ(numstatZ)
	files := parseNameStatusZ(statusZ)
	for i := range files {
		if stat, ok := stats[files[i].Path]; ok {
			files[i].Additions = stat.Additions
			files[i].Deletions = stat.Deletions
			files[i].Binary = stat.Binary
		}
	}
	sort.SliceStable(files, func(i, j int) bool {
		if files[i].Path == files[j].Path {
			return files[i].OldPath < files[j].OldPath
		}
		return files[i].Path < files[j].Path
	})
	return files
}

func parseNameStatusZ(raw []byte) []DiffFile {
	tokens := splitNULTokens(raw)
	files := make([]DiffFile, 0, len(tokens)/2)
	for i := 0; i < len(tokens); {
		code := tokens[i]
		i++
		if code == "" {
			continue
		}
		status := diffStatusName(code)
		file := DiffFile{Status: status}
		if strings.HasPrefix(code, "R") || strings.HasPrefix(code, "C") {
			if i+1 >= len(tokens) {
				break
			}
			file.OldPath = tokens[i]
			file.Path = tokens[i+1]
			i += 2
		} else {
			if i >= len(tokens) {
				break
			}
			file.Path = tokens[i]
			i++
		}
		files = append(files, file)
	}
	return files
}

func parseNumstatZ(raw []byte) map[string]DiffFile {
	tokens := splitNULTokens(raw)
	stats := map[string]DiffFile{}
	for i := 0; i < len(tokens); i++ {
		parts := strings.Split(tokens[i], "\t")
		if len(parts) < 3 {
			continue
		}
		stat := DiffFile{}
		stat.Additions, stat.Binary = parseNumstatCount(parts[0])
		var delBinary bool
		stat.Deletions, delBinary = parseNumstatCount(parts[1])
		stat.Binary = stat.Binary || delBinary
		path := parts[2]
		if path == "" {
			if i+2 >= len(tokens) {
				break
			}
			stat.OldPath = tokens[i+1]
			path = tokens[i+2]
			i += 2
		}
		stat.Path = path
		stats[path] = stat
	}
	return stats
}

func splitNULTokens(raw []byte) []string {
	parts := strings.Split(string(raw), "\x00")
	tokens := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			tokens = append(tokens, part)
		}
	}
	return tokens
}

func parseNumstatCount(raw string) (int, bool) {
	if raw == "-" {
		return 0, true
	}
	var n int
	for _, ch := range raw {
		if ch < '0' || ch > '9' {
			return 0, false
		}
		n = n*10 + int(ch-'0')
	}
	return n, false
}

func diffStatusName(code string) string {
	switch code[0] {
	case 'A':
		return "added"
	case 'C':
		return "copied"
	case 'D':
		return "deleted"
	case 'M':
		return "modified"
	case 'R':
		return "renamed"
	case 'T':
		return "typechanged"
	case 'U':
		return "unmerged"
	case 'X':
		return "unknown"
	default:
		return strings.ToLower(code)
	}
}

func currentWorkingTreeHead(ctx context.Context, workdir string) string {
	out, err := runGitCommandBytes(ctx, workdir, []string{"LC_ALL=C"}, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return "working-tree"
	}
	head := strings.TrimSpace(string(out))
	if head == "" {
		return "working-tree"
	}
	return head + "-working-tree"
}

func normalizeTextFileNewline(data []byte) []byte {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return []byte(text)
}

func writeJSONWithNewline(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

func gitDirty(workdir string) (bool, error) {
	out, err := runCommand(context.Background(), workdir, "git", "status", "--porcelain")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.Contains(line, ".tagteam/") || strings.HasSuffix(line, ".tagteam") {
			continue
		}
		return true, nil
	}
	return false, nil
}

func gitAutostash(workdir string) (string, error) {
	if _, err := runCommand(context.Background(), workdir, "git", "stash", "push", "-u", "-m", "tagteam-autostash"); err != nil {
		return "", &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	return "stash@{0}", nil
}

func gitCreateBranch(workdir, branch string) error {
	if _, err := runCommand(context.Background(), workdir, "git", "switch", "-c", branch); err == nil {
		return nil
	}
	if _, err := runCommand(context.Background(), workdir, "git", "checkout", "-b", branch); err != nil {
		return &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	return nil
}

func runCommand(ctx context.Context, workdir, binary string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = workdir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", binary, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func runGitCommandBytes(ctx context.Context, workdir string, extraEnv []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = workdir
	cmd.Env = append(os.Environ(), extraEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func safeTestOutput(output string) string {
	if strings.TrimSpace(output) == "" {
		return "(no tests run)"
	}
	return output
}

func logProgress(opts RunOptions, format string, args ...any) {
	if opts.Quiet {
		return
	}
	fmt.Fprintf(os.Stderr, "tagteam: "+format+"\n", args...)
}

func logRequestProgress(req Request, format string, args ...any) {
	if req.Quiet {
		return
	}
	fmt.Fprintf(os.Stderr, "tagteam: "+format+"\n", args...)
}

func shortDuration(d time.Duration) string {
	return d.Truncate(time.Second).String()
}

func prepareReviewInput(adversary Adapter, diff, diffPath string) reviewInput {
	diffBytes := []byte(diff)
	if adversary.Capabilities().SupportsStdin && len(diffBytes) <= maxReviewInputBytes {
		return reviewInput{
			Stdin:    diffBytes,
			ViaStdin: true,
			Mode:     "stdin",
		}
	}
	if len(diffBytes) <= maxInlineReviewPromptBytes {
		return reviewInput{
			PromptRef: diff,
			Mode:      "inline",
		}
	}
	if diffPath != "" {
		return reviewInput{
			PromptRef: fmt.Sprintf("Diff is stored at %s. Read that file from the workspace.", diffPath),
			Mode:      "file-reference",
		}
	}
	return reviewInput{
		PromptRef: diff,
		Mode:      "inline",
	}
}

func countExisting(dir, pattern string) int {
	matches, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		return 0
	}
	return len(matches)
}

func osReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func ensureGitignoreEntry(workdir, entry string) error {
	gitignorePath := filepath.Join(workdir, ".gitignore")
	if !fileExists(gitignorePath) {
		return os.WriteFile(gitignorePath, []byte(entry+"\n"), 0o644)
	}
	data, err := os.ReadFile(gitignorePath)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) == entry {
			return nil
		}
	}
	contents := strings.TrimRight(string(data), "\n")
	if contents == "" {
		contents = entry
	} else {
		contents += "\n" + entry
	}
	contents += "\n"
	return os.WriteFile(gitignorePath, []byte(contents), 0o644)
}

func ensureRunRootIgnore(rootDir string) error {
	gitignorePath := filepath.Join(rootDir, ".gitignore")
	contents := "*\n!.gitignore\n"
	if fileExists(gitignorePath) {
		data, err := os.ReadFile(gitignorePath)
		if err != nil {
			return err
		}
		if string(data) == contents {
			return nil
		}
	}
	return os.WriteFile(gitignorePath, []byte(contents), 0o644)
}

func newRunID() string {
	return time.Now().UTC().Format("2006-01-02T150405.000000000Z")
}

func readRunPrompt(runDir, fallback string) (string, error) {
	inputPath := filepath.Join(runDir, "input.md")
	if fileExists(inputPath) {
		data, err := os.ReadFile(inputPath)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	metaPath := filepath.Join(runDir, "meta.json")
	if fileExists(metaPath) {
		meta, err := readMeta(metaPath)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(meta.Prompt) != "" {
			return meta.Prompt, nil
		}
	}
	if strings.TrimSpace(fallback) != "" {
		return fallback, nil
	}
	return "", fmt.Errorf("run prompt not found in %s", runDir)
}
