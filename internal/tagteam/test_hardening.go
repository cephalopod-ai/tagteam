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
	"time"

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

// isolatedTestDirectoriesForControlResume preserves the CLI isolation behavior
// while requiring each control-resume directory mutation to use a freshly
// resolved run directory. The individual mkdir steps make every parent
// canonical before it becomes a descendant used by a later step.
func isolatedTestDirectoriesForControlResume(gate *controlResumePathGate, outputPath string) (string, string, error) {
	if gate == nil {
		return isolatedTestDirectories(outputPath)
	}
	var state, temp string
	for _, name := range []string{"test-isolation", "root", "state", "tmp"} {
		target, err := makeControlResumeIsolationDirectory(gate, outputPath, name)
		if err != nil {
			return "", "", err
		}
		switch name {
		case "state":
			state = target
		case "tmp":
			temp = target
		}
	}
	if err := validateControlResumeTestIsolationDirectories(gate, outputPath, state, temp); err != nil {
		return "", "", err
	}
	return state, temp, nil
}

func makeControlResumeIsolationDirectory(gate *controlResumePathGate, outputPath, name string) (string, error) {
	runDir, err := gate.ready()
	if err != nil {
		return "", err
	}
	outputPath = rebuildControlResumeArtifactPath(gate.runDir, runDir, outputPath)
	base := strings.TrimSuffix(filepath.Base(outputPath), filepath.Ext(outputPath))
	isolation := filepath.Join(filepath.Dir(outputPath), "test-isolation")
	root := filepath.Join(isolation, sanitizeArtifactName(base))
	targets := map[string]string{
		"test-isolation": isolation,
		"root":           root,
		"state":          filepath.Join(root, "state"),
		"tmp":            filepath.Join(root, "tmp"),
	}
	target := targets[name]
	if err := validateControlPathWithinBoundary(gate.runsRoot, filepath.Dir(target), "resolved state root"); err != nil {
		return "", err
	}
	if err := validateControlWritablePath(runDir, filepath.Dir(target)); err != nil {
		return "", err
	}
	if err := validateControlPathWithinBoundary(gate.runsRoot, target, "resolved state root"); err != nil {
		return "", err
	}
	if err := validateControlWritablePath(runDir, target); err != nil {
		return "", err
	}
	if err := validateControlIsolationDirectories(false, isolation, root, target); err != nil {
		return "", err
	}
	if err := os.MkdirAll(target, 0o700); err != nil {
		return "", err
	}
	// Re-resolve after mkdir so a replacement between validation and mutation
	// cannot leave a symlinked directory for the test subprocess to use.
	runDir, err = gate.ready()
	if err != nil {
		return "", err
	}
	if err := validateControlPathWithinBoundary(gate.runsRoot, target, "resolved state root"); err != nil {
		return "", err
	}
	if err := validateControlWritablePath(runDir, target); err != nil {
		return "", err
	}
	if err := validateControlIsolationDirectories(true, target); err != nil {
		return "", err
	}
	return target, nil
}

func validateControlResumeTestIsolationDirectories(gate *controlResumePathGate, outputPath, state, temp string) error {
	runDir, err := gate.ready()
	if err != nil {
		return err
	}
	outputPath = rebuildControlResumeArtifactPath(gate.runDir, runDir, outputPath)
	base := strings.TrimSuffix(filepath.Base(outputPath), filepath.Ext(outputPath))
	isolation := filepath.Join(filepath.Dir(outputPath), "test-isolation")
	root := filepath.Join(isolation, sanitizeArtifactName(base))
	if state != filepath.Join(root, "state") || temp != filepath.Join(root, "tmp") {
		return fmt.Errorf("control test isolation paths changed")
	}
	for _, path := range []string{isolation, root, state, temp} {
		if err := validateControlPathWithinBoundary(gate.runsRoot, path, "resolved state root"); err != nil {
			return err
		}
		if err := validateControlWritablePath(runDir, path); err != nil {
			return err
		}
	}
	return validateControlIsolationDirectories(true, isolation, root, state, temp)
}

func validateControlIsolationDirectories(required bool, paths ...string) error {
	for _, path := range paths {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			if required {
				return fmt.Errorf("control test isolation directory disappeared: %s", path)
			}
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("control test isolation directory is a symlink: %s", path)
		}
		if !info.IsDir() {
			return fmt.Errorf("control test isolation path is not a directory: %s", path)
		}
	}
	return nil
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
	if !hasConfiguredTests(opts) || opts.NoTest {
		return nil, nil
	}
	started := time.Now().UTC()
	var rebindErr error
	if runDir, rebindErr = rebindControlResumeFromContext(ctx, runDir, nil, "baseline-test.txt", hostActivityArtifact); rebindErr != nil {
		return nil, &ExitError{Code: ExitPreflightFailed, Err: rebindErr}
	}
	path := filepath.Join(runDir, "baseline-test.txt")
	activity := HostActivity{
		Actor:      "tagteam-host",
		Phase:      "baseline-test",
		Status:     "running",
		Command:    testCommandDescription(opts),
		OutputPath: path,
		StartedAt:  started,
	}
	if err := writeHostActivity(runDir, activity); err != nil {
		return nil, err
	}
	var finishGateErr error
	finish := func(status string, changed []string, cause error) {
		current, err := rebindControlResumeFromContext(ctx, runDir, nil, hostActivityArtifact)
		if err != nil {
			finishGateErr = &ExitError{Code: ExitPreflightFailed, Err: err}
			return
		}
		runDir = current
		activity.Status = status
		activity.Elapsed = shortDuration(time.Since(started))
		activity.ChangedFiles = append([]string(nil), changed...)
		activity.FinishedAt = time.Now().UTC()
		activity.OutputPath = filepath.Join(runDir, "baseline-test.txt")
		if cause != nil {
			activity.Error = cause.Error()
		}
		if writeErr := writeHostActivity(runDir, activity); writeErr != nil && finishGateErr == nil {
			finishGateErr = writeErr
		}
	}
	before, err := captureWorktreeSnapshot(ctx, opts.Workdir)
	if err != nil {
		wrapped := fmt.Errorf("capture worktree before baseline test: %w", err)
		finish("failed", nil, wrapped)
		if finishGateErr != nil {
			return nil, finishGateErr
		}
		return nil, wrapped
	}
	// Rebind before launching the test command so output cannot target a replaced run dir.
	if runDir, rebindErr = rebindControlResumeFromContext(ctx, runDir, nil, "baseline-test.txt"); rebindErr != nil {
		return nil, &ExitError{Code: ExitPreflightFailed, Err: rebindErr}
	}
	path = filepath.Join(runDir, "baseline-test.txt")
	activity.OutputPath = path
	test, err := runConfiguredTestCommands(ctx, opts, path)
	if err != nil {
		finish("failed", nil, err)
		if finishGateErr != nil {
			return nil, finishGateErr
		}
		return nil, err
	}
	after, err := captureWorktreeSnapshot(ctx, opts.Workdir)
	if err != nil {
		wrapped := fmt.Errorf("capture worktree after baseline test: %w", err)
		finish("failed", nil, wrapped)
		if finishGateErr != nil {
			return nil, finishGateErr
		}
		return nil, wrapped
	}
	if changed := worktreeDelta(before, after); len(changed) > 0 {
		paths := make([]string, 0, len(changed))
		for _, changedPath := range changed {
			paths = append(paths, "baseline-test:"+changedPath)
		}
		violation := &IntegrityViolationError{Paths: paths}
		finish("integrity_violation", changed, violation)
		if finishGateErr != nil {
			return nil, finishGateErr
		}
		return nil, violation
	}
	if startErr := baselineTestStartError(test); startErr != nil {
		finish("failed", nil, startErr)
		if finishGateErr != nil {
			return nil, finishGateErr
		}
		return nil, startErr
	}
	status := "failed"
	if test.Passed {
		status = "passed"
	}
	finish(status, nil, nil)
	if finishGateErr != nil {
		return nil, finishGateErr
	}
	return &test, nil
}

// baselineTestStartError distinguishes a genuine failing test suite from a
// shell that could not launch the configured test command. POSIX shells use
// 127 for that condition, which cannot be compared meaningfully after agents
// have changed the worktree.
func baselineTestStartError(test TestRun) error {
	if len(test.Commands) == 0 {
		if test.ExitCode != 127 {
			return nil
		}
		return &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("baseline test command could not start (shell exit status 127): %s", test.Command)}
	}
	for _, command := range test.Commands {
		if command.ExitCode == 127 {
			return &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("baseline test command could not start (shell exit status 127): %s", command.Command)}
		}
	}
	return nil
}
