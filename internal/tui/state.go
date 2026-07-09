package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/cephalopod-ai/tagteam/internal/tagteam"
)

type baseFlags struct {
	Flags           tagteam.FlagInputs
	Changed         map[string]bool
	TrustRepoConfig bool
}

type focusArea int

const (
	focusRuns focusArea = iota
	focusCompose
	focusDetail
)

type loopAction int

const (
	actionContinue loopAction = iota
	actionQuit
)

type composeField int

const (
	fieldLaunch composeField = iota
	fieldPrompt
	fieldProfile
	fieldMode
	fieldEditor
	fieldReviewer
	fieldScout
	fieldScoutMode
	fieldPostScoutMode
	fieldStrictScout
	fieldScoutRetrieval
	fieldScoutContextPolicy
	fieldRounds
	fieldTest
	fieldNoTest
	fieldSlice
	fieldAllowDirty
	fieldRepairJSON
)

type editorState struct {
	Active bool
	Field  composeField
	Buffer string
}

type composeState struct {
	Prompt             string
	Profile            string
	Mode               tagteam.Mode
	EditorTarget       string
	ReviewerTarget     string
	ScoutTarget        string
	ScoutMode          string
	PostScoutMode      string
	StrictScout        bool
	StrictScoutSet     bool
	ScoutRetrieval     bool
	ScoutRetrievalSet  bool
	ScoutContextPolicy string
	Rounds             int
	TestCmd            string
	NoTest             bool
	Slice              bool
	AllowDirty         bool
	AllowDirtySet      bool
	RepairJSONWorker   bool
	RepairJSONSet      bool
}

type runListItem struct {
	RunID     string
	RunDir    string
	Mode      tagteam.Mode
	Status    string
	Verdict   string
	UpdatedAt time.Time
	Active    bool
}

type runResult struct {
	Final tagteam.FinalRun
	Err   error
}

type model struct {
	opts             RunOptions
	workdir          string
	base             baseFlags
	cfg              tagteam.Config
	configSources    []string
	compose          composeState
	targetChoices    []string
	profileChoices   []string
	runs             []runListItem
	selectedRun      int
	currentRunDir    string
	currentSnapshot  *tagteam.RunSnapshot
	currentPlan      *tagteam.ExecutionPlan
	currentReview    *tagteam.Review
	currentTestTail  []string
	focus            focusArea
	selectedField    int
	scroll           int
	showPlan         bool
	showFindings     bool
	showArtifacts    bool
	showTestOutput   bool
	settingsOpen     bool
	runsOpen         bool
	commandMode      bool
	commandBuffer    string
	commandSelection int
	editor           editorState
	statusMessage    string
	runInFlight      bool
	runResultCh      chan runResult
	width            int
	height           int
}

func newModel(opts RunOptions) (*model, error) {
	if opts.InitialRunDir != "" {
		info, err := os.Stat(opts.InitialRunDir)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("%s is not a run directory", opts.InitialRunDir)
		}
	}
	m := &model{
		opts:           opts,
		workdir:        opts.Workdir,
		base:           baseFlags{Flags: opts.Flags, Changed: opts.Changed, TrustRepoConfig: opts.TrustRepoConfig},
		focus:          focusCompose,
		showPlan:       true,
		showFindings:   true,
		showArtifacts:  true,
		showTestOutput: true,
		runResultCh:    make(chan runResult, 1),
		width:          120,
		height:         36,
	}
	if err := m.loadConfigDefaults(); err != nil {
		return nil, err
	}
	if opts.InitialRunDir != "" && opts.InspectOnStart {
		m.currentRunDir = opts.InitialRunDir
		m.focus = focusDetail
	}
	return m, nil
}

func (m *model) loadConfigDefaults() error {
	cfg, sources, err := tagteam.LoadConfigWithOptions(m.workdir, tagteam.LoadConfigOptions{
		TrustRepoConfig: m.base.TrustRepoConfig,
	})
	if err != nil {
		return err
	}
	flags := m.base.Flags
	flags.Workdir = m.workdir
	if flags.Timeout == 0 {
		flags.Timeout = 15 * time.Minute
	}
	changed := cloneChanged(m.base.Changed)
	opts, err := tagteam.ResolveOptions(cfg, sources, flags, changed, "")
	if err != nil {
		return err
	}

	m.cfg = cfg
	m.configSources = sources
	m.setComposeFromResolved(flags.Profile, opts)
	if m.compose.Rounds <= 0 {
		m.compose.Rounds = 2
	}
	m.profileChoices = collectProfileChoices(cfg)
	m.targetChoices = collectTargetChoices(cfg)
	return nil
}

func (m *model) refresh() {
	m.loadRuns()
	m.loadCurrentRun()
	m.clampSelection()
}

func (m *model) loadRuns() {
	active := map[string]bool{}
	if current, err := tagteam.ReadActiveRunForCLI(m.workdir); err == nil && current.RunDir != "" {
		active[current.RunDir] = true
	}

	root := filepath.Join(m.workdir, ".tagteam", "runs")
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			m.runs = nil
			return
		}
		m.statusMessage = fmt.Sprintf("refresh runs failed: %v", err)
		return
	}

	items := make([]runListItem, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		runDir := filepath.Join(root, entry.Name())
		snapshot, err := tagteam.BuildRunSnapshot(m.workdir, runDir)
		if err != nil {
			continue
		}
		items = append(items, runListItem{
			RunID:     snapshot.RunID,
			RunDir:    snapshot.RunDir,
			Mode:      snapshot.Mode,
			Status:    snapshot.Status,
			Verdict:   snapshot.Verdict,
			UpdatedAt: snapshot.UpdatedAt,
			Active:    active[runDir],
		})
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].Active != items[j].Active {
			return items[i].Active
		}
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
	if len(items) > 24 {
		items = items[:24]
	}
	m.runs = items

	if m.currentRunDir == "" {
		if len(items) > 0 {
			m.selectedRun = 0
		}
		return
	}
	for i, item := range items {
		if item.RunDir == m.currentRunDir {
			m.selectedRun = i
			return
		}
	}
}

func (m *model) loadCurrentRun() {
	if m.currentRunDir == "" {
		m.currentSnapshot = nil
		m.currentPlan = nil
		m.currentReview = nil
		m.currentTestTail = nil
		return
	}
	snapshot, err := tagteam.BuildRunSnapshot(m.workdir, m.currentRunDir)
	if err != nil {
		m.currentSnapshot = nil
		m.currentPlan = nil
		m.currentReview = nil
		m.currentTestTail = nil
		return
	}
	m.currentSnapshot = &snapshot
	if plan, err := tagteam.ReadPlanForCLI(m.currentRunDir); err == nil && plan.RunID != "" {
		m.currentPlan = &plan
	} else {
		m.currentPlan = nil
	}
	m.currentReview = readReviewForSnapshot(m.currentRunDir, snapshot)
	m.currentTestTail = readTailLines(snapshot.LatestTestPath, 18)
	m.clampScroll()
}

func (m *model) clampSelection() {
	fields := m.visibleFields()
	if m.selectedField >= len(fields) {
		m.selectedField = len(fields) - 1
	}
	if m.selectedField < 0 {
		m.selectedField = 0
	}
	if m.selectedRun >= len(m.runs) {
		m.selectedRun = len(m.runs) - 1
	}
	if m.selectedRun < 0 {
		m.selectedRun = 0
	}
}

func (m *model) clampScroll() {
	lines := m.detailLines()
	visible := m.detailViewportHeight()
	maxScroll := len(lines) - visible
	if maxScroll < 0 {
		maxScroll = 0
	}
	if m.scroll > maxScroll {
		m.scroll = maxScroll
	}
	if m.scroll < 0 {
		m.scroll = 0
	}
}

func (m *model) detailViewportHeight() int {
	topHeight := len(renderTopCard(m))
	composerHeight := len(renderComposerSection(m))
	overlayBudget := maxInt(0, m.height-topHeight-composerHeight-1-3)
	overlayHeight := len(renderOverlayLines(m, overlayBudget))
	return maxInt(0, m.height-topHeight-composerHeight-overlayHeight-1)
}

func (m *model) updateTerminalSize(out *os.File) {
	width, height, err := term.GetSize(int(out.Fd()))
	if err != nil {
		return
	}
	if width > 0 {
		m.width = width
	}
	if height > 0 {
		m.height = height
	}
	m.clampScroll()
}

func (m *model) handleKey(ctx context.Context, event keyEvent) loopAction {
	if m.runsOpen {
		return m.handleRunsOverlayKey(event)
	}
	if m.commandMode {
		return m.handleCommandKey(ctx, event)
	}
	if m.editor.Active {
		return m.handleEditorKey(event)
	}

	switch event.Kind {
	case keyEsc:
		if m.settingsOpen {
			m.settingsOpen = false
		}
	case keyTab:
		m.focus = (m.focus + 1) % 3
	case keyRune:
		switch event.Rune {
		case 'q', 'Q':
			return actionQuit
		case '/':
			m.commandMode = true
			m.commandBuffer = ""
			m.commandSelection = 0
		case 'r', 'R':
			m.refresh()
			m.statusMessage = "refreshed runs and snapshots"
		case 's', 'S':
			m.settingsOpen = !m.settingsOpen
			if m.settingsOpen {
				m.focus = focusCompose
			}
		case 'u', 'U':
			m.runsOpen = true
		case 'g', 'G':
			m.launchRun(ctx)
		case 'e', 'E':
			if !m.settingsOpen {
				m.focus = focusCompose
				m.startEditor(fieldPrompt)
			}
		case 'p', 'P':
			m.showPlan = !m.showPlan
		case 'f', 'F':
			m.showFindings = !m.showFindings
		case 'a', 'A':
			m.showArtifacts = !m.showArtifacts
		case 't', 'T':
			m.showTestOutput = !m.showTestOutput
		case '?':
			m.statusMessage = "slash: /run /profile /mode /model /supervisor /scout /rounds /test /watch /settings"
		default:
			m.handleFocusedKey(ctx, event)
		}
	default:
		m.handleFocusedKey(ctx, event)
	}
	return actionContinue
}

func (m *model) handleRunsOverlayKey(event keyEvent) loopAction {
	switch event.Kind {
	case keyEsc:
		m.runsOpen = false
	case keyUp:
		if m.selectedRun > 0 {
			m.selectedRun--
		}
	case keyDown:
		if m.selectedRun+1 < len(m.runs) {
			m.selectedRun++
		}
	case keyEnter:
		if len(m.runs) > 0 {
			m.currentRunDir = m.runs[m.selectedRun].RunDir
			m.focus = focusDetail
		}
		m.runsOpen = false
		m.refresh()
	}
	return actionContinue
}

func (m *model) handleFocusedKey(ctx context.Context, event keyEvent) {
	if m.settingsOpen {
		m.handleSettingsKey(ctx, event)
		return
	}
	switch m.focus {
	case focusRuns:
		m.handleRunsKey(event)
	case focusCompose:
		m.handleComposeKey(ctx, event)
	case focusDetail:
		m.handleDetailKey(event)
	}
}

func (m *model) handleSettingsKey(ctx context.Context, event keyEvent) {
	fields := m.visibleFields()
	if len(fields) == 0 {
		return
	}
	selected := fields[m.selectedField]
	switch event.Kind {
	case keyEsc:
		m.settingsOpen = false
	case keyUp:
		if m.selectedField > 0 {
			m.selectedField--
		}
	case keyDown:
		if m.selectedField+1 < len(fields) {
			m.selectedField++
		}
	case keyLeft:
		m.adjustField(selected, -1)
	case keyRight:
		m.adjustField(selected, 1)
	case keyEnter:
		if selected == fieldLaunch {
			m.launchRun(ctx)
			return
		}
		if isEditableField(selected) {
			m.startEditor(selected)
			return
		}
		m.adjustField(selected, 1)
	}
}

func (m *model) handleRunsKey(event keyEvent) {
	switch event.Kind {
	case keyUp:
		if m.selectedRun > 0 {
			m.selectedRun--
			m.currentRunDir = m.runs[m.selectedRun].RunDir
		}
	case keyDown:
		if m.selectedRun+1 < len(m.runs) {
			m.selectedRun++
			m.currentRunDir = m.runs[m.selectedRun].RunDir
		}
	case keyEnter:
		if len(m.runs) > 0 {
			m.currentRunDir = m.runs[m.selectedRun].RunDir
			m.focus = focusDetail
		}
	}
}

func (m *model) handleComposeKey(ctx context.Context, event keyEvent) {
	switch event.Kind {
	case keyEnter:
		m.startEditor(fieldPrompt)
	case keyRune:
		switch event.Rune {
		case 'j':
			m.focus = focusDetail
		case 'k':
			m.focus = focusRuns
		}
	}
}

func (m *model) handleDetailKey(event keyEvent) {
	switch event.Kind {
	case keyUp:
		m.scroll--
	case keyDown:
		m.scroll++
	case keyPageUp:
		m.scroll -= maxInt(3, m.detailViewportHeight()-2)
	case keyPageDown:
		m.scroll += maxInt(3, m.detailViewportHeight()-2)
	case keyHome:
		m.scroll = 0
	case keyEnd:
		m.scroll = len(m.detailLines())
	case keyRune:
		switch event.Rune {
		case 'k':
			m.scroll--
		case 'j':
			m.scroll++
		}
	}
	m.clampScroll()
}

func (m *model) handleCommandKey(ctx context.Context, event keyEvent) loopAction {
	switch event.Kind {
	case keyEsc:
		m.commandMode = false
		m.commandBuffer = ""
	case keyBackspace:
		m.commandBuffer = trimLastRune(m.commandBuffer)
		m.commandSelection = 0
	case keyUp:
		m.moveCommandSelection(-1)
	case keyDown:
		m.moveCommandSelection(1)
	case keyTab:
		m.completeSelectedCommand()
	case keyEnter:
		command := strings.TrimSpace(m.commandBuffer)
		m.commandMode = false
		m.commandBuffer = ""
		if command != "" {
			m.applyCommand(ctx, command)
		}
	case keyRune:
		if event.Rune == 'q' && m.commandBuffer == "" {
			m.commandMode = false
			return actionContinue
		}
		m.commandBuffer += string(event.Rune)
		m.commandSelection = 0
	}
	return actionContinue
}

func (m *model) matchingSlashCommands() []slashCommand {
	query := strings.ToLower(strings.TrimSpace(m.commandBuffer))
	matches := make([]slashCommand, 0, len(slashCommands()))
	for _, command := range slashCommands() {
		if query == "" || strings.Contains(strings.ToLower(command.Name), query) {
			matches = append(matches, command)
		}
	}
	return matches
}

func (m *model) moveCommandSelection(delta int) {
	matches := m.matchingSlashCommands()
	if len(matches) == 0 {
		m.commandSelection = 0
		return
	}
	m.commandSelection = wrapIndex(m.commandSelection+delta, len(matches))
}

func (m *model) completeSelectedCommand() {
	matches := m.matchingSlashCommands()
	if len(matches) == 0 {
		return
	}
	if m.commandSelection < 0 || m.commandSelection >= len(matches) {
		m.commandSelection = 0
	}
	name := strings.TrimPrefix(matches[m.commandSelection].Name, "/")
	if marker := strings.Index(name, "<"); marker >= 0 {
		name = strings.TrimSpace(name[:marker]) + " "
	}
	m.commandBuffer = name
	m.commandSelection = 0
}

func (m *model) requireRelayCommand(name string) error {
	return m.requireModeCommand(name, tagteam.ModeRelay)
}

func (m *model) requireModeCommand(name string, modes ...tagteam.Mode) error {
	for _, mode := range modes {
		if m.compose.Mode == mode {
			return nil
		}
	}
	labels := make([]string, len(modes))
	for index, mode := range modes {
		labels[index] = string(mode)
	}
	return fmt.Errorf("/%s is only available in %s mode", name, strings.Join(labels, ", "))
}

func (m *model) handleEditorKey(event keyEvent) loopAction {
	switch event.Kind {
	case keyEsc:
		m.editor = editorState{}
	case keyBackspace:
		m.editor.Buffer = trimLastRune(m.editor.Buffer)
	case keyEnter:
		m.applyEditorValue()
	case keyRune:
		if event.Rune >= 32 {
			m.editor.Buffer += string(event.Rune)
		}
	}
	return actionContinue
}

func (m *model) visibleFields() []composeField {
	fields := []composeField{fieldLaunch, fieldPrompt, fieldProfile, fieldMode, fieldEditor}
	if m.compose.Mode != tagteam.ModeSolo {
		fields = append(fields, fieldReviewer)
	}
	if m.compose.Mode == tagteam.ModeRelay {
		fields = append(fields, fieldScout, fieldScoutMode, fieldPostScoutMode, fieldStrictScout, fieldScoutRetrieval, fieldScoutContextPolicy)
	}
	fields = append(fields, fieldRounds, fieldTest, fieldNoTest)
	if m.compose.Mode == tagteam.ModeSupervisor {
		fields = append(fields, fieldSlice)
	}
	fields = append(fields, fieldAllowDirty, fieldRepairJSON)
	return fields
}

func (m *model) startEditor(field composeField) {
	m.editor = editorState{
		Active: true,
		Field:  field,
		Buffer: m.fieldStringValue(field),
	}
}

func (m *model) fieldStringValue(field composeField) string {
	switch field {
	case fieldPrompt:
		return m.compose.Prompt
	case fieldProfile:
		return m.compose.Profile
	case fieldEditor:
		return m.compose.EditorTarget
	case fieldReviewer:
		return m.compose.ReviewerTarget
	case fieldScout:
		return m.compose.ScoutTarget
	case fieldTest:
		return m.compose.TestCmd
	default:
		return ""
	}
}

func (m *model) applyEditorValue() {
	value := strings.TrimSpace(m.editor.Buffer)
	switch m.editor.Field {
	case fieldPrompt:
		m.compose.Prompt = value
	case fieldProfile:
		if err := m.applyProfile(value); err != nil {
			m.statusMessage = err.Error()
		}
	case fieldEditor:
		m.compose.EditorTarget = value
	case fieldReviewer:
		m.compose.ReviewerTarget = value
	case fieldScout:
		m.compose.ScoutTarget = value
	case fieldTest:
		m.compose.TestCmd = value
	}
	m.editor = editorState{}
}

func (m *model) adjustField(field composeField, delta int) {
	switch field {
	case fieldProfile:
		next := cycleString(m.profileChoiceValues(), profileLabel(m.compose.Profile), delta)
		if err := m.applyProfile(next); err != nil {
			m.statusMessage = err.Error()
		}
	case fieldMode:
		if err := m.setMode(string(cycleMode(m.compose.Mode, delta))); err != nil {
			m.statusMessage = err.Error()
		}
	case fieldEditor:
		m.compose.EditorTarget = cycleString(m.targetChoices, m.compose.EditorTarget, delta)
	case fieldReviewer:
		m.compose.ReviewerTarget = cycleString(m.targetChoices, m.compose.ReviewerTarget, delta)
	case fieldScout:
		m.compose.ScoutTarget = cycleString(m.targetChoices, m.compose.ScoutTarget, delta)
	case fieldScoutMode:
		m.compose.ScoutMode = cycleString([]string{"recon", "lint", "polish", "tests", "risk"}, m.compose.ScoutMode, delta)
	case fieldPostScoutMode:
		m.compose.PostScoutMode = cycleString([]string{"recon", "lint", "polish", "tests", "risk"}, m.compose.PostScoutMode, delta)
	case fieldStrictScout:
		m.compose.StrictScout = !m.compose.StrictScout
		m.compose.StrictScoutSet = true
	case fieldScoutRetrieval:
		m.compose.ScoutRetrieval = !m.compose.ScoutRetrieval
		m.compose.ScoutRetrievalSet = true
	case fieldScoutContextPolicy:
		m.compose.ScoutContextPolicy = cycleString([]string{"warn", "skip", "block"}, m.compose.ScoutContextPolicy, delta)
	case fieldRounds:
		m.compose.Rounds += delta
		if m.compose.Rounds < 1 {
			m.compose.Rounds = 1
		}
		if m.compose.Rounds > 9 {
			m.compose.Rounds = 9
		}
	case fieldNoTest:
		m.compose.NoTest = !m.compose.NoTest
	case fieldSlice:
		m.compose.Slice = !m.compose.Slice
	case fieldAllowDirty:
		m.compose.AllowDirty = !m.compose.AllowDirty
		m.compose.AllowDirtySet = true
	case fieldRepairJSON:
		m.compose.RepairJSONWorker = !m.compose.RepairJSONWorker
		m.compose.RepairJSONSet = true
	}
}

func (m *model) profileChoiceValues() []string {
	values := append([]string{"off"}, m.profileChoices...)
	if m.compose.Profile != "" && m.compose.Profile != "off" && !contains(values, m.compose.Profile) {
		values = append(values, m.compose.Profile)
	}
	return values
}

func (m *model) setComposeFromResolved(profile string, opts tagteam.RunOptions) {
	m.compose = composeState{
		Prompt:             m.compose.Prompt,
		Profile:            normalizeProfileValue(profile),
		Mode:               opts.Mode,
		EditorTarget:       roleTargetString(opts.Coder),
		ReviewerTarget:     roleTargetString(opts.Adversary),
		ScoutTarget:        roleTargetString(opts.Scout),
		ScoutMode:          opts.ScoutMode,
		PostScoutMode:      opts.PostScoutMode,
		StrictScout:        opts.ScoutFailurePolicy == "fail",
		ScoutRetrieval:     opts.ScoutRetrieval,
		ScoutContextPolicy: opts.ScoutContextPolicy,
		Rounds:             opts.Rounds,
		TestCmd:            opts.TestCmd,
		NoTest:             opts.NoTest,
		Slice:              opts.SupervisorSlicing,
		AllowDirty:         opts.AllowDirty,
		RepairJSONWorker:   opts.JSONRepair == "worker",
	}
}

func (m *model) applyProfile(raw string) error {
	flags := m.base.Flags
	flags.Workdir = m.workdir
	flags.Profile = normalizeProfileValue(raw)
	if flags.Timeout == 0 {
		flags.Timeout = 15 * time.Minute
	}
	cfg, sources, err := tagteam.LoadConfigWithOptions(m.workdir, tagteam.LoadConfigOptions{
		TrustRepoConfig: m.base.TrustRepoConfig,
	})
	if err != nil {
		return err
	}
	opts, err := tagteam.ResolveOptions(cfg, sources, flags, cloneChanged(m.base.Changed), "")
	if err != nil {
		return err
	}
	prompt := m.compose.Prompt
	m.cfg = cfg
	m.configSources = sources
	m.profileChoices = collectProfileChoices(cfg)
	m.targetChoices = collectTargetChoices(cfg)
	m.setComposeFromResolved(flags.Profile, opts)
	m.compose.Prompt = prompt
	m.clampSelection()
	if flags.Profile == "" {
		m.statusMessage = "profile cleared"
	} else {
		m.statusMessage = fmt.Sprintf("profile %s applied", flags.Profile)
	}
	return nil
}

func (m *model) applyCommand(ctx context.Context, raw string) {
	command := strings.TrimSpace(strings.TrimPrefix(raw, "/"))
	if command == "" {
		return
	}
	m.statusMessage = ""
	name, rest := splitCommand(command)
	switch name {
	case "help":
		m.statusMessage = "commands: /run /profile /profiles /mode /model /supervisor /scout /rounds /test /watch /settings"
	case "refresh":
		m.refresh()
		m.statusMessage = "refreshed runs and snapshots"
	case "run":
		m.launchRun(ctx)
	case "runs":
		m.runsOpen = true
	case "watch":
		if err := m.watchRun(rest); err != nil {
			m.statusMessage = err.Error()
		}
	case "mode":
		if err := m.setMode(rest); err != nil {
			m.statusMessage = err.Error()
		}
	case "profile":
		if err := m.applyProfile(rest); err != nil {
			m.statusMessage = err.Error()
			return
		}
	case "profiles":
		m.statusMessage = "profiles: " + strings.Join(m.profileChoiceValues(), ", ")
	case "settings":
		m.settingsOpen = true
		m.focus = focusCompose
	case "editor", "model":
		m.compose.EditorTarget = strings.TrimSpace(rest)
	case "worker":
		if err := m.requireModeCommand(name, tagteam.ModeSolo, tagteam.ModeSupervisor, tagteam.ModeRelay); err != nil {
			m.statusMessage = err.Error()
			return
		}
		m.compose.EditorTarget = strings.TrimSpace(rest)
	case "coder":
		if err := m.requireModeCommand(name, tagteam.ModeRelay, tagteam.ModeAdversarial); err != nil {
			m.statusMessage = err.Error()
			return
		}
		m.compose.EditorTarget = strings.TrimSpace(rest)
	case "supervisor":
		if err := m.requireModeCommand(name, tagteam.ModeSupervisor, tagteam.ModeRelay); err != nil {
			m.statusMessage = err.Error()
			return
		}
		m.compose.ReviewerTarget = strings.TrimSpace(rest)
	case "reviewer", "adversary":
		if err := m.requireModeCommand(name, tagteam.ModeAdversarial); err != nil {
			m.statusMessage = err.Error()
			return
		}
		m.compose.ReviewerTarget = strings.TrimSpace(rest)
	case "scout":
		if err := m.requireRelayCommand(name); err != nil {
			m.statusMessage = err.Error()
			return
		}
		m.compose.ScoutTarget = strings.TrimSpace(rest)
	case "scout-mode":
		if err := m.requireRelayCommand(name); err != nil {
			m.statusMessage = err.Error()
			return
		}
		if err := requireInSet("scout-mode", strings.TrimSpace(rest), []string{"recon", "lint", "polish", "tests", "risk"}); err != nil {
			m.statusMessage = err.Error()
			return
		}
		m.compose.ScoutMode = strings.TrimSpace(rest)
	case "post-scout-mode":
		if err := m.requireRelayCommand(name); err != nil {
			m.statusMessage = err.Error()
			return
		}
		if err := requireInSet("post-scout-mode", strings.TrimSpace(rest), []string{"recon", "lint", "polish", "tests", "risk"}); err != nil {
			m.statusMessage = err.Error()
			return
		}
		m.compose.PostScoutMode = strings.TrimSpace(rest)
	case "strict-scout":
		if err := m.requireRelayCommand(name); err != nil {
			m.statusMessage = err.Error()
			return
		}
		value, err := parseBoolWord(rest)
		if err != nil {
			m.statusMessage = err.Error()
			return
		}
		m.compose.StrictScout = value
		m.compose.StrictScoutSet = true
	case "scout-retrieval":
		if err := m.requireRelayCommand(name); err != nil {
			m.statusMessage = err.Error()
			return
		}
		value, err := parseBoolWord(rest)
		if err != nil {
			m.statusMessage = err.Error()
			return
		}
		m.compose.ScoutRetrieval = value
		m.compose.ScoutRetrievalSet = true
	case "scout-context-policy":
		if err := m.requireRelayCommand(name); err != nil {
			m.statusMessage = err.Error()
			return
		}
		value := strings.TrimSpace(rest)
		if err := requireInSet("scout-context-policy", value, []string{"warn", "skip", "block"}); err != nil {
			m.statusMessage = err.Error()
			return
		}
		m.compose.ScoutContextPolicy = value
	case "rounds":
		value, err := strconv.Atoi(strings.TrimSpace(rest))
		if err != nil || value <= 0 {
			m.statusMessage = "rounds must be a positive integer"
			return
		}
		m.compose.Rounds = value
	case "test":
		m.compose.TestCmd = rest
	case "prompt":
		m.compose.Prompt = rest
	case "slice":
		if err := m.requireModeCommand(name, tagteam.ModeSupervisor); err != nil {
			m.statusMessage = err.Error()
			return
		}
		value, err := parseBoolWord(rest)
		if err != nil {
			m.statusMessage = err.Error()
			return
		}
		m.compose.Slice = value
	case "no-test":
		value, err := parseBoolWord(rest)
		if err != nil {
			m.statusMessage = err.Error()
			return
		}
		m.compose.NoTest = value
	case "allow-dirty":
		value, err := parseBoolWord(rest)
		if err != nil {
			m.statusMessage = err.Error()
			return
		}
		m.compose.AllowDirty = value
		m.compose.AllowDirtySet = true
	case "repair-json":
		value, err := parseBoolWord(rest)
		if err != nil {
			m.statusMessage = err.Error()
			return
		}
		m.compose.RepairJSONWorker = value
		m.compose.RepairJSONSet = true
	case "focus":
		switch strings.TrimSpace(rest) {
		case "runs":
			m.focus = focusRuns
		case "compose":
			m.focus = focusCompose
		case "detail":
			m.focus = focusDetail
		default:
			m.statusMessage = "focus must be runs, compose, or detail"
			return
		}
	case "toggle":
		switch strings.TrimSpace(rest) {
		case "plan":
			m.showPlan = !m.showPlan
		case "findings":
			m.showFindings = !m.showFindings
		case "artifacts":
			m.showArtifacts = !m.showArtifacts
		case "test":
			m.showTestOutput = !m.showTestOutput
		default:
			m.statusMessage = "toggle must be plan, findings, artifacts, or test"
			return
		}
	default:
		m.statusMessage = fmt.Sprintf("unknown command %q", name)
		return
	}
	if m.statusMessage == "" {
		m.statusMessage = fmt.Sprintf("updated %s", name)
	}
}

func (m *model) watchRun(raw string) error {
	target := strings.TrimSpace(raw)
	switch target {
	case "":
		return fmt.Errorf("watch requires a run id, active, or latest")
	case "active":
		active, err := tagteam.ReadActiveRunForCLI(m.workdir)
		if err != nil {
			return err
		}
		m.currentRunDir = active.RunDir
	case "latest":
		latest, err := tagteam.ReadLatestForCLI(m.workdir)
		if err != nil {
			return err
		}
		m.currentRunDir = latest.RunDir
	default:
		runDir := filepath.Join(m.workdir, ".tagteam", "runs", target)
		info, err := os.Stat(runDir)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("run %q not found", target)
		}
		m.currentRunDir = runDir
	}
	m.focus = focusDetail
	m.refresh()
	m.statusMessage = fmt.Sprintf("watching %s", filepath.Base(m.currentRunDir))
	return nil
}

func (m *model) setMode(raw string) error {
	mode, err := tagteam.ParseMode(strings.TrimSpace(raw))
	if err != nil {
		return err
	}
	previous := m.compose.Mode
	m.compose.Mode = mode
	if err := m.fillMissingModeTargets(mode); err != nil {
		m.compose.Mode = previous
		return err
	}
	m.clampSelection()
	return nil
}

func (m *model) fillMissingModeTargets(mode tagteam.Mode) error {
	flags := m.base.Flags
	flags.Workdir = m.workdir
	flags.Profile = m.compose.Profile
	flags.Mode = string(mode)
	if flags.Timeout == 0 {
		flags.Timeout = 15 * time.Minute
	}
	changed := cloneChanged(m.base.Changed)
	clearTUIControlledFlags(changed)
	changed["mode"] = true
	defaults, err := tagteam.ResolveOptions(m.cfg, m.configSources, flags, changed, "")
	if err != nil {
		return err
	}
	if m.compose.EditorTarget == "" {
		m.compose.EditorTarget = roleTargetString(defaults.Coder)
	}
	if mode != tagteam.ModeSolo && m.compose.ReviewerTarget == "" {
		m.compose.ReviewerTarget = roleTargetString(defaults.Adversary)
	}
	if mode == tagteam.ModeRelay && m.compose.ScoutTarget == "" {
		m.compose.ScoutTarget = roleTargetString(defaults.Scout)
	}
	return nil
}

func (m *model) launchRun(ctx context.Context) {
	if m.runInFlight {
		m.statusMessage = "a TUI-launched run is already in flight"
		return
	}
	opts, cfg, err := m.buildRunOptions()
	if err != nil {
		m.statusMessage = err.Error()
		return
	}
	m.runInFlight = true
	m.statusMessage = "launching run..."
	go func() {
		app := tagteam.NewApp(cfg)
		final, runErr := app.Run(ctx, opts)
		m.runResultCh <- runResult{Final: final, Err: runErr}
	}()
}

func (m *model) buildRunOptions() (tagteam.RunOptions, tagteam.Config, error) {
	flags := m.base.Flags
	flags.Workdir = m.workdir
	flags.Profile = strings.TrimSpace(m.compose.Profile)
	flags.Mode = string(m.compose.Mode)
	flags.Rounds = m.compose.Rounds
	flags.Test = m.compose.TestCmd
	flags.NoTest = m.compose.NoTest
	flags.AllowDirty = m.compose.AllowDirty
	flags.Quiet = true
	flags.Verbose = false
	flags.RepairJSONWithWorker = m.compose.RepairJSONWorker
	flags.StrictScout = m.compose.StrictScout
	flags.NoScoutRetrieval = !m.compose.ScoutRetrieval
	flags.ScoutContextPolicy = m.compose.ScoutContextPolicy
	flags.ScoutMode = m.compose.ScoutMode
	flags.PostScoutMode = m.compose.PostScoutMode
	flags.Slice = m.compose.Slice
	flags.NoSlice = !m.compose.Slice
	flags.Solo = ""
	flags.Worker = ""
	flags.CoderRole = ""
	flags.Supervisor = ""
	flags.Reviewer = ""
	flags.Scout = ""

	changed := cloneChanged(m.base.Changed)
	clearTUIControlledFlags(changed)
	changed["mode"] = true
	changed["rounds"] = true
	changed["test"] = true
	changed["no-test"] = true
	if m.compose.AllowDirty {
		changed["allow-dirty"] = true
	}
	if m.compose.RepairJSONWorker {
		changed["repair-json-with-worker"] = true
	}
	if m.compose.StrictScout {
		changed["strict-scout"] = true
	}
	if !m.compose.ScoutRetrieval {
		changed["no-scout-retrieval"] = true
	}
	if strings.TrimSpace(m.compose.ScoutContextPolicy) != "" {
		changed["scout-context-policy"] = true
	}
	if m.compose.Mode == tagteam.ModeRelay {
		changed["scout-mode"] = true
		changed["post-scout-mode"] = true
		changed["scout"] = true
		flags.Scout = strings.TrimSpace(m.compose.ScoutTarget)
	}
	if m.compose.Mode == tagteam.ModeSupervisor {
		if m.compose.Slice {
			changed["slice"] = true
		} else {
			changed["no-slice"] = true
		}
	}

	switch m.compose.Mode {
	case tagteam.ModeSolo:
		flags.Solo = strings.TrimSpace(m.compose.EditorTarget)
		changed["solo"] = true
	case tagteam.ModeSupervisor:
		flags.Worker = strings.TrimSpace(m.compose.EditorTarget)
		flags.Supervisor = strings.TrimSpace(m.compose.ReviewerTarget)
		changed["worker"] = true
		changed["supervisor"] = true
	case tagteam.ModeRelay:
		flags.CoderRole = strings.TrimSpace(m.compose.EditorTarget)
		flags.Supervisor = strings.TrimSpace(m.compose.ReviewerTarget)
		changed["coder"] = true
		changed["supervisor"] = true
	case tagteam.ModeAdversarial:
		flags.CoderRole = strings.TrimSpace(m.compose.EditorTarget)
		flags.Reviewer = strings.TrimSpace(m.compose.ReviewerTarget)
		changed["coder"] = true
		changed["reviewer"] = true
	}

	cfg, sources, err := tagteam.LoadConfigWithOptions(m.workdir, tagteam.LoadConfigOptions{
		TrustRepoConfig: m.base.TrustRepoConfig,
	})
	if err != nil {
		return tagteam.RunOptions{}, tagteam.Config{}, err
	}
	runOpts, err := tagteam.ResolveOptions(cfg, sources, flags, changed, m.compose.Prompt)
	if err != nil {
		return tagteam.RunOptions{}, tagteam.Config{}, err
	}
	runOpts.Quiet = true
	runOpts.Verbose = false
	if m.compose.StrictScoutSet {
		if m.compose.StrictScout {
			runOpts.ScoutFailurePolicy = "fail"
			runOpts.LossPolicy.Scout = tagteam.LossPolicyBlock
		} else {
			runOpts.ScoutFailurePolicy = "continue"
			runOpts.LossPolicy.Scout = tagteam.LossPolicyDegrade
		}
	}
	if m.compose.ScoutRetrievalSet {
		runOpts.ScoutRetrieval = m.compose.ScoutRetrieval
	}
	if m.compose.AllowDirtySet {
		runOpts.AllowDirty = m.compose.AllowDirty
		if !m.compose.AllowDirty && runOpts.GitSafety == "allow-dirty" {
			runOpts.GitSafety = "clean"
		}
	}
	if m.compose.RepairJSONSet {
		if m.compose.RepairJSONWorker {
			runOpts.JSONRepair = "worker"
		} else {
			runOpts.JSONRepair = "off"
		}
	}
	return runOpts, cfg, nil
}

func clearTUIControlledFlags(changed map[string]bool) {
	for _, name := range []string{
		"allow-dirty", "coder", "mc", "mode", "no-scout-retrieval", "no-slice",
		"no-test", "post-scout-mode", "profile", "relay", "repair-json-with-worker",
		"reviewer", "scout", "scout-context-policy", "scout-mode", "slice", "solo",
		"strict-scout", "supervisor", "test", "worker",
	} {
		delete(changed, name)
	}
}

func (m *model) handleRunResult(result runResult) {
	m.runInFlight = false
	if result.Final.RunDir != "" {
		m.currentRunDir = result.Final.RunDir
		m.focus = focusDetail
	}
	m.refresh()
	if result.Err != nil {
		if result.Final.RunID != "" {
			m.statusMessage = fmt.Sprintf("run %s finished with %s", result.Final.RunID, result.Final.Status)
			return
		}
		m.statusMessage = result.Err.Error()
		return
	}
	if result.Final.RunID != "" {
		m.statusMessage = fmt.Sprintf("run %s completed: %s", result.Final.RunID, result.Final.Verdict)
	}
}

func (m *model) detailLines() []string {
	if m.currentSnapshot == nil {
		return []string{
			"",
			"Ready for a new task.",
			"",
			"Type directly in the composer below or press / for commands.",
			"",
			"Useful commands:",
			"  /profile relay",
			"  /model claude:claude-sonnet-5",
			"  /mode relay",
			"  /runs",
			"  /watch latest",
			"  /settings",
		}
	}

	s := m.currentSnapshot
	lines := []string{
		fmt.Sprintf("Run: %s", s.RunID),
		fmt.Sprintf("Mode: %s", dashIfEmpty(string(s.Mode))),
		fmt.Sprintf("Status: %s", dashIfEmpty(s.Status)),
		fmt.Sprintf("Phase: %s", dashIfEmpty(s.Phase)),
		fmt.Sprintf("Verdict: %s", dashIfEmpty(s.Verdict)),
		fmt.Sprintf("Updated: %s", formatTime(s.UpdatedAt)),
		fmt.Sprintf("Rounds: %d/%d current=%d", s.RoundsCompleted, s.RoundsRequested, s.CurrentRound),
	}
	if s.Degraded {
		lines = append(lines, fmt.Sprintf("Degraded: true (%s)", dashIfEmpty(s.DegradedReason)))
	}
	if s.BlockingReason != "" {
		lines = append(lines, fmt.Sprintf("Blocking reason: %s", s.BlockingReason))
	}
	lines = append(lines, "", "Roles:")
	lines = append(lines, renderRoleDetailLines(s.RoleStatuses)...)

	if m.showPlan && m.currentPlan != nil {
		lines = append(lines, "", fmt.Sprintf("Plan (%s, %d items):", m.currentPlan.Status, len(m.currentPlan.Items)))
		for _, item := range m.currentPlan.Items {
			lines = append(lines, fmt.Sprintf("  [%s] %-9s %s", item.ID, item.Status, item.Title))
		}
	}

	if m.showFindings && (s.FindingsCount > 0 || m.currentReview != nil) {
		lines = append(lines, "", fmt.Sprintf("Findings: %d", s.FindingsCount))
		if m.currentReview != nil {
			lines = append(lines, "Review summary: "+dashIfEmpty(m.currentReview.Summary))
			for _, finding := range m.currentReview.Findings {
				location := finding.File
				if finding.Line > 0 {
					location = fmt.Sprintf("%s:%d", finding.File, finding.Line)
				}
				lines = append(lines, fmt.Sprintf("  [%s] %s %s", finding.Severity, location, finding.Issue))
			}
		}
	}

	if m.showArtifacts && (len(s.ChangedFiles) > 0 || s.LatestDiffPath != "" || s.LatestReviewPath != "" || s.LatestTestPath != "") {
		lines = append(lines, "", fmt.Sprintf("Changed files (%d):", len(s.ChangedFiles)))
		for _, file := range s.ChangedFiles {
			lines = append(lines, "  "+file)
		}
		lines = append(lines, "", "Artifacts:")
		lines = append(lines,
			"  diff:   "+dashIfEmpty(s.LatestDiffPath),
			"  review: "+dashIfEmpty(s.LatestReviewPath),
			"  test:   "+dashIfEmpty(s.LatestTestPath),
			"  run:    "+dashIfEmpty(s.RunDir),
		)
	}

	if m.showTestOutput && len(m.currentTestTail) > 0 {
		lines = append(lines, "", "Test output tail:")
		lines = append(lines, m.currentTestTail...)
	}

	return lines
}

func (m *model) settingsLines() []string {
	fields := m.visibleFields()
	lines := make([]string, 0, len(fields)+4)
	lines = append(lines, "Left/right changes values. Enter edits text fields. Esc closes.")
	if len(m.profileChoices) > 0 {
		lines = append(lines, "Profiles: off, "+strings.Join(m.profileChoices, ", "))
	}
	for index, field := range fields {
		prefix := "  "
		if m.settingsOpen && m.selectedField == index {
			prefix = "> "
		}
		if m.editor.Active && m.editor.Field == field {
			lines = append(lines, prefix+composeFieldLabel(m.compose.Mode, field)+": "+m.editor.Buffer+" _")
			continue
		}
		lines = append(lines, prefix+composeFieldLabel(m.compose.Mode, field)+": "+m.composeFieldValue(field))
	}
	return lines
}

func (m *model) composerLines(width int) []string {
	lines := []string{}
	modeLine := fmt.Sprintf("draft %s  target=%s", m.compose.Mode, m.primaryTarget())
	if m.compose.Mode != tagteam.ModeSolo {
		modeLine += "  review=" + dashIfEmpty(m.compose.ReviewerTarget)
	}
	if m.compose.Mode == tagteam.ModeRelay {
		modeLine += "  scout=" + dashIfEmpty(m.compose.ScoutTarget)
	}
	lines = append(lines, modeLine)

	switch {
	case m.commandMode:
		lines = append(lines, "/"+m.commandBuffer+"_")
	case m.editor.Active && m.editor.Field == fieldPrompt:
		lines = append(lines, "> "+m.editor.Buffer+"_")
	default:
		prompt := strings.TrimSpace(m.compose.Prompt)
		if prompt == "" {
			prompt = "Describe the task. Press Enter to edit, / for commands, s for settings."
		}
		lines = append(lines, "> "+padOrTrim(prompt, maxInt(20, width-4)))
	}
	lines = append(lines, fmt.Sprintf("profile=%s  rounds=%d  tests=%s  dirty=%s  launch=g", dashIfEmpty(profileLabel(m.compose.Profile)), m.compose.Rounds, onOff(!m.compose.NoTest), onOff(m.compose.AllowDirty)))
	footer := "Enter edit  / commands  s settings  u runs"
	if m.currentSnapshot != nil {
		footer += "  p/f/a/t detail"
	}
	lines = append(lines, footer)
	return lines
}

func (m *model) primaryTarget() string {
	switch m.compose.Mode {
	case tagteam.ModeSolo, tagteam.ModeSupervisor:
		return dashIfEmpty(m.compose.EditorTarget)
	default:
		return dashIfEmpty(m.compose.EditorTarget)
	}
}

func (m *model) composeFieldValue(field composeField) string {
	switch field {
	case fieldLaunch:
		if m.runInFlight {
			return "launching..."
		}
		return "press Enter or g"
	case fieldPrompt:
		return dashIfEmpty(m.compose.Prompt)
	case fieldProfile:
		return profileLabel(m.compose.Profile)
	case fieldMode:
		return string(m.compose.Mode)
	case fieldEditor:
		return dashIfEmpty(m.compose.EditorTarget)
	case fieldReviewer:
		return dashIfEmpty(m.compose.ReviewerTarget)
	case fieldScout:
		return dashIfEmpty(m.compose.ScoutTarget)
	case fieldScoutMode:
		return m.compose.ScoutMode
	case fieldPostScoutMode:
		return m.compose.PostScoutMode
	case fieldStrictScout:
		return onOff(m.compose.StrictScout)
	case fieldScoutRetrieval:
		return onOff(m.compose.ScoutRetrieval)
	case fieldScoutContextPolicy:
		return dashIfEmpty(m.compose.ScoutContextPolicy)
	case fieldRounds:
		return strconv.Itoa(m.compose.Rounds)
	case fieldTest:
		if m.compose.NoTest {
			return "(disabled)"
		}
		return dashIfEmpty(m.compose.TestCmd)
	case fieldNoTest:
		return onOff(m.compose.NoTest)
	case fieldSlice:
		return onOff(m.compose.Slice)
	case fieldAllowDirty:
		return onOff(m.compose.AllowDirty)
	case fieldRepairJSON:
		return onOff(m.compose.RepairJSONWorker)
	default:
		return ""
	}
}

func readReviewForSnapshot(runDir string, snapshot tagteam.RunSnapshot) *tagteam.Review {
	for _, path := range []string{
		snapshot.LatestReviewPath,
		filepath.Join(runDir, "final.json"),
	} {
		if strings.TrimSpace(path) == "" {
			continue
		}
		review := &tagteam.Review{}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if strings.HasSuffix(path, "final.json") {
			var final tagteam.FinalRun
			if err := json.Unmarshal(data, &final); err == nil && final.Review != nil {
				return final.Review
			}
			continue
		}
		if err := json.Unmarshal(data, review); err == nil && (review.Summary != "" || len(review.Findings) > 0) {
			return review
		}
	}
	return nil
}

func readTailLines(path string, maxLines int) []string {
	if path == "" || maxLines <= 0 {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	for i, line := range lines {
		lines[i] = "  " + line
	}
	return lines
}

func renderRoleDetailLines(roles map[string]tagteam.RoleStatus) []string {
	if len(roles) == 0 {
		return []string{"  (none reported yet)"}
	}
	names := make([]string, 0, len(roles))
	for name := range roles {
		names = append(names, name)
	}
	sort.Strings(names)
	lines := make([]string, 0, len(names))
	for _, name := range names {
		role := roles[name]
		line := fmt.Sprintf("  %-11s status=%-10s adapter=%s", name, dashIfEmpty(role.Status), dashIfEmpty(role.Adapter))
		if role.Model != "" {
			line += " model=" + role.Model
		}
		if role.Message != "" {
			line += " message=" + role.Message
		}
		lines = append(lines, line)
	}
	return lines
}

func composeFieldLabel(mode tagteam.Mode, field composeField) string {
	switch field {
	case fieldLaunch:
		return "launch"
	case fieldPrompt:
		return "prompt"
	case fieldProfile:
		return "profile"
	case fieldMode:
		return "mode"
	case fieldEditor:
		switch mode {
		case tagteam.ModeSolo:
			return "solo target"
		case tagteam.ModeSupervisor:
			return "worker"
		default:
			return "coder"
		}
	case fieldReviewer:
		switch mode {
		case tagteam.ModeSupervisor, tagteam.ModeRelay:
			return "supervisor"
		default:
			return "adversary"
		}
	case fieldScout:
		return "scout"
	case fieldScoutMode:
		return "scout mode"
	case fieldPostScoutMode:
		return "post-scout"
	case fieldStrictScout:
		return "strict-scout"
	case fieldScoutRetrieval:
		return "scout-retrieval"
	case fieldScoutContextPolicy:
		return "scout-context"
	case fieldRounds:
		return "rounds"
	case fieldTest:
		return "test"
	case fieldNoTest:
		return "no-test"
	case fieldSlice:
		return "slice"
	case fieldAllowDirty:
		return "allow-dirty"
	case fieldRepairJSON:
		return "repair-json"
	default:
		return "field"
	}
}

func collectProfileChoices(cfg tagteam.Config) []string {
	choices := make([]string, 0, len(cfg.Profiles))
	for name := range cfg.Profiles {
		choices = append(choices, name)
	}
	sort.Strings(choices)
	return choices
}

func collectTargetChoices(cfg tagteam.Config) []string {
	var choices []string
	add := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" || contains(choices, raw) {
			return
		}
		choices = append(choices, raw)
	}

	add(cfg.Defaults.Worker)
	add(cfg.Defaults.Supervisor)
	add(cfg.Defaults.Coder)
	add(cfg.Defaults.RelayCoder)
	add(cfg.Defaults.Adversary)
	add(cfg.Defaults.Scout)
	for _, profile := range cfg.Profiles {
		add(profile.Worker)
		add(profile.Supervisor)
		add(profile.Coder)
		add(profile.Adversary)
		add(profile.Scout)
	}
	for _, target := range []string{
		withModel("codex", cfg.Adapters.Codex.DefaultModel),
		withModel("claude", cfg.Adapters.Claude.DefaultModel),
		withModel("codex-oss", cfg.Adapters.CodexOSS.DefaultModel),
		withModel("agy", cfg.Adapters.Agy.DefaultModel),
		withModel("gosling", cfg.Adapters.Gosling.DefaultModel),
		withModel("openai-compatible", cfg.Adapters.OpenAICompatible.DefaultModel),
	} {
		add(target)
	}
	return choices
}

func withModel(adapter, model string) string {
	if strings.TrimSpace(model) == "" {
		return adapter
	}
	return adapter + ":" + model
}

func roleTargetString(target tagteam.RoleTarget) string {
	if target.Adapter == "" {
		return ""
	}
	if target.Model == "" {
		return target.Adapter
	}
	return target.Adapter + ":" + target.Model
}

func isEditableField(field composeField) bool {
	switch field {
	case fieldPrompt, fieldProfile, fieldEditor, fieldReviewer, fieldScout, fieldTest:
		return true
	default:
		return false
	}
}

func cycleMode(current tagteam.Mode, delta int) tagteam.Mode {
	values := []tagteam.Mode{tagteam.ModeSupervisor, tagteam.ModeRelay, tagteam.ModeSolo, tagteam.ModeAdversarial}
	index := 0
	for i, item := range values {
		if item == current {
			index = i
			break
		}
	}
	index = wrapIndex(index+delta, len(values))
	return values[index]
}

func cycleString(values []string, current string, delta int) string {
	if len(values) == 0 {
		return current
	}
	if current == "" {
		return values[0]
	}
	index := 0
	for i, item := range values {
		if item == current {
			index = i
			break
		}
	}
	index = wrapIndex(index+delta, len(values))
	return values[index]
}

func wrapIndex(index, length int) int {
	if length == 0 {
		return 0
	}
	for index < 0 {
		index += length
	}
	return index % length
}

func splitCommand(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	parts := strings.Fields(raw)
	if len(parts) == 1 {
		return strings.ToLower(parts[0]), ""
	}
	return strings.ToLower(parts[0]), strings.TrimSpace(strings.TrimPrefix(raw, parts[0]))
}

func parseBoolWord(raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "on", "true", "yes", "1":
		return true, nil
	case "off", "false", "no", "0":
		return false, nil
	default:
		return false, fmt.Errorf("expected on/off")
	}
}

func requireInSet(label, value string, allowed []string) error {
	for _, item := range allowed {
		if value == item {
			return nil
		}
	}
	return fmt.Errorf("%s must be one of %s", label, strings.Join(allowed, ", "))
}

func trimLastRune(s string) string {
	if s == "" {
		return s
	}
	_, size := utf8.DecodeLastRuneInString(s)
	if size <= 0 || size > len(s) {
		return ""
	}
	return s[:len(s)-size]
}

func cloneChanged(src map[string]bool) map[string]bool {
	dst := make(map[string]bool, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func dashIfEmpty(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

func profileLabel(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "off"
	}
	return s
}

func normalizeProfileValue(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "off") {
		return ""
	}
	return s
}

func wrapText(s string, width int) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return []string{""}
	}
	if width <= 1 {
		return []string{s}
	}

	words := strings.Fields(s)
	lines := make([]string, 0, len(words))
	line := ""
	for _, word := range words {
		if line == "" {
			line = word
			continue
		}
		if len([]rune(line))+1+len([]rune(word)) <= width {
			line += " " + word
			continue
		}
		lines = append(lines, line)
		line = word
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format(time.RFC3339)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
