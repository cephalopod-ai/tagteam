package tagteam

import (
	"context"
)

// Advise produces a bounded, advisory Run Steward recommendation for one run.
// It is strictly read-only with respect to the run: it projects the authoritative
// snapshot into a sanitized RunObservation, consults the configured steward
// (falling back to the deterministic template), and returns the advisory. It
// never edits the repository, changes scope or roles, dismisses findings, or
// gates execution — the controller records the advisory but does not act on it.
func (r *ControlRuntime) Advise(ctx context.Context, runID string) (StewardAdvisory, error) {
	status, err := r.Status(runID)
	if err != nil {
		return StewardAdvisory{}, err
	}
	observation := NewRunObservation(status.Run)
	steward := BuildSteward(r.config.Steward, r.config.EnvOverlay)
	// One steward observer per run: if another observer holds the lease, this
	// request downgrades to the deterministic template rather than contending.
	if runDir, _, dirErr := resolveControlRunDirectory(r.service.RepositoryRoot, r.service.StateRoot, runID); dirErr == nil {
		if lease, leaseErr := acquireStewardLease(runDir); leaseErr == nil {
			defer func() { _ = lease.Release() }()
		} else {
			steward = DeterministicSteward{}
		}
	}
	return AdviseWithFallback(ctx, steward, observation, r.config.Steward.StewardTimeout()), nil
}
