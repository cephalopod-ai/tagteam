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
					if _, err := readControlExecutionPlanOptional(context.Background(), runDir); err == nil {
						t.Fatal("readControlExecutionPlanOptional accepted escaping plan.json")
					}
				} else {
					if _, err := loadResumeRelayContextControl(context.Background(), runDir); err == nil {
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

	t.Run("run dir replaced after lock revalidation", func(t *testing.T) {
		const externalPromptMarker = "EXTERNAL_RESUME_PROMPT_MARKER"
		runID := "resume-post-lock-replace"
		runDir, err := createRunDir(repo, stateRoot, runID)
		if err != nil {
			t.Fatal(err)
		}
		writeResumeFixture(t, runDir, runID, repo, baseline, RunStatusRunning)
		// Ensure a control-safe prompt artifact exists inside the real run dir.
		if err := os.WriteFile(filepath.Join(runDir, "input.md"), []byte("legitimate in-root prompt\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		// Wire a recording fake adapter so any dispatch is observable.
		var seenPrompts []string
		testRegistryOverrides = map[string]Adapter{"fake": recordingResumeAdapter{seen: &seenPrompts}}
		t.Cleanup(func() { testRegistryOverrides = nil })

		outside := t.TempDir()
		externalRun := filepath.Join(outside, "external-run")
		if err := os.MkdirAll(externalRun, 0o700); err != nil {
			t.Fatal(err)
		}
		writeResumeFixture(t, externalRun, runID, repo, baseline, RunStatusRunning)
		if err := os.WriteFile(filepath.Join(externalRun, "input.md"), []byte(externalPromptMarker+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		// Plant a meta prompt with the same marker for double coverage.
		meta := Meta{SchemaVersion: ArtifactSchemaVersion, RunID: runID, Workdir: repo, Baseline: baseline, Command: "run", Prompt: externalPromptMarker, StartedAt: time.Now().UTC(), Adapters: map[string]string{"worker": "fake", "supervisor": "fake"}, Models: map[string]string{}}
		if err := writeJSONWithNewline(filepath.Join(externalRun, "meta.json"), meta); err != nil {
			t.Fatal(err)
		}
		sentinelPath := filepath.Join(externalRun, "sentinel.txt")
		if err := os.WriteFile(sentinelPath, []byte("external_run_dir_untouched"), 0o644); err != nil {
			t.Fatal(err)
		}
		externalOutput := filepath.Join(externalRun, "worker-round-1.md")

		controlResumePostLockHook = func() {
			backup := runDir + ".bak"
			if err := os.Rename(runDir, backup); err != nil {
				t.Errorf("post-lock rename: %v", err)
				return
			}
			if err := os.Symlink(externalRun, runDir); err != nil {
				_ = os.Rename(backup, runDir)
				t.Errorf("post-lock symlink: %v", err)
			}
		}
		t.Cleanup(func() {
			controlResumePostLockHook = nil
			// Best-effort restore for subsequent subtests sharing stateRoot.
			if info, err := os.Lstat(runDir); err == nil && info.Mode()&os.ModeSymlink != 0 {
				_ = os.Remove(runDir)
			}
			if _, err := os.Stat(runDir + ".bak"); err == nil {
				_ = os.Rename(runDir+".bak", runDir)
			}
		})

		opts, err := runtime.resumeOptions(repository.CanonicalRoot)
		if err != nil {
			t.Fatal(err)
		}
		_, resumeErr := NewApp(runtime.config).ResumeControl(context.Background(), opts, runID)
		if resumeErr == nil {
			// Symlink creation may have failed on this platform.
			if _, err := os.Lstat(runDir); err == nil {
				if info, _ := os.Lstat(runDir); info != nil && info.Mode()&os.ModeSymlink == 0 {
					t.Skip("symlinks unavailable: post-lock replacement did not install")
				}
			}
			t.Fatal("ResumeControl accepted run directory replaced after lock revalidation")
		}
		if len(seenPrompts) != 0 {
			t.Fatalf("fake adapter observed prompts after run-dir replacement: %#v", seenPrompts)
		}
		for _, prompt := range seenPrompts {
			if strings.Contains(prompt, externalPromptMarker) {
				t.Fatalf("fake adapter observed external prompt marker: %q", prompt)
			}
		}
		sentinel, err := os.ReadFile(sentinelPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(sentinel) != "external_run_dir_untouched" {
			t.Fatalf("external sentinel modified: %s", sentinel)
		}
		if _, err := os.Stat(filepath.Join(externalRun, "resume.json")); !os.IsNotExist(err) {
			t.Fatalf("external resume.json written: %v", err)
		}
		if _, err := os.Stat(filepath.Join(externalRun, "state.json")); err != nil {
			t.Fatalf("external state.json missing: %v", err)
		}
		// state.json existed from fixture; content must still be fixture-like, not resume-failed rewrite.
		stateData, err := os.ReadFile(filepath.Join(externalRun, "state.json"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(stateData), "resume_failed") {
			t.Fatalf("external state.json rewritten through replacement: %s", stateData)
		}
		if _, err := os.Stat(externalOutput); !os.IsNotExist(err) {
			t.Fatalf("external adapter output target written: %v", err)
		}
	})

	t.Run("run dir replaced after state.json read", func(t *testing.T) {
		const externalMetaMarker = "EXTERNAL_META_PROMPT_AFTER_STATE"
		const externalDiffMarker = "EXTERNAL_DIFF_AFTER_STATE"
		runID := "resume-after-state-replace"
		runDir, err := createRunDir(repo, stateRoot, runID)
		if err != nil {
			t.Fatal(err)
		}
		writeResumeFixture(t, runDir, runID, repo, baseline, RunStatusRunning)
		// Point state at in-root artifacts so verify would succeed if trust root flips.
		diffPath := filepath.Join(runDir, "diff-round-1.patch")
		if err := os.WriteFile(diffPath, []byte("diff --git a/ok b/ok\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		state, err := readRunState(runDir)
		if err != nil {
			t.Fatal(err)
		}
		state.LatestDiffPath = diffPath
		state.DiffHash = sha256Sum([]byte("diff --git a/ok b/ok\n"))
		if err := writeRunState(runDir, state); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(runDir, "events.jsonl"), []byte(fmt.Sprintf(
			`{"run_id":%q,"to_phase":%q,"status":%q,"round":%d,"diff_hash":%q}`+"\n",
			state.RunID, normalizeRunPhase(state.Phase), state.Status, state.CurrentRound, state.DiffHash,
		)), 0o644); err != nil {
			t.Fatal(err)
		}

		outside := t.TempDir()
		externalRun := filepath.Join(outside, "external-run")
		if err := os.MkdirAll(externalRun, 0o700); err != nil {
			t.Fatal(err)
		}
		writeResumeFixture(t, externalRun, runID, repo, baseline, RunStatusRunning)
		if err := os.WriteFile(filepath.Join(externalRun, "meta.json"), []byte(`{"schema_version":1,"prompt":"`+externalMetaMarker+`"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		externalDiff := filepath.Join(externalRun, "diff-round-1.patch")
		if err := os.WriteFile(externalDiff, []byte(externalDiffMarker+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		// Mirror state paths so a naive verify against the replaced root would read external.
		extState, err := readRunState(externalRun)
		if err != nil {
			t.Fatal(err)
		}
		extState.LatestDiffPath = externalDiff
		extState.DiffHash = state.DiffHash
		if err := writeRunState(externalRun, extState); err != nil {
			t.Fatal(err)
		}
		sentinelPath := filepath.Join(externalRun, "sentinel.txt")
		if err := os.WriteFile(sentinelPath, []byte("after_state_untouched"), 0o644); err != nil {
			t.Fatal(err)
		}

		controlResumeAfterStateReadHook = func() {
			backup := runDir + ".after-state.bak"
			if err := os.Rename(runDir, backup); err != nil {
				t.Errorf("after-state rename: %v", err)
				return
			}
			if err := os.Symlink(externalRun, runDir); err != nil {
				_ = os.Rename(backup, runDir)
				t.Errorf("after-state symlink: %v", err)
			}
		}
		t.Cleanup(func() {
			controlResumeAfterStateReadHook = nil
			if info, err := os.Lstat(runDir); err == nil && info.Mode()&os.ModeSymlink != 0 {
				_ = os.Remove(runDir)
			}
			if _, err := os.Stat(runDir + ".after-state.bak"); err == nil {
				_ = os.Rename(runDir+".after-state.bak", runDir)
			}
		})

		opts, err := runtime.resumeOptions(repository.CanonicalRoot)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := NewApp(runtime.config).ResumeControl(context.Background(), opts, runID); err == nil {
			if info, _ := os.Lstat(runDir); info != nil && info.Mode()&os.ModeSymlink == 0 {
				t.Skip("symlinks unavailable: after-state replacement did not install")
			}
			t.Fatal("ResumeControl accepted run directory replaced after state.json read")
		}
		metaData, err := os.ReadFile(filepath.Join(externalRun, "meta.json"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(metaData), externalMetaMarker) {
			t.Fatalf("external meta sentinel modified: %s", metaData)
		}
		diffData, err := os.ReadFile(externalDiff)
		if err != nil {
			t.Fatal(err)
		}
		if string(diffData) != externalDiffMarker+"\n" {
			t.Fatalf("external diff sentinel modified: %s", diffData)
		}
		sentinel, err := os.ReadFile(sentinelPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(sentinel) != "after_state_untouched" {
			t.Fatalf("external sentinel modified: %s", sentinel)
		}
		if _, err := os.Stat(filepath.Join(externalRun, "resume.json")); !os.IsNotExist(err) {
			t.Fatalf("external resume.json written: %v", err)
		}
	})

	t.Run("run dir replaced before adapter dispatch", func(t *testing.T) {
		const externalPromptMarker = "EXTERNAL_ADAPTER_PROMPT_MARKER"
		runID := "resume-before-adapter-replace"
		runDir, err := createRunDir(repo, stateRoot, runID)
		if err != nil {
			t.Fatal(err)
		}
		writeResumeFixture(t, runDir, runID, repo, baseline, RunStatusRunning)
		// Align journal/state and mark planning so resume constructs adapter requests.
		if err := os.WriteFile(filepath.Join(runDir, "events.jsonl"), []byte(fmt.Sprintf(
			`{"run_id":%q,"to_phase":"planning","status":"running","round":1}`+"\n", runID,
		)), 0o644); err != nil {
			t.Fatal(err)
		}
		state, err := readRunState(runDir)
		if err != nil {
			t.Fatal(err)
		}
		state.Phase = string(PhasePlanning)
		state.Status = string(RunStatusRunning)
		state.CurrentRound = 1
		if err := writeRunState(runDir, state); err != nil {
			t.Fatal(err)
		}
		final, err := readFinal(filepath.Join(runDir, "final.json"))
		if err == nil {
			final.Mode = ModeRelay
			final.BaselineTest = &TestRun{Command: "true", Passed: true}
			_ = writeJSONWithNewline(filepath.Join(runDir, "final.json"), final)
		}
		// Meta adapters force fake so Registry override is selected.
		meta, err := readMeta(filepath.Join(runDir, "meta.json"))
		if err == nil {
			meta.Adapters = map[string]string{"scout": "fake", "worker": "fake", "supervisor": "fake"}
			_ = writeJSONWithNewline(filepath.Join(runDir, "meta.json"), meta)
		}
		if err := os.WriteFile(filepath.Join(runDir, "input.md"), []byte("legitimate planning prompt\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		var seenPrompts []string
		testRegistryOverrides = map[string]Adapter{"fake": recordingResumeAdapter{seen: &seenPrompts}}
		t.Cleanup(func() { testRegistryOverrides = nil })

		outside := t.TempDir()
		externalRun := filepath.Join(outside, "external-run")
		if err := os.MkdirAll(externalRun, 0o700); err != nil {
			t.Fatal(err)
		}
		writeResumeFixture(t, externalRun, runID, repo, baseline, RunStatusRunning)
		if err := os.WriteFile(filepath.Join(externalRun, "input.md"), []byte(externalPromptMarker+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		sentinelPath := filepath.Join(externalRun, "sentinel.txt")
		if err := os.WriteFile(sentinelPath, []byte("before_adapter_untouched"), 0o644); err != nil {
			t.Fatal(err)
		}
		externalDeliveryMarker := filepath.Join(externalRun, "deliveries")
		externalProgress := filepath.Join(externalRun, "live-progress.json")
		externalScoutOut := filepath.Join(externalRun, "scout-round-1.json")

		controlResumeBeforeAdapterHook = func() {
			backup := runDir + ".before-adapter.bak"
			if err := os.Rename(runDir, backup); err != nil {
				t.Errorf("before-adapter rename: %v", err)
				return
			}
			if err := os.Symlink(externalRun, runDir); err != nil {
				_ = os.Rename(backup, runDir)
				t.Errorf("before-adapter symlink: %v", err)
			}
		}
		t.Cleanup(func() {
			controlResumeBeforeAdapterHook = nil
			if info, err := os.Lstat(runDir); err == nil && info.Mode()&os.ModeSymlink != 0 {
				_ = os.Remove(runDir)
			}
			if _, err := os.Stat(runDir + ".before-adapter.bak"); err == nil {
				_ = os.Rename(runDir+".before-adapter.bak", runDir)
			}
		})

		opts, err := runtime.resumeOptions(repository.CanonicalRoot)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := NewApp(runtime.config).ResumeControl(context.Background(), opts, runID); err == nil {
			if info, _ := os.Lstat(runDir); info != nil && info.Mode()&os.ModeSymlink == 0 {
				t.Skip("symlinks unavailable: before-adapter replacement did not install")
			}
			t.Fatal("ResumeControl accepted run directory replaced before adapter dispatch")
		}
		if len(seenPrompts) != 0 {
			t.Fatalf("fake adapter observed prompts after before-adapter replacement: %#v", seenPrompts)
		}
		for _, prompt := range seenPrompts {
			if strings.Contains(prompt, externalPromptMarker) {
				t.Fatalf("fake adapter observed external prompt marker: %q", prompt)
			}
		}
		sentinel, err := os.ReadFile(sentinelPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(sentinel) != "before_adapter_untouched" {
			t.Fatalf("external sentinel modified: %s", sentinel)
		}
		if info, err := os.Stat(externalDeliveryMarker); err == nil && info.IsDir() {
			entries, _ := os.ReadDir(externalDeliveryMarker)
			if len(entries) > 0 {
				t.Fatalf("external deliveries written: %v", entries)
			}
		}
		if _, err := os.Stat(externalProgress); !os.IsNotExist(err) {
			t.Fatalf("external live progress written: %v", err)
		}
		if _, err := os.Stat(externalScoutOut); !os.IsNotExist(err) {
			t.Fatalf("external scout output written: %v", err)
		}
	})
}

// recordingResumeAdapter is a DirectAdapter used by control-resume path gates
// to prove adapter dispatch never observes external prompt content.
type recordingResumeAdapter struct {
	seen *[]string
}

func (r recordingResumeAdapter) ID() string { return "fake" }
func (r recordingResumeAdapter) Detect(ctx context.Context) (VersionInfo, error) {
	return VersionInfo{Found: true, Runnable: true}, nil
}
func (r recordingResumeAdapter) Capabilities() CapabilitySet { return CapabilitySet{} }
func (r recordingResumeAdapter) BuildCmd(role Role, req Request) (*CommandSpec, error) {
	return &CommandSpec{Argv: []string{"fake"}, Dir: req.Workdir, Output: req.OutputPath}, nil
}
func (r recordingResumeAdapter) ParseResult(role Role, raw []byte) (Result, error) {
	return Result{Text: string(raw), Raw: raw}, nil
}
func (r recordingResumeAdapter) RunDirect(role Role, req Request) (Result, error) {
	if r.seen != nil {
		*r.seen = append(*r.seen, req.Prompt)
	}
	return Result{Text: "should-not-run", Raw: []byte("should-not-run")}, nil
}
