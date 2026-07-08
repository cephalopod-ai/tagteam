package tagteam

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
)

func Registry(cfg Config, opts RunOptions) map[string]Adapter {
	openAICompatible := &OpenAICompatibleAdapter{
		BaseURL:      cfg.Adapters.OpenAICompatible.BaseURL,
		APIKeyEnv:    cfg.Adapters.OpenAICompatible.APIKeyEnv,
		DefaultModel: cfg.Adapters.OpenAICompatible.DefaultModel,
		ExtraHeaders: cfg.Adapters.OpenAICompatible.ExtraHeaders,
		ExtraArgs:    opts.OpenAICompatibleArgs,
		EnvOverlay:   opts.EnvOverlay,
	}
	return map[string]Adapter{
		"codex": &CodexAdapter{
			IDValue:      "codex",
			DefaultModel: cfg.Adapters.Codex.DefaultModel,
			ExtraArgs:    opts.CodexArgs,
		},
		"codex-oss": &CodexAdapter{
			IDValue:      "codex-oss",
			DefaultModel: cfg.Adapters.CodexOSS.DefaultModel,
			ExtraArgs:    opts.CodexArgs,
			OSS:          true,
		},
		"claude": &ClaudeAdapter{
			DefaultModel:      cfg.Adapters.Claude.DefaultModel,
			CoderAllowedTools: cfg.Adapters.Claude.CoderAllowedTools,
			Bare:              cfg.Adapters.Claude.Bare,
			ExtraArgs:         opts.ClaudeArgs,
		},
		"agy": &AgyAdapter{
			DefaultModel: cfg.Adapters.Agy.DefaultModel,
			ExtraArgs:    opts.AgyArgs,
		},
		"gosling": &GoslingAdapter{
			DefaultModel: cfg.Adapters.Gosling.DefaultModel,
			ExtraArgs:    opts.GoslingArgs,
		},
		"openai-compatible": openAICompatible,
		"oai":               openAICompatible,
	}
}

type CodexAdapter struct {
	IDValue      string
	DefaultModel string
	ExtraArgs    []string
	OSS          bool
}

func (a *CodexAdapter) ID() string {
	return a.IDValue
}

func (a *CodexAdapter) Capabilities() CapabilitySet {
	return CapabilitySet{SupportsSchema: true}
}

func (a *CodexAdapter) Detect(ctx context.Context) (VersionInfo, error) {
	return detectBinary(ctx, "codex", []string{"--version"}, "codex login")
}

func (a *CodexAdapter) BuildCmd(role Role, req Request) (*CommandSpec, error) {
	model := req.Model
	if model == "" {
		model = a.DefaultModel
	}
	argv := []string{"codex", "exec", "-C", req.Workdir}
	if a.OSS {
		argv = append(argv, "--oss")
	}
	switch role {
	case RoleCoder:
		argv = append(argv, "-s", "workspace-write")
	case RoleAdversary, RoleSupervisor, RoleReporter, RoleScout:
		argv = append(argv, "-s", "read-only")
	default:
		return nil, fmt.Errorf("unsupported role %q", role)
	}
	if model != "" {
		argv = append(argv, "-m", model)
	}
	if (role == RoleAdversary || role == RoleSupervisor) && req.SchemaPath != "" {
		argv = append(argv, "--output-schema", req.SchemaPath)
	}
	if req.OutputPath != "" {
		argv = append(argv, "-o", req.OutputPath)
	}
	argv = append(argv, a.ExtraArgs...)
	argv = append(argv, req.Prompt)
	return &CommandSpec{Argv: argv, Dir: req.Workdir, Output: req.OutputPath}, nil
}

func (a *CodexAdapter) ParseResult(role Role, raw []byte) (Result, error) {
	result := Result{Raw: raw, Text: strings.TrimSpace(string(raw))}
	if role == RoleAdversary {
		var review Review
		if err := json.Unmarshal(raw, &review); err != nil {
			return Result{}, &OutputContractError{Err: fmt.Errorf("decode codex adversary JSON: %w", err)}
		}
		if err := review.Validate(); err != nil {
			return Result{}, &OutputContractError{Err: err}
		}
		result.Review = &review
		result.Text = review.Summary
	}
	if role == RoleScout {
		scout, err := parseScout(raw)
		if err != nil {
			return Result{}, err
		}
		result.Scout = scout
	}
	return result, nil
}

type ClaudeAdapter struct {
	DefaultModel      string
	CoderAllowedTools []string
	Bare              bool
	ExtraArgs         []string
}

func (a *ClaudeAdapter) ID() string {
	return "claude"
}

func (a *ClaudeAdapter) Capabilities() CapabilitySet {
	return CapabilitySet{SupportsSchema: true, SupportsResume: true, SupportsStdin: true}
}

func (a *ClaudeAdapter) Detect(ctx context.Context) (VersionInfo, error) {
	hint := "claude login"
	if a.Bare {
		hint = "set ANTHROPIC_API_KEY or disable adapters.claude.bare"
	}
	return detectBinary(ctx, "claude", []string{"--version"}, hint)
}

func (a *ClaudeAdapter) BuildCmd(role Role, req Request) (*CommandSpec, error) {
	model := req.Model
	if model == "" {
		model = a.DefaultModel
	}
	argv := []string{"claude", "-p", req.Prompt}
	if model != "" {
		argv = append(argv, "--model", model)
	}
	if a.Bare {
		argv = append(argv, "--bare")
	}
	switch role {
	case RoleCoder:
		argv = append(argv,
			"--permission-mode", "acceptEdits",
			"--allowedTools", strings.Join(a.CoderAllowedTools, ","),
		)
		if req.SystemPrompt != "" {
			argv = append(argv, "--append-system-prompt", req.SystemPrompt)
		}
	case RoleAdversary:
		argv = append(argv,
			"--permission-mode", "dontAsk",
			"--allowedTools", "Read,Glob,Grep,Bash(git diff *),Bash(git log *),Bash(git status *)",
		)
		if req.SchemaPath != "" {
			schemaBytes, err := osReadFile(req.SchemaPath)
			if err != nil {
				return nil, err
			}
			argv = append(argv, "--json-schema", string(schemaBytes))
		}
	case RoleSupervisor, RoleReporter, RoleScout:
		argv = append(argv,
			"--permission-mode", "dontAsk",
			"--allowedTools", "Read,Glob,Grep,Bash(git diff *),Bash(git log *),Bash(git status *)",
		)
		if role == RoleSupervisor && req.SchemaPath != "" {
			schemaBytes, err := osReadFile(req.SchemaPath)
			if err != nil {
				return nil, err
			}
			argv = append(argv, "--json-schema", string(schemaBytes))
		}
		if req.SystemPrompt != "" {
			argv = append(argv, "--append-system-prompt", req.SystemPrompt)
		}
	default:
		return nil, fmt.Errorf("unsupported role %q", role)
	}
	if req.ResumeID != "" {
		argv = append(argv, "--resume", req.ResumeID)
	}
	argv = append(argv, "--output-format", "json")
	argv = append(argv, a.ExtraArgs...)
	return &CommandSpec{Argv: argv, Dir: req.Workdir, Stdin: req.Stdin, Output: req.OutputPath}, nil
}

type claudeEnvelope struct {
	Result           string          `json:"result"`
	SessionID        string          `json:"session_id"`
	TotalCostUSD     float64         `json:"total_cost_usd"`
	StructuredOutput json.RawMessage `json:"structured_output"`
}

func (a *ClaudeAdapter) ParseResult(role Role, raw []byte) (Result, error) {
	var envelope claudeEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return Result{}, fmt.Errorf("decode claude JSON: %w", err)
	}
	result := Result{
		Raw:       raw,
		Text:      strings.TrimSpace(envelope.Result),
		SessionID: envelope.SessionID,
		CostUSD:   envelope.TotalCostUSD,
	}
	if role == RoleAdversary {
		var review Review
		reviewRaw := envelope.StructuredOutput
		if len(reviewRaw) == 0 || string(reviewRaw) == "null" {
			reviewRaw = []byte(envelope.Result)
		}
		if err := json.Unmarshal(reviewRaw, &review); err != nil {
			extracted, extractErr := extractJSONObject(reviewRaw)
			if extractErr != nil {
				return Result{}, &OutputContractError{Err: fmt.Errorf("decode claude review JSON: %w", err)}
			}
			if err := json.Unmarshal(extracted, &review); err != nil {
				return Result{}, &OutputContractError{Err: fmt.Errorf("decode claude review JSON: %w", err)}
			}
		}
		if err := review.Validate(); err != nil {
			return Result{}, &OutputContractError{Err: err}
		}
		result.Review = &review
		result.Text = review.Summary
	}
	if role == RoleSupervisor && len(envelope.StructuredOutput) > 0 && string(envelope.StructuredOutput) != "null" {
		result.Text = strings.TrimSpace(string(envelope.StructuredOutput))
	}
	if role == RoleScout {
		scoutRaw := []byte(envelope.Result)
		scout, err := parseScout(scoutRaw)
		if err != nil {
			return Result{}, err
		}
		result.Scout = scout
	}
	return result, nil
}

type AgyAdapter struct {
	DefaultModel string
	ExtraArgs    []string
}

func (a *AgyAdapter) ID() string {
	return "agy"
}

func (a *AgyAdapter) Capabilities() CapabilitySet {
	return CapabilitySet{}
}

func (a *AgyAdapter) Detect(ctx context.Context) (VersionInfo, error) {
	path, err := execLookPath("agy")
	if err != nil {
		return VersionInfo{Found: false, Auth: "unknown", Hint: "install agy", Runnable: false}, nil
	}
	cmd := execCommandContext(ctx, "agy", "--help")
	if err := cmd.Run(); err != nil {
		return VersionInfo{
			Found:    true,
			Version:  "installed",
			Auth:     "unknown",
			Binary:   path,
			Hint:     "run agy login or configure agy",
			Runnable: false,
		}, nil
	}
	return VersionInfo{
		Found:    true,
		Version:  "installed",
		Auth:     "unknown",
		Binary:   path,
		Hint:     "run agy login or configure agy",
		Runnable: true,
	}, nil
}

func (a *AgyAdapter) BuildCmd(role Role, req Request) (*CommandSpec, error) {
	model := req.Model
	if model == "" {
		model = a.DefaultModel
	}
	argv := []string{"agy", "--print", req.Prompt}
	if model != "" {
		argv = append(argv, "--model", model)
	}
	if req.Timeout > 0 {
		argv = append(argv, "--print-timeout", req.Timeout.String())
	}
	switch role {
	case RoleCoder:
		argv = append(argv, "--dangerously-skip-permissions")
	case RoleAdversary, RoleSupervisor, RoleReporter, RoleScout:
		argv = append(argv, "--sandbox")
	default:
		return nil, fmt.Errorf("unsupported role %q", role)
	}
	argv = append(argv, a.ExtraArgs...)
	return &CommandSpec{Argv: argv, Dir: req.Workdir, Output: req.OutputPath}, nil
}

func (a *AgyAdapter) ParseResult(role Role, raw []byte) (Result, error) {
	result := Result{Raw: raw, Text: strings.TrimSpace(string(raw))}
	if role == RoleAdversary {
		var review Review
		reviewRaw := raw
		if err := json.Unmarshal(reviewRaw, &review); err != nil {
			extracted, extractErr := extractJSONObject(reviewRaw)
			if extractErr != nil {
				return Result{}, &OutputContractError{Err: fmt.Errorf("decode agy review JSON: %w", err)}
			}
			if err := json.Unmarshal(extracted, &review); err != nil {
				return Result{}, &OutputContractError{Err: fmt.Errorf("decode agy review JSON: %w", err)}
			}
		}
		if err := review.Validate(); err != nil {
			return Result{}, &OutputContractError{Err: err}
		}
		result.Review = &review
		result.Text = review.Summary
	}
	if role == RoleScout {
		scout, err := parseScout(raw)
		if err != nil {
			return Result{}, err
		}
		result.Scout = scout
	}
	return result, nil
}

func parseScout(raw []byte) (*Scout, error) {
	var scout Scout
	if err := json.Unmarshal(raw, &scout); err != nil {
		extracted, extractErr := extractJSONObject(raw)
		if extractErr != nil {
			return nil, &OutputContractError{Err: fmt.Errorf("decode scout JSON: %w", err)}
		}
		if err := json.Unmarshal(extracted, &scout); err != nil {
			return nil, &OutputContractError{Err: fmt.Errorf("decode scout JSON: %w", err)}
		}
	}
	if scout.DoNotBlock == false {
		scout.DoNotBlock = true
	}
	return &scout, nil
}

type OpenAICompatibleAdapter struct {
	BaseURL      string
	APIKeyEnv    string
	DefaultModel string
	ExtraHeaders map[string]string
	ExtraArgs    []string
	EnvOverlay   map[string]string
	HTTPClient   *http.Client
}

func (a *OpenAICompatibleAdapter) ID() string {
	return "openai-compatible"
}

func (a *OpenAICompatibleAdapter) Capabilities() CapabilitySet {
	return CapabilitySet{}
}

func (a *OpenAICompatibleAdapter) Detect(ctx context.Context) (VersionInfo, error) {
	_ = ctx
	hint := "set adapters.openai_compatible.base_url"
	if a.APIKeyEnv != "" {
		hint += " and " + a.APIKeyEnv
	}
	if strings.TrimSpace(a.BaseURL) == "" {
		return VersionInfo{Found: false, Auth: "unknown", Hint: hint, Runnable: false}, nil
	}
	if a.APIKeyEnv != "" {
		if strings.TrimSpace(envValue(a.EnvOverlay, a.APIKeyEnv)) == "" {
			return VersionInfo{Found: true, Version: "configured", Auth: "missing", Hint: hint, Runnable: false}, nil
		}
	}
	return VersionInfo{Found: true, Version: "configured", Auth: "ok", Hint: hint, Runnable: true}, nil
}

func (a *OpenAICompatibleAdapter) BuildCmd(role Role, req Request) (*CommandSpec, error) {
	if role != RoleAdversary && role != RoleScout {
		return nil, unsupportedOpenAICompatibleRoleError()
	}
	return &CommandSpec{
		Argv:   []string{"openai-compatible", "POST", strings.TrimRight(a.BaseURL, "/") + "/chat/completions"},
		Dir:    req.Workdir,
		Output: req.OutputPath,
	}, nil
}

func (a *OpenAICompatibleAdapter) RunDirect(role Role, req Request) (Result, error) {
	if role != RoleAdversary && role != RoleScout {
		return Result{}, &ExitError{Code: ExitInvalidArguments, Err: unsupportedOpenAICompatibleRoleError()}
	}
	model := req.Model
	if model == "" {
		model = a.DefaultModel
	}
	if strings.TrimSpace(model) == "" {
		return Result{}, &ExitError{Code: ExitInvalidArguments, Err: fmt.Errorf("openai-compatible model is required")}
	}
	baseURL := strings.TrimRight(strings.TrimSpace(a.BaseURL), "/")
	if baseURL == "" {
		return Result{}, &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("openai-compatible not runnable; try set adapters.openai_compatible.base_url")}
	}

	messages := make([]map[string]string, 0, 2)
	if strings.TrimSpace(req.SystemPrompt) != "" {
		messages = append(messages, map[string]string{"role": "system", "content": req.SystemPrompt})
	}
	messages = append(messages, map[string]string{"role": "user", "content": req.Prompt})
	payload := map[string]any{
		"model":       model,
		"messages":    messages,
		"temperature": 0,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Result{}, err
	}

	runCtx := req.Context
	if runCtx == nil {
		runCtx = context.Background()
	}
	cancel := func() {}
	if req.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(runCtx, req.Timeout)
	}
	defer cancel()

	url := baseURL + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(runCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if a.APIKeyEnv != "" {
		apiKey := strings.TrimSpace(envValue(req.EnvOverlay, a.APIKeyEnv))
		if apiKey == "" {
			apiKey = strings.TrimSpace(envValue(a.EnvOverlay, a.APIKeyEnv))
		}
		if apiKey == "" {
			return Result{}, &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("openai-compatible not runnable; try set %s", a.APIKeyEnv)}
		}
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	for key, value := range a.ExtraHeaders {
		if strings.TrimSpace(key) != "" {
			httpReq.Header.Set(key, value)
		}
	}

	client := a.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return Result{}, &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("openai-compatible request failed: %s", redactSecretsWithOverlay(err.Error(), req.EnvOverlay))}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{}, &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("openai-compatible request failed: status %d: %s", resp.StatusCode, redactSecretsWithOverlay(strings.TrimSpace(string(raw)), req.EnvOverlay))}
	}
	if req.OutputPath != "" {
		_ = os.WriteFile(req.OutputPath+".raw", raw, 0o644)
	}
	result, err := a.ParseResult(role, raw)
	if err != nil {
		if req.OutputPath != "" {
			_ = os.WriteFile(req.OutputPath+".validation-error.txt", []byte(err.Error()+"\n"), 0o644)
		}
		return Result{}, err
	}
	result.Command = []string{"POST", url}
	return result, nil
}

func envValue(overlay map[string]string, key string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	if overlay != nil {
		return overlay[key]
	}
	return ""
}

type openAICompatibleResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		Text string `json:"text"`
	} `json:"choices"`
}

func (a *OpenAICompatibleAdapter) ParseResult(role Role, raw []byte) (Result, error) {
	if role != RoleAdversary && role != RoleScout {
		return Result{}, &OutputContractError{Err: unsupportedOpenAICompatibleRoleError()}
	}
	var envelope openAICompatibleResponse
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return Result{}, fmt.Errorf("decode openai-compatible response: %w", err)
	}
	if len(envelope.Choices) == 0 {
		return Result{}, &OutputContractError{Err: fmt.Errorf("openai-compatible response has no choices")}
	}
	content := envelope.Choices[0].Message.Content
	if content == "" {
		content = envelope.Choices[0].Text
	}
	if strings.TrimSpace(content) == "" {
		return Result{}, &OutputContractError{Err: fmt.Errorf("openai-compatible response choice has no content")}
	}
	if role == RoleScout {
		scout, err := parseScout([]byte(content))
		if err != nil {
			return Result{}, err
		}
		return Result{
			Raw:   raw,
			Text:  scout.Summary,
			Scout: scout,
		}, nil
	}
	var review Review
	reviewRaw := []byte(content)
	if err := json.Unmarshal(reviewRaw, &review); err != nil {
		extracted, extractErr := extractJSONObject(reviewRaw)
		if extractErr != nil {
			return Result{}, &OutputContractError{Err: fmt.Errorf("decode openai-compatible review JSON: %w", err)}
		}
		if err := json.Unmarshal(extracted, &review); err != nil {
			return Result{}, &OutputContractError{Err: fmt.Errorf("decode openai-compatible review JSON: %w", err)}
		}
	}
	if err := review.Validate(); err != nil {
		return Result{}, &OutputContractError{Err: err}
	}
	return Result{
		Raw:    raw,
		Text:   review.Summary,
		Review: &review,
	}, nil
}

func unsupportedOpenAICompatibleRoleError() error {
	return fmt.Errorf("openai-compatible adapter is read-only in this version; use it as reviewer/adversary or scout, not as coder/worker")
}

var (
	execLookPath       = exec.LookPath
	execCommandContext = exec.CommandContext
)

func detectBinary(ctx context.Context, binary string, versionArgs []string, hint string) (VersionInfo, error) {
	path, err := execLookPath(binary)
	if err != nil {
		return VersionInfo{Found: false, Auth: "unknown", Hint: hint, Runnable: false}, nil
	}
	cmd := execCommandContext(ctx, binary, versionArgs...)
	out, err := cmd.CombinedOutput()
	version := strings.TrimSpace(string(out))
	runnable := true
	if err != nil {
		runnable = false
		if version == "" {
			version = "unknown"
		}
		hint = fmt.Sprintf("%s (probe failed: %v)", hint, err)
	}
	return VersionInfo{
		Found:    true,
		Version:  version,
		Auth:     "unknown",
		Binary:   path,
		Hint:     hint,
		Runnable: runnable,
	}, nil
}

func extractJSONObject(raw []byte) ([]byte, error) {
	text := string(raw)
	start := strings.IndexByte(text, '{')
	if start < 0 {
		return nil, fmt.Errorf("no JSON object found")
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(text); i++ {
		ch := text[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return []byte(text[start : i+1]), nil
			}
		}
	}
	return nil, fmt.Errorf("unterminated JSON object")
}

type GoslingAdapter struct {
	DefaultModel string
	ExtraArgs    []string
}

func (a *GoslingAdapter) ID() string {
	return "gosling"
}

func (a *GoslingAdapter) Capabilities() CapabilitySet {
	return CapabilitySet{}
}

func (a *GoslingAdapter) Detect(ctx context.Context) (VersionInfo, error) {
	hint := "install gosling"
	return detectBinary(ctx, "gosling", []string{"--version"}, hint)
}

func (a *GoslingAdapter) BuildCmd(role Role, req Request) (*CommandSpec, error) {
	switch role {
	case RoleCoder, RoleReporter:
	case RoleAdversary:
		return nil, fmt.Errorf("gosling is not supported as an adversary adapter")
	case RoleSupervisor:
		return nil, fmt.Errorf("gosling is not supported as a supervisor adapter")
	case RoleScout:
		return nil, fmt.Errorf("gosling is not supported as a scout adapter")
	default:
		return nil, fmt.Errorf("unsupported role %q", role)
	}
	model := req.Model
	if model == "" {
		model = a.DefaultModel
	}
	argv := []string{"gosling", "run", "--no-session", "--quiet", "--output-format", "json"}
	if model != "" {
		argv = append(argv, "--model", model)
	}
	argv = append(argv, "--text", req.Prompt)
	argv = append(argv, a.ExtraArgs...)
	return &CommandSpec{Argv: argv, Dir: req.Workdir, Output: req.OutputPath}, nil
}

type goslingMessageContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type goslingMessage struct {
	Role    string                  `json:"role"`
	Content []goslingMessageContent `json:"content"`
}

type goslingMetadata struct {
	TotalTokens  *int `json:"total_tokens,omitempty"`
	InputTokens  *int `json:"input_tokens,omitempty"`
	OutputTokens *int `json:"output_tokens,omitempty"`
}

type goslingEnvelope struct {
	Messages []goslingMessage `json:"messages"`
	Metadata goslingMetadata  `json:"metadata"`
}

func (a *GoslingAdapter) ParseResult(role Role, raw []byte) (Result, error) {
	if role == RoleAdversary {
		return Result{}, &OutputContractError{Err: fmt.Errorf("gosling is not supported as an adversary adapter")}
	}
	var envelope goslingEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return Result{Raw: raw, Text: strings.TrimSpace(string(raw))}, nil
	}

	var assistantText string
	for i := len(envelope.Messages) - 1; i >= 0; i-- {
		msg := envelope.Messages[i]
		if strings.ToLower(msg.Role) == "assistant" {
			var sb strings.Builder
			for _, item := range msg.Content {
				if item.Type == "text" {
					sb.WriteString(item.Text)
				}
			}
			assistantText = sb.String()
			break
		}
	}

	result := Result{
		Raw:  raw,
		Text: strings.TrimSpace(assistantText),
	}

	return result, nil
}
