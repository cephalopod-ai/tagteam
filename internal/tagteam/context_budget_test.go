package tagteam

import (
	"strings"
	"testing"
)

func TestEstimatePromptTokensDeterministic(t *testing.T) {
	prompt := "short deterministic prompt"
	first := estimatePromptTokens(prompt)
	second := estimatePromptTokens(prompt)
	if first != second {
		t.Fatalf("estimates differ: %d vs %d", first, second)
	}
	if first != (len([]byte(prompt))+2)/3 {
		t.Fatalf("estimate = %d, want deterministic byte approximation", first)
	}
}

func TestEstimateScoutPromptBudgetStatuses(t *testing.T) {
	unknown := estimateScoutPromptBudget("prompt", ScoutContextLimit{})
	if unknown.Status != scoutContextStatusUnknown || !unknown.NoConfiguredLimit {
		t.Fatalf("unknown budget = %#v", unknown)
	}

	ok := estimateScoutPromptBudget(strings.Repeat("a", 90), ScoutContextLimit{MaxContextTokens: 100, ReservedOutputTokens: 10})
	if ok.Status != scoutContextStatusOK {
		t.Fatalf("ok status = %#v", ok)
	}

	near := estimateScoutPromptBudget(strings.Repeat("a", 210), ScoutContextLimit{MaxContextTokens: 100, ReservedOutputTokens: 10})
	if near.Status != scoutContextStatusNearLimit {
		t.Fatalf("near status = %#v", near)
	}

	exceeds := estimateScoutPromptBudget(strings.Repeat("a", 301), ScoutContextLimit{MaxContextTokens: 100, ReservedOutputTokens: 10})
	if exceeds.Status != scoutContextStatusExceeds {
		t.Fatalf("exceeds status = %#v", exceeds)
	}
}

func TestEstimateScoutPromptBudgetRetrievalIncreasesEstimate(t *testing.T) {
	base := BuildScoutPrompt("/repo", "ship it", "", "recon", "pre", "", "", "")
	withRetrieval := BuildScoutPrompt("/repo", "ship it", "", "recon", "pre", "", "", strings.Repeat("retrieval evidence ", 100))
	baseBudget := estimateScoutPromptBudget(base, ScoutContextLimit{MaxContextTokens: 10000})
	retrievalBudget := estimateScoutPromptBudget(withRetrieval, ScoutContextLimit{MaxContextTokens: 10000})
	if retrievalBudget.EstimatedInputTokens <= baseBudget.EstimatedInputTokens {
		t.Fatalf("retrieval estimate %d <= base estimate %d", retrievalBudget.EstimatedInputTokens, baseBudget.EstimatedInputTokens)
	}
}

func TestScoutContextLimitForOpenAICompatibleAlias(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Adapters.OpenAICompatible.MaxContextTokens = testIntPtr(32768)
	cfg.Adapters.OpenAICompatible.ReservedOutputTokens = testIntPtr(2048)
	got := scoutContextLimitForAdapter(cfg, "oai")
	if got.MaxContextTokens != 32768 || got.ReservedOutputTokens != 2048 {
		t.Fatalf("limit = %#v", got)
	}
}

func testIntPtr(v int) *int {
	return &v
}
