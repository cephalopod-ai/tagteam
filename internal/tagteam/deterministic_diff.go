package tagteam

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func deterministicDiffPatch(ctx context.Context, workdir, baseline, indexPath string) ([]byte, error) {
	patch, _, _, _, err := deterministicDiffOutputs(ctx, workdir, baseline, indexPath)
	return patch, err
}

func deterministicDiffOutputs(ctx context.Context, workdir, baseline, indexPath string) ([]byte, []byte, []byte, []byte, error) {
	defer os.Remove(indexPath)
	defer os.Remove(indexPath + ".lock")
	pathspecPath := indexPath + ".pathspec"
	defer os.Remove(pathspecPath)
	env := []string{"LC_ALL=C", "GIT_INDEX_FILE=" + indexPath}
	if _, err := runGitCommandBytes(ctx, workdir, env, "read-tree", baseline); err != nil {
		return nil, nil, nil, nil, err
	}
	tracked, err := runGitCommandBytes(ctx, workdir, env, "ls-files", "-z", "--", ".")
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if len(tracked) > 0 {
		if _, err := runGitCommandBytes(ctx, workdir, env, "add", "-u", "--", "."); err != nil {
			return nil, nil, nil, nil, err
		}
	}
	pathspec, err := deterministicAdditionalPathspec(ctx, workdir, baseline)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if len(pathspec) > 0 {
		if err := os.WriteFile(pathspecPath, pathspec, 0o644); err != nil {
			return nil, nil, nil, nil, err
		}
		// Additional paths include ordinary untracked files plus additions that
		// were explicitly staged in the real index. The latter can be ignored
		// session/governance artifacts staged with `git add -f`; preserve them in
		// this disposable review index without discovering any other ignored files.
		if _, err := runGitCommandBytes(ctx, workdir, env, "add", "-f", "--pathspec-from-file="+pathspecPath, "--pathspec-file-nul"); err != nil {
			return nil, nil, nil, nil, err
		}
	}
	patch, err := runGitCommandBytes(ctx, workdir, env, "-c", "core.quotepath=false", "diff", "--cached", "--no-ext-diff", "--no-color", "--binary", "--full-index", "--find-renames=50%", baseline, "--", ".")
	if err != nil {
		return nil, nil, nil, nil, err
	}
	numstat, err := runGitCommandBytes(ctx, workdir, env, "-c", "core.quotepath=false", "diff", "--cached", "--no-ext-diff", "--no-color", "--numstat", baseline, "--", ".")
	if err != nil {
		return nil, nil, nil, nil, err
	}
	statusZ, err := runGitCommandBytes(ctx, workdir, env, "-c", "core.quotepath=false", "diff", "--cached", "--no-ext-diff", "--no-color", "--name-status", "-z", "--find-renames=50%", baseline, "--", ".")
	if err != nil {
		return nil, nil, nil, nil, err
	}
	numstatZ, err := runGitCommandBytes(ctx, workdir, env, "-c", "core.quotepath=false", "diff", "--cached", "--no-ext-diff", "--no-color", "--numstat", "-z", baseline, "--", ".")
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return patch, numstat, statusZ, numstatZ, nil
}

func deterministicAdditionalPathspec(ctx context.Context, workdir, baseline string) ([]byte, error) {
	untracked, err := runGitCommandBytes(ctx, workdir, []string{"LC_ALL=C"}, "ls-files", "-z", "--others", "--exclude-standard", "--", ".")
	if err != nil {
		return nil, err
	}
	// A staged new file is no longer reported by ls-files --others. Rebuilding
	// the temporary index from the baseline must therefore include additions
	// from the real index as well, or review artifacts silently omit them.
	stagedAdds, err := runGitCommandBytes(ctx, workdir, []string{"LC_ALL=C"}, "diff", "--cached", "--name-only", "--diff-filter=ACR", "-z", "--find-renames=50%", baseline, "--", ".")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	paths := []string{}
	for _, raw := range append(splitNULTokens(untracked), splitNULTokens(stagedAdds)...) {
		path := strings.TrimPrefix(raw, "./")
		if path == "" || path == ".tagteam" || strings.HasPrefix(path, ".tagteam/") {
			continue
		}
		if seen[path] {
			continue
		}
		if _, err := os.Lstat(filepath.Join(workdir, filepath.FromSlash(path))); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		seen[path] = true
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var buf bytes.Buffer
	for _, path := range paths {
		buf.WriteString(path)
		buf.WriteByte(0)
	}
	return buf.Bytes(), nil
}
