package tagteam

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// gitPrepareSyncedRun checkpoints user work on the Tagteam run branch, then
// advances only the original checked-out branch by fast-forward before rebasing
// the checkpoint. It never pushes, auto-merges divergence, or resolves conflicts.
func gitPrepareSyncedRun(workdir, runID string) (string, error) {
	sourceBranch, err := gitCurrentBranch(workdir)
	if err != nil {
		return "", err
	}
	runBranch := "tagteam/" + runID
	dirty, err := gitDirty(workdir)
	if err != nil {
		return "", &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("inspect worktree before sync: %w", err)}
	}
	if dirty {
		if err := gitRejectUnmergedPaths(workdir); err != nil {
			return "", err
		}
		if _, err := gitCreateCheckpointBranch(workdir, runBranch, runID); err != nil {
			return "", err
		}
		if err := gitSwitchBranch(workdir, sourceBranch, "restore source branch before sync"); err != nil {
			return "", err
		}
	}

	advanced, err := gitFastForwardUpstream(workdir, sourceBranch)
	if err != nil {
		return "", err
	}
	if dirty {
		if err := gitSwitchBranch(workdir, runBranch, "return to checkpoint branch after sync"); err != nil {
			return "", err
		}
		if advanced {
			if err := gitRebaseCheckpoint(workdir, runBranch, sourceBranch); err != nil {
				return "", err
			}
		}
	} else if err := gitCreateBranch(workdir, runBranch); err != nil {
		return "", err
	}

	baseline, err := ensureGitRepo(workdir)
	if err != nil {
		return "", err
	}
	return baseline, nil
}

// gitPrepareIntegratedRun turns the selected worktree's pre-existing changes
// into a normal local commit on its checked-out branch before creating the
// isolated run branch. It only integrates that branch's configured upstream;
// other local branches and linked worktrees remain untouched. A temporary
// checkpoint branch lets every conflict or setup failure leave the source
// branch clean and unchanged.
func gitPrepareIntegratedRun(workdir, runID string) (string, error) {
	sourceBranch, err := gitCurrentBranchForPolicy(workdir, "integrate")
	if err != nil {
		return "", err
	}
	dirty, err := gitDirty(workdir)
	if err != nil {
		return "", &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("inspect worktree before integration: %w", err)}
	}
	if dirty {
		if err := gitRejectUnmergedPathsForPolicy(workdir, "integrate"); err != nil {
			return "", err
		}
	}

	checkpointBranch := "tagteam/preflight/" + runID
	if dirty {
		if _, err := gitCreateCheckpointBranch(workdir, checkpointBranch, runID); err != nil {
			return "", err
		}
	} else if err := gitCreateBranch(workdir, checkpointBranch); err != nil {
		return "", err
	}

	if err := gitMergeTrackedUpstreamIntoCandidate(workdir, sourceBranch, checkpointBranch, dirty); err != nil {
		return "", err
	}
	runBranch := "tagteam/" + runID
	if err := gitCreateBranchAt(workdir, runBranch, checkpointBranch); err != nil {
		return "", err
	}
	if err := gitSwitchBranch(workdir, sourceBranch, "restore source branch before applying prepared integration"); err != nil {
		return "", err
	}
	if _, err := runCommand(context.Background(), workdir, "git", "merge", "--ff-only", checkpointBranch); err != nil {
		return "", &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("apply prepared integration %q to source branch %q: %w", checkpointBranch, sourceBranch, err)}
	}
	if err := gitSwitchBranch(workdir, runBranch, "switch to isolated run branch after integration"); err != nil {
		return "", err
	}
	// The preflight candidate is now reachable from both the source and run
	// branches. Drop only this Tagteam-owned temporary pointer; the run branch
	// remains the recovery and review branch for the model work.
	if _, err := runCommand(context.Background(), workdir, "git", "branch", "-d", checkpointBranch); err != nil {
		return "", &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("remove integrated preflight branch %q: %w", checkpointBranch, err)}
	}
	return ensureGitRepo(workdir)
}

func gitMergeTrackedUpstreamIntoCandidate(workdir, sourceBranch, checkpointBranch string, hasCheckpoint bool) error {
	upstream, hasUpstream, err := gitCurrentUpstream(workdir, sourceBranch)
	if err != nil {
		return restoreSourceAfterCandidateFailure(workdir, sourceBranch, err)
	}
	if !hasUpstream {
		return nil
	}
	if _, err := runCommand(context.Background(), workdir, "git", "fetch", "--quiet"); err != nil {
		return restoreSourceAfterCandidateFailure(workdir, sourceBranch, fmt.Errorf("integration preflight fetch failed for branch %q: %w", sourceBranch, err))
	}
	ahead, behind, err := gitBranchAheadBehind(workdir, sourceBranch, upstream)
	if err != nil {
		return restoreSourceAfterCandidateFailure(workdir, sourceBranch, err)
	}
	if behind == 0 {
		return nil
	}
	args := []string{"merge", "--no-edit", "--no-ff", upstream}
	if ahead == 0 && !hasCheckpoint {
		args = []string{"merge", "--ff-only", upstream}
	}
	if _, err := runCommand(context.Background(), workdir, "git", args...); err != nil {
		return abortCandidateMergeAndRestoreSource(workdir, sourceBranch, checkpointBranch, upstream, err)
	}
	return nil
}

func abortCandidateMergeAndRestoreSource(workdir, sourceBranch, checkpointBranch, upstream string, cause error) error {
	if unmerged, inspectErr := gitHasUnmergedPaths(workdir); inspectErr != nil {
		return restoreSourceAfterCandidateFailure(workdir, sourceBranch, fmt.Errorf("integrate preflight merge of %q into %q failed and conflict state could not be inspected: %w", upstream, checkpointBranch, inspectErr))
	} else if unmerged {
		if _, abortErr := runCommand(context.Background(), workdir, "git", "merge", "--abort"); abortErr != nil {
			return &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("integrate preflight merge of %q into %q failed and could not abort safely: %w", upstream, checkpointBranch, abortErr)}
		}
	}
	return restoreSourceAfterCandidateFailure(workdir, sourceBranch, fmt.Errorf("integrate preflight could not merge tracked upstream %q into %q; source branch was restored and checkpoint %q was preserved: %w", upstream, checkpointBranch, checkpointBranch, cause))
}

func restoreSourceAfterCandidateFailure(workdir, sourceBranch string, cause error) error {
	if err := gitSwitchBranch(workdir, sourceBranch, "restore source branch after integration failure"); err != nil {
		return err
	}
	return &ExitError{Code: ExitPreflightFailed, Err: cause}
}

func gitCurrentBranch(workdir string) (string, error) {
	return gitCurrentBranchForPolicy(workdir, "sync")
}

func gitCurrentBranchForPolicy(workdir, policy string) (string, error) {
	branch, err := runCommand(context.Background(), workdir, "git", "branch", "--show-current")
	if err != nil {
		return "", &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("resolve current branch for %s: %w", policy, err)}
	}
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return "", &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("git_safety=%s requires an attached branch; check out a branch or set git_safety = \"branch\"", policy)}
	}
	return branch, nil
}

func gitRejectUnmergedPaths(workdir string) error {
	return gitRejectUnmergedPathsForPolicy(workdir, "sync")
}

func gitRejectUnmergedPathsForPolicy(workdir, policy string) error {
	unmerged, err := gitHasUnmergedPaths(workdir)
	if err != nil {
		return &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("inspect unresolved Git paths before %s: %w", policy, err)}
	}
	if !unmerged {
		return nil
	}
	return &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("git_safety=%s refuses unresolved merge conflicts; resolve them before starting Tagteam", policy)}
}

func gitHasUnmergedPaths(workdir string) (bool, error) {
	out, err := runCommand(context.Background(), workdir, "git", "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

func gitSwitchBranch(workdir, branch, action string) error {
	if _, err := runCommand(context.Background(), workdir, "git", "switch", branch); err != nil {
		return &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("%s %q: %w", action, branch, err)}
	}
	return nil
}

func gitCreateBranchAt(workdir, branch, startPoint string) error {
	if _, err := runCommand(context.Background(), workdir, "git", "branch", branch, startPoint); err != nil {
		return &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("create isolated run branch %q from %q: %w", branch, startPoint, err)}
	}
	return nil
}

func gitFastForwardUpstream(workdir, branch string) (bool, error) {
	upstream, hasUpstream, err := gitCurrentUpstream(workdir, branch)
	if err != nil {
		return false, err
	}
	if !hasUpstream {
		return false, nil
	}
	if _, err := runCommand(context.Background(), workdir, "git", "fetch", "--quiet"); err != nil {
		return false, &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("sync preflight fetch failed for branch %q: %w", branch, err)}
	}
	ahead, behind, err := gitAheadBehind(workdir)
	if err != nil {
		return false, err
	}
	if behind == 0 {
		return false, nil
	}
	if ahead > 0 {
		return false, &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("sync preflight refuses non-fast-forward update: branch %q diverges from %q; merge or rebase it manually, or set git_safety = \"branch\"", branch, upstream)}
	}
	if _, err := runCommand(context.Background(), workdir, "git", "merge", "--ff-only", upstream); err != nil {
		return false, &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("sync preflight fast-forward failed for branch %q from %q: %w", branch, upstream, err)}
	}
	return true, nil
}

func gitCurrentUpstream(workdir, branch string) (string, bool, error) {
	out, err := runCommand(context.Background(), workdir, "git", "for-each-ref", "--format=%(upstream:short)", "refs/heads/"+branch)
	if err != nil {
		return "", false, &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("resolve upstream for branch %q: %w", branch, err)}
	}
	upstream := strings.TrimSpace(strings.SplitN(out, "\n", 2)[0])
	if upstream == "" {
		return "", false, nil
	}
	return upstream, true, nil
}

func gitAheadBehind(workdir string) (int, int, error) {
	return gitBranchAheadBehind(workdir, "HEAD", "@{upstream}")
}

func gitBranchAheadBehind(workdir, branch, upstream string) (int, int, error) {
	out, err := runCommand(context.Background(), workdir, "git", "rev-list", "--left-right", "--count", branch+"..."+upstream)
	if err != nil {
		return 0, 0, &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("inspect tracked-branch divergence: %w", err)}
	}
	counts := strings.Fields(out)
	if len(counts) != 2 {
		return 0, 0, &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("inspect tracked-branch divergence: unexpected Git output %q", strings.TrimSpace(out))}
	}
	ahead, aheadErr := strconv.Atoi(counts[0])
	behind, behindErr := strconv.Atoi(counts[1])
	if aheadErr != nil || behindErr != nil || ahead < 0 || behind < 0 {
		return 0, 0, &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("inspect tracked-branch divergence: unexpected Git counts %q", strings.TrimSpace(out))}
	}
	return ahead, behind, nil
}

func gitRebaseCheckpoint(workdir, runBranch, sourceBranch string) error {
	if _, err := runCommand(context.Background(), workdir, "git", "rebase", sourceBranch); err == nil {
		return nil
	} else {
		rebaseErr := err
		if _, abortErr := runCommand(context.Background(), workdir, "git", "rebase", "--abort"); abortErr != nil {
			return &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("sync preflight checkpoint rebase failed for %q onto %q and could not abort safely: %w", runBranch, sourceBranch, abortErr)}
		}
		if switchErr := gitSwitchBranch(workdir, sourceBranch, "restore source branch after checkpoint rebase conflict"); switchErr != nil {
			return switchErr
		}
		return &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("sync preflight checkpoint %q conflicts with updated branch %q; rebase was aborted and the checkpoint was preserved: %w", runBranch, sourceBranch, rebaseErr)}
	}
}
