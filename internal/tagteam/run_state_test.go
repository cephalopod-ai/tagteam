package tagteam

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestClassifyRoleFailureWorkerNonTimeout(t *testing.T) {
	// A non-timeout worker/coder failure must be classified as unavailable, not
	// as a timeout (the two branches previously returned the same reason).
	nonTimeout := errors.New("coder adapter binary not found")
	for _, role := range []string{"worker", "coder", "solo"} {
		if got := classifyRoleFailure(role, nonTimeout); got != ReasonWorkerUnavailable {
			t.Fatalf("classifyRoleFailure(%q, non-timeout) = %q, want %q", role, got, ReasonWorkerUnavailable)
		}
		if got := classifyRoleFailure(role, context.DeadlineExceeded); got != ReasonWorkerTimeout {
			t.Fatalf("classifyRoleFailure(%q, deadline) = %q, want %q", role, got, ReasonWorkerTimeout)
		}
	}
}

func TestClassifyRoleFailureScoutContextSentinel(t *testing.T) {
	err := fmt.Errorf("wrapped: %w", errScoutContextTooSmall)
	if got := classifyRoleFailure("scout", err); got != ReasonScoutContextTooSmall {
		t.Fatalf("classifyRoleFailure(scout, sentinel) = %q, want %q", got, ReasonScoutContextTooSmall)
	}
	// An unrelated scout error that merely mentions the words no longer trips
	// the context classification (it is sentinel-based, not substring-based).
	if got := classifyRoleFailure("scout", errors.New("scout lost network context")); got != ReasonScoutUnavailable {
		t.Fatalf("classifyRoleFailure(scout, plain) = %q, want %q", got, ReasonScoutUnavailable)
	}
}

func TestReasonForExitBlockingFindings(t *testing.T) {
	if got := reasonForExit(ExitBlockingFindings); got != ReasonBlockingFindings {
		t.Fatalf("reasonForExit(ExitBlockingFindings) = %q, want %q", got, ReasonBlockingFindings)
	}
	if got := reasonForExit(ExitTestsFailed); got != ReasonTestFailed {
		t.Fatalf("reasonForExit(ExitTestsFailed) = %q, want %q", got, ReasonTestFailed)
	}
}

func TestUnlimitedInvocationBudgetStillAccountsUsage(t *testing.T) {
	budget := &InvocationBudget{}
	if err := budget.Before("worker", "implementation"); err != nil {
		t.Fatalf("unlimited budget rejected invocation: %v", err)
	}
	if budget.Used != 1 {
		t.Fatalf("budget used = %d, want 1", budget.Used)
	}
}

func TestFinalizeRunStateWiresBudgetExhausted(t *testing.T) {
	final := &FinalRun{ExitCode: ExitPreflightFailed, BlockingReason: string(ReasonBudgetExceeded)}
	finalizeRunState(final)
	if !final.Budgets.Exhausted || final.Budgets.ReasonCode != ReasonBudgetExceeded {
		t.Fatalf("budget exhaustion not wired: %#v", final.Budgets)
	}

	// A non-budget block must leave the budget fields untouched.
	other := &FinalRun{ExitCode: ExitAdapterFailure, BlockingReason: string(ReasonReviewerUnavailable)}
	finalizeRunState(other)
	if other.Budgets.Exhausted {
		t.Fatalf("non-budget block should not mark budget exhausted: %#v", other.Budgets)
	}
}

func TestFinalizeRunStateMakesHostGateFailureAuthoritative(t *testing.T) {
	final := &FinalRun{
		ExitCode: ExitBlockingFindings,
		Verdict:  "pass",
		Review:   &Review{Verdict: "pass", Summary: "reviewer approved"},
	}
	finalizeRunState(final)
	if final.Status != RunStatusBlocked || final.Verdict != "needs_changes" {
		t.Fatalf("host gate result not reflected in final status: %#v", final)
	}
	if final.Review.Verdict != "pass" {
		t.Fatalf("reviewer verdict should remain separately inspectable: %#v", final.Review)
	}
}

func TestFinalizeRunStateNormalizesFailedDoneVerdict(t *testing.T) {
	final := &FinalRun{ExitCode: ExitTestsFailed, Verdict: "done"}
	finalizeRunState(final)
	if final.Status != RunStatusFailed || final.Verdict != "error" {
		t.Fatalf("failed run retained success-like verdict: %#v", final)
	}
}

func TestSetRoleStatusRedactsOverlaySecret(t *testing.T) {
	final := &FinalRun{}
	initFinalState(final, RunOptions{EnvOverlay: map[string]string{"PROVIDER_API_KEY": "super-secret-value"}})
	setRoleStatus(final, "coder", RoleTarget{Adapter: "claude"}, "failed", ReasonWorkerUnavailable, "auth failed: super-secret-value in header")
	msg := final.RoleStatuses["coder"].Message
	if msg == "" || strings.Contains(msg, "super-secret-value") {
		t.Fatalf("overlay secret not redacted from role status message: %q", msg)
	}
}
