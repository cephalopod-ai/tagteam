package tagteam

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewFindingsPromptContextIncludesOnlyOpenBlockerOrMajor(t *testing.T) {
	runDir := t.TempDir()
	ledger := FindingsLedger{
		SchemaVersion: ArtifactSchemaVersion,
		Entries: []FindingEntry{
			{ID: "open-major", Severity: "major", File: "main.go", Line: 12, Issue: "fix this", Status: "open", FirstRound: 1, LastRound: 2},
			{ID: "open-blocker", Severity: "blocker", File: "auth.go", Line: 8, Issue: "secure this", Status: "open"},
			{ID: "open-minor", Severity: "minor", Issue: "not required", Status: "open"},
			{ID: "resolved-major", Severity: "major", Issue: "already fixed", Status: "resolved"},
		},
	}
	if err := writeJSONWithNewline(filepath.Join(runDir, findingsLedgerFilename), ledger); err != nil {
		t.Fatal(err)
	}

	context, err := reviewFindingsPromptContext(runDir)
	if err != nil {
		t.Fatalf("reviewFindingsPromptContext() error = %v", err)
	}
	for _, want := range []string{"open-major", "open-blocker", "prior_finding_dispositions"} {
		if !strings.Contains(context, want) {
			t.Fatalf("context missing %q:\n%s", want, context)
		}
	}
	for _, excluded := range []string{"open-minor", "resolved-major"} {
		if strings.Contains(context, excluded) {
			t.Fatalf("context unexpectedly includes %q:\n%s", excluded, context)
		}
	}
}

func TestReviewFindingsPromptContextFailsWhenOpenFindingsExceedBudget(t *testing.T) {
	runDir := t.TempDir()
	ledger := FindingsLedger{
		SchemaVersion: ArtifactSchemaVersion,
		Entries: []FindingEntry{{
			ID:       "large-major",
			Severity: "major",
			Issue:    strings.Repeat("x", maxReviewFindingsPromptBytes),
			Status:   "open",
		}},
	}
	if err := writeJSONWithNewline(filepath.Join(runDir, findingsLedgerFilename), ledger); err != nil {
		t.Fatal(err)
	}

	_, err := reviewFindingsPromptContext(runDir)
	if err == nil || !strings.Contains(err.Error(), "open blocker/major findings context exceeds") {
		t.Fatalf("reviewFindingsPromptContext() error = %v, want bounded-context failure", err)
	}
}
