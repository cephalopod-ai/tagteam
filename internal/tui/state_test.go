package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cephalopod-ai/tagteam/internal/tagteam"
)

func TestBuildRunOptionsUsesComposeModeTargets(t *testing.T) {
	workdir := t.TempDir()
	m, err := newModel(RunOptions{Workdir: workdir})
	if err != nil {
		t.Fatalf("newModel() error = %v", err)
	}
	m.compose.Mode = tagteam.ModeRelay
	m.compose.Prompt = "ship it"
	m.compose.EditorTarget = "codex:gpt-5.5"
	m.compose.ReviewerTarget = "claude:sonnet"
	m.compose.ScoutTarget = "agy:gemini-3.5-flash-low"
	m.compose.ScoutMode = "recon"
	m.compose.PostScoutMode = "polish"
	m.compose.Rounds = 3

	opts, _, err := m.buildRunOptions()
	if err != nil {
		t.Fatalf("buildRunOptions() error = %v", err)
	}
	if opts.Mode != tagteam.ModeRelay {
		t.Fatalf("opts.Mode = %q, want relay", opts.Mode)
	}
	if got := roleTargetString(opts.Coder); got != "codex:gpt-5.5" {
		t.Fatalf("coder = %q", got)
	}
	if got := roleTargetString(opts.Adversary); got != "claude:sonnet" {
		t.Fatalf("reviewer = %q", got)
	}
	if got := roleTargetString(opts.Scout); got != "agy:gemini-3.5-flash-low" {
		t.Fatalf("scout = %q", got)
	}
	if opts.Rounds != 3 {
		t.Fatalf("rounds = %d, want 3", opts.Rounds)
	}
}

func TestApplyCommandUpdatesCoreComposeFields(t *testing.T) {
	workdir := t.TempDir()
	m, err := newModel(RunOptions{Workdir: workdir})
	if err != nil {
		t.Fatalf("newModel() error = %v", err)
	}
	m.applyCommand(nil, "/mode solo")
	m.applyCommand(nil, "/worker codex:gpt-5.5")
	m.applyCommand(nil, "/rounds 4")
	m.applyCommand(nil, "/prompt add OAuth login")

	if m.compose.Mode != tagteam.ModeSolo {
		t.Fatalf("mode = %q, want solo", m.compose.Mode)
	}
	if m.compose.EditorTarget != "codex:gpt-5.5" {
		t.Fatalf("editor = %q", m.compose.EditorTarget)
	}
	if m.compose.Rounds != 4 {
		t.Fatalf("rounds = %d, want 4", m.compose.Rounds)
	}
	if m.compose.Prompt != "add OAuth login" {
		t.Fatalf("prompt = %q", m.compose.Prompt)
	}
}

func TestApplyProfileReconfiguresComposeState(t *testing.T) {
	workdir := t.TempDir()
	m, err := newModel(RunOptions{Workdir: workdir})
	if err != nil {
		t.Fatalf("newModel() error = %v", err)
	}

	if err := m.applyProfile("relay"); err != nil {
		t.Fatalf("applyProfile() error = %v", err)
	}

	if m.compose.Profile != "relay" {
		t.Fatalf("profile = %q, want relay", m.compose.Profile)
	}
	if m.compose.Mode != tagteam.ModeRelay {
		t.Fatalf("mode = %q, want relay", m.compose.Mode)
	}
	if strings.TrimSpace(m.compose.ScoutTarget) == "" {
		t.Fatal("expected relay profile to populate scout target")
	}
	if m.compose.ScoutMode == "" {
		t.Fatal("expected relay profile to populate scout mode")
	}
}

func TestSetModeRelaySuppliesMissingScoutTarget(t *testing.T) {
	m, err := newModel(RunOptions{Workdir: t.TempDir()})
	if err != nil {
		t.Fatalf("newModel() error = %v", err)
	}
	if m.compose.ScoutTarget != "" {
		t.Fatalf("initial scout target = %q, want empty outside relay mode", m.compose.ScoutTarget)
	}

	m.applyCommand(nil, "/mode relay")
	if m.compose.Mode != tagteam.ModeRelay {
		t.Fatalf("mode = %q, want relay", m.compose.Mode)
	}
	if m.compose.ScoutTarget == "" {
		t.Fatal("switching to relay must populate a valid scout target")
	}
}

func TestTargetChoicesIncludeRelaySpecificCoder(t *testing.T) {
	cfg := tagteam.DefaultConfig()
	cfg.Defaults.RelayCoder = "claude:relay-only-model"
	want := cfg.Defaults.RelayCoder
	for _, target := range collectTargetChoices(cfg) {
		if target == want {
			return
		}
	}
	t.Fatalf("target choices do not include relay coder %q", want)
}

func TestSettingsModeCycleSuppliesMissingScoutTarget(t *testing.T) {
	m, err := newModel(RunOptions{Workdir: t.TempDir()})
	if err != nil {
		t.Fatalf("newModel() error = %v", err)
	}
	m.adjustField(fieldMode, 1)
	if m.compose.Mode != tagteam.ModeRelay || m.compose.ScoutTarget == "" {
		t.Fatalf("settings mode cycle = mode %q scout %q, want relay with default scout", m.compose.Mode, m.compose.ScoutTarget)
	}
}

func TestBuildRunOptionsHonorsTUIOverridesOverInitialFlags(t *testing.T) {
	m, err := newModel(RunOptions{
		Workdir: t.TempDir(),
		Flags: tagteam.FlagInputs{
			Mode:                 "relay",
			StrictScout:          true,
			NoScoutRetrieval:     true,
			RepairJSONWithWorker: true,
			AllowDirty:           true,
		},
		Changed: map[string]bool{
			"mode":                    true,
			"strict-scout":            true,
			"no-scout-retrieval":      true,
			"repair-json-with-worker": true,
			"allow-dirty":             true,
		},
	})
	if err != nil {
		t.Fatalf("newModel() error = %v", err)
	}

	m.compose.StrictScout = false
	m.compose.StrictScoutSet = true
	m.compose.ScoutRetrieval = true
	m.compose.ScoutRetrievalSet = true
	m.compose.RepairJSONWorker = false
	m.compose.RepairJSONSet = true
	m.compose.AllowDirty = false
	m.compose.AllowDirtySet = true

	opts, _, err := m.buildRunOptions()
	if err != nil {
		t.Fatalf("buildRunOptions() error = %v", err)
	}
	if opts.ScoutFailurePolicy != "continue" || opts.LossPolicy.Scout != tagteam.LossPolicyDegrade {
		t.Fatalf("strict scout off resolved to policy=%q loss=%q", opts.ScoutFailurePolicy, opts.LossPolicy.Scout)
	}
	if !opts.ScoutRetrieval {
		t.Fatal("scout retrieval on was overridden by the initial --no-scout-retrieval flag")
	}
	if opts.JSONRepair != "off" {
		t.Fatalf("JSON repair = %q, want off", opts.JSONRepair)
	}
	if opts.AllowDirty || opts.GitSafety == "allow-dirty" {
		t.Fatalf("allow-dirty off resolved to allow_dirty=%v git_safety=%q", opts.AllowDirty, opts.GitSafety)
	}
}

func TestBuildRunOptionsClearsStaleNoSliceFlag(t *testing.T) {
	m, err := newModel(RunOptions{
		Workdir: t.TempDir(),
		Flags:   tagteam.FlagInputs{NoSlice: true},
		Changed: map[string]bool{"no-slice": true},
	})
	if err != nil {
		t.Fatalf("newModel() error = %v", err)
	}
	m.compose.Slice = true

	opts, _, err := m.buildRunOptions()
	if err != nil {
		t.Fatalf("buildRunOptions() error = %v", err)
	}
	if !opts.SupervisorSlicing {
		t.Fatal("slice on was overridden by the initial --no-slice flag")
	}
}

func TestCommandPaletteSelectionCompletesCommand(t *testing.T) {
	m, err := newModel(RunOptions{Workdir: t.TempDir()})
	if err != nil {
		t.Fatalf("newModel() error = %v", err)
	}
	m.commandMode = true
	m.handleCommandKey(nil, keyEvent{Kind: keyDown})
	m.handleCommandKey(nil, keyEvent{Kind: keyTab})
	if m.commandBuffer != "refresh" {
		t.Fatalf("command completion = %q, want refresh", m.commandBuffer)
	}
}

func TestRoleSpecificCommandsRejectWrongMode(t *testing.T) {
	m, err := newModel(RunOptions{Workdir: t.TempDir()})
	if err != nil {
		t.Fatalf("newModel() error = %v", err)
	}
	m.applyCommand(nil, "/coder codex:gpt-5.5")
	if !strings.Contains(m.statusMessage, "only available") {
		t.Fatalf("coder in supervisor mode = %q, want clear mode error", m.statusMessage)
	}

	m.applyCommand(nil, "/mode relay")
	m.applyCommand(nil, "/reviewer claude:opus")
	if !strings.Contains(m.statusMessage, "only available") {
		t.Fatalf("reviewer in relay mode = %q, want clear mode error", m.statusMessage)
	}
}

func TestDisplayedSlashCommandsAreRecognized(t *testing.T) {
	workdir := t.TempDir()
	runDir := filepath.Join(workdir, ".tagteam", "runs", "run-123")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "final.json"), []byte(`{"schema_version":1,"run_id":"run-123","run_dir":"`+runDir+`","mode":"solo","status":"passed","verdict":"done","exit_code":0}`), 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := newModel(RunOptions{Workdir: workdir})
	if err != nil {
		t.Fatalf("newModel() error = %v", err)
	}
	m.runInFlight = true

	samples := []string{
		"/run",
		"/refresh",
		"/runs",
		"/watch latest",
		"/watch active",
		"/watch run-123",
		"/settings",
		"/profiles",
		"/profile off",
		"/mode relay",
		"/model codex:gpt-5.5",
		"/worker codex:gpt-5.5",
		"/coder codex:gpt-5.5",
		"/supervisor claude:opus",
		"/reviewer claude:opus",
		"/scout agy:gemini-3.5-flash-low",
		"/scout-mode recon",
		"/post-scout-mode polish",
		"/strict-scout on",
		"/scout-retrieval off",
		"/scout-context-policy block",
		"/rounds 3",
		"/test go test ./...",
		"/no-test on",
		"/slice on",
		"/allow-dirty on",
		"/repair-json on",
		"/prompt ship it",
		"/toggle plan",
		"/toggle findings",
		"/toggle artifacts",
		"/toggle test",
	}

	for _, sample := range samples {
		m.applyCommand(nil, sample)
		if strings.Contains(m.statusMessage, "unknown command") {
			t.Fatalf("slash command %q was not recognized", sample)
		}
	}
}
