package tagteam

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractRetrievalQueriesStable(t *testing.T) {
	got := extractRetrievalQueries(`Implement "Codex model discovery" in packages/web/src/settings.tsx and add model-selector tests.`)
	want := []string{"codex model discovery", "model-selector", "packages/web/src/settings.tsx", "tests"}
	for _, query := range want {
		if !containsString(got, query) {
			t.Fatalf("queries = %#v, missing %q", got, query)
		}
	}
	if len(got) > maxRetrievalQueries {
		t.Fatalf("queries len = %d, want <= %d", len(got), maxRetrievalQueries)
	}
	sorted := append([]string{}, got...)
	for i := 1; i < len(sorted); i++ {
		if sorted[i-1] > sorted[i] {
			t.Fatalf("queries not sorted: %#v", got)
		}
	}
}

func TestBuildRetrievalArtifactFindsEvidenceAndSkipsIgnoredDirs(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "internal", "models.go"), "package internal\n// codex registry\n")
	mustWriteFile(t, filepath.Join(repo, "internal", "models_test.go"), "package internal\n")
	mustWriteFile(t, filepath.Join(repo, ".tagteam", "ignored.md"), "codex should not be indexed\n")
	runGit(t, repo, "add", "internal/models.go", "internal/models_test.go")
	runGit(t, repo, "commit", "-m", "init")

	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg not installed")
	}
	artifact := buildRetrievalArtifact(context.Background(), repo, "fix codex registry", true)
	if artifact.Status != "ok" && artifact.Status != "degraded" {
		t.Fatalf("status = %q artifact=%#v", artifact.Status, artifact)
	}
	if len(artifact.Evidence) == 0 {
		t.Fatalf("expected evidence: %#v", artifact)
	}
	for _, item := range artifact.Evidence {
		if strings.Contains(item.File, ".tagteam") {
			t.Fatalf("ignored path included: %#v", item)
		}
	}
	if !containsRetrievalFile(artifact.Evidence, "internal/models.go") {
		t.Fatalf("expected models.go evidence: %#v", artifact.Evidence)
	}
	if !containsRetrievalFile(artifact.Evidence, "internal/models_test.go") {
		t.Fatalf("expected related test evidence: %#v", artifact.Evidence)
	}
}

func TestRunScoutRetrievalWritesDisabledArtifact(t *testing.T) {
	runDir := t.TempDir()
	artifact, err := runScoutRetrieval(context.Background(), t.TempDir(), "anything", runDir, false)
	if err != nil {
		t.Fatalf("runScoutRetrieval() error = %v", err)
	}
	if artifact.Status != "disabled" || artifact.Enabled {
		t.Fatalf("artifact = %#v", artifact)
	}
	if !fileExists(filepath.Join(runDir, "retrieval-round-1.json")) {
		t.Fatal("expected disabled retrieval artifact")
	}
}

func TestBuildRetrievalArtifactUnavailableWithoutRg(t *testing.T) {
	old := execLookPath
	execLookPath = func(file string) (string, error) {
		if file == "rg" {
			return "", exec.ErrNotFound
		}
		return old(file)
	}
	t.Cleanup(func() { execLookPath = old })

	artifact := buildRetrievalArtifact(context.Background(), t.TempDir(), "codex", true)
	if artifact.Status != "unavailable" {
		t.Fatalf("status = %q", artifact.Status)
	}
}

func TestBuildRetrievalArtifactTimeout(t *testing.T) {
	old := execLookPath
	execLookPath = func(file string) (string, error) {
		if file == "rg" {
			return "/fake/rg", nil
		}
		return old(file)
	}
	t.Cleanup(func() { execLookPath = old })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	artifact := buildRetrievalArtifact(ctx, t.TempDir(), "codex registry", true)
	if artifact.Status != "timeout" {
		t.Fatalf("status = %q artifact=%#v", artifact.Status, artifact)
	}
}

func TestCompactRetrievalForPromptCapsBytes(t *testing.T) {
	artifact := RetrievalArtifact{
		SchemaVersion: ArtifactSchemaVersion,
		Enabled:       true,
		Status:        "ok",
		Summary:       "many items",
		Queries:       []string{"codex"},
	}
	for i := 0; i < 200; i++ {
		artifact.Evidence = append(artifact.Evidence, RetrievalEvidence{
			File:   strings.Repeat("very-long-path/", 20) + "file.go",
			Line:   i + 1,
			Kind:   "content",
			Reason: strings.Repeat("reason ", 100),
		})
	}
	compact := CompactRetrievalForPrompt(artifact)
	if len(compact) > maxRetrievalPromptBytes {
		t.Fatalf("compact bytes = %d, want <= %d", len(compact), maxRetrievalPromptBytes)
	}
	if !strings.Contains(compact, `"truncated": true`) {
		t.Fatalf("expected truncation marker: %s", compact)
	}
}

func TestCollectScopeSymlinkTopologyIsBoundedAndAdvisory(t *testing.T) {
	repo := t.TempDir()
	mustWriteFile(t, filepath.Join(repo, "internal", "a.go"), "package internal\n")
	if err := os.MkdirAll(filepath.Join(repo, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(filepath.Join(repo, "internal", "a.go"), filepath.Join(repo, "internal", "alias.go")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(repo, "internal", "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(repo, "missing"), filepath.Join(repo, "internal", "broken")); err != nil {
		t.Fatal(err)
	}
	// Outside-scope link must not appear when allowed scope is internal/.
	if err := os.Symlink(outside, filepath.Join(repo, "root-escape")); err != nil {
		t.Fatal(err)
	}
	evidence := collectScopeSymlinkTopology(repo, []string{"internal/"})
	if len(evidence) == 0 {
		t.Fatal("expected symlink topology evidence")
	}
	var sawInternal, sawEscaping, sawBroken, sawRootEscape bool
	for _, item := range evidence {
		if item.Kind != symlinkTopologyEvidenceKind {
			t.Fatalf("unexpected kind %#v", item)
		}
		if !strings.Contains(item.Reason, "advisory only") {
			t.Fatalf("missing advisory marker: %#v", item)
		}
		if strings.Contains(item.File, "root-escape") {
			sawRootEscape = true
		}
		switch {
		case strings.Contains(item.Reason, "disposition=internal"):
			sawInternal = true
		case strings.Contains(item.Reason, "disposition=escaping"):
			sawEscaping = true
		case strings.Contains(item.Reason, "disposition=broken"):
			sawBroken = true
		}
	}
	if sawRootEscape {
		t.Fatal("topology inspected outside the selected scope")
	}
	if !sawInternal || !sawEscaping || !sawBroken {
		t.Fatalf("missing dispositions in %#v", evidence)
	}
	compact := CompactSymlinkTopologyForPrompt(evidence)
	if compact == "" || len(compact) > maxSymlinkTopologyBytes {
		t.Fatalf("compact topology invalid: len=%d", len(compact))
	}
	// Merging topology cannot invent broader authorization scopes.
	merged := mergeSymlinkTopologyEvidence(nil, evidence)
	for _, item := range merged {
		if strings.HasPrefix(item.File, "../") || filepath.IsAbs(item.File) {
			t.Fatalf("topology broadened scope: %#v", item)
		}
	}
}

func containsRetrievalFile(items []RetrievalEvidence, file string) bool {
	for _, item := range items {
		if item.File == file {
			return true
		}
	}
	return false
}

func mustWriteFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}
