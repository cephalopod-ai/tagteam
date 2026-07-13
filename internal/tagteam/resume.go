package tagteam

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type ResumeRecord struct {
	SchemaVersion  int       `json:"schema_version"`
	SourceRunID    string    `json:"source_run_id"`
	ContinuedRunID string    `json:"continued_run_id,omitempty"`
	VerifiedPhase  RunPhase  `json:"verified_phase"`
	Baseline       string    `json:"baseline"`
	DiffHash       string    `json:"diff_hash,omitempty"`
	Status         string    `json:"status"`
	Message        string    `json:"message,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// Resume verifies an interrupted run and continues the first incomplete phase
// in the same authoritative run directory.
func (a *App) Resume(ctx context.Context, opts RunOptions, runID string) (FinalRun, error) {
	ctx = context.WithValue(ctx, maxOutputBytesContextKey{}, opts.MaxOutputBytes)
	locator, err := resolveStateLocator(opts.Workdir, opts.StateRoot)
	if err != nil {
		return FinalRun{}, &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	if err := locator.Prepare(); err != nil {
		return FinalRun{}, &ExitError{Code: ExitAdapterFailure, Err: err}
	}
	runDir, err := locator.RunDir(runID)
	if err != nil {
		return FinalRun{}, &ExitError{Code: ExitInvalidArguments, Err: err}
	}
	if info, statErr := os.Stat(runDir); statErr != nil || !info.IsDir() {
		return FinalRun{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("run %q not found", runID)}
	}
	return a.resumeAtRunDir(ctx, opts, runID, runDir, false)
}

// ResumeControl is the MCP-owned resume entry point. It re-resolves the
// canonical run directory under the state root immediately before lock and
// mutation so a post-assessment symlink replacement cannot redirect I/O.
func (a *App) ResumeControl(ctx context.Context, opts RunOptions, runID string) (FinalRun, error) {
	ctx = context.WithValue(ctx, maxOutputBytesContextKey{}, opts.MaxOutputBytes)
	runDir, err := resolveAndValidateControlResumeRunDir(opts.Workdir, opts.StateRoot, runID)
	if err != nil {
		return FinalRun{}, &ExitError{Code: ExitInvalidArguments, Err: err}
	}
	return a.resumeAtRunDir(ctx, opts, runID, runDir, true)
}

func resolveAndValidateControlResumeRunDir(workdir, stateRoot, runID string) (string, error) {
	runDir, _, err := resolveControlRunDirectory(workdir, stateRoot, runID)
	if err != nil {
		return "", err
	}
	if err := ensureControlWritableArtifacts(runDir, "state.json", "meta.json", "final.json", "run.lock", "resume.json", "resume-verify.index"); err != nil {
		return "", err
	}
	// Reject escaping named artifacts before any content is consumed.
	for _, name := range []string{
		"state.json", "meta.json", "final.json", "run.lock",
		"events.jsonl", "input.md", "plan.json",
		"supervisor-brief.md", "supervisor-instructions.md",
		"scout-round-1.json", "post-scout-round-1.json", "supervisor-work-plan.json",
	} {
		if _, err := os.Lstat(filepath.Join(runDir, name)); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return "", err
		}
		if _, err := readControlArtifactBytes(runDir, name); err != nil {
			return "", err
		}
	}
	// Immediate re-resolve closes the TOCTOU window between validation and lock.
	again, _, err := resolveControlRunDirectory(workdir, stateRoot, runID)
	if err != nil {
		return "", err
	}
	if again != runDir {
		return "", fmt.Errorf("run %q path changed under the resolved state root", runID)
	}
	return runDir, nil
}

func (a *App) resumeAtRunDir(ctx context.Context, opts RunOptions, runID, runDir string, controlSafe bool) (FinalRun, error) {
	lock, err := acquireRunLock(runDir, true)
	if err != nil {
		return FinalRun{}, &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	defer lock.Release()
	if controlSafe {
		// Re-resolve after lock: refuse if the run directory escaped while
		// waiting for exclusive ownership.
		resolved, err := resolveAndValidateControlResumeRunDir(opts.Workdir, opts.StateRoot, runID)
		if err != nil {
			return FinalRun{}, &ExitError{Code: ExitPreflightFailed, Err: err}
		}
		if resolved != runDir {
			return FinalRun{}, &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("run %q path changed under the resolved state root", runID)}
		}
		runDir = resolved
	}
	readState := readRunState
	readMetaFn := func(dir string) (Meta, error) { return readMeta(filepath.Join(dir, "meta.json")) }
	readFinalFn := func(dir string) (FinalRun, error) { return readFinal(filepath.Join(dir, "final.json")) }
	if controlSafe {
		readState = readControlRunState
		readMetaFn = readControlMeta
		readFinalFn = func(dir string) (FinalRun, error) {
			final, _, err := readControlFinalOptional(dir)
			return final, err
		}
	}
	state, err := readState(runDir)
	if err != nil {
		return FinalRun{}, &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("read resumable state: %w", err)}
	}
	if state.SchemaVersion < runStateSchemaVersion {
		return FinalRun{}, &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("legacy run state schema %d is readable but not resumable", state.SchemaVersion)}
	}
	meta, err := readMetaFn(runDir)
	if err != nil {
		return FinalRun{}, &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("read run metadata: %w", err)}
	}
	currentHead, err := ensureGitRepo(opts.Workdir)
	if err != nil {
		return FinalRun{}, err
	}
	if currentHead != meta.Baseline {
		return quarantineResume(runDir, state, fmt.Errorf("current HEAD %s does not match run baseline %s", currentHead, meta.Baseline))
	}
	if controlSafe {
		if err := ensureControlWritableArtifacts(runDir, "resume-verify.index", "events.jsonl", "input.md", "resume.json"); err != nil {
			return FinalRun{}, &ExitError{Code: ExitPreflightFailed, Err: err}
		}
	}
	patch, err := deterministicDiffPatch(ctx, opts.Workdir, meta.Baseline, filepath.Join(runDir, "resume-verify.index"))
	if err != nil {
		return FinalRun{}, err
	}
	currentDiffHash := sha256Sum(patch)
	phase := normalizeRunPhase(state.Phase)
	if state.DiffHash != "" && state.DiffHash != currentDiffHash && phase != PhaseImplementing && phase != PhaseRepairing {
		return quarantineResume(runDir, state, fmt.Errorf("worktree diff hash changed after completed %s phase", phase))
	}
	if err := verifyResumeArtifacts(runDir, state, controlSafe); err != nil {
		return quarantineResume(runDir, state, err)
	}
	var prompt string
	if controlSafe {
		prompt, err = readControlRunPrompt(runDir, opts.Prompt)
	} else {
		prompt, err = readRunPrompt(runDir, opts.Prompt)
	}
	if err != nil {
		return FinalRun{}, err
	}
	saved, _ := readFinalFn(runDir)
	if saved.Status == RunStatusPassed || saved.Status == RunStatusDegraded {
		return saved, nil
	}
	opts.Prompt = prompt
	opts.Baseline = meta.Baseline
	opts.SkipDirtyCheck = true
	opts.AllowDirty = true
	opts.ResumedFrom = runID
	if saved.Mode != "" {
		opts.Mode = saved.Mode
	}
	if saved.Coder.Adapter != "" {
		opts.Coder = saved.Coder
	}
	if saved.Adversary.Adapter != "" {
		opts.Adversary = saved.Adversary
	}
	if saved.Scout.Adapter != "" {
		opts.Scout = saved.Scout
	}
	if opts.Mode == "" {
		opts.Mode = metaMode(meta)
	}
	if opts.Coder.Adapter == "" || (opts.Mode != ModeSolo && opts.Adversary.Adapter == "") {
		restoreTargetsFromMeta(&opts, meta)
	}
	if controlSafe {
		if err := ensureControlWritableArtifacts(runDir, "resume.json"); err != nil {
			return FinalRun{}, &ExitError{Code: ExitPreflightFailed, Err: err}
		}
	}
	record := ResumeRecord{SchemaVersion: ArtifactSchemaVersion, SourceRunID: runID, VerifiedPhase: phase, Baseline: meta.Baseline, DiffHash: currentDiffHash, Status: "verified", CreatedAt: time.Now().UTC()}
	_ = writeJSONWithNewline(filepath.Join(runDir, "resume.json"), record)
	var prior *Review
	if state.LatestReviewPath != "" {
		if review, reviewErr := readReviewArtifact(state.LatestReviewPath); reviewErr == nil {
			prior = &review
		}
	}
	continued, err := a.resumeExistingRun(ctx, opts, runDir, meta, state, saved, prior, currentDiffHash, controlSafe)
	record.ContinuedRunID = runID
	record.Status = "resumed"
	if err != nil {
		if continued.FinishedAt.IsZero() {
			record.Status = "resume_failed"
		}
		record.Message = err.Error()
	}
	_ = writeJSONWithNewline(filepath.Join(runDir, "resume.json"), record)
	return continued, err
}

func verifyResumeArtifacts(runDir string, state RunState, controlSafe bool) error {
	if state.LatestDiffPath != "" {
		if controlSafe {
			if err := validateControlWritablePath(runDir, state.LatestDiffPath); err != nil {
				return err
			}
		} else if err := verifyResumeArtifactPath(runDir, state.LatestDiffPath); err != nil {
			return err
		}
		patch, err := os.ReadFile(state.LatestDiffPath)
		if err != nil {
			return fmt.Errorf("read latest diff artifact: %w", err)
		}
		if state.DiffHash != "" && sha256Sum(patch) != state.DiffHash {
			return fmt.Errorf("latest diff artifact hash does not match state")
		}
	}
	if state.LatestReviewPath != "" {
		if controlSafe {
			if err := validateControlWritablePath(runDir, state.LatestReviewPath); err != nil {
				return err
			}
		} else if err := verifyResumeArtifactPath(runDir, state.LatestReviewPath); err != nil {
			return err
		}
		if _, err := readReviewArtifact(state.LatestReviewPath); err != nil {
			return fmt.Errorf("latest review artifact is invalid: %w", err)
		}
	}
	var events []byte
	if controlSafe {
		data, present, err := readControlOptionalArtifactBytes(runDir, "events.jsonl")
		if err != nil {
			return fmt.Errorf("read state journal: %w", err)
		}
		if !present {
			return nil
		}
		events = data
	} else {
		var err error
		events, err = os.ReadFile(filepath.Join(runDir, "events.jsonl"))
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read state journal: %w", err)
		}
	}
	lines := bytes.Split(bytes.TrimSpace(events), []byte{'\n'})
	if len(lines) == 0 || len(lines[0]) == 0 {
		return fmt.Errorf("state journal is empty")
	}
	var last StateEvent
	if err := json.Unmarshal(lines[len(lines)-1], &last); err != nil {
		return fmt.Errorf("decode final state journal event: %w", err)
	}
	if last.RunID != state.RunID || last.ToPhase != normalizeRunPhase(state.Phase) || last.Status != state.Status || last.Round != state.CurrentRound {
		return fmt.Errorf("state journal does not match the latest state transition")
	}
	if last.DiffHash != "" && state.DiffHash != "" && last.DiffHash != state.DiffHash {
		return fmt.Errorf("state journal diff hash does not match state")
	}
	return nil
}

// readControlRunPrompt loads input.md (or meta prompt) through control-safe
// artifact readers so escaping symlinks cannot feed external content into resume.
func readControlRunPrompt(runDir, fallback string) (string, error) {
	data, present, err := readControlOptionalArtifactBytes(runDir, "input.md")
	if err != nil {
		return "", err
	}
	if present {
		return string(data), nil
	}
	meta, err := readControlMeta(runDir)
	if err == nil && strings.TrimSpace(meta.Prompt) != "" {
		return meta.Prompt, nil
	}
	if strings.TrimSpace(fallback) != "" {
		return fallback, nil
	}
	return "", fmt.Errorf("run prompt not found in %s", runDir)
}

func verifyResumeArtifactPath(runDir, path string) error {
	root, err := canonicalPath(runDir, true)
	if err != nil {
		return err
	}
	artifact, err := canonicalPath(path, true)
	if err != nil {
		return err
	}
	if !pathWithin(root, artifact) {
		return fmt.Errorf("resume artifact escapes run directory: %s", path)
	}
	return nil
}

func quarantineResume(runDir string, state RunState, cause error) (FinalRun, error) {
	state.Status = string(RunStatusQuarantined)
	state.RecoveryStatus = cause.Error()
	_ = writeRunState(runDir, state)
	record := ResumeRecord{SchemaVersion: ArtifactSchemaVersion, SourceRunID: state.RunID, VerifiedPhase: normalizeRunPhase(state.Phase), Baseline: state.BaselineSHA, DiffHash: state.DiffHash, Status: "quarantined", Message: cause.Error(), CreatedAt: time.Now().UTC()}
	_ = writeJSONWithNewline(filepath.Join(runDir, "resume.json"), record)
	final := FinalRun{RunID: state.RunID, RunDir: runDir, Workdir: state.Workdir, Baseline: state.BaselineSHA, Mode: state.Mode, Status: RunStatusQuarantined, Verdict: "quarantined", BlockingReason: string(ReasonQuarantined), ExitCode: ExitAdapterFailure}
	return final, &ExitError{Code: ExitAdapterFailure, Err: cause}
}

func metaMode(meta Meta) Mode {
	if _, ok := meta.Adapters["solo"]; ok {
		return ModeSolo
	}
	if _, ok := meta.Adapters["worker"]; ok {
		return ModeSupervisor
	}
	if _, ok := meta.Adapters["scout"]; ok {
		return ModeRelay
	}
	return ModeAdversarial
}

func restoreTargetsFromMeta(opts *RunOptions, meta Meta) {
	editor, reviewer := roleLabels(opts.Mode)
	if adapter := strings.TrimSpace(meta.Adapters[editor]); adapter != "" {
		opts.Coder = RoleTarget{Adapter: adapter, Model: meta.Models[editor]}
	}
	if opts.Mode != ModeSolo {
		if adapter := strings.TrimSpace(meta.Adapters[reviewer]); adapter != "" {
			opts.Adversary = RoleTarget{Adapter: adapter, Model: meta.Models[reviewer]}
		}
	}
	if opts.Mode == ModeRelay {
		if adapter := strings.TrimSpace(meta.Adapters["scout"]); adapter != "" {
			opts.Scout = RoleTarget{Adapter: adapter, Model: meta.Models["scout"]}
		}
	}
}
