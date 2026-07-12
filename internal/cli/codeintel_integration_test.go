package cli

import (
	"strings"
	"testing"
)

func TestCodeIntelCLIHelpersPreservePromptWordsAndLineDiffs(t *testing.T) {
	if got := joinArgs([]string{"trace", "the", "call", "path"}); got != "trace the call path" {
		t.Fatalf("joined prompt = %q", got)
	}
	diff := unifiedDiff([]byte("same\nremoved\n"), []byte("same\nadded\n"))
	for _, line := range []string{" same", "-removed", "+added"} {
		if !strings.Contains(diff, line) {
			t.Fatalf("diff missing %q: %s", line, diff)
		}
	}
	if got := unifiedDiff([]byte("same\n"), []byte("same\n")); got != "(no changes)" {
		t.Fatalf("unchanged diff = %q", got)
	}
	if diff := unifiedDiff([]byte("same\n"), []byte("same")); !strings.Contains(diff, "\\ No newline at end of file") {
		t.Fatalf("trailing-newline diff = %q", diff)
	}
}
