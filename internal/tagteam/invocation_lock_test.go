package tagteam

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// testInvocationRequest returns a Request whose RunDir places the lock under
// an isolated per-test state root.
func testInvocationRequest(t *testing.T) (Request, string) {
	t.Helper()
	root := t.TempDir()
	runDir := filepath.Join(root, "repo-id", "runs", "run-1")
	return Request{Quiet: true, RunDir: runDir}, root
}

func TestStateRootFromRunDir(t *testing.T) {
	if got := stateRootFromRunDir("/state/repo/runs/run-1"); got != "/state" {
		t.Fatalf("state root = %q", got)
	}
	if got := stateRootFromRunDir("/somewhere/else"); got != "" {
		t.Fatalf("non-canonical run dir should not derive a root, got %q", got)
	}
	if got := stateRootFromRunDir("/work/.tagteam/runs/run-1"); got != "" {
		t.Fatalf("legacy in-worktree run dir must not place the lock in the worktree, got %q", got)
	}
	if got := stateRootFromRunDir(""); got != "" {
		t.Fatalf("empty run dir should not derive a root, got %q", got)
	}
}

func TestTryLockInvocationFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude-invocation.lock")
	lock, holder, err := tryLockInvocationFile(path)
	if err != nil || lock == nil || holder != 0 {
		t.Fatalf("acquire: lock=%v holder=%d err=%v", lock, holder, err)
	}
	lock.recordHolder()

	second, holder, err := tryLockInvocationFile(path)
	if err != nil {
		t.Fatalf("second try: %v", err)
	}
	if second != nil || holder != os.Getpid() {
		t.Fatalf("expected busy lock held by this process, got lock=%v holder=%d", second, holder)
	}

	if err := lock.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	third, _, err := tryLockInvocationFile(path)
	if err != nil || third == nil {
		t.Fatalf("reacquire after release: lock=%v err=%v", third, err)
	}
	_ = third.Release()
}

func TestAcquireInvocationSlotWaitsForRelease(t *testing.T) {
	req, root := testInvocationRequest(t)
	path, err := invocationLockPath("claude", req)
	if err != nil {
		t.Fatalf("lock path: %v", err)
	}
	if !strings.HasPrefix(path, root) {
		t.Fatalf("lock path %q should live under the run's state root %q", path, root)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	holder, _, err := tryLockInvocationFile(path)
	if err != nil || holder == nil {
		t.Fatalf("pre-hold: lock=%v err=%v", holder, err)
	}
	holder.recordHolder()
	go func() {
		time.Sleep(1200 * time.Millisecond)
		_ = holder.Release()
	}()
	start := time.Now()
	waitCalls := 0
	waitHolder := 0
	release, err := acquireInvocationSlot(context.Background(), "claude", req, time.Minute, func(pid int) {
		waitCalls++
		waitHolder = pid
	})
	if err != nil {
		t.Fatalf("acquire slot: %v", err)
	}
	if waited := time.Since(start); waited < time.Second {
		t.Fatalf("expected to wait for the holder, waited %s", waited)
	}
	if waitCalls < 2 || waitHolder != os.Getpid() {
		t.Fatalf("onWait should fire with the holder pid: calls=%d holder=%d", waitCalls, waitHolder)
	}
	release()
}

func TestAcquireInvocationSlotCancelledWhileWaiting(t *testing.T) {
	req, _ := testInvocationRequest(t)
	path, err := invocationLockPath("claude", req)
	if err != nil {
		t.Fatalf("lock path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	holder, _, err := tryLockInvocationFile(path)
	if err != nil || holder == nil {
		t.Fatalf("pre-hold: lock=%v err=%v", holder, err)
	}
	defer holder.Release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := acquireInvocationSlot(ctx, "claude", req, time.Minute, nil); err == nil {
		t.Fatal("expected cancellation error while waiting for a held lock")
	}
}

func TestAcquireInvocationSlotTimeoutFailsClosed(t *testing.T) {
	req, _ := testInvocationRequest(t)
	path, err := invocationLockPath("claude", req)
	if err != nil {
		t.Fatalf("lock path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	holder, _, err := tryLockInvocationFile(path)
	if err != nil || holder == nil {
		t.Fatalf("pre-hold: lock=%v err=%v", holder, err)
	}
	defer holder.Release()
	_, err = acquireInvocationSlot(context.Background(), "claude", req, time.Millisecond, nil)
	if err == nil {
		t.Fatal("timeout must fail closed, not run unlocked")
	}
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != ExitAdapterFailure {
		t.Fatalf("expected classified adapter failure, got %v", err)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error = %q", err.Error())
	}
}

const (
	invocationLockHelperPathEnv = "TAGTEAM_INVOCATION_LOCK_HELPER_PATH"
	invocationLockHelperLogEnv  = "TAGTEAM_INVOCATION_LOCK_HELPER_LOG"
)

// TestInvocationLockMultiProcessMutualExclusion re-executes the test binary
// so several real processes contend on one lock file, then verifies their
// critical sections never overlapped.
func TestInvocationLockMultiProcessMutualExclusion(t *testing.T) {
	if path := os.Getenv(invocationLockHelperPathEnv); path != "" {
		runInvocationLockHelper(path, os.Getenv(invocationLockHelperLogEnv))
		return
	}
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "claude-invocation.lock")
	logPath := filepath.Join(dir, "intervals.log")
	const workers = 3
	commands := make([]*exec.Cmd, 0, workers)
	for i := 0; i < workers; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=TestInvocationLockMultiProcessMutualExclusion$")
		cmd.Env = append(os.Environ(),
			invocationLockHelperPathEnv+"="+lockPath,
			invocationLockHelperLogEnv+"="+logPath,
		)
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			t.Fatalf("start helper: %v", err)
		}
		commands = append(commands, cmd)
	}
	for _, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("helper failed: %v", err)
		}
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read helper log: %v", err)
	}
	type interval struct{ start, end int64 }
	intervals := map[string]*interval{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var kind, pid string
		var stamp int64
		if _, err := fmt.Sscanf(line, "%s %s %d", &kind, &pid, &stamp); err != nil {
			t.Fatalf("parse %q: %v", line, err)
		}
		entry := intervals[pid]
		if entry == nil {
			entry = &interval{}
			intervals[pid] = entry
		}
		if kind == "start" {
			entry.start = stamp
		} else {
			entry.end = stamp
		}
	}
	if len(intervals) != workers {
		t.Fatalf("expected %d worker intervals, got %d: %q", workers, len(intervals), data)
	}
	ordered := make([]*interval, 0, len(intervals))
	for _, entry := range intervals {
		if entry.start == 0 || entry.end == 0 || entry.end < entry.start {
			t.Fatalf("incomplete interval: %+v", entry)
		}
		ordered = append(ordered, entry)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].start < ordered[j].start })
	for i := 1; i < len(ordered); i++ {
		if ordered[i].start < ordered[i-1].end {
			t.Fatalf("critical sections overlapped: %+v then %+v", ordered[i-1], ordered[i])
		}
	}
}

func runInvocationLockHelper(lockPath, logPath string) {
	appendLine := func(format string, args ...any) {
		file, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Fprintf(file, format+"\n", args...)
		_ = file.Close()
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		lock, _, err := tryLockInvocationFile(lockPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if lock != nil {
			lock.recordHolder()
			appendLine("start %d %d", os.Getpid(), time.Now().UnixNano())
			time.Sleep(150 * time.Millisecond)
			appendLine("end %d %d", os.Getpid(), time.Now().UnixNano())
			_ = lock.Release()
			os.Exit(0)
		}
		time.Sleep(10 * time.Millisecond)
	}
	fmt.Fprintln(os.Stderr, "helper timed out waiting for lock")
	os.Exit(1)
}
