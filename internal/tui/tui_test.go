package tui

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunNonTTYCompletedRunRendersOnceAndReturns(t *testing.T) {
	workdir := t.TempDir()
	runDir := filepath.Join(workdir, ".tagteam", "runs", "run-1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	final := map[string]any{
		"schema_version": 1,
		"run_id":         "run-1",
		"run_dir":        runDir,
		"mode":           "solo",
		"status":         "passed",
		"verdict":        "done",
		"exit_code":      0,
		"finished_at":    time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(final)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "final.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	outRead, outWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer outRead.Close()
	inRead, inWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inWrite.Close()

	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), RunOptions{Workdir: workdir, InitialRunDir: runDir, InspectOnStart: true}, outWrite, inRead)
	}()

	select {
	case err := <-done:
		outWrite.Close()
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return promptly for a completed run in a non-TTY environment")
	}
	rendered, err := io.ReadAll(outRead)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rendered), "\x1b[H\x1b[2J") {
		t.Fatalf("expected non-interactive output to omit clear-screen escape codes, got:\n%s", string(rendered))
	}
	if !strings.Contains(string(rendered), "Run: run-1") {
		t.Fatalf("expected rendered dashboard to include run details, got:\n%s", string(rendered))
	}
}

func TestRunNonTTYNoRunsRendersComposeAndReturns(t *testing.T) {
	workdir := t.TempDir()
	outRead, outWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer outRead.Close()
	inRead, inWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inWrite.Close()

	if err := Run(context.Background(), RunOptions{Workdir: workdir}, outWrite, inRead); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	outWrite.Close()
	rendered, err := io.ReadAll(outRead)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rendered), "Ready for a new task.") {
		t.Fatalf("expected empty-state render, got:\n%s", string(rendered))
	}
}

func TestRunMissingRunDirReturnsError(t *testing.T) {
	workdir := t.TempDir()
	inRead, inWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inWrite.Close()
	defer inRead.Close()
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer devNull.Close()

	err = Run(context.Background(), RunOptions{
		Workdir:       workdir,
		InitialRunDir: filepath.Join(workdir, ".tagteam", "runs", "does-not-exist"),
	}, devNull, inRead)
	if err == nil {
		t.Fatal("expected an error for a missing run directory")
	}
}

func TestNormalizeTerminalNewlinesUsesCRLF(t *testing.T) {
	input := "one\ntwo\r\nthree\rfour"
	got := normalizeTerminalNewlines(input)
	if want := "one\r\ntwo\r\nthree\r\nfour"; got != want {
		t.Fatalf("normalizeTerminalNewlines() = %q, want %q", got, want)
	}
}

func TestInputDecoderPreservesSplitEscapeAndUTF8Sequences(t *testing.T) {
	decoder := inputDecoder{}
	if events := decoder.Feed([]byte("\x1b[")); len(events) != 0 {
		t.Fatalf("partial arrow emitted events: %#v", events)
	}
	events := decoder.Feed([]byte("A"))
	if len(events) != 1 || events[0].Kind != keyUp {
		t.Fatalf("split arrow = %#v, want keyUp", events)
	}

	if events := decoder.Feed([]byte{0xc3}); len(events) != 0 {
		t.Fatalf("partial UTF-8 rune emitted events: %#v", events)
	}
	events = decoder.Feed([]byte{0xa9})
	if len(events) != 1 || events[0].Kind != keyRune || events[0].Rune != rune(0x00e9) {
		t.Fatalf("split UTF-8 rune = %#v, want U+00E9", events)
	}
}
