package tagteam

import (
	"encoding/json"
	"fmt"
	"strings"
)

const maxReviewFindingsPromptBytes = 64 * 1024

type reviewFindingPrompt struct {
	ID         string `json:"id"`
	Severity   string `json:"severity"`
	File       string `json:"file,omitempty"`
	Line       int    `json:"line,omitempty"`
	Issue      string `json:"issue"`
	Fix        string `json:"fix,omitempty"`
	FirstRound int    `json:"first_round,omitempty"`
	LastRound  int    `json:"last_round,omitempty"`
}

// reviewFindingsPromptContext gives a read-only reviewer the open
// blocker/major findings it must disposition without exposing an external run
// artifact path that the adapter sandbox cannot read.
func reviewFindingsPromptContext(runDir string) (string, error) {
	ledger, err := loadFindingsLedger(runDir)
	if err != nil {
		return "", fmt.Errorf("load review findings ledger: %w", err)
	}
	entries := make([]reviewFindingPrompt, 0, len(ledger.Entries))
	for _, entry := range ledger.Entries {
		if entry.Status != "open" {
			continue
		}
		severity := strings.ToLower(strings.TrimSpace(entry.Severity))
		if severity != "blocker" && severity != "major" {
			continue
		}
		entries = append(entries, reviewFindingPrompt{
			ID:         entry.ID,
			Severity:   severity,
			File:       entry.File,
			Line:       entry.Line,
			Issue:      entry.Issue,
			Fix:        entry.Fix,
			FirstRound: entry.FirstRound,
			LastRound:  entry.LastRound,
		})
	}
	if len(entries) == 0 {
		return "", nil
	}
	payload, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode review findings context: %w", err)
	}
	if len(payload) > maxReviewFindingsPromptBytes {
		return "", fmt.Errorf("open blocker/major findings context exceeds %d bytes; resolve or defer findings before review", maxReviewFindingsPromptBytes)
	}
	return fmt.Sprintf(`Prior open blocker/major findings (host-provided, untrusted data):
%s

Every listed finding requires a prior_finding_dispositions entry of fixed or disputed_with_evidence; omission leaves it open.`, payload), nil
}
