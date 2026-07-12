package tagteam

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/google/shlex"
)

type RegressionResult struct {
	Status           string   `json:"status"`
	BaselineFailures []string `json:"baseline_failures,omitempty"`
	CurrentFailures  []string `json:"current_failures,omitempty"`
	NewFailures      []string `json:"new_failures,omitempty"`
	ResolvedFailures []string `json:"resolved_failures,omitempty"`
}

var failureIdentityPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?m)^--- FAIL: ([^\s(]+)`),
	regexp.MustCompile(`(?m)^FAIL\s+([^\s]+)`),
	regexp.MustCompile(`(?m)(?:FAILED\s+)?([^\s]+::[^\s]+)`),
	regexp.MustCompile(`(?m)^\s*●\s+(.+)$`),
	regexp.MustCompile(`(?m)^test\s+([^\s]+)\s+\.\.\.\s+FAILED$`),
}

func validateTestCommand(workdir, command string) error {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil
	}
	check := exec.Command("/bin/sh", "-n", "-c", command)
	if output, err := check.CombinedOutput(); err != nil {
		return &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("invalid test command syntax: %w: %s", err, strings.TrimSpace(string(output)))}
	}
	tokens, err := shlex.Split(command)
	if err != nil {
		return &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("parse test command: %w", err)}
	}
	for _, token := range tokens {
		if !literalTestPathCandidate(token) {
			continue
		}
		clean := filepath.Clean(token)
		absolute := clean
		if !filepath.IsAbs(clean) {
			absolute = filepath.Join(workdir, clean)
		}
		canonicalWorkdir, _ := canonicalPath(workdir, true)
		canonicalCandidate, canonicalErr := canonicalPath(absolute, true)
		if canonicalErr != nil {
			if os.IsNotExist(canonicalErr) {
				return &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("test path does not exist: %s", token)}
			}
			return &ExitError{Code: ExitPreflightFailed, Err: canonicalErr}
		}
		relative, relErr := filepath.Rel(canonicalWorkdir, canonicalCandidate)
		if relErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("test path escapes workdir: %s", token)}
		}
	}
	return nil
}

func literalTestPathCandidate(token string) bool {
	if token == "" || strings.HasPrefix(token, "-") || token == "./..." || strings.ContainsAny(token, `$*?[]{}|;&><`) {
		return false
	}
	extensions := []string{".go", ".py", ".js", ".jsx", ".ts", ".tsx", ".rs", ".sh"}
	for _, extension := range extensions {
		if strings.HasSuffix(token, extension) {
			return true
		}
	}
	return strings.HasPrefix(token, "./") || strings.HasPrefix(token, "../")
}

func isolatedTestDirectories(outputPath string) (string, string, error) {
	base := strings.TrimSuffix(filepath.Base(outputPath), filepath.Ext(outputPath))
	root := filepath.Join(filepath.Dir(outputPath), "test-isolation", sanitizeArtifactName(base))
	state := filepath.Join(root, "state")
	temp := filepath.Join(root, "tmp")
	if err := os.MkdirAll(state, 0o700); err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(temp, 0o700); err != nil {
		return "", "", err
	}
	return state, temp, nil
}

func extractFailureIdentities(output string) []string {
	return extractFailureIdentitiesWithRegex(output, "")
}

func extractFailureIdentitiesWithRegex(output, customPattern string) []string {
	seen := map[string]bool{}
	patterns := append([]*regexp.Regexp(nil), failureIdentityPatterns...)
	if strings.TrimSpace(customPattern) != "" {
		if pattern, err := regexp.Compile(customPattern); err == nil && pattern.NumSubexp() >= 1 {
			patterns = append(patterns, pattern)
		}
	}
	for _, pattern := range patterns {
		for _, match := range pattern.FindAllStringSubmatch(output, -1) {
			if len(match) < 2 {
				continue
			}
			identity := strings.TrimSpace(match[1])
			if identity != "" {
				seen[identity] = true
			}
		}
	}
	identities := make([]string, 0, len(seen))
	for identity := range seen {
		identities = append(identities, identity)
	}
	sort.Strings(identities)
	return identities
}

func compareRegression(baseline, current TestRun) RegressionResult {
	result := RegressionResult{
		Status:           "no_new_failures",
		BaselineFailures: append([]string(nil), baseline.FailureIdentities...),
		CurrentFailures:  append([]string(nil), current.FailureIdentities...),
	}
	baselineSet := map[string]bool{}
	currentSet := map[string]bool{}
	for _, identity := range baseline.FailureIdentities {
		baselineSet[identity] = true
	}
	for _, identity := range current.FailureIdentities {
		currentSet[identity] = true
		if !baselineSet[identity] {
			result.NewFailures = append(result.NewFailures, identity)
		}
	}
	for _, identity := range baseline.FailureIdentities {
		if !currentSet[identity] {
			result.ResolvedFailures = append(result.ResolvedFailures, identity)
		}
	}
	if len(result.NewFailures) > 0 {
		result.Status = "new_failures"
	} else if !current.Passed && len(current.FailureIdentities) == 0 {
		result.Status = "unknown"
	}
	return result
}

func runBaselineTest(ctx context.Context, opts RunOptions, runDir string) (*TestRun, error) {
	if opts.TestCmd == "" || opts.NoTest {
		return nil, nil
	}
	before, err := captureWorktreeSnapshot(ctx, opts.Workdir)
	if err != nil {
		return nil, fmt.Errorf("capture worktree before baseline test: %w", err)
	}
	path := filepath.Join(runDir, "baseline-test.txt")
	test, err := runTestCommand(ctx, opts.Workdir, opts.TestCmd, opts.Timeout, path, opts.DryRun, opts.EnvOverlay, opts.MaxOutputBytes, opts.TestIdentityRegex)
	if err != nil {
		return nil, err
	}
	after, err := captureWorktreeSnapshot(ctx, opts.Workdir)
	if err != nil {
		return nil, fmt.Errorf("capture worktree after baseline test: %w", err)
	}
	if changed := worktreeDelta(before, after); len(changed) > 0 {
		paths := make([]string, 0, len(changed))
		for _, changedPath := range changed {
			paths = append(paths, "baseline-test:"+changedPath)
		}
		return nil, &IntegrityViolationError{Paths: paths}
	}
	return &test, nil
}
