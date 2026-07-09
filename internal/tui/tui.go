package tui

import (
	"context"
	"os"
	"time"

	"golang.org/x/term"

	"github.com/cephalopod-ai/tagteam/internal/tagteam"
)

const pollInterval = time.Second

// Run drives the read-only polling view for the run at runDir until the run
// stops being "running" or the user quits. It never invokes an adapter or
// mutates the run directory -- each tick only re-reads the on-disk snapshot.
func Run(ctx context.Context, workdir, runDir string, out *os.File, in *os.File) error {
	toggles := DefaultToggles()
	interactive := in != nil && out != nil && term.IsTerminal(int(in.Fd())) && term.IsTerminal(int(out.Fd()))

	var restore func()
	if interactive {
		oldState, err := term.MakeRaw(int(in.Fd()))
		if err == nil {
			restore = func() { _ = term.Restore(int(in.Fd()), oldState) }
			defer restore()
		}
	}

	// One long-lived reader goroutine feeds every tick's key-wait from a
	// shared channel for the life of Run. A fresh reader per tick would leave
	// each timed-out tick's goroutine blocked on the next keypress forever
	// (os.File reads cannot be canceled from another goroutine) -- and worse,
	// a keypress landing on an abandoned prior-tick goroutine's private
	// channel would never reach the tick actually waiting for it, making the
	// keypress appear to do nothing.
	var keyCh chan byte
	var errCh chan error
	if interactive {
		keyCh = make(chan byte, 16)
		errCh = make(chan error, 1)
		go readKeys(in, keyCh, errCh)
	}

	for {
		snapshot, err := tagteam.BuildRunSnapshot(workdir, runDir)
		if err != nil {
			return err
		}
		plan, hasPlan := readPlan(runDir)
		var planPtr *tagteam.ExecutionPlan
		if hasPlan {
			planPtr = &plan
		}

		if interactive {
			clearScreen(out)
		}
		Render(out, snapshot, planPtr, toggles)

		if snapshot.Status != "running" {
			return nil
		}

		key, timedOut, err := waitForKey(ctx, keyCh, errCh, interactive)
		if err != nil {
			return nil
		}
		if timedOut {
			continue
		}
		switch key {
		case 'q', 'Q', 3: // 3 = Ctrl-C, since raw mode disables signal generation
			return nil
		case 'r', 'R':
			// fall through and re-render immediately
		case 'p', 'P':
			toggles.ShowPlan = !toggles.ShowPlan
		case 'f', 'F':
			toggles.ShowFindings = !toggles.ShowFindings
		case 'd', 'D':
			toggles.ShowArtifacts = !toggles.ShowArtifacts
		}
	}
}

func readPlan(runDir string) (tagteam.ExecutionPlan, bool) {
	plan, err := tagteam.ReadPlanForCLI(runDir)
	if err != nil {
		return tagteam.ExecutionPlan{}, false
	}
	return plan, true
}

func clearScreen(out *os.File) {
	_, _ = out.WriteString("\x1b[H\x1b[2J")
}

// readKeys reads single bytes from in until it errors (EOF, closed fd, etc.),
// pushing each byte onto keyCh. It runs for the lifetime of one Run call.
func readKeys(in *os.File, keyCh chan<- byte, errCh chan<- error) {
	buf := make([]byte, 1)
	for {
		n, err := in.Read(buf)
		if err != nil {
			errCh <- err
			return
		}
		if n > 0 {
			keyCh <- buf[0]
		}
	}
}

// waitForKey waits up to one poll interval for a single keypress from the
// shared reader channels. On a non-TTY stdin (CI, piped input) there is no
// interactive key to read, so it just sleeps out the tick.
func waitForKey(ctx context.Context, keyCh <-chan byte, errCh <-chan error, isTTY bool) (key byte, timedOut bool, err error) {
	if !isTTY {
		select {
		case <-ctx.Done():
			return 0, false, ctx.Err()
		case <-time.After(pollInterval):
			return 0, true, nil
		}
	}

	select {
	case <-ctx.Done():
		return 0, false, ctx.Err()
	case readErr := <-errCh:
		return 0, false, readErr
	case k := <-keyCh:
		return k, false, nil
	case <-time.After(pollInterval):
		return 0, true, nil
	}
}
