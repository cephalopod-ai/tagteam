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
// remain pending). It is a cross-process PID lock below the user-level state
// root, so concurrent tagteam runs take turns instead of overlapping.

type invocationLock struct {
	path string
	pid  int
}

func invocationLockPath(adapterID string) (string, error) {
	root, err := defaultStateRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "locks", adapterID+"-invocation.lock"), nil
}

// tryAcquireInvocationLock attempts one non-blocking acquisition. It returns
// the held lock, or the live holder PID when the lock is busy. Records whose
// process is gone are treated as stale and removed.
func tryAcquireInvocationLock(path string) (*invocationLock, int, error) {
	if data, err := os.ReadFile(path); err == nil {
		var existing runLockRecord
		if json.Unmarshal(data, &existing) == nil && existing.PID > 0 && existing.PID != os.Getpid() && processAlive(existing.PID) {
			return nil, existing.PID, nil
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return nil, 0, err
		}
	} else if !os.IsNotExist(err) {
		return nil, 0, err
	}
	record := runLockRecord{PID: os.Getpid(), CreatedAt: time.Now().UTC()}
	data, err := marshalJSON(record, true)
	if err != nil {
		return nil, 0, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, 0, err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return nil, 0, err
	}
	return &invocationLock{path: path, pid: os.Getpid()}, 0, nil
}

func (l *invocationLock) Release() error {
	if l == nil || l.path == "" {
		return nil
	}
	data, err := os.ReadFile(l.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var existing runLockRecord
	if json.Unmarshal(data, &existing) != nil || existing.PID != l.pid {
		return nil
	}
	return os.Remove(l.path)
}

// acquireInvocationSlot blocks until the adapter's cross-process lock is
// free, the context is cancelled, or maxWait elapses. Lock infrastructure
// failures and a wait timeout degrade to running unlocked (current behavior
// without serialization) and are reported through the request progress log;
// only context cancellation aborts the invocation.
func acquireInvocationSlot(ctx context.Context, adapterID string, req Request, maxWait time.Duration) (func(), error) {
	unlocked := func() {}
	path, err := invocationLockPath(adapterID)
	if err == nil {
		err = os.MkdirAll(filepath.Dir(path), 0o700)
	}
	if err != nil {
		logRequestProgress(req, "%s invocation serialization unavailable (%v); continuing without it", adapterID, err)
		return unlocked, nil
	}
	if maxWait <= 0 {
		maxWait = 15 * time.Minute
	}
	start := time.Now()
	deadline := start.Add(maxWait)
	nextLog := start
	for {
		lock, holder, tryErr := tryAcquireInvocationLock(path)
		if tryErr != nil {
			logRequestProgress(req, "%s invocation serialization unavailable (%v); continuing without it", adapterID, tryErr)
			return unlocked, nil
		}
		if lock != nil {
			if waited := time.Since(start); waited >= time.Second {
				logRequestProgress(req, "%s invocation lock acquired after %s", adapterID, shortDuration(waited))
			}
			return func() { _ = lock.Release() }, nil
		}
		now := time.Now()
		if now.After(deadline) {
			logRequestProgress(req, "%s invocation lock still held by pid %d after %s; continuing without serialization", adapterID, holder, shortDuration(maxWait))
			return unlocked, nil
		}
		if !now.Before(nextLog) {
			logRequestProgress(req, "waiting for concurrent %s invocation (pid %d) to finish before starting", adapterID, holder)
			nextLog = now.Add(30 * time.Second)
		}
		select {
		case <-ctx.Done():
			return unlocked, &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("cancelled while waiting for %s invocation lock held by pid %d: %w", adapterID, holder, ctx.Err())}
		case <-time.After(500 * time.Millisecond):
		}
	}
}
