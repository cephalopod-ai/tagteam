package tagteam

import "context"

func (r *ControlRuntime) registerJob(runID string, cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.jobs[runID] = cancel
	if r.watcherCancel != nil {
		return
	}
	watcherContext, watcherCancel := context.WithCancel(context.Background())
	r.watcherCancel = watcherCancel
	r.watcherDone = make(chan struct{})
	go r.watchControlCancellation(watcherContext, r.watcherDone)
}

func (r *ControlRuntime) unregisterJob(runID string) {
	r.mu.Lock()
	delete(r.jobs, runID)
	if len(r.jobs) != 0 || r.watcherCancel == nil {
		r.mu.Unlock()
		return
	}
	watcherCancel := r.watcherCancel
	watcherDone := r.watcherDone
	r.watcherCancel = nil
	r.watcherDone = nil
	r.mu.Unlock()

	watcherCancel()
	<-watcherDone
}

// startJob registers and tracks one runtime-owned worker. Close is allowed to
// return only after every tracked worker has observed cancellation and exited;
// otherwise callers can tear down state directories while a run still writes.
func (r *ControlRuntime) startJob(runID string, cancel context.CancelFunc, job func()) {
	r.registerJob(runID, cancel)
	r.workers.Add(1)
	go func() {
		defer r.workers.Done()
		defer r.unregisterJob(runID)
		job()
	}()
}

// Close cancels every job owned by this local runtime. MCP stdio calls it when
// the host disconnects so its worker processes are not left running after a
// normal transport shutdown. Socket daemons call it once, after client sessions
// have drained. Close is idempotent and does not return while workers can still
// mutate run state.
func (r *ControlRuntime) Close() {
	r.closeOnce.Do(func() {
		// Exclude a concurrent Start until its worker is registered. After closed
		// is set, later starts fail before consuming an approval nonce.
		r.lifecycleMu.Lock()
		r.mu.Lock()
		r.closed = true
		cancels := make([]context.CancelFunc, 0, len(r.jobs))
		for _, cancel := range r.jobs {
			cancels = append(cancels, cancel)
		}
		r.jobs = map[string]context.CancelFunc{}
		r.stewards = map[string]Steward{}
		watcherCancel := r.watcherCancel
		watcherDone := r.watcherDone
		r.watcherCancel = nil
		r.watcherDone = nil
		r.mu.Unlock()
		r.lifecycleMu.Unlock()

		for _, cancel := range cancels {
			cancel()
		}
		if watcherCancel != nil {
			watcherCancel()
			<-watcherDone
		}
		r.workers.Wait()
		r.mu.Lock()
		r.terminalErrors = map[string]error{}
		r.mu.Unlock()
	})
}

func (r *ControlRuntime) recordTerminalError(runID string, err error) {
	if err == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.terminalErrors) >= controlMaxApprovalRecords {
		return
	}
	r.terminalErrors[runID] = err
}

func (r *ControlRuntime) terminalError(runID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.terminalErrors[runID]
}
