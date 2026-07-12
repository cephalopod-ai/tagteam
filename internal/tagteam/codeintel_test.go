package tagteam

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClassifyCodeIntelStaleness(t *testing.T) {
	repo := newCodeIntelGitRepo(t)
	head := codeIntelGitOutput(t, repo, "rev-parse", "HEAD")
	if got := classifyCodeIntelStaleness(context.Background(), repo, head); got != codeIntelStalenessFresh {
		t.Fatalf("clean worktree staleness = %q", got)
	}
	if err := os.WriteFile(filepath.Join(repo, "dirty.go"), []byte("package dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := classifyCodeIntelStaleness(context.Background(), repo, head); got != codeIntelStalenessDirty {
		t.Fatalf("dirty worktree staleness = %q", got)
	}
	if err := os.Remove(filepath.Join(repo, "dirty.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "tracked.go"), []byte("package tracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	codeIntelGitRun(t, repo, "add", "tracked.go")
	codeIntelGitRun(t, repo, "commit", "-m", "second")
	if got := classifyCodeIntelStaleness(context.Background(), repo, head); got != codeIntelStalenessStale {
		t.Fatalf("older revision staleness = %q", got)
	}
	if got := classifyCodeIntelStaleness(context.Background(), repo, "not-a-sha"); got != codeIntelStalenessUnknown {
		t.Fatalf("invalid revision staleness = %q", got)
	}
	codeIntelGitRun(t, repo, "checkout", "--detach", "HEAD")
	current := codeIntelGitOutput(t, repo, "rev-parse", "HEAD")
	if got := classifyCodeIntelStaleness(context.Background(), repo, current); got != codeIntelStalenessUnknown {
		t.Fatalf("detached HEAD staleness = %q", got)
	}
}

func TestNormalizeCodeIntelArtifactFiltersInvalidObservations(t *testing.T) {
	repo := newCodeIntelGitRepo(t)
	head := codeIntelGitOutput(t, repo, "rev-parse", "HEAD")
	valid := CodeIntelObservation{
		SchemaVersion: ArtifactSchemaVersion,
		Provider:      "fixture 1.0",
		Revision:      head,
		Kind:          "symbol",
		Subject:       "main.go:Main",
		Summary:       "entry point",
		Evidence:      []RetrievalEvidence{{File: "main.go", Line: 1, Kind: "definition", Reason: "fixture"}},
		Confidence:    0.8,
		GeneratedAt:   time.Now().UTC(),
	}
	invalidConfidence := valid
	invalidConfidence.Confidence = 2
	invalidSubject := valid
	invalidSubject.Subject = "../outside.go"
	artifact, err := normalizeCodeIntelArtifact(context.Background(), repo, CodeIntelArtifact{
		SchemaVersion: ArtifactSchemaVersion,
		Observations:  []CodeIntelObservation{valid, invalidConfidence, invalidSubject},
		GeneratedAt:   time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("normalizeCodeIntelArtifact() error = %v", err)
	}
	if len(artifact.Observations) != 1 || len(artifact.Errors) != 2 {
		t.Fatalf("normalized artifact = %#v", artifact)
	}
	if artifact.Staleness != codeIntelStalenessFresh || artifact.Status != codeIntelStatusError {
		t.Fatalf("normalized status = %#v", artifact)
	}
	if err := artifact.Validate(repo); err != nil {
		t.Fatalf("artifact.Validate() error = %v", err)
	}
	data, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip CodeIntelArtifact
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if roundTrip.Observations[0].Revision != head {
		t.Fatalf("round-trip revision = %q", roundTrip.Observations[0].Revision)
	}
}

func TestCompactCodeIntelForPromptRequiresFreshArtifact(t *testing.T) {
	artifact := CodeIntelArtifact{
		SchemaVersion: ArtifactSchemaVersion,
		Status:        codeIntelStatusStale,
		Staleness:     codeIntelStalenessStale,
		Observations: []CodeIntelObservation{{
			SchemaVersion: ArtifactSchemaVersion,
			Provider:      "fixture",
			Revision:      strings.Repeat("a", 40),
			Kind:          "symbol",
			Subject:       "main.go:Main",
			Summary:       "stale",
			Confidence:    1,
			GeneratedAt:   time.Now().UTC(),
		}},
	}
	if got := CompactCodeIntelForPrompt(artifact); got != "" {
		t.Fatalf("stale artifact prompt context = %q", got)
	}
	artifact.Status = codeIntelStatusOK
	artifact.Staleness = codeIntelStalenessFresh
	artifact.Observations[0].Staleness = codeIntelStalenessFresh
	if got := CompactCodeIntelForPrompt(artifact); !strings.Contains(got, `"staleness": "fresh"`) {
		t.Fatalf("fresh artifact prompt context = %q", got)
	}
}

func TestCommandCodeIntelProviderSubprocesses(t *testing.T) {
	repo := newCodeIntelGitRepo(t)
	head := codeIntelGitOutput(t, repo, "rev-parse", "HEAD")
	validJSON := fmt.Sprintf(`{"schema_version":1,"status":"ok","observations":[{"schema_version":1,"provider":"fixture 1.0","revision":"%s","kind":"symbol","subject":"main.go:Main","summary":"fixture result","confidence":0.9,"generated_at":"2026-01-01T00:00:00Z"}]}`, head)
	validScript := writeCodeIntelScript(t, "printf '%s' '"+validJSON+"'")
	provider, err := NewCommandCodeIntelProvider(validScript)
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Probe(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
	artifact, err := provider.Observe(context.Background(), CodeIntelRequest{Workdir: repo, Prompt: "inspect"})
	if err != nil || artifact.Status != codeIntelStatusOK || len(artifact.Observations) != 1 {
		t.Fatalf("successful provider = %#v, error=%v", artifact, err)
	}

	invalidScript := writeCodeIntelScript(t, "printf '%s' invalid")
	invalidProvider, _ := NewCommandCodeIntelProvider(invalidScript)
	if _, err := invalidProvider.Observe(context.Background(), CodeIntelRequest{Workdir: repo}); err == nil {
		t.Fatal("invalid provider output unexpectedly succeeded")
	}

	timeoutScript := writeCodeIntelScript(t, "sleep 11")
	timeoutProvider, _ := NewCommandCodeIntelProvider(timeoutScript)
	start := time.Now()
	if _, err := timeoutProvider.Observe(context.Background(), CodeIntelRequest{Workdir: repo}); err == nil {
		t.Fatal("timeout provider unexpectedly succeeded")
	} else if time.Since(start) > 12*time.Second {
		t.Fatalf("provider timeout took too long: %s", time.Since(start))
	}

	missing, _ := NewCommandCodeIntelProvider("code-intel-command-that-does-not-exist")
	if err := missing.Probe(context.Background(), repo); err == nil {
		t.Fatal("missing provider unexpectedly passed Probe")
	}
}

func TestRunCodeIntelWritesArtifactWhenProviderFails(t *testing.T) {
	runDir := t.TempDir()
	artifact, err := runCodeIntel(context.Background(), t.TempDir(), "inspect", runDir, "code-intel-command-that-does-not-exist")
	if err == nil || artifact.Status != codeIntelStatusProviderUnavailable {
		t.Fatalf("failed provider = %#v, error=%v", artifact, err)
	}
	data, readErr := os.ReadFile(filepath.Join(runDir, "code-intel-round-1.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	var persisted CodeIntelArtifact
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Status != codeIntelStatusProviderUnavailable {
		t.Fatalf("persisted artifact = %#v", persisted)
	}
}

func newCodeIntelGitRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	codeIntelGitRun(t, repo, "init")
	codeIntelGitRun(t, repo, "config", "user.email", "codeintel@example.test")
	codeIntelGitRun(t, repo, "config", "user.name", "Code Intel Test")
	codeIntelGitRun(t, repo, "add", "main.go")
	codeIntelGitRun(t, repo, "commit", "-m", "initial")
	return repo
}

func codeIntelGitRun(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func codeIntelGitOutput(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(output))
}

func writeCodeIntelScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "provider.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
