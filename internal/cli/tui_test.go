package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeRunFixture(t *testing.T, workdir, runID string) string {
	t.Helper()
	runDir := filepath.Join(workdir, ".tagteam", "runs", runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	final := `{"schema_version":1,"run_id":"` + runID + `","run_dir":"` + runDir + `","mode":"solo","status":"passed","verdict":"done","exit_code":0,"finished_at":"2026-01-01T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(runDir, "final.json"), []byte(final), 0o644); err != nil {
		t.Fatal(err)
	}
	return runDir
}

func writeLatestPointer(t *testing.T, workdir, runID, runDir string) {
	t.Helper()
	latest := `{"run_id":"` + runID + `","run_dir":"` + runDir + `","final_path":"` + filepath.Join(runDir, "final.json") + `","verdict":"done","exit_code":0,"updated_at":"2026-01-01T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(workdir, ".tagteam", "latest.json"), []byte(latest), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveTUIRunDir_ExplicitRunID(t *testing.T) {
	workdir := t.TempDir()
	runDir := writeRunFixture(t, workdir, "run-explicit")

	got, err := resolveTUIRunDir(workdir, []string{"run-explicit"})
	if err != nil {
		t.Fatalf("resolveTUIRunDir() error = %v", err)
	}
	if got != runDir {
		t.Fatalf("resolved run dir = %q, want %q", got, runDir)
	}
}

func TestResolveTUIRunDir_LatestWhenNoArgs(t *testing.T) {
	workdir := t.TempDir()
	runDir := writeRunFixture(t, workdir, "run-latest")
	writeLatestPointer(t, workdir, "run-latest", runDir)

	got, err := resolveTUIRunDir(workdir, nil)
	if err != nil {
		t.Fatalf("resolveTUIRunDir() error = %v", err)
	}
	if got != runDir {
		t.Fatalf("resolved run dir = %q, want %q", got, runDir)
	}
}

func TestResolveTUIRunDir_MissingExplicitRunReturnsClearError(t *testing.T) {
	workdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workdir, ".tagteam"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := resolveTUIRunDir(workdir, []string{"does-not-exist"})
	if err == nil {
		t.Fatal("expected an error for a missing explicit run id")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Fatalf("error should name the missing run id, got: %v", err)
	}
}

func TestResolveTUIRunDir_NoRunsAtAllReturnsClearError(t *testing.T) {
	workdir := t.TempDir()

	runDir, err := resolveTUIRunDir(workdir, nil)
	if err != nil {
		t.Fatalf("resolveTUIRunDir() error = %v", err)
	}
	if runDir != "" {
		t.Fatalf("expected no initial run dir, got %q", runDir)
	}
}

func TestTUICommand_MissingRunReturnsError(t *testing.T) {
	workdir := t.TempDir()
	flags := &flagState{}
	flags.Workdir = workdir
	cmd := newTUICommand(flags)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"nope"})

	if err := cmd.Execute(); err == nil {
		t.Fatal("expected an error for a missing run id")
	}
}

func TestTUICommand_ResolvesCompletedLatestRunAndReturns(t *testing.T) {
	workdir := t.TempDir()
	runDir := writeRunFixture(t, workdir, "run-cli-latest")
	writeLatestPointer(t, workdir, "run-cli-latest", runDir)

	flags := &flagState{}
	flags.Workdir = workdir
	cmd := newTUICommand(flags)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(nil)

	done := make(chan error, 1)
	go func() { done <- cmd.Execute() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("tui command did not return promptly for a completed run")
	}
}
