package tagteam

import (
	"os"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	// Isolate the test process from ambient sensitive-keyed environment values.
	// The secret redactor replaces the *values* of env vars whose key looks
	// sensitive (API_KEY, AUTH_TOKEN, ACCESS_TOKEN, SECRET, ...) everywhere in
	// output. A host or CI value that is short or numeric — e.g. a
	// *_OAUTH_TOKEN_FILE_DESCRIPTOR holding a small file-descriptor number —
	// would otherwise be replaced inside unrelated timestamps and byte streams,
	// flakily corrupting tests. Redaction tests set their own sensitive vars via
	// t.Setenv, so clearing ambient ones here does not weaken that coverage and
	// makes the suite deterministic across developer and CI environments.
	for _, entry := range os.Environ() {
		if key, _, ok := strings.Cut(entry, "="); ok && isSensitiveEnvKey(key) {
			_ = os.Unsetenv(key)
		}
	}
	root, err := os.MkdirTemp("", "tagteam-test-state-*")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("TAGTEAM_STATE_ROOT", root)
	code := m.Run()
	_ = os.RemoveAll(root)
	os.Exit(code)
}
