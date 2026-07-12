package tagteam

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRedactSecrets_ReplacesSensitiveEnvValues(t *testing.T) {
	t.Setenv("FEATHERLESS_API_KEY", "secret-token")
	t.Setenv("OPENROUTER_API_KEY", "another-secret")

	got := redactSecrets("Bearer secret-token and another-secret should not print")
	if strings.Contains(got, "secret-token") || strings.Contains(got, "another-secret") {
		t.Fatalf("redacted text still contains secret: %q", got)
	}
	if count := strings.Count(got, redactedSecret); count != 2 {
		t.Fatalf("redacted count = %d, want 2: %q", count, got)
	}
}

func TestRedactSecretsWithOverlay_ReplacesSensitiveOverlayValues(t *testing.T) {
	got := redactSecretsWithOverlay("Bearer dotenv-only-token should not print", map[string]string{
		"PURDUE_API_KEY": "dotenv-only-token",
	})
	if strings.Contains(got, "dotenv-only-token") {
		t.Fatalf("redacted text still contains overlay secret: %q", got)
	}
	if !strings.Contains(got, redactedSecret) {
		t.Fatalf("redacted text missing marker: %q", got)
	}
}

func TestRedactSecretsWithOverlayIncludesEffectiveOverlayWhenShellKeyExists(t *testing.T) {
	t.Setenv("PURDUE_API_KEY", "shell-secret-token")
	got := redactSecretsWithOverlay("shell-secret-token overlay-secret-token", map[string]string{
		"PURDUE_API_KEY": "overlay-secret-token",
	})
	if strings.Contains(got, "shell-secret-token") || strings.Contains(got, "overlay-secret-token") {
		t.Fatalf("redacted text still contains effective secret: %q", got)
	}
	if count := strings.Count(got, redactedSecret); count != 2 {
		t.Fatalf("redacted count = %d, want 2: %q", count, got)
	}
}

func TestRunAdapterRedactsPersistedPromptArgvAndRawArtifacts(t *testing.T) {
	app := NewApp(DefaultConfig())
	runDir := t.TempDir()
	outputPath := filepath.Join(runDir, "review.json")
	secret := "overlay-secret-token"
	adapter := fakeAdapter{
		build: func(role Role, req Request) (*CommandSpec, error) {
			return &CommandSpec{
				Argv: []string{"/bin/sh", "-c", "printf '%s' " + secret},
				Dir:  runDir,
			}, nil
		},
		parse: func(role Role, raw []byte) (Result, error) {
			return Result{Text: string(raw)}, nil
		},
	}
	_, err := app.runAdapter(context.Background(), adapter, RoleReporter, Request{
		Context:    context.Background(),
		Prompt:     "prompt contains " + secret,
		EnvOverlay: map[string]string{"PURDUE_API_KEY": secret},
		RunDir:     runDir,
		OutputPath: outputPath,
		Timeout:    5 * time.Second,
	}, false)
	if err != nil {
		t.Fatalf("runAdapter() error = %v", err)
	}
	for _, path := range []string{outputPath, outputPath + ".raw"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if strings.Contains(string(data), secret) {
			t.Fatalf("%s leaked secret: %q", path, string(data))
		}
	}
	entries, err := os.ReadDir(filepath.Join(runDir, "deliveries"))
	if err != nil {
		t.Fatalf("read deliveries: %v", err)
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(runDir, "deliveries", entry.Name()))
		if err != nil {
			t.Fatalf("read delivery %s: %v", entry.Name(), err)
		}
		if strings.Contains(string(data), secret) {
			t.Fatalf("delivery artifact %s leaked secret: %q", entry.Name(), string(data))
		}
	}
}

func TestRunAdapterFailsWhenOutputExceedsLimit(t *testing.T) {
	app := NewApp(DefaultConfig())
	runDir := t.TempDir()
	adapter := fakeAdapter{
		build: func(role Role, req Request) (*CommandSpec, error) {
			return &CommandSpec{
				Argv: []string{"/bin/sh", "-c", "printf '1234567890'"},
				Dir:  runDir,
			}, nil
		},
		parse: func(role Role, raw []byte) (Result, error) {
			return Result{Text: string(raw)}, nil
		},
	}
	_, err := app.runAdapter(context.Background(), adapter, RoleReporter, Request{
		Context:        context.Background(),
		RunDir:         runDir,
		OutputPath:     filepath.Join(runDir, "out.txt"),
		Timeout:        5 * time.Second,
		MaxOutputBytes: 5,
	}, false)
	if err == nil {
		t.Fatal("expected output limit error")
	}
	if !strings.Contains(err.Error(), "max_output_bytes=5") {
		t.Fatalf("error = %v", err)
	}
}

func TestOpenAICompatibleRunDirectRedactsErrorBody(t *testing.T) {
	t.Setenv("FEATHERLESS_API_KEY", "secret-token")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad key secret-token", http.StatusUnauthorized)
	}))
	defer server.Close()

	adapter := &OpenAICompatibleAdapter{
		BaseURL:      server.URL,
		APIKeyEnv:    "FEATHERLESS_API_KEY",
		DefaultModel: "openai/gpt-oss-120b",
	}
	_, err := adapter.RunDirect(RoleAdversary, Request{
		Context: context.Background(),
		Prompt:  "review this",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("error leaked secret: %v", err)
	}
	if !strings.Contains(err.Error(), redactedSecret) {
		t.Fatalf("error did not show redaction marker: %v", err)
	}
}

func TestOpenAICompatibleRunDirectRedactsOverlayErrorBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad key overlay-secret-token", http.StatusUnauthorized)
	}))
	defer server.Close()

	adapter := &OpenAICompatibleAdapter{
		BaseURL:      server.URL,
		APIKeyEnv:    "PURDUE_API_KEY",
		DefaultModel: "openai/gpt-oss-120b",
	}
	_, err := adapter.RunDirect(RoleAdversary, Request{
		Context:    context.Background(),
		Prompt:     "review this",
		EnvOverlay: map[string]string{"PURDUE_API_KEY": "overlay-secret-token"},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "overlay-secret-token") {
		t.Fatalf("error leaked overlay secret: %v", err)
	}
	if !strings.Contains(err.Error(), redactedSecret) {
		t.Fatalf("error did not show redaction marker: %v", err)
	}
}
