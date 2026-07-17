package tagteam

import (
	"context"
)

const controlMaxStewardStates = 1024

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
	steward := r.stewardForRun(runID)
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

func (r *ControlRuntime) stewardForRun(runID string) Steward {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return DeterministicSteward{}
	}
	if steward, ok := r.stewards[runID]; ok {
		return steward
	}
	// Bound daemon memory without evicting an existing run's budget state. Once
	// the cap is reached, uncached runs retain safe deterministic advisories.
	if len(r.stewards) >= controlMaxStewardStates {
		return DeterministicSteward{}
	}
	steward := BuildSteward(r.config.Steward, r.config.EnvOverlay)
	r.stewards[runID] = steward
	return steward
}
