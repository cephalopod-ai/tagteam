package tagteam

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type GateFinding struct {
	ID       string `json:"id"`
	Gate     string `json:"gate"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Path     string `json:"path,omitempty"`
}

type QualityGateResult struct {
	SchemaVersion int           `json:"schema_version"`
	Round         int           `json:"round"`
	AllowedScope  []string      `json:"allowed_scope"`
	Findings      []GateFinding `json:"findings"`
	Blocking      bool          `json:"blocking"`
	CheckedAt     time.Time     `json:"checked_at"`
}

// replaceQualityGateForRound keeps final.json aligned with the one persisted
// quality-gate artifact for each round. Resume can re-evaluate an interrupted
// round after the operator supplies a corrected explicit scope.
func replaceQualityGateForRound(results []QualityGateResult, next QualityGateResult) []QualityGateResult {
	updated := make([]QualityGateResult, 0, len(results))
	replaced := false
	for _, result := range results {
		if result.Round != next.Round {
			updated = append(updated, result)
			continue
		}
		if !replaced {
			updated = append(updated, next)
			replaced = true
		}
	}
	if !replaced {
		updated = append(updated, next)
	}
	return updated
}

func evaluateQualityGates(ctx context.Context, opts RunOptions, baseline string, round int, diff DiffArtifact, allowedScope []string) QualityGateResult {
	result := QualityGateResult{
		SchemaVersion: ArtifactSchemaVersion,
		Round:         round,
		AllowedScope:  append([]string(nil), allowedScope...),
		Findings:      []GateFinding{},
		CheckedAt:     time.Now().UTC(),
	}
	result.Findings = append(result.Findings, evaluateScopeFindings(diff.Metadata.Files, allowedScope)...)
	result.Findings = append(result.Findings, evaluateChurnFindings(ctx, opts.Workdir, baseline, diff.Metadata.Files, opts.Churn)...)
	for _, finding := range result.Findings {
		if finding.Severity == "blocker" || finding.Severity == "major" {
			result.Blocking = true
		}
	}
	return result
}

func evaluateScopeFindings(files []DiffFile, allowedScope []string) []GateFinding {
	allowed := normalizeAllowedScope(allowedScope)
	findings := []GateFinding{}
	for _, file := range files {
		path := filepath.ToSlash(filepath.Clean(file.Path))
		if hostDeniedPath(path) {
			findings = append(findings, GateFinding{ID: gateFindingID("scope", path), Gate: "scope", Severity: "blocker", Message: "host-owned or generated path changed", Path: path})
			continue
		}
		if !pathAllowed(path, allowed) {
			findings = append(findings, GateFinding{ID: gateFindingID("scope", path), Gate: "scope", Severity: "major", Message: "changed path is outside the explicit allowlist", Path: path})
		}
	}
	return findings
}

func normalizeAllowedScope(raw []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, item := range raw {
		item = filepath.ToSlash(strings.TrimSpace(item))
		if item == "" || filepath.IsAbs(item) || strings.Contains(item, "..") || strings.ContainsAny(item, `*?[]{}`) {
			continue
		}
		directoryPrefix := strings.HasSuffix(item, "/")
		if item != "." {
			item = strings.TrimPrefix(filepath.ToSlash(filepath.Clean(item)), "./")
			if directoryPrefix {
				item += "/"
			}
		}
		if !seen[item] {
			seen[item] = true
			out = append(out, item)
		}
	}
	sort.Strings(out)
	return out
}

func validateExplicitAllowedScope(raw []string) error {
	if len(raw) == 0 {
		return fmt.Errorf("at least one --allow-path is required; use an exact repo-relative file or a directory prefix ending in / (examples: --allow-path README.md --allow-path docs/)")
	}
	seen := make(map[string]string, len(raw))
	for _, item := range raw {
		normalized, err := normalizeExplicitAllowedPath(item)
		if err != nil {
			return err
		}
		if previous, exists := seen[normalized]; exists {
			return fmt.Errorf("invalid --allow-path %q: duplicates %q after normalization to %q; remove one entry", item, previous, normalized)
		}
		seen[normalized] = item
	}
	return nil
}

// ValidateAllowedPaths validates operator-provided write scope in input order,
// producing stable errors that identify the first invalid value.
func ValidateAllowedPaths(raw []string) error {
	return validateExplicitAllowedScope(raw)
}

func normalizeExplicitAllowedPath(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("invalid --allow-path %q: value is empty", raw)
	}
	if strings.Contains(trimmed, `\`) {
		return "", fmt.Errorf("invalid --allow-path %q: use / as the path separator", raw)
	}
	if filepath.IsAbs(trimmed) || strings.HasPrefix(trimmed, "/") {
		suggestion := strings.TrimLeft(trimmed, "/")
		if suggestion == "" {
			suggestion = "."
		}
		return "", fmt.Errorf("invalid --allow-path %q: path must be repo-relative, not absolute; use %q for an exact path or %q for a directory", raw, strings.TrimSuffix(suggestion, "/"), strings.TrimSuffix(suggestion, "/")+"/")
	}
	if strings.ContainsAny(trimmed, `*?[]{}`) {
		return "", fmt.Errorf("invalid --allow-path %q: glob syntax is not supported; use an exact file or a directory prefix ending in /", raw)
	}
	for _, segment := range strings.Split(filepath.ToSlash(trimmed), "/") {
		if segment == ".." {
			return "", fmt.Errorf("invalid --allow-path %q: parent traversal (..) is forbidden", raw)
		}
	}
	directoryPrefix := strings.HasSuffix(trimmed, "/")
	normalized := filepath.ToSlash(filepath.Clean(trimmed))
	if normalized != "." {
		normalized = strings.TrimPrefix(normalized, "./")
		if directoryPrefix {
			normalized += "/"
		}
	}
	return normalized, nil
}

func pathAllowed(path string, allowed []string) bool {
	for _, item := range allowed {
		if item == "." || path == strings.TrimSuffix(item, "/") {
			return true
		}
		if strings.HasSuffix(item, "/") && strings.HasPrefix(path, item) {
			return true
		}
	}
	return false
}

func hostDeniedPath(path string) bool {
	return path == ".tagteam" || strings.HasPrefix(path, ".tagteam/") ||
		path == ".dory" || strings.HasPrefix(path, ".dory/") ||
		path == "live-progress.json"
}

func evaluateChurnFindings(ctx context.Context, workdir, baseline string, files []DiffFile, thresholds ChurnThresholds) []GateFinding {
	if thresholds.MaxFiles <= 0 {
		thresholds = DefaultConfig().Defaults.Churn
	}
	findings := []GateFinding{}
	changedLines := 0
	fixtureFiles := 0
	for _, file := range files {
		changedLines += file.Additions + file.Deletions
		lower := strings.ToLower(filepath.ToSlash(file.Path))
		if strings.Contains(lower, "/fixtures/") || strings.Contains(lower, "/testdata/") || strings.HasPrefix(lower, "fixtures/") || strings.HasPrefix(lower, "testdata/") {
			fixtureFiles++
		}
	}
	if len(files) > thresholds.MaxFiles {
		findings = append(findings, GateFinding{ID: "CHURN-FILES", Gate: "churn", Severity: "major", Message: fmt.Sprintf("diff changes %d files; threshold is %d", len(files), thresholds.MaxFiles)})
	}
	if changedLines > thresholds.MaxChangedLines {
		findings = append(findings, GateFinding{ID: "CHURN-LINES", Gate: "churn", Severity: "major", Message: fmt.Sprintf("diff changes %d lines; threshold is %d", changedLines, thresholds.MaxChangedLines)})
	}
	if fixtureFiles > thresholds.MaxFixtureFiles {
		findings = append(findings, GateFinding{ID: "CHURN-FIXTURES", Gate: "churn", Severity: "major", Message: fmt.Sprintf("diff changes %d fixture files; threshold is %d", fixtureFiles, thresholds.MaxFixtureFiles)})
	}
	trackedChangedLines, err := gitChangedLines(ctx, workdir, baseline, false)
	if err != nil {
		return append(findings, GateFinding{ID: "CHURN-MEASURE", Gate: "churn", Severity: "major", Message: "unable to measure tracked changed lines"})
	}
	if trackedChangedLines >= 100 {
		ignoreSpace, err := gitChangedLines(ctx, workdir, baseline, true)
		if err != nil {
			return append(findings, GateFinding{ID: "CHURN-MEASURE", Gate: "churn", Severity: "major", Message: "unable to measure whitespace-insensitive changed lines"})
		}
		semanticRatio := float64(ignoreSpace) / float64(trackedChangedLines)
		whitespaceRatio := 1 - semanticRatio
		if whitespaceRatio >= thresholds.WhitespaceRatio {
			findings = append(findings, GateFinding{ID: "CHURN-WHITESPACE", Gate: "churn", Severity: "major", Message: fmt.Sprintf("estimated whitespace-only churn is %.0f%%", whitespaceRatio*100)})
		}
		if semanticRatio < thresholds.MinimumSemanticRatio {
			findings = append(findings, GateFinding{ID: "CHURN-DENSITY", Gate: "churn", Severity: "major", Message: fmt.Sprintf("whitespace-insensitive semantic density is %.0f%%", semanticRatio*100)})
		}
	}
	return findings
}

func gitChangedLines(ctx context.Context, workdir, baseline string, ignoreWhitespace bool) (int, error) {
	args := []string{"diff"}
	if ignoreWhitespace {
		args = append(args, "-w")
	}
	args = append(args, "--numstat", baseline, "--", ".", ":(exclude).tagteam")
	out, err := runGitCommandBytes(ctx, workdir, []string{"LC_ALL=C"}, args...)
	if err != nil {
		return 0, err
	}
	additions, deletions := 0, 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		var add, del int
		if _, err := fmt.Sscanf(line, "%d\t%d", &add, &del); err == nil {
			additions += add
			deletions += del
		}
	}
	return additions + deletions, nil
}

func gateFindingID(gate, path string) string {
	hash := sha256Sum([]byte(gate + "\x00" + path))
	return strings.ToUpper(gate) + "-" + hash[:10]
}

func allowedScopeForRound(opts RunOptions, selected *WorkPackage) []string {
	if selected != nil && len(selected.AllowedScope) > 0 {
		return selected.AllowedScope
	}
	if len(opts.AllowedPaths) > 0 {
		return opts.AllowedPaths
	}
	// Reviewed legacy flows without a work package remain compatible, but the
	// transfer gate treats this broad scope as unbounded and requires review.
	return []string{"."}
}
