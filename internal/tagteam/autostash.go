package tagteam

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

type AutostashRecovery struct {
	SchemaVersion int       `json:"schema_version"`
	RunID         string    `json:"run_id,omitempty"`
	Status        string    `json:"status"`
	StashOID      string    `json:"stash_oid"`
	StashRef      string    `json:"stash_ref,omitempty"`
	Error         string    `json:"error"`
	SafeCommands  []string  `json:"safe_commands"`
	CreatedAt     time.Time `json:"created_at"`
}

func gitAutostash(workdir, runID string) (string, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("create autostash identity: %w", err)}
	}
	message := "tagteam-autostash:" + runID + ":" + hex.EncodeToString(nonce)
	if _, err := runCommand(context.Background(), workdir, "git", "stash", "push", "-u", "-m", message); err != nil {
		return "", &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	list, err := runCommand(context.Background(), workdir, "git", "stash", "list", "--format=%H%x09%gs")
	if err != nil {
		return "", &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("list autostash objects: %w", err)}
	}
	var oid string
	for _, line := range strings.Split(strings.TrimSpace(list), "\n") {
		candidate, subject, ok := strings.Cut(line, "\t")
		if ok && strings.HasSuffix(strings.TrimSpace(subject), message) {
			oid = strings.TrimSpace(candidate)
			break
		}
	}
	if (len(oid) != 40 && len(oid) != 64) || !validHexObjectID(oid) {
		return "", &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("resolve created autostash object: invalid object ID")}
	}
	return oid, nil
}

func validHexObjectID(value string) bool {
	_, err := hex.DecodeString(value)
	return err == nil
}

func restoreAutostash(workdir, runDir, stashOID string) error {
	if _, err := runCommand(context.Background(), workdir, "git", "stash", "apply", stashOID); err != nil {
		stashRef, _ := findStashRefByOID(workdir, stashOID)
		return autostashRecoveryError(runDir, stashOID, stashRef, "apply_failed", err)
	}
	// Keep the successfully applied stash entry. Git only drops stash entries by
	// positional reflog selector, which can move if another process pushes a
	// stash between lookup and drop. Retention is safer than deleting unrelated
	// user work and preserves an immutable recovery point.
	return nil
}

func findStashRefByOID(workdir, stashOID string) (string, error) {
	out, err := runCommand(context.Background(), workdir, "git", "stash", "list", "--format=%H%x09%gd")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		oid, ref, ok := strings.Cut(line, "\t")
		if ok && strings.TrimSpace(oid) == stashOID {
			return strings.TrimSpace(ref), nil
		}
	}
	return "", nil
}

func autostashRecoveryError(runDir, stashOID, stashRef, status string, cause error) error {
	runID := ""
	if runDir != "" {
		runID = filepath.Base(runDir)
	}
	recovery := AutostashRecovery{
		SchemaVersion: ArtifactSchemaVersion,
		RunID:         runID,
		Status:        status,
		StashOID:      stashOID,
		StashRef:      stashRef,
		Error:         cause.Error(),
		SafeCommands: []string{
			"git status --short",
			"git stash list --format='%H %gd %gs'",
			"git stash apply " + stashOID,
		},
		CreatedAt: time.Now().UTC(),
	}
	var persistErr error
	if runDir == "" {
		persistErr = fmt.Errorf("run directory unavailable for autostash recovery artifact")
	} else {
		persistErr = writeJSONWithNewline(filepath.Join(runDir, "autostash-recovery.json"), recovery)
	}
	return &ExitError{
		Code: ExitAdapterFailure,
		Err: errors.Join(
			fmt.Errorf("restore autostash %s (%s): %w", stashOID, status, cause),
			mandatoryPersistenceError("autostash recovery artifact", persistErr),
		),
	}
}

func (a *App) finishPreflightCleanup(opts RunOptions, runDir string, cleanup preflightCleanup, final *FinalRun, runErr *error) {
	if cleanup == nil {
		return
	}
	cleanupErr := cleanup(runDir)
	if cleanupErr == nil {
		return
	}
	*runErr = errors.Join(*runErr, cleanupErr)
	if final == nil || final.RunID == "" || runDir == "" {
		return
	}
	message := "autostash restoration failed; original changes remain recoverable by object ID"
	if strings.TrimSpace(final.Summary) == "" {
		final.Summary = message
	} else {
		final.Summary = strings.TrimSpace(final.Summary) + "\n\n" + message
	}
	final.ExitCode = ExitAdapterFailure
	final.Status = RunStatusFailed
	final.Verdict = "error"
	final.BlockingReason = string(ReasonAutostashRestore)
	final.FinishedAt = time.Now().UTC()
	state := runStateForFinal(*final, opts.Mode, final.Phase, "autostash_restore_failed")
	if persistErr := a.persistTerminalRun(opts.Workdir, final, state); persistErr != nil {
		*runErr = errors.Join(*runErr, persistErr)
	}
}
