package tagteam

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const findingsLedgerFilename = "findings.json"

type FindingEntry struct {
	ID         string    `json:"id"`
	Source     string    `json:"source"`
	Severity   string    `json:"severity"`
	File       string    `json:"file,omitempty"`
	Line       int       `json:"line,omitempty"`
	Issue      string    `json:"issue"`
	Fix        string    `json:"fix,omitempty"`
	Status     string    `json:"status"`
	Evidence   string    `json:"evidence,omitempty"`
	Reason     string    `json:"reason,omitempty"`
	ApprovedBy string    `json:"approved_by,omitempty"`
	FirstRound int       `json:"first_round,omitempty"`
	LastRound  int       `json:"last_round,omitempty"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type FindingsLedger struct {
	SchemaVersion int            `json:"schema_version"`
	RunID         string         `json:"run_id"`
	UpdatedAt     time.Time      `json:"updated_at"`
	Entries       []FindingEntry `json:"entries"`
}

type FindingsSummary struct {
	Path               string `json:"path,omitempty"`
	OpenBlockerOrMajor int    `json:"open_blocker_or_major"`
	OpenTotal          int    `json:"open_total"`
}

type PersistedFinding struct {
	RunID      string       `json:"run_id"`
	LedgerPath string       `json:"ledger_path"`
	Finding    FindingEntry `json:"finding"`
}

type FindingsReport struct {
	SchemaVersion int                `json:"schema_version"`
	RunsRoot      string             `json:"runs_root"`
	OpenTotal     int                `json:"open_total"`
	Entries       []PersistedFinding `json:"entries"`
}

// ListFindings returns findings from every persisted run for a repository.
func ListFindings(workdir string, includeAll bool) (FindingsReport, error) {
	return ListFindingsAtStateRoot(workdir, "", includeAll)
}

// ListFindingsAtStateRoot returns findings from the repository's selected
// authoritative store. An empty state root retains pointer and legacy lookup.
func ListFindingsAtStateRoot(workdir, stateRoot string, includeAll bool) (FindingsReport, error) {
	runsRoot := RunsRootForCLI(workdir)
	if strings.TrimSpace(stateRoot) != "" {
		locator, err := resolveStateLocator(workdir, stateRoot)
		if err != nil {
			return FindingsReport{}, err
		}
		runsRoot = locator.RunsRoot
	}
	report := FindingsReport{SchemaVersion: ArtifactSchemaVersion, RunsRoot: runsRoot, Entries: []PersistedFinding{}}
	runs, err := os.ReadDir(runsRoot)
	if os.IsNotExist(err) {
		return report, nil
	}
	if err != nil {
		return FindingsReport{}, fmt.Errorf("read runs root: %w", err)
	}
	for _, run := range runs {
		if !run.IsDir() {
			continue
		}
		runDir := filepath.Join(runsRoot, run.Name())
		ledgerPath := filepath.Join(runDir, findingsLedgerFilename)
		if _, err := os.Stat(ledgerPath); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return FindingsReport{}, fmt.Errorf("inspect findings ledger %s: %w", ledgerPath, err)
		}
		ledger, err := loadFindingsLedger(runDir)
		if err != nil {
			return FindingsReport{}, fmt.Errorf("load findings ledger %s: %w", ledgerPath, err)
		}
		for _, entry := range ledger.Entries {
			if entry.Status == "open" {
				report.OpenTotal++
			}
			if !includeAll && entry.Status != "open" {
				continue
			}
			report.Entries = append(report.Entries, PersistedFinding{RunID: run.Name(), LedgerPath: ledgerPath, Finding: entry})
		}
	}
	sort.Slice(report.Entries, func(i, j int) bool {
		if report.Entries[i].Finding.UpdatedAt.Equal(report.Entries[j].Finding.UpdatedAt) {
			if report.Entries[i].RunID == report.Entries[j].RunID {
				return report.Entries[i].Finding.ID < report.Entries[j].Finding.ID
			}
			return report.Entries[i].RunID < report.Entries[j].RunID
		}
		return report.Entries[i].Finding.UpdatedAt.After(report.Entries[j].Finding.UpdatedAt)
	})
	return report, nil
}

func stableFindingID(finding Finding) string {
	canonical := strings.Join([]string{
		strings.ToLower(strings.TrimSpace(finding.Severity)),
		filepath.ToSlash(strings.TrimSpace(finding.File)),
		fmt.Sprintf("%d", finding.Line),
		strings.TrimSpace(finding.Issue),
		strings.TrimSpace(finding.Fix),
	}, "\x00")
	sum := sha256.Sum256([]byte(canonical))
	return "finding-" + hex.EncodeToString(sum[:8])
}

func stableGateFindingID(finding GateFinding) string {
	return stableFindingID(Finding{Severity: finding.Severity, File: finding.Path, Issue: finding.Message, Fix: "resolve quality gate"})
}

func notApplicableDataLossChecks(evidence string) *DataLossChecks {
	check := DataLossCheck{Status: "not_applicable", Evidence: evidence}
	return &DataLossChecks{
		MalformedInputPreservation: check,
		AnnotationHistoryRetention: check,
		AmbiguousIdentityHandling:  check,
		ReadOnlyNonMutation:        check,
	}
}

func loadFindingsLedger(runDir string) (FindingsLedger, error) {
	path := filepath.Join(runDir, findingsLedgerFilename)
	ledger := FindingsLedger{SchemaVersion: ArtifactSchemaVersion, RunID: filepath.Base(runDir), Entries: []FindingEntry{}}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ledger, nil
	}
	if err != nil {
		return FindingsLedger{}, err
	}
	if err := json.Unmarshal(data, &ledger); err != nil {
		return FindingsLedger{}, fmt.Errorf("decode findings ledger: %w", err)
	}
	if ledger.SchemaVersion != ArtifactSchemaVersion {
		return FindingsLedger{}, fmt.Errorf("unsupported findings schema_version %d", ledger.SchemaVersion)
	}
	return ledger, nil
}

func updateFindingsLedger(runDir string, round int, review *Review, gates *QualityGateResult) (FindingsSummary, error) {
	ledger, err := loadFindingsLedger(runDir)
	if err != nil {
		return FindingsSummary{}, err
	}
	now := time.Now().UTC()
	entries := make(map[string]FindingEntry, len(ledger.Entries))
	for _, entry := range ledger.Entries {
		entries[entry.ID] = entry
	}
	if review != nil {
		for _, disposition := range review.PriorFindingDispositions {
			entry, ok := entries[disposition.FindingID]
			if !ok {
				return FindingsSummary{}, fmt.Errorf("review disposed unknown finding %q", disposition.FindingID)
			}
			entry.Status = disposition.Status
			entry.Evidence = disposition.Evidence
			entry.UpdatedAt = now
			entries[entry.ID] = entry
		}
		for _, finding := range review.Findings {
			id := finding.ID
			if id == "" {
				id = stableFindingID(finding)
			}
			entry, ok := entries[id]
			if !ok {
				entry.FirstRound = round
			}
			entry.ID = id
			entry.Source = "review"
			entry.Severity = finding.Severity
			entry.File = finding.File
			entry.Line = finding.Line
			entry.Issue = finding.Issue
			entry.Fix = finding.Fix
			entry.Status = "open"
			entry.LastRound = round
			entry.UpdatedAt = now
			entries[id] = entry
		}
	}
	if gates != nil {
		currentGateIDs := make(map[string]struct{}, len(gates.Findings))
		for _, finding := range gates.Findings {
			id := stableGateFindingID(finding)
			currentGateIDs[id] = struct{}{}
			entry := entries[id]
			entry.ID = id
			entry.Source = "quality_gate"
			entry.Severity = finding.Severity
			entry.File = finding.Path
			entry.Issue = finding.Message
			entry.Fix = "resolve the gate violation or record evidence through review"
			entry.Status = "open"
			entry.LastRound = round
			if entry.FirstRound == 0 {
				entry.FirstRound = round
			}
			entry.UpdatedAt = now
			entries[id] = entry
		}
		// A quality-gate result is a complete evaluation of the current diff,
		// unlike a review response. Reconcile only prior open gate findings that
		// are absent from this result; review findings still require an explicit
		// reviewer disposition before they can close.
		for id, entry := range entries {
			if entry.Source != "quality_gate" || entry.Status != "open" {
				continue
			}
			if _, present := currentGateIDs[id]; present {
				continue
			}
			entry.Status = "resolved"
			entry.Evidence = fmt.Sprintf("not present in complete quality-gate evaluation for round %d", round)
			entry.LastRound = round
			entry.UpdatedAt = now
			entries[id] = entry
		}
	}
	ledger.Entries = ledger.Entries[:0]
	for _, entry := range entries {
		ledger.Entries = append(ledger.Entries, entry)
	}
	sort.Slice(ledger.Entries, func(i, j int) bool { return ledger.Entries[i].ID < ledger.Entries[j].ID })
	ledger.SchemaVersion = ArtifactSchemaVersion
	ledger.UpdatedAt = now
	path := filepath.Join(runDir, findingsLedgerFilename)
	if err := writeJSONWithNewline(path, ledger); err != nil {
		return FindingsSummary{}, err
	}
	return summarizeFindings(path, ledger), nil
}

func summarizeFindings(path string, ledger FindingsLedger) FindingsSummary {
	summary := FindingsSummary{Path: path}
	for _, entry := range ledger.Entries {
		if entry.Status != "open" {
			continue
		}
		summary.OpenTotal++
		if entry.Severity == "blocker" || entry.Severity == "major" {
			summary.OpenBlockerOrMajor++
		}
	}
	return summary
}

func DeferFinding(runDir, findingID, reason string) (FindingsSummary, error) {
	if strings.TrimSpace(reason) == "" {
		return FindingsSummary{}, fmt.Errorf("deferral reason is required")
	}
	ledger, err := loadFindingsLedger(runDir)
	if err != nil {
		return FindingsSummary{}, err
	}
	found := false
	for i := range ledger.Entries {
		if ledger.Entries[i].ID != findingID {
			continue
		}
		if ledger.Entries[i].Severity != "blocker" && ledger.Entries[i].Severity != "major" {
			return FindingsSummary{}, fmt.Errorf("finding %q is not blocker or major", findingID)
		}
		ledger.Entries[i].Status = "deferred_with_approval"
		ledger.Entries[i].Reason = strings.TrimSpace(reason)
		ledger.Entries[i].ApprovedBy = "operator"
		ledger.Entries[i].UpdatedAt = time.Now().UTC()
		found = true
		break
	}
	if !found {
		return FindingsSummary{}, fmt.Errorf("finding %q not found", findingID)
	}
	ledger.UpdatedAt = time.Now().UTC()
	path := filepath.Join(runDir, findingsLedgerFilename)
	if err := writeJSONWithNewline(path, ledger); err != nil {
		return FindingsSummary{}, err
	}
	return summarizeFindings(path, ledger), nil
}

// ResolveNonBlockingReviewFinding closes an open minor or nit review finding
// after the operator has independently applied and verified its fix. Blocking
// findings and host quality gates remain governed by their existing review or
// gate paths and cannot be closed through this operator command.
func ResolveNonBlockingReviewFinding(runDir, findingID, evidence string) (FindingsSummary, error) {
	evidence = strings.TrimSpace(evidence)
	if evidence == "" {
		return FindingsSummary{}, fmt.Errorf("resolution evidence is required")
	}
	ledger, err := loadFindingsLedger(runDir)
	if err != nil {
		return FindingsSummary{}, err
	}
	for index := range ledger.Entries {
		entry := &ledger.Entries[index]
		if entry.ID != findingID {
			continue
		}
		if entry.Status != "open" {
			return FindingsSummary{}, fmt.Errorf("finding %q is not open (status %q)", findingID, entry.Status)
		}
		if entry.Source != "review" {
			return FindingsSummary{}, fmt.Errorf("finding %q is not a review finding", findingID)
		}
		if entry.Severity == "blocker" || entry.Severity == "major" {
			return FindingsSummary{}, fmt.Errorf("finding %q is blocker or major and requires a new review disposition or approved deferral", findingID)
		}
		if entry.Severity != "minor" && entry.Severity != "nit" {
			return FindingsSummary{}, fmt.Errorf("finding %q is not a nonblocking review finding", findingID)
		}
		entry.Status = "fixed"
		entry.Evidence = evidence
		entry.UpdatedAt = time.Now().UTC()
		ledger.UpdatedAt = entry.UpdatedAt
		path := filepath.Join(runDir, findingsLedgerFilename)
		if err := writeJSONWithNewline(path, ledger); err != nil {
			return FindingsSummary{}, err
		}
		return summarizeFindings(path, ledger), nil
	}
	return FindingsSummary{}, fmt.Errorf("finding %q not found", findingID)
}
