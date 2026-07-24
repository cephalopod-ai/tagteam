package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cephalopod-ai/tagteam/internal/tagteam"
)

func (m *model) settingsLines() []string {
	fields := m.visibleFields()
	lines := make([]string, 0, len(fields)+4)
	lines = append(lines, "Execution policy only. Left/right chooses. Enter edits. Esc closes.")
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

func (m *model) teamLines() []string {
	fields := m.teamFields()
	lines := make([]string, 0, len(fields)+2)
	lines = append(lines, "Build the team: choose a workflow, then assign each role. Esc closes.")
	roleByField := make(map[composeField]teamRole, len(m.teamRoles()))
	for _, role := range m.teamRoles() {
		roleByField[role.Field] = role
	}
	for index, field := range fields {
		prefix := "  "
		if m.teamOpen && m.selectedTeam == index {
			prefix = "> "
		}
		label := composeFieldLabel(m.compose.Mode, field)
		if role, ok := roleByField[field]; ok {
			label = role.Name + " [" + role.Purpose + "]"
		}
		value := m.composeFieldValue(field)
		if m.editor.Active && m.editor.Field == field {
			value = m.editor.Buffer + " _"
		}
		lines = append(lines, prefix+label+": "+value)
	}
	return lines
}

func (m *model) composerLines(width int) []string {
	lines := []string{}
	switch {
	case m.commandMode:
		prompt := strings.TrimSpace(m.compose.Prompt)
		if prompt == "" {
			prompt = "Describe the task..."
		}
		lines = append(lines, "> "+padOrTrim(prompt, maxInt(20, width-4)))
	case m.editor.Active && m.editor.Field == fieldPrompt:
		lines = append(lines, "> "+m.editor.Buffer+"_")
	default:
		prompt := strings.TrimSpace(m.compose.Prompt)
		if prompt == "" {
			prompt = "Describe the task..."
		}
		lines = append(lines, "> "+padOrTrim(prompt, maxInt(20, width-4)))
	}
	scope := "required"
	if len(m.compose.AllowedPaths) > 0 {
		scope = fmt.Sprintf("%d %s", len(m.compose.AllowedPaths), pluralWord(len(m.compose.AllowedPaths), "path", "paths"))
	} else if m.compose.Mode == tagteam.ModeSupervisor && m.compose.Slice {
		scope = "from plan"
	}
	lines = append(lines, fmt.Sprintf("mode=%s  profile=%s  rounds=%d  scope=%s  tests=%s  dirty=%s", m.compose.Mode, profileLabel(m.compose.Profile), m.compose.Rounds, scope, onOff(!m.compose.NoTest), onOff(m.compose.AllowDirty)))
	footer := "Enter edit  m team  / commands  s settings  u runs"
	if m.currentSnapshot != nil {
		footer += "  p/f/a/t detail"
	}
	lines = append(lines, footer)
	return lines
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
	case fieldCodexEffort:
		return dashIfEmpty(m.compose.CodexEffort)
	case fieldClaudeEffort:
		return dashIfEmpty(m.compose.ClaudeEffort)
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
	case fieldAllowedPaths:
		if len(m.compose.AllowedPaths) == 0 {
			return "(required unless supervisor slicing supplies scope)"
		}
		return strings.Join(m.compose.AllowedPaths, ", ")
	case fieldRounds:
		return strconv.Itoa(m.compose.Rounds)
	case fieldTimeout:
		return m.compose.Timeout.String()
	case fieldWatchdogTimeout:
		return m.compose.WatchdogTimeout.String()
	case fieldTest:
		if m.compose.NoTest {
			return "(disabled)"
		}
		return dashIfEmpty(m.compose.TestCmd)
	case fieldLint:
		return dashIfEmpty(m.compose.LintCmd)
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
	case fieldCodexEffort:
		return "codex effort"
	case fieldClaudeEffort:
		return "claude effort"
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
	case fieldAllowedPaths:
		return "allowed paths"
	case fieldRounds:
		return "rounds"
	case fieldTimeout:
		return "invocation timeout"
	case fieldWatchdogTimeout:
		return "no-progress timeout"
	case fieldTest:
		return "test"
	case fieldLint:
		return "lint"
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

	for _, model := range tagteam.AgyGemini36FlashModelChoices() {
		add(withModel("agy", model))
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
		withModel("grok", cfg.Adapters.Grok.DefaultModel),
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

func (m *model) validateTargetForKind(label, raw string, kind targetKind) (string, error) {
	value, err := validateTargetWord(label, raw)
	if err != nil {
		return "", err
	}
	target, err := tagteam.ParseRoleTarget(value)
	if err != nil {
		return "", err
	}
	if err := tagteam.ValidateRoleTarget(m.roleForTargetKind(kind), target); err != nil {
		return "", err
	}
	return value, nil
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
	case fieldPrompt, fieldProfile, fieldEditor, fieldReviewer, fieldScout, fieldAllowedPaths, fieldTimeout, fieldWatchdogTimeout, fieldTest, fieldLint:
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

func parsePositiveDuration(label, raw string) (time.Duration, error) {
	value, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration such as 5m or 1h", label)
	}
	return value, nil
}

func pluralWord(count int, singular, plural string) string {
	if count == 1 {
		return singular
	}
	return plural
}

func validateTargetWord(label, raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("%s requires adapter[:model]; type /%s then Space to choose", label, label)
	}
	if _, err := tagteam.ParseRoleTarget(value); err != nil {
		return "", fmt.Errorf("invalid %s target: %w", label, err)
	}
	return value, nil
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
