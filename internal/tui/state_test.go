package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cephalopod-ai/tagteam/internal/tagteam"
)

func TestBuildRunOptionsUsesComposeModeTargets(t *testing.T) {
	workdir := t.TempDir()
	m, err := newModel(RunOptions{
		Workdir: workdir,
		Flags:   tagteam.FlagInputs{AllowedPaths: []string{"README.md"}},
	})
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
			AllowedPaths:         []string{"README.md"},
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
		Flags: tagteam.FlagInputs{
			AllowedPaths: []string{"README.md"},
			NoSlice:      true,
		},
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
	m.commandBuffer = "model"
	m.handleCommandKey(nil, keyEvent{Kind: keyTab})
	if m.commandBuffer != "model " {
		t.Fatalf("command completion = %q, want model picker", m.commandBuffer)
	}
}

func TestCommandPaletteCompletesModelArgumentAndAppliesSelection(t *testing.T) {
	m, err := newModel(RunOptions{Workdir: t.TempDir()})
	if err != nil {
		t.Fatalf("newModel() error = %v", err)
	}
	m.commandMode = true
	m.targetChoices = []string{"claude:claude-sonnet-5", "codex:gpt-5.6-terra"}
	m.commandBuffer = "model "
	matches := m.matchingSlashCommands()
	if len(matches) < 2 {
		t.Fatalf("model role suggestions = %#v", matches)
	}
	m.commandSelection = 0
	m.handleCommandKey(nil, keyEvent{Kind: keyTab})
	if m.commandBuffer != "model worker " {
		t.Fatalf("role completion = %q", m.commandBuffer)
	}
	matches = m.matchingSlashCommands()
	if len(matches) != 3 {
		t.Fatalf("worker target suggestions = %#v", matches)
	}
	want := "codex:gpt-5.6-terra"
	found := false
	for i, match := range matches {
		if match.Name == "/model worker "+want {
			m.commandSelection = i
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("worker target %q missing from suggestions: %#v", want, matches)
	}
	m.handleCommandKey(nil, keyEvent{Kind: keyEnter})
	if m.compose.EditorTarget != want {
		t.Fatalf("editor target = %q, want %q", m.compose.EditorTarget, want)
	}
}

func TestCommandPaletteEnterOpensAndAcceptsArgumentPicker(t *testing.T) {
	m, err := newModel(RunOptions{Workdir: t.TempDir()})
	if err != nil {
		t.Fatalf("newModel() error = %v", err)
	}
	m.commandMode = true
	m.commandBuffer = "model"
	m.handleCommandKey(nil, keyEvent{Kind: keyEnter})
	if !m.commandMode || m.commandBuffer != "model " {
		t.Fatalf("model command did not open argument picker: mode=%t buffer=%q", m.commandMode, m.commandBuffer)
	}
	m.commandSelection = 0
	m.handleCommandKey(nil, keyEvent{Kind: keyEnter})
	if !m.commandMode || m.commandBuffer != "model worker " {
		t.Fatalf("role selection did not open target picker: mode=%t buffer=%q", m.commandMode, m.commandBuffer)
	}
	want := m.compose.EditorTarget
	m.handleCommandKey(nil, keyEvent{Kind: keyEnter})
	if m.commandMode || m.compose.EditorTarget != want {
		t.Fatalf("target selection not applied: mode=%t target=%q want=%q", m.commandMode, m.compose.EditorTarget, want)
	}
}

func TestRoleFirstModelCommandAssignsSelectedRole(t *testing.T) {
	m, err := newModel(RunOptions{Workdir: t.TempDir()})
	if err != nil {
		t.Fatalf("newModel() error = %v", err)
	}
	worker := m.compose.EditorTarget
	m.applyCommand(nil, "/model supervisor codex:gpt-5.6-sol")
	if m.compose.ReviewerTarget != "codex:gpt-5.6-sol" {
		t.Fatalf("supervisor target = %q", m.compose.ReviewerTarget)
	}
	if m.compose.EditorTarget != worker {
		t.Fatalf("supervisor assignment changed worker from %q to %q", worker, m.compose.EditorTarget)
	}

	m.applyCommand(nil, "/mode relay")
	m.applyCommand(nil, "/model scout agy:Gemini 3.5 Flash (Medium)")
	if m.compose.ScoutTarget != "agy:Gemini 3.5 Flash (Medium)" {
		t.Fatalf("scout target = %q", m.compose.ScoutTarget)
	}
}

func TestTeamShortcutSeparatesCompositionFromRunSettings(t *testing.T) {
	m, err := newModel(RunOptions{Workdir: t.TempDir()})
	if err != nil {
		t.Fatalf("newModel() error = %v", err)
	}
	m.handleKey(nil, keyEvent{Kind: keyRune, Rune: 'm'})
	if !m.teamOpen || m.settingsOpen {
		t.Fatalf("team shortcut state = team:%t settings:%t", m.teamOpen, m.settingsOpen)
	}
	for _, field := range m.teamFields() {
		if field == fieldRounds || field == fieldTest || field == fieldAllowDirty {
			t.Fatalf("team fields include execution setting %v", field)
		}
	}
	for _, field := range m.visibleFields() {
		if field == fieldEditor || field == fieldReviewer || field == fieldScout {
			t.Fatalf("settings fields include team role %v", field)
		}
	}
}

func TestModeSwitchLoadsDefaultsAndRestoresEachModesTeam(t *testing.T) {
	m, err := newModel(RunOptions{Workdir: t.TempDir()})
	if err != nil {
		t.Fatalf("newModel() error = %v", err)
	}
	m.compose.EditorTarget = "claude:custom-worker"
	m.compose.ReviewerTarget = "codex:custom-supervisor"

	if err := m.setMode("adversarial"); err != nil {
		t.Fatal(err)
	}
	if m.compose.EditorTarget != "codex:gpt-5.6-terra" || m.compose.ReviewerTarget != "codex:gpt-5.6-sol" {
		t.Fatalf("adversarial defaults = coder:%q reviewer:%q", m.compose.EditorTarget, m.compose.ReviewerTarget)
	}
	m.compose.EditorTarget = "codex:custom-coder"

	if err := m.setMode("supervisor"); err != nil {
		t.Fatal(err)
	}
	if m.compose.EditorTarget != "claude:custom-worker" || m.compose.ReviewerTarget != "codex:custom-supervisor" {
		t.Fatalf("restored supervisor team = worker:%q supervisor:%q", m.compose.EditorTarget, m.compose.ReviewerTarget)
	}
	if err := m.setMode("adversarial"); err != nil {
		t.Fatal(err)
	}
	if m.compose.EditorTarget != "codex:custom-coder" {
		t.Fatalf("restored adversarial coder = %q", m.compose.EditorTarget)
	}
}

func TestCommandPaletteOffersProfileValues(t *testing.T) {
	m, err := newModel(RunOptions{Workdir: t.TempDir()})
	if err != nil {
		t.Fatalf("newModel() error = %v", err)
	}
	m.commandBuffer = "profile rel"
	matches := m.matchingSlashCommands()
	if len(matches) != 1 || matches[0].Name != "/profile relay" {
		t.Fatalf("profile suggestions = %#v", matches)
	}
}

func TestCommandPaletteFiltersCommandsByMode(t *testing.T) {
	m, err := newModel(RunOptions{Workdir: t.TempDir()})
	if err != nil {
		t.Fatalf("newModel() error = %v", err)
	}
	for _, command := range m.matchingSlashCommands() {
		if strings.HasPrefix(command.Name, "/scout") || strings.HasPrefix(command.Name, "/reviewer") {
			t.Fatalf("supervisor palette exposed irrelevant command %q", command.Name)
		}
	}
	if err := m.setMode("relay"); err != nil {
		t.Fatal(err)
	}
	foundScout := false
	for _, command := range m.matchingSlashCommands() {
		foundScout = foundScout || strings.HasPrefix(command.Name, "/scout ")
	}
	if !foundScout {
		t.Fatal("relay palette did not expose scout selection")
	}
}

func TestEffortCommandsPropagateToRunConfig(t *testing.T) {
	m, err := newModel(RunOptions{Workdir: t.TempDir()})
	if err != nil {
		t.Fatalf("newModel() error = %v", err)
	}
	m.compose.Prompt = "ship it"
	m.applyCommand(nil, "/codex-effort xhigh")
	m.applyCommand(nil, "/claude-effort medium")
	_, cfg, err := m.buildRunOptions()
	if err != nil {
		t.Fatalf("buildRunOptions() error = %v", err)
	}
	if cfg.Adapters.Codex.ReasoningEffort != "xhigh" || cfg.Adapters.Claude.Effort != "medium" {
		t.Fatalf("efforts = codex:%q claude:%q", cfg.Adapters.Codex.ReasoningEffort, cfg.Adapters.Claude.Effort)
	}
}

func TestExecutionPolicyCommandsPropagateToRunOptions(t *testing.T) {
	m, err := newModel(RunOptions{Workdir: t.TempDir()})
	if err != nil {
		t.Fatalf("newModel() error = %v", err)
	}
	m.compose.Prompt = "ship it"
	m.applyCommand(nil, "/allow-path internal/, README.md")
	m.applyCommand(nil, "/timeout 20m")
	m.applyCommand(nil, "/watchdog-timeout 90s")
	m.applyCommand(nil, "/lint go vet ./...")

	opts, _, err := m.buildRunOptions()
	if err != nil {
		t.Fatalf("buildRunOptions() error = %v", err)
	}
	if strings.Join(opts.AllowedPaths, ",") != "internal/,README.md" {
		t.Fatalf("allowed paths = %#v", opts.AllowedPaths)
	}
	if opts.Timeout != 20*time.Minute || opts.WatchdogTimeout != 90*time.Second {
		t.Fatalf("timeouts = invocation:%s watchdog:%s", opts.Timeout, opts.WatchdogTimeout)
	}
	if opts.LintCmd != "go vet ./..." {
		t.Fatalf("lint = %q", opts.LintCmd)
	}
}

func TestBareModelCommandDoesNotClearSelection(t *testing.T) {
	m, err := newModel(RunOptions{Workdir: t.TempDir()})
	if err != nil {
		t.Fatalf("newModel() error = %v", err)
	}
	want := m.compose.EditorTarget
	m.applyCommand(nil, "/model")
	if m.compose.EditorTarget != want {
		t.Fatalf("bare /model changed target from %q to %q", want, m.compose.EditorTarget)
	}
	if !strings.Contains(m.statusMessage, "Space to choose") {
		t.Fatalf("status = %q", m.statusMessage)
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
	m.applyCommand(nil, "/model reviewer claude:opus")
	if !strings.Contains(m.statusMessage, "not part of supervisor mode") {
		t.Fatalf("role-first reviewer in supervisor mode = %q, want clear mode error", m.statusMessage)
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
		"/team",
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
		"/allow-path internal/, README.md",
		"/rounds 3",
		"/timeout 20m",
		"/watchdog-timeout 2m",
		"/test go test ./...",
		"/lint go vet ./...",
		"/no-test on",
		"/slice on",
		"/allow-dirty on",
		"/repair-json on",
		"/codex-effort high",
		"/claude-effort high",
		"/prompt ship it",
		"/focus compose",
		"/help",
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
