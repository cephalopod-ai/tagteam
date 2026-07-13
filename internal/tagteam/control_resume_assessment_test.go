package tagteam

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestControlServicePrepareResumeIsReadOnlyAndReady(t *testing.T) {
	repo, baseline := createResumeFixtureRepo(t)
	stateRoot := t.TempDir()
	runID := "resume-assessment-ready"
	runDir, err := createRunDir(repo, stateRoot, runID)
	if err != nil {
		t.Fatal(err)
	}
	writeResumeFixture(t, runDir, runID, repo, baseline, RunStatusRunning)
	stateBefore, err := os.ReadFile(filepath.Join(runDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	repository, err := resolveControlRepository(repo)
	if err != nil {
		t.Fatal(err)
	}
	service := ControlService{RepositoryRoot: repo, StateRoot: stateRoot, ProducerVersion: "test"}
	assessment, err := service.PrepareResume(context.Background(), ControlResumeRequest{SchemaVersion: ControlContractVersion, Repository: repository, RunID: runID})
	if err != nil {
		t.Fatal(err)
	}
	if !assessment.Resumable || assessment.ReasonCode != "resumable" || assessment.ActionDigest == "" {
		t.Fatalf("assessment = %#v", assessment)
	}
	stateAfter, err := os.ReadFile(filepath.Join(runDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(stateAfter) != string(stateBefore) {
		t.Fatal("resume assessment changed state.json")
	}
	if _, err := os.Stat(filepath.Join(runDir, "resume.json")); !os.IsNotExist(err) {
		t.Fatalf("resume assessment wrote resume.json: %v", err)
	}
	if status := runGit(t, repo, "status", "--porcelain"); status != "" {
		t.Fatalf("resume assessment changed worktree: %q", status)
	}
}

func TestControlServicePrepareResumeReportsBaselineMismatchWithoutQuarantine(t *testing.T) {
	repo, baseline := createResumeFixtureRepo(t)
	stateRoot := t.TempDir()
	runID := "resume-assessment-mismatch"
	runDir, err := createRunDir(repo, stateRoot, runID)
	if err != nil {
		t.Fatal(err)
	}
	writeResumeFixture(t, runDir, runID, repo, baseline, RunStatusRunning)
	stateBefore, err := os.ReadFile(filepath.Join(runDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("new head\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "advance")
	repository, err := resolveControlRepository(repo)
	if err != nil {
		t.Fatal(err)
	}
	service := ControlService{RepositoryRoot: repo, StateRoot: stateRoot, ProducerVersion: "test"}
	assessment, err := service.PrepareResume(context.Background(), ControlResumeRequest{SchemaVersion: ControlContractVersion, Repository: repository, RunID: runID})
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Resumable || assessment.ReasonCode != "baseline_mismatch" {
		t.Fatalf("assessment = %#v", assessment)
	}
	stateAfter, err := os.ReadFile(filepath.Join(runDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(stateAfter) != string(stateBefore) {
		t.Fatal("resume assessment quarantined or otherwise changed state.json")
	}
}

func TestControlServicePrepareResumeRejectsEscapingArtifactSymlinks(t *testing.T) {
	repo, baseline := createResumeFixtureRepo(t)
	stateRoot := t.TempDir()
	repository, err := resolveControlRepository(repo)
	if err != nil {
		t.Fatal(err)
	}
	service := ControlService{RepositoryRoot: repo, StateRoot: stateRoot, ProducerVersion: "test"}
	for _, artifact := range []string{"state.json", "meta.json", "final.json", "run.lock"} {
		t.Run(artifact, func(t *testing.T) {
			runID := "prep-escape-" + strings.ReplaceAll(artifact, ".", "-")
			runDir, err := createRunDir(repo, stateRoot, runID)
			if err != nil {
				t.Fatal(err)
			}
			writeResumeFixture(t, runDir, runID, repo, baseline, RunStatusRunning)
			outside := t.TempDir()
			sentinel := filepath.Join(outside, "secret-"+artifact)
			if err := os.WriteFile(sentinel, []byte(`{"status":"external_sentinel","run_id":"leaked"}`), 0o644); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(runDir, artifact)
			if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if err := os.Symlink(sentinel, target); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			assessment, err := service.PrepareResume(context.Background(), ControlResumeRequest{
				SchemaVersion: ControlContractVersion,
				Repository:    repository,
				RunID:         runID,
			})
			if err != nil {
				t.Fatal(err)
			}
			if assessment.Resumable {
				t.Fatalf("escaping %s reported resumable: %#v", artifact, assessment)
			}
			after, err := os.ReadFile(sentinel)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(after), "external_sentinel") {
				t.Fatalf("external sentinel for %s was modified: %s", artifact, after)
			}
		})
	}
}
