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

func TestRecoverEditorFailureContinuesPartialDiffAfterFencedSupervisorDecision(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "before\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")
	baseline := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	before, err := captureWorktreeSnapshot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(repo, "README.md"), "partial\n")

	reviewer := fakeDirectAdapter{
		build: func(role Role, req Request) (*CommandSpec, error) {
			return &CommandSpec{Argv: []string{"reviewer"}, Dir: repo, Output: req.OutputPath}, nil
		},
		direct: func(role Role, req Request) (Result, error) {
			if role != RoleSupervisor {
				t.Fatalf("reviewer role = %q", role)
			}
			for _, required := range []string{"\"schema_version\": 1", "\"evidence\"", "README.md"} {
				if !strings.Contains(req.Prompt, required) {
					t.Fatalf("recovery prompt missing %q:\n%s", required, req.Prompt)
				}
			}
			return Result{Text: "```json\n{\"schema_version\":1,\"decision\":\"continue_with_fallback\",\"reason\":\"the partial README change is in scope and the primary worker contract failed\",\"evidence\":[\"README.md changed\",\"primary worker returned invalid JSON\"]}\n```"}, nil
		},
	}
	fallback := fakeDirectAdapter{
		build: func(role Role, req Request) (*CommandSpec, error) {
			return &CommandSpec{Argv: []string{"fallback"}, Dir: repo, Output: req.OutputPath}, nil
		},
		direct: func(role Role, req Request) (Result, error) {
			if role != RoleCoder || !strings.Contains(req.Prompt, "preserve the existing partial diff") {
				t.Fatalf("fallback request role=%q prompt=%q", role, req.Prompt)
			}
			mustWriteFile(t, filepath.Join(repo, "FOLLOWUP.md"), "completed\n")
			raw := []byte(`{"schema_version":1,"status":"completed","summary":"completed the recovery","files_changed":["FOLLOWUP.md"],"checks_run":[],"remaining_risks":[]}`)
			return Result{Raw: raw, Text: string(raw)}, nil
		},
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
	runDir := t.TempDir()
	request := Request{
		Context:               context.Background(),
		Prompt:                workerContractPrompt("complete the README work"),
		Workdir:               repo,
		RunDir:                runDir,
		OutputPath:            filepath.Join(runDir, "worker-round-1.json"),
		Timeout:               10 * time.Second,
		ProgressRole:          Role("worker"),
		RequireWorkerContract: true,
	}
	final := FinalRun{RunID: "partial-diff-fallback", RunDir: runDir, Workdir: repo, Mode: ModeSupervisor}
	result, selected, _, err := NewApp(DefaultConfig()).recoverEditorFailure(
		context.Background(), opts, 1, runDir, baseline, "", "", request,
		RoleTarget{Adapter: "primary", Model: "model"}, fakeAdapter{}, reviewer,
		map[string]Adapter{"fallback": fallback}, &OutputContractError{Err: fmt.Errorf("invalid worker output")}, before, &final,
	)
	if err != nil {
		t.Fatalf("recoverEditorFailure() error = %v", err)
	}
	if roleTargetString(selected) != "fallback:model" || result.Worker == nil {
		t.Fatalf("fallback result = selected:%#v result:%#v", selected, result)
	}
	if data, readErr := os.ReadFile(filepath.Join(repo, "FOLLOWUP.md")); readErr != nil || string(data) != "completed\n" {
		t.Fatalf("fallback edit = %q err=%v", data, readErr)
	}
	var artifact RecoveryArtifact
	readJSONFile(t, filepath.Join(runDir, "recovery-round-1.json"), &artifact)
	if artifact.Status != "recovered" || artifact.Decision.Decision != "continue_with_fallback" || artifact.DiffPath == "" {
		t.Fatalf("partial-diff recovery artifact = %#v", artifact)
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

func TestParseRecoveryDecisionAcceptsFencedContract(t *testing.T) {
	decision, err := parseRecoveryDecision([]byte("```json\n{\"schema_version\":1,\"decision\":\"continue_with_fallback\",\"reason\":\"focused tests passed after the worker contract failure\",\"evidence\":[\"diff captured\",\"tests passed\"]}\n```"), map[string]bool{
		"quarantine":             true,
		"continue_with_fallback": true,
	})
	if err != nil {
		t.Fatalf("parseRecoveryDecision() error = %v", err)
	}
	if decision.Decision != "continue_with_fallback" || len(decision.Evidence) != 2 {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestRecoveryPromptSuppliesCompleteDecisionEnvelope(t *testing.T) {
	prompt := recoveryPrompt(
		fmt.Errorf("worker output contract failed"),
		DiffArtifact{PatchPath: "/tmp/diff.patch", Metadata: DiffFilesMetadata{DiffSHA256: "abc123"}},
		nil,
		map[string]bool{"quarantine": true, "continue_with_fallback": true},
	)
	for _, required := range []string{
		`"schema_version": 1`,
		`"decision": "one of the allowed decisions"`,
		`"reason": "a concise, evidence-backed explanation"`,
		`"evidence": ["one or more concrete facts from the failure, diff, or focused tests"]`,
		"Do not use Markdown fences or prose.",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("recovery prompt missing %q:\n%s", required, prompt)
		}
	}
}

func TestRecoverEditorFailurePersistsInvalidSupervisorDecisionReason(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "before\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")
	baseline := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	before, err := captureWorktreeSnapshot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(repo, "README.md"), "partial\n")

	reviewer := fakeDirectAdapter{
		build: func(role Role, req Request) (*CommandSpec, error) {
			return &CommandSpec{Argv: []string{"reviewer"}, Dir: repo, Output: req.OutputPath}, nil
		},
		direct: func(role Role, req Request) (Result, error) {
			if role != RoleSupervisor {
				t.Fatalf("reviewer role = %q", role)
			}
			return Result{Text: `{"decision":"continue_with_fallback","reason":"missing required fields"}`}, nil
		},
	}
	opts := RunOptions{
		Workdir: repo,
		Mode:    ModeSupervisor,
		LossPolicy: RoleLossPolicies{
			Worker: LossPolicyReplaceThenBlock,
		},
		Fallbacks: RoleFallbacks{Worker: []string{"fallback:model"}},
	}
	runDir := t.TempDir()
	final := FinalRun{RunID: "invalid-recovery-decision", RunDir: runDir, Workdir: repo, Mode: ModeSupervisor}
	_, _, _, err = NewApp(DefaultConfig()).recoverEditorFailure(
		context.Background(), opts, 1, runDir, baseline, "", "", Request{Workdir: repo, RunDir: runDir},
		RoleTarget{Adapter: "primary", Model: "model"}, fakeAdapter{}, reviewer,
		map[string]Adapter{}, &OutputContractError{Err: fmt.Errorf("invalid worker output")}, before, &final,
	)
	if err == nil {
		t.Fatal("expected invalid recovery decision to quarantine partial work")
	}
	var artifact RecoveryArtifact
	readJSONFile(t, filepath.Join(runDir, "recovery-round-1.json"), &artifact)
	if !strings.Contains(artifact.Decision.Reason, "recovery supervisor returned invalid decision") {
		t.Fatalf("recovery reason = %q", artifact.Decision.Reason)
	}
}
