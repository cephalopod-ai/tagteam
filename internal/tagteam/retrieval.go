package tagteam

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

const (
	maxRetrievalEvidenceItems = 20
	maxRetrievalQueries       = 12
	maxRetrievalReasonLength  = 240
	maxRetrievalPromptBytes   = 12 * 1024
	maxRetrievalPathLength    = 500
	retrievalTimeout          = 10 * time.Second
)

var retrievalIgnoredDirs = []string{
	".git",
	".tagteam",
	"dist",
	"build",
	"coverage",
	"node_modules",
	"vendor",
	".cache",
	".dory",
}

type RetrievalArtifact struct {
	SchemaVersion int                 `json:"schema_version"`
	Enabled       bool                `json:"enabled"`
	Status        string              `json:"status"`
	Summary       string              `json:"summary"`
	Queries       []string            `json:"queries"`
	Evidence      []RetrievalEvidence `json:"evidence"`
	Truncated     bool                `json:"truncated"`
	Errors        []string            `json:"errors,omitempty"`
	GeneratedAt   time.Time           `json:"generated_at"`
}

type RetrievalEvidence struct {
	File   string `json:"file"`
	Line   int    `json:"line,omitempty"`
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
}

func runScoutRetrieval(ctx context.Context, workdir, userPrompt, runDir string, enabled bool) (RetrievalArtifact, error) {
	artifact := buildRetrievalArtifact(ctx, workdir, userPrompt, enabled)
	if runDir == "" {
		return artifact, nil
	}
	if err := writeJSONWithNewline(filepath.Join(runDir, "retrieval-round-1.json"), artifact); err != nil {
		return artifact, err
	}
	return artifact, nil
}

func buildRetrievalArtifact(ctx context.Context, workdir, userPrompt string, enabled bool) RetrievalArtifact {
	artifact := RetrievalArtifact{
		SchemaVersion: ArtifactSchemaVersion,
		Enabled:       enabled,
		Status:        "disabled",
		Summary:       "retrieval disabled",
		Queries:       []string{},
		Evidence:      []RetrievalEvidence{},
		GeneratedAt:   time.Now().UTC(),
	}
	if !enabled {
		return artifact
	}
	artifact.Status = "unavailable"
	artifact.Summary = "rg is not available; continuing without host retrieval"
	if _, err := execLookPath("rg"); err != nil {
		return artifact
	}

	root := retrievalRoot(ctx, workdir)
	queries := extractRetrievalQueries(userPrompt)
	artifact.Queries = queries
	if len(queries) == 0 {
		artifact.Status = "empty"
		artifact.Summary = "no useful retrieval queries extracted"
		return artifact
	}

	retrievalCtx, cancel := context.WithTimeout(ctx, retrievalTimeout)
	defer cancel()
	evidence, errs, timedOut, truncated := runRetrievalQueries(retrievalCtx, root, queries)
	artifact.Errors = errs
	artifact.Truncated = truncated
	if timedOut {
		artifact.Status = "timeout"
		artifact.Summary = "retrieval timed out; continuing with scout-only reconnaissance"
		artifact.Evidence = capRetrievalEvidence(evidence, maxRetrievalEvidenceItems, &artifact.Truncated)
		return artifact
	}
	evidence = append(evidence, relatedTestEvidence(root, evidence)...)
	artifact.Evidence = capRetrievalEvidence(evidence, maxRetrievalEvidenceItems, &artifact.Truncated)
	if len(artifact.Evidence) == 0 {
		artifact.Status = "empty"
		artifact.Summary = "retrieval found no useful local evidence"
		if len(errs) > 0 {
			artifact.Status = "degraded"
			artifact.Summary = "retrieval degraded and found no useful local evidence"
		}
		return artifact
	}
	if len(errs) > 0 {
		artifact.Status = "degraded"
		artifact.Summary = fmt.Sprintf("retrieval found %d evidence item(s) with degraded diagnostics", len(artifact.Evidence))
		return artifact
	}
	artifact.Status = "ok"
	artifact.Summary = fmt.Sprintf("retrieval found %d evidence item(s)", len(artifact.Evidence))
	return artifact
}

func retrievalRoot(ctx context.Context, workdir string) string {
	if out, err := runCommand(ctx, workdir, "git", "rev-parse", "--show-toplevel"); err == nil {
		root := strings.TrimSpace(out)
		if root != "" {
			if abs, absErr := filepath.Abs(root); absErr == nil {
				return abs
			}
			return root
		}
	}
	if abs, err := filepath.Abs(workdir); err == nil {
		return abs
	}
	return workdir
}

func extractRetrievalQueries(prompt string) []string {
	stop := map[string]bool{
		"and": true, "are": true, "but": true, "for": true, "from": true, "has": true, "have": true,
		"into": true, "not": true, "that": true, "the": true, "this": true, "use": true, "with": true,
		"you": true, "your": true, "make": true, "add": true, "fix": true, "implement": true, "change": true,
		"should": true, "would": true, "could": true, "when": true, "where": true, "what": true,
	}
	seen := map[string]bool{}
	add := func(raw string) {
		q := strings.ToLower(strings.TrimSpace(raw))
		q = strings.Trim(q, "`'\".,;:()[]{}<>")
		if len(q) < 3 || stop[q] {
			return
		}
		if seen[q] {
			return
		}
		seen[q] = true
	}

	quoted := regexp.MustCompile(`"([^"]+)"|'([^']+)'|` + "`" + `([^` + "`" + `]+)` + "`")
	for _, match := range quoted.FindAllStringSubmatch(prompt, -1) {
		for _, group := range match[1:] {
			if strings.TrimSpace(group) != "" {
				add(group)
			}
		}
	}

	var token strings.Builder
	flush := func() {
		if token.Len() > 0 {
			add(token.String())
			token.Reset()
		}
	}
	for _, r := range prompt {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '_', r == '-', r == '.', r == '/':
			token.WriteRune(r)
		default:
			flush()
		}
	}
	flush()

	queries := make([]string, 0, len(seen))
	for q := range seen {
		queries = append(queries, q)
	}
	sort.Strings(queries)
	if len(queries) > maxRetrievalQueries {
		queries = queries[:maxRetrievalQueries]
	}
	return queries
}

func runRetrievalQueries(ctx context.Context, root string, queries []string) ([]RetrievalEvidence, []string, bool, bool) {
	seen := map[string]bool{}
	var evidence []RetrievalEvidence
	var errs []string
	truncated := false
	for _, query := range queries {
		if ctx.Err() != nil {
			return evidence, errs, true, truncated
		}
		args := []string{
			"--line-number",
			"--no-heading",
			"--color", "never",
			"--hidden",
			"--fixed-strings",
			"--ignore-case",
			"--max-count", "3",
			"--max-filesize", "1M",
		}
		for _, dir := range retrievalIgnoredDirs {
			args = append(args, "--glob", "!"+dir+"/**")
		}
		args = append(args, query, ".")
		cmd := exec.CommandContext(ctx, "rg", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "LC_ALL=C")
		out, err := cmd.CombinedOutput()
		if ctx.Err() != nil {
			return evidence, errs, true, truncated
		}
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
				continue
			}
			if len(out) == 0 {
				errs = append(errs, sanitizeRetrievalDiagnostic(err.Error()))
				continue
			}
			errs = append(errs, sanitizeRetrievalDiagnostic(string(out)))
		}
		for _, item := range parseRgEvidence(root, query, out) {
			key := fmt.Sprintf("%s:%d:%s", item.File, item.Line, item.Kind)
			if seen[key] {
				continue
			}
			seen[key] = true
			evidence = append(evidence, item)
			if len(evidence) > maxRetrievalEvidenceItems*3 {
				truncated = true
				return evidence, errs, false, truncated
			}
		}
	}
	sortRetrievalEvidence(evidence)
	return evidence, errs, false, truncated
}

func parseRgEvidence(root, query string, out []byte) []RetrievalEvidence {
	lines := bytes.Split(out, []byte{'\n'})
	items := make([]RetrievalEvidence, 0, len(lines))
	for _, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		parts := bytes.SplitN(line, []byte{':'}, 3)
		if len(parts) < 2 {
			continue
		}
		file := string(parts[0])
		lineNo, _ := strconvAtoiBytes(parts[1])
		rel, ok := safeRelPath(root, filepath.Join(root, file))
		if !ok || ignoredRetrievalPath(rel) {
			continue
		}
		items = append(items, RetrievalEvidence{
			File:   trimPathForEvidence(rel),
			Line:   lineNo,
			Kind:   "content",
			Reason: trimReason(fmt.Sprintf("matched query %q", query)),
		})
	}
	return items
}

func relatedTestEvidence(root string, evidence []RetrievalEvidence) []RetrievalEvidence {
	seen := map[string]bool{}
	var related []RetrievalEvidence
	for _, item := range evidence {
		if item.File == "" || item.Kind == "test" {
			continue
		}
		dir := filepath.Dir(item.File)
		base := filepath.Base(item.File)
		ext := filepath.Ext(base)
		stem := strings.TrimSuffix(base, ext)
		patterns := []string{
			filepath.Join(root, dir, stem+"_test.go"),
			filepath.Join(root, dir, stem+".test.*"),
			filepath.Join(root, dir, stem+".spec.*"),
			filepath.Join(root, dir, "*"+stem+"*test*"),
			filepath.Join(root, dir, "*"+stem+"*spec*"),
		}
		for _, pattern := range patterns {
			matches, _ := filepath.Glob(pattern)
			sort.Strings(matches)
			for _, match := range matches {
				rel, ok := safeRelPath(root, match)
				if !ok || ignoredRetrievalPath(rel) || rel == item.File || seen[rel] {
					continue
				}
				seen[rel] = true
				related = append(related, RetrievalEvidence{
					File:   trimPathForEvidence(rel),
					Kind:   "test",
					Reason: trimReason("nearby test-like filename for " + item.File),
				})
				if len(related) >= maxRetrievalEvidenceItems {
					return related
				}
			}
		}
	}
	sortRetrievalEvidence(related)
	return related
}

func safeRelPath(root, path string) (string, bool) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func ignoredRetrievalPath(rel string) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for _, part := range parts {
		for _, ignored := range retrievalIgnoredDirs {
			if part == ignored {
				return true
			}
		}
	}
	return false
}

func capRetrievalEvidence(items []RetrievalEvidence, max int, truncated *bool) []RetrievalEvidence {
	sortRetrievalEvidence(items)
	capped := make([]RetrievalEvidence, 0, minInt(len(items), max))
	for _, item := range items {
		item.File = trimPathForEvidence(item.File)
		item.Reason = trimReason(item.Reason)
		capped = append(capped, item)
		if len(capped) == max {
			break
		}
	}
	if len(items) > max && truncated != nil {
		*truncated = true
	}
	return capped
}

func sortRetrievalEvidence(items []RetrievalEvidence) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].File != items[j].File {
			return items[i].File < items[j].File
		}
		if items[i].Line != items[j].Line {
			return items[i].Line < items[j].Line
		}
		if items[i].Kind != items[j].Kind {
			return items[i].Kind < items[j].Kind
		}
		return items[i].Reason < items[j].Reason
	})
}

func CompactRetrievalForPrompt(artifact RetrievalArtifact) string {
	return compactRetrievalForPrompt(artifact, maxRetrievalPromptBytes)
}

func CompactRetrievalForPromptAggressive(artifact RetrievalArtifact) string {
	return compactRetrievalForPrompt(artifact, maxRetrievalPromptBytes/8)
}

func compactRetrievalForPrompt(artifact RetrievalArtifact, maxBytes int) string {
	if maxBytes <= 0 {
		maxBytes = maxRetrievalPromptBytes
	}
	artifact.Evidence = capRetrievalEvidence(artifact.Evidence, maxRetrievalEvidenceItems, &artifact.Truncated)
	if len(artifact.Queries) > maxRetrievalQueries {
		artifact.Queries = append([]string{}, artifact.Queries[:maxRetrievalQueries]...)
		artifact.Truncated = true
	}
	data, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		return ""
	}
	if len(data) <= maxBytes {
		return string(data)
	}
	compact := artifact
	for len(compact.Evidence) > 0 {
		compact.Truncated = true
		compact.Evidence = compact.Evidence[:len(compact.Evidence)-1]
		data, err = json.MarshalIndent(compact, "", "  ")
		if err == nil && len(data) <= maxBytes {
			return string(data)
		}
	}
	compact.Errors = nil
	data, err = json.MarshalIndent(compact, "", "  ")
	if err != nil {
		return ""
	}
	if len(data) > maxBytes {
		return string(data[:maxBytes])
	}
	return string(data)
}

func CompactScoutForPrompt(scout Scout) string {
	normalizeScout(&scout)
	if len(scout.Evidence) > maxRetrievalEvidenceItems {
		scout.Evidence = append([]ScoutEvidence{}, scout.Evidence[:maxRetrievalEvidenceItems]...)
		scout.RetrievalTruncated = true
	}
	for i := range scout.Evidence {
		scout.Evidence[i].File = trimPathForEvidence(scout.Evidence[i].File)
		scout.Evidence[i].Reason = trimReason(scout.Evidence[i].Reason)
	}
	if len(scout.RetrievalQueries) > maxRetrievalQueries {
		scout.RetrievalQueries = append([]string{}, scout.RetrievalQueries[:maxRetrievalQueries]...)
		scout.RetrievalTruncated = true
	}
	data, err := json.MarshalIndent(scout, "", "  ")
	if err != nil {
		return "{}"
	}
	if len(data) <= maxRetrievalPromptBytes {
		return string(data)
	}
	for len(scout.Evidence) > 0 {
		scout.RetrievalTruncated = true
		scout.Evidence = scout.Evidence[:len(scout.Evidence)-1]
		data, err = json.MarshalIndent(scout, "", "  ")
		if err == nil && len(data) <= maxRetrievalPromptBytes {
			return string(data)
		}
	}
	return "{}"
}

func retrievalScoutEvidence(items []RetrievalEvidence) []ScoutEvidence {
	out := make([]ScoutEvidence, 0, len(items))
	for _, item := range items {
		out = append(out, ScoutEvidence{
			File:   item.File,
			Line:   item.Line,
			Kind:   item.Kind,
			Reason: item.Reason,
		})
	}
	return out
}

func trimReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if len(reason) <= maxRetrievalReasonLength {
		return reason
	}
	return reason[:maxRetrievalReasonLength]
}

func trimPathForEvidence(path string) string {
	path = filepath.ToSlash(strings.TrimSpace(path))
	if len(path) <= maxRetrievalPathLength {
		return path
	}
	return path[:maxRetrievalPathLength]
}

func sanitizeRetrievalDiagnostic(raw string) string {
	raw = strings.TrimSpace(redactSecrets(raw))
	raw = strings.ReplaceAll(raw, "\n", " ")
	if len(raw) > maxRetrievalReasonLength {
		return raw[:maxRetrievalReasonLength]
	}
	return raw
}

func strconvAtoiBytes(raw []byte) (int, error) {
	n := 0
	if len(raw) == 0 {
		return 0, fmt.Errorf("empty integer")
	}
	for _, b := range raw {
		if b < '0' || b > '9' {
			return 0, fmt.Errorf("invalid integer")
		}
		n = n*10 + int(b-'0')
	}
	return n, nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
