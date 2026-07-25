package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cephalopod-ai/tagteam/internal/tagteam"
)

func TestBuildRunOptionsCarriesParallelTestCommands(t *testing.T) {
	m, err := newModel(RunOptions{
		Workdir: t.TempDir(),
		Flags: tagteam.FlagInputs{
			AllowedPaths: []string{"README.md"},
			Tests:        []string{"go test ./one", "go test ./two"},
		},
		Changed: map[string]bool{"test": true},
	})
	if err != nil {
		t.Fatalf("newModel() error = %v", err)
	}
	if strings.Join(m.compose.TestCmds, "|") != "go test ./one|go test ./two" {
		t.Fatalf("compose test commands = %#v", m.compose.TestCmds)
	}

	opts, _, err := m.buildRunOptions()
	if err != nil {
		t.Fatalf("buildRunOptions() error = %v", err)
	}
	if strings.Join(opts.TestCommands, "|") != "go test ./one|go test ./two" {
		t.Fatalf("launch dropped parallel test commands: %#v", opts.TestCommands)
	}
	if opts.TestCmd != "go test ./one" {
		t.Fatalf("test command = %q", opts.TestCmd)
	}
}

func TestSlashTestCommandReplacesParallelList(t *testing.T) {
	m, err := newModel(RunOptions{
		Workdir: t.TempDir(),
		Flags: tagteam.FlagInputs{
			AllowedPaths: []string{"README.md"},
			Tests:        []string{"go test ./one", "go test ./two"},
		},
		Changed: map[string]bool{"test": true},
	})
	if err != nil {
		t.Fatalf("newModel() error = %v", err)
	}
	m.applyCommand(nil, "/test go test ./three")

	opts, _, err := m.buildRunOptions()
	if err != nil {
		t.Fatalf("buildRunOptions() error = %v", err)
	}
	if opts.TestCmd != "go test ./three" || strings.Join(opts.TestCommands, "|") != "go test ./three" {
		t.Fatalf("TUI /test override lost: command=%q commands=%#v", opts.TestCmd, opts.TestCommands)
	}
}

func TestEditorResubmitKeepsParallelTestCommands(t *testing.T) {
	m, err := newModel(RunOptions{
		Workdir: t.TempDir(),
		Flags: tagteam.FlagInputs{
			AllowedPaths: []string{"README.md"},
			Tests:        []string{"go test ./one", "go test ./two"},
		},
		Changed: map[string]bool{"test": true},
	})
	if err != nil {
		t.Fatalf("newModel() error = %v", err)
	}
	m.startEditor(fieldTest)
	m.applyEditorValue()
	if strings.Join(m.compose.TestCmds, "|") != "go test ./one|go test ./two" {
		t.Fatalf("unchanged editor submit collapsed the list: %#v", m.compose.TestCmds)
	}

	m.startEditor(fieldTest)
	m.editor.Buffer = "go test ./solo"
	m.applyEditorValue()
	if strings.Join(m.compose.TestCmds, "|") != "go test ./solo" {
		t.Fatalf("edited test command = %#v", m.compose.TestCmds)
	}
}

func TestSettingsShowParallelTestCommands(t *testing.T) {
	m, err := newModel(RunOptions{Workdir: t.TempDir()})
	if err != nil {
		t.Fatalf("newModel() error = %v", err)
	}
	m.compose.TestCmd = "go test ./one"
	m.compose.TestCmds = []string{"go test ./one", "go test ./two"}
	if got := m.composeFieldValue(fieldTest); !strings.Contains(got, "2 parallel") || !strings.Contains(got, "go test ./two") {
		t.Fatalf("test field value = %q, want both parallel commands", got)
	}
}

func TestStatusBadgeCoversCancellationStatuses(t *testing.T) {
	if got := statusBadge(string(tagteam.RunStatusCancelled)); got != "stop" {
		t.Fatalf("cancelled badge = %q, want stop", got)
	}
	if got := statusBadge(string(tagteam.RunStatusQuarantined)); got != "quar" {
		t.Fatalf("quarantined badge = %q, want quar", got)
	}
}

func TestWatchActiveWithoutRunReportsClearError(t *testing.T) {
	workdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workdir, ".tagteam"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workdir, ".tagteam", "active.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := newModel(RunOptions{Workdir: workdir})
	if err != nil {
		t.Fatalf("newModel() error = %v", err)
	}
	if err := m.watchRun("active"); err == nil {
		t.Fatal("watch active without an active run should error")
	}
	if m.currentRunDir != "" {
		t.Fatalf("currentRunDir = %q, want empty", m.currentRunDir)
	}
}

func writeCompatRunFinal(t *testing.T, runDir, status string) {
	t.Helper()
	final := map[string]any{
		"schema_version": 1,
		"run_id":         filepath.Base(runDir),
		"run_dir":        runDir,
		"mode":           "solo",
		"status":         status,
		"verdict":        "done",
		"finished_at":    time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(final)
	if err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(runDir, ".compat-tmp.json")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(runDir, "final.json")); err != nil {
		t.Fatal(err)
	}
}

func TestLoadRunsRefreshesWhenRunDirectoryChanges(t *testing.T) {
	workdir := t.TempDir()
	runDir := filepath.Join(workdir, ".tagteam", "runs", "run-1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeCompatRunFinal(t, runDir, "passed")

	m, err := newModel(RunOptions{Workdir: workdir})
	if err != nil {
		t.Fatalf("newModel() error = %v", err)
	}
	m.refresh()
	if len(m.runs) != 1 || m.runs[0].Status != "passed" {
		t.Fatalf("initial runs = %#v", m.runs)
	}
	if len(m.runListCache) != 1 {
		t.Fatalf("run list cache = %#v, want one entry", m.runListCache)
	}

	m.refresh()
	if len(m.runs) != 1 || m.runs[0].Status != "passed" {
		t.Fatalf("cached refresh runs = %#v", m.runs)
	}

	writeCompatRunFinal(t, runDir, "failed")
	touched := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(runDir, touched, touched); err != nil {
		t.Fatal(err)
	}
	m.refresh()
	if len(m.runs) != 1 || m.runs[0].Status != "failed" {
		t.Fatalf("refresh after run change = %#v, want failed status", m.runs)
	}
}

func TestInputDecoderSwallowsUnknownEscapeSequences(t *testing.T) {
	for _, sequence := range []string{"\x1b[3~", "\x1b[15~", "\x1bOP"} {
		if events := decodeKeyEvents([]byte(sequence)); len(events) != 0 {
			t.Fatalf("sequence %q leaked events: %#v", sequence, events)
		}
	}

	decoder := inputDecoder{}
	if events := decoder.Feed([]byte("\x1b[1")); len(events) != 0 {
		t.Fatalf("partial CSI emitted events: %#v", events)
	}
	if events := decoder.Feed([]byte("5~")); len(events) != 0 || decoder.HasPending() {
		t.Fatalf("completed F5 = %#v pending=%t, want swallowed", events, decoder.HasPending())
	}

	if events := decodeKeyEvents([]byte("\x1b[1~")); len(events) != 1 || events[0].Kind != keyHome {
		t.Fatalf("ESC[1~ = %#v, want keyHome", events)
	}
	if events := decodeKeyEvents([]byte("\x1b[4~")); len(events) != 1 || events[0].Kind != keyEnd {
		t.Fatalf("ESC[4~ = %#v, want keyEnd", events)
	}
	if events := decodeKeyEvents([]byte{27}); len(events) != 1 || events[0].Kind != keyEsc {
		t.Fatalf("bare ESC = %#v, want keyEsc", events)
	}
}

func TestComposerEditorShowsPromptTail(t *testing.T) {
	m, err := newModel(RunOptions{Workdir: t.TempDir()})
	if err != nil {
		t.Fatalf("newModel() error = %v", err)
	}
	m.editor = editorState{Active: true, Field: fieldPrompt, Buffer: strings.Repeat("start ", 20) + "THE-END"}
	lines := m.composerLines(40)
	if len(lines) == 0 || !strings.Contains(lines[0], "THE-END_") {
		t.Fatalf("composer editor hides the cursor end: %q", lines)
	}
}
