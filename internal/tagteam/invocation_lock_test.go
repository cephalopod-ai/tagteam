package tagteam

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func writeInvocationLockRecord(t *testing.T, path string, pid int) {
	t.Helper()
	data, err := marshalJSON(runLockRecord{PID: pid, CreatedAt: time.Now().UTC()}, true)
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write record: %v", err)
	}
}

func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start probe process: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait probe process: %v", err)
	}
	return pid
}

func TestTryAcquireInvocationLockRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude-invocation.lock")
	lock, holder, err := tryAcquireInvocationLock(path)
	if err != nil || lock == nil || holder != 0 {
		t.Fatalf("acquire = %v holder=%d err=%v", lock, holder, err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if fileExists(path) {
		t.Fatal("lock file should be removed on release")
	}
}

func TestTryAcquireInvocationLockReportsLiveHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude-invocation.lock")
	holderPID := os.Getppid()
	writeInvocationLockRecord(t, path, holderPID)
	lock, holder, err := tryAcquireInvocationLock(path)
	if err != nil {
		t.Fatalf("try acquire: %v", err)
	}
	if lock != nil || holder != holderPID {
		t.Fatalf("expected busy lock held by %d, got lock=%v holder=%d", holderPID, lock, holder)
	}
}

func TestTryAcquireInvocationLockRemovesStaleRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude-invocation.lock")
	writeInvocationLockRecord(t, path, deadPID(t))
	lock, holder, err := tryAcquireInvocationLock(path)
	if err != nil || lock == nil || holder != 0 {
		t.Fatalf("stale takeover failed: lock=%v holder=%d err=%v", lock, holder, err)
	}
	_ = lock.Release()
}

func TestAcquireInvocationSlotWaitsForRelease(t *testing.T) {
	adapterID := "slot-wait-test"
	path, err := invocationLockPath(adapterID)
	if err != nil {
		t.Fatalf("lock path: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	writeInvocationLockRecord(t, path, os.Getppid())
	go func() {
		time.Sleep(700 * time.Millisecond)
		_ = os.Remove(path)
	}()
	start := time.Now()
	release, err := acquireInvocationSlot(context.Background(), adapterID, Request{Quiet: true}, time.Minute)
	if err != nil {
		t.Fatalf("acquire slot: %v", err)
	}
	defer release()
	if waited := time.Since(start); waited < 500*time.Millisecond {
		t.Fatalf("expected to wait for the holder, waited %s", waited)
	}
	if !fileExists(path) {
		t.Fatal("expected our own lock record to exist after acquisition")
	}
	release()
	if fileExists(path) {
		t.Fatal("lock file should be removed after release")
	}
}

func TestAcquireInvocationSlotCancelledWhileWaiting(t *testing.T) {
	adapterID := "slot-cancel-test"
	path, err := invocationLockPath(adapterID)
	if err != nil {
		t.Fatalf("lock path: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	writeInvocationLockRecord(t, path, os.Getppid())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := acquireInvocationSlot(ctx, adapterID, Request{Quiet: true}, time.Minute); err == nil {
		t.Fatal("expected cancellation error while waiting for a held lock")
	}
}

func TestAcquireInvocationSlotTimesOutAndProceedsUnlocked(t *testing.T) {
	adapterID := "slot-timeout-test"
	path, err := invocationLockPath(adapterID)
	if err != nil {
		t.Fatalf("lock path: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	writeInvocationLockRecord(t, path, os.Getppid())
	release, err := acquireInvocationSlot(context.Background(), adapterID, Request{Quiet: true}, time.Millisecond)
	if err != nil {
		t.Fatalf("acquire slot: %v", err)
	}
	release()
	if !fileExists(path) {
		t.Fatal("holder's lock record must be left untouched when proceeding unlocked")
	}
}
