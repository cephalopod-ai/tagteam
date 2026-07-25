package tagteam

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const WorkerResultSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "additionalProperties": false,
  "required": ["schema_version", "status", "summary", "files_changed", "checks_run", "remaining_risks"],
  "properties": {
    "schema_version": {"type": "integer", "const": 1},
    "status": {"type": "string", "const": "completed"},
    "summary": {"type": "string", "minLength": 1},
    "files_changed": {"type": "array", "items": {"type": "string"}},
    "checks_run": {"type": "array", "items": {"type": "string"}},
    "remaining_risks": {"type": "array", "items": {"type": "string"}}
  }
}`

type WorkerResult struct {
	SchemaVersion  int      `json:"schema_version"`
	Status         string   `json:"status"`
	Summary        string   `json:"summary"`
	FilesChanged   []string `json:"files_changed"`
	ChecksRun      []string `json:"checks_run"`
	RemainingRisks []string `json:"remaining_risks"`
}

func (w WorkerResult) Validate() error {
	if w.SchemaVersion != ArtifactSchemaVersion {
		return fmt.Errorf("unsupported worker schema_version %d", w.SchemaVersion)
	}
	if w.Status != "completed" {
		return fmt.Errorf("invalid worker status %q", w.Status)
	}
	if strings.TrimSpace(w.Summary) == "" {
		return fmt.Errorf("worker summary is empty")
	}
	if w.FilesChanged == nil || w.ChecksRun == nil || w.RemainingRisks == nil {
		return fmt.Errorf("worker arrays files_changed, checks_run, and remaining_risks are required")
	}
	seen := map[string]bool{}
	for _, raw := range w.FilesChanged {
		path, err := normalizeWorkerPath(raw)
		if err != nil {
			return err
		}
		if seen[path] {
			return fmt.Errorf("duplicate worker files_changed path %q", path)
		}
		seen[path] = true
	}
	return nil
}

func parseWorkerResult(raw []byte) (*WorkerResult, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, &OutputContractError{Err: fmt.Errorf("worker output is empty")}
	}
	var result WorkerResult
	err := decodeEmbeddedJSON([]byte(trimmed), func(candidate []byte) error {
		var parsed WorkerResult
		if err := json.Unmarshal(candidate, &parsed); err != nil {
			return fmt.Errorf("decode worker result JSON: %w", err)
		}
		if err := parsed.Validate(); err != nil {
			return err
		}
		result = parsed
		return nil
	})
	if err != nil {
		return nil, &OutputContractError{Err: err}
	}
	for i, path := range result.FilesChanged {
		result.FilesChanged[i], _ = normalizeWorkerPath(path)
	}
	sort.Strings(result.FilesChanged)
	return &result, nil
}

func normalizeWorkerPath(raw string) (string, error) {
	raw = filepath.ToSlash(strings.TrimSpace(raw))
	if raw == "" || strings.HasPrefix(raw, "/") || strings.Contains(raw, "\x00") {
		return "", fmt.Errorf("invalid worker path %q", raw)
	}
	clean := filepath.ToSlash(filepath.Clean(raw))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean == ".tagteam" || strings.HasPrefix(clean, ".tagteam/") {
		return "", fmt.Errorf("worker path escapes or targets host state: %q", raw)
	}
	return clean, nil
}

func workerContractPrompt(prompt string) string {
	return strings.TrimSpace(prompt) + `

Your final response MUST be JSON only and match this envelope exactly:
{
  "schema_version": 1,
  "status": "completed",
  "summary": "concise description of the implemented work",
  "files_changed": ["repo-relative paths changed during this invocation"],
  "checks_run": ["commands you ran; agent claims only"],
  "remaining_risks": ["honest unresolved risks"]
}
Use empty arrays when appropriate. Do not return identity text, Markdown fences, or prose outside the JSON object.
The files_changed array MUST exactly match Git-visible content changes made
during this invocation. Do not list a path solely because you staged or
unstaged an unchanged pre-existing edit. Do not list gitignored local artifacts
in files_changed, even when repository instructions require writing them;
report those local-only writes in remaining_risks instead. If an ignored
artifact must be reviewed and transferred, explicitly stage it with git add -f
before the final response so Git and files_changed agree.
Validation commands must be non-interactive and must not modify files outside the task's allowed scope. Do not run setup or configuration commands such as pnpm approve-builds as validation.`
}

type worktreeSnapshot map[string]string

func captureWorktreeSnapshot(ctx context.Context, workdir string) (worktreeSnapshot, error) {
	out, err := runGitCommandBytes(ctx, workdir, []string{"LC_ALL=C"}, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	tokens := splitNULTokens(out)
	snapshot := worktreeSnapshot{}
	for i := 0; i < len(tokens); i++ {
		token := tokens[i]
		if len(token) < 4 {
			continue
		}
		status := token[:2]
		path := token[3:]
		if (strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C")) && i+1 < len(tokens) {
			// In porcelain -z mode the first path is the destination and the
			// following token is the source path. Track the destination while
			// consuming the source token.
			i++
		}
		path = filepath.ToSlash(path)
		if path == ".tagteam" || strings.HasPrefix(path, ".tagteam/") {
			continue
		}
		absolute := filepath.Join(workdir, filepath.FromSlash(path))
		fingerprint, include, snapshotErr := snapshotWorktreeEntry(absolute, status)
		if snapshotErr != nil {
			return nil, snapshotErr
		}
		if include {
			snapshot[path] = fingerprint
		}
	}
	return snapshot, nil
}

// snapshotWorktreeEntry hashes only entries Git can version. In particular,
// pytest's current-result symlink can target a directory, which must be
// fingerprinted as a symlink rather than followed with os.ReadFile.
func snapshotWorktreeEntry(path, status string) (fingerprint string, include bool, err error) {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "deleted:" + status, true, nil
		}
		return "", false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return "", false, err
		}
		sum := sha256.Sum256([]byte(target))
		return "symlink:" + status + ":" + hex.EncodeToString(sum[:]), true, nil
	}
	if info.IsDir() || !info.Mode().IsRegular() {
		return "", false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "deleted:" + status, true, nil
		}
		return "", false, err
	}
	sum := sha256.Sum256(data)
	return status + ":" + hex.EncodeToString(sum[:]), true, nil
}

func worktreeDelta(before, after worktreeSnapshot) []string {
	seen := map[string]bool{}
	for path, fingerprint := range before {
		if after[path] != fingerprint {
			seen[path] = true
		}
	}
	for path, fingerprint := range after {
		if before[path] != fingerprint {
			seen[path] = true
		}
	}
	paths := make([]string, 0, len(seen))
	for path := range seen {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func worktreeContentDelta(before, after worktreeSnapshot) []string {
	seen := map[string]bool{}
	for path, fingerprint := range before {
		if worktreeContentFingerprint(after[path]) != worktreeContentFingerprint(fingerprint) {
			seen[path] = true
		}
	}
	for path, fingerprint := range after {
		if worktreeContentFingerprint(before[path]) != worktreeContentFingerprint(fingerprint) {
			seen[path] = true
		}
	}
	paths := make([]string, 0, len(seen))
	for path := range seen {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

// worktreeContentFingerprint removes Git index/worktree status metadata while
// retaining the entry's observable content. A worker contract describes
// content written during its invocation, not whether it staged an edit that
// already existed before it started.
func worktreeContentFingerprint(value string) string {
	if value == "" || strings.HasPrefix(value, "deleted:") {
		return "deleted"
	}
	if strings.HasPrefix(value, "symlink:") {
		parts := strings.SplitN(value, ":", 3)
		if len(parts) == 3 {
			return "symlink:" + parts[2]
		}
		return value
	}
	if index := strings.IndexByte(value, ':'); index >= 0 {
		return value[index+1:]
	}
	return value
}

func validateWorkerGitClaim(ctx context.Context, workdir string, result *WorkerResult, before, after worktreeSnapshot) error {
	// Match the same content-level change set shown in the review artifact.
	// Index-only transitions on pre-existing edits must not cause a false
	// quarantine in later repair rounds.
	actual := worktreeContentDelta(before, after)
	claimed := append([]string(nil), result.FilesChanged...)
	sort.Strings(claimed)
	if strings.Join(actual, "\x00") != strings.Join(claimed, "\x00") {
		if ignored := ignoredMissingWorkerClaims(ctx, workdir, claimed, actual); len(ignored) > 0 {
			return &OutputContractError{Err: fmt.Errorf("worker files_changed includes ignored paths that Tagteam cannot safely include in review artifacts: %v; track them or explicitly stage them with git add -f before the run (claimed=%v actual=%v)", ignored, claimed, actual)}
		}
		if normalized, cumulative := normalizeCumulativeWorkerClaims(claimed, actual, before, after); cumulative {
			result.FilesChanged = normalized
			result.RemainingRisks = append(result.RemainingRisks, "Tagteam normalized files_changed by removing cumulative paths unchanged during this invocation")
			return nil
		}
		return &OutputContractError{Err: fmt.Errorf("worker files_changed inconsistent with Git: claimed=%v actual=%v", claimed, actual)}
	}
	return nil
}

func normalizeCumulativeWorkerClaims(claimed, actual []string, before, after worktreeSnapshot) ([]string, bool) {
	actualSet := make(map[string]bool, len(actual))
	for _, path := range actual {
		actualSet[path] = true
	}
	normalized := make([]string, 0, len(claimed))
	removed := false
	for _, path := range claimed {
		if actualSet[path] {
			normalized = append(normalized, path)
			continue
		}
		beforeFingerprint, existedBefore := before[path]
		afterFingerprint, existsAfter := after[path]
		if !existedBefore || !existsAfter || worktreeContentFingerprint(beforeFingerprint) != worktreeContentFingerprint(afterFingerprint) {
			return nil, false
		}
		removed = true
	}
	sort.Strings(normalized)
	return normalized, removed && strings.Join(normalized, "\x00") == strings.Join(actual, "\x00")
}

func ignoredMissingWorkerClaims(ctx context.Context, workdir string, claimed, actual []string) []string {
	actualSet := make(map[string]bool, len(actual))
	for _, path := range actual {
		actualSet[path] = true
	}
	ignored := []string{}
	for _, path := range claimed {
		if actualSet[path] {
			continue
		}
		if _, err := runGitCommandBytes(ctx, workdir, []string{"LC_ALL=C"}, "check-ignore", "-q", "--", path); err == nil {
			ignored = append(ignored, path)
		}
	}
	return ignored
}

func validateWorkerResultForRequest(ctx context.Context, req Request, result *Result, before worktreeSnapshot) error {
	if !req.RequireWorkerContract {
		return nil
	}
	worker, err := parseWorkerResult([]byte(result.Text))
	if err != nil {
		return err
	}
	after, err := captureWorktreeSnapshot(ctx, req.Workdir)
	if err != nil {
		return err
	}
	if err := validateWorkerGitClaim(ctx, req.Workdir, worker, before, after); err != nil {
		return err
	}
	result.Worker = worker
	result.Text = worker.Summary
	return nil
}
