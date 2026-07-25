//go:build !windows

package tagteam

import (
	"bufio"
	"context"
	"errors"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestProcessTreeCancellationStopsChildProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "sleep 30 & echo $!; wait")
	prepareProcessTree(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start process tree: %v", err)
	}

	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		_ = cmd.Process.Kill()
		t.Fatalf("read child pid: %v", scanner.Err())
	}
	childPID, err := strconv.Atoi(scanner.Text())
	if err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("parse child pid %q: %v", scanner.Text(), err)
	}

	cancel()
	if err := cmd.Wait(); err == nil {
		t.Fatal("cancelled process tree exited without cancellation error")
	}

	deadline := time.Now().Add(time.Second)
	for {
		err = syscall.Kill(childPID, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			if err == nil {
				t.Fatalf("child process %d still running after cancellation", childPID)
			}
			t.Fatalf("check child process %d: %v", childPID, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
