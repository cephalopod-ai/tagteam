package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/cephalopod-ai/tagteam/internal/tagteam"
)

func fixtureModel() *model {
	updated := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	return &model{
		workdir: "/repo",
		width:   120,
		height:  48,
		focus:   focusCompose,
		compose: composeState{
			Mode:           tagteam.ModeSupervisor,
			EditorTarget:   "agy:Gemini 3.5 Flash (High)",
			ReviewerTarget: "claude:opus",
			Prompt:         "add OAuth login",
			Rounds:         2,
			Slice:          true,
		},
		profileChoices: []string{"relay", "claude-failover"},
		runs: []runListItem{
			{RunID: "run-2", RunDir: "/repo/.tagteam/runs/run-2", Mode: tagteam.ModeSupervisor, Status: "running", Verdict: "needs_changes", UpdatedAt: updated, Active: true},
			{RunID: "run-1", RunDir: "/repo/.tagteam/runs/run-1", Mode: tagteam.ModeSolo, Status: "passed", Verdict: "done", UpdatedAt: updated.Add(-time.Hour)},
		},
		currentRunDir: "/repo/.tagteam/runs/run-2",
		currentSnapshot: &tagteam.RunSnapshot{
			RunID:           "run-2",
			RunDir:          "/repo/.tagteam/runs/run-2",
			Mode:            tagteam.ModeSupervisor,
			Status:          "running",
			Phase:           "review",
			Verdict:         "needs_changes",
			UpdatedAt:       updated,
			CurrentRound:    1,
			RoundsCompleted: 0,
			RoundsRequested: 2,
			RoleStatuses: map[string]tagteam.RoleStatus{
				"worker":     {Role: "worker", Status: "completed", Adapter: "agy", Model: "Gemini 3.5 Flash (High)"},
				"supervisor": {Role: "supervisor", Status: "running", Adapter: "claude", Model: "opus"},
			},
			LatestDiffPath:   "/repo/.tagteam/runs/run-2/diff-round-1.patch",
			LatestReviewPath: "/repo/.tagteam/runs/run-2/supervisor-round-1.json",
			LatestTestPath:   "/repo/.tagteam/runs/run-2/test-round-1.txt",
			ChangedFiles:     []string{"main.go", "README.md"},
			FindingsCount:    2,
		},
		currentPlan: &tagteam.ExecutionPlan{
			RunID:  "run-2",
			Status: "running",
			Items: []tagteam.PlanItem{
				{ID: "P1", Title: "Add login flow", Status: tagteam.PlanStatusInProgress},
				{ID: "P2", Title: "Add tests", Status: tagteam.PlanStatusPending},
			},
		},
		currentReview: &tagteam.Review{
			Summary: "Fix the failing edge case.",
			Findings: []tagteam.Finding{
				{Severity: "major", File: "main.go", Line: 42, Issue: "nil check missing"},
				{Severity: "minor", File: "README.md", Line: 12, Issue: "docs drift"},
			},
		},
		currentTestTail: []string{"  FAIL TestLoginFlow", "  expected 200 got 500"},
		showPlan:        true,
		showFindings:    true,
		showArtifacts:   true,
		showTestOutput:  true,
	}
}

func TestRenderDashboardIncludesCorePanels(t *testing.T) {
	out := renderDashboard(fixtureModel())
	for _, want := range []string{
		"tagteam  repo",
		"mode supervisor",
		"watching run-2 [run]",
		"draft supervisor  target=agy:Gemini 3.5 Flash (High)",
		"> add OAuth login",
		"Run: run-2",
		"Mode: supervisor",
		"Roles:",
		"Plan (running, 2 items):",
		"Review summary: Fix the failing edge case.",
		"Changed files (2):",
		"Enter edit  / commands",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("render output missing %q\nfull output:\n%s", want, out)
		}
	}
}

func TestRenderDashboardNoSelectedRunShowsComposeHint(t *testing.T) {
	m := fixtureModel()
	m.currentSnapshot = nil
	m.currentPlan = nil
	m.currentReview = nil
	m.currentTestTail = nil
	m.currentRunDir = ""
	out := renderDashboard(m)
	for _, want := range []string{
		"Ready for a new task.",
		"Type in the composer below to start a run.",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("render output missing %q\nfull output:\n%s", want, out)
		}
	}
}

func TestRenderDashboardSettingsOverlay(t *testing.T) {
	m := fixtureModel()
	m.settingsOpen = true
	out := renderDashboard(m)
	for _, want := range []string{
		"Settings",
		"Profiles: off",
		"mode: supervisor",
		"worker: agy:Gemini 3.5 Flash (High)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("render output missing %q\nfull output:\n%s", want, out)
		}
	}
}

func TestRenderDashboardCommandOverlay(t *testing.T) {
	m := fixtureModel()
	m.commandMode = true
	m.commandBuffer = ""
	out := renderDashboard(m)
	for _, want := range []string{
		"Commands",
		"/profile <name>",
		"/model",
		"Set the primary editor/worker target",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("render output missing %q\nfull output:\n%s", want, out)
		}
	}
}

func TestRenderDashboardUsesDetailScrollOffset(t *testing.T) {
	m := fixtureModel()
	m.height = 18
	m.scroll = 6
	out := renderDashboard(m)
	if strings.Contains(out, "Run: run-2") {
		t.Fatalf("detail viewport ignored scroll offset:\n%s", out)
	}
	if !strings.Contains(out, "Roles:") {
		t.Fatalf("detail viewport did not advance to later content:\n%s", out)
	}
}

func TestRenderDashboardBoundsOverlaysToTerminalHeight(t *testing.T) {
	m := fixtureModel()
	m.height = 24
	m.commandMode = true
	out := renderDashboard(m)
	if got := len(strings.Split(strings.TrimSuffix(out, "\n"), "\n")); got > m.height {
		t.Fatalf("command overlay rendered %d rows in a %d-row terminal:\n%s", got, m.height, out)
	}
	if !strings.Contains(out, "tab complete") {
		t.Fatalf("command overlay missing completion hint:\n%s", out)
	}
}

func TestRenderDashboardKeepsSelectedSettingVisible(t *testing.T) {
	m := fixtureModel()
	m.height = 24
	m.compose.Mode = tagteam.ModeRelay
	m.settingsOpen = true
	m.selectedField = len(m.visibleFields()) - 1
	out := renderDashboard(m)
	if !strings.Contains(out, "> repair-json:") {
		t.Fatalf("selected off-screen setting was not rendered:\n%s", out)
	}
}

func TestRenderDashboardTrimsLongDetailLines(t *testing.T) {
	m := fixtureModel()
	m.width = 40
	m.currentReview.Findings[0].Issue = strings.Repeat("long finding ", 12)
	out := renderDashboard(m)
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if width := len([]rune(line)); width > m.width {
			t.Fatalf("rendered line has width %d, want <= %d: %q", width, m.width, line)
		}
	}
}

func TestDecodeKeyEventsParsesArrowsAndText(t *testing.T) {
	events := decodeKeyEvents([]byte("\x1b[A/x"))
	if len(events) != 3 {
		t.Fatalf("events len = %d, want 3", len(events))
	}
	if events[0].Kind != keyUp {
		t.Fatalf("first event = %#v, want keyUp", events[0])
	}
	if events[1].Kind != keyRune || events[1].Rune != '/' {
		t.Fatalf("second event = %#v, want slash rune", events[1])
	}
	if events[2].Kind != keyRune || events[2].Rune != 'x' {
		t.Fatalf("third event = %#v, want x rune", events[2])
	}
}
