package tagteam

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func mergeCommandEnv(overlay map[string]string, extra []string) []string {
	env := os.Environ()
	if len(overlay) > 0 {
		existing := map[string]bool{}
		for _, item := range env {
			key, _, _ := strings.Cut(item, "=")
			existing[key] = true
		}
		keys := make([]string, 0, len(overlay))
		for key := range overlay {
			if existing[key] {
				continue
			}
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			env = append(env, key+"="+overlay[key])
		}
	}
	if len(extra) > 0 {
		env = append(env, extra...)
	}
	return env
}

func mergeCommandEnvForRole(role Role, overlay map[string]string, extra []string) []string {
	if roleAllowsParentEnv(role) {
		return mergeCommandEnv(overlay, extra)
	}
	return mergeRestrictedCommandEnv(overlay, extra)
}

// commandEnvForInvocation prevents provider commands and their validation
// tools from placing incidental temporary artifacts in the repository
// worktree. Intentional project edits use their explicit repository paths and
// do not require the caller's ambient temporary directory.
func commandEnvForInvocation(role Role, req Request, extra []string) ([]string, error) {
	env := mergeCommandEnvForRole(role, req.EnvOverlay, extra)
	if strings.TrimSpace(req.RunDir) == "" {
		return env, nil
	}
	invocationID := strings.TrimSpace(req.InvocationID)
	if invocationID == "" {
		invocationID = string(role)
	}
	tempDir := filepath.Join(req.RunDir, "tmp", "invocations", invocationID)
	if err := os.MkdirAll(tempDir, 0o700); err != nil {
		return nil, fmt.Errorf("create isolated temporary directory for %s: %w", role, err)
	}
	return overrideCommandEnv(env, map[string]string{
		"TMPDIR": tempDir,
		"TMP":    tempDir,
		"TEMP":   tempDir,
	}), nil
}

func overrideCommandEnv(env []string, overrides map[string]string) []string {
	result := make([]string, 0, len(env)+len(overrides))
	seen := make(map[string]bool, len(overrides))
	for _, item := range env {
		key, _, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		if value, replace := overrides[key]; replace {
			if !seen[key] {
				result = append(result, key+"="+value)
				seen[key] = true
			}
			continue
		}
		result = append(result, item)
	}
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		if !seen[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		result = append(result, key+"="+overrides[key])
	}
	return result
}

func roleAllowsParentEnv(role Role) bool {
	switch role {
	case RoleCoder:
		return true
	default:
		return false
	}
}

func mergeRestrictedCommandEnv(overlay map[string]string, extra []string) []string {
	allowed := map[string]bool{
		"HOME":          true,
		"LANG":          true,
		"LC_ALL":        true,
		"LOGNAME":       true,
		"PATH":          true,
		"SHELL":         true,
		"SSH_AUTH_SOCK": true,
		"TEMP":          true,
		"TERM":          true,
		"TERM_PROGRAM":  true,
		"TMP":           true,
		"TMPDIR":        true,
		"USER":          true,
	}
	values := map[string]string{}
	for _, item := range os.Environ() {
		key, value, ok := strings.Cut(item, "=")
		if !ok || (!allowed[key] && !isForwardableAuthEnvKey(key)) {
			continue
		}
		values[key] = value
	}
	for key, value := range overlay {
		if _, exists := values[key]; exists {
			continue
		}
		values[key] = value
	}
	for _, item := range extra {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+values[key])
	}
	return env
}

// isForwardableAuthEnvKey reports whether an ambient env var is a provider auth
// credential that non-coder adapter subprocesses need to authenticate. It is
// deliberately narrower than isSensitiveEnvKey (the redaction set): we redact
// broadly but forward narrowly, so only well-known auth key shapes cross the
// restricted-env boundary. Forwarded keys are still scrubbed from artifacts by
// secretValuesFromEnv, which also matches API_KEY/AUTH_TOKEN.
func isForwardableAuthEnvKey(key string) bool {
	upper := strings.ToUpper(strings.TrimSpace(key))
	switch upper {
	case "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY", "ANTHROPIC_AUTH_TOKEN":
		return true
	}
	return strings.HasSuffix(upper, "_API_KEY") || strings.HasSuffix(upper, "_AUTH_TOKEN")
}

func createRunDir(workdir, stateRoot, runID string) (string, error) {
	locator, err := resolveStateLocator(workdir, stateRoot)
	if err != nil {
		return "", err
	}
	if err := locator.Prepare(); err != nil {
		return "", err
	}
	root, err := locator.RunDir(runID)
	if err != nil {
		return "", err
	}
	if err := os.Mkdir(root, 0o755); err != nil {
		return "", err
	}
	return root, nil
}

func writeJSON(path string, value any) error {
	return writeJSONDurable(path, value, false, true)
}

func readLatest(workdir string) (LatestRun, error) {
	var latest LatestRun
	data, err := os.ReadFile(statePathForWorkdir(workdir, "latest.json"))
	if err != nil {
		return LatestRun{}, err
	}
	if err := json.Unmarshal(data, &latest); err != nil {
		return LatestRun{}, err
	}
	return latest, nil
}

func readFinal(path string) (FinalRun, error) {
	var final FinalRun
	data, err := os.ReadFile(path)
	if err != nil {
		return FinalRun{}, err
	}
	if err := json.Unmarshal(data, &final); err != nil {
		return FinalRun{}, err
	}
	return final, nil
}

func readExecutionPlan(runDir string) (ExecutionPlan, error) {
	var plan ExecutionPlan
	data, err := os.ReadFile(filepath.Join(runDir, "plan.json"))
	if err != nil {
		return ExecutionPlan{}, err
	}
	if err := json.Unmarshal(data, &plan); err != nil {
		return ExecutionPlan{}, err
	}
	return plan, nil
}

func readMeta(path string) (Meta, error) {
	var meta Meta
	data, err := os.ReadFile(path)
	if err != nil {
		return Meta{}, err
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return Meta{}, err
	}
	return meta, nil
}

func readLatestPrompt(workdir string) (string, error) {
	latest, err := readLatest(workdir)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(latest.RunDir, "input.md"))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (a DiffArtifact) ChangedFiles() []string {
	files := make([]string, 0, len(a.Metadata.Files))
	for _, file := range a.Metadata.Files {
		files = append(files, file.Path)
	}
	return files
}

func captureDiffArtifact(ctx context.Context, workdir, baseline, runDir string, round int) (DiffArtifact, error) {
	if current, err := rebindControlResumeFromContext(ctx, runDir, nil); err != nil {
		return DiffArtifact{}, &ExitError{Code: ExitPreflightFailed, Err: err}
	} else {
		runDir = current
	}
	prefix := filepath.Join(runDir, fmt.Sprintf("diff-round-%d", round))
	indexPath := filepath.Join(runDir, fmt.Sprintf("tmp-diff-round-%d.index", round))
	defer os.Remove(indexPath)
	defer os.Remove(indexPath + ".lock")

	patch, numstat, statusZ, numstatZ, err := deterministicDiffOutputs(ctx, workdir, baseline, indexPath)
	if err != nil {
		return DiffArtifact{}, err
	}
	if current, rebindErr := rebindControlResumeFromContext(ctx, runDir, nil); rebindErr != nil {
		return DiffArtifact{}, &ExitError{Code: ExitPreflightFailed, Err: rebindErr}
	} else {
		runDir = current
		prefix = filepath.Join(runDir, fmt.Sprintf("diff-round-%d", round))
	}
	patchPath := prefix + ".patch"
	if err := writeFileDurable(patchPath, patch, 0o644, true); err != nil {
		return DiffArtifact{}, err
	}
	sum := sha256.Sum256(patch)
	diffHash := hex.EncodeToString(sum[:])
	shaPath := prefix + ".sha256"
	if err := writeFileDurable(shaPath, []byte(diffHash+"\n"), 0o644, true); err != nil {
		return DiffArtifact{}, err
	}
	numstatPath := prefix + ".numstat"
	if err := writeFileDurable(numstatPath, normalizeTextFileNewline(numstat), 0o644, true); err != nil {
		return DiffArtifact{}, err
	}
	files := buildDiffFiles(statusZ, numstatZ)
	metadata := DiffFilesMetadata{
		SchemaVersion: ArtifactSchemaVersion,
		Baseline:      baseline,
		Head:          currentWorkingTreeHead(ctx, workdir),
		GeneratedAt:   time.Now().UTC(),
		DiffSHA256:    diffHash,
		Files:         files,
	}
	filesPath := prefix + ".files.json"
	if err := writeJSONWithNewline(filesPath, metadata); err != nil {
		return DiffArtifact{}, err
	}
	return DiffArtifact{
		PatchPath:   patchPath,
		NumstatPath: numstatPath,
		FilesPath:   filesPath,
		SHA256Path:  shaPath,
		Patch:       string(patch),
		Metadata:    metadata,
	}, nil
}

func buildDiffFiles(statusZ, numstatZ []byte) []DiffFile {
	stats := parseNumstatZ(numstatZ)
	files := parseNameStatusZ(statusZ)
	for i := range files {
		if stat, ok := stats[files[i].Path]; ok {
			files[i].Additions = stat.Additions
			files[i].Deletions = stat.Deletions
			files[i].Binary = stat.Binary
		}
	}
	sort.SliceStable(files, func(i, j int) bool {
		if files[i].Path == files[j].Path {
			return files[i].OldPath < files[j].OldPath
		}
		return files[i].Path < files[j].Path
	})
	return files
}

func parseNameStatusZ(raw []byte) []DiffFile {
	tokens := splitNULTokens(raw)
	files := make([]DiffFile, 0, len(tokens)/2)
	for i := 0; i < len(tokens); {
		code := tokens[i]
		i++
		if code == "" {
			continue
		}
		status := diffStatusName(code)
		file := DiffFile{Status: status}
		if strings.HasPrefix(code, "R") || strings.HasPrefix(code, "C") {
			if i+1 >= len(tokens) {
				break
			}
			file.OldPath = tokens[i]
			file.Path = tokens[i+1]
			i += 2
		} else {
			if i >= len(tokens) {
				break
			}
			file.Path = tokens[i]
			i++
		}
		files = append(files, file)
	}
	return files
}

func parseNumstatZ(raw []byte) map[string]DiffFile {
	tokens := splitNULTokens(raw)
	stats := map[string]DiffFile{}
	for i := 0; i < len(tokens); i++ {
		parts := strings.Split(tokens[i], "\t")
		if len(parts) < 3 {
			continue
		}
		stat := DiffFile{}
		stat.Additions, stat.Binary = parseNumstatCount(parts[0])
		var delBinary bool
		stat.Deletions, delBinary = parseNumstatCount(parts[1])
		stat.Binary = stat.Binary || delBinary
		path := parts[2]
		if path == "" {
			if i+2 >= len(tokens) {
				break
			}
			stat.OldPath = tokens[i+1]
			path = tokens[i+2]
			i += 2
		}
		stat.Path = path
		stats[path] = stat
	}
	return stats
}

func splitNULTokens(raw []byte) []string {
	parts := strings.Split(string(raw), "\x00")
	tokens := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			tokens = append(tokens, part)
		}
	}
	return tokens
}

func parseNumstatCount(raw string) (int, bool) {
	if raw == "-" {
		return 0, true
	}
	var n int
	for _, ch := range raw {
		if ch < '0' || ch > '9' {
			return 0, false
		}
		n = n*10 + int(ch-'0')
	}
	return n, false
}

func diffStatusName(code string) string {
	switch code[0] {
	case 'A':
		return "added"
	case 'C':
		return "copied"
	case 'D':
		return "deleted"
	case 'M':
		return "modified"
	case 'R':
		return "renamed"
	case 'T':
		return "typechanged"
	case 'U':
		return "unmerged"
	case 'X':
		return "unknown"
	default:
		return strings.ToLower(code)
	}
}

func currentWorkingTreeHead(ctx context.Context, workdir string) string {
	out, err := runGitCommandBytes(ctx, workdir, []string{"LC_ALL=C"}, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return "working-tree"
	}
	head := strings.TrimSpace(string(out))
	if head == "" {
		return "working-tree"
	}
	return head + "-working-tree"
}

func normalizeTextFileNewline(data []byte) []byte {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return []byte(text)
}

func writeJSONWithNewline(path string, value any) error {
	return writeJSONDurable(path, value, true, true)
}

func gitDirty(workdir string) (bool, error) {
	out, err := runCommand(context.Background(), workdir, "git", "status", "--porcelain")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.Contains(line, ".tagteam/") || strings.HasSuffix(line, ".tagteam") {
			continue
		}
		return true, nil
	}
	return false, nil
}

func gitCreateBranch(workdir, branch string) error {
	if _, err := runCommand(context.Background(), workdir, "git", "switch", "-c", branch); err == nil {
		return nil
	}
	if _, err := runCommand(context.Background(), workdir, "git", "checkout", "-b", branch); err != nil {
		return &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	return nil
}

func gitCreateCheckpointBranch(workdir, branch, runID string) (string, error) {
	if err := gitCreateBranch(workdir, branch); err != nil {
		return "", err
	}
	dirty, err := gitDirty(workdir)
	if err != nil {
		return "", &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	if dirty {
		if _, err := runCommand(context.Background(), workdir, "git", "add", "-A"); err != nil {
			return "", &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("checkpoint dirty worktree: %w", err)}
		}
		message := "tagteam: checkpoint pre-existing worktree"
		if strings.TrimSpace(runID) != "" {
			message += " for " + runID
		}
		if _, err := runCommand(context.Background(), workdir, "git", "commit", "-m", message); err != nil {
			return "", &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("commit dirty-worktree checkpoint: %w", err)}
		}
		if _, err := runCommand(context.Background(), workdir, "git", "diff", "--check", "HEAD^", "HEAD"); err != nil {
			return "", &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("validate dirty-worktree checkpoint: %w", err)}
		}
	}
	head, err := ensureGitRepo(workdir)
	if err != nil {
		return "", err
	}
	return head, nil
}

func runCommand(ctx context.Context, workdir, binary string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = workdir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", binary, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func runGitCommandBytes(ctx context.Context, workdir string, extraEnv []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = workdir
	cmd.Env = append(os.Environ(), extraEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func safeTestOutput(output string) string {
	if strings.TrimSpace(output) == "" {
		return "(no tests run)"
	}
	return output
}

func logProgress(opts RunOptions, format string, args ...any) {
	if opts.Quiet {
		return
	}
	fmt.Fprintf(os.Stderr, "tagteam: "+format+"\n", args...)
}

func logRequestProgress(req Request, format string, args ...any) {
	if req.Quiet {
		return
	}
	fmt.Fprintf(os.Stderr, "tagteam: "+format+"\n", args...)
}

func shortDuration(d time.Duration) string {
	return d.Truncate(time.Second).String()
}

func prepareReviewInput(adversary Adapter, diff, diffPath string) reviewInput {
	diffBytes := []byte(diff)
	if adversary.Capabilities().SupportsStdin && len(diffBytes) <= maxReviewInputBytes {
		return reviewInput{
			Stdin:    diffBytes,
			ViaStdin: true,
			Mode:     "stdin",
		}
	}
	if len(diffBytes) <= maxInlineReviewPromptBytes {
		return reviewInput{
			PromptRef: diff,
			Mode:      "inline",
		}
	}
	if diffPath != "" {
		return reviewInput{
			PromptRef: fmt.Sprintf("Diff is stored at %s. Read that file from the workspace.", diffPath),
			Mode:      "file-reference",
		}
	}
	return reviewInput{
		PromptRef: diff,
		Mode:      "inline",
	}
}

func osReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func ensureGitignoreEntry(workdir, entry string) error {
	gitignorePath := filepath.Join(workdir, ".gitignore")
	if !fileExists(gitignorePath) {
		return os.WriteFile(gitignorePath, []byte(entry+"\n"), 0o644)
	}
	data, err := os.ReadFile(gitignorePath)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) == entry {
			return nil
		}
	}
	contents := strings.TrimRight(string(data), "\n")
	if contents == "" {
		contents = entry
	} else {
		contents += "\n" + entry
	}
	contents += "\n"
	return os.WriteFile(gitignorePath, []byte(contents), 0o644)
}

func newRunID() string {
	return time.Now().UTC().Format("2006-01-02T150405.000000000Z")
}

func runTestCommand(ctx context.Context, workdir, testCmd string, timeout time.Duration, outputPath string, dryRun bool, envOverlay map[string]string, maxBytes int64, identityRegex string) (TestRun, error) {
	if gate := controlResumeGateFrom(ctx); gate != nil {
		if err := guardControlResumeWritePath(gate, outputPath); err != nil {
			return TestRun{}, &ExitError{Code: ExitPreflightFailed, Err: err}
		}
	}
	if dryRun {
		return TestRun{Command: testCmd, Passed: true, Output: "dry-run"}, nil
	}
	if maxBytes <= 0 {
		maxBytes = 2 * 1024 * 1024
	}
	runCtx := ctx
	cancel := func() {}
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()
	cmd := exec.CommandContext(runCtx, "/bin/sh", "-lc", testCmd)
	prepareProcessTree(cmd)
	cmd.Dir = workdir
	stateRoot, tempDir, isolationErr := isolatedTestDirectoriesForControlResume(controlResumeGateFrom(ctx), outputPath)
	if isolationErr != nil {
		if controlResumeGateFrom(ctx) != nil {
			return TestRun{}, &ExitError{Code: ExitPreflightFailed, Err: isolationErr}
		}
		return TestRun{}, isolationErr
	}
	commandTempDir, cleanupTempAlias, aliasErr := shortTestTempAlias(tempDir)
	if aliasErr != nil {
		return TestRun{}, aliasErr
	}
	defer cleanupTempAlias()
	cmd.Env = mergeCommandEnv(envOverlay, []string{
		"TAGTEAM_STATE_ROOT=" + stateRoot,
		"XDG_STATE_HOME=" + stateRoot,
		"TMPDIR=" + commandTempDir,
		"TMP=" + commandTempDir,
		"TEMP=" + commandTempDir,
	})
	var out boundedBuffer
	out.limit = maxBytes
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	output := out.Bytes()
	if out.Exceeded() {
		err = outputLimitError("test command", maxBytes)
	}
	exitCode := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		exitCode = exitErr.ExitCode()
	}
	testRun := TestRun{
		Command:           testCmd,
		Output:            redactSecretsWithOverlay(string(output), envOverlay),
		Passed:            err == nil,
		ExitCode:          exitCode,
		FailureIdentities: extractFailureIdentitiesWithRegex(string(output), identityRegex),
		StateRoot:         stateRoot,
		TempDir:           tempDir,
	}
	if gate := controlResumeGateFrom(ctx); gate != nil {
		if err := guardControlResumeWritePath(gate, outputPath); err != nil {
			return TestRun{}, &ExitError{Code: ExitPreflightFailed, Err: err}
		}
	}
	if err := writeRedactedBytes(outputPath, output, envOverlay); err != nil {
		return TestRun{}, fmt.Errorf("persist test output: %w", err)
	}
	return testRun, nil
}
