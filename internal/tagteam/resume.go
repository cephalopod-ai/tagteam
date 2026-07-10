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
	lock, err := acquireRunLock(runDir, true)
	if err != nil {
		return FinalRun{}, &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	defer lock.Release()
	state, err := readRunState(runDir)
	if err != nil {
		return FinalRun{}, &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("read resumable state: %w", err)}
	}
	if state.SchemaVersion < runStateSchemaVersion {
		return FinalRun{}, &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("legacy run state schema %d is readable but not resumable", state.SchemaVersion)}
	}
	meta, err := readMeta(filepath.Join(runDir, "meta.json"))
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
	patch, err := deterministicDiffPatch(ctx, opts.Workdir, meta.Baseline, filepath.Join(runDir, "resume-verify.index"))
	if err != nil {
		return FinalRun{}, err
	}
	currentDiffHash := sha256Sum(patch)
	phase := normalizeRunPhase(state.Phase)
	if state.DiffHash != "" && state.DiffHash != currentDiffHash && phase != PhaseImplementing && phase != PhaseRepairing {
		return quarantineResume(runDir, state, fmt.Errorf("worktree diff hash changed after completed %s phase", phase))
	}
	if err := verifyResumeArtifacts(runDir, state); err != nil {
		return quarantineResume(runDir, state, err)
	}
	prompt, err := readRunPrompt(runDir, opts.Prompt)
	if err != nil {
		return FinalRun{}, err
	}
	saved, _ := readFinal(filepath.Join(runDir, "final.json"))
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
	record := ResumeRecord{SchemaVersion: ArtifactSchemaVersion, SourceRunID: runID, VerifiedPhase: phase, Baseline: meta.Baseline, DiffHash: currentDiffHash, Status: "verified", CreatedAt: time.Now().UTC()}
	_ = writeJSONWithNewline(filepath.Join(runDir, "resume.json"), record)
	var prior *Review
	if state.LatestReviewPath != "" {
		if review, reviewErr := readReviewArtifact(state.LatestReviewPath); reviewErr == nil {
			prior = &review
		}
	}
	continued, err := a.resumeExistingRun(ctx, opts, runDir, meta, state, saved, prior, currentDiffHash)
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

func verifyResumeArtifacts(runDir string, state RunState) error {
	if state.LatestDiffPath != "" {
		if err := verifyResumeArtifactPath(runDir, state.LatestDiffPath); err != nil {
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
		if err := verifyResumeArtifactPath(runDir, state.LatestReviewPath); err != nil {
			return err
		}
		if _, err := readReviewArtifact(state.LatestReviewPath); err != nil {
			return fmt.Errorf("latest review artifact is invalid: %w", err)
		}
	}
	events, err := os.ReadFile(filepath.Join(runDir, "events.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read state journal: %w", err)
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
