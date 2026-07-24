package tagteam

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

const liveScopeGuardInterval = time.Second

// LiveScopeViolationError reports the first observed editor write that falls
// outside host-derived scope. The partial diff remains for operator review.
type LiveScopeViolationError struct {
	Paths []string
}

func (e *LiveScopeViolationError) Error() string {
	return fmt.Sprintf("live scope guard cancelled editor after out-of-scope write(s): %s", strings.Join(e.Paths, ", "))
}

func isLiveScopeViolation(err error) bool {
	var violation *LiveScopeViolationError
	return errors.As(err, &violation)
}

// startLiveScopeGuard stops a mutating editor as soon as its invocation-local
// worktree delta crosses the host-approved write boundary. It intentionally
// never removes the partial diff: recovery records preserve it for inspection.
func startLiveScopeGuard(ctx context.Context, req Request, before worktreeSnapshot, cancel context.CancelFunc) func() error {
	if !req.RequireWorkerContract || req.Workdir == "" || len(req.AllowedScope) == 0 {
		return func() error { return nil }
	}
	allowed := normalizeAllowedScope(req.AllowedScope)
	if len(allowed) == 0 {
		return func() error { return nil }
	}
	done := make(chan struct{})
	stopped := make(chan struct{})
	var stopOnce sync.Once
	var mu sync.Mutex
	var violation error

	go func() {
		defer close(stopped)
		ticker := time.NewTicker(liveScopeGuardInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				after, err := captureWorktreeSnapshot(context.Background(), req.Workdir)
				if err != nil {
					continue
				}
				paths := outOfScopeDeltaPaths(before, after, allowed)
				if len(paths) == 0 {
					continue
				}
				mu.Lock()
				if violation == nil {
					violation = &LiveScopeViolationError{Paths: paths}
					logRequestProgress(req, "%s scope violation detected paths=%s; cancelling editor", req.Phase, strings.Join(paths, ","))
					_, _ = writeLiveProgress(context.Background(), req, req.ProgressRole, req.Phase, time.Now(), "scope_violation")
					cancel()
				}
				mu.Unlock()
				return
			case <-done:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	return func() error {
		stopOnce.Do(func() {
			close(done)
			<-stopped
		})
		// Catch a fast write that completed between polling ticks.
		after, snapshotErr := captureWorktreeSnapshot(context.Background(), req.Workdir)
		paths := []string(nil)
		if snapshotErr == nil {
			paths = outOfScopeDeltaPaths(before, after, allowed)
		}
		mu.Lock()
		defer mu.Unlock()
		if violation == nil && len(paths) > 0 {
			violation = &LiveScopeViolationError{Paths: paths}
		}
		return violation
	}
}

func outOfScopeDeltaPaths(before, after worktreeSnapshot, allowed []string) []string {
	paths := []string{}
	for _, path := range worktreeDelta(before, after) {
		if hostDeniedPath(path) || !pathAllowed(path, allowed) {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	return paths
}
