package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cephalopod-ai/tagteam/internal/tagteam"
)

func (m *model) matchingSlashCommands() []slashCommand {
	raw := strings.TrimLeft(m.commandBuffer, " ")
	name, argument := splitCommand(raw)
	if strings.Contains(raw, " ") {
		if suggestions, ok := m.slashArgumentSuggestions(name, argument, strings.HasSuffix(raw, " ")); ok {
			query := strings.ToLower(strings.TrimSpace(argument))
			if name == "model" {
				parts := strings.Fields(argument)
				if len(parts) > 0 && m.teamRole(parts[0]) != nil && (len(parts) > 1 || strings.HasSuffix(raw, " ")) {
					query = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(argument, parts[0])))
				}
			}
			matches := make([]slashCommand, 0, len(suggestions))
			for _, suggestion := range suggestions {
				if query == "" || strings.Contains(strings.ToLower(suggestion.Name+" "+suggestion.Description), query) {
					matches = append(matches, suggestion)
				}
			}
			return matches
		}
	}

	query := strings.ToLower(strings.TrimSpace(raw))
	matches := make([]slashCommand, 0, len(slashCommands()))
	for _, command := range slashCommands() {
		commandName, _ := splitCommand(strings.TrimPrefix(command.Name, "/"))
		if !m.slashCommandAvailable(commandName) {
			continue
		}
		if query == "" || strings.Contains(strings.ToLower(command.Name), query) {
			matches = append(matches, command)
		}
	}
	return matches
}

func (m *model) slashCommandAvailable(name string) bool {
	switch name {
	case "worker", "coder":
		// Kept as accepted aliases, but /model is the single primary picker.
		return false
	case "supervisor":
		return m.compose.Mode == tagteam.ModeSupervisor || m.compose.Mode == tagteam.ModeRelay
	case "reviewer":
		return m.compose.Mode == tagteam.ModeAdversarial
	case "scout", "scout-mode", "post-scout-mode", "strict-scout", "scout-retrieval", "scout-context-policy":
		return m.compose.Mode == tagteam.ModeRelay
	case "slice":
		return m.compose.Mode == tagteam.ModeSupervisor
	case "repair-json":
		return m.compose.Mode != tagteam.ModeSolo
	default:
		return true
	}
}

func (m *model) slashArgumentSuggestions(name, argument string, trailingSpace bool) ([]slashCommand, bool) {
	choice := func(value, description string) slashCommand {
		return slashCommand{Name: "/" + name + " " + value, Description: description}
	}
	current := func(value, selected, description string) slashCommand {
		if value == selected {
			description = "current · " + description
		}
		return choice(value, description)
	}

	switch name {
	case "mode":
		values := []tagteam.Mode{tagteam.ModeSupervisor, tagteam.ModeRelay, tagteam.ModeSolo, tagteam.ModeAdversarial}
		out := make([]slashCommand, 0, len(values))
		for _, value := range values {
			out = append(out, current(string(value), string(m.compose.Mode), modeDescription(value)))
		}
		return out, true
	case "profile":
		values := m.profileChoiceValues()
		out := make([]slashCommand, 0, len(values))
		selected := profileLabel(m.compose.Profile)
		for _, value := range values {
			description := "use config defaults"
			if value != "off" {
				profile := m.cfg.Profiles[value]
				description = "named profile"
				if profile.Mode != "" {
					description = profile.Mode + " mode"
				}
			}
			out = append(out, current(value, selected, description))
		}
		return out, true
	case "model":
		parts := strings.Fields(argument)
		if len(parts) == 0 {
			return m.modelRoleSuggestions(), true
		}
		if role := m.teamRole(parts[0]); role != nil {
			if len(parts) == 1 && !trailingSpace {
				return m.modelRoleSuggestions(), true
			}
			return m.targetSuggestions("model "+role.Name, m.composeFieldValue(role.Field), role.Purpose, role.Kind), true
		}
		// Preserve direct /model <target> for scripts and muscle memory.
		return m.targetSuggestions(name, m.compose.EditorTarget, "primary model", targetEditor), true
	case "worker", "coder":
		return m.targetSuggestions(name, m.compose.EditorTarget, "primary model", targetEditor), true
	case "supervisor", "reviewer":
		return m.targetSuggestions(name, m.compose.ReviewerTarget, "review model", targetReviewer), true
	case "scout":
		return m.targetSuggestions(name, m.compose.ScoutTarget, "scout model", targetScout), true
	case "codex-effort":
		return valueSuggestions(name, []string{"none", "minimal", "low", "medium", "high", "xhigh"}, m.compose.CodexEffort), true
	case "claude-effort":
		return valueSuggestions(name, []string{"low", "medium", "high", "xhigh", "max"}, m.compose.ClaudeEffort), true
	case "scout-mode":
		return valueSuggestions(name, []string{"recon", "lint", "polish", "tests", "risk"}, m.compose.ScoutMode), true
	case "post-scout-mode":
		return valueSuggestions(name, []string{"recon", "lint", "polish", "tests", "risk"}, m.compose.PostScoutMode), true
	case "scout-context-policy":
		return valueSuggestions(name, []string{"warn", "skip", "block"}, m.compose.ScoutContextPolicy), true
	case "strict-scout":
		return valueSuggestions(name, []string{"on", "off"}, onOff(m.compose.StrictScout)), true
	case "scout-retrieval":
		return valueSuggestions(name, []string{"on", "off"}, onOff(m.compose.ScoutRetrieval)), true
	case "no-test":
		return valueSuggestions(name, []string{"on", "off"}, onOff(m.compose.NoTest)), true
	case "slice":
		return valueSuggestions(name, []string{"on", "off"}, onOff(m.compose.Slice)), true
	case "allow-dirty":
		return valueSuggestions(name, []string{"on", "off"}, onOff(m.compose.AllowDirty)), true
	case "repair-json":
		return valueSuggestions(name, []string{"on", "off"}, onOff(m.compose.RepairJSONWorker)), true
	case "rounds":
		values := []string{"1", "2", "3", "4", "6", "8"}
		return valueSuggestions(name, values, strconv.Itoa(m.compose.Rounds)), true
	case "timeout":
		return valueSuggestions(name, []string{"5m", "10m", "15m", "30m", "1h"}, m.compose.Timeout.String()), true
	case "watchdog-timeout":
		return valueSuggestions(name, []string{"1m", "2m", "5m", "10m"}, m.compose.WatchdogTimeout.String()), true
	case "focus":
		return valueSuggestions(name, []string{"compose", "detail", "runs"}, ""), true
	case "toggle":
		return valueSuggestions(name, []string{"plan", "findings", "artifacts", "test"}, ""), true
	case "watch":
		out := []slashCommand{choice("latest", "latest saved run"), choice("active", "currently running task")}
		for _, run := range m.runs {
			out = append(out, choice(run.RunID, shortMode(string(run.Mode))+" · "+statusBadge(run.Status)))
		}
		return out, true
	default:
		return nil, false
	}
}

type teamRole struct {
	Name    string
	Field   composeField
	Purpose string
	Kind    targetKind
}

func (m *model) teamRoles() []teamRole {
	switch m.compose.Mode {
	case tagteam.ModeSolo:
		return []teamRole{{Name: "model", Field: fieldEditor, Purpose: "writes code", Kind: targetEditor}}
	case tagteam.ModeSupervisor:
		return []teamRole{
			{Name: "worker", Field: fieldEditor, Purpose: "writes code", Kind: targetEditor},
			{Name: "supervisor", Field: fieldReviewer, Purpose: "plans + reviews · read-only", Kind: targetReviewer},
		}
	case tagteam.ModeRelay:
		return []teamRole{
			{Name: "coder", Field: fieldEditor, Purpose: "writes code", Kind: targetEditor},
			{Name: "supervisor", Field: fieldReviewer, Purpose: "plans + reviews · read-only", Kind: targetReviewer},
			{Name: "scout", Field: fieldScout, Purpose: "recon + advisory · read-only", Kind: targetScout},
		}
	case tagteam.ModeAdversarial:
		return []teamRole{
			{Name: "coder", Field: fieldEditor, Purpose: "writes code", Kind: targetEditor},
			{Name: "reviewer", Field: fieldReviewer, Purpose: "reviews + gates · read-only", Kind: targetReviewer},
		}
	default:
		return nil
	}
}

func (m *model) teamRole(name string) *teamRole {
	if strings.EqualFold(strings.TrimSpace(name), "adversary") {
		name = "reviewer"
	}
	for _, role := range m.teamRoles() {
		if role.Name == strings.ToLower(strings.TrimSpace(name)) {
			matched := role
			return &matched
		}
	}
	return nil
}

func isTeamRoleName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "model", "worker", "coder", "supervisor", "reviewer", "adversary", "scout":
		return true
	default:
		return false
	}
}

func (m *model) modelRoleSuggestions() []slashCommand {
	roles := m.teamRoles()
	out := make([]slashCommand, 0, len(roles))
	for _, role := range roles {
		out = append(out, slashCommand{
			Name:        "/model " + role.Name + " <target>",
			Description: role.Purpose + " · current " + m.composeFieldValue(role.Field),
		})
	}
	return out
}

func (m *model) setRoleTarget(role teamRole, value string) {
	switch role.Field {
	case fieldEditor:
		m.compose.EditorTarget = value
	case fieldReviewer:
		m.compose.ReviewerTarget = value
	case fieldScout:
		m.compose.ScoutTarget = value
	}
}

type targetKind int

const (
	targetEditor targetKind = iota
	targetReviewer
	targetScout
)

func (m *model) targetSuggestions(command, selected, description string, kind targetKind) []slashCommand {
	values := m.targetChoiceValues(selected, kind)
	out := make([]slashCommand, 0, len(values))
	for _, value := range values {
		adapter := strings.SplitN(value, ":", 2)[0]
		label := description + " · " + adapter
		if value == selected {
			label = "current · " + label
		}
		out = append(out, slashCommand{Name: "/" + command + " " + value, Description: label})
	}
	return out
}

func (m *model) targetChoiceValues(selected string, kind targetKind) []string {
	values := make([]string, 0, len(m.targetChoices)+1)
	if selected != "" {
		// Enter should preserve the current role target when a picker first opens.
		values = append(values, selected)
	}
	for _, value := range m.targetChoices {
		if value != selected {
			values = append(values, value)
		}
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		adapter := strings.SplitN(value, ":", 2)[0]
		if targetAllowedInPicker(adapter, kind) {
			out = append(out, value)
		}
	}
	return out
}

func targetAllowedInPicker(adapter string, kind targetKind) bool {
	switch kind {
	case targetEditor:
		return adapter != "openai-compatible" && adapter != "oai"
	case targetReviewer, targetScout:
		return adapter != "gosling"
	default:
		return true
	}
}

func valueSuggestions(command string, values []string, selected string) []slashCommand {
	out := make([]slashCommand, 0, len(values))
	for _, value := range values {
		description := "select"
		if value == selected {
			description = "current"
		}
		out = append(out, slashCommand{Name: "/" + command + " " + value, Description: description})
	}
	return out
}

func modeDescription(mode tagteam.Mode) string {
	switch mode {
	case tagteam.ModeSupervisor:
		return "worker + supervisor"
	case tagteam.ModeRelay:
		return "scout + coder + supervisor"
	case tagteam.ModeSolo:
		return "one implementation model"
	case tagteam.ModeAdversarial:
		return "coder + reviewer"
	default:
		return ""
	}
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
	selected, ok := m.selectedSlashCommand()
	if !ok {
		return
	}
	m.commandBuffer, _ = slashCommandCompletion(selected.Name)
	m.commandSelection = 0
}

func (m *model) selectedSlashCommand() (slashCommand, bool) {
	matches := m.matchingSlashCommands()
	if len(matches) == 0 {
		return slashCommand{}, false
	}
	selection := m.commandSelection
	if selection < 0 || selection >= len(matches) {
		selection = 0
	}
	return matches[selection], true
}

func shouldAcceptSlashSelection(buffer string, selected slashCommand) bool {
	raw := strings.TrimSpace(buffer)
	if raw == "" || strings.Contains(buffer, " ") {
		return true
	}
	return !strings.EqualFold(raw, strings.TrimPrefix(selected.Name, "/"))
}

func slashCommandCompletion(raw string) (string, bool) {
	name := strings.TrimPrefix(raw, "/")
	if marker := strings.Index(name, "<"); marker >= 0 {
		return strings.TrimSpace(name[:marker]) + " ", true
	}
	parts := strings.Fields(name)
	if len(parts) > 1 && strings.Contains(parts[len(parts)-1], "|") {
		return strings.Join(parts[:len(parts)-1], " ") + " ", true
	}
	return name, false
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
	fields := []composeField{}
	if m.compose.Mode == tagteam.ModeRelay {
		fields = append(fields, fieldScoutMode, fieldPostScoutMode, fieldStrictScout, fieldScoutRetrieval, fieldScoutContextPolicy)
	}
	fields = append(fields, fieldAllowedPaths, fieldRounds, fieldTimeout, fieldWatchdogTimeout, fieldTest, fieldLint, fieldNoTest)
	if m.compose.Mode == tagteam.ModeSupervisor {
		fields = append(fields, fieldSlice)
	}
	fields = append(fields, fieldAllowDirty, fieldRepairJSON)
	return fields
}

func (m *model) teamFields() []composeField {
	fields := []composeField{fieldProfile, fieldMode}
	for _, role := range m.teamRoles() {
		fields = append(fields, role.Field)
	}
	return append(fields, fieldCodexEffort, fieldClaudeEffort)
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
	case fieldAllowedPaths:
		return strings.Join(m.compose.AllowedPaths, ", ")
	case fieldTimeout:
		return m.compose.Timeout.String()
	case fieldWatchdogTimeout:
		return m.compose.WatchdogTimeout.String()
	case fieldTest:
		return m.compose.TestCmd
	case fieldLint:
		return m.compose.LintCmd
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
	case fieldAllowedPaths:
		m.compose.AllowedPaths = parseAllowedPaths(value)
	case fieldTimeout:
		m.applyDurationEditorValue("timeout", value, &m.compose.Timeout)
	case fieldWatchdogTimeout:
		m.applyDurationEditorValue("watchdog-timeout", value, &m.compose.WatchdogTimeout)
	case fieldTest:
		m.compose.TestCmd = value
	case fieldLint:
		m.compose.LintCmd = value
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
		m.compose.EditorTarget = cycleString(m.targetChoiceValues(m.compose.EditorTarget, targetEditor), m.compose.EditorTarget, delta)
	case fieldReviewer:
		m.compose.ReviewerTarget = cycleString(m.targetChoiceValues(m.compose.ReviewerTarget, targetReviewer), m.compose.ReviewerTarget, delta)
	case fieldScout:
		m.compose.ScoutTarget = cycleString(m.targetChoiceValues(m.compose.ScoutTarget, targetScout), m.compose.ScoutTarget, delta)
	case fieldCodexEffort:
		m.compose.CodexEffort = cycleString([]string{"none", "minimal", "low", "medium", "high", "xhigh"}, m.compose.CodexEffort, delta)
	case fieldClaudeEffort:
		m.compose.ClaudeEffort = cycleString([]string{"low", "medium", "high", "xhigh", "max"}, m.compose.ClaudeEffort, delta)
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
	case fieldTimeout:
		m.compose.Timeout = cycleDuration([]time.Duration{5 * time.Minute, 10 * time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour}, m.compose.Timeout, delta)
	case fieldWatchdogTimeout:
		m.compose.WatchdogTimeout = cycleDuration([]time.Duration{time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute}, m.compose.WatchdogTimeout, delta)
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
		CodexEffort:        m.cfg.Adapters.Codex.ReasoningEffort,
		ClaudeEffort:       m.cfg.Adapters.Claude.Effort,
		ScoutMode:          opts.ScoutMode,
		PostScoutMode:      opts.PostScoutMode,
		StrictScout:        opts.ScoutFailurePolicy == "fail",
		ScoutRetrieval:     opts.ScoutRetrieval,
		ScoutContextPolicy: opts.ScoutContextPolicy,
		AllowedPaths:       append([]string(nil), opts.AllowedPaths...),
		Rounds:             opts.Rounds,
		Timeout:            opts.Timeout,
		WatchdogTimeout:    opts.WatchdogTimeout,
		TestCmd:            opts.TestCmd,
		LintCmd:            opts.LintCmd,
		NoTest:             opts.NoTest,
		Slice:              opts.SupervisorSlicing,
		AllowDirty:         opts.AllowDirty,
		RepairJSONWorker:   opts.JSONRepair == "worker",
	}
}

func (m *model) applyDurationEditorValue(label, raw string, destination *time.Duration) {
	value, err := parsePositiveDuration(label, raw)
	if err != nil {
		m.statusMessage = err.Error()
		return
	}
	*destination = value
}

func parseAllowedPaths(raw string) []string {
	seen := map[string]bool{}
	paths := []string{}
	for _, value := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '\n' }) {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		paths = append(paths, value)
	}
	return paths
}

func cycleDuration(values []time.Duration, current time.Duration, delta int) time.Duration {
	labels := make([]string, len(values))
	for index, value := range values {
		labels[index] = value.String()
	}
	next := cycleString(labels, current.String(), delta)
	parsed, _ := time.ParseDuration(next)
	return parsed
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
	m.resetTeamAssignments()
	m.compose.Prompt = prompt
	m.clampSelection()
	if flags.Profile == "" {
		m.statusMessage = "profile cleared"
	} else {
		m.statusMessage = fmt.Sprintf("profile %s applied", flags.Profile)
	}
	return nil
}
