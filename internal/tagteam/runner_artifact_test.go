package tagteam

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestReadRunPrompt_FallsBackToMeta(t *testing.T) {
	runDir := t.TempDir()
	data, err := json.Marshal(Meta{Prompt: "review prompt"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "meta.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	prompt, err := readRunPrompt(runDir, "")
	if err != nil {
		t.Fatalf("readRunPrompt() error = %v", err)
	}
	if prompt != "review prompt" {
		t.Fatalf("prompt = %q", prompt)
	}
}

func TestRunAdapterStoresNormalizedGrokWorkerArtifact(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "baseline\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "baseline")

	worker := `{"schema_version":1,"status":"completed","summary":"implemented","files_changed":[],"checks_run":[],"remaining_risks":[]}`
	raw := `{"text":` + strconv.Quote(worker) + `,"thought":"provider-only reasoning"}`
	runDir := t.TempDir()
	outputPath := filepath.Join(runDir, "coder-round-1.md")
	adapter := fakeAdapter{
		id: "grok",
		build: func(role Role, req Request) (*CommandSpec, error) {
			return &CommandSpec{Argv: []string{"sh", "-c", "printf '%s' '" + raw + "'"}, Dir: repo}, nil
		},
		parse: (&GrokAdapter{}).ParseResult,
	}
	result, err := NewApp(DefaultConfig()).runAdapter(context.Background(), adapter, RoleCoder, Request{
		Context:               context.Background(),
		Workdir:               repo,
		RunDir:                runDir,
		OutputPath:            outputPath,
		Timeout:               time.Second,
		RequireWorkerContract: true,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Worker == nil || result.Worker.Summary != "implemented" {
		t.Fatalf("worker result = %#v", result.Worker)
	}
	for _, path := range []string{outputPath, outputPath + ".raw"} {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if strings.Contains(string(data), "provider-only reasoning") {
			t.Fatalf("artifact %s retained provider-only reasoning", path)
		}
	}
	data, readErr := os.ReadFile(outputPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	parsedWorker, parseErr := parseWorkerResult(data)
	if parseErr != nil || parsedWorker.Summary != "implemented" {
		t.Fatalf("normalized worker artifact = %q, parse error = %v", data, parseErr)
	}
	stdoutPaths, globErr := filepath.Glob(filepath.Join(runDir, "deliveries", "*.stdout.txt"))
	if globErr != nil || len(stdoutPaths) != 1 {
		t.Fatalf("stdout artifacts = %v, error = %v", stdoutPaths, globErr)
	}
	stdout, readErr := os.ReadFile(stdoutPaths[0])
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(stdout), "provider-only reasoning") {
		t.Fatalf("invocation stdout retained provider-only reasoning")
	}
}
