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

	"github.com/cephalopod-ai/tagteam/internal/tagteam"
)

// TestRunNonTTYCompletedRunRendersOnceAndReturns exercises Run() against a
// completed (non-running) fixture run using a non-TTY stdin (an os.Pipe read
// end, never a real terminal in CI). Run() must render once and return
// immediately without entering the interactive polling loop -- proof the
// package builds and behaves in a headless/CI environment.
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
		done <- Run(context.Background(), workdir, runDir, outWrite, inRead)
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

	err = Run(context.Background(), workdir, filepath.Join(workdir, ".tagteam", "runs", "does-not-exist"), devNull, inRead)
	if err == nil {
		t.Fatal("expected an error for a missing run directory")
	}
}

var _ tagteam.RunSnapshot // keeps the tagteam import honest if fixtures above change
