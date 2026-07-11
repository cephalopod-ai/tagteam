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
			IDValue:         "codex",
			DefaultModel:    cfg.Adapters.Codex.DefaultModel,
			ReasoningEffort: cfg.Adapters.Codex.ReasoningEffort,
			ExtraArgs:       opts.CodexArgs,
		},
		"codex-oss": &CodexAdapter{
			IDValue:      "codex-oss",
			DefaultModel: cfg.Adapters.CodexOSS.DefaultModel,
			ExtraArgs:    opts.CodexArgs,
			OSS:          true,
		},
		"claude": &ClaudeAdapter{
			DefaultModel:      cfg.Adapters.Claude.DefaultModel,
			Effort:            cfg.Adapters.Claude.Effort,
			CoderAllowedTools: cfg.Adapters.Claude.CoderAllowedTools,
			Bare:              cfg.Adapters.Claude.Bare,
			Serialize:         cfg.Adapters.Claude.Serialize == nil || *cfg.Adapters.Claude.Serialize,
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
	IDValue         string
	DefaultModel    string
	ReasoningEffort string
	ExtraArgs       []string
	OSS             bool
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
	if a.ReasoningEffort != "" {
		argv = append(argv, "-c", fmt.Sprintf("model_reasoning_effort=%q", a.ReasoningEffort))
	}
	if (role == RoleCoder || role == RoleAdversary || role == RoleSupervisor) && req.SchemaPath != "" {
		argv = append(argv, "--output-schema", req.SchemaPath)
	}
	if req.OutputPath != "" {
		argv = append(argv, "-o", req.OutputPath)
	}
	argv = append(argv, a.ExtraArgs...)
	argv = append(argv, "-")
	return &CommandSpec{Argv: argv, Dir: req.Workdir, Stdin: promptStdin(req), Output: req.OutputPath}, nil
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
	Effort            string
	CoderAllowedTools []string
	Bare              bool
	Serialize         bool
	ExtraArgs         []string
}

func (a *ClaudeAdapter) ID() string {
	return "claude"
}

func (a *ClaudeAdapter) Capabilities() CapabilitySet {
	return CapabilitySet{SupportsSchema: true, SupportsResume: true, SupportsStdin: true, SerializeInvocations: a.Serialize}
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
	argv := []string{"claude", "-p"}
	if model != "" {
		argv = append(argv, "--model", model)
	}
	if a.Effort != "" {
		argv = append(argv, "--effort", a.Effort)
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
		if req.SchemaPath != "" {
			schemaBytes, err := osReadFile(req.SchemaPath)
			if err != nil {
				return nil, err
			}
			argv = append(argv, "--json-schema", string(schemaBytes))
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
	return &CommandSpec{Argv: argv, Dir: req.Workdir, Stdin: promptStdin(req), Output: req.OutputPath}, nil
}

type claudeEnvelope struct {
	Subtype          string          `json:"subtype"`
	IsError          bool            `json:"is_error"`
	Result           string          `json:"result"`
	SessionID        string          `json:"session_id"`
	TotalCostUSD     float64         `json:"total_cost_usd"`
	StructuredOutput json.RawMessage `json:"structured_output"`
}

func (a *ClaudeAdapter) ParseResult(role Role, raw []byte) (Result, error) {
	var envelope claudeEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		if role == RoleAdversary {
			review, parseErr := parseReviewPayload(raw)
			if parseErr == nil {
				return Result{Raw: raw, Text: review.Summary, Review: review}, nil
			}
		}
		return Result{}, fmt.Errorf("decode claude JSON: %w", err)
	}
	if envelope.IsError || strings.HasPrefix(envelope.Subtype, "error") {
		return Result{}, claudeEnvelopeError(envelope)
	}
	result := Result{
		Raw:       raw,
		Text:      strings.TrimSpace(envelope.Result),
		SessionID: envelope.SessionID,
		CostUSD:   envelope.TotalCostUSD,
	}
	if role == RoleCoder {
		structured := normalizeClaudeStructuredOutput(envelope.StructuredOutput)
		if len(structured) > 0 && string(structured) != "null" {
			result.Text = strings.TrimSpace(string(structured))
		}
	}
	if role == RoleAdversary {
		reviewRaw := normalizeClaudeStructuredOutput(envelope.StructuredOutput)
		if len(reviewRaw) == 0 || string(reviewRaw) == "null" {
			reviewRaw = []byte(envelope.Result)
		}
		review, err := parseReviewPayload(reviewRaw)
		if err != nil {
			return Result{}, err
		}
		result.Review = review
		result.Text = review.Summary
	}
	if role == RoleSupervisor {
		structured := normalizeClaudeStructuredOutput(envelope.StructuredOutput)
		if len(structured) > 0 && string(structured) != "null" {
			result.Text = strings.TrimSpace(string(structured))
		}
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

func normalizeClaudeStructuredOutput(raw json.RawMessage) []byte {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	if trimmed[0] != '"' {
		return trimmed
	}
	var encoded string
	if err := json.Unmarshal(trimmed, &encoded); err != nil {
		return trimmed
	}
	return []byte(encoded)
}

// claudeEnvelopeError converts a claude result envelope that reports its own
// failure (is_error or an error_* subtype) into a clear adapter error instead
// of letting the error text cascade into a misleading JSON contract failure.
func claudeEnvelopeError(envelope claudeEnvelope) error {
	subtype := strings.TrimSpace(envelope.Subtype)
	if subtype == "" {
		subtype = "error"
	}
	detail := strings.TrimSpace(envelope.Result)
	if detail == "" {
		detail = "no result text"
	}
	const maxDetail = 400
	if len(detail) > maxDetail {
		detail = detail[:maxDetail] + "..."
	}
	return fmt.Errorf("claude reported %s: %s", subtype, detail)
}

func parseReviewPayload(raw []byte) (*Review, error) {
	return parseReviewPayloadLabeled(raw, "claude")
}

func parseReviewPayloadLabeled(raw []byte, label string) (*Review, error) {
	var review Review
	err := decodeEmbeddedJSON(raw, func(candidate []byte) error {
		var parsed Review
		if err := json.Unmarshal(candidate, &parsed); err != nil {
			return fmt.Errorf("decode %s review JSON: %w", label, err)
		}
		if err := parsed.Validate(); err != nil {
			return err
		}
		review = parsed
		return nil
	})
	if err != nil {
		return nil, &OutputContractError{Err: err}
	}
	return &review, nil
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
	versionCmd := execCommandContext(ctx, "agy", "--version")
	versionOutput, versionErr := versionCmd.CombinedOutput()
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
		Version:  strings.TrimSpace(string(versionOutput)),
		Auth:     "unknown",
		Binary:   path,
		Hint:     "run agy login or configure agy",
		Runnable: versionErr == nil,
	}, nil
}

func (a *AgyAdapter) BuildCmd(role Role, req Request) (*CommandSpec, error) {
	model := req.Model
	if model == "" {
		model = a.DefaultModel
	}
	// Agy 1.1+ does not read a print-mode prompt from stdin when launched by a
	// subprocess. The prompt must be the value of --print.
	prompt := strings.TrimSuffix(string(promptStdin(req)), "\n")
	argv := []string{"agy", "--print=" + prompt}
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
		review, err := parseReviewPayloadLabeled(raw, "agy")
		if err != nil {
			return Result{}, err
		}
		result.Review = review
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
	err := decodeEmbeddedJSON(raw, func(candidate []byte) error {
		var parsed Scout
		if err := json.Unmarshal(candidate, &parsed); err != nil {
			return fmt.Errorf("decode scout JSON: %w", err)
		}
		scout = parsed
		return nil
	})
	if err != nil {
		return nil, &OutputContractError{Err: err}
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
	limit := maxOutputBytes(req)
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return Result{}, err
	}
	if int64(len(raw)) > limit {
		return Result{}, &ExitError{Code: ExitAdapterFailure, Err: outputLimitError("openai-compatible", limit)}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{}, &ExitError{Code: ExitAdapterFailure, Err: fmt.Errorf("openai-compatible request failed: status %d: %s", resp.StatusCode, redactSecretsWithOverlay(strings.TrimSpace(string(raw)), req.EnvOverlay))}
	}
	if req.OutputPath != "" {
		_ = writeRedactedBytes(req.OutputPath+".raw", raw, req.EnvOverlay)
	}
	result, err := a.ParseResult(role, raw)
	if err != nil {
		if req.OutputPath != "" {
			_ = writeRedactedBytes(req.OutputPath+".validation-error.txt", []byte(err.Error()+"\n"), req.EnvOverlay)
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
	review, err := parseReviewPayloadLabeled([]byte(content), "openai-compatible")
	if err != nil {
		return Result{}, err
	}
	return Result{
		Raw:    raw,
		Text:   review.Summary,
		Review: review,
	}, nil
}

func unsupportedOpenAICompatibleRoleError() error {
	return fmt.Errorf("openai-compatible adapter is read-only in this version; use it as reviewer/adversary or scout, not as coder/worker")
}

var (
	execLookPath       = exec.LookPath
	execCommandContext = exec.CommandContext
)
