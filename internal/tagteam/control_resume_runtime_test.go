package tagteam

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestControlRuntimeCapabilitiesIncludeResumeOnlyWhenEnabled(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	service := ControlService{RepositoryRoot: repo, StateRoot: t.TempDir(), ProducerVersion: "test"}
	base := service.Capabilities()
	if controlContainsString(base.Capabilities, "resume") {
		t.Fatalf("base capabilities unexpectedly include resume: %#v", base.Capabilities)
	}
	runtime := NewControlRuntime(service, DefaultConfig(), nil)
	if !controlContainsString(runtime.Capabilities().Capabilities, "resume") {
		t.Fatalf("runtime capabilities do not include resume: %#v", runtime.Capabilities())
	}
}

func TestControlRuntimeResumeRejectsTypedApprovalFailures(t *testing.T) {
	repo, baseline := createResumeFixtureRepo(t)
	stateRoot := t.TempDir()
	runID := "control-resume-approval"
	runDir, err := createRunDir(repo, stateRoot, runID)
	if err != nil {
		t.Fatal(err)
	}
	writeResumeFixture(t, runDir, runID, repo, baseline, RunStatusRunning)
	repository, err := resolveControlRepository(repo)
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewControlRuntime(ControlService{RepositoryRoot: repo, StateRoot: stateRoot, ProducerVersion: "test"}, DefaultConfig(), nil)

	tests := []struct {
		name string
		edit func(*ControlResumeRequest)
		code string
	}{
		{name: "missing", edit: func(request *ControlResumeRequest) {}, code: "approval_missing"},
		{name: "action mismatch", edit: func(request *ControlResumeRequest) {
			request.Approval = validResumeApproval(t, *request, "wrong-nonce")
			request.Approval.ActionDigest = "wrong"
		}, code: "approval_action_mismatch"},
		{name: "expired", edit: func(request *ControlResumeRequest) {
			request.Approval = validResumeApproval(t, *request, "expired-nonce")
			request.Approval.ApprovedAt = time.Now().UTC().Add(-10 * time.Minute)
			request.Approval.ExpiresAt = time.Now().UTC().Add(-time.Minute)
		}, code: "approval_expired"},
		{name: "too long", edit: func(request *ControlResumeRequest) {
			request.Approval = validResumeApproval(t, *request, "long-nonce")
			request.Approval.ExpiresAt = request.Approval.ApprovedAt.Add(ControlApprovalMaxLifetime + time.Second)
		}, code: "approval_lifetime_exceeded"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := ControlResumeRequest{SchemaVersion: ControlContractVersion, Repository: repository, RunID: runID}
			test.edit(&request)
			_, err := runtime.Resume(context.Background(), request)
			assertControlResumeError(t, err, test.code)
		})
	}
}

func TestControlRuntimeResumeRejectsUnresumableRunWithBoundedReason(t *testing.T) {
	repo, baseline := createResumeFixtureRepo(t)
	stateRoot := t.TempDir()
	runID := "control-resume-terminal"
	runDir, err := createRunDir(repo, stateRoot, runID)
	if err != nil {
		t.Fatal(err)
	}
	writeResumeFixture(t, runDir, runID, repo, baseline, RunStatusPassed)
	repository, err := resolveControlRepository(repo)
	if err != nil {
		t.Fatal(err)
	}
	request := ControlResumeRequest{SchemaVersion: ControlContractVersion, Repository: repository, RunID: runID}
	request.Approval = validResumeApproval(t, request, "terminal-nonce")
	runtime := NewControlRuntime(ControlService{RepositoryRoot: repo, StateRoot: stateRoot, ProducerVersion: "test"}, DefaultConfig(), nil)
	if _, err := runtime.Resume(context.Background(), request); err == nil {
		t.Fatal("terminal run was resumed")
	} else {
		assertControlResumeError(t, err, "already_terminal")
	}
	locator, err := resolveStateLocator(repo, stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := readControlApprovalLedger(filepath.Join(locator.RepoRoot, controlApprovalLedgerName))
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger.Resumes) != 0 {
		t.Fatalf("unresumable request consumed approval: %#v", ledger.Resumes)
	}
}

func TestControlRuntimeResumeIsIdempotentAndPersistsNonceAcrossRuntimeRestart(t *testing.T) {
	repo, baseline := createResumeFixtureRepo(t)
	stateRoot := t.TempDir()
	runID := "control-resume-idempotent"
	runDir, err := createRunDir(repo, stateRoot, runID)
	if err != nil {
		t.Fatal(err)
	}
	writeResumeFixture(t, runDir, runID, repo, baseline, RunStatusRunning)
	repository, err := resolveControlRepository(repo)
	if err != nil {
		t.Fatal(err)
	}
	request := ControlResumeRequest{SchemaVersion: ControlContractVersion, Repository: repository, RunID: runID}
	request.Approval = validResumeApproval(t, request, "resume-once")
	service := ControlService{RepositoryRoot: repo, StateRoot: stateRoot, ProducerVersion: "test"}
	ctx, cancel := context.WithCancel(context.Background())
	firstRuntime := NewControlRuntime(service, DefaultConfig(), nil)
	first, err := firstRuntime.Resume(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	waitForControlResumeJob(t, firstRuntime, runID)

	secondRuntime := NewControlRuntime(service, DefaultConfig(), nil)
	second, err := secondRuntime.Resume(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("reissued resume handle = %#v, first = %#v", second, first)
	}
	locator, err := resolveStateLocator(repo, stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := readControlApprovalLedger(filepath.Join(locator.RepoRoot, controlApprovalLedgerName))
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger.Resumes) != 1 || ledger.Resumes[0].Nonce != request.Approval.Nonce {
		t.Fatalf("resume ledger = %#v", ledger)
	}

	replay := request
	replay.RunID = "control-resume-other"
	replay.Approval = validResumeApproval(t, replay, request.Approval.Nonce)
	if _, err := secondRuntime.Resume(context.Background(), replay); err == nil {
		t.Fatal("resume nonce replay was accepted for another run")
	} else {
		assertControlResumeError(t, err, "approval_nonce_replayed")
	}
}

func TestControlRuntimeResumeRejectsEscapingArtifactsAndRunDirReplacement(t *testing.T) {
	repo, baseline := createResumeFixtureRepo(t)
	stateRoot := t.TempDir()
	repository, err := resolveControlRepository(repo)
	if err != nil {
		t.Fatal(err)
	}
	service := ControlService{RepositoryRoot: repo, StateRoot: stateRoot, ProducerVersion: "test"}
	runtime := NewControlRuntime(service, DefaultConfig(), nil)

	t.Run("escaping state.json", func(t *testing.T) {
		runID := "resume-escape-state"
		runDir, err := createRunDir(repo, stateRoot, runID)
		if err != nil {
			t.Fatal(err)
		}
		writeResumeFixture(t, runDir, runID, repo, baseline, RunStatusRunning)
		outside := t.TempDir()
		sentinel := filepath.Join(outside, "secret-state.json")
		if err := os.WriteFile(sentinel, []byte(`{"status":"external_sentinel"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(runDir, "state.json")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(sentinel, filepath.Join(runDir, "state.json")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		request := ControlResumeRequest{SchemaVersion: ControlContractVersion, Repository: repository, RunID: runID}
		request.Approval = validResumeApproval(t, request, "resume-escape-state")
		if _, err := runtime.Resume(context.Background(), request); err == nil {
			t.Fatal("Resume accepted escaping state.json")
		}
		after, err := os.ReadFile(sentinel)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(after), "external_sentinel") {
			t.Fatalf("external state sentinel modified: %s", after)
		}
	})

	t.Run("escaping events.jsonl symlink is not consumed", func(t *testing.T) {
		runID := "resume-escape-events"
		runDir, err := createRunDir(repo, stateRoot, runID)
		if err != nil {
			t.Fatal(err)
		}
		writeResumeFixture(t, runDir, runID, repo, baseline, RunStatusRunning)
		outside := t.TempDir()
		sentinel := filepath.Join(outside, "events.jsonl")
		if err := os.WriteFile(sentinel, []byte(`{"run_id":"leaked","to_phase":"implementing","status":"running","round":1}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		eventsPath := filepath.Join(runDir, "events.jsonl")
		if err := os.Remove(eventsPath); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if err := os.Symlink(sentinel, eventsPath); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		opts, err := runtime.resumeOptions(repository.CanonicalRoot)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := NewApp(runtime.config).ResumeControl(context.Background(), opts, runID); err == nil {
			t.Fatal("ResumeControl accepted escaping events.jsonl")
		}
		after, err := os.ReadFile(sentinel)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(after), "leaked") {
			t.Fatalf("external events sentinel modified: %s", after)
		}
		// Assessment must also refuse without consuming external journal content.
		assessment, err := service.PrepareResume(context.Background(), ControlResumeRequest{
			SchemaVersion: ControlContractVersion,
			Repository:    repository,
			RunID:         runID,
		})
		if err != nil {
			t.Fatal(err)
		}
		if assessment.Resumable {
			t.Fatalf("PrepareResume accepted escaping events.jsonl: %#v", assessment)
		}
	})

	t.Run("escaping input.md symlink is not consumed", func(t *testing.T) {
		runID := "resume-escape-input"
		runDir, err := createRunDir(repo, stateRoot, runID)
		if err != nil {
			t.Fatal(err)
		}
		writeResumeFixture(t, runDir, runID, repo, baseline, RunStatusRunning)
		// Valid journal so resume proceeds past verifyResumeArtifacts to prompt load.
		if err := os.WriteFile(filepath.Join(runDir, "events.jsonl"), []byte(`{"run_id":"resume-escape-input","to_phase":"implementing","status":"running","round":1}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		// Align state with the journal event so verification gets to prompt read.
		state, err := readRunState(runDir)
		if err != nil {
			t.Fatal(err)
		}
		state.Phase = string(PhaseImplementing)
		state.Status = string(RunStatusRunning)
		state.CurrentRound = 1
		if err := writeRunState(runDir, state); err != nil {
			t.Fatal(err)
		}
		outside := t.TempDir()
		sentinel := filepath.Join(outside, "input.md")
		if err := os.WriteFile(sentinel, []byte("EXTERNAL_PROMPT_LEAK"), 0o644); err != nil {
			t.Fatal(err)
		}
		inputPath := filepath.Join(runDir, "input.md")
		if err := os.Remove(inputPath); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if err := os.Symlink(sentinel, inputPath); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		opts, err := runtime.resumeOptions(repository.CanonicalRoot)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := NewApp(runtime.config).ResumeControl(context.Background(), opts, runID); err == nil {
			t.Fatal("ResumeControl accepted escaping input.md")
		}
		after, err := os.ReadFile(sentinel)
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != "EXTERNAL_PROMPT_LEAK" {
			t.Fatalf("external input.md sentinel modified: %s", after)
		}
	})

	t.Run("escaping relay auxiliary artifacts are not consumed", func(t *testing.T) {
		for _, artifact := range []string{
			"supervisor-brief.md",
			"scout-round-1.json",
			"supervisor-work-plan.json",
			"plan.json",
		} {
			artifact := artifact
			t.Run(artifact, func(t *testing.T) {
				runID := "resume-escape-" + strings.ReplaceAll(strings.ReplaceAll(artifact, ".", "-"), "/", "-")
				runDir, err := createRunDir(repo, stateRoot, runID)
				if err != nil {
					t.Fatal(err)
				}
				writeResumeFixture(t, runDir, runID, repo, baseline, RunStatusRunning)
				// Align journal/state so resume can reach relay/plan loading.
				if err := os.WriteFile(filepath.Join(runDir, "events.jsonl"), []byte(fmt.Sprintf(`{"run_id":%q,"to_phase":"implementing","status":"running","round":1}`+"\n", runID)), 0o644); err != nil {
					t.Fatal(err)
				}
				state, err := readRunState(runDir)
				if err != nil {
					t.Fatal(err)
				}
				state.Phase = string(PhaseImplementing)
				state.Status = string(RunStatusRunning)
				state.CurrentRound = 1
				if err := writeRunState(runDir, state); err != nil {
					t.Fatal(err)
				}
				// Mark as relay so prepareResumeRuntime loads scout context.
				final, err := readFinal(filepath.Join(runDir, "final.json"))
				if err == nil {
					final.Mode = ModeRelay
					_ = writeJSONWithNewline(filepath.Join(runDir, "final.json"), final)
				}
				outside := t.TempDir()
				sentinelName := "external-" + filepath.Base(artifact)
				sentinel := filepath.Join(outside, sentinelName)
				payload := "EXTERNAL_RELAY_LEAK"
				if strings.HasSuffix(artifact, ".json") {
					payload = `{"summary":"EXTERNAL_RELAY_LEAK","schema_version":1}`
				}
				if err := os.WriteFile(sentinel, []byte(payload), 0o644); err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(runDir, artifact)
				if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				if err := os.Symlink(sentinel, target); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				opts, err := runtime.resumeOptions(repository.CanonicalRoot)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := NewApp(runtime.config).ResumeControl(context.Background(), opts, runID); err == nil {
					t.Fatalf("ResumeControl accepted escaping %s", artifact)
				}
				after, err := os.ReadFile(sentinel)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(after), "EXTERNAL_RELAY_LEAK") {
					t.Fatalf("external %s sentinel modified: %s", artifact, after)
				}
				// Prove load helpers themselves refuse without consuming.
				if artifact == "plan.json" {
					if _, err := readControlExecutionPlanOptional(runDir); err == nil {
						t.Fatal("readControlExecutionPlanOptional accepted escaping plan.json")
					}
				} else {
					if _, err := loadResumeRelayContextControl(runDir); err == nil {
						t.Fatalf("loadResumeRelayContextControl accepted escaping %s", artifact)
					}
				}
			})
		}
	})

	t.Run("run dir replaced with external symlink after assessment", func(t *testing.T) {
		runID := "resume-escape-rundir"
		runDir, err := createRunDir(repo, stateRoot, runID)
		if err != nil {
			t.Fatal(err)
		}
		writeResumeFixture(t, runDir, runID, repo, baseline, RunStatusRunning)
		// Prove assessment succeeds on the real directory first.
		assessment, err := service.PrepareResume(context.Background(), ControlResumeRequest{
			SchemaVersion: ControlContractVersion,
			Repository:    repository,
			RunID:         runID,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !assessment.Resumable {
			t.Fatalf("precondition assessment failed: %#v", assessment)
		}
		outside := t.TempDir()
		externalRun := filepath.Join(outside, "external-run")
		if err := os.MkdirAll(externalRun, 0o700); err != nil {
			t.Fatal(err)
		}
		// Move real run aside and plant an escaping directory symlink.
		backup := runDir + ".bak"
		if err := os.Rename(runDir, backup); err != nil {
			t.Fatal(err)
		}
		// Copy fixture into external target so a naive resume would succeed.
		writeResumeFixture(t, externalRun, runID, repo, baseline, RunStatusRunning)
		if err := os.WriteFile(filepath.Join(externalRun, "sentinel.txt"), []byte("external_run_dir"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(externalRun, runDir); err != nil {
			_ = os.Rename(backup, runDir)
			t.Skipf("symlinks unavailable: %v", err)
		}
		request := ControlResumeRequest{SchemaVersion: ControlContractVersion, Repository: repository, RunID: runID}
		request.Approval = validResumeApproval(t, request, "resume-escape-rundir")
		// ResumeControl path must refuse the escaping run directory.
		opts, err := runtime.resumeOptions(repository.CanonicalRoot)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := NewApp(runtime.config).ResumeControl(context.Background(), opts, runID); err == nil {
			t.Fatal("ResumeControl accepted escaping run directory symlink")
		}
		// Full MCP Resume should also fail closed during PrepareResume.
		if _, err := runtime.Resume(context.Background(), request); err == nil {
			t.Fatal("Resume accepted escaping run directory after assessment window")
		}
		sentinel, err := os.ReadFile(filepath.Join(externalRun, "sentinel.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if string(sentinel) != "external_run_dir" {
			t.Fatalf("external run dir was modified: %s", sentinel)
		}
		// External resume.json must not appear from a successful write.
		if _, err := os.Stat(filepath.Join(externalRun, "resume.json")); !os.IsNotExist(err) {
			t.Fatalf("external resume.json written: %v", err)
		}
	})
}

func validResumeApproval(t *testing.T, request ControlResumeRequest, nonce string) ControlApproval {
	t.Helper()
	digest, err := ControlResumeActionDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	return ControlApproval{ActionDigest: digest, ApprovedAt: now.Add(-time.Second), ExpiresAt: now.Add(5 * time.Minute), Nonce: nonce}
}

func assertControlResumeError(t *testing.T, err error, wantCode string) {
	t.Helper()
	var resumeErr *ControlResumeError
	if err == nil || !errors.As(err, &resumeErr) {
		t.Fatalf("error = %v, want ControlResumeError/%s", err, wantCode)
	}
	if resumeErr.ReasonCode != wantCode || resumeErr.Reason == "" {
		t.Fatalf("resume error = %#v, want code %q and bounded reason", resumeErr, wantCode)
	}
}

func waitForControlResumeJob(t *testing.T, runtime *ControlRuntime, runID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runtime.mu.Lock()
		_, active := runtime.jobs[runID]
		runtime.mu.Unlock()
		if !active {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("resume job %q did not finish", runID)
}

func controlContainsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
