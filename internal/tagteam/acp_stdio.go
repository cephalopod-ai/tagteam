package tagteam

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// acpRPC is a minimal newline-delimited JSON-RPC 2.0 client for driving a
// local Agent Client Protocol (ACP) agent subprocess over stdio
// (https://agentclientprotocol.com). Framing and dispatch mirror the shared
// transport cuttlefish's ACP-speaking engines already use in production
// (packages/cuttlefish/src/engines/hermes-jsonrpc.ts): one JSON object per
// line, requests carry an "id", notifications and server-to-client requests
// are told apart by the presence of that "id".
type acpRPC struct {
	w io.Writer

	writeMu sync.Mutex
	nextID  int64

	pendingMu sync.Mutex
	pending   map[int64]chan acpResult

	// onNotify handles a peer notification (a message with "method" but no
	// "id"), e.g. "session/update".
	onNotify func(method string, params json.RawMessage)
	// onServerRequest handles a peer request (a message with both "method"
	// and "id"), e.g. "session/request_permission". The returned value is
	// marshalled as the JSON-RPC result.
	onServerRequest func(method string, params json.RawMessage) (any, error)

	closed atomic.Bool
}

type acpResult struct {
	result json.RawMessage
	err    error
}

type acpEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *acpRPCError    `json:"error,omitempty"`
}

type acpRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *acpRPCError) Error() string {
	return fmt.Sprintf("acp agent error %d: %s", e.Code, e.Message)
}

func newACPRPC(w io.Writer) *acpRPC {
	return &acpRPC{w: w, pending: map[int64]chan acpResult{}}
}

// call sends a JSON-RPC request and blocks until its response arrives, ctx
// is cancelled, or the peer's stdout stream ends (see serve).
func (a *acpRPC) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := atomic.AddInt64(&a.nextID, 1)
	ch := make(chan acpResult, 1)
	a.pendingMu.Lock()
	a.pending[id] = ch
	a.pendingMu.Unlock()

	if err := a.writeEnvelope(acpEnvelope{JSONRPC: "2.0", ID: &id, Method: method, Params: mustMarshalACP(params)}); err != nil {
		a.pendingMu.Lock()
		delete(a.pending, id)
		a.pendingMu.Unlock()
		return nil, fmt.Errorf("write %s request: %w", method, err)
	}

	select {
	case res := <-ch:
		return res.result, res.err
	case <-ctx.Done():
		a.pendingMu.Lock()
		delete(a.pending, id)
		a.pendingMu.Unlock()
		return nil, ctx.Err()
	}
}

// notify sends a one-way JSON-RPC notification (no "id", no response).
func (a *acpRPC) notify(method string, params any) error {
	return a.writeEnvelope(acpEnvelope{JSONRPC: "2.0", Method: method, Params: mustMarshalACP(params)})
}

// respond answers a peer request (session/request_permission and similar)
// previously delivered to onServerRequest.
func (a *acpRPC) respond(id int64, result any, callErr error) error {
	env := acpEnvelope{JSONRPC: "2.0", ID: &id}
	if callErr != nil {
		env.Error = &acpRPCError{Code: -32000, Message: callErr.Error()}
	} else {
		env.Result = mustMarshalACP(result)
	}
	return a.writeEnvelope(env)
}

func (a *acpRPC) writeEnvelope(env acpEnvelope) error {
	line, err := json.Marshal(env)
	if err != nil {
		return err
	}
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	if _, err := a.w.Write(append(line, '\n')); err != nil {
		return err
	}
	return nil
}

// serve reads newline-delimited JSON-RPC messages from r until it hits EOF
// or a read error, dispatching each to the matching pending call, onNotify,
// or onServerRequest. It always returns a non-nil error (io.EOF on a clean
// peer exit) and rejects every still-pending call with that error before
// returning, so callers blocked in call() are never left hanging.
func (a *acpRPC) serve(r io.Reader) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var env acpEnvelope
		if err := json.Unmarshal(line, &env); err != nil {
			continue // malformed line: skip rather than abort the whole session
		}
		a.dispatch(env)
	}
	readErr := scanner.Err()
	if readErr == nil {
		readErr = io.EOF
	}
	a.closed.Store(true)
	a.rejectPending(readErr)
	return readErr
}

func (a *acpRPC) dispatch(env acpEnvelope) {
	if env.ID != nil && env.Method == "" {
		// Response to a call() we issued.
		a.pendingMu.Lock()
		ch, ok := a.pending[*env.ID]
		if ok {
			delete(a.pending, *env.ID)
		}
		a.pendingMu.Unlock()
		if !ok {
			return
		}
		if env.Error != nil {
			ch <- acpResult{err: env.Error}
		} else {
			ch <- acpResult{result: env.Result}
		}
		return
	}
	if env.ID != nil && env.Method != "" {
		// Peer-to-us request, e.g. session/request_permission.
		id := *env.ID
		if a.onServerRequest == nil {
			_ = a.respond(id, map[string]any{}, nil)
			return
		}
		result, err := a.onServerRequest(env.Method, env.Params)
		_ = a.respond(id, result, err)
		return
	}
	if env.Method != "" && a.onNotify != nil {
		a.onNotify(env.Method, env.Params)
	}
}

func (a *acpRPC) rejectPending(err error) {
	a.pendingMu.Lock()
	defer a.pendingMu.Unlock()
	for id, ch := range a.pending {
		ch <- acpResult{err: err}
		delete(a.pending, id)
	}
}

func mustMarshalACP(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return raw
}
