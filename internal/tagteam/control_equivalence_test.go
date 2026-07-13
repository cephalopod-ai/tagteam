package tagteam

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// TestControlLaunchNormalizationEqualsDirectCLIOptions proves the MCP launch
// path projects onto the same ResolveOptions a direct CLI invocation uses: the
// normalized worktree, mode, roles, scope, rounds, and budgets are identical.
func TestControlLaunchNormalizationEqualsDirectCLIOptions(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	stateRoot := t.TempDir()
	cfg := DefaultConfig()
	runtime := NewControlRuntime(ControlService{RepositoryRoot: repo, StateRoot: stateRoot, ProducerVersion: "test"}, cfg, nil)

	spec := controlLaunchFixture(t, repo)
	spec.TestPreset = ""
	normalized, err := NormalizeControlLaunch(spec)
	if err != nil {
		t.Fatal(err)
	}
	mcpOpts, err := runtime.optionsForLaunch(normalized)
	if err != nil {
		t.Fatal(err)
	}

	// The direct CLI path: the same launch expressed as a user's run flags.
	flags := FlagInputs{
		Mode:            "supervisor",
		Workdir:         normalized.Repository.CanonicalRoot,
		StateRoot:       stateRoot,
		Worker:          "agy:gemini-3.5-flash",
		Supervisor:      "codex:gpt-5.6-sol",
		AllowedPaths:    append([]string(nil), normalized.AllowedPaths...),
		Rounds:          normalized.Rounds,
		Timeout:         time.Duration(normalized.TimeBudget.InvocationTimeoutSeconds) * time.Second,
		WatchdogTimeout: time.Duration(normalized.TimeBudget.WatchdogTimeoutSeconds) * time.Second,
		MaxWallTime:     time.Duration(normalized.TimeBudget.WallTimeoutSeconds) * time.Second,
	}
	changed := map[string]bool{
		"mode": true, "worker": true, "supervisor": true, "allow-path": true,
		"rounds": true, "watchdog-timeout": true, "max-wall-time": true, "state-root": true,
	}
	cliOpts, err := ResolveOptions(cfg, nil, flags, changed, normalized.Prompt)
	if err != nil {
		t.Fatal(err)
	}

	if mcpOpts.Workdir != cliOpts.Workdir {
		t.Fatalf("workdir MCP=%q CLI=%q", mcpOpts.Workdir, cliOpts.Workdir)
	}
	if mcpOpts.Mode != cliOpts.Mode {
		t.Fatalf("mode MCP=%q CLI=%q", mcpOpts.Mode, cliOpts.Mode)
	}
	if !reflect.DeepEqual(mcpOpts.AllowedPaths, cliOpts.AllowedPaths) {
		t.Fatalf("allowed paths MCP=%#v CLI=%#v", mcpOpts.AllowedPaths, cliOpts.AllowedPaths)
	}
	if mcpOpts.Rounds != cliOpts.Rounds {
		t.Fatalf("rounds MCP=%d CLI=%d", mcpOpts.Rounds, cliOpts.Rounds)
	}
	if !reflect.DeepEqual(mcpOpts.Coder, cliOpts.Coder) || !reflect.DeepEqual(mcpOpts.Adversary, cliOpts.Adversary) {
		t.Fatalf("roles MCP coder=%#v adversary=%#v CLI coder=%#v adversary=%#v", mcpOpts.Coder, mcpOpts.Adversary, cliOpts.Coder, cliOpts.Adversary)
	}
	if mcpOpts.MaxWallTime != cliOpts.MaxWallTime || mcpOpts.WatchdogTimeout != cliOpts.WatchdogTimeout {
		t.Fatalf("budgets MCP wall=%s watchdog=%s CLI wall=%s watchdog=%s", mcpOpts.MaxWallTime, mcpOpts.WatchdogTimeout, cliOpts.MaxWallTime, cliOpts.WatchdogTimeout)
	}
	// The projected roles must carry the exact adapters and models requested.
	if mcpOpts.Coder.Adapter != "agy" || mcpOpts.Coder.Model != "gemini-3.5-flash" {
		t.Fatalf("worker role = %#v", mcpOpts.Coder)
	}
	if mcpOpts.Adversary.Adapter != "codex" || mcpOpts.Adversary.Model != "gpt-5.6-sol" {
		t.Fatalf("supervisor role = %#v", mcpOpts.Adversary)
	}
}

// TestControlTerminalRecordMatchesDirectCLIClassification proves the MCP path
// and the direct CLI runner classify the same unrunnable-adapter launch into an
// identical terminal record: same failed status, exit code, and blocking reason.
func TestControlTerminalRecordMatchesDirectCLIClassification(t *testing.T) {
	cliRepo, _ := createResumeFixtureRepo(t)
	mcpRepo, _ := createResumeFixtureRepo(t)
	cfg := DefaultConfig()

	// Direct CLI runner path: App.Run is exactly what `tagteam run` invokes.
	cliRepository, err := resolveControlRepository(cliRepo)
	if err != nil {
		t.Fatal(err)
	}
	cliFlags := FlagInputs{
		Mode:         "supervisor",
		Workdir:      cliRepository.CanonicalRoot,
		StateRoot:    t.TempDir(),
		Worker:       "missing",
		Supervisor:   "missing",
		AllowedPaths: []string{"README.md"},
		Rounds:       2,
	}
	cliChanged := map[string]bool{"mode": true, "worker": true, "supervisor": true, "allow-path": true, "rounds": true, "state-root": true}
	cliOpts, err := ResolveOptions(cfg, nil, cliFlags, cliChanged, "repair the parser")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, cliErr := NewApp(cfg).Run(ctx, cliOpts)
	if cliErr == nil {
		t.Fatal("direct CLI run with a missing adapter unexpectedly succeeded")
	}
	cliExit := ExitCode(cliErr)
	cliReason := string(reasonForExit(cliExit))
	if cliReason == "" {
		cliReason = string(ReasonWorkerUnavailable)
	}

	// MCP path: Start persists a terminal record when preflight fails.
	stateRoot := t.TempDir()
	runtime := NewControlRuntime(ControlService{RepositoryRoot: mcpRepo, StateRoot: stateRoot, ProducerVersion: "test"}, cfg, nil)
	handle, err := runtime.Start(context.Background(), controlStartFixture(t, mcpRepo))
	if err != nil {
		t.Fatal(err)
	}
	waitForControlRunFailure(t, runtime, "session-1-generation-1")
	locator, err := resolveStateLocator(mcpRepo, stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	runDir, err := locator.RunDir(handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(runDir, "final.json"))
	if err != nil {
		t.Fatal(err)
	}
	var final FinalRun
	if err := json.Unmarshal(data, &final); err != nil {
		t.Fatal(err)
	}

	if final.Status != RunStatusFailed {
		t.Fatalf("MCP terminal status = %q, want failed like the CLI path", final.Status)
	}
	if final.ExitCode != cliExit {
		t.Fatalf("terminal exit code MCP=%d CLI=%d", final.ExitCode, cliExit)
	}
	if final.BlockingReason != cliReason {
		t.Fatalf("terminal blocking reason MCP=%q CLI=%q", final.BlockingReason, cliReason)
	}
	if final.Mode != cliOpts.Mode {
		t.Fatalf("terminal mode MCP=%q CLI=%q", final.Mode, cliOpts.Mode)
	}
	waitForControlJobsDrained(t, runtime)
}

// TestMCPStdioServerSurfacesTypedLifecycleErrorsForRecovery proves an MCP host
// can recover from start, resume, and cancel failures using stable structured
// reason codes over the wire, without parsing prose or reading source.
func TestMCPStdioServerSurfacesTypedLifecycleErrorsForRecovery(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	stateRoot := t.TempDir()
	repository, err := resolveControlRepository(repo)
	if err != nil {
		t.Fatal(err)
	}
	service := ControlService{RepositoryRoot: repo, StateRoot: stateRoot, ProducerVersion: "test"}
	runtime := NewControlRuntime(service, DefaultConfig(), nil)

	startReq := controlStartFixture(t, repo)
	startReq.Approval.ActionDigest = "0000000000000000000000000000000000000000000000000000000000000000"

	resumeReq := ControlResumeRequest{SchemaVersion: ControlContractVersion, Repository: repository, RunID: "typed-error-run"}
	resumeReq.Approval = validResumeApproval(t, resumeReq, "resume-typed-nonce")
	now := time.Now().UTC()
	resumeReq.Approval.ApprovedAt = now.Add(-10 * time.Minute)
	resumeReq.Approval.ExpiresAt = now.Add(-time.Minute)

	cancelReq := ControlCancelRequest{SchemaVersion: ControlContractVersion, Repository: repository, RunID: "typed-error-run"}
	cancelReq.Approval = validCancelApproval(t, cancelReq, "cancel-typed-nonce")
	cancelReq.Approval.ActionDigest = "0000000000000000000000000000000000000000000000000000000000000000"

	responses := runMCPStdioWithRuntime(t, service, runtime,
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "tagteam_start", "arguments": startReq}},
		map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": map[string]any{"name": "tagteam_resume", "arguments": resumeReq}},
		map[string]any{"jsonrpc": "2.0", "id": 4, "method": "tools/call", "params": map[string]any{"name": "tagteam_cancel", "arguments": cancelReq}},
	)
	expected := []struct {
		index int
		code  string
	}{
		{1, "approval_action_mismatch"},
		{2, "approval_expired"},
		{3, "approval_action_mismatch"},
	}
	for _, want := range expected {
		result, ok := responses[want.index]["result"].(map[string]any)
		if !ok {
			t.Fatalf("response %d = %#v", want.index, responses[want.index])
		}
		if result["isError"] != true {
			t.Fatalf("response %d isError = %v, want true", want.index, result["isError"])
		}
		structured, ok := result["structuredContent"].(map[string]any)
		if !ok {
			t.Fatalf("response %d structuredContent = %#v", want.index, result["structuredContent"])
		}
		if structured["code"] != want.code {
			t.Fatalf("response %d code = %v, want %q", want.index, structured["code"], want.code)
		}
		if structured["recoverable"] != true {
			t.Fatalf("response %d recoverable = %v, want true", want.index, structured["recoverable"])
		}
	}
}
