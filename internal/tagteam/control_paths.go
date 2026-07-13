package tagteam

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolveControlRepository resolves a path to the real Git worktree root and
// derives the Tagteam repository identity. Callers may supply a subdirectory or
// symlink alias; the returned root is always the canonical worktree top-level.
func ResolveControlRepository(root string) (ControlRepository, error) {
	return resolveControlRepository(root)
}

func resolveControlRepository(root string) (ControlRepository, error) {
	if strings.TrimSpace(root) == "" {
		return ControlRepository{}, fmt.Errorf("resolve repository root: path is empty")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return ControlRepository{}, fmt.Errorf("resolve repository root: %w", err)
	}
	start := abs
	if resolved, evalErr := filepath.EvalSymlinks(abs); evalErr == nil {
		start = resolved
	}
	top, err := runCommand(context.Background(), start, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return ControlRepository{}, fmt.Errorf("repository root is not a Git worktree: %w", err)
	}
	canonicalTop, err := canonicalPath(strings.TrimSpace(top), true)
	if err != nil {
		return ControlRepository{}, fmt.Errorf("resolve Git worktree root: %w", err)
	}
	repoID, err := deriveRepoID(canonicalTop)
	if err != nil {
		return ControlRepository{}, fmt.Errorf("derive repository identity: %w", err)
	}
	return ControlRepository{CanonicalRoot: canonicalTop, RepoID: repoID}, nil
}

// canonicalizeControlAllowedPaths applies syntax rejection, then resolves each
// scope through real paths under the repository. Results are stable
// repo-relative scopes suitable for approval digests and RunOptions.
func canonicalizeControlAllowedPaths(repoRoot string, raw []string) ([]string, error) {
	if len(raw) == 0 || len(raw) > controlMaxAllowedPaths {
		return nil, fmt.Errorf("allowed_paths must contain between 1 and %d entries", controlMaxAllowedPaths)
	}
	if err := ValidateAllowedPaths(raw); err != nil {
		return nil, err
	}
	canonicalRoot, err := canonicalPath(repoRoot, true)
	if err != nil {
		return nil, fmt.Errorf("resolve repository for allowed_paths: %w", err)
	}
	seenReal := make(map[string]string, len(raw))
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		lexical, err := normalizeExplicitAllowedPath(item)
		if err != nil {
			return nil, err
		}
		scope, realKey, err := resolveControlAllowedScope(canonicalRoot, lexical)
		if err != nil {
			return nil, fmt.Errorf("invalid allowed_paths %q: %w", item, err)
		}
		if previous, exists := seenReal[realKey]; exists {
			return nil, fmt.Errorf("invalid allowed_paths %q: duplicates %q after real-path resolution to %q", item, previous, realKey)
		}
		seenReal[realKey] = item
		out = append(out, scope)
	}
	return out, nil
}

func revalidateControlAllowedPaths(repoRoot string, expected []string) error {
	got, err := canonicalizeControlAllowedPaths(repoRoot, expected)
	if err != nil {
		return err
	}
	if len(got) != len(expected) {
		return fmt.Errorf("allowed_paths changed under the repository since approval")
	}
	for i := range expected {
		if got[i] != expected[i] {
			return fmt.Errorf("allowed_paths changed under the repository since approval")
		}
	}
	return nil
}

func resolveControlAllowedScope(repoRoot, lexical string) (scope string, realKey string, err error) {
	directoryPrefix := strings.HasSuffix(lexical, "/")
	relative := strings.TrimSuffix(lexical, "/")
	if lexical == "." || relative == "." {
		// Treat the whole-repository scope as a directory real-path key so a
		// root-resolving alias (repoRoot + "/") collides with "." consistently.
		return ".", controlRealPathDupKey(repoRoot, true), nil
	}
	abs := filepath.Join(repoRoot, filepath.FromSlash(relative))
	resolved, err := resolvePathUnderRepository(repoRoot, abs)
	if err != nil {
		return "", "", err
	}
	rel, err := filepath.Rel(repoRoot, resolved)
	if err != nil {
		return "", "", fmt.Errorf("scope escapes the repository")
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", "", fmt.Errorf("scope escapes the repository")
	}
	if directoryPrefix {
		if info, statErr := os.Lstat(resolved); statErr == nil {
			if info.Mode()&os.ModeSymlink == 0 && !info.IsDir() {
				return "", "", fmt.Errorf("directory scope resolves to a non-directory")
			}
			if info.Mode()&os.ModeSymlink != 0 {
				if targetInfo, targetErr := os.Stat(resolved); targetErr == nil && !targetInfo.IsDir() {
					return "", "", fmt.Errorf("directory scope resolves to a non-directory")
				}
			}
		}
		if rel != "." {
			rel += "/"
		}
	}
	return rel, controlRealPathDupKey(resolved, directoryPrefix), nil
}

// controlRealPathDupKey builds a stable uniqueness key for allowed-path
// real targets. Directory scopes always use a trailing separator so root
// aliases (".", "root-alias/") share one key after Clean.
func controlRealPathDupKey(resolved string, directory bool) string {
	key := filepath.Clean(resolved)
	if !directory {
		return key
	}
	sep := string(filepath.Separator)
	if key == sep {
		return key
	}
	return strings.TrimSuffix(key, sep) + sep
}

// resolvePathUnderRepository walks path components under the repository,
// evaluating existing symlink parents and rejecting broken or escaping links.
// Non-existent trailing components are allowed only when every existing parent
// stays inside the repository.
func resolvePathUnderRepository(repoRoot, abs string) (string, error) {
	abs = filepath.Clean(abs)
	repoRoot = filepath.Clean(repoRoot)
	rel, err := filepath.Rel(repoRoot, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		if resolved, evalErr := filepath.EvalSymlinks(abs); evalErr == nil {
			if resolved == repoRoot || pathWithin(repoRoot, resolved) {
				return resolved, nil
			}
			return "", fmt.Errorf("symlink escapes the repository")
		}
		if isBrokenSymlink(abs) {
			return "", fmt.Errorf("broken symlink in allowed scope")
		}
		return "", fmt.Errorf("scope escapes the repository")
	}
	current := repoRoot
	if rel == "." {
		return current, nil
	}
	parts := strings.Split(rel, string(filepath.Separator))
	for i, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			return "", fmt.Errorf("parent traversal is forbidden")
		}
		next := filepath.Join(current, part)
		info, lerr := os.Lstat(next)
		if os.IsNotExist(lerr) {
			joined := current
			for _, name := range parts[i:] {
				if name == "" || name == "." {
					continue
				}
				if name == ".." {
					return "", fmt.Errorf("parent traversal is forbidden")
				}
				joined = filepath.Join(joined, name)
			}
			if joined != repoRoot && !pathWithin(repoRoot, joined) {
				return "", fmt.Errorf("scope escapes the repository")
			}
			return joined, nil
		}
		if lerr != nil {
			return "", lerr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			resolved, err := filepath.EvalSymlinks(next)
			if err != nil {
				return "", fmt.Errorf("broken symlink in allowed scope")
			}
			if resolved != repoRoot && !pathWithin(repoRoot, resolved) {
				return "", fmt.Errorf("symlink escapes the repository")
			}
			current = resolved
			continue
		}
		if resolved, err := filepath.EvalSymlinks(next); err == nil {
			if resolved != repoRoot && !pathWithin(repoRoot, resolved) {
				return "", fmt.Errorf("path escapes the repository")
			}
			current = resolved
		} else {
			current = next
		}
	}
	return current, nil
}

func isBrokenSymlink(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return false
	}
	_, err = filepath.EvalSymlinks(path)
	return err != nil
}

// resolveControlRunDirectory returns the canonical run directory under the
// resolved state root. Symlinked run directories that escape the runs root are
// rejected before any artifact read or write.
func resolveControlRunDirectory(repositoryRoot, stateRootOverride, runID string) (string, StateLocator, error) {
	if err := validateRunID(runID); err != nil {
		return "", StateLocator{}, err
	}
	repository, err := resolveControlRepository(repositoryRoot)
	if err != nil {
		return "", StateLocator{}, err
	}
	locator, err := resolveStateLocator(repository.CanonicalRoot, stateRootOverride)
	if err != nil {
		return "", StateLocator{}, fmt.Errorf("resolve run state: %w", err)
	}
	runDir, err := locator.RunDir(runID)
	if err != nil {
		return "", StateLocator{}, err
	}
	info, err := os.Lstat(runDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", locator, fmt.Errorf("run %q not found", runID)
		}
		return "", locator, err
	}
	canonicalRunsRoot, err := ensureCanonicalRunsRoot(locator)
	if err != nil {
		return "", locator, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		resolved, evalErr := filepath.EvalSymlinks(runDir)
		if evalErr != nil {
			return "", locator, fmt.Errorf("run %q is a broken symlink", runID)
		}
		if !pathWithin(canonicalRunsRoot, resolved) {
			return "", locator, fmt.Errorf("run %q escapes the resolved state root", runID)
		}
		info, err = os.Stat(resolved)
		if err != nil {
			return "", locator, err
		}
		if !info.IsDir() {
			return "", locator, fmt.Errorf("run %q is not a directory", runID)
		}
		return resolved, locator, nil
	}
	if !info.IsDir() {
		return "", locator, fmt.Errorf("run %q is not a directory", runID)
	}
	canonicalRunDir, err := canonicalPath(runDir, true)
	if err != nil {
		return "", locator, fmt.Errorf("resolve run directory: %w", err)
	}
	if !pathWithin(canonicalRunsRoot, canonicalRunDir) {
		return "", locator, fmt.Errorf("run %q escapes the resolved state root", runID)
	}
	return canonicalRunDir, locator, nil
}

// ensureCanonicalRunsRoot resolves the host-derived runs directory under
// <stateRoot>/<repoID>/runs and requires every existing component (including a
// symlinked repo-id parent or runs directory) to stay inside that repository
// state directory. An escaping or broken link never becomes the trust boundary.
// Missing intermediate directories are allowed only when their lexical path stays
// under the host state root (so Start can prepare a clean store).
func ensureCanonicalRunsRoot(locator StateLocator) (string, error) {
	if strings.TrimSpace(locator.RepoID) == "" {
		return "", fmt.Errorf("resolve runs root: repository identity is empty")
	}
	stateRoot, err := canonicalPath(locator.StateRoot, false)
	if err != nil {
		return "", fmt.Errorf("resolve host state root: %w", err)
	}
	expectedRepoRoot := filepath.Clean(filepath.Join(stateRoot, locator.RepoID))
	if err := rejectControlPathOutsideBoundary(expectedRepoRoot, stateRoot, "repository state directory"); err != nil {
		return "", fmt.Errorf("repository state directory escapes the host state root")
	}
	resolvedRepoRoot, err := resolveControlStateDir(expectedRepoRoot, stateRoot, "repository state directory")
	if err != nil {
		return "", err
	}
	expectedRunsRoot := filepath.Clean(filepath.Join(stateRoot, locator.RepoID, "runs"))
	if err := rejectControlPathOutsideBoundary(expectedRunsRoot, resolvedRepoRoot, "runs root"); err != nil {
		return "", fmt.Errorf("runs root escapes the repository state directory")
	}
	// Also reject a runs symlink that only appears under a resolved internal
	// repo-state parent (stateRoot/repoID -> internal sibling).
	if resolvedRepoRoot != expectedRepoRoot {
		altRuns := filepath.Clean(filepath.Join(resolvedRepoRoot, "runs"))
		if err := rejectControlPathOutsideBoundary(altRuns, resolvedRepoRoot, "runs root"); err != nil {
			return "", fmt.Errorf("runs root escapes the repository state directory")
		}
	}
	resolvedRuns, err := resolveControlStateDir(expectedRunsRoot, resolvedRepoRoot, "runs root")
	if err != nil {
		return "", err
	}
	if resolvedRuns != resolvedRepoRoot && !pathWithin(resolvedRepoRoot, resolvedRuns) {
		return "", fmt.Errorf("runs root escapes the repository state directory")
	}
	return resolvedRuns, nil
}

// rejectControlPathOutsideBoundary fails when an existing path resolves outside
// boundary, or when a missing path is not lexically under boundary.
func rejectControlPathOutsideBoundary(path, boundary, label string) error {
	boundary = filepath.Clean(boundary)
	path = filepath.Clean(path)
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			if path == boundary || pathWithin(boundary, path) {
				return nil
			}
			return fmt.Errorf("%s escapes its trust boundary", label)
		}
		return err
	}
	_ = info
	return validateControlPathWithinBoundary(boundary, path, label)
}

// resolveControlStateDir returns the real directory for path when present,
// requiring it to stay under boundary. Missing paths return the cleaned path
// only when it remains lexically under boundary (for MkdirAll preparation).
func resolveControlStateDir(path, boundary, label string) (string, error) {
	boundary = filepath.Clean(boundary)
	path = filepath.Clean(path)
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			if path == boundary || pathWithin(boundary, path) {
				return path, nil
			}
			return "", fmt.Errorf("%s escapes its trust boundary", label)
		}
		return "", fmt.Errorf("resolve %s: %w", label, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		resolved, evalErr := filepath.EvalSymlinks(path)
		if evalErr != nil {
			return "", fmt.Errorf("%s is a broken symlink", label)
		}
		if resolved != boundary && !pathWithin(boundary, resolved) {
			return "", fmt.Errorf("%s escapes its trust boundary", label)
		}
		targetInfo, statErr := os.Stat(resolved)
		if statErr != nil {
			return "", fmt.Errorf("resolve %s: %w", label, statErr)
		}
		if !targetInfo.IsDir() {
			return "", fmt.Errorf("%s is not a directory", label)
		}
		return resolved, nil
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", label)
	}
	canonical, err := canonicalPath(path, true)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", label, err)
	}
	if canonical != boundary && !pathWithin(boundary, canonical) {
		return "", fmt.Errorf("%s escapes its trust boundary", label)
	}
	return canonical, nil
}

// validateControlWritablePath ensures a path about to be read or written stays
// inside the canonical run directory after symlink resolution.
func validateControlWritablePath(runDir, path string) error {
	root, err := canonicalPath(runDir, true)
	if err != nil {
		return fmt.Errorf("resolve control run directory: %w", err)
	}
	return validateControlPathWithinBoundary(root, path, "canonical run directory")
}

// validateControlPathWithinBoundary requires path (or its parent when absent)
// to resolve inside boundary after symlink evaluation. Callers that must
// survive run-directory replacement pass the resolved runs root as boundary
// rather than trusting a previously resolved runDir string.
func validateControlPathWithinBoundary(boundary, path, label string) error {
	boundary = filepath.Clean(boundary)
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			parent, parentErr := canonicalPath(filepath.Dir(path), true)
			if parentErr != nil {
				return fmt.Errorf("resolve control path parent: %w", parentErr)
			}
			if parent != boundary && !pathWithin(boundary, parent) {
				return fmt.Errorf("control path escapes the %s", label)
			}
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		resolved, evalErr := filepath.EvalSymlinks(path)
		if evalErr != nil {
			return fmt.Errorf("control path is a broken symlink")
		}
		if resolved != boundary && !pathWithin(boundary, resolved) {
			return fmt.Errorf("control path escapes the %s", label)
		}
		return nil
	}
	realPath, err := canonicalPath(path, true)
	if err != nil {
		return err
	}
	if realPath != boundary && !pathWithin(boundary, realPath) {
		return fmt.Errorf("control path escapes the %s", label)
	}
	return nil
}

// resolveControlCancelIO re-resolves the run directory under the state root
// immediately before cancel-request reads or writes. Validation uses the
// resolved runs-root boundary so a replaced run directory cannot redirect I/O
// by making both the trust root and child resolve outside the runs tree.
func resolveControlCancelIO(repositoryRoot, stateRoot, runID string) (runDir, cancelPath, runsRoot string, err error) {
	runDir, locator, err := resolveControlRunDirectory(repositoryRoot, stateRoot, runID)
	if err != nil {
		return "", "", "", err
	}
	runsRoot, err = ensureCanonicalRunsRoot(locator)
	if err != nil {
		return "", "", "", err
	}
	if runDir != runsRoot && !pathWithin(runsRoot, runDir) {
		return "", "", "", fmt.Errorf("run %q escapes the resolved state root", runID)
	}
	cancelPath = filepath.Join(runDir, controlCancelRequestName)
	// Boundary is the runs root, not the (possibly replaced) run directory.
	if err := validateControlPathWithinBoundary(runsRoot, cancelPath, "resolved state root"); err != nil {
		return "", "", "", err
	}
	// Also require the lexical parent to still be the resolved run directory.
	if err := validateControlWritablePath(runDir, cancelPath); err != nil {
		return "", "", "", err
	}
	again, _, err := resolveControlRunDirectory(repositoryRoot, stateRoot, runID)
	if err != nil {
		return "", "", "", err
	}
	if again != runDir {
		return "", "", "", fmt.Errorf("run %q path changed under the resolved state root", runID)
	}
	return runDir, cancelPath, runsRoot, nil
}

// readControlArtifactBytes validates that name stays inside runDir after
// symlink resolution, then reads it. Escaping or broken links fail closed
// without consuming external content.
func readControlArtifactBytes(runDir, name string) ([]byte, error) {
	path := filepath.Join(runDir, name)
	if err := validateControlWritablePath(runDir, path); err != nil {
		return nil, fmt.Errorf("control artifact %s: %w", name, err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		// Validation already proved the real target is inside the run dir.
		// Require a regular file at the resolved target.
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil, fmt.Errorf("control artifact %s is a broken symlink", name)
		}
		targetInfo, err := os.Stat(resolved)
		if err != nil {
			return nil, err
		}
		if !targetInfo.Mode().IsRegular() {
			return nil, fmt.Errorf("control artifact %s must resolve to a regular file", name)
		}
		return os.ReadFile(resolved)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("control artifact %s must be a regular file", name)
	}
	return os.ReadFile(path)
}

// readControlOptionalArtifactBytes is like readControlArtifactBytes but treats
// a missing path as (nil, false, nil). Symlinks that escape are still errors.
func readControlOptionalArtifactBytes(runDir, name string) ([]byte, bool, error) {
	path := filepath.Join(runDir, name)
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil, false, nil
	} else if err != nil {
		return nil, false, err
	}
	data, err := readControlArtifactBytes(runDir, name)
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func readControlRunState(runDir string) (RunState, error) {
	data, err := readControlArtifactBytes(runDir, "state.json")
	if err != nil {
		return RunState{}, err
	}
	var state RunState
	if err := json.Unmarshal(data, &state); err != nil {
		return RunState{}, err
	}
	return state, nil
}

func readControlMeta(runDir string) (Meta, error) {
	data, err := readControlArtifactBytes(runDir, "meta.json")
	if err != nil {
		return Meta{}, err
	}
	var meta Meta
	if err := json.Unmarshal(data, &meta); err != nil {
		return Meta{}, err
	}
	return meta, nil
}

func readControlFinalOptional(runDir string) (FinalRun, bool, error) {
	data, present, err := readControlOptionalArtifactBytes(runDir, "final.json")
	if err != nil || !present {
		return FinalRun{}, present, err
	}
	var final FinalRun
	if err := json.Unmarshal(data, &final); err != nil {
		return FinalRun{}, true, err
	}
	return final, true, nil
}

// ensureControlWritableArtifacts re-checks optional write targets immediately
// before mutation so a post-assessment symlink replacement cannot redirect I/O.
func ensureControlWritableArtifacts(runDir string, names ...string) error {
	for _, name := range names {
		if err := validateControlWritablePath(runDir, filepath.Join(runDir, name)); err != nil {
			return fmt.Errorf("control artifact %s: %w", name, err)
		}
	}
	return nil
}
