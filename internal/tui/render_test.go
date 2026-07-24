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
			Mode:            tagteam.ModeSupervisor,
			EditorTarget:    "agy:gemini-3.6-flash-high",
			ReviewerTarget:  "claude:opus",
			Prompt:          "add OAuth login",
			AllowedPaths:    []string{"internal/"},
			Rounds:          2,
			Timeout:         15 * time.Minute,
			WatchdogTimeout: 5 * time.Minute,
			LintCmd:         "go vet ./...",
			Slice:           true,
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
				"worker":     {Role: "worker", Status: "completed", Adapter: "agy", Model: "gemini-3.6-flash-high"},
				"supervisor": {Role: "supervisor", Status: "running", Adapter: "claude", Model: "opus"},
			},
			LiveProgress:     &tagteam.LiveProgress{Role: tagteam.RoleSupervisor, Status: "running", Elapsed: "1m10s", NoProgressFor: "12s", FilesChanged: 2, Additions: 14, Deletions: 3},
			PreexistingFiles: []string{"already-dirty.go"},
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
		"supervisor  worker agy:gemini-3.6-flash-high",
		"watching run-2 [run]",
		"supervisor running · idle 12s",
		"> add OAuth login",
		"Run: run-2",
		"Mode: supervisor",
		"Roles:",
		"Activity: supervisor · running · elapsed 1m10s · idle 12s",
		"Working diff: 2 files (+14 -3)",
		"Baseline: cumulative dirty worktree (1 pre-existing file)",
		"Plan (running, 2 items):",
		"Review summary: Fix the failing edge case.",
		"Changed files (2):",
		"Enter edit  m team  / commands",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("render output missing %q\nfull output:\n%s", want, out)
		}
	}
}

func TestRenderDashboardShowsSerializedInvocationWait(t *testing.T) {
	m := fixtureModel()
	m.currentSnapshot.Phase = "implementing"
	m.currentSnapshot.RoundsRequested = 0
	m.currentSnapshot.LiveProgress = &tagteam.LiveProgress{
		Role:       tagteam.Role("worker"),
		Status:     "waiting",
		WaitingFor: "claude",
		HolderPID:  4242,
		Elapsed:    "42s",
	}
	out := renderDashboard(m)
	for _, want := range []string{
		"worker queued for claude (pid 4242)",
		"Activity: worker · queued for claude (pid 4242) · elapsed 42s",
		"status=waiting",
		"Round: 1",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("render output missing %q\nfull output:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Rounds: 0/0") {
		t.Fatalf("active run rendered an unknown round budget as 0/0:\n%s", out)
	}
}

func TestRenderDashboardShowsQualityGateBlockers(t *testing.T) {
	m := fixtureModel()
	m.currentSnapshot.BlockingReason = "blocking_findings"
	m.currentSnapshot.QualityGateBlockers = []tagteam.GateFinding{{
		ID:       "CHURN-LINES",
		Severity: "major",
		Gate:     "churn",
		Message:  "diff changes 5815 lines; threshold is 5000",
	}}
	out := renderDashboard(m)
	for _, want := range []string{
		"Blocking reason: blocking_findings",
		"Quality gate blockers:",
		"[major] CHURN-LINES: diff changes 5815 lines; threshold is 5000",
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
		"Enter a task below. Use / only when you need commands.",
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
		"Execution policy only.",
		"rounds: 2",
		"allowed paths: internal/",
		"invocation timeout: 15m0s",
		"no-progress timeout: 5m0s",
		"test:",
		"lint: go vet ./...",
		"allow-dirty:",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("render output missing %q\nfull output:\n%s", want, out)
		}
	}
}

func TestRenderDashboardTeamOverlayExplainsRoleAuthority(t *testing.T) {
	m := fixtureModel()
	m.teamOpen = true
	out := renderDashboard(m)
	for _, want := range []string{
		"Team · supervisor",
		"Build the team:",
		"worker [writes code]: agy:gemini-3.6-flash-high",
		"supervisor [plans + reviews · read-only]: claude:opus",
		"codex effort:",
		"claude effort:",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("team overlay missing %q\nfull output:\n%s", want, out)
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
		"/team",
		"/profile <name>",
		"/model <role> <target>",
		"Choose a role, then assign its model",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("render output missing %q\nfull output:\n%s", want, out)
		}
	}
}

func TestRenderDashboardCommandOverlayScrollsAllCommandsOnCompactTerminal(t *testing.T) {
	m := fixtureModel()
	m.height = 24
	m.commandMode = true
	matches := m.matchingSlashCommands()
	for index, command := range matches {
		m.commandSelection = index
		out := renderDashboard(m)
		if !strings.Contains(out, command.Name) {
			t.Fatalf("compact command overlay did not scroll to %q at selection %d:\n%s", command.Name, index, out)
		}
	}
}

func TestRenderDashboardCommandOverlayShowsExitAliasesInitially(t *testing.T) {
	m := fixtureModel()
	m.height = 24
	m.commandMode = true
	out := renderDashboard(m)
	for _, want := range []string{"/run", "/exit", "/quit"} {
		if !strings.Contains(out, want) {
			t.Fatalf("initial compact command overlay missing %q:\n%s", want, out)
		}
	}
}

func TestRenderDashboardModelPickerShowsRolesBeforeTargets(t *testing.T) {
	m := fixtureModel()
	m.commandMode = true
	m.commandBuffer = "model "
	m.targetChoices = []string{"claude:claude-sonnet-5", "codex:gpt-5.6-terra"}
	out := renderDashboard(m)
	for _, want := range []string{"/model worker <target>", "/model supervisor <target>", "writes code", "read-only"} {
		if !strings.Contains(out, want) {
			t.Fatalf("model role picker missing %q:\n%s", want, out)
		}
	}

	m.commandBuffer = "model worker "
	out = renderDashboard(m)
	for _, want := range []string{"/model worker claude:claude-sonnet-5", "/model worker codex:gpt-5.6-terra"} {
		if !strings.Contains(out, want) {
			t.Fatalf("model target picker missing %q:\n%s", want, out)
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

func TestRenderDashboardNarrowRelayHeaderKeepsEveryRoleVisible(t *testing.T) {
	m := fixtureModel()
	m.width = 80
	m.compose.Mode = tagteam.ModeRelay
	m.compose.EditorTarget = "claude:claude-sonnet-5"
	m.compose.ReviewerTarget = "codex:gpt-5.6-sol"
	m.compose.ScoutTarget = "agy:gemini-3.6-flash-medium"
	out := renderDashboard(m)
	for _, want := range []string{m.compose.EditorTarget, m.compose.ReviewerTarget, m.compose.ScoutTarget} {
		if !strings.Contains(out, want) {
			t.Fatalf("narrow relay header missing %q:\n%s", want, out)
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
