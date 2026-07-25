package tagteam

import (
	"fmt"
	"time"
)

func workPlanBudgetSeconds(timeout time.Duration) int64 {
	return int64(timeout.Seconds() * 0.8)
}

// validateWorkPlanBudget limits the selected package for a normal run. When
// auto-next is enabled, every package can execute and must fit the same cap.
func validateWorkPlanBudget(plan WorkPlan, budgetSeconds int64, validateAll bool) error {
	if budgetSeconds <= 0 {
		return nil
	}
	packages := plan.Packages
	if !validateAll {
		selected, ok := plan.Selected()
		if !ok {
			return fmt.Errorf("selected package %q not found in work plan", plan.SelectedPackage)
		}
		packages = []WorkPackage{selected}
	}
	for _, pkg := range packages {
		if int64(pkg.EstimatedSeconds) > budgetSeconds {
			return fmt.Errorf("package %s estimated_seconds=%d exceeds calibrated package budget=%d", pkg.ID, pkg.EstimatedSeconds, budgetSeconds)
		}
	}
	return nil
}
