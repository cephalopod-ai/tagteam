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

func gitCurrentBranch(workdir string) (string, error) {
	branch, err := runCommand(context.Background(), workdir, "git", "branch", "--show-current")
	if err != nil {
		return "", &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("resolve current branch for sync: %w", err)}
	}
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return "", &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("git_safety=sync requires an attached branch; check out a branch or set git_safety = \"branch\"")}
	}
	return branch, nil
}

func gitRejectUnmergedPaths(workdir string) error {
	out, err := runCommand(context.Background(), workdir, "git", "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("inspect unresolved Git paths before sync: %w", err)}
	}
	if strings.TrimSpace(out) == "" {
		return nil
	}
	return &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("git_safety=sync refuses unresolved merge conflicts; resolve them before starting Tagteam")}
}

func gitSwitchBranch(workdir, branch, action string) error {
	if _, err := runCommand(context.Background(), workdir, "git", "switch", branch); err != nil {
		return &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("%s %q: %w", action, branch, err)}
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
	out, err := runCommand(context.Background(), workdir, "git", "rev-list", "--left-right", "--count", "HEAD...@{upstream}")
	if err != nil {
		return 0, 0, &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("inspect branch divergence before sync: %w", err)}
	}
	counts := strings.Fields(out)
	if len(counts) != 2 {
		return 0, 0, &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("inspect branch divergence before sync: unexpected Git output %q", strings.TrimSpace(out))}
	}
	ahead, aheadErr := strconv.Atoi(counts[0])
	behind, behindErr := strconv.Atoi(counts[1])
	if aheadErr != nil || behindErr != nil || ahead < 0 || behind < 0 {
		return 0, 0, &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("inspect branch divergence before sync: unexpected Git counts %q", strings.TrimSpace(out))}
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
