//go:build !windows

package cli

import (
	"context"
	"syscall"
	"testing"
	"time"
)

func TestCommandSignalContextCancelsOnSIGTERM(t *testing.T) {
	ctx, stop := commandSignalContext(context.Background())
	defer stop()

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}
	select {
	case <-ctx.Done():
		if ctx.Err() != context.Canceled {
			t.Fatalf("context error = %v, want %v", ctx.Err(), context.Canceled)
		}
	case <-time.After(time.Second):
		t.Fatal("SIGTERM did not cancel command context")
	}
}
