package sharedcatalog

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestVendoredFilesMatchRecordedDigests fails when a vendored copy of the
// shared catalog is edited in place instead of being re-vendored from
// e3742526/control-hooks. Without it a consumer could silently fork the
// roster and the three repositories would disagree about which models exist.
func TestVendoredFilesMatchRecordedDigests(t *testing.T) {
	recorded, err := os.ReadFile(sumsPath(t))
	if err != nil {
		t.Fatalf("read SHA256SUMS: %v", err)
	}
	checked := 0
	for _, line := range strings.Split(string(recorded), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		want, name, ok := strings.Cut(line, "  ")
		if !ok {
			t.Fatalf("malformed SHA256SUMS line %q", line)
		}
		data, err := os.ReadFile(filepath.Join(filepath.Dir(sumsPath(t)), name))
		if err != nil {
			// The canonical checkout keeps SHA256SUMS one level above the
			// files it covers, so resolve names relative to the manifest.
			data, err = os.ReadFile(name)
		}
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Errorf("%s digest = %s, want %s; re-vendor from control-hooks shared/model-catalog instead of editing in place", name, got, want)
		}
		checked++
	}
	if checked != 2 {
		t.Fatalf("SHA256SUMS covered %d files, want catalog.go and model_catalog.json", checked)
	}
}

// sumsPath locates SHA256SUMS in both layouts this package lives in. A
// consumer vendors it beside these files; in the canonical control-hooks
// checkout it sits one level up at shared/SHA256SUMS, shared with any other
// vendorable artifact under shared/. Without this the canonical copy could
// not run its own test.
func sumsPath(t *testing.T) string {
	t.Helper()
	for _, candidate := range []string{"SHA256SUMS", filepath.Join("..", "SHA256SUMS")} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	t.Fatal("SHA256SUMS not found beside this package or in its parent directory")
	return ""
}

func TestCatalogLoadsAndExposesMaintainedRoster(t *testing.T) {
	catalog, err := Load()
	if err != nil {
		t.Fatalf("load shared catalog: %v", err)
	}
	if len(catalog.Adapters) == 0 || len(catalog.Models) == 0 {
		t.Fatalf("shared catalog is empty: %d adapters, %d models", len(catalog.Adapters), len(catalog.Models))
	}
	for _, want := range []string{"gpt-6-astra", "claude-fable-5-1", "grok-4.6", "gemini-3.8-flash-medium"} {
		if _, ok := LookupModel(want); !ok {
			t.Errorf("shared catalog is missing model %q", want)
		}
	}
	if got := SeriesModels("gemini-3.6-flash"); len(got) != 3 || got[0] != "gemini-3.6-flash-low" || got[2] != "gemini-3.6-flash-high" {
		t.Errorf("SeriesModels ordered by effort tier = %#v", got)
	}
	if MaintainedModelsFor("mistral-acp") != nil {
		t.Error("mistral-acp has no maintained roster entry; its models come from live ACP discovery")
	}
	if !AdapterAllowsRole("claude", "reviewer") || AdapterAllowsRole("claude", "editor") {
		t.Error("claude must stay review-only in the shared roster")
	}
	if !AdapterAllowsRole("agy", "scout") || AdapterAllowsRole("agy", "reviewer") {
		t.Error("agy must stay scout-only in the shared roster")
	}
}

func TestMaintainedTargetsUseTheRequestedSeparator(t *testing.T) {
	colon := MaintainedTargets(":")
	slash := MaintainedTargets("/")
	if len(colon) != len(slash) || len(colon) == 0 {
		t.Fatalf("target counts disagree: %d vs %d", len(colon), len(slash))
	}
	for index := range colon {
		if strings.ReplaceAll(colon[index], ":", "/") != slash[index] {
			t.Fatalf("target %d differs: %q vs %q", index, colon[index], slash[index])
		}
	}
	if colon[0] != "codex:gpt-6-astra" {
		t.Errorf("first maintained target = %q, want codex:gpt-6-astra", colon[0])
	}
}
