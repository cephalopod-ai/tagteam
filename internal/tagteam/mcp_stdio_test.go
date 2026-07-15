package tagteam

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestMCPStdioServerServesBoundedReadTools(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	service := ControlService{RepositoryRoot: repo, StateRoot: t.TempDir(), ProducerVersion: "test"}
	responses := runMCPStdio(t, service,
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": MCPProtocolVersion, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"}}},
		map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": map[string]any{}},
		map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": map[string]any{"name": "tagteam_diagnostics", "arguments": map[string]any{}}},
		map[string]any{"jsonrpc": "2.0", "id": 4, "method": "tools/call", "params": map[string]any{"name": "tagteam_not_a_tool", "arguments": map[string]any{}}},
	)
	if len(responses) != 4 {
		t.Fatalf("responses = %#v", responses)
	}
	if got := responses[0]["result"].(map[string]any)["protocolVersion"]; got != MCPProtocolVersion {
		t.Fatalf("protocol version = %v", got)
	}
	tools := responses[1]["result"].(map[string]any)["tools"].([]any)
	foundStart := false
	foundPrepareStart := false
	foundPrepareResume := false
	for _, tool := range tools {
		switch tool.(map[string]any)["name"] {
		case "tagteam_start":
			foundStart = true
		case "tagteam_prepare_start":
			foundPrepareStart = true
		case "tagteam_prepare_resume":
			foundPrepareResume = true
		}
	}
	if foundStart {
		t.Fatal("MCP server advertised an unavailable start tool")
	}
	if !foundPrepareStart {
		t.Fatal("MCP server did not advertise prepare_start")
	}
	if !foundPrepareResume {
		t.Fatal("MCP server did not advertise prepare_resume")
	}
	diagnostics := responses[2]["result"].(map[string]any)["structuredContent"].(map[string]any)
	if diagnostics["status"] != "ready" {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
	if responses[3]["result"].(map[string]any)["isError"] != true {
		t.Fatalf("unknown tool response = %#v", responses[3])
	}
}

func TestMCPStdioServerRequiresInitialization(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	service := ControlService{RepositoryRoot: repo, StateRoot: t.TempDir(), ProducerVersion: "test"}
	responses := runMCPStdio(t, service,
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{}},
		map[string]any{"jsonrpc": "2.0", "method": "tools/list", "params": map[string]any{}},
	)
	if len(responses) != 1 {
		t.Fatalf("responses = %#v", responses)
	}
	errorResult := responses[0]["error"].(map[string]any)
	if errorResult["code"] != float64(-32002) || errorResult["message"] != "server not initialized" {
		t.Fatalf("initialization error = %#v", errorResult)
	}
}

func TestMCPStdioServerValidatesLaunchWithoutStartingIt(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	service := ControlService{RepositoryRoot: repo, StateRoot: t.TempDir(), ProducerVersion: "test"}
	spec := controlLaunchFixture(t, repo)
	responses := runMCPStdio(t, service,
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "tagteam_validate_launch", "arguments": spec}},
	)
	result := responses[1]["result"].(map[string]any)
	if result["isError"] != false {
		t.Fatalf("launch validation = %#v", result)
	}
	structured := result["structuredContent"].(map[string]any)
	if _, ok := structured["launch_digest"].(string); !ok {
		t.Fatalf("missing launch digest: %#v", structured)
	}
}

func TestMCPStdioServerPreparesTheApprovalBoundStartDigest(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	service := ControlService{RepositoryRoot: repo, StateRoot: t.TempDir(), ProducerVersion: "test"}
	request := ControlStartRequest{SchemaVersion: ControlContractVersion, Launch: controlLaunchFixture(t, repo), IdempotencyKey: "session-1-generation-1"}
	responses := runMCPStdio(t, service,
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "tagteam_prepare_start", "arguments": request}},
	)
	prepared := responses[1]["result"].(map[string]any)["structuredContent"].(map[string]any)
	if _, ok := prepared["action_digest"].(string); !ok {
		t.Fatalf("prepared start = %#v", prepared)
	}
}

func TestMCPStdioServerPreparesResumeWithoutRunningIt(t *testing.T) {
	repo, baseline := createResumeFixtureRepo(t)
	stateRoot := t.TempDir()
	runID := "mcp-resume-assessment"
	runDir, err := createRunDir(repo, stateRoot, runID)
	if err != nil {
		t.Fatal(err)
	}
	writeResumeFixture(t, runDir, runID, repo, baseline, RunStatusRunning)
	repository, err := resolveControlRepository(repo)
	if err != nil {
		t.Fatal(err)
	}
	service := ControlService{RepositoryRoot: repo, StateRoot: stateRoot, ProducerVersion: "test"}
	request := ControlResumeRequest{SchemaVersion: ControlContractVersion, Repository: repository, RunID: runID}
	responses := runMCPStdio(t, service,
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "tagteam_prepare_resume", "arguments": request}},
	)
	prepared := responses[1]["result"].(map[string]any)["structuredContent"].(map[string]any)
	if prepared["resumable"] != true || prepared["reason_code"] != "resumable" {
		t.Fatalf("prepared resume = %#v", prepared)
	}
}

func TestMCPStdioServerAdvertisesStartOnlyWithLifecycleRuntime(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	service := ControlService{RepositoryRoot: repo, StateRoot: t.TempDir(), ProducerVersion: "test"}
	runtime := NewControlRuntime(service, DefaultConfig(), nil)
	responses := runMCPStdioWithRuntime(t, service, runtime,
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": map[string]any{}},
	)
	tools := responses[1]["result"].(map[string]any)["tools"].([]any)
	foundResume := false
	foundCancel := false
	for _, tool := range tools {
		switch tool.(map[string]any)["name"] {
		case "tagteam_start":
		case "tagteam_resume":
			foundResume = true
		case "tagteam_cancel":
			foundCancel = true
		}
	}
	if !foundResume {
		t.Fatalf("runtime tool list did not include resume: %#v", tools)
	}
	if !foundCancel {
		t.Fatalf("runtime tool list did not include cancel: %#v", tools)
	}
	for _, tool := range tools {
		if tool.(map[string]any)["name"] == "tagteam_start" {
			return
		}
	}
	t.Fatalf("runtime tool list did not include start: %#v", tools)
}

func TestMCPStdioServerStopsOwnedJobsWhenInputCloses(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	service := ControlService{RepositoryRoot: repo, StateRoot: t.TempDir(), ProducerVersion: "test"}
	runtime := NewControlRuntime(service, DefaultConfig(), nil)
	jobContext, cancelJob := context.WithCancel(context.Background())
	runtime.registerJob("mcp-stdio-close", cancelJob)
	runMCPStdioWithRuntime(t, service, runtime,
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}},
	)
	select {
	case <-jobContext.Done():
	case <-time.After(time.Second):
		t.Fatal("MCP stdio shutdown did not cancel its owned job")
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if len(runtime.jobs) != 0 || runtime.watcherCancel != nil || runtime.watcherDone != nil {
		t.Fatalf("MCP stdio shutdown retained runtime state: jobs=%d watcher=%v/%v", len(runtime.jobs), runtime.watcherCancel != nil, runtime.watcherDone != nil)
	}
}

func TestMCPStdioServerStartsApprovedRunWithDurableHandle(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	service := ControlService{RepositoryRoot: repo, StateRoot: t.TempDir(), ProducerVersion: "test"}
	runtime := NewControlRuntime(service, DefaultConfig(), nil)
	request := controlStartFixture(t, repo)
	responses := runMCPStdioWithRuntime(t, service, runtime,
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "tagteam_start", "arguments": request}},
	)
	result := responses[1]["result"].(map[string]any)
	if result["isError"] != false {
		t.Fatalf("start result = %#v", result)
	}
	handle := result["structuredContent"].(map[string]any)
	runID, ok := handle["run_id"].(string)
	if !ok || runID == "" {
		t.Fatalf("start handle = %#v", handle)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, err := runtime.Status(runID)
		if err == nil && (status.Run.Status == string(RunStatusFailed) || status.Run.Status == string(RunStatusCancelled)) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("MCP-started run %q did not persist a terminal result", runID)
}

func TestMCPStdioServerResumesApprovedRunWithDurableHandle(t *testing.T) {
	repo, baseline := createResumeFixtureRepo(t)
	stateRoot := t.TempDir()
	runID := "mcp-resume-runtime"
	runDir, err := createRunDir(repo, stateRoot, runID)
	if err != nil {
		t.Fatal(err)
	}
	writeResumeFixture(t, runDir, runID, repo, baseline, RunStatusRunning)
	repository, err := resolveControlRepository(repo)
	if err != nil {
		t.Fatal(err)
	}
	request := ControlResumeRequest{SchemaVersion: ControlContractVersion, Repository: repository, RunID: runID}
	request.Approval = validResumeApproval(t, request, "mcp-resume-once")
	service := ControlService{RepositoryRoot: repo, StateRoot: stateRoot, ProducerVersion: "test"}
	runtime := NewControlRuntime(service, DefaultConfig(), nil)
	responses := runMCPStdioWithRuntime(t, service, runtime,
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "tagteam_resume", "arguments": request}},
	)
	result := responses[1]["result"].(map[string]any)
	if result["isError"] != false {
		t.Fatalf("resume result = %#v", result)
	}
	handle := result["structuredContent"].(map[string]any)
	if handle["run_id"] != runID {
		t.Fatalf("resume handle = %#v", handle)
	}
	waitForControlResumeJob(t, runtime, runID)
}

func TestMCPStdioServerSurfacesUnknownTestPresetAsToolError(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	service := ControlService{RepositoryRoot: repo, StateRoot: t.TempDir(), ProducerVersion: "test"}
	runtime := NewControlRuntime(service, DefaultConfig(), nil)
	request := controlStartFixture(t, repo)
	request.Launch.TestPreset = "no-such-preset"
	digest, err := ControlStartActionDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	request.Approval.ActionDigest = digest
	responses := runMCPStdioWithRuntime(t, service, runtime,
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "tagteam_start", "arguments": request}},
	)
	result := responses[1]["result"].(map[string]any)
	if result["isError"] != true {
		t.Fatalf("unknown preset start result = %#v", result)
	}
	structured := result["structuredContent"].(map[string]any)
	if structured["code"] != "operation_failed" || structured["recoverable"] != true {
		t.Fatalf("unknown preset error contract = %#v", structured)
	}
	if got, _ := structured["reason"].(string); !strings.Contains(got, `unknown test_preset "no-such-preset"`) {
		t.Fatalf("structured error = %#v", structured)
	}
}

func TestMCPStdioServerSurfacesTypedLifecycleErrors(t *testing.T) {
	repo, baseline := createResumeFixtureRepo(t)
	stateRoot := t.TempDir()
	runID := "mcp-typed-errors"
	runDir, err := createRunDir(repo, stateRoot, runID)
	if err != nil {
		t.Fatal(err)
	}
	writeResumeFixture(t, runDir, runID, repo, baseline, RunStatusRunning)
	repository, err := resolveControlRepository(repo)
	if err != nil {
		t.Fatal(err)
	}
	resume := ControlResumeRequest{SchemaVersion: ControlContractVersion, Repository: repository, RunID: runID}
	cancel := ControlCancelRequest{SchemaVersion: ControlContractVersion, Repository: repository, RunID: runID}
	service := ControlService{RepositoryRoot: repo, StateRoot: stateRoot, ProducerVersion: "test"}
	runtime := NewControlRuntime(service, DefaultConfig(), nil)
	responses := runMCPStdioWithRuntime(t, service, runtime,
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "tagteam_resume", "arguments": resume}},
		map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": map[string]any{"name": "tagteam_cancel", "arguments": cancel}},
	)
	for index, wantCode := range []string{"approval_missing", "approval_missing"} {
		result := responses[index+1]["result"].(map[string]any)
		if result["isError"] != true {
			t.Fatalf("lifecycle result = %#v", result)
		}
		structured := result["structuredContent"].(map[string]any)
		if structured["code"] != wantCode || structured["recoverable"] != true {
			t.Fatalf("lifecycle error contract = %#v", structured)
		}
		if reason, _ := structured["reason"].(string); reason == "" {
			t.Fatalf("lifecycle error lacked bounded reason: %#v", structured)
		}
	}
}

func TestMCPStdioServerStartsWithTrustedTestPreset(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	service := ControlService{RepositoryRoot: repo, StateRoot: t.TempDir(), ProducerVersion: "test"}
	cfg := DefaultConfig()
	cfg.TestPresets = map[string]TestPresetConfig{
		"go-test": {Command: "true"},
	}
	runtime := NewControlRuntime(service, cfg, nil)
	request := controlStartFixture(t, repo)
	request.Launch.TestPreset = "go-test"
	digest, err := ControlStartActionDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	request.Approval.ActionDigest = digest
	request.Approval.Nonce = "operator-approved-preset-nonce"
	responses := runMCPStdioWithRuntime(t, service, runtime,
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "tagteam_start", "arguments": request}},
	)
	result := responses[1]["result"].(map[string]any)
	if result["isError"] != false {
		t.Fatalf("preset start result = %#v", result)
	}
	handle := result["structuredContent"].(map[string]any)
	runID, ok := handle["run_id"].(string)
	if !ok || runID == "" {
		t.Fatalf("start handle = %#v", handle)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, statusErr := runtime.Status(runID)
		if statusErr == nil && (status.Run.Status == string(RunStatusFailed) || status.Run.Status == string(RunStatusCancelled)) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("MCP-started preset run %q did not persist a terminal result", runID)
}

func TestMCPToolSurfaceExcludesCommandCwdAndArtifactReaders(t *testing.T) {
	tools := mcpControlTools(true)
	allowed := map[string]bool{
		"tagteam_capabilities":    true,
		"tagteam_validate_launch": true,
		"tagteam_prepare_start":   true,
		"tagteam_prepare_resume":  true,
		"tagteam_status":          true,
		"tagteam_plan":            true,
		"tagteam_findings":        true,
		"tagteam_diagnostics":     true,
		"tagteam_start":           true,
		"tagteam_resume":          true,
		"tagteam_cancel":          true,
	}
	forbiddenKeys := []string{"command", "argv", "cwd", "workdir", "shell", "artifact_path", "read_artifact", "passthrough"}
	for _, tool := range tools {
		name, _ := tool["name"].(string)
		if !allowed[name] {
			t.Fatalf("unexpected MCP tool %q", name)
		}
		delete(allowed, name)
		raw, _ := json.Marshal(tool)
		lower := strings.ToLower(string(raw))
		for _, key := range forbiddenKeys {
			// Schema property names for launch fields must not introduce command/cwd surfaces.
			if strings.Contains(lower, `"`+key+`"`) {
				t.Fatalf("tool %q schema mentions forbidden key %q: %s", name, key, raw)
			}
		}
	}
	if len(allowed) != 0 {
		t.Fatalf("missing expected MCP tools: %#v", allowed)
	}
}

func runMCPStdio(t *testing.T, service ControlService, messages ...map[string]any) []map[string]any {
	return runMCPStdioWithRuntime(t, service, nil, messages...)
}

func runMCPStdioWithRuntime(t *testing.T, service ControlService, runtime *ControlRuntime, messages ...map[string]any) []map[string]any {
	t.Helper()
	var input bytes.Buffer
	for _, message := range messages {
		payload, err := json.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		input.Write(payload)
		input.WriteByte('\n')
	}
	var output bytes.Buffer
	server := NewMCPStdioServer(service, &input, &output)
	if runtime != nil {
		server.WithRuntime(runtime)
	}
	if err := server.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	responses := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		var response map[string]any
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			t.Fatal(err)
		}
		responses = append(responses, response)
	}
	return responses
}
