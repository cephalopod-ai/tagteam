package tagteam

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPersistRunStateJournalsPhaseTransitions(t *testing.T) {
	runDir := t.TempDir()
	states := []RunState{
		{RunID: "run-1", Status: "running", Phase: string(PhasePlanning)},
		{RunID: "run-1", Status: "running", Phase: string(PhaseImplementing)},
		{RunID: "run-1", Status: "running", Phase: string(PhaseTesting)},
		{RunID: "run-1", Status: "running", Phase: string(PhaseReviewing)},
	}
	for _, state := range states {
		if err := persistRunState(runDir, state); err != nil {
			t.Fatal(err)
		}
	}
	state, err := readRunState(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if state.SchemaVersion != runStateSchemaVersion || state.Phase != string(PhaseReviewing) || state.CompletedPhase != PhaseTesting {
		t.Fatalf("state = %#v", state)
	}
	events, err := os.ReadFile(filepath.Join(runDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(strings.TrimSpace(string(events)), "\n") + 1; got != len(states) {
		t.Fatalf("event count = %d, want %d\n%s", got, len(states), events)
	}
}

func TestResumeReturnsAlreadyPassedRunAfterVerification(t *testing.T) {
	repo, baseline := createResumeFixtureRepo(t)
	runID := "resume-passed"
	runDir, err := createRunDir(repo, "", runID)
	if err != nil {
		t.Fatal(err)
	}
	writeResumeFixture(t, runDir, runID, repo, baseline, RunStatusPassed)
	final, err := NewApp(DefaultConfig()).Resume(context.Background(), RunOptions{Workdir: repo}, runID)
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if final.RunID != runID || final.Status != RunStatusPassed {
		t.Fatalf("final = %#v", final)
	}
}

func TestResumeQuarantinesBaselineMismatch(t *testing.T) {
	repo, baseline := createResumeFixtureRepo(t)
	runID := "resume-mismatch"
	runDir, err := createRunDir(repo, "", runID)
	if err != nil {
		t.Fatal(err)
	}
	writeResumeFixture(t, runDir, runID, repo, baseline, RunStatusRunning)
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("new head\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "advance")
	final, err := NewApp(DefaultConfig()).Resume(context.Background(), RunOptions{Workdir: repo}, runID)
	if err == nil || final.Status != RunStatusQuarantined {
		t.Fatalf("final=%#v err=%v", final, err)
	}
	state, readErr := readRunState(runDir)
	if readErr != nil || state.Status != string(RunStatusQuarantined) {
		t.Fatalf("state=%#v err=%v", state, readErr)
	}
}

func TestApplySavedResumeTargetsHonorsExplicitCompatibleOverrides(t *testing.T) {
	opts := RunOptions{
		Mode:                  ModeRelay,
		Coder:                 RoleTarget{Adapter: "grok", Model: "grok-4.5"},
		Adversary:             RoleTarget{Adapter: "codex", Model: "gpt-5.6-sol"},
		AdversaryExplicit:     true,
		AdversaryExplicitMode: ModeRelay,
	}
	saved := FinalRun{
		Mode:      ModeRelay,
		Coder:     RoleTarget{Adapter: "grok", Model: "grok-4.5"},
		Adversary: RoleTarget{Adapter: "claude", Model: "claude-sonnet-5"},
		Scout:     RoleTarget{Adapter: "agy", Model: "gemini-3.6-flash-medium"},
	}
	if err := applySavedResumeTargets(&opts, saved); err != nil {
		t.Fatal(err)
	}
	if opts.Adversary.Adapter != "codex" || opts.Adversary.Model != "gpt-5.6-sol" {
		t.Fatalf("explicit supervisor override was replaced: %#v", opts.Adversary)
	}
	if opts.Coder != saved.Coder || opts.Scout != saved.Scout {
		t.Fatalf("non-explicit roles were not restored: %#v", opts)
	}
}

func TestApplySavedResumeTargetsRejectsIncompatibleOverride(t *testing.T) {
	opts := RunOptions{Mode: ModeRelay, AdversaryExplicit: true, AdversaryExplicitMode: ModeAdversarial}
	if err := applySavedResumeTargets(&opts, FinalRun{Mode: ModeRelay}); err == nil {
		t.Fatal("expected incompatible reviewer override to be rejected")
	}
}

func TestRestoreTargetsFromMetaDoesNotReplaceExplicitResumeTarget(t *testing.T) {
	opts := RunOptions{
		Mode:              ModeRelay,
		Adversary:         RoleTarget{Adapter: "codex", Model: "gpt-5.6-sol"},
		AdversaryExplicit: true,
	}
	meta := Meta{Adapters: map[string]string{"coder": "grok", "supervisor": "claude", "scout": "agy"}, Models: map[string]string{"coder": "grok-4.5", "supervisor": "claude-sonnet-5", "scout": "gemini-3.6-flash-medium"}}
	restoreTargetsFromMeta(&opts, meta)
	if opts.Coder.Adapter != "grok" || opts.Scout.Adapter != "agy" {
		t.Fatalf("missing legacy roles were not restored: %#v", opts)
	}
	if opts.Adversary.Adapter != "codex" || opts.Adversary.Model != "gpt-5.6-sol" {
		t.Fatalf("explicit replacement was overwritten: %#v", opts.Adversary)
	}
}

func TestResumeContinuesSameRunFromFirstIncompletePhase(t *testing.T) {
	installFakeClaudeBinary(t)
	for _, phase := range []RunPhase{PhasePlanning, PhaseImplementing, PhaseTesting, PhaseReviewing, PhaseRepairing} {
		t.Run(string(phase), func(t *testing.T) {
			repo, baseline := createResumeFixtureRepo(t)
			runID := "resume-" + string(phase)
			runDir, err := createRunDir(repo, "", runID)
			if err != nil {
				t.Fatal(err)
			}
			meta := Meta{
				SchemaVersion: ArtifactSchemaVersion,
				RunID:         runID,
				Workdir:       repo,
				Baseline:      baseline,
				Command:       "run",
				Prompt:        "fixture",
				StartedAt:     time.Now().UTC(),
				Adapters:      map[string]string{"coder": "claude", "adversary": "claude"},
				Models:        map[string]string{},
			}
			if err := writeJSONWithNewline(filepath.Join(runDir, "meta.json"), meta); err != nil {
				t.Fatal(err)
			}
			if err := writeFileDurable(filepath.Join(runDir, "input.md"), []byte("fixture\n"), 0o600, false); err != nil {
				t.Fatal(err)
			}
			diff, err := captureDiffArtifact(context.Background(), repo, baseline, runDir, 1)
			if err != nil {
				t.Fatal(err)
			}
			state := RunState{RunID: runID, Workdir: repo, BaselineSHA: baseline, Mode: ModeAdversarial, Status: string(RunStatusRunning), Phase: string(phase), CurrentRound: 1, DiffHash: diff.Metadata.DiffSHA256, LatestDiffPath: diff.PatchPath}
			if err := persistRunState(runDir, state); err != nil {
				t.Fatal(err)
			}
			final := FinalRun{SchemaVersion: ArtifactSchemaVersion, RunID: runID, RunDir: runDir, Workdir: repo, Baseline: baseline, Mode: ModeAdversarial, Coder: RoleTarget{Adapter: "claude"}, Adversary: RoleTarget{Adapter: "claude"}, Status: RunStatusRunning, RoundsRequested: 1, BaselineTest: &TestRun{Command: "true", Passed: true}, Costs: map[string]float64{}, StartedAt: meta.StartedAt}
			if err := writeJSONWithNewline(filepath.Join(runDir, "final.json"), final); err != nil {
				t.Fatal(err)
			}

			resumed, resumeErr := NewApp(DefaultConfig()).Resume(context.Background(), RunOptions{Workdir: repo, Mode: ModeAdversarial, Rounds: 1, TestCmd: "true", Timeout: 10 * time.Second, AllowedPaths: []string{"README.md"}}, runID)
			if resumeErr == nil || ExitCode(resumeErr) != ExitBlockingFindings {
				t.Fatalf("Resume() error = %v, want blocking review result", resumeErr)
			}
			if resumed.RunID != runID || resumed.RunDir != runDir {
				t.Fatalf("resume created a replacement run: %#v", resumed)
			}
			coderOutput := filepath.Join(runDir, "coder-round-1.md")
			_, coderErr := os.Stat(coderOutput)
			shouldRunCoder := phase == PhasePlanning || phase == PhaseImplementing || phase == PhaseRepairing
			if shouldRunCoder && coderErr != nil {
				t.Fatalf("coder should run from %s: %v", phase, coderErr)
			}
			if !shouldRunCoder && !os.IsNotExist(coderErr) {
				t.Fatalf("coder reran after completed implementation in %s", phase)
			}
			var record ResumeRecord
			readJSONFile(t, filepath.Join(runDir, "resume.json"), &record)
			if record.Status != "resumed" || record.ContinuedRunID != runID {
				t.Fatalf("resume record = %#v", record)
			}
		})
	}
}

func TestResumeRoutesInterruptedPartialDiffThroughRecovery(t *testing.T) {
	installFakeClaudeBinary(t)
	repo, baseline := createResumeFixtureRepo(t)
	runID := "resume-partial"
	runDir, err := createRunDir(repo, "", runID)
	if err != nil {
		t.Fatal(err)
	}
	meta := Meta{SchemaVersion: ArtifactSchemaVersion, RunID: runID, Workdir: repo, Baseline: baseline, Command: "run", Prompt: "fixture", StartedAt: time.Now().UTC(), Adapters: map[string]string{"coder": "claude", "adversary": "claude"}, Models: map[string]string{}}
	if err := writeJSONWithNewline(filepath.Join(runDir, "meta.json"), meta); err != nil {
		t.Fatal(err)
	}
	if err := writeFileDurable(filepath.Join(runDir, "input.md"), []byte("fixture\n"), 0o600, false); err != nil {
		t.Fatal(err)
	}
	before, err := captureDiffArtifact(context.Background(), repo, baseline, runDir, 1)
	if err != nil {
		t.Fatal(err)
	}
	state := RunState{RunID: runID, Workdir: repo, BaselineSHA: baseline, Mode: ModeAdversarial, Status: string(RunStatusRunning), Phase: string(PhaseImplementing), CurrentRound: 1, DiffHash: before.Metadata.DiffSHA256, LatestDiffPath: before.PatchPath, InvocationID: "interrupted-invocation"}
	if err := persistRunState(runDir, state); err != nil {
		t.Fatal(err)
	}
	final := FinalRun{SchemaVersion: ArtifactSchemaVersion, RunID: runID, RunDir: runDir, Workdir: repo, Baseline: baseline, Mode: ModeAdversarial, Coder: RoleTarget{Adapter: "claude"}, Adversary: RoleTarget{Adapter: "claude"}, Status: RunStatusRunning, RoundsRequested: 1, BaselineTest: &TestRun{Command: "true", Passed: true}, Costs: map[string]float64{}, StartedAt: meta.StartedAt}
	if err := writeJSONWithNewline(filepath.Join(runDir, "final.json"), final); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("partial worker edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	resumed, resumeErr := NewApp(DefaultConfig()).Resume(context.Background(), RunOptions{Workdir: repo, Mode: ModeAdversarial, Rounds: 1, TestCmd: "true", Timeout: 10 * time.Second, AllowedPaths: []string{"README.md"}}, runID)
	if resumeErr == nil || resumed.Status != RunStatusQuarantined {
		t.Fatalf("final=%#v err=%v, want quarantined recovery", resumed, resumeErr)
	}
	if _, err := os.Stat(filepath.Join(runDir, "recovery-round-1.json")); err != nil {
		t.Fatalf("missing recovery artifact: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(repo, "README.md")); err != nil || string(data) != "partial worker edit\n" {
		t.Fatalf("partial work was not preserved: %q err=%v", data, err)
	}
}

func TestResumeStateAfterPlanningUsesVerifiedWorktree(t *testing.T) {
	state := RunState{
		Phase:        string(PhasePlanning),
		DiffHash:     "stale-diff",
		InvocationID: "planning-invocation",
		Role:         string(RoleSupervisor),
		Adapter:      "claude",
		Model:        "sonnet",
	}

	got := resumeStateAfterPlanning(state, "verified-diff")
	if got.Phase != string(PhaseImplementing) || got.DiffHash != "verified-diff" {
		t.Fatalf("resume state did not begin implementation from verified worktree: %#v", got)
	}
	if got.InvocationID != "" || got.Role != "" || got.Adapter != "" || got.Model != "" {
		t.Fatalf("planning invocation leaked into editor recovery state: %#v", got)
	}
}

func TestInterruptedEditorFailureUsesPhaseWithoutInvocation(t *testing.T) {
	err := interruptedEditorFailure(RunState{}, PhaseImplementing)
	if got := err.Error(); got != "uncheckpointed worktree diff found while resuming implementing; no active invocation was recorded" {
		t.Fatalf("error = %q", got)
	}
}

func createResumeFixtureRepo(t *testing.T) (string, string) {
	t.Helper()
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("baseline\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "baseline")
	return repo, strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
}

func writeResumeFixture(t *testing.T, runDir, runID, repo, baseline string, status RunStatus) {
	t.Helper()
	meta := Meta{SchemaVersion: ArtifactSchemaVersion, RunID: runID, Workdir: repo, Baseline: baseline, Command: "run", Prompt: "fixture", StartedAt: time.Now().UTC(), Adapters: map[string]string{"worker": "fake", "supervisor": "fake"}, Models: map[string]string{}}
	if err := writeJSONWithNewline(filepath.Join(runDir, "meta.json"), meta); err != nil {
		t.Fatal(err)
	}
	emptyDiff := sha256Sum([]byte{})
	state := RunState{SchemaVersion: runStateSchemaVersion, RunID: runID, Workdir: repo, BaselineSHA: baseline, Status: string(status), Phase: string(PhaseReviewing), DiffHash: emptyDiff}
	if err := writeJSONWithNewline(filepath.Join(runDir, "state.json"), state); err != nil {
		t.Fatal(err)
	}
	final := FinalRun{SchemaVersion: ArtifactSchemaVersion, RunID: runID, RunDir: runDir, Workdir: repo, Baseline: baseline, Mode: ModeSupervisor, Status: status, Verdict: "pass", ExitCode: ExitSuccess, FinishedAt: time.Now().UTC()}
	if err := writeJSONWithNewline(filepath.Join(runDir, "final.json"), final); err != nil {
		t.Fatal(err)
	}
}
