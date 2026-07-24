package tagteam

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecoverEditorFailureUsesConfiguredFallbackWithoutPartialDiff(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "before\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")
	baseline := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	runDir := t.TempDir()

	fallback := fakeDirectAdapter{
		build: func(role Role, req Request) (*CommandSpec, error) {
			return &CommandSpec{Argv: []string{"fallback"}, Dir: repo, Output: req.OutputPath}, nil
		},
		direct: func(role Role, req Request) (Result, error) {
			if !strings.Contains(req.Prompt, "change README") || !strings.Contains(req.Prompt, "primary editor failed") {
				t.Fatalf("fallback did not receive original task and recovery context: %q", req.Prompt)
			}
			mustWriteFile(t, filepath.Join(repo, "README.md"), "after\n")
			raw := []byte(`{"schema_version":1,"status":"completed","summary":"updated README","files_changed":["README.md"],"checks_run":[],"remaining_risks":[]}`)
			return Result{Raw: raw, Text: string(raw)}, nil
		},
	}
	primary := fakeDirectAdapter{
		build: func(role Role, req Request) (*CommandSpec, error) {
			return &CommandSpec{Argv: []string{"primary"}, Dir: repo, Output: req.OutputPath}, nil
		},
		direct: func(role Role, req Request) (Result, error) {
			return Result{}, fmt.Errorf("primary should not be reinvoked")
		},
	}
	before, err := captureWorktreeSnapshot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	opts := RunOptions{
		Workdir: repo,
		Mode:    ModeSupervisor,
		Timeout: 10 * time.Second,
		LossPolicy: RoleLossPolicies{
			Worker: LossPolicyReplaceThenBlock,
		},
		Fallbacks: RoleFallbacks{Worker: []string{"fallback:model"}},
	}
	request := Request{
		Context:               context.Background(),
		Prompt:                workerContractPrompt("change README"),
		Workdir:               repo,
		RunDir:                runDir,
		OutputPath:            filepath.Join(runDir, "worker-round-1.json"),
		Timeout:               10 * time.Second,
		ProgressRole:          Role("worker"),
		RequireWorkerContract: true,
	}
	final := FinalRun{RunID: "fallback-run", RunDir: runDir, Workdir: repo, Mode: ModeSupervisor}
	result, selected, _, err := NewApp(DefaultConfig()).recoverEditorFailure(
		context.Background(), opts, 1, runDir, baseline, "", "", request,
		RoleTarget{Adapter: "primary", Model: "model"}, primary, nil,
		map[string]Adapter{"fallback": fallback},
		&OutputContractError{Err: fmt.Errorf("invalid worker output")}, before, &final,
	)
	if err != nil {
		t.Fatalf("recoverEditorFailure() error = %v", err)
	}
	if roleTargetString(selected) != "fallback:model" || result.Worker == nil {
		t.Fatalf("fallback result = selected:%#v result:%#v", selected, result)
	}
	if data, readErr := os.ReadFile(filepath.Join(repo, "README.md")); readErr != nil || string(data) != "after\n" {
		t.Fatalf("fallback edit = %q err=%v", data, readErr)
	}
	var artifact RecoveryArtifact
	readJSONFile(t, filepath.Join(runDir, "recovery-round-1.json"), &artifact)
	if artifact.Status != "recovered" || artifact.Decision.Decision != "continue_with_fallback" || artifact.DiffPath != "" {
		t.Fatalf("zero-delta recovery artifact = %#v", artifact)
	}
}

func TestRecoverEditorFailureQuarantinesLiveScopeViolation(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "before\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")
	baseline := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	runDir := t.TempDir()
	before, err := captureWorktreeSnapshot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(repo, "README.md"), "partial\n")

	opts := RunOptions{
		Workdir: repo,
		Mode:    ModeSupervisor,
		LossPolicy: RoleLossPolicies{
			Worker: LossPolicyReplaceThenBlock,
		},
		Fallbacks: RoleFallbacks{Worker: []string{"fallback:model"}},
	}
	final := FinalRun{RunID: "scope-violation", RunDir: runDir, Workdir: repo, Mode: ModeSupervisor}
	_, _, _, err = NewApp(DefaultConfig()).recoverEditorFailure(
		context.Background(), opts, 1, runDir, baseline, "", "", Request{Workdir: repo, RunDir: runDir},
		RoleTarget{Adapter: "primary", Model: "model"}, fakeAdapter{}, nil,
		map[string]Adapter{}, &LiveScopeViolationError{Paths: []string{"outside.md"}}, before, &final,
	)
	if err == nil || final.Status != RunStatusQuarantined {
		t.Fatalf("final=%#v err=%v, want quarantined scope violation", final, err)
	}
	var artifact RecoveryArtifact
	readJSONFile(t, filepath.Join(runDir, "recovery-round-1.json"), &artifact)
	if artifact.Status != "quarantined" || artifact.Decision.Decision != "quarantine" {
		t.Fatalf("recovery artifact = %#v", artifact)
	}
	if strings.Contains(artifact.Decision.Reason, "supervisor unavailable") {
		t.Fatalf("scope violation should not invoke a recovery supervisor: %#v", artifact.Decision)
	}
}
