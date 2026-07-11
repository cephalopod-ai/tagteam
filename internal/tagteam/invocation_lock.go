package tagteam

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// The invocation lock serializes subprocess invocations for adapters whose
// CLIs misbehave when several copies run concurrently (claude can stall or
// remain pending). It is a cross-process lock below the run-state root, so
// concurrent tagteam runs take turns instead of overlapping. On Unix the
// kernel flock is the lock, so a crashed holder releases automatically; the
// file content only records the holder PID for diagnostics and the file
// intentionally persists after release (unlinking a locked path would race
// with waiters that already opened the old inode).

type invocationLock struct {
	file *os.File
	path string
	pid  int
}

func invocationLockPath(adapterID string, req Request) (string, error) {
	root := stateRootFromRunDir(req.RunDir)
	if root == "" {
		var err error
		root, err = defaultStateRoot()
		if err != nil {
			return "", err
		}
	}
	return filepath.Join(root, "locks", adapterID+"-invocation.lock"), nil
}

// stateRootFromRunDir recovers the resolved state root from the canonical
// run directory layout <state-root>/<repo-id>/runs/<run-id>, so the lock
// honors --state-root / profile overrides applied when the run was created.
// The legacy in-worktree layout <workdir>/.tagteam/runs/<run-id> is rejected:
// a lock inside the worktree would dirty it and trip the integrity gate.
func stateRootFromRunDir(runDir string) string {
	if runDir == "" {
		return ""
	}
	runsDir := filepath.Dir(filepath.Clean(runDir))
	if filepath.Base(runsDir) != "runs" {
		return ""
	}
	repoDir := filepath.Dir(runsDir)
	if filepath.Base(repoDir) == ".tagteam" {
		return ""
	}
	return filepath.Dir(repoDir)
}

func readInvocationHolderPID(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var record runLockRecord
	if json.Unmarshal(data, &record) != nil {
		return 0
	}
	return record.PID
}

func (l *invocationLock) recordHolder() {
	if l == nil || l.file == nil {
		return
	}
	data, err := marshalJSON(runLockRecord{PID: l.pid, CreatedAt: time.Now().UTC()}, true)
	if err != nil {
		return
	}
	if err := l.file.Truncate(0); err != nil {
		return
	}
	if _, err := l.file.Seek(0, 0); err != nil {
		return
	}
	_, _ = l.file.Write(data)
	_ = l.file.Sync()
}

// acquireInvocationSlot blocks until the adapter's cross-process lock is
// held, the context is cancelled, or maxWait elapses. Every failure path is
// fail-closed: a timeout or lock-infrastructure error returns a classified
// adapter failure (so role fallback policies can engage) instead of running
// unlocked and reintroducing the concurrency this lock exists to prevent.
// onWait, when non-nil, is called with the holder PID when contention is
// first observed and again on each periodic wait log, so callers can surface
// the queued state to live-status consumers.
func acquireInvocationSlot(ctx context.Context, adapterID string, req Request, maxWait time.Duration, onWait func(holderPID int)) (func(), error) {
	failure := func(err error) (func(), error) {
		return func() {}, &ExitError{Code: ExitAdapterFailure, Err: err}
	}
	path, err := invocationLockPath(adapterID, req)
	if err == nil {
		err = os.MkdirAll(filepath.Dir(path), 0o700)
	}
	if err != nil {
		return failure(fmt.Errorf("%s invocation lock unavailable: %w", adapterID, err))
	}
	if maxWait <= 0 {
		maxWait = 15 * time.Minute
	}
	start := time.Now()
	deadline := start.Add(maxWait)
	nextLog := start
	nextStatus := start
	for {
		lock, holder, tryErr := tryLockInvocationFile(path)
		if tryErr != nil {
			return failure(fmt.Errorf("%s invocation lock unavailable: %w", adapterID, tryErr))
		}
		if lock != nil {
			lock.recordHolder()
			if waited := time.Since(start); waited >= time.Second {
				logRequestProgress(req, "%s invocation lock acquired after %s", adapterID, shortDuration(waited))
			}
			return func() { _ = lock.Release() }, nil
		}
		now := time.Now()
		if now.After(deadline) {
			return failure(fmt.Errorf("timed out after %s waiting for the %s invocation lock held by pid %d; another process is running %s (stagger runs or set adapters.%s.serialize = false)", shortDuration(maxWait), adapterID, holder, adapterID, adapterID))
		}
		if !now.Before(nextStatus) {
			if onWait != nil {
				onWait(holder)
			}
			nextStatus = now.Add(time.Second)
		}
		if !now.Before(nextLog) {
			logRequestProgress(req, "waiting for concurrent %s invocation (pid %d) to finish before starting", adapterID, holder)
			nextLog = now.Add(30 * time.Second)
		}
		select {
		case <-ctx.Done():
			return failure(fmt.Errorf("cancelled while waiting for %s invocation lock held by pid %d: %w", adapterID, holder, ctx.Err()))
		case <-time.After(500 * time.Millisecond):
		}
	}
}
