package tagteam

import (
	"strings"
	"testing"
)

func TestHostBaselineEvidenceRequiresReconciliationAndBoundsOutput(t *testing.T) {
	baseline := &TestRun{
		Command: "giles scan --read-only --format json .",
		Passed:  true,
		Output:  strings.Repeat("x", maxHostBaselineEvidenceBytes+1),
	}
	prompt := BuildSupervisorBriefPrompt("/repo", "repair the ledger", false, baseline)
	for _, want := range []string{
		"Host baseline evidence (executed by the host before agents started)",
		"Result: passed",
		"claim that conflicts with it must be directly verified",
		"...[truncated]",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("supervisor prompt missing %q", want)
		}
	}
	if strings.Contains(prompt, strings.Repeat("x", maxHostBaselineEvidenceBytes+1)) {
		t.Fatal("supervisor prompt included unbounded baseline output")
	}
}

func TestHostBaselineEvidenceReachesRelayScoutAndWorker(t *testing.T) {
	baseline := &TestRun{Command: "giles scan --read-only --format json .", Passed: true, Output: "feature_ledger findings: 0"}
	scout := BuildScoutPrompt("/repo", "repair the ledger", "brief", "recon", "pre", "", "", "", baseline)
	worker, err := buildRoundEditorPrompt(nil, RunOptions{Mode: ModeRelay, Workdir: "/repo", Prompt: "repair the ledger"}, 1, "/run", "baseline", baseline, "", Review{}, nil, RelayContext{}, nil, nil, "brief", false)
	if err != nil {
		t.Fatalf("buildRoundEditorPrompt() error = %v", err)
	}
	for name, prompt := range map[string]string{"scout": scout, "worker": worker} {
		if !strings.Contains(prompt, "feature_ledger findings: 0") || !strings.Contains(prompt, "claim that conflicts with it must be directly verified") {
			t.Fatalf("%s prompt did not include reconcilable host baseline evidence:\n%s", name, prompt)
		}
	}
}
