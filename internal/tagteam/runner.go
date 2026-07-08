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

type reviewInput struct {
	PromptRef string
	Stdin     []byte
	ViaStdin  bool
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

func NewApp(cfg Config) *App {
	return &App{Config: cfg}
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

func (a *App) Review(ctx context.Context, opts RunOptions, prompt string) (FinalRun, error) {
	normalizeDefaultMode(&opts)
	if err := validateReviewRoles(opts); err != nil {
		return FinalRun{}, err
	}
	if err := a.validateReviewTargets(opts); err != nil {
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
	if err := checkAdapters(ctx, reviewer); err != nil {
		return FinalRun{}, err
	}
	runID := newRunID()
	runDir, err := createRunDir(opts.Workdir, runID)
	if err != nil {
		return FinalRun{}, &ExitError{Code: ExitAdapterFailure, Err: err}
	}
	if prompt == "" {
		if latestPrompt, _ := readLatestPrompt(opts.Workdir); latestPrompt != "" {
			prompt = latestPrompt
		} else {
			prompt = "Review the current working tree diff."
		}
	}
	if err := os.WriteFile(filepath.Join(runDir, "input.md"), []byte(prompt), 0o644); err != nil {
		return FinalRun{}, err
	}
	meta := Meta{
		RunID:         runID,
		Workdir:       opts.Workdir,
		Baseline:      baseline,
		Command:       "review",
		Prompt:        prompt,
		StartedAt:     time.Now().UTC(),
		Adapters:      map[string]string{reviewerLabel: opts.Adversary.Adapter},
		Models:        map[string]string{reviewerLabel: opts.Adversary.Model},
		ConfigSources: opts.ConfigSources,
	}
	if err := writeJSON(filepath.Join(runDir, "meta.json"), meta); err != nil {
		return FinalRun{}, err
	}
	schemaPath := filepath.Join(runDir, "review-schema.json")
	if err := os.WriteFile(schemaPath, []byte(ReviewSchema), 0o644); err != nil {
		return FinalRun{}, err
	}
	diffArtifact, err := captureDiffArtifact(ctx, opts.Workdir, baseline, runDir, 1)
	if err != nil {
		return FinalRun{}, err
	}
	review, cost, outputPath, err := a.runAdversary(ctx, opts, 1, runDir, schemaPath, prompt, baseline, diffArtifact.Patch, diffArtifact.PatchPath, "", RelayContext{})
	if err != nil {
		return FinalRun{}, err
	}
	savedCoder := RoleTarget{}
	if opts.CoderExplicit {
		savedCoder = opts.Coder
	}
	final := FinalRun{
		RunID:    runID,
		RunDir:   runDir,
		Workdir:  opts.Workdir,
		Baseline: baseline,
		Mode:     opts.Mode,
		// Coder is persisted only when explicitly selected for this review
		// invocation. Review never invokes the editor, so default/stale
		// editor config must not block reviewer-only runs or poison a later
		// bare `tagteam fix`; explicit -mc/--worker is preserved for fix.
		Coder:             savedCoder,
		Adversary:         opts.Adversary,
		Verdict:           review.Verdict,
		Summary:           review.Summary,
		ExitCode:          ExitSuccess,
		RoundsRequested:   1,
		RoundsCompleted:   1,
		ChangedFiles:      diffArtifact.ChangedFiles(),
		LatestDiffPath:    diffArtifact.PatchPath,
		LatestNumstatPath: diffArtifact.NumstatPath,
		LatestFilesPath:   diffArtifact.FilesPath,
		LatestSHA256Path:  diffArtifact.SHA256Path,
		LatestDiffSHA256:  diffArtifact.Metadata.DiffSHA256,
		LatestReviewPath:  outputPath,
		Review:            review,
		Costs:             map[string]float64{reviewerLabel: cost},
		Adapters:          meta.Adapters,
		Models:            meta.Models,
		StartedAt:         meta.StartedAt,
		FinishedAt:        time.Now().UTC(),
	}
	if opts.FailOnReview && review.HasBlockingFindings() {
		final.ExitCode = ExitBlockingFindings
	}
	if err := a.persistFinal(opts.Workdir, final); err != nil {
		return FinalRun{}, err
	}
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
	if opts.Mode != ModeAdversarial && (opts.Adversary.Adapter == "openai-compatible" || opts.Adversary.Adapter == "oai") {
		return &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("openai-compatible adapter is review-only in this version; use it with --mode adversarial or tagteam review")}
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
	if role != RoleAdversary && (target.Adapter == "openai-compatible" || target.Adapter == "oai") {
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
	if err := os.WriteFile(filepath.Join(runDir, "input.md"), []byte(opts.Prompt), 0o644); err != nil {
		return FinalRun{}, err
	}
	meta := Meta{
		RunID:         runID,
		Workdir:       opts.Workdir,
		Baseline:      baseline,
		Command:       "run",
		Prompt:        opts.Prompt,
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
		RunID:           runID,
		RunDir:          runDir,
		Workdir:         opts.Workdir,
		Baseline:        baseline,
		Mode:            opts.Mode,
		Coder:           opts.Coder,
		Verdict:         "done",
		RoundsRequested: 1,
		Costs:           map[string]float64{},
		Adapters:        meta.Adapters,
		Models:          meta.Models,
		StartedAt:       meta.StartedAt,
	}

	logProgress(opts, "solo implementation started adapter=%s", editor.ID())
	outputPath := filepath.Join(runDir, "solo-round-1.md")
	editorResult, err := a.runAdapter(ctx, editor, RoleCoder, Request{
		Context:      ctx,
		Prompt:       BuildSoloPrompt(opts.Workdir, opts.Prompt),
		SystemPrompt: soloSystemPrompt,
		Model:        opts.Coder.Model,
		Workdir:      opts.Workdir,
		RunDir:       runDir,
		OutputPath:   outputPath,
		Timeout:      opts.Timeout,
		Phase:        fmt.Sprintf("solo %s", editor.ID()),
		Quiet:        opts.Quiet,
		Verbose:      opts.Verbose,
	}, opts.DryRun)
	if err != nil {
		return final, err
	}
	final.Costs[editorLabel] += editorResult.CostUSD
	final.Summary = strings.TrimSpace(editorResult.Text)
	final.RoundsCompleted = 1
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
		testRun, err := runTestCommand(ctx, opts.Workdir, opts.TestCmd, opts.Timeout, testPath, opts.DryRun)
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
	logProgress(opts, "run %s finished mode=solo exit=%d", runID, final.ExitCode)
	if err := a.persistFinal(opts.Workdir, final); err != nil {
		return final, err
	}
	if final.ExitCode != ExitSuccess {
		return final, &ExitError{Code: final.ExitCode, Err: fmt.Errorf("run completed with exit code %d", final.ExitCode)}
	}
	return final, nil
}

func (a *App) runLoop(ctx context.Context, opts RunOptions, initialReview *Review) (FinalRun, error) {
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
	if err := os.WriteFile(filepath.Join(runDir, "input.md"), []byte(opts.Prompt), 0o644); err != nil {
		return FinalRun{}, err
	}
	schemaPath := filepath.Join(runDir, "review-schema.json")
	if err := os.WriteFile(schemaPath, []byte(ReviewSchema), 0o644); err != nil {
		return FinalRun{}, err
	}
	meta := Meta{
		RunID:         runID,
		Workdir:       opts.Workdir,
		Baseline:      baseline,
		Command:       "run",
		Prompt:        opts.Prompt,
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
	logProgress(opts, "run %s started mode=%s baseline=%s run-dir=%s", runID, opts.Mode, baseline, runDir)

	registry := Registry(a.Config, opts)
	editor, ok := registry[opts.Coder.Adapter]
	if !ok {
		return FinalRun{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("unknown %s adapter %q", editorLabel, opts.Coder.Adapter)}
	}
	reviewer, ok := registry[opts.Adversary.Adapter]
	if !ok {
		return FinalRun{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("unknown %s adapter %q", reviewerLabel, opts.Adversary.Adapter)}
	}
	adaptersToCheck := []Adapter{editor, reviewer}
	var scout Adapter
	if opts.Mode == ModeRelay {
		var scoutOK bool
		scout, scoutOK = registry[opts.Scout.Adapter]
		if !scoutOK {
			return FinalRun{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("unknown scout adapter %q", opts.Scout.Adapter)}
		}
		adaptersToCheck = append(adaptersToCheck, scout)
	}
	if err := checkAdapters(ctx, adaptersToCheck...); err != nil {
		return FinalRun{}, err
	}

	final := FinalRun{
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
		Costs:             map[string]float64{},
		Adapters:          meta.Adapters,
		Models:            meta.Models,
		StartedAt:         meta.StartedAt,
	}

	var brief string
	var relay RelayContext
	var workPlan *WorkPlan
	var selectedPackage *WorkPackage
	if opts.Mode == ModeSupervisor && initialReview == nil {
		if opts.SupervisorSlicing {
			logProgress(opts, "supervisor slicing started adapter=%s max-packages=%d", reviewer.ID(), opts.MaxPackages)
			planOutputPath := filepath.Join(runDir, "supervisor-work-plan.json")
			var plan WorkPlan
			var planCost float64
			if opts.DryRun {
				plan = syntheticWorkPlan(opts.Prompt, opts.Package)
			} else {
				planResult, err := a.runAdapter(ctx, reviewer, RoleSupervisor, Request{
					Context:    ctx,
					Prompt:     BuildSupervisorWorkPlanPrompt(opts.Workdir, opts.Prompt, opts.MaxPackages, opts.Package),
					Model:      opts.Adversary.Model,
					Workdir:    opts.Workdir,
					RunDir:     runDir,
					OutputPath: planOutputPath,
					Timeout:    opts.Timeout,
					Phase:      fmt.Sprintf("supervisor slicing %s", reviewer.ID()),
					Quiet:      opts.Quiet,
					Verbose:    opts.Verbose,
				}, false)
				if err != nil {
					return final, err
				}
				parsed, err := parseWorkPlan([]byte(planResult.Text), opts.Package, opts.MaxPackages)
				if err != nil {
					return final, err
				}
				plan = parsed
				planCost = planResult.CostUSD
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
				Prompt:     BuildSupervisorBriefPrompt(opts.Workdir, opts.Prompt, opts.SupervisorCanEdit),
				Model:      opts.Adversary.Model,
				Workdir:    opts.Workdir,
				RunDir:     runDir,
				OutputPath: briefOutputPath,
				Timeout:    opts.Timeout,
				Phase:      fmt.Sprintf("supervisor brief %s", reviewer.ID()),
				Quiet:      opts.Quiet,
				Verbose:    opts.Verbose,
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
		logProgress(opts, "scout %s started adapter=%s", opts.ScoutMode, scout.ID())
		scoutResult, err := a.runAdapter(ctx, scout, RoleScout, Request{
			Context:    ctx,
			Prompt:     BuildScoutPrompt(opts.Workdir, opts.Prompt, "", opts.ScoutMode, "pre", "", ""),
			Model:      opts.Scout.Model,
			Workdir:    opts.Workdir,
			RunDir:     runDir,
			OutputPath: scoutOutputPath,
			Timeout:    opts.Timeout,
			Phase:      fmt.Sprintf("scout %s %s", opts.ScoutMode, scout.ID()),
			Quiet:      opts.Quiet,
			Verbose:    opts.Verbose,
		}, opts.DryRun)
		if err != nil {
			return final, err
		}
		if scoutResult.Scout != nil {
			relay.Scout = *scoutResult.Scout
		}
		final.Costs["scout"] += scoutResult.CostUSD
		logProgress(opts, "scout %s completed output=%s", opts.ScoutMode, scoutOutputPath)

		logProgress(opts, "supervisor brief started adapter=%s", reviewer.ID())
		briefOutputPath := filepath.Join(runDir, "supervisor-brief.md")
		briefResult, err := a.runAdapter(ctx, reviewer, supervisorBriefRole(opts.SupervisorCanEdit), Request{
			Context:    ctx,
			Prompt:     BuildSupervisorBriefPrompt(opts.Workdir, opts.Prompt, opts.SupervisorCanEdit),
			Model:      opts.Adversary.Model,
			Workdir:    opts.Workdir,
			RunDir:     runDir,
			OutputPath: briefOutputPath,
			Timeout:    opts.Timeout,
			Phase:      fmt.Sprintf("supervisor brief %s", reviewer.ID()),
			Quiet:      opts.Quiet,
			Verbose:    opts.Verbose,
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
			Prompt:     BuildRelaySupervisorInstructionsPrompt(opts.Prompt, brief, relay.Scout),
			Model:      opts.Adversary.Model,
			Workdir:    opts.Workdir,
			RunDir:     runDir,
			OutputPath: instructionsPath,
			Timeout:    opts.Timeout,
			Phase:      fmt.Sprintf("relay supervisor instructions %s", reviewer.ID()),
			Quiet:      opts.Quiet,
			Verbose:    opts.Verbose,
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
		implementSelectedPackage = false
		editorOutputPath := filepath.Join(runDir, fmt.Sprintf("%s-round-%d.md", editorLabel, round))
		editorResult, err := a.runAdapter(ctx, editor, RoleCoder, Request{
			Context:      ctx,
			Prompt:       editorPrompt,
			SystemPrompt: editorSystemPrompt,
			Model:        opts.Coder.Model,
			Workdir:      opts.Workdir,
			RunDir:       runDir,
			OutputPath:   editorOutputPath,
			ResumeID:     sessionID,
			Timeout:      opts.Timeout,
			Phase:        fmt.Sprintf("round %d %s %s", round, editorLabel, editor.ID()),
			Quiet:        opts.Quiet,
			Verbose:      opts.Verbose,
		}, opts.DryRun)
		if err != nil {
			return final, err
		}
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
		logProgress(opts, "round %d diff captured bytes=%d path=%s", round, len(diff), diffArtifact.PatchPath)

		testOutput := ""
		if opts.TestCmd != "" && !opts.NoTest {
			testPath := filepath.Join(runDir, fmt.Sprintf("test-round-%d.txt", round))
			logProgress(opts, "round %d tests started command=%q", round, opts.TestCmd)
			testRun, err := runTestCommand(ctx, opts.Workdir, opts.TestCmd, opts.Timeout, testPath, opts.DryRun)
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

		if opts.Mode == ModeRelay {
			postScoutPath := filepath.Join(runDir, fmt.Sprintf("post-scout-round-%d.json", round))
			logProgress(opts, "round %d post-scout %s started adapter=%s", round, opts.PostScoutMode, scout.ID())
			postScoutResult, err := a.runAdapter(ctx, scout, RoleScout, Request{
				Context:    ctx,
				Prompt:     BuildScoutPrompt(opts.Workdir, opts.Prompt, relay.Brief, opts.PostScoutMode, "post", diff, safeTestOutput(testOutput)),
				Model:      opts.Scout.Model,
				Workdir:    opts.Workdir,
				RunDir:     runDir,
				OutputPath: postScoutPath,
				Timeout:    opts.Timeout,
				Phase:      fmt.Sprintf("round %d post-scout %s %s", round, opts.PostScoutMode, scout.ID()),
				Quiet:      opts.Quiet,
				Verbose:    opts.Verbose,
			}, opts.DryRun)
			if err != nil {
				return final, err
			}
			if postScoutResult.Scout != nil {
				relay.PostScout = *postScoutResult.Scout
			}
			final.Costs["scout"] += postScoutResult.CostUSD
			logProgress(opts, "round %d post-scout %s completed output=%s", round, opts.PostScoutMode, postScoutPath)
		}

		logProgress(opts, "round %d %s started adapter=%s", round, reviewerLabel, reviewer.ID())
		review, cost, reviewPath, err := a.runAdversary(ctx, opts, round, runDir, schemaPath, opts.Prompt, baseline, diff, diffArtifact.PatchPath, testOutput, relay)
		if err != nil {
			return final, err
		}
		logProgress(opts, "round %d %s completed verdict=%s findings=%d output=%s", round, reviewerLabel, review.Verdict, len(review.Findings), reviewPath)
		final.Costs[reviewerLabel] += cost
		final.RoundsCompleted = round
		final.Review = review
		final.LatestReviewPath = reviewPath
		latestReview = *review

		if review.Verdict == "pass" {
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
					logProgress(opts, "package %s passed; continuing to %s: %s", reviewPackageID(final.SelectedPackage), nextPackage.ID, nextPackage.Title)
					continue
				}
			}
			final.Verdict = "pass"
			final.Summary = review.Summary
			if len(final.RemainingPackages) > 0 {
				final.Summary = appendRemainingPackagesSummary(final.Summary, final.RemainingPackages)
			}
			break
		}
		if review.OnlyMinorOrNit() {
			final.Verdict = review.Verdict
			final.Summary = review.Summary
			if len(final.RemainingPackages) > 0 {
				final.Summary = appendRemainingPackagesSummary(final.Summary, final.RemainingPackages)
			}
			break
		}
		if round == opts.Rounds {
			final.Verdict = review.Verdict
			final.Summary = review.Summary
			final.RoundLimitReached = true
			logProgress(opts, "round limit reached after %d rounds; collecting final reports", opts.Rounds)
			reports, reportCosts := a.collectRoundLimitReports(ctx, opts, runDir, baseline, diff, *review, final.Tests)
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
	logProgress(opts, "run %s finished verdict=%s exit=%d rounds=%d/%d", runID, final.Verdict, final.ExitCode, final.RoundsCompleted, final.RoundsRequested)
	if err := a.persistFinal(opts.Workdir, final); err != nil {
		return final, err
	}
	if final.ExitCode != ExitSuccess {
		return final, &ExitError{Code: final.ExitCode, Err: fmt.Errorf("run completed with exit code %d", final.ExitCode)}
	}
	return final, nil
}

func (a *App) collectRoundLimitReports(ctx context.Context, opts RunOptions, runDir, baseline, diff string, review Review, tests []TestRun) ([]RoundLimitReport, map[string]float64) {
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
		prompt := BuildRoundLimitReportPrompt(target.label, target.counterpart, opts.Mode, opts.Prompt, diffWithBaselineHeader(baseline, diff), review, tests)
		result, err := a.runAdapter(ctx, adapter, RoleReporter, Request{
			Context:    ctx,
			Prompt:     prompt,
			Model:      target.model,
			Workdir:    opts.Workdir,
			RunDir:     runDir,
			OutputPath: reportPath,
			Timeout:    opts.Timeout,
			Phase:      fmt.Sprintf("round-limit %s %s", target.label, adapter.ID()),
			Quiet:      opts.Quiet,
			Verbose:    opts.Verbose,
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
		Summary: "dry-run work package",
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

func (a *App) runAdversary(ctx context.Context, opts RunOptions, round int, runDir, schemaPath, prompt, baseline, diff, diffPath, testOutput string, relay RelayContext) (*Review, float64, string, error) {
	_, reviewerLabel := roleLabels(opts.Mode)
	outputLabel := reviewerLabel
	if opts.Mode == ModeRelay {
		outputLabel = "supervisor-review"
	}
	outputPath := filepath.Join(runDir, fmt.Sprintf("%s-round-%d.json", outputLabel, round))
	registry := Registry(a.Config, opts)
	adversary, ok := registry[opts.Adversary.Adapter]
	if !ok {
		return nil, 0, "", &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("unknown %s adapter %q", reviewerLabel, opts.Adversary.Adapter)}
	}
	if err := checkAdapters(ctx, adversary); err != nil {
		return nil, 0, "", err
	}
	input := prepareReviewInput(adversary, diff, diffPath)
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
	if !adversary.Capabilities().SupportsSchema {
		reviewPrompt += "\n\nJSON schema:\n" + ReviewSchema
	}
	req := Request{
		Context:     ctx,
		Prompt:      reviewPrompt,
		Model:       opts.Adversary.Model,
		Workdir:     opts.Workdir,
		RunDir:      runDir,
		OutputPath:  outputPath,
		SchemaPath:  schemaPath,
		Passthrough: opts.ClaudeArgs,
		Timeout:     opts.Timeout,
		Phase:       fmt.Sprintf("round %d %s %s", round, reviewerLabel, adversary.ID()),
		Quiet:       opts.Quiet,
		Verbose:     opts.Verbose,
	}
	req.Stdin = input.Stdin
	result, err := a.runAdapter(ctx, adversary, RoleAdversary, req, opts.DryRun)
	if err != nil {
		if !IsOutputContractError(err) {
			return nil, 0, "", err
		}
		logProgress(opts, "round %d %s output invalid; retrying once error=%q", round, reviewerLabel, err.Error())
		req.Prompt = req.Prompt + "\n\nValidation error from the previous response:\n" + err.Error() + "\n\nReturn JSON exactly matching the schema."
		result, err = a.runAdapter(ctx, adversary, RoleAdversary, req, opts.DryRun)
		if err != nil {
			if IsOutputContractError(err) {
				return nil, 0, "", &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("%s output failed validation after retry: %w", reviewerLabel, err)}
			}
			return nil, 0, "", err
		}
		return result.Review, result.CostUSD, outputPath, nil
	}
	return result.Review, result.CostUSD, outputPath, nil
}

func (a *App) runAdapter(ctx context.Context, adapter Adapter, role Role, req Request, dryRun bool) (Result, error) {
	if direct, ok := adapter.(DirectAdapter); ok {
		spec, err := adapter.BuildCmd(role, req)
		if err != nil {
			return Result{}, &ExitError{Code: ExitInvalidArguments, Err: err}
		}
		if dryRun {
			payload, _ := json.MarshalIndent(spec, "", "  ")
			result := Result{Text: string(payload), Command: spec.Argv}
			if role == RoleAdversary {
				result.Review = &Review{
					Verdict:         "pass",
					Summary:         "dry-run",
					Findings:        []Finding{},
					TestSuggestions: []string{},
				}
			}
			return result, nil
		}
		result, err := direct.RunDirect(role, req)
		if err != nil {
			return Result{}, err
		}
		if req.OutputPath != "" && !fileExists(req.OutputPath) {
			raw := result.Raw
			if len(raw) == 0 {
				raw = []byte(result.Text)
			}
			if writeErr := os.WriteFile(req.OutputPath, raw, 0o644); writeErr != nil {
				return Result{}, writeErr
			}
		}
		return result, nil
	}
	spec, err := adapter.BuildCmd(role, req)
	if err != nil {
		return Result{}, &ExitError{Code: ExitInvalidArguments, Err: err}
	}
	if dryRun {
		payload, _ := json.MarshalIndent(spec, "", "  ")
		result := Result{Text: string(payload), Command: spec.Argv}
		if role == RoleAdversary {
			result.Review = &Review{
				Verdict:         "pass",
				Summary:         "dry-run",
				Findings:        []Finding{},
				TestSuggestions: []string{},
			}
		}
		if role == RoleScout {
			result.Scout = &Scout{
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
	cmd.Dir = spec.Dir
	if len(spec.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(spec.Stdin)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
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
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		logRequestProgress(req, "%s failed elapsed=%s", phase, shortDuration(time.Since(started)))
		return Result{}, &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("%s failed: %s", adapter.ID(), msg)}
	}
	close(done)
	logRequestProgress(req, "%s process completed elapsed=%s", phase, shortDuration(time.Since(started)))
	raw := stdout.Bytes()
	if req.OutputPath != "" && fileExists(req.OutputPath) {
		var readErr error
		raw, readErr = os.ReadFile(req.OutputPath)
		if readErr != nil {
			return Result{}, readErr
		}
	}
	if len(raw) == 0 {
		raw = stdout.Bytes()
	}
	if req.OutputPath != "" && !fileExists(req.OutputPath) {
		if writeErr := os.WriteFile(req.OutputPath, raw, 0o644); writeErr != nil {
			return Result{}, writeErr
		}
	}
	result, err := adapter.ParseResult(role, raw)
	if err != nil {
		return Result{}, err
	}
	if role == RoleScout && req.OutputPath != "" && result.Scout != nil {
		if err := writeJSONWithNewline(req.OutputPath, result.Scout); err != nil {
			return Result{}, err
		}
	}
	result.Command = spec.Argv
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

func runTestCommand(ctx context.Context, workdir, testCmd string, timeout time.Duration, outputPath string, dryRun bool) (TestRun, error) {
	if dryRun {
		return TestRun{Command: testCmd, Passed: true, Output: "dry-run"}, nil
	}
	runCtx := ctx
	cancel := func() {}
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()
	cmd := exec.CommandContext(runCtx, "/bin/sh", "-lc", testCmd)
	cmd.Dir = workdir
	out, err := cmd.CombinedOutput()
	testRun := TestRun{
		Command: testCmd,
		Output:  string(out),
		Passed:  err == nil,
	}
	_ = os.WriteFile(outputPath, out, 0o644)
	return testRun, nil
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
		Baseline:    baseline,
		Head:        currentWorkingTreeHead(ctx, workdir),
		GeneratedAt: time.Now().UTC(),
		DiffSHA256:  diffHash,
		Files:       files,
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
		}
	}
	if len(diffBytes) <= maxInlineReviewPromptBytes {
		return reviewInput{
			PromptRef: diff,
		}
	}
	if diffPath != "" {
		return reviewInput{
			PromptRef: fmt.Sprintf("Diff is stored at %s. Read that file from the workspace.", diffPath),
		}
	}
	return reviewInput{
		PromptRef: diff,
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
