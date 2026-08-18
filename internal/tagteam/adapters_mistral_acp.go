package tagteam

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// MistralAcpAdapter drives Mistral's `vibe-acp` binary — the ACP-over-stdio
// entrypoint shipped by the `mistral-vibe` package (`uv tool install
// mistral-vibe`; `vibe --setup` to authenticate) — as a review-only Agent
// Client Protocol (https://agentclientprotocol.com) client.
//
// It intentionally mirrors the fleet's other two working ACP integrations
// rather than inventing new wire behavior: gosling's Rust client
// (crates/gosling/src/providers/vibe_acp.rs, crates/gosling/src/acp/provider.rs)
// and cuttlefish's TypeScript client (packages/cuttlefish/src/engines/vibe-acp.ts,
// hermes-jsonrpc.ts, vibe-protocol.ts). Method names, params shapes, and the
// permission auto-approve behavior are taken from those two verified,
// production integrations rather than from protocol docs alone.
//
// Scope is deliberately narrower than gosling's/cuttlefish's: this adapter
// only supports the read-only reviewer/scout roles (like OpenAICompatibleAdapter),
// never RoleCoder, and it requests Vibe's most restrictive "plan" session
// mode rather than "auto-approve". A single tagteam invocation is one
// request/response turn, not an interactive multi-turn chat session, so
// there is no persistent session to reuse across calls.
type MistralAcpAdapter struct {
	// Binary overrides the resolved executable name; defaults to "vibe-acp".
	Binary string
	// SessionMode overrides Vibe's session/set_mode value; defaults to
	// "plan" (Vibe's most restrictive, read-only-exploration mode — see
	// vibe_acp.rs's vibe_mode_mapping doc comment for the full mode table
	// and its caveat that even "plan" can write a plan file under
	// ~/.vibe/plans/).
	SessionMode  string
	DefaultModel string
	ExtraArgs    []string
	EnvOverlay   map[string]string
}

func (a *MistralAcpAdapter) ID() string { return "mistral-acp" }

func (a *MistralAcpAdapter) Capabilities() CapabilitySet { return CapabilitySet{} }

func (a *MistralAcpAdapter) binary() string {
	if strings.TrimSpace(a.Binary) != "" {
		return a.Binary
	}
	return "vibe-acp"
}

func (a *MistralAcpAdapter) sessionMode() string {
	if strings.TrimSpace(a.SessionMode) != "" {
		return a.SessionMode
	}
	return "plan"
}

func (a *MistralAcpAdapter) Detect(ctx context.Context) (VersionInfo, error) {
	return detectBinary(ctx, a.binary(), []string{"--version"},
		"install the Mistral Vibe CLI (`uv tool install mistral-vibe`), authenticate with `vibe --setup`, "+
			"and confirm the ACP adapter is on PATH (`vibe-acp --version`)")
}

func unsupportedMistralAcpRoleError() error {
	return fmt.Errorf("mistral-acp adapter is read-only in this version; use it as reviewer/adversary or scout, not as coder/worker")
}

func (a *MistralAcpAdapter) BuildCmd(role Role, req Request) (*CommandSpec, error) {
	if role != RoleAdversary && role != RoleScout {
		return nil, unsupportedMistralAcpRoleError()
	}
	// Cosmetic only, like OpenAICompatibleAdapter's BuildCmd: RunDirect owns
	// the actual subprocess/session lifecycle. This is used for delivery
	// records and --dry-run display.
	argv := append([]string{a.binary()}, a.ExtraArgs...)
	return &CommandSpec{Argv: argv, Dir: req.Workdir, Output: req.OutputPath}, nil
}

func (a *MistralAcpAdapter) ParseResult(role Role, raw []byte) (Result, error) {
	if role != RoleAdversary && role != RoleScout {
		return Result{}, &OutputContractError{Err: unsupportedMistralAcpRoleError()}
	}
	if strings.TrimSpace(string(raw)) == "" {
		return Result{}, &OutputContractError{Err: fmt.Errorf("mistral-acp turn produced no assistant text")}
	}
	if role == RoleScout {
		scout, err := parseScout(raw)
		if err != nil {
			return Result{}, err
		}
		return Result{Raw: raw, Text: scout.Summary, Scout: scout}, nil
	}
	review, err := parseReviewPayloadLabeled(raw, "mistral-acp")
	if err != nil {
		return Result{}, err
	}
	return Result{Raw: raw, Text: review.Summary, Review: review}, nil
}

// acpSessionUpdate is the subset of ACP's session/update notification this
// adapter reads: the discriminant field ("sessionUpdate") and the assistant
// text carried by agent_message_chunk updates. Shape confirmed against
// cuttlefish's vibe-protocol.ts (mapSessionUpdate), which shares the same
// vocabulary between its Hermes and Vibe ACP engines.
type acpSessionUpdate struct {
	SessionID string `json:"sessionId"`
	Update    struct {
		Kind    string          `json:"sessionUpdate"`
		Content json.RawMessage `json:"content"`
		Text    json.RawMessage `json:"text"`
	} `json:"update"`
}

func acpMessageChunkText(u acpSessionUpdate) string {
	if u.Update.Kind != "agent_message_chunk" && u.Update.Kind != "agent_message_text" {
		return ""
	}
	if text := acpContentText(u.Update.Content); text != "" {
		return text
	}
	return acpContentText(u.Update.Text)
}

// acpContentText reads a ContentBlock's text whether it arrives as a bare
// JSON string or as {"type":"text","text":"..."}.
func acpContentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var block struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &block); err == nil {
		return block.Text
	}
	return ""
}

// acpPermissionOption mirrors PermissionOption from the ACP schema
// (optionId/name/kind), as sent inside a session/request_permission request.
type acpPermissionOption struct {
	OptionID string `json:"optionId"`
	Kind     string `json:"kind"`
}

// selectACPPermissionOutcome auto-approves a session/request_permission
// call the way cuttlefish's vibe-acp/hermes-acp engines already do ("shared
// HermesRpc transport and permission auto-approve behavior" — see
// vibe-acp.ts), since this adapter only ever runs read-only reviewer/scout
// turns with no human present to prompt. It prefers the least-privileged
// option Vibe offers (a one-shot allow) over a standing allow_always grant.
func selectACPPermissionOutcome(params json.RawMessage) map[string]any {
	var req struct {
		Options []acpPermissionOption `json:"options"`
	}
	_ = json.Unmarshal(params, &req)
	optionID := ""
	for _, kind := range []string{"allow_once", "allow_always"} {
		for _, opt := range req.Options {
			if opt.Kind == kind {
				optionID = opt.OptionID
				break
			}
		}
		if optionID != "" {
			break
		}
	}
	if optionID == "" && len(req.Options) > 0 {
		optionID = req.Options[0].OptionID
	}
	if optionID == "" {
		return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}
	}
	return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": optionID}}
}

func (a *MistralAcpAdapter) RunDirect(role Role, req Request) (Result, error) {
	if role != RoleAdversary && role != RoleScout {
		return Result{}, &ExitError{Code: ExitInvalidArguments, Err: unsupportedMistralAcpRoleError()}
	}

	runCtx := req.Context
	if runCtx == nil {
		runCtx = context.Background()
	}
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(runCtx, req.Timeout)
		defer cancel()
	}
	procCtx, stopProc := context.WithCancel(runCtx)
	defer stopProc()

	argv := append([]string{a.binary()}, a.ExtraArgs...)
	cmd := execCommandContext(procCtx, argv[0], argv[1:]...)
	prepareProcessTree(cmd)
	cmd.Dir = req.Workdir
	cmd.Env = mergeRestrictedCommandEnv(joinEnvOverlay(a.EnvOverlay, req.EnvOverlay), nil)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return Result{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, err
	}
	if err := cmd.Start(); err != nil {
		return Result{}, &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("mistral-acp not runnable; try `uv tool install mistral-vibe` and `vibe --setup`: %w", err)}
	}

	rpc := newACPRPC(stdin)
	var transcript strings.Builder
	rpc.onNotify = func(method string, params json.RawMessage) {
		if method != "session/update" {
			return
		}
		var update acpSessionUpdate
		if err := json.Unmarshal(params, &update); err != nil {
			return
		}
		transcript.WriteString(acpMessageChunkText(update))
	}
	rpc.onServerRequest = func(method string, params json.RawMessage) (any, error) {
		if method == "session/request_permission" {
			return selectACPPermissionOutcome(params), nil
		}
		return map[string]any{}, nil
	}

	serveDone := make(chan error, 1)
	go func() { serveDone <- rpc.serve(stdout) }()

	result, runErr := a.runACPTurn(procCtx, rpc, role, req, &transcript)

	stopProc()
	_ = stdin.Close()
	<-serveDone
	_ = cmd.Wait()

	if runErr != nil {
		return Result{}, runErr
	}
	return result, nil
}

// runACPTurn drives the initialize -> session/new -> (best-effort
// session/set_mode / session/set_model) -> session/prompt handshake and
// returns the parsed Result. session/set_mode and session/set_model are
// tolerated failures (matches cuttlefish's `.catch(() => {})`): Vibe's own
// session/new response does not name protocol-version-guaranteed support
// for either, only that a live vibe-acp session accepts them.
func (a *MistralAcpAdapter) runACPTurn(ctx context.Context, rpc *acpRPC, role Role, req Request, transcript *strings.Builder) (Result, error) {
	if _, err := rpc.call(ctx, "initialize", map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{},
	}); err != nil {
		return Result{}, &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("mistral-acp initialize failed: %w", redactACPError(err, req.EnvOverlay))}
	}

	newSessionRaw, err := rpc.call(ctx, "session/new", map[string]any{
		"cwd":        req.Workdir,
		"mcpServers": []any{},
	})
	if err != nil {
		return Result{}, &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("mistral-acp session/new failed: %w", redactACPError(err, req.EnvOverlay))}
	}
	var newSession struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(newSessionRaw, &newSession); err != nil || strings.TrimSpace(newSession.SessionID) == "" {
		return Result{}, &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("mistral-acp session/new returned no sessionId")}
	}
	sessionID := newSession.SessionID

	_, _ = rpc.call(ctx, "session/set_mode", map[string]any{"sessionId": sessionID, "modeId": a.sessionMode()})

	model := req.Model
	if model == "" {
		model = a.DefaultModel
	}
	if model != "" {
		_, _ = rpc.call(ctx, "session/set_model", map[string]any{"sessionId": sessionID, "modelId": model})
	}

	promptRaw, err := rpc.call(ctx, "session/prompt", map[string]any{
		"sessionId": sessionID,
		"prompt":    []map[string]string{{"type": "text", "text": string(promptStdin(req))}},
	})
	if err != nil {
		return Result{}, &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("mistral-acp session/prompt failed: %w", redactACPError(err, req.EnvOverlay))}
	}
	var promptResult struct {
		StopReason string `json:"stopReason"`
	}
	_ = json.Unmarshal(promptRaw, &promptResult)

	raw := []byte(transcript.String())
	if len(strings.TrimSpace(string(raw))) == 0 {
		switch promptResult.StopReason {
		case "refusal", "cancelled":
			return Result{}, &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("mistral-acp turn ended: %s", promptResult.StopReason)}
		}
	}

	result, parseErr := a.ParseResult(role, raw)
	if parseErr != nil {
		return Result{Raw: raw}, &directValidationError{cause: parseErr, raw: raw}
	}
	result.SessionID = sessionID
	result.Command = []string{a.binary(), "session/prompt"}
	return result, nil
}

func redactACPError(err error, overlay map[string]string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s", redactSecretsWithOverlay(err.Error(), overlay))
}

func joinEnvOverlay(base, extra map[string]string) map[string]string {
	if len(base) == 0 {
		return extra
	}
	if len(extra) == 0 {
		return base
	}
	merged := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range extra {
		merged[k] = v
	}
	return merged
}
