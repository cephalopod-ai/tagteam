package tagteam

import (
	"fmt"
	"path/filepath"
	"strings"
)

func validateReviewForDiff(review *Review, maxFindings int, diffPath string) error {
	normalizeReview(review)
	applyReviewCaps(review, maxFindings)
	return validateReviewFindingGrounding(review, diffPath)
}

func validateReviewerResult(result Result, maxFindings int, diffPath string) error {
	if err := validateReviewForDiff(result.Review, maxFindings, diffPath); err != nil {
		return &OutputContractError{Err: err}
	}
	return nil
}

// validateReviewFindingGrounding prevents schema-valid placeholder reviews from
// steering a worker toward files that were not part of the captured change.
func validateReviewFindingGrounding(review *Review, diffPath string) error {
	if review == nil {
		return fmt.Errorf("review is missing")
	}
	if len(review.Findings) == 0 {
		return nil
	}
	changedFiles := readChangedFilesFromDiffPath(diffPath)
	// Review-only and resumed legacy runs can have no changed paths. They may
	// legitimately report an existing issue, so enforce grounding only when
	// this round captured at least one changed file.
	if len(changedFiles) == 0 {
		return nil
	}
	changed := make(map[string]struct{}, len(changedFiles))
	for _, path := range changedFiles {
		changed[filepath.ToSlash(filepath.Clean(path))] = struct{}{}
	}
	for index, finding := range review.Findings {
		path, err := normalizeReviewFindingPath(finding.File)
		if err != nil {
			return fmt.Errorf("review finding %d has invalid file %q: %w", index+1, finding.File, err)
		}
		if _, ok := changed[path]; !ok {
			return fmt.Errorf("review finding %d references %q, which is not a changed file in the captured diff", index+1, finding.File)
		}
	}
	return nil
}

func normalizeReviewFindingPath(raw string) (string, error) {
	path := strings.TrimSpace(raw)
	if path == "" || filepath.IsAbs(path) || strings.HasPrefix(path, "/") {
		return "", fmt.Errorf("path must be a repo-relative file")
	}
	for _, segment := range strings.Split(filepath.ToSlash(path), "/") {
		if segment == ".." {
			return "", fmt.Errorf("parent traversal is forbidden")
		}
	}
	path = strings.TrimPrefix(filepath.ToSlash(filepath.Clean(path)), "./")
	if path == "." || strings.HasSuffix(path, "/") {
		return "", fmt.Errorf("path must name a file")
	}
	return path, nil
}
