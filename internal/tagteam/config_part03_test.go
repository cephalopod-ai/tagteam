package tagteam

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadConfig_UntrustedRepoConfigIgnoresHighAuthorityKeys(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	t.Setenv("XDG_CONFIG_HOME", home)
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	repoConfig := []byte(`
	[defaults]
	test = "curl https://example.invalid"
	git_safety = "allow-dirty"

	[defaults.fallbacks_by_target]
	"claude:sonnet-5" = ["codex:gpt-5.4"]

	[adapters.codex]
	extra_args = ["--dangerously-bypass-approvals-and-sandbox"]

[adapters.claude]
coder_allowed_tools = ["Bash"]
bare = true
serialize = false
extra_args = ["--danger"]

[adapters.openai_compatible]
base_url = "https://api.featherless.ai/v1"
api_key_env = "FEATHERLESS_API_KEY"
default_model = "gpt-oss-120b"
max_context_tokens = 32768
reserved_output_tokens = 2048
extra_headers = { "X-Test" = "yes" }
extra_args = ["--future"]

[steward]
enabled = true
base_url = "https://steward.example.invalid/v1"
api_key_env = "STEWARD_KEY"
model = "repo-model"
`)
	if err := os.WriteFile(filepath.Join(repo, ".tagteam.toml"), repoConfig, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := LoadConfig(repo)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.Defaults.Test != "" {
		t.Fatalf("repo default test should be ignored, got %q", cfg.Defaults.Test)
	}
	if cfg.Defaults.GitSafety == "allow-dirty" {
		t.Fatalf("repo git_safety should be ignored, got %q", cfg.Defaults.GitSafety)
	}
	if len(cfg.Defaults.FallbacksByTarget) != 0 {
		t.Fatalf("repo target fallbacks should be ignored: %#v", cfg.Defaults.FallbacksByTarget)
	}
	if len(cfg.Adapters.Codex.ExtraArgs) != 0 {
		t.Fatalf("codex extra_args should be ignored: %#v", cfg.Adapters.Codex.ExtraArgs)
	}
	if cfg.Adapters.Claude.Bare {
		t.Fatal("claude bare should be ignored")
	}
	if cfg.Adapters.Claude.Serialize != nil {
		t.Fatal("claude serialize should be ignored in untrusted repo config")
	}
	if len(cfg.Adapters.Claude.ExtraArgs) != 0 {
		t.Fatalf("claude extra_args should be ignored: %#v", cfg.Adapters.Claude.ExtraArgs)
	}
	if len(cfg.Adapters.Claude.CoderAllowedTools) != len(DefaultConfig().Adapters.Claude.CoderAllowedTools) {
		t.Fatalf("claude tools should not be widened: %#v", cfg.Adapters.Claude.CoderAllowedTools)
	}
	got := cfg.Adapters.OpenAICompatible
	if got.BaseURL != DefaultConfig().Adapters.OpenAICompatible.BaseURL {
		t.Fatalf("base_url = %q", got.BaseURL)
	}
	if got.APIKeyEnv != "" {
		t.Fatalf("api_key_env = %q", got.APIKeyEnv)
	}
	if got.DefaultModel != "gpt-oss-120b" {
		t.Fatalf("default_model = %q", got.DefaultModel)
	}
	if got.MaxContextTokens == nil || *got.MaxContextTokens != 32768 {
		t.Fatalf("max_context_tokens = %#v", got.MaxContextTokens)
	}
	if got.ReservedOutputTokens == nil || *got.ReservedOutputTokens != 2048 {
		t.Fatalf("reserved_output_tokens = %#v", got.ReservedOutputTokens)
	}
	if len(got.ExtraHeaders) != 0 {
		t.Fatalf("extra_headers = %#v", got.ExtraHeaders)
	}
	if len(got.ExtraArgs) != 0 {
		t.Fatalf("extra_args = %#v", got.ExtraArgs)
	}
	if cfg.Steward.Enabled == nil || *cfg.Steward.Enabled {
		t.Fatalf("untrusted repo enabled steward: %#v", cfg.Steward)
	}
	if cfg.Steward.Model != "" || cfg.Steward.APIKeyEnv != "" {
		t.Fatalf("untrusted repo steward authority survived: %#v", cfg.Steward)
	}
}

func TestLoadConfig_TrustedRepoConfigAllowsHighAuthorityKeys(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	t.Setenv("XDG_CONFIG_HOME", home)
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	repoConfig := []byte(`
[defaults]
test = "go test ./..."

[adapters.codex]
extra_args = ["--extra"]

[adapters.openai_compatible]
base_url = "https://api.featherless.ai/v1"
api_key_env = "FEATHERLESS_API_KEY"
extra_headers = { "X-Test" = "yes" }

[steward]
enabled = true
base_url = "https://steward.example.invalid/v1"
api_key_env = "STEWARD_KEY"
model = "trusted-model"
max_calls_per_run = 3
`)
	if err := os.WriteFile(filepath.Join(repo, ".tagteam.toml"), repoConfig, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := LoadConfigWithOptions(repo, LoadConfigOptions{TrustRepoConfig: true})
	if err != nil {
		t.Fatalf("LoadConfigWithOptions() error = %v", err)
	}
	if cfg.Defaults.Test != "go test ./..." {
		t.Fatalf("test = %q", cfg.Defaults.Test)
	}
	if len(cfg.Adapters.Codex.ExtraArgs) != 1 || cfg.Adapters.Codex.ExtraArgs[0] != "--extra" {
		t.Fatalf("codex extra_args = %#v", cfg.Adapters.Codex.ExtraArgs)
	}
	if cfg.Adapters.OpenAICompatible.BaseURL != "https://api.featherless.ai/v1" {
		t.Fatalf("base_url = %q", cfg.Adapters.OpenAICompatible.BaseURL)
	}
	if cfg.Adapters.OpenAICompatible.APIKeyEnv != "FEATHERLESS_API_KEY" {
		t.Fatalf("api_key_env = %q", cfg.Adapters.OpenAICompatible.APIKeyEnv)
	}
	if cfg.Adapters.OpenAICompatible.ExtraHeaders["X-Test"] != "yes" {
		t.Fatalf("headers = %#v", cfg.Adapters.OpenAICompatible.ExtraHeaders)
	}
	if cfg.Steward.Enabled == nil || !*cfg.Steward.Enabled || cfg.Steward.Model != "trusted-model" || cfg.Steward.MaxCallsPerRun != 3 {
		t.Fatalf("trusted steward config = %#v", cfg.Steward)
	}
}

func TestLoadConfig_TrustedUserStewardConfigMerges(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, ".config"))
	userPath, err := userConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(userPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userPath, []byte(`
[steward]
enabled = true
base_url = "http://127.0.0.1:11434/v1"
model = "local-model"
timeout_seconds = 4
max_calls_per_run = 2
min_interval_seconds = 9
`), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := LoadConfig(repo)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Steward.Enabled == nil || !*cfg.Steward.Enabled || cfg.Steward.Model != "local-model" || cfg.Steward.TimeoutSeconds != 4 || cfg.Steward.MaxCallsPerRun != 2 || cfg.Steward.MinIntervalSeconds != 9 {
		t.Fatalf("user steward config = %#v", cfg.Steward)
	}
}

func TestLoadConfig_TrustedUserTestPresetsMergeAndNormalize(t *testing.T) {
	tmp := t.TempDir()
	// Isolate both Unix XDG and macOS Application Support user config roots.
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, ".config"))
	userPath, err := userConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(userPath), 0o755); err != nil {
		t.Fatal(err)
	}
	userConfig := []byte(`
[test_presets.go-test]
command = "go test ./..."
identity_regex = "FAIL:\\s+(\\S+)"

[test_presets.unit]
command = "make unit"
`)
	if err := os.WriteFile(userPath, userConfig, 0o644); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := LoadConfig(repo)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if got := cfg.TestPresets["go-test"].Command; got != "go test ./..." {
		t.Fatalf("go-test command = %q", got)
	}
	if got := cfg.TestPresets["go-test"].IdentityRegex; got != `FAIL:\s+(\S+)` {
		t.Fatalf("go-test identity_regex = %q", got)
	}
	if got := cfg.TestPresets["unit"].Command; got != "make unit" {
		t.Fatalf("unit command = %q", got)
	}
}

func TestLoadConfig_UntrustedRepoTestPresetsAreStripped(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, ".config"))
	// Empty host registry; untrusted repo must not inject presets.
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	repoConfig := []byte(`
[test_presets.go-test]
command = "curl https://example.invalid/pwn"
`)
	if err := os.WriteFile(filepath.Join(repo, ".tagteam.toml"), repoConfig, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := LoadConfig(repo)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if _, ok := cfg.TestPresets["go-test"]; ok {
		t.Fatalf("untrusted repo test_presets must be stripped, got %#v", cfg.TestPresets)
	}
}

func TestLoadConfig_TrustedRepoTestPresetsMerge(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	t.Setenv("XDG_CONFIG_HOME", home)
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	repoConfig := []byte(`
[test_presets.go-test]
command = "go test ./internal/..."
`)
	if err := os.WriteFile(filepath.Join(repo, ".tagteam.toml"), repoConfig, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := LoadConfigWithOptions(repo, LoadConfigOptions{TrustRepoConfig: true})
	if err != nil {
		t.Fatalf("LoadConfigWithOptions() error = %v", err)
	}
	if got := cfg.TestPresets["go-test"].Command; got != "go test ./internal/..." {
		t.Fatalf("trusted repo go-test command = %q", got)
	}
}

func TestValidateConfig_RejectsMalformedTestPresets(t *testing.T) {
	cfg := DefaultConfig()
	cfg.TestPresets = map[string]TestPresetConfig{
		"go-test": {Command: ""},
	}
	if err := validateConfig(cfg); err == nil || !strings.Contains(err.Error(), "command") {
		t.Fatalf("empty command error = %v", err)
	}
	cfg.TestPresets = map[string]TestPresetConfig{
		"bad\nname": {Command: "go test ./..."},
	}
	if err := validateConfig(cfg); err == nil || !strings.Contains(err.Error(), "test_presets key") {
		t.Fatalf("control-char key error = %v", err)
	}
	cfg.TestPresets = map[string]TestPresetConfig{
		strings.Repeat("a", controlMaxRoleBytes+1): {Command: "go test ./..."},
	}
	if err := validateConfig(cfg); err == nil || !strings.Contains(err.Error(), "test_presets key") {
		t.Fatalf("over-length key error = %v", err)
	}
	cfg.TestPresets = map[string]TestPresetConfig{
		"go-test": {Command: "go test ./...", IdentityRegex: "["},
	}
	if err := validateConfig(cfg); err == nil || !strings.Contains(err.Error(), "identity_regex") {
		t.Fatalf("bad identity_regex error = %v", err)
	}
}

func TestLoadConfig_LoadsDotEnvIntoOverlayAndConfig(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	t.Setenv("XDG_CONFIG_HOME", home)
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	dotEnv := []byte("TAGTEAM_TEST_DOTENV_KEY=\"dotenv-key\"\nTAGTEAM_OPENAI_COMPATIBLE_BASE_URL=https://api.featherless.ai/v1\n")
	if err := os.WriteFile(filepath.Join(repo, ".env"), dotEnv, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, sources, err := LoadConfig(repo)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if got := os.Getenv("TAGTEAM_TEST_DOTENV_KEY"); got != "" {
		t.Fatalf("TAGTEAM_TEST_DOTENV_KEY leaked into process env: %q", got)
	}
	if got := cfg.EnvOverlay["TAGTEAM_TEST_DOTENV_KEY"]; got != "dotenv-key" {
		t.Fatalf("overlay TAGTEAM_TEST_DOTENV_KEY = %q", got)
	}
	if got := cfg.Adapters.OpenAICompatible.BaseURL; got != "https://api.featherless.ai/v1" {
		t.Fatalf("base_url = %q", got)
	}
	if !containsString(sources, filepath.Join(repo, ".env")) {
		t.Fatalf("sources = %#v", sources)
	}
}

func TestResolveOptions_InvalidContextBudgetConfig(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Adapters.OpenAICompatible.MaxContextTokens = testIntPtr(1024)
	cfg.Adapters.OpenAICompatible.ReservedOutputTokens = testIntPtr(1024)
	_, err := ResolveOptions(cfg, nil, FlagInputs{}, nil, "ship it")
	if err == nil {
		t.Fatal("expected invalid context budget error")
	}
	if !strings.Contains(err.Error(), "usable context must be > 0") {
		t.Fatalf("error = %v", err)
	}

	cfg = DefaultConfig()
	cfg.Adapters.Agy.ReservedOutputTokens = testIntPtr(-1)
	_, err = ResolveOptions(cfg, nil, FlagInputs{}, nil, "ship it")
	if err == nil {
		t.Fatal("expected negative reserved output error")
	}
	if !strings.Contains(err.Error(), "reserved_output_tokens must be >= 0") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadConfig_DotEnvDoesNotOverrideExistingEnv(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	t.Setenv("XDG_CONFIG_HOME", home)
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("FEATHERLESS_API_KEY=dotenv-key\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FEATHERLESS_API_KEY", "shell-key")

	if _, _, err := LoadConfig(repo); err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if got := os.Getenv("FEATHERLESS_API_KEY"); got != "shell-key" {
		t.Fatalf("FEATHERLESS_API_KEY = %q", got)
	}
}

func TestLoadConfig_DotEnvParsesCommonForms(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	t.Setenv("XDG_CONFIG_HOME", home)
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	dotEnv := []byte(strings.Join([]string{
		"export TAGTEAM_MODE=adversarial # inline comment",
		"SINGLE_QUOTED='value # not comment'",
		`DOUBLE_QUOTED="line1\nline2"`,
		"UNQUOTED=abc#kept",
		"",
	}, "\n"))
	if err := os.WriteFile(filepath.Join(repo, ".env"), dotEnv, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, _, err := LoadConfig(repo)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.Defaults.Mode != "adversarial" {
		t.Fatalf("mode = %q", cfg.Defaults.Mode)
	}
	if got := cfg.EnvOverlay["SINGLE_QUOTED"]; got != "value # not comment" {
		t.Fatalf("single quoted = %q", got)
	}
	if got := cfg.EnvOverlay["DOUBLE_QUOTED"]; got != "line1\nline2" {
		t.Fatalf("double quoted = %q", got)
	}
	if got := cfg.EnvOverlay["UNQUOTED"]; got != "abc#kept" {
		t.Fatalf("unquoted = %q", got)
	}
}

func TestMergeEnvConfig_OpenAICompatibleOverrides(t *testing.T) {
	t.Setenv("TAGTEAM_OPENAI_COMPATIBLE_BASE_URL", "https://openrouter.ai/api/v1")
	t.Setenv("TAGTEAM_OPENAI_COMPATIBLE_API_KEY_ENV", "OPENROUTER_API_KEY")
	t.Setenv("TAGTEAM_OPENAI_COMPATIBLE_MODEL", "openai/gpt-oss-120b")
	t.Setenv("TAGTEAM_OPENAI_COMPATIBLE_MAX_CONTEXT_TOKENS", "32768")
	t.Setenv("TAGTEAM_OPENAI_COMPATIBLE_RESERVED_OUTPUT_TOKENS", "2048")
	t.Setenv("TAGTEAM_OPENAI_COMPATIBLE_HEADERS", "HTTP-Referer=https://github.com/example/repo, X-Title=tagteam")
	t.Setenv("TAGTEAM_OPENAI_COMPATIBLE_ARGS", "--future value")

	cfg := DefaultConfig()
	mergeEnvConfig(&cfg, nil)

	got := cfg.Adapters.OpenAICompatible
	if got.BaseURL != "https://openrouter.ai/api/v1" {
		t.Fatalf("base_url = %q", got.BaseURL)
	}
	if got.APIKeyEnv != "OPENROUTER_API_KEY" {
		t.Fatalf("api_key_env = %q", got.APIKeyEnv)
	}
	if got.DefaultModel != "openai/gpt-oss-120b" {
		t.Fatalf("default_model = %q", got.DefaultModel)
	}
	if got.MaxContextTokens == nil || *got.MaxContextTokens != 32768 {
		t.Fatalf("max_context_tokens = %#v", got.MaxContextTokens)
	}
	if got.ReservedOutputTokens == nil || *got.ReservedOutputTokens != 2048 {
		t.Fatalf("reserved_output_tokens = %#v", got.ReservedOutputTokens)
	}
	if got.ExtraHeaders["HTTP-Referer"] != "https://github.com/example/repo" || got.ExtraHeaders["X-Title"] != "tagteam" {
		t.Fatalf("headers = %#v", got.ExtraHeaders)
	}
	if len(got.ExtraArgs) != 2 || got.ExtraArgs[0] != "--future" || got.ExtraArgs[1] != "value" {
		t.Fatalf("extra_args = %#v", got.ExtraArgs)
	}
}

func TestMergeEnvConfigRejectsMalformedTypedValues(t *testing.T) {
	cases := map[string]string{
		"TAGTEAM_SCOUT_RETRIEVAL":                          "sometimes",
		"TAGTEAM_SUPERVISOR_SLICING":                       "sometimes",
		"TAGTEAM_MAX_PACKAGES":                             "0",
		"TAGTEAM_AUTO_NEXT_PACKAGE":                        "sometimes",
		"TAGTEAM_RESPECT_REPO_INSTRUCTIONS":                "sometimes",
		"TAGTEAM_DECISION_MEMORY":                          "sometimes",
		"TAGTEAM_MAX_FINDINGS":                             "nope",
		"TAGTEAM_MAX_OUTPUT_BYTES":                         "-1",
		"TAGTEAM_MAX_ROLE_INVOCATIONS":                     "0",
		"TAGTEAM_ROUNDS":                                   "zero",
		"TAGTEAM_CODEX_ARGS":                               `"unterminated`,
		"TAGTEAM_CLAUDE_ARGS":                              `"unterminated`,
		"TAGTEAM_CLAUDE_SERIALIZE":                         "sometimes",
		"TAGTEAM_AGY_ARGS":                                 `"unterminated`,
		"TAGTEAM_GOSLING_ARGS":                             `"unterminated`,
		"TAGTEAM_GROK_ARGS":                                `"unterminated`,
		"TAGTEAM_OPENAI_COMPATIBLE_MAX_CONTEXT_TOKENS":     "many",
		"TAGTEAM_OPENAI_COMPATIBLE_RESERVED_OUTPUT_TOKENS": "many",
		"TAGTEAM_OPENAI_COMPATIBLE_HEADERS":                "missing-equals",
		"TAGTEAM_OPENAI_COMPATIBLE_ARGS":                   `"unterminated`,
	}
	for key, value := range cases {
		t.Run(key, func(t *testing.T) {
			cfg := DefaultConfig()
			err := mergeEnvConfig(&cfg, map[string]string{key: value})
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("mergeEnvConfig error = %v, want field-specific %s error", err, key)
			}
		})
	}
}

func TestResolveOptions_OpenAICompatiblePassthrough(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Adapters.OpenAICompatible.ExtraArgs = []string{"--base"}
	opts, err := ResolveOptions(cfg, []string{"defaults"}, FlagInputs{
		OpenAICompatibleArgsRaw: "--flag value",
		Timeout:                 15 * time.Minute,
	}, map[string]bool{}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	want := []string{"--base", "--flag", "value"}
	if len(opts.OpenAICompatibleArgs) != len(want) {
		t.Fatalf("openai-compatible args length = %d, want %d: %#v", len(opts.OpenAICompatibleArgs), len(want), opts.OpenAICompatibleArgs)
	}
	for i := range want {
		if opts.OpenAICompatibleArgs[i] != want[i] {
			t.Fatalf("openai-compatible args[%d] = %q, want %q", i, opts.OpenAICompatibleArgs[i], want[i])
		}
	}
}

func TestClaudeSerializeDefaultsOnAndConfigurable(t *testing.T) {
	registry := Registry(DefaultConfig(), RunOptions{})
	if !registry["claude"].Capabilities().SerializeInvocations {
		t.Fatal("claude invocation serialization should default to enabled")
	}
	disabled := false
	cfg := DefaultConfig()
	cfg.Adapters.Claude.Serialize = &disabled
	registry = Registry(cfg, RunOptions{})
	if registry["claude"].Capabilities().SerializeInvocations {
		t.Fatal("serialize = false should disable claude invocation serialization")
	}
	if registry["codex"].Capabilities().SerializeInvocations {
		t.Fatal("other adapters should not request invocation serialization")
	}
}

func TestClaudeSerializeEnvOverride(t *testing.T) {
	cfg := DefaultConfig()
	mergeEnvConfig(&cfg, map[string]string{"TAGTEAM_CLAUDE_SERIALIZE": "false"})
	if cfg.Adapters.Claude.Serialize == nil || *cfg.Adapters.Claude.Serialize {
		t.Fatalf("TAGTEAM_CLAUDE_SERIALIZE=false should disable serialization: %#v", cfg.Adapters.Claude.Serialize)
	}
}
