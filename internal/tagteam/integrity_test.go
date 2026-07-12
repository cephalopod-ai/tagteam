package tagteam

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateRequestArtifactPathsRejectsEscape(t *testing.T) {
	runDir := t.TempDir()
	err := validateRequestArtifactPaths(Request{RunDir: runDir, OutputPath: filepath.Join(filepath.Dir(runDir), "escape.json")})
	if err == nil {
		t.Fatal("expected escaped output path to be rejected")
	}
}

func TestRunAdapterRejectsReadOnlyGitMutation(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	readme := filepath.Join(repo, "README.md")
	if err := os.WriteFile(readme, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "baseline")

	adapter := fakeDirectAdapter{
		build: func(role Role, req Request) (*CommandSpec, error) { return &CommandSpec{}, nil },
		direct: func(role Role, req Request) (Result, error) {
			if err := os.WriteFile(readme, []byte("mutated\n"), 0o644); err != nil {
				return Result{}, err
			}
			return Result{Text: "report"}, nil
		},
	}
	runDir := t.TempDir()
	_, err := (&App{}).runAdapter(context.Background(), adapter, RoleReporter, Request{Workdir: repo, RunDir: runDir, Phase: "read-only test"}, false)
	if err == nil || !IsIntegrityViolation(err) {
		t.Fatalf("error = %v, want integrity violation", err)
	}
	if _, statErr := os.Stat(filepath.Join(runDir, "integrity-violation.json")); statErr != nil {
		t.Fatalf("integrity artifact: %v", statErr)
	}
}

func TestRunAdapterAllowsReadOnlyIndexStatusTransition(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	readme := filepath.Join(repo, "README.md")
	if err := os.WriteFile(readme, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "baseline")
	if err := os.WriteFile(readme, []byte("existing worker edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")

	adapter := fakeDirectAdapter{
		build: func(role Role, req Request) (*CommandSpec, error) { return &CommandSpec{}, nil },
		direct: func(role Role, req Request) (Result, error) {
			_, err := runCommand(context.Background(), repo, "git", "reset", "--quiet")
			return Result{Text: "report"}, err
		},
	}
	if _, err := (&App{}).runAdapter(context.Background(), adapter, RoleReporter, Request{Workdir: repo, RunDir: t.TempDir(), Phase: "index transition"}, false); err != nil {
		t.Fatalf("status-only transition rejected: %v", err)
	}
}

func TestScoutIntegrityViolationAlwaysBlocks(t *testing.T) {
	err := &IntegrityViolationError{Paths: []string{"git:governed.yaml"}}
	if !shouldBlockScoutFailure(LossPolicy("degrade"), err) {
		t.Fatal("integrity violation must block even when ordinary scout failures degrade")
	}
	if shouldBlockScoutFailure(LossPolicy("degrade"), context.DeadlineExceeded) {
		t.Fatal("ordinary scout failure should retain configured degradation policy")
	}
}

func TestRunAdapterRestoresProtectedPointer(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("baseline\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "baseline")
	pointer := filepath.Join(repo, ".tagteam", "repo.json")
	if err := os.MkdirAll(filepath.Dir(pointer), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pointer, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	adapter := fakeDirectAdapter{
		build: func(role Role, req Request) (*CommandSpec, error) { return &CommandSpec{}, nil },
		direct: func(role Role, req Request) (Result, error) {
			if err := os.WriteFile(pointer, []byte("tampered\n"), 0o644); err != nil {
				return Result{}, err
			}
			return Result{Text: "report"}, nil
		},
	}
	_, err := (&App{}).runAdapter(context.Background(), adapter, RoleReporter, Request{Workdir: repo, RunDir: t.TempDir(), Phase: "pointer test"}, false)
	if err == nil || !IsIntegrityViolation(err) {
		t.Fatalf("error = %v, want integrity violation", err)
	}
	data, readErr := os.ReadFile(pointer)
	if readErr != nil || string(data) != "original\n" {
		t.Fatalf("pointer was not restored: %q err=%v", data, readErr)
	}
}

func TestRunAdapterRestoresProtectedRunState(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("baseline\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "baseline")

	runDir := t.TempDir()
	statePath := filepath.Join(runDir, "state.json")
	if err := writeRunState(runDir, RunState{RunID: "integrity-test", Status: "running", Phase: string(PhasePlanning)}); err != nil {
		t.Fatal(err)
	}
	adapter := fakeDirectAdapter{
		build: func(role Role, req Request) (*CommandSpec, error) { return &CommandSpec{}, nil },
		direct: func(role Role, req Request) (Result, error) {
			if err := os.WriteFile(statePath, []byte("tampered\n"), 0o600); err != nil {
				return Result{}, err
			}
			return Result{Text: "report"}, nil
		},
	}
	_, err := (&App{}).runAdapter(context.Background(), adapter, RoleReporter, Request{Workdir: repo, RunDir: runDir, Phase: "state test"}, false)
	if err == nil || !IsIntegrityViolation(err) {
		t.Fatalf("error = %v, want integrity violation", err)
	}
	var restored RunState
	readJSONFile(t, statePath, &restored)
	if restored.InvocationID == "" || restored.Role != string(RoleReporter) {
		t.Fatalf("restored state did not retain the host transition: %#v", restored)
	}
}
