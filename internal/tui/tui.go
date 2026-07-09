package tui

import (
	"bytes"
	"context"
	"os"
	"strings"
	"time"
	"unicode/utf8"

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
		var rendered bytes.Buffer
		Render(&rendered, snapshot, planPtr, toggles)
		_, _ = out.WriteString(formatForTerminal(rendered.String(), terminalWidth(out)))

		running := snapshot.Status == "running"
		if !interactive && !running {
			return nil
		}

		key, timedOut, err := waitForKey(ctx, keyCh, errCh, interactive, running)
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

func terminalWidth(out *os.File) int {
	if out == nil {
		return 0
	}
	width, _, err := term.GetSize(int(out.Fd()))
	if err != nil || width < 20 {
		return 0
	}
	return width
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
func waitForKey(ctx context.Context, keyCh <-chan byte, errCh <-chan error, isTTY, poll bool) (key byte, timedOut bool, err error) {
	if !isTTY {
		select {
		case <-ctx.Done():
			return 0, false, ctx.Err()
		case <-time.After(pollInterval):
			return 0, true, nil
		}
	}

	if !poll {
		select {
		case <-ctx.Done():
			return 0, false, ctx.Err()
		case readErr := <-errCh:
			return 0, false, readErr
		case k := <-keyCh:
			return k, false, nil
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

func formatForTerminal(text string, width int) string {
	if width <= 0 || text == "" {
		return text
	}
	lines := strings.Split(text, "\n")
	var out []string
	for _, line := range lines {
		out = append(out, wrapLine(line, width)...)
	}
	return strings.Join(out, "\n")
}

func wrapLine(line string, width int) []string {
	if width <= 0 || utf8.RuneCountInString(line) <= width {
		return []string{line}
	}
	indent := leadingWhitespace(line)
	continuation := indent + "  "
	words := strings.Fields(line)
	if len(words) == 0 {
		return []string{line}
	}

	var lines []string
	current := indent
	wordsOnLine := 0
	for _, word := range words {
		for len(word) > 0 {
			candidate := word
			if wordsOnLine > 0 {
				candidate = current + " " + word
			} else {
				candidate = current + word
			}
			if utf8.RuneCountInString(candidate) <= width {
				current = candidate
				wordsOnLine++
				break
			}

			if wordsOnLine > 0 {
				lines = append(lines, current)
				current = continuation
				wordsOnLine = 0
				continue
			}

			available := width - utf8.RuneCountInString(current)
			if available <= 0 {
				lines = append(lines, current)
				current = continuation
				continue
			}
			part, rest := splitRunes(word, available)
			lines = append(lines, current+part)
			current = continuation
			word = rest
		}
	}
	lines = append(lines, current)
	return lines
}

func leadingWhitespace(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r != ' ' && r != '\t' {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

func splitRunes(s string, limit int) (head, tail string) {
	if limit <= 0 || s == "" {
		return "", s
	}
	count := 0
	for i := range s {
		if count == limit {
			return s[:i], s[i:]
		}
		count++
	}
	return s, ""
}
