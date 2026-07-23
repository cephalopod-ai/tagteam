package tagteam

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"
)

const defaultSoftWatchdogTimeout = 5 * time.Minute

func softWatchdogTimeout(req Request) time.Duration {
	if req.WatchdogTimeout > 0 {
		return req.WatchdogTimeout
	}
	return defaultSoftWatchdogTimeout
}

func softWatchdogTickInterval(timeout time.Duration) time.Duration {
	interval := 30 * time.Second
	if timeout/4 < interval {
		interval = timeout / 4
	}
	if interval < time.Second {
		return time.Second
	}
	return interval
}

func liveProgressFingerprint(progress LiveProgress, outputPath string) string {
	return fmt.Sprintf("%s:%d:%d:%s", progress.DiffHash, progress.StdoutBytes, progress.StderrBytes, outputArtifactFingerprint(outputPath))
}

// startSoftProgressMonitor records host-observed progress without using a
// lack of output as authority to terminate a live provider process. Provider
// output, output-artifact changes, and worktree changes reset the soft timer;
// the request timeout and total run wall-time remain the actual hard limits.
func startSoftProgressMonitor(
	ctx context.Context,
	req Request,
	role Role,
	phase string,
	started time.Time,
	syncOutput func(),
) func() {
	lastActivity := req.ProgressLastActivity
	if lastActivity == nil {
		initial := started
		lastActivity = &initial
		req.ProgressLastActivity = lastActivity
	}
	initialProgress, _ := writeLiveProgress(ctx, req, role, phase, started, "running")
	lastFingerprint := liveProgressFingerprint(initialProgress, req.OutputPath)
	softTimeout := softWatchdogTimeout(req)
	tickInterval := softWatchdogTickInterval(softTimeout)
	done := make(chan struct{})
	stopped := make(chan struct{})
	var stopOnce sync.Once

	go func() {
		defer close(stopped)
		ticker := time.NewTicker(tickInterval)
		defer ticker.Stop()
		softAlerted := false
		for {
			select {
			case <-ticker.C:
				if syncOutput != nil {
					syncOutput()
				}
				progress, err := writeLiveProgress(ctx, req, role, phase, started, "running")
				fingerprint := liveProgressFingerprint(progress, req.OutputPath)
				if fingerprint != lastFingerprint {
					lastFingerprint = fingerprint
					*lastActivity = time.Now()
					softAlerted = false
					progress, err = writeLiveProgress(ctx, req, role, phase, started, "running")
				}
				if time.Since(*lastActivity) >= softTimeout {
					if !softAlerted && !req.Quiet {
						logRequestProgress(req, "%s awaiting telemetry elapsed=%s idle=%s; preserving the live process until its hard timeout", phase, shortDuration(time.Since(started)), shortDuration(time.Since(*lastActivity)))
					}
					softAlerted = true
					var alertErr error
					progress, alertErr = writeLiveProgress(ctx, req, role, phase, started, "awaiting_telemetry")
					if alertErr != nil {
						err = alertErr
					}
				}
				if !req.Quiet {
					if err != nil {
						logRequestProgress(req, "%s still running elapsed=%s progress_error=%q", phase, shortDuration(time.Since(started)), err.Error())
					} else {
						logRequestProgress(
							req,
							"%s still running elapsed=%s idle=%s files=%d +%d -%d status=%s progress=%s",
							phase,
							shortDuration(time.Since(started)),
							progress.NoProgressFor,
							progress.FilesChanged,
							progress.Additions,
							progress.Deletions,
							progress.Status,
							filepath.Join(req.RunDir, liveProgressArtifact),
						)
					}
				}
			case <-done:
				return
			}
		}
	}()

	return func() {
		stopOnce.Do(func() {
			close(done)
			<-stopped
		})
	}
}
