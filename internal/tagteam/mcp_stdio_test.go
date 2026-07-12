package tagteam

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
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
	for _, tool := range tools {
		if tool.(map[string]any)["name"] == "tagteam_start" {
			foundStart = true
		}
	}
	if foundStart {
		t.Fatal("MCP server advertised an unavailable start tool")
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
	if _, ok := structured["action_digest"].(string); !ok {
		t.Fatalf("missing action digest: %#v", structured)
	}
}

func runMCPStdio(t *testing.T, service ControlService, messages ...map[string]any) []map[string]any {
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
