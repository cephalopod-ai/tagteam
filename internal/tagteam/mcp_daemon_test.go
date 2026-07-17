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

func TestServeMCPSocketClientDisconnectDoesNotCancelDaemonJob(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	service := ControlService{RepositoryRoot: repo, StateRoot: t.TempDir(), ProducerVersion: "test"}
	runtime := NewControlRuntime(service, DefaultConfig(), nil)
	jobContext, cancelJob := context.WithCancel(context.Background())
	runtime.registerJob("daemon-owned-job", cancelJob)
	t.Cleanup(func() {
		cancelJob()
		runtime.unregisterJob("daemon-owned-job")
	})
	socket := startMCPSocketDaemon(t, service, runtime)

	// The round trip closes its client connection after initialization. A socket
	// session borrows the daemon runtime, so that disconnect must not cancel a
	// daemon-owned job.
	mcpSocketRoundTrip(t, socket, 1,
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}},
	)
	select {
	case <-jobContext.Done():
		t.Fatal("client disconnect cancelled a daemon-owned job")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestControlRuntimeCloseWaitsForTrackedWorkers(t *testing.T) {
	runtime := NewControlRuntime(ControlService{}, DefaultConfig(), nil)
	jobContext, cancelJob := context.WithCancel(context.Background())
	release := make(chan struct{})
	runtime.startJob("tracked-worker", cancelJob, func() {
		<-jobContext.Done()
		<-release
	})

	closed := make(chan struct{})
	go func() {
		runtime.Close()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("runtime Close returned before the tracked worker exited")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("runtime Close did not return after the tracked worker exited")
	}
}

func TestControlRuntimeStartFailsAfterClose(t *testing.T) {
	runtime := NewControlRuntime(ControlService{}, DefaultConfig(), nil)
	runtime.Close()

	_, err := runtime.Start(context.Background(), ControlStartRequest{})
	startErr, ok := err.(*ControlStartError)
	if !ok || startErr.ReasonCode != "runtime_closed" {
		t.Fatalf("Start after Close error = %#v, want runtime_closed", err)
	}
}

func TestListenMCPUnixSocketFailsClosedWhenPermissionsCannotBeSet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.sock")
	listener, err := listenMCPUnixSocket(path, func(string, os.FileMode) error {
		return os.ErrPermission
	})
	if err == nil {
		if listener != nil {
			_ = listener.Close()
		}
		t.Fatal("socket listener succeeded after chmod failure")
	}
	if listener != nil {
		t.Fatal("chmod failure returned a live listener")
	}
	if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
		t.Fatalf("socket path survived chmod failure: %v", statErr)
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

	// Client B is a fresh connection. It must observe the run's expected
	// preflight failure, never a cancellation caused by client A disconnecting.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
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
		switch run["status"] {
		case string(RunStatusFailed):
			return
		case string(RunStatusCancelled):
			t.Fatal("client disconnect cancelled the daemon-owned run")
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("daemon-owned run %q did not reach its expected terminal failure", runID)
}
