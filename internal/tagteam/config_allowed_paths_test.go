package tagteam

import (
	"strings"
	"testing"
	"time"
)

func TestResolveOptionsRejectsInvalidAllowedPaths(t *testing.T) {
	_, err := ResolveOptions(DefaultConfig(), nil, FlagInputs{
		AllowedPaths: []string{"../outside"},
		Timeout:      15 * time.Minute,
	}, map[string]bool{"allow-path": true}, "ship it")
	if err == nil || !strings.Contains(err.Error(), `invalid --allow-path "../outside": parent traversal (..) is forbidden`) {
		t.Fatalf("ResolveOptions() error = %v, want invalid allow-path error", err)
	}
}

func TestValidateAllowedPathsReturnsDeterministicActionableErrors(t *testing.T) {
	tests := []struct {
		name  string
		paths []string
		want  string
	}{
		{name: "missing", paths: nil, want: "at least one --allow-path is required"},
		{name: "absolute", paths: []string{"/internal"}, want: `invalid --allow-path "/internal": path must be repo-relative, not absolute; use "internal" for an exact path or "internal/" for a directory`},
		{name: "parent", paths: []string{"docs/../secrets"}, want: `invalid --allow-path "docs/../secrets": parent traversal (..) is forbidden`},
		{name: "glob", paths: []string{"docs/*.md"}, want: `invalid --allow-path "docs/*.md": glob syntax is not supported`},
		{name: "duplicate", paths: []string{"./docs/", "docs/"}, want: `invalid --allow-path "docs/": duplicates "./docs/" after normalization to "docs/"`},
		{name: "first error wins", paths: []string{"/first", "../second"}, want: `invalid --allow-path "/first": path must be repo-relative`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateAllowedPaths(test.paths)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateAllowedPaths(%q) error = %v, want %q", test.paths, err, test.want)
			}
		})
	}
}

func TestValidateAllowedPathsAcceptsDocumentedForms(t *testing.T) {
	if err := ValidateAllowedPaths([]string{"README.md", "docs/", "./internal/", "."}); err != nil {
		t.Fatalf("documented allow paths rejected: %v", err)
	}
}
