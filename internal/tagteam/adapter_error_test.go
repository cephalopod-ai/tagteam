package tagteam

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type contractErrorTestAdapter struct {
	workdir string
	error   error
}

func (a contractErrorTestAdapter) ID() string { return "contract-test" }

func (a contractErrorTestAdapter) Detect(context.Context) (VersionInfo, error) {
	return VersionInfo{Found: true, Runnable: true}, nil
}

func (a contractErrorTestAdapter) Capabilities() CapabilitySet { return CapabilitySet{} }

func (a contractErrorTestAdapter) BuildCmd(Role, Request) (*CommandSpec, error) {
	return &CommandSpec{Argv: []string{"sh", "-c", "printf '{\"bad\":true}'"}, Dir: a.workdir}, nil
}

func (a contractErrorTestAdapter) ParseResult(Role, []byte) (Result, error) {
	return Result{}, a.error
}

func TestRunAdapterBoundsContractErrorAndPersistsRawResult(t *testing.T) {
	app := NewApp(DefaultConfig())
	tmp := t.TempDir()
	outputPath := filepath.Join(tmp, "supervisor-round-1.json")
	largeMessage := "HEAD-MARKER\n" + strings.Repeat("untrusted prompt content ", 400) + "\nTAIL-MARKER"
	adapter := contractErrorTestAdapter{
		workdir: tmp,
		error:   &OutputContractError{Err: fmt.Errorf("%s", largeMessage)},
	}
	result, err := app.runAdapter(context.Background(), adapter, RoleAdversary, Request{
		Context:      context.Background(),
		Prompt:       "review prompt",
		RunDir:       tmp,
		OutputPath:   outputPath,
		Timeout:      time.Second,
		InputMode:    "inline",
		ProgressRole: RoleSupervisor,
	}, false)
	if err == nil || !IsOutputContractError(err) {
		t.Fatalf("error = %T %v, want output contract error", err, err)
	}
	for _, want := range []string{"HEAD-MARKER", "full validation error: " + outputPath + ".validation-error.txt"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("bounded error missing %q: %q", want, err)
		}
	}
	if strings.Contains(err.Error(), "untrusted prompt content") || strings.Contains(err.Error(), "TAIL-MARKER") {
		t.Fatalf("bounded error leaked provider body: %q", err)
	}
	if string(result.Raw) != `{"bad":true}` {
		t.Fatalf("result raw = %q, want provider output for repair", result.Raw)
	}
	entries, readErr := os.ReadDir(filepath.Join(tmp, "deliveries"))
	if readErr != nil {
		t.Fatalf("read deliveries: %v", readErr)
	}
	var recordPath string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			recordPath = filepath.Join(tmp, "deliveries", entry.Name())
			break
		}
	}
	if recordPath == "" {
		t.Fatalf("delivery JSON missing from %#v", entries)
	}
	if !strings.Contains(filepath.Base(recordPath), "supervisor-contract-test") {
		t.Fatalf("delivery record path = %q, want logical supervisor role", recordPath)
	}
	var record DeliveryRecord
	data, readErr := os.ReadFile(recordPath)
	if readErr != nil {
		t.Fatalf("read delivery record: %v", readErr)
	}
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("decode delivery record: %v", err)
	}
	if record.Role != RoleSupervisor || !strings.Contains(record.Error, "HEAD-MARKER") {
		t.Fatalf("delivery record = %#v", record)
	}
}

func TestRunAdapterRedactsContractErrorBeforeReturningIt(t *testing.T) {
	app := NewApp(DefaultConfig())
	tmp := t.TempDir()
	adapter := contractErrorTestAdapter{
		workdir: tmp,
		error:   &OutputContractError{Err: fmt.Errorf("provider echoed overlay-secret-token")},
	}
	_, err := app.runAdapter(context.Background(), adapter, RoleAdversary, Request{
		Context:    context.Background(),
		RunDir:     tmp,
		OutputPath: filepath.Join(tmp, "review.json"),
		EnvOverlay: map[string]string{"TEST_API_KEY": "overlay-secret-token"},
		Timeout:    time.Second,
	}, false)
	if err == nil || !IsOutputContractError(err) {
		t.Fatalf("error = %T %v, want output contract error", err, err)
	}
	if strings.Contains(err.Error(), "overlay-secret-token") || !strings.Contains(err.Error(), redactedSecret) {
		t.Fatalf("returned error was not redacted: %q", err)
	}
}
