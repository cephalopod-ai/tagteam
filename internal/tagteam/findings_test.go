package tagteam

import (
	"os"
	"path/filepath"
	"testing"
)

func currentReviewForTest() *Review {
	return &Review{
		SchemaVersion:            ReviewSchemaVersion,
		Verdict:                  "needs_changes",
		Summary:                  "issue found",
		Findings:                 []Finding{{Severity: "major", File: "main.go", Line: 1, Issue: "bug", Fix: "fix it"}},
		TestSuggestions:          []string{},
		DataLossChecks:           notApplicableDataLossChecks("not applicable in fixture"),
		PriorFindingDispositions: []FindingDisposition{},
	}
}

func TestFindingsLedgerKeepsOmittedMajorOpenUntilDisposition(t *testing.T) {
	runDir := t.TempDir()
	review := currentReviewForTest()
	normalizeReview(review)
	summary, err := updateFindingsLedger(runDir, 1, review, nil)
	if err != nil || summary.OpenBlockerOrMajor != 1 {
		t.Fatalf("initial summary=%#v err=%v", summary, err)
	}
	pass := currentReviewForTest()
	pass.Verdict = "pass"
	pass.Findings = []Finding{}
	pass.Summary = "fixed"
	pass.PriorFindingDispositions = []FindingDisposition{{FindingID: review.Findings[0].ID, Status: "fixed", Evidence: "focused regression test passes"}}
	summary, err = updateFindingsLedger(runDir, 2, pass, nil)
	if err != nil || summary.OpenBlockerOrMajor != 0 {
		t.Fatalf("resolved summary=%#v err=%v", summary, err)
	}
	if summary.Path != filepath.Join(runDir, findingsLedgerFilename) {
		t.Fatalf("ledger path = %q", summary.Path)
	}
}

func TestDeferFindingRequiresOperatorReason(t *testing.T) {
	runDir := t.TempDir()
	review := currentReviewForTest()
	normalizeReview(review)
	if _, err := updateFindingsLedger(runDir, 1, review, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := DeferFinding(runDir, review.Findings[0].ID, ""); err == nil {
		t.Fatal("expected empty reason rejection")
	}
	summary, err := DeferFinding(runDir, review.Findings[0].ID, "accepted operational risk")
	if err != nil || summary.OpenBlockerOrMajor != 0 {
		t.Fatalf("summary=%#v err=%v", summary, err)
	}
}

func TestListFindingsAggregatesRunsAndFiltersResolved(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runsRoot := RunsRootForCLI(repo)
	openDir := filepath.Join(runsRoot, "run-open")
	resolvedDir := filepath.Join(runsRoot, "run-resolved")
	if err := os.MkdirAll(openDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(resolvedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	openReview := currentReviewForTest()
	normalizeReview(openReview)
	if _, err := updateFindingsLedger(openDir, 1, openReview, nil); err != nil {
		t.Fatal(err)
	}
	resolvedReview := currentReviewForTest()
	normalizeReview(resolvedReview)
	if _, err := updateFindingsLedger(resolvedDir, 1, resolvedReview, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := DeferFinding(resolvedDir, resolvedReview.Findings[0].ID, "accepted risk"); err != nil {
		t.Fatal(err)
	}

	report, err := ListFindings(repo, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.OpenTotal != 1 || len(report.Entries) != 1 || report.Entries[0].RunID != "run-open" {
		t.Fatalf("open report = %#v", report)
	}
	all, err := ListFindings(repo, true)
	if err != nil {
		t.Fatal(err)
	}
	if all.OpenTotal != 1 || len(all.Entries) != 2 {
		t.Fatalf("all report = %#v", all)
	}
}

func TestListFindingsRejectsMalformedLedger(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runDir := filepath.Join(RunsRootForCLI(repo), "bad-run")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, findingsLedgerFilename), []byte("{bad"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ListFindings(repo, false); err == nil {
		t.Fatal("expected malformed ledger error")
	}
}

func TestReviewRejectsFailedDataLossCheckWithoutBlockingFinding(t *testing.T) {
	review := currentReviewForTest()
	review.Findings = []Finding{}
	review.DataLossChecks.MalformedInputPreservation = DataLossCheck{Status: "fail", Evidence: "malformed record was dropped"}
	if err := review.ValidateCurrent(); err == nil {
		t.Fatal("expected failed data-loss check without major finding to be rejected")
	}
}
