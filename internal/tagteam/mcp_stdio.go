package tagteam

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

const (
	MCPProtocolVersion = "2025-11-25"
	mcpMaxMessageBytes = 1024 * 1024
)

type MCPStdioServer struct {
	service ControlService
	in      io.Reader
	out     io.Writer
	mu      sync.Mutex
}

func NewMCPStdioServer(service ControlService, in io.Reader, out io.Writer) *MCPStdioServer {
	return &MCPStdioServer{service: service, in: in, out: out}
}

// Serve runs a newline-delimited MCP stdio session. It intentionally exposes
// only control operations with implemented, non-mutating handlers.
func (s *MCPStdioServer) Serve(ctx context.Context) error {
	scanner := bufio.NewScanner(s.in)
	scanner.Buffer(make([]byte, 0, 64*1024), mcpMaxMessageBytes)
	initialized := false
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var request mcpRequest
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			if writeErr := s.writeError(nil, -32700, "parse error"); writeErr != nil {
				return writeErr
			}
			continue
		}
		if request.JSONRPC != "2.0" || request.Method == "" {
			if request.ID != nil {
				if err := s.writeError(request.ID, -32600, "invalid request"); err != nil {
					return err
				}
			}
			continue
		}

		switch request.Method {
		case "initialize":
			if request.ID == nil {
				continue
			}
			initialized = true
			if err := s.writeResult(request.ID, map[string]any{
				"protocolVersion": MCPProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]string{"name": "tagteam", "version": normalizedProducerVersion(s.service.ProducerVersion)},
				"instructions":    "Use Tagteam control tools for bounded status and diagnostics. Start, resume, and cancel are not available in this server revision.",
			}); err != nil {
				return err
			}
		case "notifications/initialized":
			continue
		case "ping":
			if request.ID != nil {
				if err := s.writeResult(request.ID, map[string]any{}); err != nil {
					return err
				}
			}
		case "tools/list":
			if !initialized {
				if request.ID != nil {
					if err := s.writeError(request.ID, -32002, "server not initialized"); err != nil {
						return err
					}
				}
				continue
			}
			if request.ID == nil {
				continue
			}
			if err := s.writeResult(request.ID, map[string]any{"tools": mcpControlTools()}); err != nil {
				return err
			}
		case "tools/call":
			if !initialized {
				if request.ID != nil {
					if err := s.writeError(request.ID, -32002, "server not initialized"); err != nil {
						return err
					}
				}
				continue
			}
			if request.ID == nil {
				continue
			}
			result, err := s.callTool(request.Params)
			if err != nil {
				result = mcpToolFailure(err)
			}
			if err := s.writeResult(request.ID, result); err != nil {
				return err
			}
		default:
			if request.ID != nil {
				if err := s.writeError(request.ID, -32601, "method not found"); err != nil {
					return err
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read MCP stdio message: %w", err)
	}
	return nil
}

func (s *MCPStdioServer) callTool(raw json.RawMessage) (map[string]any, error) {
	var call mcpToolCall
	if err := json.Unmarshal(raw, &call); err != nil {
		return nil, fmt.Errorf("invalid tools/call parameters")
	}
	switch call.Name {
	case "tagteam_capabilities":
		return mcpToolSuccess(s.service.Capabilities())
	case "tagteam_validate_launch":
		var spec ControlLaunchSpec
		if err := json.Unmarshal(call.Arguments, &spec); err != nil {
			return nil, fmt.Errorf("invalid launch specification")
		}
		result, err := s.service.ValidateLaunch(spec)
		if err != nil {
			return nil, err
		}
		return mcpToolSuccess(result)
	case "tagteam_status":
		var input struct {
			RunID string `json:"run_id"`
		}
		if err := json.Unmarshal(call.Arguments, &input); err != nil {
			return nil, fmt.Errorf("invalid status arguments")
		}
		result, err := s.service.Status(input.RunID)
		if err != nil {
			return nil, err
		}
		return mcpToolSuccess(result)
	case "tagteam_plan":
		var input mcpPagedRunInput
		if err := json.Unmarshal(call.Arguments, &input); err != nil {
			return nil, fmt.Errorf("invalid plan arguments")
		}
		if input.Limit == 0 {
			input.Limit = 50
		}
		result, err := s.service.Plan(input.RunID, input.Cursor, input.Limit)
		if err != nil {
			return nil, err
		}
		return mcpToolSuccess(result)
	case "tagteam_findings":
		var input mcpPagedRunInput
		if err := json.Unmarshal(call.Arguments, &input); err != nil {
			return nil, fmt.Errorf("invalid findings arguments")
		}
		if input.Limit == 0 {
			input.Limit = 50
		}
		result, err := s.service.Findings(input.RunID, input.Cursor, input.Limit)
		if err != nil {
			return nil, err
		}
		return mcpToolSuccess(result)
	case "tagteam_diagnostics":
		result, err := s.service.Diagnostics()
		if err != nil {
			return nil, err
		}
		return mcpToolSuccess(result)
	default:
		return nil, fmt.Errorf("unknown Tagteam tool %q", call.Name)
	}
}

func (s *MCPStdioServer) writeResult(id json.RawMessage, result any) error {
	return s.write(mcpResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func (s *MCPStdioServer) writeError(id json.RawMessage, code int, message string) error {
	return s.write(mcpResponse{JSONRPC: "2.0", ID: id, Error: &mcpError{Code: code, Message: message}})
}

func (s *MCPStdioServer) write(response mcpResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	payload, err := json.Marshal(response)
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	_, err = s.out.Write(payload)
	return err
}

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *mcpError       `json:"error,omitempty"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpToolCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type mcpPagedRunInput struct {
	RunID  string `json:"run_id"`
	Cursor string `json:"cursor"`
	Limit  int    `json:"limit"`
}

func mcpToolSuccess(value any) (map[string]any, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"content":           []map[string]string{{"type": "text", "text": string(payload)}},
		"structuredContent": value,
		"isError":           false,
	}, nil
}

func mcpToolFailure(err error) map[string]any {
	payload, _ := json.Marshal(map[string]string{"error": err.Error()})
	return map[string]any{
		"content":           []map[string]string{{"type": "text", "text": string(payload)}},
		"structuredContent": map[string]string{"error": err.Error()},
		"isError":           true,
	}
}

func mcpControlTools() []map[string]any {
	readOnly := map[string]bool{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false}
	return []map[string]any{
		mcpTool("tagteam_capabilities", "Read Tagteam control-plane capabilities.", map[string]any{"type": "object", "additionalProperties": false}, readOnly),
		mcpTool("tagteam_validate_launch", "Validate and normalize a Tagteam launch without starting it.", mcpLaunchSchema(), readOnly),
		mcpTool("tagteam_status", "Read bounded status for one Tagteam run.", mcpRunSchema(false), readOnly),
		mcpTool("tagteam_plan", "Read a bounded page of a run plan.", mcpRunSchema(true), readOnly),
		mcpTool("tagteam_findings", "Read a bounded page of persisted findings.", mcpRunSchema(true), readOnly),
		mcpTool("tagteam_diagnostics", "Verify repository identity and state-root access without writing state.", map[string]any{"type": "object", "additionalProperties": false}, readOnly),
	}
}

func mcpLaunchSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"schema_version", "repository", "prompt", "team", "allowed_paths", "rounds", "time_budget", "recovery_policy"},
		"properties": map[string]any{
			"schema_version":  map[string]any{"type": "integer", "const": ControlContractVersion},
			"repository":      map[string]any{"type": "object"},
			"prompt":          map[string]any{"type": "string", "minLength": 1, "maxLength": controlMaxPromptBytes},
			"team":            map[string]any{"type": "object"},
			"allowed_paths":   map[string]any{"type": "array", "minItems": 1, "maxItems": controlMaxAllowedPaths, "items": map[string]any{"type": "string"}},
			"rounds":          map[string]any{"type": "integer", "minimum": 1, "maximum": controlMaxRounds},
			"time_budget":     map[string]any{"type": "object"},
			"test_preset":     map[string]any{"type": "string"},
			"recovery_policy": map[string]any{"type": "string", "const": "assist"},
		},
	}
}

func mcpTool(name, description string, schema map[string]any, annotations map[string]bool) map[string]any {
	return map[string]any{"name": name, "description": description, "inputSchema": schema, "annotations": annotations}
}

func mcpRunSchema(paged bool) map[string]any {
	properties := map[string]any{"run_id": map[string]any{"type": "string", "minLength": 1}}
	if paged {
		properties["cursor"] = map[string]any{"type": "string"}
		properties["limit"] = map[string]any{"type": "integer", "minimum": 1, "maximum": controlMaxPageSize}
	}
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"run_id"}, "properties": properties}
}
