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

// startLiveScopeGuard stops a mutating editor after an invocation-local
// worktree delta persists across two observations outside the host-approved
// write boundary. The one-poll settle window avoids cancelling valid tests
// that briefly create and remove temporary artifacts. It intentionally never
// removes a persistent partial diff: recovery records preserve it for review.
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
	pending := map[string]time.Time{}

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
				paths := settledOutOfScopeDeltaPaths(before, after, allowed, pending, time.Now())
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

// settledOutOfScopeDeltaPaths returns paths that remain out of scope for a
// full poll interval. A path that disappears before the next observation is a
// transient test artifact rather than a persistent editor side effect.
func settledOutOfScopeDeltaPaths(before, after worktreeSnapshot, allowed []string, pending map[string]time.Time, now time.Time) []string {
	current := map[string]bool{}
	for _, path := range outOfScopeDeltaPaths(before, after, allowed) {
		current[path] = true
	}
	for path := range pending {
		if !current[path] {
			delete(pending, path)
		}
	}
	paths := []string{}
	for path := range current {
		firstSeen, ok := pending[path]
		if !ok {
			pending[path] = now
			continue
		}
		if now.Sub(firstSeen) >= liveScopeGuardInterval {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	return paths
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
