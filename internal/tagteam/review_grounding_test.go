package tagteam

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidateReviewFindingGrounding(t *testing.T) {
	runDir := t.TempDir()
	diffPath := filepath.Join(runDir, "diff-round-1.patch")
	if err := writeJSONWithNewline(strings.TrimSuffix(diffPath, ".patch")+".files.json", DiffFilesMetadata{
		SchemaVersion: ArtifactSchemaVersion,
		Files:         []DiffFile{{Path: "README.md"}, {Path: "docs/guide.md"}},
	}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		file    string
		wantErr string
	}{
		{name: "changed file", file: "docs/guide.md"},
		{name: "unchanged placeholder", file: "a.md", wantErr: "not a changed file"},
		{name: "traversal", file: "../secret.md", wantErr: "parent traversal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			review := &Review{Findings: []Finding{{File: tt.file}}}
			err := validateReviewFindingGrounding(review, diffPath)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("validateReviewFindingGrounding() error = %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("validateReviewFindingGrounding() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateReviewFindingGroundingAllowsReviewOnlyDiff(t *testing.T) {
	if err := validateReviewFindingGrounding(&Review{Findings: []Finding{{File: "existing.go"}}}, filepath.Join(t.TempDir(), "diff-round-1.patch")); err != nil {
		t.Fatalf("validateReviewFindingGrounding() error = %v", err)
	}
}

func TestRunAdversaryRetriesUngroundedReviewFinding(t *testing.T) {
	runDir := t.TempDir()
	diffPath := filepath.Join(runDir, "diff-round-1.patch")
	if err := writeJSONWithNewline(strings.TrimSuffix(diffPath, ".patch")+".files.json", DiffFilesMetadata{
		SchemaVersion: ArtifactSchemaVersion,
		Files:         []DiffFile{{Path: "README.md"}},
	}); err != nil {
		t.Fatal(err)
	}

	calls := 0
	adapter := fakeDirectAdapter{
		build: func(role Role, req Request) (*CommandSpec, error) {
			return &CommandSpec{Argv: []string{"fake"}, Dir: runDir, Output: req.OutputPath}, nil
		},
		direct: func(role Role, req Request) (Result, error) {
			calls++
			file := "a.md"
			if calls == 2 {
				if !strings.Contains(req.Prompt, "not a changed file") {
					t.Fatalf("retry prompt omitted grounding error: %s", req.Prompt)
				}
				file = "README.md"
			}
			return Result{Review: groundedReviewForTest(file)}, nil
		},
	}
	testRegistryOverrides = map[string]Adapter{"grounding-fake": adapter}
	t.Cleanup(func() { testRegistryOverrides = nil })

	final := FinalRun{Adapters: map[string]string{"supervisor": "grounding-fake"}, Models: map[string]string{"supervisor": "test"}}
	initFinalState(&final, RunOptions{})
	review, _, _, err := NewApp(DefaultConfig()).runAdversary(context.Background(), RunOptions{
		Prompt:         "review the change",
		Mode:           ModeSupervisor,
		Adversary:      RoleTarget{Adapter: "grounding-fake", Model: "test"},
		Timeout:        time.Second,
		MaxOutputBytes: 1 << 20,
	}, 1, runDir, "", "review the change", "HEAD", "diff --git a/README.md b/README.md\n", diffPath, "", "", nil, RelayContext{}, "", &final)
	if err != nil {
		t.Fatalf("runAdversary() error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("reviewer calls = %d, want 2", calls)
	}
	if review == nil || len(review.Findings) != 1 || review.Findings[0].File != "README.md" {
		t.Fatalf("review = %#v", review)
	}
}

func groundedReviewForTest(file string) *Review {
	return &Review{
		SchemaVersion: ReviewSchemaVersion,
		Verdict:       "needs_changes",
		Summary:       "The changed file needs a correction.",
		Findings: []Finding{{
			ID:       "finding-grounding-test",
			Severity: "major",
			File:     file,
			Line:     1,
			Issue:    "The implementation has a concrete defect.",
			Fix:      "Correct the implementation and add coverage.",
		}},
		TestSuggestions: []string{},
		DataLossChecks: &DataLossChecks{
			MalformedInputPreservation: DataLossCheck{Status: "not_applicable", Evidence: "No malformed input behavior changed."},
			AnnotationHistoryRetention: DataLossCheck{Status: "not_applicable", Evidence: "No annotation history behavior changed."},
			AmbiguousIdentityHandling:  DataLossCheck{Status: "not_applicable", Evidence: "No identity behavior changed."},
			ReadOnlyNonMutation:        DataLossCheck{Status: "pass", Evidence: "The reviewer did not modify the worktree."},
		},
	}
}
