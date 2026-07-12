package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

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
	fieldCodexEffort
	fieldClaudeEffort
	fieldScoutMode
	fieldPostScoutMode
	fieldStrictScout
	fieldScoutRetrieval
	fieldScoutContextPolicy
	fieldAllowedPaths
	fieldRounds
	fieldTimeout
	fieldWatchdogTimeout
	fieldTest
	fieldLint
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
	CodexEffort        string
	ClaudeEffort       string
	ScoutMode          string
	PostScoutMode      string
	StrictScout        bool
	StrictScoutSet     bool
	ScoutRetrieval     bool
	ScoutRetrievalSet  bool
	ScoutContextPolicy string
	AllowedPaths       []string
	Rounds             int
	Timeout            time.Duration
	WatchdogTimeout    time.Duration
	TestCmd            string
	LintCmd            string
	NoTest             bool
	Slice              bool
	AllowDirty         bool
	AllowDirtySet      bool
	RepairJSONWorker   bool
	RepairJSONSet      bool
}

type roleAssignments struct {
	Editor   string
	Reviewer string
	Scout    string
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
	teamAssignments  map[tagteam.Mode]roleAssignments
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
	selectedTeam     int
	scroll           int
	showPlan         bool
	showFindings     bool
	showArtifacts    bool
	showTestOutput   bool
	teamOpen         bool
	settingsOpen     bool
	runsOpen         bool
	commandMode      bool
	commandBuffer    string
	commandSelection int
	editor           editorState
	statusMessage    string
	runInFlight      bool
	mutationBlocked  string
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
		opts:            opts,
		workdir:         opts.Workdir,
		base:            baseFlags{Flags: opts.Flags, Changed: opts.Changed, TrustRepoConfig: opts.TrustRepoConfig},
		focus:           focusCompose,
		teamAssignments: make(map[tagteam.Mode]roleAssignments),
		showPlan:        true,
		showFindings:    true,
		showArtifacts:   true,
		showTestOutput:  true,
		runResultCh:     make(chan runResult, 1),
		width:           120,
		height:          36,
		mutationBlocked: opts.MutationBlocked,
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
	m.resetTeamAssignments()
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

	root := tagteam.RunsRootForCLI(m.workdir)
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
	teamFields := m.teamFields()
	if m.selectedTeam >= len(teamFields) {
		m.selectedTeam = len(teamFields) - 1
	}
	if m.selectedTeam < 0 {
		m.selectedTeam = 0
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
		if m.teamOpen {
			m.teamOpen = false
		}
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
				m.teamOpen = false
				m.focus = focusCompose
			}
		case 'm', 'M':
			m.teamOpen = !m.teamOpen
			if m.teamOpen {
				m.settingsOpen = false
				m.focus = focusCompose
			}
		case 'u', 'U':
			m.runsOpen = true
		case 'g', 'G':
			m.launchRun(ctx)
		case 'e', 'E':
			if !m.teamOpen && !m.settingsOpen {
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
			m.statusMessage = "m team  / contextual commands  s run settings  u runs  g launch"
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
	if m.teamOpen {
		m.handleTeamKey(event)
		return
	}
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

func (m *model) handleTeamKey(event keyEvent) {
	fields := m.teamFields()
	if len(fields) == 0 {
		return
	}
	selected := fields[m.selectedTeam]
	switch event.Kind {
	case keyEsc:
		m.teamOpen = false
	case keyUp:
		if m.selectedTeam > 0 {
			m.selectedTeam--
		}
	case keyDown:
		if m.selectedTeam+1 < len(fields) {
			m.selectedTeam++
		}
	case keyLeft:
		m.adjustField(selected, -1)
		m.clampSelection()
	case keyRight:
		m.adjustField(selected, 1)
		m.clampSelection()
	case keyEnter:
		if isEditableField(selected) {
			m.startEditor(selected)
			return
		}
		m.adjustField(selected, 1)
		m.clampSelection()
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
		if selected, ok := m.selectedSlashCommand(); ok && shouldAcceptSlashSelection(m.commandBuffer, selected) {
			completion, needsArgument := slashCommandCompletion(selected.Name)
			if needsArgument {
				m.commandBuffer = completion
				m.commandSelection = 0
				return actionContinue
			}
			command = completion
		}
		m.commandMode = false
		m.commandBuffer = ""
		if command != "" {
			return m.applyCommand(ctx, command)
		}
	case keyRune:
		m.commandBuffer += string(event.Rune)
		m.commandSelection = 0
	}
	return actionContinue
}
