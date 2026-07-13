package tagteam

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func startMCPSocketDaemon(t *testing.T, service ControlService, runtime *ControlRuntime) string {
	t.Helper()
	// Unix socket paths are length-limited (~104 bytes on macOS/BSD). t.TempDir()
	// embeds the (long) test name and overflows that limit on macOS, so use a
	// short temp dir and short socket name instead.
	dir, err := os.MkdirTemp("", "tt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s.sock")
	listener, err := ListenMCPUnixSocket(socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ServeMCPSocket(ctx, listener, service, runtime) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("MCP socket daemon did not stop after context cancellation")
		}
	})
	return socket
}

func mcpSocketRoundTrip(t *testing.T, socket string, wantResponses int, messages ...map[string]any) []map[string]any {
	t.Helper()
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, message := range messages {
		payload, err := json.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Write(append(payload, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	responses := make([]map[string]any, 0, wantResponses)
	for len(responses) < wantResponses && scanner.Scan() {
		var response map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
			t.Fatalf("decode socket response: %v", err)
		}
		responses = append(responses, response)
	}
	if len(responses) < wantResponses {
		t.Fatalf("got %d responses, want %d", len(responses), wantResponses)
	}
	return responses
}

func TestServeMCPSocketServesReadOnlyToolsOverSocket(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	service := ControlService{RepositoryRoot: repo, StateRoot: t.TempDir(), ProducerVersion: "test"}
	socket := startMCPSocketDaemon(t, service, nil)

	responses := mcpSocketRoundTrip(t, socket, 2,
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}},
		map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "tagteam_diagnostics", "arguments": map[string]any{}}},
	)
	if got := responses[0]["result"].(map[string]any)["protocolVersion"]; got != MCPProtocolVersion {
		t.Fatalf("protocol version = %v", got)
	}
	diagnostics := responses[1]["result"].(map[string]any)["structuredContent"].(map[string]any)
	if diagnostics["status"] != "ready" {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
}

func TestServeMCPSocketSupportsMultipleClients(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	service := ControlService{RepositoryRoot: repo, StateRoot: t.TempDir(), ProducerVersion: "test"}
	socket := startMCPSocketDaemon(t, service, nil)

	for i := 0; i < 3; i++ {
		responses := mcpSocketRoundTrip(t, socket, 2,
			map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}},
			map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "tagteam_diagnostics", "arguments": map[string]any{}}},
		)
		if responses[1]["result"].(map[string]any)["isError"] != false {
			t.Fatalf("client %d diagnostics errored: %#v", i, responses[1])
		}
	}
}

func TestServeMCPSocketDurableOwnershipAcrossClientDisconnect(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	service := ControlService{RepositoryRoot: repo, StateRoot: t.TempDir(), ProducerVersion: "test"}
	runtime := NewControlRuntime(service, DefaultConfig(), nil)
	socket := startMCPSocketDaemon(t, service, runtime)

	// Client A starts a run, receives a durable handle, then disconnects.
	startResponses := mcpSocketRoundTrip(t, socket, 2,
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "tagteam_start", "arguments": controlStartFixture(t, repo)}},
	)
	startResult := startResponses[1]["result"].(map[string]any)
	if startResult["isError"] != false {
		t.Fatalf("start over socket errored: %#v", startResult)
	}
	runID, _ := startResult["structuredContent"].(map[string]any)["run_id"].(string)
	if runID == "" {
		t.Fatalf("start handle = %#v", startResult)
	}

	// Client B is a fresh connection: the run is owned by the daemon, not by the
	// originating connection, so B can still observe it after A disconnected.
	statusResponses := mcpSocketRoundTrip(t, socket, 2,
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "tagteam_status", "arguments": map[string]any{"run_id": runID}}},
	)
	statusResult := statusResponses[1]["result"].(map[string]any)
	if statusResult["isError"] != false {
		t.Fatalf("status over reconnected socket errored: %#v", statusResult)
	}
	run := statusResult["structuredContent"].(map[string]any)["run"].(map[string]any)
	if run["run_id"] != runID {
		t.Fatalf("reconnected status run_id = %v, want %q", run["run_id"], runID)
	}
}
