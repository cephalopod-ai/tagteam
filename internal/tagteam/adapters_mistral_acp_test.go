package tagteam

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRegistryIncludesMistralAcp(t *testing.T) {
	registry := Registry(DefaultConfig(), RunOptions{})
	adapter, ok := registry["mistral-acp"]
	if !ok {
		t.Fatal("expected mistral-acp adapter in registry")
	}
	if adapter.ID() != "mistral-acp" {
		t.Fatalf("adapter ID = %q", adapter.ID())
	}
}

func TestMistralAcpBuildCmdRejectsCoderRole(t *testing.T) {
	adapter := &MistralAcpAdapter{}
	if _, err := adapter.BuildCmd(RoleCoder, Request{}); err == nil {
		t.Fatal("expected coder role error")
	} else if !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("error = %v", err)
	}
	if _, err := adapter.RunDirect(RoleCoder, Request{}); err == nil {
		t.Fatal("expected direct coder role error")
	} else if !strings.Contains(err.Error(), "not as coder/worker") {
		t.Fatalf("error = %v", err)
	}
}

func TestMistralAcpBuildCmdScoutUsesBinaryAndExtraArgs(t *testing.T) {
	adapter := &MistralAcpAdapter{Binary: "vibe-acp", ExtraArgs: []string{"--foo"}}
	spec, err := adapter.BuildCmd(RoleScout, Request{Workdir: "/repo", OutputPath: "/tmp/out.json"})
	if err != nil {
		t.Fatalf("BuildCmd() error = %v", err)
	}
	want := []string{"vibe-acp", "--foo"}
	if len(spec.Argv) != len(want) || spec.Argv[0] != want[0] || spec.Argv[1] != want[1] {
		t.Fatalf("argv = %#v", spec.Argv)
	}
	if spec.Dir != "/repo" || spec.Output != "/tmp/out.json" {
		t.Fatalf("spec = %#v", spec)
	}
}

func TestMistralAcpDetectMissingBinary(t *testing.T) {
	adapter := &MistralAcpAdapter{Binary: "tagteam-test-nonexistent-vibe-acp"}
	info, err := adapter.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if info.Found || info.Runnable {
		t.Fatalf("missing binary should not be found/runnable: %#v", info)
	}
}

func TestMistralAcpDefaultsBinaryAndSessionMode(t *testing.T) {
	adapter := &MistralAcpAdapter{}
	if got := adapter.binary(); got != "vibe-acp" {
		t.Fatalf("binary() = %q", got)
	}
	if got := adapter.sessionMode(); got != "plan" {
		t.Fatalf("sessionMode() = %q", got)
	}
}

func TestMistralAcpParseResultRejectsEmptyTranscript(t *testing.T) {
	adapter := &MistralAcpAdapter{}
	if _, err := adapter.ParseResult(RoleAdversary, []byte("  ")); err == nil {
		t.Fatal("expected error for empty transcript")
	}
}

func TestAcpMessageChunkText(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"bare string content", `{"sessionId":"s","update":{"sessionUpdate":"agent_message_chunk","content":"hello"}}`, "hello"},
		{"object content", `{"sessionId":"s","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi there"}}}`, "hi there"},
		{"non message update ignored", `{"sessionId":"s","update":{"sessionUpdate":"plan"}}`, ""},
		{"thought chunk ignored", `{"sessionId":"s","update":{"sessionUpdate":"agent_thought_chunk","content":"secret plan"}}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var update acpSessionUpdate
			if err := json.Unmarshal([]byte(tc.raw), &update); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := acpMessageChunkText(update); got != tc.want {
				t.Fatalf("acpMessageChunkText() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSelectACPPermissionOutcomePrefersAllowOnce(t *testing.T) {
	params := json.RawMessage(`{"options":[{"optionId":"reject-1","kind":"reject_once"},{"optionId":"allow-always-1","kind":"allow_always"},{"optionId":"allow-once-1","kind":"allow_once"}]}`)
	outcome := selectACPPermissionOutcome(params)
	inner, ok := outcome["outcome"].(map[string]any)
	if !ok || inner["outcome"] != "selected" || inner["optionId"] != "allow-once-1" {
		t.Fatalf("outcome = %#v", outcome)
	}
}

func TestSelectACPPermissionOutcomeFallsBackToFirstOption(t *testing.T) {
	params := json.RawMessage(`{"options":[{"optionId":"only-option","kind":"reject_once"}]}`)
	outcome := selectACPPermissionOutcome(params)
	inner := outcome["outcome"].(map[string]any)
	if inner["outcome"] != "selected" || inner["optionId"] != "only-option" {
		t.Fatalf("outcome = %#v", outcome)
	}
}

func TestSelectACPPermissionOutcomeCancelsWhenNoOptions(t *testing.T) {
	outcome := selectACPPermissionOutcome(json.RawMessage(`{"options":[]}`))
	inner := outcome["outcome"].(map[string]any)
	if inner["outcome"] != "cancelled" {
		t.Fatalf("outcome = %#v", outcome)
	}
}

// --- acpRPC transport tests --------------------------------------------

func TestACPRPCCallMatchesResponseByID(t *testing.T) {
	agentReader, clientWriter := io.Pipe()
	clientReader, agentWriter := io.Pipe()
	rpc := newACPRPC(clientWriter)
	go func() { _ = rpc.serve(clientReader) }()

	go func() {
		reader := bufio.NewReader(agentReader)
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		var req acpEnvelope
		_ = json.Unmarshal([]byte(line), &req)
		resp := acpEnvelope{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{"ok":true}`)}
		b, _ := json.Marshal(resp)
		_, _ = agentWriter.Write(append(b, '\n'))
	}()

	result, err := rpc.call(context.Background(), "ping", map[string]any{"x": 1})
	if err != nil {
		t.Fatalf("call() error = %v", err)
	}
	if string(result) != `{"ok":true}` {
		t.Fatalf("result = %s", result)
	}
}

func TestACPRPCServeRejectsPendingCallOnEOF(t *testing.T) {
	agentReader, clientWriter := io.Pipe()
	clientReader, agentWriter := io.Pipe()
	rpc := newACPRPC(clientWriter)
	go func() { _ = rpc.serve(clientReader) }()
	// The agent side never replies in this test, but something must still
	// drain what the client writes or call()'s blocking Write on the
	// unbuffered pipe would hang forever before it ever reaches its select.
	go func() { _, _ = io.Copy(io.Discard, agentReader) }()

	errCh := make(chan error, 1)
	go func() {
		_, err := rpc.call(context.Background(), "ping", nil)
		errCh <- err
	}()
	// Give call() time to register itself as pending before the peer exits.
	time.Sleep(20 * time.Millisecond)
	_ = agentWriter.Close()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected an error when the peer's stream ends")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("call() did not return after peer EOF")
	}
}

func TestACPRPCServerRequestDispatchesAndResponds(t *testing.T) {
	agentReader, clientWriter := io.Pipe()
	clientReader, agentWriter := io.Pipe()
	rpc := newACPRPC(clientWriter)
	var gotMethod string
	rpc.onServerRequest = func(method string, params json.RawMessage) (any, error) {
		gotMethod = method
		return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": "allow_once"}}, nil
	}
	go func() { _ = rpc.serve(clientReader) }()

	id := int64(7)
	req := acpEnvelope{JSONRPC: "2.0", ID: &id, Method: "session/request_permission", Params: json.RawMessage(`{}`)}
	b, _ := json.Marshal(req)
	go func() { _, _ = agentWriter.Write(append(b, '\n')) }()

	reader := bufio.NewReader(agentReader)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	var resp acpEnvelope
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.ID == nil || *resp.ID != 7 {
		t.Fatalf("response id = %#v", resp.ID)
	}
	if gotMethod != "session/request_permission" {
		t.Fatalf("gotMethod = %q", gotMethod)
	}
}

// --- RunDirect integration against a fake vibe-acp subprocess ----------
//
// TestMistralAcpFakeAgentHelperProcess re-executes this test binary as the
// ACP agent subprocess (the same self-exec pattern os/exec's own tests use
// for TestHelperProcess). It is a no-op under a normal `go test` run and
// only speaks the protocol when TAGTEAM_MISTRAL_ACP_FAKE_AGENT=1 is set,
// which the RunDirect tests below do via Request.EnvOverlay.

func TestMistralAcpFakeAgentHelperProcess(t *testing.T) {
	if os.Getenv("TAGTEAM_MISTRAL_ACP_FAKE_AGENT") != "1" {
		return
	}
	runMistralAcpFakeAgent(os.Getenv("TAGTEAM_MISTRAL_ACP_FAKE_MODE"))
	os.Exit(0)
}

func runMistralAcpFakeAgent(mode string) {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	write := func(v map[string]any) {
		v["jsonrpc"] = "2.0"
		b, err := json.Marshal(v)
		if err != nil {
			return
		}
		_, _ = os.Stdout.Write(append(b, '\n'))
	}
	for scanner.Scan() {
		var req struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}
		switch req.Method {
		case "initialize":
			write(map[string]any{"id": req.ID, "result": map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{}, "authMethods": []any{}}})
		case "session/new":
			write(map[string]any{"id": req.ID, "result": map[string]any{"sessionId": "fake-session-1"}})
		case "session/set_mode":
			if mode == "set_mode_error" {
				write(map[string]any{"id": req.ID, "error": map[string]any{"code": -32001, "message": "set_mode not supported"}})
				continue
			}
			write(map[string]any{"id": req.ID, "result": map[string]any{}})
		case "session/set_model":
			write(map[string]any{"id": req.ID, "result": map[string]any{}})
		case "session/prompt":
			fakeAgentRespondToPrompt(mode, req.ID, scanner, write)
		}
	}
}

func fakeAgentRespondToPrompt(mode string, promptID *int64, scanner *bufio.Scanner, write func(map[string]any)) {
	update := func(content any) {
		write(map[string]any{"method": "session/update", "params": map[string]any{
			"sessionId": "fake-session-1",
			"update":    map[string]any{"sessionUpdate": "agent_message_chunk", "content": content},
		}})
	}
	switch mode {
	case "refusal":
		write(map[string]any{"id": promptID, "result": map[string]any{"stopReason": "refusal"}})
	case "flood":
		// One chunk, deliberately far larger than the tiny MaxOutputBytes the
		// flood test configures, so the client's bounded transcript trips on
		// the first update and kills this process before any reply is sent.
		update(map[string]any{"type": "text", "text": strings.Repeat("x", 64*1024)})
		write(map[string]any{"id": promptID, "result": map[string]any{"stopReason": "end_turn"}})
	case "permission":
		write(map[string]any{"id": int64(9001), "method": "session/request_permission", "params": map[string]any{
			"sessionId": "fake-session-1",
			"options": []map[string]any{
				{"optionId": "reject-once-1", "kind": "reject_once"},
				{"optionId": "allow-once-1", "kind": "allow_once"},
			},
		}})
		scanner.Scan() // drain the client's response before finishing the turn
		fallthrough
	case "scout":
		update(map[string]any{"type": "text", "text": `{"schema_version":1,"mode":"recon","summary":"fake scout summary","relevant_files":["a.go"],"likely_entry_points":[],"existing_patterns":[],"risks":[],"suggested_tests":[],"do_not_block":true}`})
		write(map[string]any{"id": promptID, "result": map[string]any{"stopReason": "end_turn"}})
	default: // "review"
		update(map[string]any{"type": "text", "text": `{"schema_version":1,"verdict":"pass","summary":"fake review summary","findings":[],"test_suggestions":[]}`})
		write(map[string]any{"id": promptID, "result": map[string]any{"stopReason": "end_turn"}})
	}
}

func fakeMistralAcpAdapter(t *testing.T, mode string) *MistralAcpAdapter {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable(): %v", err)
	}
	return &MistralAcpAdapter{
		Binary:    self,
		ExtraArgs: []string{"-test.run=^TestMistralAcpFakeAgentHelperProcess$"},
		EnvOverlay: map[string]string{
			"TAGTEAM_MISTRAL_ACP_FAKE_AGENT": "1",
			"TAGTEAM_MISTRAL_ACP_FAKE_MODE":  mode,
		},
	}
}

func TestMistralAcpRunDirectReviewerRole(t *testing.T) {
	adapter := fakeMistralAcpAdapter(t, "review")
	result, err := adapter.RunDirect(RoleAdversary, Request{
		Context: context.Background(),
		Prompt:  "review this change",
		Workdir: t.TempDir(),
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("RunDirect() error = %v", err)
	}
	if result.Review == nil || result.Review.Summary != "fake review summary" || result.Review.Verdict != "pass" {
		t.Fatalf("review = %#v", result.Review)
	}
	if result.SessionID != "fake-session-1" {
		t.Fatalf("session id = %q", result.SessionID)
	}
}

func TestMistralAcpRunDirectScoutRole(t *testing.T) {
	adapter := fakeMistralAcpAdapter(t, "scout")
	result, err := adapter.RunDirect(RoleScout, Request{
		Context: context.Background(),
		Prompt:  "map this repo",
		Workdir: t.TempDir(),
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("RunDirect() error = %v", err)
	}
	if result.Scout == nil || result.Scout.Summary != "fake scout summary" {
		t.Fatalf("scout = %#v", result.Scout)
	}
}

func TestMistralAcpRunDirectAutoApprovesPermissionRequest(t *testing.T) {
	adapter := fakeMistralAcpAdapter(t, "permission")
	result, err := adapter.RunDirect(RoleScout, Request{
		Context: context.Background(),
		Prompt:  "map this repo",
		Workdir: t.TempDir(),
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("RunDirect() error = %v", err)
	}
	if result.Scout == nil || result.Scout.Summary != "fake scout summary" {
		t.Fatalf("scout = %#v", result.Scout)
	}
}

func TestMistralAcpRunDirectSurfacesRefusal(t *testing.T) {
	adapter := fakeMistralAcpAdapter(t, "refusal")
	_, err := adapter.RunDirect(RoleAdversary, Request{
		Context: context.Background(),
		Prompt:  "review this change",
		Workdir: t.TempDir(),
		Timeout: 10 * time.Second,
	})
	if err == nil {
		t.Fatal("expected an error for a refused turn with no assistant text")
	}
	if !strings.Contains(err.Error(), "refusal") {
		t.Fatalf("error = %v", err)
	}
}

func TestMistralAcpRunDirectFailsWhenReadOnlyModeCannotBeSelected(t *testing.T) {
	adapter := fakeMistralAcpAdapter(t, "set_mode_error")
	_, err := adapter.RunDirect(RoleAdversary, Request{
		Context: context.Background(),
		Prompt:  "review this change",
		Workdir: t.TempDir(),
		Timeout: 10 * time.Second,
	})
	if err == nil {
		t.Fatal("expected an error when session/set_mode fails")
	}
	if !strings.Contains(err.Error(), "read-only session mode") {
		t.Fatalf("error = %v, want a read-only-mode failure, not a silent fallback into an unknown mode", err)
	}
}

func TestMistralAcpRunDirectBoundsStreamedTranscript(t *testing.T) {
	adapter := fakeMistralAcpAdapter(t, "flood")
	_, err := adapter.RunDirect(RoleAdversary, Request{
		Context:        context.Background(),
		Prompt:         "review this change",
		Workdir:        t.TempDir(),
		Timeout:        10 * time.Second,
		MaxOutputBytes: 256,
	})
	if err == nil {
		t.Fatal("expected an output-limit error for a flooding agent")
	}
	if !strings.Contains(err.Error(), "mistral-acp") || !strings.Contains(err.Error(), "max_output_bytes") {
		t.Fatalf("error = %v, want an output-limit error naming mistral-acp", err)
	}
}
