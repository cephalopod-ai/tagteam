package tagteam

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func invalidOpenAICompatibleResponse() *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":""}}]}`)),
	}
}

func TestOpenAICompatibleRunAdapterPersistsParseFailureArtifacts(t *testing.T) {
	runDir := t.TempDir()
	outputPath := filepath.Join(runDir, "scout-round-1.json")
	adapter := &OpenAICompatibleAdapter{
		BaseURL:      "http://openai-compatible.test",
		DefaultModel: "test-model",
		HTTPClient:   &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) { return invalidOpenAICompatibleResponse(), nil })},
	}
	_, err := NewApp(DefaultConfig()).runAdapter(context.Background(), adapter, RoleScout, Request{Context: context.Background(), RunDir: runDir, OutputPath: outputPath}, false)
	if err == nil {
		t.Fatal("expected parse failure")
	}
	for _, path := range []string{outputPath + ".raw", outputPath + ".validation-error.txt"} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("runner did not persist %s: %v", path, err)
		}
	}
}

func TestOpenAICompatibleRunAdapterRejectsRunDirReplacementAfterResponse(t *testing.T) {
	fx := newHelperFX(t, "direct-sidecar-replacement", false)
	external := t.TempDir()
	outputPath := filepath.Join(fx.runDir, "scout-round-1.json")
	adapter := &OpenAICompatibleAdapter{
		BaseURL:      "http://openai-compatible.test",
		DefaultModel: "test-model",
		HTTPClient: &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			replaceRunDirSymlink(t, fx.runDir, external)
			return invalidOpenAICompatibleResponse(), nil
		})},
	}
	_, err := NewApp(DefaultConfig()).runAdapter(fx.ctx, adapter, RoleScout, Request{Context: fx.ctx, RunDir: fx.runDir, OutputPath: outputPath}, false)
	assertPreflight(t, err)
	for _, path := range []string{outputPath + ".raw", outputPath + ".validation-error.txt"} {
		if _, err := os.Stat(filepath.Join(external, filepath.Base(path))); !os.IsNotExist(err) {
			t.Fatalf("external sidecar %s was written: %v", path, err)
		}
	}
}
