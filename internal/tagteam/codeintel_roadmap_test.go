package tagteam

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodeIntelConfiguredProvidersAggregatePartialFailure(t *testing.T) {
	repo := newCodeIntelGitRepo(t)
	head := codeIntelGitOutput(t, repo, "rev-parse", "HEAD")
	good := writeCodeIntelScript(t, "printf '%s' '"+`{"schema_version":1,"observations":[{"schema_version":1,"provider":"untrusted","revision":"`+head+`","kind":"symbol","subject":"main.go:Main","summary":"ok","confidence":1,"generated_at":"2026-01-01T00:00:00Z"}]}`+"'")
	opts := RunOptions{Workdir: repo, Prompt: "find", CodeIntel: CodeIntelConfig{Providers: map[string]CodeIntelProviderConfig{"codebase-memory": {Command: good}, "gitnexus": {Command: "missing-code-intel-provider"}}}}
	artifact, err := runConfiguredCodeIntel(context.Background(), opts, t.TempDir())
	if err != nil || len(artifact.Observations) != 1 || artifact.Observations[0].Provider != "codebase-memory" || len(artifact.Errors) == 0 {
		t.Fatalf("aggregate = %#v, err=%v", artifact, err)
	}
}

func TestSnapshotAndOptInBridgeContracts(t *testing.T) {
	repo := newCodeIntelGitRepo(t)
	if identity := SnapshotIdentityForCodeIntel(context.Background(), repo); identity.Revision == "" || identity.DirtyDigest != "" {
		t.Fatalf("clean identity = %#v", identity)
	}
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if identity := SnapshotIdentityForCodeIntel(context.Background(), repo); identity.DirtyDigest == "" {
		t.Fatalf("dirty identity = %#v", identity)
	}
	bridge := CodeIntelFileBridgeConfig{Enabled: true, Path: filepath.Join(t.TempDir(), "dory.json")}
	runDir := t.TempDir()
	artifact := CodeIntelArtifact{SchemaVersion: ArtifactSchemaVersion, Status: codeIntelStatusOK, Staleness: codeIntelStalenessUnknown, Observations: []CodeIntelObservation{}, GeneratedAt: time.Now().UTC()}
	path, err := WriteDoryCheckpoint(context.Background(), repo, runDir, bridge, artifact)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadDoryHandoff(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bridge.Path); err != nil {
		t.Fatal(err)
	}
	candidate := MuninnCandidateEvidence{Candidate: "candidate", Evidence: []RetrievalEvidence{{File: "main.go", Kind: "definition", Reason: "test"}}}
	muninn := CodeIntelFileBridgeConfig{Enabled: true, Path: filepath.Join(t.TempDir(), "muninn.json")}
	path, err = ExportMuninnCandidateEvidence(context.Background(), repo, runDir, "r1", muninn, candidate)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ImportMuninnCandidateEvidence(path)
	if err != nil || got.Candidate != "candidate" {
		t.Fatalf("muninn = %#v, %v", got, err)
	}
	alexandria := CodeIntelFileBridgeConfig{Enabled: true, Path: filepath.Join(t.TempDir(), "alex.json")}
	_, skipped, err := ExportAlexandriaObservations(context.Background(), repo, runDir, "r1", alexandria, artifact)
	if err != nil || skipped {
		t.Fatalf("first alex export skipped=%v err=%v", skipped, err)
	}
	_, skipped, err = ExportAlexandriaObservations(context.Background(), repo, runDir, "r1", alexandria, artifact)
	if err != nil || !skipped {
		t.Fatalf("repeat alex export skipped=%v err=%v", skipped, err)
	}
}

func TestUntrustedRepoDoesNotEnableCodeIntelBridges(t *testing.T) {
	cfg := sanitizeUntrustedRepoConfig(Config{CodeIntel: CodeIntelConfig{Providers: map[string]CodeIntelProviderConfig{"gitnexus": {Command: "run"}}, Dory: CodeIntelFileBridgeConfig{Enabled: true}}})
	if len(cfg.CodeIntel.Providers) != 0 || cfg.CodeIntel.Dory.Enabled {
		t.Fatalf("untrusted code-intel config survived: %#v", cfg.CodeIntel)
	}
}

func TestGatewayUnavailableIsTruthfulJSON(t *testing.T) {
	result := RunCodeIntelGateway(context.Background(), DefaultConfig(), t.TempDir(), "", "find")
	if result.Status != codeIntelStatusProviderUnavailable {
		t.Fatalf("gateway status = %#v", result)
	}
	data, err := CodeIntelGatewayJSON(result)
	if err != nil || !strings.Contains(string(data), "provider_unavailable") {
		t.Fatalf("gateway JSON = %s, %v", data, err)
	}
	var roundTrip CodeIntelGatewayResult
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatal(err)
	}
}

func TestCodeIntelBenchRejectsUnallowedRepositoryAndIsDeterministic(t *testing.T) {
	repo := newCodeIntelGitRepo(t)
	denied := DefaultConfig()
	denied.CodeIntel.AllowedRepos = []string{t.TempDir()}
	if _, err := RunCodeIntelBench(context.Background(), denied, repo, ""); err == nil {
		t.Fatal("unallowed repository ran benchmark")
	}
	first, err := RunCodeIntelBench(context.Background(), DefaultConfig(), repo, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := RunCodeIntelBench(context.Background(), DefaultConfig(), repo, "")
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, _ := json.Marshal(first)
	secondJSON, _ := json.Marshal(second)
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("benchmark artifact is not deterministic: %s != %s", firstJSON, secondJSON)
	}
	if strings.Contains(string(firstJSON), "generated_at") || strings.Contains(string(firstJSON), "latency_ms") {
		t.Fatalf("benchmark artifact has non-deterministic metadata: %s", firstJSON)
	}
}

func TestAggregateCodeIntelProvidersReportsAllFailures(t *testing.T) {
	repo := newCodeIntelGitRepo(t)
	provider, err := newNamedCommandCodeIntelProvider("missing", "code-intel-provider-that-does-not-exist", codeIntelTimeout)
	if err != nil {
		t.Fatal(err)
	}
	artifact := aggregateCodeIntelProviders(context.Background(), repo, "find", []CodeIntelProvider{provider})
	if artifact.Status != codeIntelStatusError || len(artifact.Errors) == 0 {
		t.Fatalf("all-provider failure artifact = %#v", artifact)
	}
}
