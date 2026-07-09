package tui

import (
	"context"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/cephalopod-ai/tagteam/internal/tagteam"
)

const pollInterval = time.Second
const inputSequenceTimeout = 25 * time.Millisecond

// RunOptions controls TUI startup state.
type RunOptions struct {
	Workdir         string
	InitialRunDir   string
	InspectOnStart  bool
	Flags           tagteam.FlagInputs
	Changed         map[string]bool
	TrustRepoConfig bool
}

// Run drives the interactive tagteam terminal UI. It can launch new runs via
// the existing runner/config path and monitor saved or active runs without
// invoking a separate CLI subprocess.
func Run(ctx context.Context, opts RunOptions, out *os.File, in *os.File) error {
	interactive := in != nil && out != nil && term.IsTerminal(int(in.Fd())) && term.IsTerminal(int(out.Fd()))

	model, err := newModel(opts)
	if err != nil {
		return err
	}
	model.refresh()

	if !interactive {
		_, _ = out.WriteString(renderDashboard(model))
		return nil
	}

	loopCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	oldState, err := term.MakeRaw(int(in.Fd()))
	if err != nil {
		return err
	}
	defer func() { _ = term.Restore(int(in.Fd()), oldState) }()

	_, _ = out.WriteString("\x1b[?1049h\x1b[?25l")
	defer func() { _, _ = out.WriteString("\x1b[?25h\x1b[?1049l") }()

	inputCh := make(chan []byte, 16)
	errCh := make(chan error, 1)
	go readInputChunks(in, inputCh, errCh)
	decoder := inputDecoder{}
	var inputTimer *time.Timer
	var inputTimerC <-chan time.Time
	defer func() {
		if inputTimer != nil {
			inputTimer.Stop()
		}
	}()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	renderFrame(out, model)
	for {
		select {
		case <-loopCtx.Done():
			return loopCtx.Err()
		case <-ticker.C:
			model.refresh()
			renderFrame(out, model)
		case <-inputTimerC:
			inputTimerC = nil
			for _, event := range decoder.Flush() {
				if model.handleKey(loopCtx, event) == actionQuit {
					return nil
				}
			}
			renderFrame(out, model)
		case result := <-model.runResultCh:
			model.handleRunResult(result)
			renderFrame(out, model)
		case readErr := <-errCh:
			if readErr == nil {
				continue
			}
			return nil
		case chunk := <-inputCh:
			events := decoder.Feed(chunk)
			for _, event := range events {
				action := model.handleKey(loopCtx, event)
				if action == actionQuit {
					return nil
				}
			}
			if decoder.HasPending() {
				if inputTimer == nil {
					inputTimer = time.NewTimer(inputSequenceTimeout)
				} else {
					if !inputTimer.Stop() {
						select {
						case <-inputTimer.C:
						default:
						}
					}
					inputTimer.Reset(inputSequenceTimeout)
				}
				inputTimerC = inputTimer.C
			} else {
				inputTimerC = nil
			}
			renderFrame(out, model)
		}
	}
}

func renderFrame(out *os.File, model *model) {
	model.updateTerminalSize(out)
	_, _ = out.WriteString("\x1b[H\x1b[2J")
	_, _ = out.WriteString(normalizeTerminalNewlines(renderDashboard(model)))
}

func readInputChunks(in *os.File, inputCh chan<- []byte, errCh chan<- error) {
	buf := make([]byte, 64)
	for {
		n, err := in.Read(buf)
		if err != nil {
			errCh <- err
			return
		}
		if n == 0 {
			continue
		}
		chunk := append([]byte(nil), buf[:n]...)
		inputCh <- chunk
	}
}

func normalizeTerminalNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}
