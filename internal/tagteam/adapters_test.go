package tagteam

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCodexBuildCmd(t *testing.T) {
	adapter := &CodexAdapter{IDValue: "codex", DefaultModel: "gpt-5-codex", ExtraArgs: []string{"--foo", "bar"}}
	spec, err := adapter.BuildCmd(RoleCoder, Request{
		Prompt:     "make it work",
		Model:      "",
		Workdir:    "/repo",
		OutputPath: "/tmp/out.md",
	})
	if err != nil {
		t.Fatalf("BuildCmd() error = %v", err)
	}
	want := []string{"codex", "exec", "-C", "/repo", "-s", "workspace-write", "-m", "gpt-5-codex", "-o", "/tmp/out.md", "--foo", "bar", "-"}
	if !reflect.DeepEqual(spec.Argv, want) {
		t.Fatalf("argv mismatch\nwant: %#v\ngot:  %#v", want, spec.Argv)
	}
	if string(spec.Stdin) != "make it work\n" {
		t.Fatalf("stdin = %q", string(spec.Stdin))
	}
}

func TestCodexBuildCmdSupervisor(t *testing.T) {
	adapter := &CodexAdapter{IDValue: "codex", DefaultModel: "gpt-5.6-sol", ReasoningEffort: "high"}
	spec, err := adapter.BuildCmd(RoleSupervisor, Request{
		Prompt:  "write a brief",
		Workdir: "/repo",
	})
	if err != nil {
		t.Fatalf("BuildCmd() error = %v", err)
	}
	want := []string{"codex", "exec", "-C", "/repo", "-s", "read-only", "-m", "gpt-5.6-sol", "-c", `model_reasoning_effort="high"`, "-"}
	if !reflect.DeepEqual(spec.Argv, want) {
		t.Fatalf("argv mismatch\nwant: %#v\ngot:  %#v", want, spec.Argv)
	}
	if string(spec.Stdin) != "write a brief\n" {
		t.Fatalf("stdin = %q", string(spec.Stdin))
	}
}

func TestCodexBuildCmdSupervisorWithSchema(t *testing.T) {
	adapter := &CodexAdapter{IDValue: "codex", DefaultModel: "gpt-5-codex"}
	spec, err := adapter.BuildCmd(RoleSupervisor, Request{
		Prompt:     "write a work plan",
		Workdir:    "/repo",
		SchemaPath: "/tmp/work-plan-schema.json",
	})
	if err != nil {
		t.Fatalf("BuildCmd() error = %v", err)
	}
	want := []string{"codex", "exec", "-C", "/repo", "-s", "read-only", "-m", "gpt-5-codex", "--output-schema", "/tmp/work-plan-schema.json", "-"}
	if !reflect.DeepEqual(spec.Argv, want) {
		t.Fatalf("argv mismatch\nwant: %#v\ngot:  %#v", want, spec.Argv)
	}
	if string(spec.Stdin) != "write a work plan\n" {
		t.Fatalf("stdin = %q", string(spec.Stdin))
	}
}

func TestCodexBuildCmdReporterIsReadOnly(t *testing.T) {
	adapter := &CodexAdapter{IDValue: "codex", DefaultModel: "gpt-5-codex"}
	spec, err := adapter.BuildCmd(RoleReporter, Request{
		Prompt:  "report remaining work",
		Workdir: "/repo",
	})
	if err != nil {
		t.Fatalf("BuildCmd() error = %v", err)
	}
	want := []string{"codex", "exec", "-C", "/repo", "-s", "read-only", "-m", "gpt-5-codex", "-"}
	if !reflect.DeepEqual(spec.Argv, want) {
		t.Fatalf("argv mismatch\nwant: %#v\ngot:  %#v", want, spec.Argv)
	}
	if string(spec.Stdin) != "report remaining work\n" {
		t.Fatalf("stdin = %q", string(spec.Stdin))
	}
}

func TestClaudeBuildCmdAdversary(t *testing.T) {
	tmp := t.TempDir()
	schemaPath := filepath.Join(tmp, "schema.json")
	if err := os.WriteFile(schemaPath, []byte(`{"type":"object"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	adapter := &ClaudeAdapter{DefaultModel: "opus"}
	spec, err := adapter.BuildCmd(RoleAdversary, Request{
		Prompt:     "review",
		Model:      "",
		Workdir:    "/repo",
		SchemaPath: schemaPath,
		Stdin:      []byte("diff"),
	})
	if err != nil {
		t.Fatalf("BuildCmd() error = %v", err)
	}
	if spec.Argv[0] != "claude" {
		t.Fatalf("binary = %q", spec.Argv[0])
	}
	if strings.Contains(strings.Join(spec.Argv, "\x00"), "review") {
		t.Fatalf("prompt leaked into argv: %v", spec.Argv)
	}
	if !bytes.Contains(spec.Stdin, []byte("review")) || !bytes.Contains(spec.Stdin, []byte("diff")) {
		t.Fatalf("stdin did not include prompt and diff: %q", string(spec.Stdin))
	}
}

func TestClaudeBuildCmdSupervisor(t *testing.T) {
	adapter := &ClaudeAdapter{DefaultModel: "claude-sonnet-5", Effort: "high"}
	spec, err := adapter.BuildCmd(RoleSupervisor, Request{
		Prompt:  "write a brief",
		Workdir: "/repo",
	})
	if err != nil {
		t.Fatalf("BuildCmd() error = %v", err)
	}
	if spec.Argv[0] != "claude" {
		t.Fatalf("binary = %q", spec.Argv[0])
	}
	argv := strings.Join(spec.Argv, " ")
	if !strings.Contains(argv, "--model claude-sonnet-5 --effort high") {
		t.Fatalf("expected model and effort, got argv = %v", spec.Argv)
	}
	if !strings.Contains(argv, "--permission-mode dontAsk") {
		t.Fatalf("expected read-only permission mode, got argv = %v", spec.Argv)
	}
	if strings.Contains(argv, "--json-schema") {
		t.Fatalf("supervisor brief step should not force a JSON schema, got argv = %v", spec.Argv)
	}
}

func TestClaudeBuildCmdSupervisorWorkPlanUsesSchema(t *testing.T) {
	tmp := t.TempDir()
	schemaPath := filepath.Join(tmp, "work-plan-schema.json")
	if err := os.WriteFile(schemaPath, []byte(WorkPlanSchema), 0o644); err != nil {
		t.Fatal(err)
	}
	adapter := &ClaudeAdapter{DefaultModel: "opus"}
	spec, err := adapter.BuildCmd(RoleSupervisor, Request{
		Prompt:     "write a work plan",
		Workdir:    "/repo",
		SchemaPath: schemaPath,
	})
	if err != nil {
		t.Fatalf("BuildCmd() error = %v", err)
	}
	argv := strings.Join(spec.Argv, " ")
	if !strings.Contains(argv, "--permission-mode dontAsk") {
		t.Fatalf("expected read-only permission mode, got argv = %v", spec.Argv)
	}
	if !strings.Contains(argv, "--json-schema") {
		t.Fatalf("expected supervisor work-plan schema, got argv = %v", spec.Argv)
	}
	if !strings.Contains(argv, `"packages"`) {
		t.Fatalf("expected schema JSON in argv, got argv = %v", spec.Argv)
	}
}

func TestClaudeBuildCmdReporterDoesNotUseSchema(t *testing.T) {
	adapter := &ClaudeAdapter{DefaultModel: "opus"}
	spec, err := adapter.BuildCmd(RoleReporter, Request{
		Prompt:  "report remaining work",
		Workdir: "/repo",
	})
	if err != nil {
		t.Fatalf("BuildCmd() error = %v", err)
	}
	argv := strings.Join(spec.Argv, " ")
	if !strings.Contains(argv, "--permission-mode dontAsk") {
		t.Fatalf("expected read-only permission mode, got argv = %v", spec.Argv)
	}
	if strings.Contains(argv, "--json-schema") {
		t.Fatalf("reporter step should not force a JSON schema, got argv = %v", spec.Argv)
	}
}

func TestClaudeParseResult(t *testing.T) {
	adapter := &ClaudeAdapter{}
	raw := []byte(`{"result":"ok","session_id":"sess_1","total_cost_usd":1.25,"structured_output":{"verdict":"pass","summary":"looks good","findings":[],"test_suggestions":[]}}`)
	result, err := adapter.ParseResult(RoleAdversary, raw)
	if err != nil {
		t.Fatalf("ParseResult() error = %v", err)
	}
	if result.SessionID != "sess_1" {
		t.Fatalf("session_id = %q", result.SessionID)
	}
	if result.Review == nil || result.Review.Verdict != "pass" {
		t.Fatalf("review = %#v", result.Review)
	}
}

func TestClaudeParseResultSupervisorStructuredOutput(t *testing.T) {
	adapter := &ClaudeAdapter{}
	raw := []byte(`{"result":"","session_id":"sess_1","total_cost_usd":0.5,"structured_output":{"schema_version":1,"summary":"split","packages":[{"id":"P1","title":"First","goal":"Do first","acceptance":["ok"],"validation":["go test ./..."]}],"selected_package":"P1","defer":[]}}`)
	result, err := adapter.ParseResult(RoleSupervisor, raw)
	if err != nil {
		t.Fatalf("ParseResult() error = %v", err)
	}
	if result.SessionID != "sess_1" || result.CostUSD != 0.5 {
		t.Fatalf("metadata = %q %v", result.SessionID, result.CostUSD)
	}
	if !strings.Contains(result.Text, `"selected_package":"P1"`) {
		t.Fatalf("structured output was not exposed as text: %q", result.Text)
	}
}

func TestClaudeParseResultFallsBackToResultJSON(t *testing.T) {
	adapter := &ClaudeAdapter{}
	raw := []byte(`{"result":"{\"verdict\":\"pass\",\"summary\":\"looks good\",\"findings\":[],\"test_suggestions\":[]}","session_id":"sess_1","total_cost_usd":1.25}`)
	result, err := adapter.ParseResult(RoleAdversary, raw)
	if err != nil {
		t.Fatalf("ParseResult() error = %v", err)
	}
	if result.Review == nil || result.Review.Verdict != "pass" {
		t.Fatalf("review = %#v", result.Review)
	}
}

func TestClaudeParseResultAcceptsStringifiedStructuredOutput(t *testing.T) {
	adapter := &ClaudeAdapter{}
	raw := []byte(`{"result":"","structured_output":"{\"verdict\":\"pass\",\"summary\":\"looks good\",\"findings\":[],\"test_suggestions\":[]}"}`)
	result, err := adapter.ParseResult(RoleAdversary, raw)
	if err != nil {
		t.Fatalf("ParseResult() error = %v", err)
	}
	if result.Review == nil || result.Review.Verdict != "pass" || result.Text != "looks good" {
		t.Fatalf("review = %#v text=%q", result.Review, result.Text)
	}
}

func TestClaudeParseResultSupervisorAcceptsStringifiedStructuredOutput(t *testing.T) {
	adapter := &ClaudeAdapter{}
	raw := []byte(`{"result":"","structured_output":"{\"schema_version\":1,\"summary\":\"split\",\"packages\":[]}"}`)
	result, err := adapter.ParseResult(RoleSupervisor, raw)
	if err != nil {
		t.Fatalf("ParseResult() error = %v", err)
	}
	if !strings.Contains(result.Text, `"summary":"split"`) {
		t.Fatalf("text = %q", result.Text)
	}
}

func TestClaudeParseResultExtractsFencedResultJSON(t *testing.T) {
	adapter := &ClaudeAdapter{}
	raw := []byte("{\"result\":\"```json\\n{\\\"verdict\\\":\\\"pass\\\",\\\"summary\\\":\\\"looks good\\\",\\\"findings\\\":[],\\\"test_suggestions\\\":[]}\\n```\"}")
	result, err := adapter.ParseResult(RoleAdversary, raw)
	if err != nil {
		t.Fatalf("ParseResult() error = %v", err)
	}
	if result.Review == nil || result.Review.Verdict != "pass" {
		t.Fatalf("review = %#v", result.Review)
	}
}

func TestClaudeParseResultRecoversPlainTextWrappedReviewJSON(t *testing.T) {
	adapter := &ClaudeAdapter{}
	raw := []byte(`Review complete.

{
  "schema_version": 1,
  "verdict": "pass",
  "summary": "looks good",
  "findings": [],
  "test_suggestions": []
}`)
	result, err := adapter.ParseResult(RoleAdversary, raw)
	if err != nil {
		t.Fatalf("ParseResult() error = %v", err)
	}
	if result.Review == nil || result.Review.Verdict != "pass" {
		t.Fatalf("review = %#v", result.Review)
	}
	if result.Text != "looks good" {
		t.Fatalf("text = %q", result.Text)
	}
}

func TestClaudeParseResultWrapsContractErrors(t *testing.T) {
	adapter := &ClaudeAdapter{}
	_, err := adapter.ParseResult(RoleAdversary, []byte(`{"result":"ok","structured_output":{"verdict":"pass","summary":"bad","findings":[{"severity":"major","file":"main.go","issue":"bug","fix":"fix"}],"test_suggestions":[]}}`))
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsOutputContractError(err) {
		t.Fatalf("expected output contract error, got %T", err)
	}
}

func TestAgyBuildCmdCoder(t *testing.T) {
	adapter := &AgyAdapter{DefaultModel: "gemini-3.5-flash", ExtraArgs: []string{"--project", "demo"}}
	spec, err := adapter.BuildCmd(RoleCoder, Request{
		Prompt:  "make it work",
		Workdir: "/repo",
		Timeout: 15 * time.Second,
	})
	if err != nil {
		t.Fatalf("BuildCmd() error = %v", err)
	}
	want := []string{
		"agy", "--print",
		"--model", "gemini-3.5-flash",
		"--print-timeout", "15s",
		"--dangerously-skip-permissions",
		"--project", "demo",
	}
	if !reflect.DeepEqual(spec.Argv, want) {
		t.Fatalf("argv mismatch\nwant: %#v\ngot:  %#v", want, spec.Argv)
	}
	if string(spec.Stdin) != "make it work\n" {
		t.Fatalf("stdin = %q", string(spec.Stdin))
	}
}

func TestAgyBuildCmdAdversary(t *testing.T) {
	adapter := &AgyAdapter{DefaultModel: "gemini-3.5-flash"}
	spec, err := adapter.BuildCmd(RoleAdversary, Request{
		Prompt:  "review",
		Workdir: "/repo",
	})
	if err != nil {
		t.Fatalf("BuildCmd() error = %v", err)
	}
	want := []string{"agy", "--print", "--model", "gemini-3.5-flash", "--sandbox"}
	if !reflect.DeepEqual(spec.Argv, want) {
		t.Fatalf("argv mismatch\nwant: %#v\ngot:  %#v", want, spec.Argv)
	}
	if string(spec.Stdin) != "review\n" {
		t.Fatalf("stdin = %q", string(spec.Stdin))
	}
}

func TestAgyBuildCmdSupervisor(t *testing.T) {
	adapter := &AgyAdapter{DefaultModel: "gemini-3.5-flash"}
	spec, err := adapter.BuildCmd(RoleSupervisor, Request{
		Prompt:  "write a brief",
		Workdir: "/repo",
	})
	if err != nil {
		t.Fatalf("BuildCmd() error = %v", err)
	}
	want := []string{"agy", "--print", "--model", "gemini-3.5-flash", "--sandbox"}
	if !reflect.DeepEqual(spec.Argv, want) {
		t.Fatalf("argv mismatch\nwant: %#v\ngot:  %#v", want, spec.Argv)
	}
	if string(spec.Stdin) != "write a brief\n" {
		t.Fatalf("stdin = %q", string(spec.Stdin))
	}
}

func TestAgyBuildCmdReporter(t *testing.T) {
	adapter := &AgyAdapter{DefaultModel: "gemini-3.5-flash"}
	spec, err := adapter.BuildCmd(RoleReporter, Request{
		Prompt:  "report remaining work",
		Workdir: "/repo",
	})
	if err != nil {
		t.Fatalf("BuildCmd() error = %v", err)
	}
	want := []string{"agy", "--print", "--model", "gemini-3.5-flash", "--sandbox"}
	if !reflect.DeepEqual(spec.Argv, want) {
		t.Fatalf("argv mismatch\nwant: %#v\ngot:  %#v", want, spec.Argv)
	}
	if string(spec.Stdin) != "report remaining work\n" {
		t.Fatalf("stdin = %q", string(spec.Stdin))
	}
}

func TestAgyBuildCmdScout(t *testing.T) {
	adapter := &AgyAdapter{DefaultModel: "gemini-3.5-flash-low"}
	spec, err := adapter.BuildCmd(RoleScout, Request{
		Prompt:  "scout the repo",
		Workdir: "/repo",
	})
	if err != nil {
		t.Fatalf("BuildCmd() error = %v", err)
	}
	want := []string{"agy", "--print", "--model", "gemini-3.5-flash-low", "--sandbox"}
	if !reflect.DeepEqual(spec.Argv, want) {
		t.Fatalf("argv mismatch\nwant: %#v\ngot:  %#v", want, spec.Argv)
	}
	if string(spec.Stdin) != "scout the repo\n" {
		t.Fatalf("stdin = %q", string(spec.Stdin))
	}
}

func TestAgyParseResultExtractsFencedReviewJSON(t *testing.T) {
	adapter := &AgyAdapter{}
	raw := []byte("```json\n{\"verdict\":\"pass\",\"summary\":\"looks good\",\"findings\":[],\"test_suggestions\":[]}\n```")
	result, err := adapter.ParseResult(RoleAdversary, raw)
	if err != nil {
		t.Fatalf("ParseResult() error = %v", err)
	}
	if result.Review == nil || result.Review.Verdict != "pass" {
		t.Fatalf("review = %#v", result.Review)
	}
}

func TestRegistryIncludesOpenAICompatibleAliases(t *testing.T) {
	registry := Registry(DefaultConfig(), RunOptions{})
	if _, ok := registry["openai-compatible"]; !ok {
		t.Fatal("expected openai-compatible adapter")
	}
	if _, ok := registry["oai"]; !ok {
		t.Fatal("expected oai alias")
	}
	if registry["openai-compatible"].ID() != "openai-compatible" {
		t.Fatalf("adapter ID = %q", registry["openai-compatible"].ID())
	}
	if registry["oai"].ID() != "openai-compatible" {
		t.Fatalf("alias adapter ID = %q", registry["oai"].ID())
	}
}

func TestOpenAICompatibleDetect(t *testing.T) {
	t.Setenv("TAGTEAM_TEST_MISSING_OPENAI_KEY", "")
	missingBase := &OpenAICompatibleAdapter{APIKeyEnv: "TAGTEAM_TEST_MISSING_OPENAI_KEY"}
	info, err := missingBase.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if info.Found || info.Runnable {
		t.Fatalf("missing base_url should not be runnable: %#v", info)
	}

	missingKey := &OpenAICompatibleAdapter{BaseURL: "https://example.test/v1", APIKeyEnv: "TAGTEAM_TEST_MISSING_OPENAI_KEY"}
	info, err = missingKey.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if !info.Found || info.Runnable || info.Auth != "missing" {
		t.Fatalf("missing API key env should be found but not runnable: %#v", info)
	}

	t.Setenv("TAGTEAM_TEST_PRESENT_OPENAI_KEY", "test-key")
	runnable := &OpenAICompatibleAdapter{BaseURL: "https://example.test/v1", APIKeyEnv: "TAGTEAM_TEST_PRESENT_OPENAI_KEY"}
	info, err = runnable.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if !info.Found || !info.Runnable || info.Auth != "ok" {
		t.Fatalf("configured adapter should be runnable: %#v", info)
	}

	overlayRunnable := &OpenAICompatibleAdapter{
		BaseURL:    "https://example.test/v1",
		APIKeyEnv:  "TAGTEAM_TEST_OPENAI_KEY",
		EnvOverlay: map[string]string{"TAGTEAM_TEST_OPENAI_KEY": "overlay-key"},
	}
	info, err = overlayRunnable.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect() overlay error = %v", err)
	}
	if !info.Found || !info.Runnable || info.Auth != "ok" {
		t.Fatalf("overlay-configured adapter should be runnable: %#v", info)
	}
}

func TestOpenAICompatibleRunDirectBuildsChatCompletionsRequest(t *testing.T) {
	reviewJSON := `{"schema_version":1,"verdict":"pass","summary":"looks good","findings":[],"test_suggestions":[]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s", r.Method)
		}
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret-token" {
			t.Fatalf("authorization = %q", got)
		}
		if got := r.Header.Get("X-Test"); got != "yes" {
			t.Fatalf("X-Test = %q", got)
		}
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			Temperature float64 `json:"temperature"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if body.Model != "override-model" {
			t.Fatalf("model = %q", body.Model)
		}
		if body.Temperature != 0 {
			t.Fatalf("temperature = %v", body.Temperature)
		}
		if len(body.Messages) != 2 {
			t.Fatalf("messages = %#v", body.Messages)
		}
		if body.Messages[0].Role != "system" || body.Messages[0].Content != "system prompt" {
			t.Fatalf("system message = %#v", body.Messages[0])
		}
		if body.Messages[1].Role != "user" || body.Messages[1].Content != "review prompt" {
			t.Fatalf("user message = %#v", body.Messages[1])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": reviewJSON}},
			},
		})
	}))
	defer server.Close()

	adapter := &OpenAICompatibleAdapter{
		BaseURL:      server.URL,
		APIKeyEnv:    "TAGTEAM_TEST_OPENAI_DIRECT_KEY",
		DefaultModel: "default-model",
		ExtraHeaders: map[string]string{"X-Test": "yes"},
	}
	result, err := adapter.RunDirect(RoleAdversary, Request{
		Context:      context.Background(),
		Prompt:       "review prompt",
		SystemPrompt: "system prompt",
		EnvOverlay:   map[string]string{"TAGTEAM_TEST_OPENAI_DIRECT_KEY": "secret-token"},
		Model:        "override-model",
	})
	if err != nil {
		t.Fatalf("RunDirect() error = %v", err)
	}
	if result.Review == nil || result.Review.Verdict != "pass" {
		t.Fatalf("review = %#v", result.Review)
	}
	if len(result.Command) == 0 || result.Command[0] != "POST" {
		t.Fatalf("command = %#v", result.Command)
	}
}

func TestOpenAICompatibleRunDirectWritesRawOnParseFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": ""}},
			},
		})
	}))
	defer server.Close()

	outputPath := filepath.Join(t.TempDir(), "scout-round-1.json")
	adapter := &OpenAICompatibleAdapter{
		BaseURL:      server.URL,
		DefaultModel: "openai/gpt-oss-20b",
	}
	_, err := adapter.RunDirect(RoleScout, Request{
		Context:    context.Background(),
		Prompt:     "scout",
		OutputPath: outputPath,
	})
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !fileExists(outputPath + ".raw") {
		t.Fatal("expected raw quarantine artifact")
	}
	if !fileExists(outputPath + ".validation-error.txt") {
		t.Fatal("expected validation error artifact")
	}
}

func TestOpenAICompatibleParseResultAcceptsMessageContent(t *testing.T) {
	adapter := &OpenAICompatibleAdapter{}
	raw := []byte(`{"choices":[{"message":{"content":"{\"schema_version\":1,\"verdict\":\"pass\",\"summary\":\"ok\",\"findings\":[],\"test_suggestions\":[]}"}}]}`)
	result, err := adapter.ParseResult(RoleAdversary, raw)
	if err != nil {
		t.Fatalf("ParseResult() error = %v", err)
	}
	if result.Review == nil || result.Review.Summary != "ok" {
		t.Fatalf("review = %#v", result.Review)
	}
}

func TestOpenAICompatibleParseResultAcceptsFencedJSON(t *testing.T) {
	adapter := &OpenAICompatibleAdapter{}
	raw := []byte("{\"choices\":[{\"message\":{\"content\":\"```json\\n{\\\"schema_version\\\":1,\\\"verdict\\\":\\\"pass\\\",\\\"summary\\\":\\\"ok\\\",\\\"findings\\\":[],\\\"test_suggestions\\\":[]}\\n```\"}}]}")
	result, err := adapter.ParseResult(RoleAdversary, raw)
	if err != nil {
		t.Fatalf("ParseResult() error = %v", err)
	}
	if result.Review == nil || result.Review.Verdict != "pass" {
		t.Fatalf("review = %#v", result.Review)
	}
}

func TestOpenAICompatibleCoderRoleUnsupported(t *testing.T) {
	adapter := &OpenAICompatibleAdapter{}
	_, err := adapter.BuildCmd(RoleCoder, Request{})
	if err == nil {
		t.Fatal("expected coder role error")
	}
	if !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("error = %v", err)
	}
	_, err = adapter.RunDirect(RoleCoder, Request{})
	if err == nil {
		t.Fatal("expected direct coder role error")
	}
	if !strings.Contains(err.Error(), "not as coder/worker") {
		t.Fatalf("error = %v", err)
	}
}

func TestOpenAICompatibleScoutRoleSupported(t *testing.T) {
	adapter := &OpenAICompatibleAdapter{BaseURL: "https://example.test/v1"}
	spec, err := adapter.BuildCmd(RoleScout, Request{Workdir: "/repo", OutputPath: "/tmp/scout.json"})
	if err != nil {
		t.Fatalf("BuildCmd scout error = %v", err)
	}
	if spec.Argv[0] != "openai-compatible" || !strings.HasSuffix(spec.Argv[2], "/chat/completions") {
		t.Fatalf("argv = %#v", spec.Argv)
	}

	raw := []byte(`{"choices":[{"message":{"content":"{\"schema_version\":1,\"mode\":\"recon\",\"summary\":\"mapped\",\"relevant_files\":[\"runner.go\"],\"likely_entry_points\":[],\"existing_patterns\":[],\"risks\":[],\"suggested_tests\":[],\"do_not_block\":true}"}}]}`)
	result, err := adapter.ParseResult(RoleScout, raw)
	if err != nil {
		t.Fatalf("ParseResult scout error = %v", err)
	}
	if result.Scout == nil || result.Scout.Summary != "mapped" || result.Scout.SchemaVersion != 1 {
		t.Fatalf("scout = %#v", result.Scout)
	}
}

func TestGoslingBuildCmdCoder(t *testing.T) {
	adapter := &GoslingAdapter{DefaultModel: "gemini-2.5-flash", ExtraArgs: []string{"--foo", "bar"}}
	spec, err := adapter.BuildCmd(RoleCoder, Request{
		Prompt:       "do something",
		SystemPrompt: "you are a helper",
		Workdir:      "/repo",
	})
	if err != nil {
		t.Fatalf("BuildCmd() error = %v", err)
	}
	want := []string{
		"gosling", "run", "--no-session", "--quiet", "--output-format", "json",
		"--model", "gemini-2.5-flash",
		"--instructions", "-",
		"--foo", "bar",
	}
	if !reflect.DeepEqual(spec.Argv, want) {
		t.Fatalf("argv mismatch\nwant: %#v\ngot:  %#v", want, spec.Argv)
	}
	if string(spec.Stdin) != "do something\n" {
		t.Fatalf("stdin = %q", string(spec.Stdin))
	}
}

func TestGoslingBuildCmdAdversary(t *testing.T) {
	adapter := &GoslingAdapter{DefaultModel: "gemini-2.5-flash"}
	_, err := adapter.BuildCmd(RoleAdversary, Request{
		Prompt:  "review code",
		Workdir: "/repo",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "gosling is not supported as an adversary adapter") {
		t.Fatalf("expected error containing 'gosling is not supported as an adversary adapter', got: %v", err)
	}
}

func TestGoslingBuildCmdSupervisor(t *testing.T) {
	adapter := &GoslingAdapter{DefaultModel: "gemini-2.5-flash"}
	_, err := adapter.BuildCmd(RoleSupervisor, Request{
		Prompt:  "write a brief",
		Workdir: "/repo",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "gosling is not supported as a supervisor adapter") {
		t.Fatalf("expected error containing 'gosling is not supported as a supervisor adapter', got: %v", err)
	}
}

func TestGoslingBuildCmdRejectsUnknownRole(t *testing.T) {
	adapter := &GoslingAdapter{}
	_, err := adapter.BuildCmd(Role("invalid"), Request{Prompt: "x", Workdir: "/repo"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "unsupported role") {
		t.Fatalf("error = %q", err.Error())
	}
}

func TestGoslingParseResultCoder(t *testing.T) {
	adapter := &GoslingAdapter{}
	raw := []byte(`{
  "messages": [
    {
      "role": "user",
      "content": [
        {
          "type": "text",
          "text": "Say hello"
        }
      ]
    },
    {
      "role": "assistant",
      "content": [
        {
          "type": "text",
          "text": "Hello, world!"
        }
      ]
    }
  ]
}`)
	result, err := adapter.ParseResult(RoleCoder, raw)
	if err != nil {
		t.Fatalf("ParseResult() error = %v", err)
	}
	if result.Text != "Hello, world!" {
		t.Fatalf("text = %q", result.Text)
	}
}

func TestGoslingParseResultAdversaryUnsupported(t *testing.T) {
	adapter := &GoslingAdapter{}
	_, err := adapter.ParseResult(RoleAdversary, []byte(`{"messages":[]}`))
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsOutputContractError(err) {
		t.Fatalf("expected output contract error, got %T", err)
	}
	if !strings.Contains(err.Error(), "not supported as an adversary adapter") {
		t.Fatalf("error = %q", err.Error())
	}
}

func TestGoslingDetectNonZeroExit(t *testing.T) {
	oldLookPath := execLookPath
	oldCommandContext := execCommandContext
	defer func() {
		execLookPath = oldLookPath
		execCommandContext = oldCommandContext
	}()

	execLookPath = func(file string) (string, error) {
		if file == "gosling" {
			return "/mock/bin/gosling", nil
		}
		return "", os.ErrNotExist
	}

	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "gosling" {
			return exec.CommandContext(ctx, "false")
		}
		return exec.CommandContext(ctx, name, args...)
	}

	adapter := &GoslingAdapter{}
	info, err := adapter.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if !info.Found {
		t.Fatal("expected Found = true")
	}
	if info.Runnable {
		t.Fatal("expected Runnable = false")
	}
	if !strings.Contains(info.Hint, "probe failed") {
		t.Fatalf("expected hint to mention probe failure, got: %q", info.Hint)
	}
}
