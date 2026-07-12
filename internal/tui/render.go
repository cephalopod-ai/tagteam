package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cephalopod-ai/tagteam/internal/tagteam"
)

type slashCommand struct {
	Name        string
	Description string
}

func renderDashboard(model *model) string {
	topLines := renderTopCard(model)
	composerLines := renderComposerSection(model)
	overlayBudget := maxInt(0, model.height-len(topLines)-len(composerLines)-1-3)
	overlayLines := renderOverlayLines(model, overlayBudget)
	mainHeight := maxInt(0, model.height-len(topLines)-len(overlayLines)-len(composerLines)-1)

	mainLines := renderMainLines(model)
	scroll := model.scroll
	if model.currentSnapshot == nil {
		scroll = 0
	}
	lines := make([]string, 0, model.height)
	lines = append(lines, topLines...)
	lines = append(lines, viewport(mainLines, scroll, mainHeight)...)
	lines = append(lines, overlayLines...)
	lines = append(lines, composerLines...)
	lines = append(lines, renderStatus(model))
	for index, line := range lines {
		lines[index] = padOrTrim(line, model.width)
	}
	if model.height > 0 && len(lines) > model.height {
		lines = lines[:model.height]
	}
	return strings.Join(lines, "\n") + "\n"
}

func renderTopCard(model *model) []string {
	dir := filepath.Base(model.workdir)
	versionLine := fmt.Sprintf("> tagteam  %s", dir)
	pathLine := shortenPath(model.workdir, model.width)
	lines := []string{versionLine}
	lines = append(lines, roleSummaryLines(model, model.width)...)
	lines = append(lines, pathLine)
	if model.currentSnapshot != nil {
		watching := fmt.Sprintf("watching %s [%s]", shortRunLabel(model.currentSnapshot.RunID), statusBadge(model.currentSnapshot.Status))
		if live := model.currentSnapshot.LiveProgress; live != nil && model.currentSnapshot.Status == "running" {
			watching += fmt.Sprintf(" · %s %s", dashIfEmpty(string(live.Role)), liveStatusLabel(live))
			if live.NoProgressFor != "" {
				watching += " · idle " + live.NoProgressFor
			}
		}
		lines = append(lines, watching)
	} else if len(model.runs) > 0 {
		lines = append(lines, fmt.Sprintf("latest %s [%s] | press u for recent runs", shortRunLabel(model.runs[0].RunID), statusBadge(model.runs[0].Status)))
	}
	lines = append(lines, "")
	return lines
}

func liveStatusLabel(live *tagteam.LiveProgress) string {
	if live == nil {
		return "-"
	}
	if live.Status != "waiting" || live.WaitingFor == "" {
		return dashIfEmpty(live.Status)
	}
	label := "queued for " + live.WaitingFor
	if live.HolderPID > 0 {
		label += fmt.Sprintf(" (pid %d)", live.HolderPID)
	}
	return label
}

func roleSummaryLines(model *model, width int) []string {
	line := roleSummaryLine(model)
	if len([]rune(line)) <= width {
		return []string{line}
	}
	switch model.compose.Mode {
	case tagteam.ModeSupervisor:
		return []string{
			"supervisor  worker " + dashIfEmpty(model.compose.EditorTarget),
			"            supervisor " + dashIfEmpty(model.compose.ReviewerTarget),
		}
	case tagteam.ModeRelay:
		secondary := "       supervisor " + dashIfEmpty(model.compose.ReviewerTarget) + " | scout " + dashIfEmpty(model.compose.ScoutTarget)
		if len([]rune(secondary)) <= width {
			return []string{"relay  coder " + dashIfEmpty(model.compose.EditorTarget), secondary}
		}
		return []string{
			"relay  coder " + dashIfEmpty(model.compose.EditorTarget),
			"       supervisor " + dashIfEmpty(model.compose.ReviewerTarget),
			"       scout " + dashIfEmpty(model.compose.ScoutTarget),
		}
	case tagteam.ModeAdversarial:
		return []string{
			"adversarial  coder " + dashIfEmpty(model.compose.EditorTarget),
			"             reviewer " + dashIfEmpty(model.compose.ReviewerTarget),
		}
	default:
		return []string{line}
	}
}

func roleSummaryLine(model *model) string {
	switch model.compose.Mode {
	case tagteam.ModeSolo:
		return "solo  model " + dashIfEmpty(model.compose.EditorTarget)
	case tagteam.ModeSupervisor:
		return "supervisor  worker " + dashIfEmpty(model.compose.EditorTarget) + " | supervisor " + dashIfEmpty(model.compose.ReviewerTarget)
	case tagteam.ModeRelay:
		return "relay  coder " + dashIfEmpty(model.compose.EditorTarget) + " | supervisor " + dashIfEmpty(model.compose.ReviewerTarget) + " | scout " + dashIfEmpty(model.compose.ScoutTarget)
	case tagteam.ModeAdversarial:
		return "adversarial  coder " + dashIfEmpty(model.compose.EditorTarget) + " | reviewer " + dashIfEmpty(model.compose.ReviewerTarget)
	default:
		return string(model.compose.Mode)
	}
}

func renderMainLines(model *model) []string {
	if model.currentSnapshot == nil {
		return renderEmptySessionLines(model)
	}
	return model.detailLines()
}

func renderEmptySessionLines(model *model) []string {
	lines := []string{
		"",
		"Ready for a new task.",
		"",
		"Enter a task below. Use / only when you need commands.",
	}
	if len(model.runs) > 0 {
		lines = append(lines, "")
		lines = append(lines, fmt.Sprintf("Latest saved run: %s  %s  %s", shortRunLabel(model.runs[0].RunID), shortMode(string(model.runs[0].Mode)), statusBadge(model.runs[0].Status)))
	}
	return lines
}

func renderStatus(model *model) string {
	status := strings.TrimSpace(model.statusMessage)
	if status == "" {
		status = "? for shortcuts"
	}
	return padOrTrim(status, model.width)
}

func renderOverlayLines(model *model, maxHeight int) []string {
	if maxHeight < 5 {
		return nil
	}
	width := minInt(maxInt(8, model.width-4), 88)
	contentHeight := maxInt(1, maxHeight-4)
	switch {
	case model.commandMode:
		content := renderSlashCommandLines(model, contentHeight, width-2)
		lines := []string{""}
		lines = append(lines, box("Commands", width, len(content)+2, content)...)
		lines = append(lines, "")
		return lines
	case model.teamOpen:
		team := model.teamLines()
		selectedLine := model.selectedTeam + 1
		content := trimBlankTail(viewport(team, viewportStart(len(team), selectedLine, contentHeight), contentHeight))
		lines := []string{""}
		lines = append(lines, box("Team · "+string(model.compose.Mode), minInt(width, 96), len(content)+2, content)...)
		lines = append(lines, "")
		return lines
	case model.settingsOpen:
		settings := model.settingsLines()
		selectedLine := model.selectedField + 1
		content := trimBlankTail(viewport(settings, viewportStart(len(settings), selectedLine, contentHeight), contentHeight))
		lines := []string{""}
		lines = append(lines, box("Settings", minInt(width, 96), len(content)+2, content)...)
		lines = append(lines, "")
		return lines
	case model.runsOpen:
		runs := renderRunPickerLines(model)
		content := trimBlankTail(viewport(runs, viewportStart(len(runs), model.selectedRun, contentHeight), contentHeight))
		lines := []string{""}
		lines = append(lines, box("Runs", minInt(width, 72), len(content)+2, content)...)
		lines = append(lines, "")
		return lines
	default:
		return nil
	}
}

func trimBlankTail(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func renderComposerSection(model *model) []string {
	lines := viewport(model.composerLines(model.width), 0, composerPaneHeight(model.height)-1)
	out := []string{strings.Repeat("─", model.width)}
	for _, line := range lines {
		out = append(out, padOrTrim(line, model.width))
	}
	return out
}

func renderSlashCommandLines(model *model, height, width int) []string {
	if height <= 0 {
		return nil
	}
	matches := model.matchingSlashCommands()
	selection := model.commandSelection
	if selection < 0 || selection >= len(matches) {
		selection = 0
	}
	lines := []string{"/" + model.commandBuffer + "_"}
	listHeight := maxInt(1, height-2)
	start := viewportStart(len(matches), selection, listHeight)
	nameWidth := 18
	for _, command := range matches {
		nameWidth = maxInt(nameWidth, len([]rune(command.Name))+1)
	}
	nameWidth = minInt(nameWidth, maxInt(18, width-28))
	for index, command := range matches[start:minInt(len(matches), start+listHeight)] {
		prefix := "  "
		if start+index == selection {
			prefix = "> "
		}
		lines = append(lines, prefix+padOrTrim(command.Name, nameWidth)+" "+command.Description)
	}
	if len(matches) == 0 {
		lines = append(lines, "  no matching commands")
	}
	lines = append(lines, "↑/↓ select  tab complete  enter submit  esc cancel")
	if len(lines) > height {
		return lines[:height]
	}
	return lines
}

func slashCommands() []slashCommand {
	return []slashCommand{
		{Name: "/run", Description: "Launch the current draft"},
		{Name: "/exit", Description: "Exit the TUI"},
		{Name: "/quit", Description: "Exit the TUI"},
		{Name: "/team", Description: "Choose orchestration mode and assign role models"},
		{Name: "/model <role> <target>", Description: "Choose a role, then assign its model"},
		{Name: "/profile <name>", Description: "Apply a named profile or /profile off"},
		{Name: "/mode <mode>", Description: "Switch between supervisor, relay, solo, adversarial"},
		{Name: "/worker <target>", Description: "Set the worker target for solo/supervisor/relay"},
		{Name: "/coder <target>", Description: "Set the coder target for relay/adversarial"},
		{Name: "/supervisor <target>", Description: "Set the supervisor target"},
		{Name: "/reviewer <target>", Description: "Set the adversarial reviewer target"},
		{Name: "/scout <target>", Description: "Set the relay scout target"},
		{Name: "/codex-effort <level>", Description: "Set Codex reasoning effort"},
		{Name: "/claude-effort <level>", Description: "Set Claude effort"},
		{Name: "/settings", Description: "Open execution policy settings"},
		{Name: "/scout-mode <mode>", Description: "Set relay pre-scout mode: recon/lint/polish/tests/risk"},
		{Name: "/post-scout-mode <mode>", Description: "Set relay post-scout mode"},
		{Name: "/strict-scout on|off", Description: "Fail relay when scout execution fails"},
		{Name: "/scout-retrieval on|off", Description: "Enable or disable relay recon retrieval"},
		{Name: "/scout-context-policy <policy>", Description: "Set relay scout context policy: warn/skip/block"},
		{Name: "/allow-path <paths>", Description: "Set comma-separated repo-relative write scope"},
		{Name: "/rounds <n>", Description: "Set the run round limit"},
		{Name: "/timeout <duration>", Description: "Set the per-invocation timeout"},
		{Name: "/watchdog-timeout <duration>", Description: "Set the no-progress cancellation timeout"},
		{Name: "/test <cmd>", Description: "Set the test command"},
		{Name: "/lint <cmd>", Description: "Set the lint command"},
		{Name: "/no-test on|off", Description: "Enable or disable tests"},
		{Name: "/slice on|off", Description: "Enable or disable supervisor slicing"},
		{Name: "/allow-dirty on|off", Description: "Review the cumulative dirty diff against HEAD"},
		{Name: "/repair-json on|off", Description: "Enable worker JSON repair fallback"},
		{Name: "/prompt <text>", Description: "Set the draft prompt directly"},
		{Name: "/profiles", Description: "List available named profiles"},
		{Name: "/runs", Description: "Browse recent runs"},
		{Name: "/watch <run>", Description: "Open active, latest, or a saved run"},
		{Name: "/refresh", Description: "Reload runs and snapshots from disk"},
		{Name: "/focus <area>", Description: "Focus runs, compose, or detail"},
		{Name: "/toggle plan", Description: "Show or hide plan details"},
		{Name: "/toggle findings", Description: "Show or hide findings"},
		{Name: "/toggle artifacts", Description: "Show or hide artifacts"},
		{Name: "/toggle test", Description: "Show or hide test output"},
		{Name: "/help", Description: "Show command help"},
	}
}

func renderRunPickerLines(model *model) []string {
	if len(model.runs) == 0 {
		return []string{"No saved runs."}
	}
	lines := make([]string, 0, len(model.runs))
	for index, run := range model.runs {
		prefix := "  "
		if index == model.selectedRun {
			prefix = "> "
		}
		lines = append(lines, fmt.Sprintf("%s%-4s %-3s %s", prefix, statusBadge(run.Status), shortMode(string(run.Mode)), shortRunLabel(run.RunID)))
	}
	return lines
}

func sectionPane(title string, width, height int, content []string) []string {
	if width < 8 {
		width = 8
	}
	if height < 2 {
		height = 2
	}
	lines := make([]string, 0, height)
	lines = append(lines, padOrTrim(title+" "+strings.Repeat("─", maxInt(0, width-len([]rune(title))-1)), width))
	for i := 0; i < height-1; i++ {
		text := ""
		if i < len(content) {
			text = content[i]
		}
		lines = append(lines, padOrTrim(text, width))
	}
	return lines
}

func box(title string, width, height int, content []string) []string {
	if width < 8 {
		width = 8
	}
	if height < 3 {
		height = 3
	}
	lines := make([]string, 0, height)
	top := "+" + padOrTrim(" "+title+" ", width-2, '-') + "+"
	lines = append(lines, top)
	for i := 0; i < height-2; i++ {
		text := ""
		if i < len(content) {
			text = content[i]
		}
		lines = append(lines, "|"+padOrTrim(text, width-2)+"|")
	}
	lines = append(lines, "+"+strings.Repeat("-", width-2)+"+")
	return lines
}

func overlayBox(base []string, overlay []string, x, y int) []string {
	out := append([]string(nil), base...)
	for row := 0; row < len(overlay); row++ {
		targetRow := y + row
		if targetRow < 0 || targetRow >= len(out) {
			continue
		}
		line := []rune(padOrTrim(out[targetRow], maxInt(len([]rune(out[targetRow])), x+len([]rune(overlay[row])))))
		patch := []rune(overlay[row])
		for col := 0; col < len(patch) && x+col < len(line); col++ {
			line[x+col] = patch[col]
		}
		out[targetRow] = string(line)
	}
	return out
}

func viewport(lines []string, scroll, height int) []string {
	if height <= 0 {
		return nil
	}
	if scroll < 0 {
		scroll = 0
	}
	if scroll > len(lines) {
		scroll = len(lines)
	}
	end := scroll + height
	if end > len(lines) {
		end = len(lines)
	}
	window := append([]string(nil), lines[scroll:end]...)
	for len(window) < height {
		window = append(window, "")
	}
	return window
}

func viewportStart(total, selected, height int) int {
	if total <= height || height <= 0 {
		return 0
	}
	if selected < 0 {
		selected = 0
	}
	if selected >= total {
		selected = total - 1
	}
	start := selected - height/2
	if start < 0 {
		return 0
	}
	if maxStart := total - height; start > maxStart {
		return maxStart
	}
	return start
}

func padOrTrim(s string, width int, fill ...rune) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) > width {
		if width <= 1 {
			return string(runes[:width])
		}
		return string(runes[:width-1]) + ">"
	}
	pad := ' '
	if len(fill) > 0 {
		pad = fill[0]
	}
	return s + strings.Repeat(string(pad), width-len(runes))
}

func composerPaneHeight(totalHeight int) int {
	return 4
}

func shortRunLabel(runID string) string {
	if ts, err := parseRunTime(runID); err == nil {
		return ts.Format("Jan02 15:04")
	}
	if len(runID) > 16 {
		return runID[len(runID)-16:]
	}
	return runID
}

func parseRunTime(runID string) (time.Time, error) {
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02T150405.000000000Z07:00",
		"2006-01-02T150405.000000000Z",
	} {
		if ts, err := time.Parse(layout, runID); err == nil {
			return ts, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized run id time")
}

func statusBadge(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "running":
		return "run"
	case "passed", "finished":
		return "ok"
	case "degraded":
		return "warn"
	case "blocked":
		return "hold"
	case "failed", "error":
		return "fail"
	default:
		return "idle"
	}
}

func shortMode(mode string) string {
	switch mode {
	case "supervisor":
		return "sup"
	case "adversarial":
		return "adv"
	case "relay":
		return "rly"
	case "solo":
		return "sol"
	default:
		return "-"
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func shortenPath(path string, width int) string {
	if width <= 0 {
		return path
	}
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, home) {
		path = "~" + strings.TrimPrefix(path, home)
	}
	maxWidth := maxInt(24, width-2)
	runes := []rune(path)
	if len(runes) <= maxWidth {
		return path
	}
	return "..." + string(runes[len(runes)-maxWidth+3:])
}
