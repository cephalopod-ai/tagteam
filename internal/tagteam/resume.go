package tagteam

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// controlResumePostLockHook is a narrowly scoped test seam invoked once after
// MCP ResumeControl acquires the run lock and re-validates the run directory.
// Production code leaves it nil.
var controlResumePostLockHook func()

// controlResumeAfterStateReadHook is a narrowly scoped test seam invoked after
// state.json has been read during MCP resume, before subsequent artifact reads.
// Production code leaves it nil.
var controlResumeAfterStateReadHook func()

// controlResumeBeforeAdapterHook is a narrowly scoped test seam invoked inside
// runAdapter after the control-resume gate is bound, before dispatch. Production
// code leaves it nil.
var controlResumeBeforeAdapterHook func()

// controlResumeBeforeContractRetryHook is a narrowly scoped test seam invoked
// after an output-contract failure is selected for a single retry and before
// the retry-prompt path is rebound/written. Production code leaves it nil.
var controlResumeBeforeContractRetryHook func()

// controlResumeAfterRepairMkdirHook is a narrowly scoped test seam invoked
// after JSON-repair MkdirAll and before the repair-prompt write rebind.
// Production code leaves it nil.
var controlResumeAfterRepairMkdirHook func()

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
	return a.resumeAtRunDir(ctx, opts, runID, runDir, nil)
}

// ResumeControl is the MCP-owned resume entry point. It carries the canonical
// runs-root boundary through resumed execution and re-resolves the run
// directory immediately before lock, mutation, and adapter dispatch.
func (a *App) ResumeControl(ctx context.Context, opts RunOptions, runID string) (FinalRun, error) {
	ctx = context.WithValue(ctx, maxOutputBytesContextKey{}, opts.MaxOutputBytes)
	gate, err := newControlResumePathGate(opts.Workdir, opts.StateRoot, runID)
	if err != nil {
		return FinalRun{}, &ExitError{Code: ExitInvalidArguments, Err: err}
	}
	ctx = withControlResumeGate(ctx, gate)
	return a.resumeAtRunDir(ctx, opts, runID, gate.runDir, gate)
}

func (a *App) resumeAtRunDir(ctx context.Context, opts RunOptions, runID, runDir string, gate *controlResumePathGate) (FinalRun, error) {
	controlSafe := gate != nil
	if controlSafe {
		ctx = withControlResumeGate(ctx, gate)
	}
	lock, err := acquireRunLock(runDir, true)
	if err != nil {
		return FinalRun{}, &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	defer lock.Release()
	if gate != nil {
		// Re-resolve after lock: refuse if the run directory escaped while
		// waiting for exclusive ownership.
		resolved, err := gate.current()
		if err != nil {
			return FinalRun{}, &ExitError{Code: ExitPreflightFailed, Err: err}
		}
		if resolved != runDir {
			return FinalRun{}, &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("run %q path changed under the resolved state root", runID)}
		}
		runDir = resolved
		if controlResumePostLockHook != nil {
			controlResumePostLockHook()
		}
	}
	// Re-bind under the runs-root boundary before any post-lock artifact I/O.
	if current, err := rebindControlResumeRunDir(gate, runDir, nil); err != nil {
		return FinalRun{}, &ExitError{Code: ExitPreflightFailed, Err: err}
	} else {
		runDir = current
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
	if controlSafe && controlResumeAfterStateReadHook != nil {
		controlResumeAfterStateReadHook()
	}
	// Re-resolve before meta and every subsequent artifact read.
	if runDir, err = rebindControlResumeRunDir(gate, runDir, nil, "meta.json"); err != nil {
		return FinalRun{}, &ExitError{Code: ExitPreflightFailed, Err: err}
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
		return quarantineResume(runDir, state, fmt.Errorf("current HEAD %s does not match run baseline %s", currentHead, meta.Baseline), gate)
	}
	if controlSafe {
		var readyErr error
		runDir, readyErr = rebindControlResumeRunDir(gate, runDir, nil, "resume-verify.index", "events.jsonl", "input.md", "resume.json")
		if readyErr != nil {
			return FinalRun{}, &ExitError{Code: ExitPreflightFailed, Err: readyErr}
		}
	}
	patch, err := deterministicDiffPatch(ctx, opts.Workdir, meta.Baseline, filepath.Join(runDir, "resume-verify.index"))
	if err != nil {
		return FinalRun{}, err
	}
	currentDiffHash := sha256Sum(patch)
	phase := normalizeRunPhase(state.Phase)
	if state.DiffHash != "" && state.DiffHash != currentDiffHash && phase != PhaseImplementing && phase != PhaseRepairing {
		return quarantineResume(runDir, state, fmt.Errorf("worktree diff hash changed after completed %s phase", phase), gate)
	}
	if runDir, err = rebindControlResumeRunDir(gate, runDir, nil); err != nil {
		return FinalRun{}, &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	if err := verifyResumeArtifacts(runDir, state, controlSafe, gate); err != nil {
		return quarantineResume(runDir, state, err, gate)
	}
	var prompt string
	if controlSafe {
		if runDir, err = rebindControlResumeRunDir(gate, runDir, nil); err != nil {
			return FinalRun{}, &ExitError{Code: ExitPreflightFailed, Err: err}
		}
		prompt, err = readControlRunPrompt(ctx, runDir, opts.Prompt)
	} else {
		prompt, err = readRunPrompt(runDir, opts.Prompt)
	}
	if err != nil {
		return FinalRun{}, err
	}
	if runDir, err = rebindControlResumeRunDir(gate, runDir, nil, "final.json"); err != nil {
		return FinalRun{}, &ExitError{Code: ExitPreflightFailed, Err: err}
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
	if opts.Mode == "" {
		opts.Mode = metaMode(meta)
	}
	if err := applySavedResumeTargets(&opts, saved); err != nil {
		return FinalRun{}, err
	}
	if opts.Coder.Adapter == "" || (opts.Mode != ModeSolo && opts.Adversary.Adapter == "") {
		restoreTargetsFromMeta(&opts, meta)
	}
	if controlSafe {
		var readyErr error
		runDir, readyErr = rebindControlResumeRunDir(gate, runDir, nil, "resume.json")
		if readyErr != nil {
			return FinalRun{}, &ExitError{Code: ExitPreflightFailed, Err: readyErr}
		}
	}
	record := ResumeRecord{SchemaVersion: ArtifactSchemaVersion, SourceRunID: runID, VerifiedPhase: phase, Baseline: meta.Baseline, DiffHash: currentDiffHash, Status: "verified", CreatedAt: time.Now().UTC()}
	if err := writeJSONWithNewline(filepath.Join(runDir, "resume.json"), record); err != nil {
		return FinalRun{}, mandatoryPersistenceError("verified resume record", err)
	}
	var prior *Review
	if state.LatestReviewPath != "" {
		if runDir, err = rebindControlResumeRunDir(gate, runDir, nil); err != nil {
			return FinalRun{}, &ExitError{Code: ExitPreflightFailed, Err: err}
		}
		if controlSafe {
			if err := validateControlWritablePath(runDir, state.LatestReviewPath); err != nil {
				return FinalRun{}, &ExitError{Code: ExitPreflightFailed, Err: err}
			}
			if gate != nil {
				if err := validateControlPathWithinBoundary(gate.runsRoot, state.LatestReviewPath, "resolved state root"); err != nil {
					return FinalRun{}, &ExitError{Code: ExitPreflightFailed, Err: err}
				}
			}
		}
		var review Review
		var reviewErr error
		if controlSafe {
			review, reviewErr = readControlReviewArtifact(runDir, state.LatestReviewPath)
		} else {
			review, reviewErr = readReviewArtifact(state.LatestReviewPath)
		}
		if reviewErr == nil {
			prior = &review
		}
	}
	continued, err := a.resumeExistingRun(ctx, opts, runDir, meta, state, saved, prior, currentDiffHash, gate)
	record.ContinuedRunID = runID
	record.Status = "resumed"
	if err != nil {
		if continued.FinishedAt.IsZero() {
			record.Status = "resume_failed"
		}
		record.Message = err.Error()
	}
	if writeDir, writeErr := rebindControlResumeRunDir(gate, runDir, nil, "resume.json"); writeErr != nil {
		// Fail closed: do not persist resume records through a replaced run dir.
		if err == nil {
			err = &ExitError{Code: ExitPreflightFailed, Err: writeErr}
		}
		return continued, err
	} else {
		runDir = writeDir
	}
	if recordErr := writeJSONWithNewline(filepath.Join(runDir, "resume.json"), record); recordErr != nil {
		err = errors.Join(err, mandatoryPersistenceError("terminal resume record", recordErr))
	}
	return continued, err
}

// applySavedResumeTargets retains a run's recorded roles unless the operator
// explicitly selects a compatible replacement for this resume attempt.
func applySavedResumeTargets(opts *RunOptions, saved FinalRun) error {
	if err := validateExplicitTargetModes(*opts); err != nil {
		return err
	}
	if !opts.CoderExplicit && saved.Coder.Adapter != "" {
		opts.Coder = saved.Coder
	}
	if !opts.AdversaryExplicit && saved.Adversary.Adapter != "" {
		opts.Adversary = saved.Adversary
	}
	if !opts.ScoutExplicit && saved.Scout.Adapter != "" {
		opts.Scout = saved.Scout
	}
	return nil
}

// verifyResumeArtifacts checks diff/review/journal integrity. controlSafe uses
// control artifact readers. When gate is set (MCP resume execution), the run
// directory is re-resolved and paths are checked against the original runs-root
// boundary so a post-state-read replacement cannot redefine the trust root.
func verifyResumeArtifacts(runDir string, state RunState, controlSafe bool, gate *controlResumePathGate) error {
	if gate != nil {
		controlSafe = true
		current, err := gate.ready()
		if err != nil {
			return err
		}
		runDir = current
	}
	if state.LatestDiffPath != "" {
		if gate != nil {
			var err error
			runDir, err = gate.ready()
			if err != nil {
				return err
			}
		}
		if controlSafe {
			if err := validateControlWritablePath(runDir, state.LatestDiffPath); err != nil {
				return err
			}
			if gate != nil {
				if err := validateControlPathWithinBoundary(gate.runsRoot, state.LatestDiffPath, "resolved state root"); err != nil {
					return err
				}
			}
		} else if err := verifyResumeArtifactPath(runDir, state.LatestDiffPath); err != nil {
			return err
		}
		var patch []byte
		var err error
		if controlSafe {
			rel, relErr := filepath.Rel(runDir, state.LatestDiffPath)
			if relErr != nil || strings.HasPrefix(rel, "..") {
				return fmt.Errorf("diff path escapes run directory: %s", state.LatestDiffPath)
			}
			patch, err = readControlArtifactBytes(runDir, rel)
		} else {
			patch, err = os.ReadFile(state.LatestDiffPath)
		}
		if err != nil {
			return fmt.Errorf("read latest diff artifact: %w", err)
		}
		if state.DiffHash != "" && sha256Sum(patch) != state.DiffHash {
			return fmt.Errorf("latest diff artifact hash does not match state")
		}
	}
	if state.LatestReviewPath != "" {
		if gate != nil {
			var err error
			runDir, err = gate.ready()
			if err != nil {
				return err
			}
		}
		if controlSafe {
			if err := validateControlWritablePath(runDir, state.LatestReviewPath); err != nil {
				return err
			}
			if gate != nil {
				if err := validateControlPathWithinBoundary(gate.runsRoot, state.LatestReviewPath, "resolved state root"); err != nil {
					return err
				}
			}
		} else if err := verifyResumeArtifactPath(runDir, state.LatestReviewPath); err != nil {
			return err
		}
		var err error
		if controlSafe {
			_, err = readControlReviewArtifact(runDir, state.LatestReviewPath)
		} else {
			_, err = readReviewArtifact(state.LatestReviewPath)
		}
		if err != nil {
			return fmt.Errorf("latest review artifact is invalid: %w", err)
		}
	}
	var events []byte
	if controlSafe {
		if gate != nil {
			var err error
			runDir, err = gate.ready()
			if err != nil {
				return err
			}
		}
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

func readControlReviewArtifact(runDir, path string) (Review, error) {
	rel, relErr := filepath.Rel(runDir, path)
	if relErr != nil || strings.HasPrefix(rel, "..") {
		return Review{}, fmt.Errorf("review path escapes run directory: %s", path)
	}
	data, err := readControlArtifactBytes(runDir, rel)
	if err != nil {
		return Review{}, err
	}
	var review Review
	if err := json.Unmarshal(data, &review); err != nil {
		return Review{}, err
	}
	return review, nil
}

// readControlRunPrompt loads input.md (or meta prompt) through control-safe
// artifact readers so escaping symlinks cannot feed external content into resume.
// When a control-resume gate is present on ctx, re-resolve before each optional
// read and fail closed on control-artifact read errors (no caller-prompt fallback).
func readControlRunPrompt(ctx context.Context, runDir, fallback string) (string, error) {
	gated := controlResumeGateFrom(ctx) != nil
	current, err := rebindControlResumeFromContext(ctx, runDir, nil)
	if err != nil {
		return "", &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	runDir = current
	data, present, err := readControlOptionalArtifactBytes(runDir, "input.md")
	if err != nil {
		if gated {
			return "", &ExitError{Code: ExitPreflightFailed, Err: err}
		}
		return "", err
	}
	if present {
		return string(data), nil
	}
	current, err = rebindControlResumeFromContext(ctx, runDir, nil)
	if err != nil {
		return "", &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	runDir = current
	meta, err := readControlMeta(runDir)
	if err != nil {
		// Gated control resume must not fall open to the caller prompt when the
		// protected meta artifact is escaping, broken, or otherwise unreadable.
		if gated {
			return "", &ExitError{Code: ExitPreflightFailed, Err: err}
		}
	} else if strings.TrimSpace(meta.Prompt) != "" {
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

func quarantineResume(runDir string, state RunState, cause error, gate *controlResumePathGate) (FinalRun, error) {
	if current, err := rebindControlResumeRunDir(gate, runDir, nil, "state.json", "resume.json"); err != nil {
		// Fail closed: skip writes and surface the path-change condition.
		return FinalRun{}, &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("run directory path changed during quarantine (%v); original cause: %w", err, cause)}
	} else {
		runDir = current
	}
	state.Status = string(RunStatusQuarantined)
	state.RecoveryStatus = cause.Error()
	stateErr := writeRunState(runDir, state)
	record := ResumeRecord{SchemaVersion: ArtifactSchemaVersion, SourceRunID: state.RunID, VerifiedPhase: normalizeRunPhase(state.Phase), Baseline: state.BaselineSHA, DiffHash: state.DiffHash, Status: "quarantined", Message: cause.Error(), CreatedAt: time.Now().UTC()}
	recordErr := writeJSONWithNewline(filepath.Join(runDir, "resume.json"), record)
	final := FinalRun{RunID: state.RunID, RunDir: runDir, Workdir: state.Workdir, Baseline: state.BaselineSHA, Mode: state.Mode, Status: RunStatusQuarantined, Verdict: "quarantined", BlockingReason: string(ReasonQuarantined), ExitCode: ExitAdapterFailure}
	return final, errors.Join(
		&ExitError{Code: ExitAdapterFailure, Err: cause},
		mandatoryPersistenceError("quarantined resume state", stateErr),
		mandatoryPersistenceError("quarantined resume record", recordErr),
	)
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
	if !opts.CoderExplicit && opts.Coder.Adapter == "" {
		if adapter := strings.TrimSpace(meta.Adapters[editor]); adapter != "" {
			opts.Coder = RoleTarget{Adapter: adapter, Model: meta.Models[editor]}
		}
	}
	if opts.Mode != ModeSolo && !opts.AdversaryExplicit && opts.Adversary.Adapter == "" {
		if adapter := strings.TrimSpace(meta.Adapters[reviewer]); adapter != "" {
			opts.Adversary = RoleTarget{Adapter: adapter, Model: meta.Models[reviewer]}
		}
	}
	if opts.Mode == ModeRelay && !opts.ScoutExplicit && opts.Scout.Adapter == "" {
		if adapter := strings.TrimSpace(meta.Adapters["scout"]); adapter != "" {
			opts.Scout = RoleTarget{Adapter: adapter, Model: meta.Models["scout"]}
		}
	}
}
