package tagteam

import (
	"os"
	"path/filepath"
	"strings"
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

func TestFindingsLedgerReconcilesAbsentQualityGateFinding(t *testing.T) {
	runDir := t.TempDir()
	blocked := QualityGateResult{
		SchemaVersion: ArtifactSchemaVersion,
		Round:         1,
		Findings: []GateFinding{{
			ID:       "SCOPE-EXAMPLE",
			Gate:     "scope",
			Severity: "major",
			Message:  "changed path is outside the explicit allowlist",
			Path:     "governance/logs/file_size_report.json",
		}},
	}
	first, err := updateFindingsLedger(runDir, 1, nil, &blocked)
	if err != nil || first.OpenBlockerOrMajor != 1 {
		t.Fatalf("initial summary=%#v err=%v", first, err)
	}

	clean := QualityGateResult{SchemaVersion: ArtifactSchemaVersion, Round: 1, Findings: []GateFinding{}}
	second, err := updateFindingsLedger(runDir, 1, nil, &clean)
	if err != nil || second.OpenBlockerOrMajor != 0 {
		t.Fatalf("reconciled summary=%#v err=%v", second, err)
	}
	ledger, err := loadFindingsLedger(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger.Entries) != 1 || ledger.Entries[0].Status != "resolved" {
		t.Fatalf("ledger=%#v", ledger)
	}
	if ledger.Entries[0].Evidence != "not present in complete quality-gate evaluation for round 1" {
		t.Fatalf("resolution evidence=%q", ledger.Entries[0].Evidence)
	}
}

func TestReplaceQualityGateForRoundReplacesResumedRound(t *testing.T) {
	old := QualityGateResult{Round: 1, Blocking: true}
	current := QualityGateResult{Round: 1, Blocking: false}
	results := replaceQualityGateForRound([]QualityGateResult{old, old}, current)
	if len(results) != 1 || results[0].Blocking {
		t.Fatalf("same-round replacement=%#v", results)
	}
	results = replaceQualityGateForRound(results, QualityGateResult{Round: 2})
	if len(results) != 2 || results[1].Round != 2 {
		t.Fatalf("new-round append=%#v", results)
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

func TestResolveNonBlockingReviewFindingRequiresEvidenceAndPreservesGates(t *testing.T) {
	runDir := t.TempDir()
	review := currentReviewForTest()
	review.Findings[0].Severity = "minor"
	normalizeReview(review)
	if _, err := updateFindingsLedger(runDir, 1, review, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveNonBlockingReviewFinding(runDir, review.Findings[0].ID, ""); err == nil {
		t.Fatal("expected missing evidence rejection")
	}
	summary, err := ResolveNonBlockingReviewFinding(runDir, review.Findings[0].ID, "commit 8000a57; full validation passed")
	if err != nil || summary.OpenTotal != 0 {
		t.Fatalf("resolved summary=%#v err=%v", summary, err)
	}
	ledger, err := loadFindingsLedger(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := ledger.Entries[0]; got.Status != "fixed" || got.Evidence != "commit 8000a57; full validation passed" {
		t.Fatalf("resolved finding=%#v", got)
	}

	gate := QualityGateResult{Findings: []GateFinding{{Severity: "minor", Message: "host check", Path: "README.md"}}}
	if _, err := updateFindingsLedger(runDir, 2, nil, &gate); err != nil {
		t.Fatal(err)
	}
	gateID := stableGateFindingID(gate.Findings[0])
	if _, err := ResolveNonBlockingReviewFinding(runDir, gateID, "not permitted"); err == nil || !strings.Contains(err.Error(), "not a review finding") {
		t.Fatalf("quality gate resolution error=%v", err)
	}
}

func TestResolveNonBlockingReviewFindingRejectsMajor(t *testing.T) {
	runDir := t.TempDir()
	review := currentReviewForTest()
	normalizeReview(review)
	if _, err := updateFindingsLedger(runDir, 1, review, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveNonBlockingReviewFinding(runDir, review.Findings[0].ID, "commit 8000a57"); err == nil || !strings.Contains(err.Error(), "blocker or major") {
		t.Fatalf("major resolution error=%v", err)
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

func TestListFindingsAtStateRootUsesExplicitStore(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	stateRoot := t.TempDir()
	runDir, err := RunDirForCLIAtStateRoot(repo, stateRoot, "external-run")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	review := currentReviewForTest()
	normalizeReview(review)
	if _, err := updateFindingsLedger(runDir, 1, review, nil); err != nil {
		t.Fatal(err)
	}

	report, err := ListFindingsAtStateRoot(repo, stateRoot, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.OpenTotal != 1 || len(report.Entries) != 1 || report.Entries[0].RunID != "external-run" {
		t.Fatalf("explicit-store report = %#v", report)
	}
	canonicalStateRoot, err := canonicalPath(stateRoot, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(report.RunsRoot, canonicalStateRoot+string(filepath.Separator)) {
		t.Fatalf("runs root %q is not under explicit state root %q", report.RunsRoot, canonicalStateRoot)
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
