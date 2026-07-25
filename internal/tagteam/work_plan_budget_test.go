package tagteam

import (
	"strings"
	"testing"
)

func TestValidateWorkPlanBudgetOnlyRequiresSelectedPackageUnlessAutoNext(t *testing.T) {
	plan := WorkPlan{
		Packages: []WorkPackage{
			{ID: "P1", EstimatedSeconds: 300},
			{ID: "P2", EstimatedSeconds: 600},
		},
		SelectedPackage: "P1",
	}
	if err := validateWorkPlanBudget(plan, 480, false); err != nil {
		t.Fatalf("single-package run rejected deferred package: %v", err)
	}
	if err := validateWorkPlanBudget(plan, 480, true); err == nil {
		t.Fatal("expected auto-next validation to reject oversized deferred package")
	}
	plan.SelectedPackage = "P2"
	if err := validateWorkPlanBudget(plan, 480, false); err == nil {
		t.Fatal("expected selected oversized package rejection")
	}
}

func TestBuildSupervisorWorkPlanPromptIncludesCalibratedBudget(t *testing.T) {
	prompt := BuildSupervisorWorkPlanPrompt("/repo", "repair docs", 5, "", 480)
	if !strings.Contains(prompt, "estimated_seconds to no more than 480") {
		t.Fatalf("prompt does not state calibrated budget:\n%s", prompt)
	}
}

func TestBuildSupervisorWorkPlanPromptRequiresAChangeForImplementationRequests(t *testing.T) {
	prompt := BuildSupervisorWorkPlanPrompt("/repo", "repair docs", 5, "", 480)
	for _, want := range []string{
		"selected package must perform a requested change",
		"Do not select a read-only triage, inspection, or planning package first",
		"request itself is explicitly planning-, audit-, review-, or investigation-only",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}
