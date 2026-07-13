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
	"strings"
	"time"
)

type App struct {
	Config Config
}

type maxOutputBytesContextKey struct{}

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
	// Re-resolve immediately before each write so a replaced run directory
	// cannot redirect repo-instruction persistence outside the runs root.
	current, rebindErr := rebindControlResumeFromContext(ctx, runDir, nil, "repo-instructions.md")
	if rebindErr != nil {
		return "", &ExitError{Code: ExitPreflightFailed, Err: rebindErr}
	}
	if err := writeFileDurable(filepath.Join(current, "repo-instructions.md"), []byte(bundle.Text), 0o644, true); err != nil {
		return "", err
	}
	current, rebindErr = rebindControlResumeFromContext(ctx, current, nil, "repo-instructions.json")
	if rebindErr != nil {
		return "", &ExitError{Code: ExitPreflightFailed, Err: rebindErr}
	}
	if err := writeJSON(filepath.Join(current, "repo-instructions.json"), bundle.Metadata); err != nil {
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
	return persistRunState(runDir, state)
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

func persistExecutionPlan(ctx context.Context, runDir string, plan *ExecutionPlan) error {
	if plan == nil {
		return nil
	}
	// Gate-aware independent rebind before each plan artifact so a replacement
	// between plan.json and plan-events.jsonl cannot escape the runs root.
	current, err := rebindControlResumeFromContext(ctx, runDir, nil, "plan.json")
	if err != nil {
		return &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	if err := writeJSONWithNewline(filepath.Join(current, "plan.json"), plan); err != nil {
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
	current, err = rebindControlResumeFromContext(ctx, current, nil, "plan-events.jsonl")
	if err != nil {
		return &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	return writeFileDurable(filepath.Join(current, "plan-events.jsonl"), events.Bytes(), 0o644, true)
}

func (a *App) Run(ctx context.Context, opts RunOptions) (FinalRun, error) {
	normalizeDefaultMode(&opts)
	ctx = context.WithValue(ctx, maxOutputBytesContextKey{}, opts.MaxOutputBytes)
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
