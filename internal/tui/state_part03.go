package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cephalopod-ai/tagteam/internal/tagteam"
)

func (m *model) applyCommand(ctx context.Context, raw string) (action loopAction) {
	command := strings.TrimSpace(strings.TrimPrefix(raw, "/"))
	if command == "" {
		return
	}
	m.statusMessage = ""
	name, rest := splitCommand(command)
	switch name {
	case "exit", "quit":
		return actionQuit
	case "help":
		m.statusMessage = "type / then search; model, profile, mode, effort, run, watch, and settings are available"
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
		if strings.TrimSpace(rest) == "" {
			m.statusMessage = "mode requires a value; type /mode then Space to choose"
			return
		}
		if err := m.setMode(rest); err != nil {
			m.statusMessage = err.Error()
		}
	case "profile":
		if strings.TrimSpace(rest) == "" {
			m.statusMessage = "profile requires a name; type /profile then Space to choose"
			return
		}
		if err := m.applyProfile(rest); err != nil {
			m.statusMessage = err.Error()
			return
		}
	case "profiles":
		m.statusMessage = "profiles: " + strings.Join(m.profileChoiceValues(), ", ")
	case "team":
		m.teamOpen = true
		m.settingsOpen = false
		m.focus = focusCompose
	case "settings":
		m.settingsOpen = true
		m.teamOpen = false
		m.focus = focusCompose
	case "model":
		roleName, target := splitCommand(rest)
		if role := m.teamRole(roleName); role != nil {
			value, err := validateTargetWord("model "+role.Name, target)
			if err != nil {
				m.statusMessage = err.Error()
				return
			}
			m.setRoleTarget(*role, value)
			m.statusMessage = role.Name + " model set to " + value
			return
		}
		if isTeamRoleName(roleName) {
			m.statusMessage = fmt.Sprintf("role %s is not part of %s mode; type /model then Space to see active roles", roleName, m.compose.Mode)
			return
		}
		value, err := validateTargetWord("model", rest)
		if err != nil {
			m.statusMessage = err.Error()
			return
		}
		m.compose.EditorTarget = value
	case "editor":
		value, err := validateTargetWord("model", rest)
		if err != nil {
			m.statusMessage = err.Error()
			return
		}
		m.compose.EditorTarget = value
	case "worker":
		if err := m.requireModeCommand(name, tagteam.ModeSolo, tagteam.ModeSupervisor, tagteam.ModeRelay); err != nil {
			m.statusMessage = err.Error()
			return
		}
		value, err := validateTargetWord("worker", rest)
		if err != nil {
			m.statusMessage = err.Error()
			return
		}
		m.compose.EditorTarget = value
	case "coder":
		if err := m.requireModeCommand(name, tagteam.ModeRelay, tagteam.ModeAdversarial); err != nil {
			m.statusMessage = err.Error()
			return
		}
		value, err := validateTargetWord("coder", rest)
		if err != nil {
			m.statusMessage = err.Error()
			return
		}
		m.compose.EditorTarget = value
	case "supervisor":
		if err := m.requireModeCommand(name, tagteam.ModeSupervisor, tagteam.ModeRelay); err != nil {
			m.statusMessage = err.Error()
			return
		}
		value, err := validateTargetWord("supervisor", rest)
		if err != nil {
			m.statusMessage = err.Error()
			return
		}
		m.compose.ReviewerTarget = value
	case "reviewer", "adversary":
		if err := m.requireModeCommand(name, tagteam.ModeAdversarial); err != nil {
			m.statusMessage = err.Error()
			return
		}
		value, err := validateTargetWord("reviewer", rest)
		if err != nil {
			m.statusMessage = err.Error()
			return
		}
		m.compose.ReviewerTarget = value
	case "scout":
		if err := m.requireRelayCommand(name); err != nil {
			m.statusMessage = err.Error()
			return
		}
		value, err := validateTargetWord("scout", rest)
		if err != nil {
			m.statusMessage = err.Error()
			return
		}
		m.compose.ScoutTarget = value
	case "codex-effort":
		value := strings.TrimSpace(rest)
		if err := requireInSet("codex-effort", value, []string{"none", "minimal", "low", "medium", "high", "xhigh"}); err != nil {
			m.statusMessage = err.Error()
			return
		}
		m.compose.CodexEffort = value
	case "claude-effort":
		value := strings.TrimSpace(rest)
		if err := requireInSet("claude-effort", value, []string{"low", "medium", "high", "xhigh", "max"}); err != nil {
			m.statusMessage = err.Error()
			return
		}
		m.compose.ClaudeEffort = value
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
	case "allow-path":
		paths := parseAllowedPaths(rest)
		if len(paths) == 0 {
			m.statusMessage = "allow-path requires one or more comma-separated repo-relative paths"
			return
		}
		m.compose.AllowedPaths = paths
	case "timeout":
		value, err := parsePositiveDuration(name, rest)
		if err != nil {
			m.statusMessage = err.Error()
			return
		}
		m.compose.Timeout = value
	case "watchdog-timeout":
		value, err := parsePositiveDuration(name, rest)
		if err != nil {
			m.statusMessage = err.Error()
			return
		}
		m.compose.WatchdogTimeout = value
	case "test":
		m.compose.TestCmd = rest
	case "lint":
		m.compose.LintCmd = rest
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
		if value {
			m.statusMessage = "allow-dirty enabled: review and gates use the cumulative diff against HEAD"
		}
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
	return actionContinue
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
		runDir, resolveErr := tagteam.RunDirForCLI(m.workdir, target)
		if resolveErr != nil {
			return resolveErr
		}
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
	if mode == previous {
		return nil
	}
	m.teamAssignments[previous] = m.currentRoleAssignments()
	if saved, ok := m.teamAssignments[mode]; ok {
		m.compose.Mode = mode
		m.applyRoleAssignments(saved)
		m.clampSelection()
		return nil
	}
	assignments, err := m.defaultModeTargets(mode)
	if err != nil {
		return err
	}
	m.compose.Mode = mode
	m.applyRoleAssignments(assignments)
	m.teamAssignments[mode] = assignments
	m.clampSelection()
	return nil
}

func (m *model) defaultModeTargets(mode tagteam.Mode) (roleAssignments, error) {
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
		return roleAssignments{}, err
	}
	assignments := roleAssignments{Editor: roleTargetString(defaults.Coder)}
	if mode != tagteam.ModeSolo {
		assignments.Reviewer = roleTargetString(defaults.Adversary)
	}
	if mode == tagteam.ModeRelay {
		assignments.Scout = roleTargetString(defaults.Scout)
	}
	return assignments, nil
}

func (m *model) currentRoleAssignments() roleAssignments {
	return roleAssignments{
		Editor:   m.compose.EditorTarget,
		Reviewer: m.compose.ReviewerTarget,
		Scout:    m.compose.ScoutTarget,
	}
}

func (m *model) applyRoleAssignments(assignments roleAssignments) {
	m.compose.EditorTarget = assignments.Editor
	m.compose.ReviewerTarget = assignments.Reviewer
	m.compose.ScoutTarget = assignments.Scout
}

func (m *model) resetTeamAssignments() {
	m.teamAssignments = map[tagteam.Mode]roleAssignments{
		m.compose.Mode: m.currentRoleAssignments(),
	}
}

func (m *model) launchRun(ctx context.Context) {
	if m.mutationBlocked != "" {
		m.statusMessage = "run launch blocked: " + m.mutationBlocked
		return
	}
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
	flags.AllowedPaths = append([]string(nil), m.compose.AllowedPaths...)
	flags.Timeout = m.compose.Timeout
	flags.WatchdogTimeout = m.compose.WatchdogTimeout
	flags.Test = m.compose.TestCmd
	flags.Lint = m.compose.LintCmd
	flags.NoTest = m.compose.NoTest
	flags.AllowDirty = m.compose.AllowDirty
	flags.Quiet = true
	flags.Verbose = false
	flags.RepairJSONWithWorker = m.compose.RepairJSONWorker
	flags.StrictScout = m.compose.StrictScout
	flags.NoScoutRetrieval = m.compose.Mode == tagteam.ModeRelay && !m.compose.ScoutRetrieval
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
	changed["allow-path"] = true
	changed["timeout"] = true
	changed["watchdog-timeout"] = true
	changed["test"] = true
	changed["lint"] = true
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
	if m.compose.Mode == tagteam.ModeRelay && !m.compose.ScoutRetrieval {
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
	cfg.Adapters.Codex.ReasoningEffort = m.compose.CodexEffort
	cfg.Adapters.Claude.Effort = m.compose.ClaudeEffort
	runOpts, err := tagteam.ResolveOptions(cfg, sources, flags, changed, m.compose.Prompt)
	if err != nil {
		return tagteam.RunOptions{}, tagteam.Config{}, err
	}
	if len(runOpts.AllowedPaths) == 0 && (runOpts.Mode != tagteam.ModeSupervisor || !runOpts.SupervisorSlicing) {
		return tagteam.RunOptions{}, tagteam.Config{}, fmt.Errorf("%s mode requires at least one --allow-path before TUI launch", runOpts.Mode)
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
		"allow-dirty", "allow-path", "coder", "lint", "mc", "mode", "model", "no-scout-retrieval", "no-slice",
		"no-test", "post-scout-mode", "profile", "relay", "repair-json-with-worker",
		"reviewer", "scout", "scout-context-policy", "scout-mode", "slice", "solo",
		"strict-scout", "supervisor", "test", "timeout", "watchdog-timeout", "worker",
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
		fmt.Sprintf("Completed phase: %s", dashIfEmpty(string(s.CompletedPhase))),
		fmt.Sprintf("Verdict: %s", dashIfEmpty(s.Verdict)),
		fmt.Sprintf("Updated: %s", formatTime(s.UpdatedAt)),
	}
	if s.RoundsRequested > 0 || s.RoundsCompleted > 0 {
		lines = append(lines, fmt.Sprintf("Rounds: %d/%d current=%d", s.RoundsCompleted, s.RoundsRequested, s.CurrentRound))
	} else if s.CurrentRound > 0 {
		lines = append(lines, fmt.Sprintf("Round: %d", s.CurrentRound))
	}
	if live := s.LiveProgress; live != nil && (s.Status == "running" || live.Status == "stalled") {
		activity := fmt.Sprintf("Activity: %s · %s · elapsed %s", dashIfEmpty(string(live.Role)), liveStatusLabel(live), dashIfEmpty(live.Elapsed))
		if live.NoProgressFor != "" {
			activity += " · idle " + live.NoProgressFor
		}
		lines = append(lines, activity)
		lines = append(lines, fmt.Sprintf("Working diff: %d files (+%d -%d)", live.FilesChanged, live.Additions, live.Deletions))
	}
	if len(s.PreexistingFiles) > 0 {
		lines = append(lines, fmt.Sprintf("Baseline: cumulative dirty worktree (%d pre-existing %s)", len(s.PreexistingFiles), pluralWord(len(s.PreexistingFiles), "file", "files")))
	}
	if s.RecoveryStatus != "" {
		lines = append(lines, fmt.Sprintf("Recovery: %s", s.RecoveryStatus))
	}
	if s.OpenMajorCount > 0 {
		lines = append(lines, fmt.Sprintf("Open blocker/major findings: %d", s.OpenMajorCount))
	}
	if s.Degraded {
		lines = append(lines, fmt.Sprintf("Degraded: true (%s)", dashIfEmpty(s.DegradedReason)))
	}
	if s.BlockingReason != "" {
		lines = append(lines, fmt.Sprintf("Blocking reason: %s", s.BlockingReason))
	}
	lines = append(lines, "", "Roles:")
	roles := s.RoleStatuses
	if live := s.LiveProgress; live != nil && live.Status == "waiting" {
		roles = roleStatusesWithLiveState(roles, live)
	}
	lines = append(lines, renderRoleDetailLines(roles)...)

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
			"  state:  "+dashIfEmpty(s.StateRoot),
		)
	}

	if m.showTestOutput && len(m.currentTestTail) > 0 {
		lines = append(lines, "", "Test output tail:")
		lines = append(lines, m.currentTestTail...)
	}

	return lines
}

func roleStatusesWithLiveState(roles map[string]tagteam.RoleStatus, live *tagteam.LiveProgress) map[string]tagteam.RoleStatus {
	copyRoles := make(map[string]tagteam.RoleStatus, len(roles))
	for name, role := range roles {
		if role.Role == string(live.Role) || name == string(live.Role) {
			role.Status = live.Status
		}
		copyRoles[name] = role
	}
	return copyRoles
}
