package tagteam

import (
	"strings"
	"testing"

	"github.com/cephalopod-ai/tagteam/internal/sharedcatalog"
)

// TestModelPresetConstantsExistInSharedCatalog keeps the compile-time preset
// names and the vendored fleet roster from drifting apart. A default that
// names a model the roster does not carry would ship a target no other repo
// in the fleet recognizes.
func TestModelPresetConstantsExistInSharedCatalog(t *testing.T) {
	for _, model := range []string{
		openAIGPT6Astra, claudeFable51, grok46,
		agyGemini38FlashLow, agyGemini38FlashMedium, agyGemini38FlashHigh,
		agyGemini36FlashLow, agyGemini36FlashMedium, agyGemini36FlashHigh,
	} {
		if _, ok := sharedcatalog.LookupModel(model); !ok {
			t.Errorf("preset model %q is absent from the shared catalog", model)
		}
	}
}

// TestAgyGemini36FlashModelChoicesAreRosterBacked pins the tiers the TUI
// picker offers. The choices now come from the roster's free-form "series"
// field, so a renamed series would return an empty slice — and every TUI test
// that ranges over these choices, including the one guarding Gemini out of the
// worker slot, would pass vacuously instead of failing.
func TestAgyGemini36FlashModelChoicesAreRosterBacked(t *testing.T) {
	want := []string{agyGemini36FlashLow, agyGemini36FlashMedium, agyGemini36FlashHigh}
	got := AgyGemini36FlashModelChoices()
	if len(got) != len(want) {
		t.Fatalf("AgyGemini36FlashModelChoices() = %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("AgyGemini36FlashModelChoices() = %#v, want %#v", got, want)
		}
	}
}

// TestDefaultRoleTargetsAreRosterBacked checks the shipped role defaults
// against the roster, including the adapter's permitted roles. The local
// relay scout is deliberately exempt: it names a user-run Ollama model, not a
// maintained first-party one.
func TestDefaultRoleTargetsAreRosterBacked(t *testing.T) {
	cases := []struct{ target, role string }{
		{defaultSupervisorTarget, "reviewer"},
		{defaultSupervisorFallback, "reviewer"},
		{defaultWorkerTarget, "editor"},
		{defaultWorkerFallback, "editor"},
		{defaultAdversarialCoderTarget, "editor"},
	}
	for _, testCase := range cases {
		target, role := testCase.target, testCase.role
		adapter, model, ok := strings.Cut(target, ":")
		if !ok {
			t.Fatalf("default target %q is not adapter:model", target)
		}
		if _, found := sharedcatalog.LookupModel(model); !found {
			t.Errorf("default target %q names a model absent from the shared catalog", target)
		}
		if !sharedcatalog.AdapterAllowsRole(adapter, role) {
			t.Errorf("default target %q holds role %q, which the shared roster does not permit for %s", target, role, adapter)
		}
	}
	if adapter, _, _ := strings.Cut(defaultRelayScoutTarget, ":"); adapter != "openai-compatible" {
		t.Errorf("relay scout default = %q, want an openai-compatible endpoint", defaultRelayScoutTarget)
	}
}
