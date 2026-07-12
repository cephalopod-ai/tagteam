package tagteam

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeClaudeScript returns a shell script that emulates the parts of the
// `claude` CLI tagteam depends on: --version for Detect, and, for -p
// invocations, a coder-style "ok" result unless the invocation carries the
// adversary/supervisor "dontAsk" permission mode, in which case it returns a
// blocking (needs_changes/major) review.
const fakeClaudeScript = `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "1.0.0"
  exit 0
fi
stdin=$(cat)
if [ -n "$CLAUDE_ARGS_LOG" ]; then
  printf '%s\n%s\n---\n' "$*" "$stdin" >> "$CLAUDE_ARGS_LOG"
fi
match="$* $stdin"
case "$match" in
  *"bounded host-controlled orchestration workflow"*)
    mode="supervisor"
    case "$match" in *"Current mode: relay"*) mode="relay" ;; esac
    source="supervisor"
    case "$match" in *"implementation worker/coder"*) source="worker" ;; esac
    rec="keep"
    target="$mode"
    reason="current mode is appropriate"
    case "$TAGTEAM_TEST_ORCH_ADVISORY:$mode:$source" in
      simplify:relay:supervisor)
        rec="simplify"; target="supervisor"; reason="direct supervisor workflow is enough"
        ;;
      escalate:supervisor:worker)
        rec="escalate"; target="relay"; reason="worker needs scout context"
        ;;
      escalate:supervisor:supervisor)
        rec="escalate"; target="relay"; reason="supervisor agrees scout is needed"
        ;;
      conflict:supervisor:worker)
        rec="escalate"; target="relay"; reason="worker wants more context"
        ;;
      conflict:supervisor:supervisor)
        rec="keep"; target="supervisor"; reason="supervisor prefers simpler mode"
        ;;
      invalid:*)
        printf '%s' '{"result":"not-json","session_id":"","total_cost_usd":0}'
        exit 0
        ;;
    esac
    printf '{"result":"{\\"schema_version\\":1,\\"recommendation\\":\\"%s\\",\\"target_mode\\":\\"%s\\",\\"reason\\":\\"%s\\",\\"confidence\\":\\"high\\"}","session_id":"","total_cost_usd":0}' "$rec" "$target" "$reason"
    exit 0
    ;;
  *"implementation work packages"*)
    printf '%s' '{"result":"{\"schema_version\":1,\"summary\":\"Implement package one\",\"packages\":[{\"id\":\"P1\",\"title\":\"Package one\",\"goal\":\"Do the first slice\",\"estimated_seconds\":1,\"allowed_scope\":[\"README.md\"],\"acceptance\":[\"README updated\"],\"validation\":[\"go test ./...\"]},{\"id\":\"P2\",\"title\":\"Package two\",\"goal\":\"Deferred follow-up\",\"estimated_seconds\":1,\"allowed_scope\":[\"README.md\"],\"acceptance\":[\"follow-up done\"],\"validation\":[\"go test ./...\"]}],\"selected_package\":\"P1\",\"defer\":[\"P2\"]}","session_id":"","total_cost_usd":0}'
    exit 0
    ;;
esac
is_review=0
for arg in "$@"; do
  if [ "$arg" = "dontAsk" ]; then
    is_review=1
  fi
done
if [ "$is_review" = "1" ]; then
  printf '%s' '{"result":"{\"schema_version\":2,\"verdict\":\"needs_changes\",\"summary\":\"needs fixes\",\"findings\":[{\"severity\":\"major\",\"file\":\"main.go\",\"line\":1,\"issue\":\"bug\",\"fix\":\"fix it\"}],\"test_suggestions\":[],\"data_loss_checks\":{\"malformed_input_preservation\":{\"status\":\"not_applicable\",\"evidence\":\"not applicable\"},\"annotation_history_retention\":{\"status\":\"not_applicable\",\"evidence\":\"not applicable\"},\"ambiguous_identity_handling\":{\"status\":\"not_applicable\",\"evidence\":\"not applicable\"},\"read_only_non_mutation\":{\"status\":\"pass\",\"evidence\":\"read-only reviewer\"}},\"prior_finding_dispositions\":[]}","session_id":"","total_cost_usd":0}'
else
  printf '%s' '{"result":"{\"schema_version\":1,\"status\":\"completed\",\"summary\":\"ok\",\"files_changed\":[],\"checks_run\":[],\"remaining_risks\":[]}","session_id":"sess1","total_cost_usd":0}'
fi
`

const fakeClaudeInvokeFailScript = `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "1.0.0"
  exit 0
fi
echo "reviewer exploded" >&2
exit 7
`

const fakeClaudeInvalidReviewScript = `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "1.0.0"
  exit 0
fi
printf '%s' '{"result":"not-json","session_id":"","total_cost_usd":0}'
`

const fakeCodexPassingReviewScript = `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "codex 1.0.0"
  exit 0
fi
printf '%s' '{"schema_version":2,"verdict":"pass","summary":"fallback passed","findings":[],"test_suggestions":[],"data_loss_checks":{"malformed_input_preservation":{"status":"not_applicable","evidence":"not applicable"},"annotation_history_retention":{"status":"not_applicable","evidence":"not applicable"},"ambiguous_identity_handling":{"status":"not_applicable","evidence":"not applicable"},"read_only_non_mutation":{"status":"pass","evidence":"read-only reviewer"}},"prior_finding_dispositions":[]}'
`

const fakeAgyScript = `#!/bin/sh
if [ "$1" = "--help" ] || [ "$1" = "--version" ]; then
  echo "agy fake"
  exit 0
fi
stdin=$(cat)
if [ -n "$AGY_ARGS_LOG" ]; then
  printf '%s\n%s\n---\n' "$*" "$stdin" >> "$AGY_ARGS_LOG"
fi
printf '%s' '{"relevant_files":["README.md"],"likely_entry_points":["README"],"existing_patterns":["plain text"],"risks":["none"],"suggested_tests":["go test ./..."]}'
`

const fakeAgyFailScript = `#!/bin/sh
if [ "$1" = "--help" ] || [ "$1" = "--version" ]; then
  echo "agy fake"
  exit 0
fi
echo "scout adapter failed" >&2
exit 2
`

const fakeAgyInvalidScript = `#!/bin/sh
if [ "$1" = "--help" ] || [ "$1" = "--version" ]; then
  echo "agy fake"
  exit 0
fi
printf '%s' 'not-json'
`

func installFakeClaudeBinary(t *testing.T) {
	t.Helper()
	binDir := t.TempDir()
	scriptPath := filepath.Join(binDir, "claude")
	if err := os.WriteFile(scriptPath, []byte(fakeClaudeScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func installFakeBinaries(t *testing.T, scripts map[string]string) {
	t.Helper()
	binDir := t.TempDir()
	for name, script := range scripts {
		scriptPath := filepath.Join(binDir, name)
		if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestRunLoop_RelayModeWritesExpectedArtifacts(t *testing.T) {
	installFakeBinaries(t, map[string]string{
		"agy":    fakeAgyScript,
		"claude": fakeClaudeScript,
	})
	agyLogPath := filepath.Join(t.TempDir(), "agy.log")

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	app := NewApp(DefaultConfig())
	codeIntelCommand := writeCodeIntelScript(t, "echo sensor failed >&2; exit 7")
	final, err := app.Run(context.Background(), RunOptions{
		Prompt:           "add a feature",
		Workdir:          repo,
		Mode:             ModeRelay,
		Scout:            RoleTarget{Adapter: "agy", Model: "gemini-3.5-flash-low"},
		Coder:            RoleTarget{Adapter: "claude"},
		Adversary:        RoleTarget{Adapter: "claude"},
		ScoutMode:        "recon",
		PostScoutMode:    "polish",
		ScoutRetrieval:   true,
		CodeIntelCommand: codeIntelCommand,
		Rounds:           1,
		Timeout:          10 * time.Second,
		EnvOverlay:       map[string]string{"AGY_ARGS_LOG": agyLogPath},
	})
	if err == nil {
		t.Fatal("expected blocking-findings error from fake supervisor review")
	}
	if final.Mode != ModeRelay {
		t.Fatalf("mode = %q", final.Mode)
	}
	for _, name := range []string{
		"supervisor-brief.md",
		"orchestration-decision.json",
		"retrieval-round-1.json",
		"code-intel-round-1.json",
		"scout-context-round-1.json",
		"scout-execution-round-1.json",
		"scout-round-1.json",
		"supervisor-instructions.md",
		"coder-round-1.md",
		"diff-round-1.patch",
		"post-scout-execution-round-1.json",
		"post-scout-round-1.json",
		"supervisor-review-round-1.json",
		"final.json",
	} {
		if !fileExists(filepath.Join(final.RunDir, name)) {
			t.Fatalf("expected relay artifact %s in %s", name, final.RunDir)
		}
	}
	var codeIntel CodeIntelArtifact
	readJSONFile(t, filepath.Join(final.RunDir, "code-intel-round-1.json"), &codeIntel)
	if codeIntel.Status != codeIntelStatusError || len(codeIntel.Errors) == 0 {
		t.Fatalf("code-intel failure artifact = %#v", codeIntel)
	}
	if final.Adapters["scout"] != "agy" || final.Adapters["coder"] != "claude" || final.Adapters["supervisor"] != "claude" {
		t.Fatalf("adapters = %#v", final.Adapters)
	}
	agyLog, err := os.ReadFile(agyLogPath)
	if err != nil {
		t.Fatalf("read agy log: %v", err)
	}
	if !strings.Contains(string(agyLog), "Host retrieval evidence") {
		t.Fatalf("scout prompt did not include retrieval context:\n%s", string(agyLog))
	}
}

func TestRunLoop_RelaySimplifiesToSupervisorBeforeScout(t *testing.T) {
	t.Setenv("TAGTEAM_TEST_ORCH_ADVISORY", "simplify")
	installFakeClaudeBinary(t)

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "hello\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	app := NewApp(DefaultConfig())
	final, err := app.Run(context.Background(), RunOptions{
		Prompt:             "fix typo",
		Workdir:            repo,
		Mode:               ModeRelay,
		Scout:              RoleTarget{Adapter: "agy", Model: "Gemini 3.5 Flash (Medium)"},
		Coder:              RoleTarget{Adapter: "claude"},
		Adversary:          RoleTarget{Adapter: "claude"},
		ScoutMode:          "recon",
		PostScoutMode:      "polish",
		ScoutRetrieval:     true,
		ScoutFailurePolicy: "continue",
		Rounds:             1,
		Timeout:            10 * time.Second,
		EnvOverlay:         map[string]string{"TAGTEAM_TEST_ORCH_ADVISORY": "simplify"},
	})
	if err == nil {
		t.Fatal("expected blocking review error from fake supervisor")
	}
	if final.Mode != ModeSupervisor {
		t.Fatalf("mode = %q error=%v", final.Mode, err)
	}
	if _, ok := final.RoleStatuses["coder"]; ok {
		t.Fatalf("stale coder status after supervisor transition: %#v", final.RoleStatuses)
	}
	if _, ok := final.RoleStatuses["worker"]; !ok {
		t.Fatalf("missing worker status after supervisor transition: %#v", final.RoleStatuses)
	}
	if fileExists(filepath.Join(final.RunDir, "scout-round-1.json")) {
		t.Fatal("scout should be skipped after relay simplification")
	}
	var decision OrchestrationDecision
	readJSONFile(t, filepath.Join(final.RunDir, orchestrationDecisionArtifact), &decision)
	if decision.AppliedTransition == nil || decision.AppliedTransition.From != ModeRelay || decision.AppliedTransition.To != ModeSupervisor {
		t.Fatalf("decision = %#v", decision)
	}
	if !decision.TransitionLimitConsumed {
		t.Fatal("expected transition limit consumed")
	}
}

func TestRunLoop_SupervisorEscalatesToRelayWhenBothAgentsAgree(t *testing.T) {
	t.Setenv("TAGTEAM_TEST_ORCH_ADVISORY", "escalate")
	installFakeBinaries(t, map[string]string{
		"agy":    fakeAgyScript,
		"claude": fakeClaudeScript,
	})

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "hello\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	cfg := DefaultConfig()
	// Keep this orchestration transition test hermetic; the built-in relay scout
	// may use a local HTTP adapter that is covered separately.
	cfg.Defaults.Scout = "agy:Gemini 3.5 Flash (Medium)"
	app := NewApp(cfg)
	final, err := app.Run(context.Background(), RunOptions{
		Prompt:             "map this unfamiliar repo and fix the bug",
		Workdir:            repo,
		Mode:               ModeSupervisor,
		Coder:              RoleTarget{Adapter: "claude"},
		Adversary:          RoleTarget{Adapter: "claude"},
		ScoutMode:          "recon",
		PostScoutMode:      "polish",
		ScoutRetrieval:     false,
		ScoutFailurePolicy: "continue",
		Rounds:             1,
		Timeout:            10 * time.Second,
		EnvOverlay:         map[string]string{"TAGTEAM_TEST_ORCH_ADVISORY": "escalate"},
	})
	if err == nil {
		t.Fatal("expected blocking review error from fake supervisor")
	}
	if final.Mode != ModeRelay {
		t.Fatalf("mode = %q", final.Mode)
	}
	if got := roleTargetString(final.Scout); got != cfg.Defaults.Scout {
		t.Fatalf("escalated scout = %q", got)
	}
	if _, ok := final.RoleStatuses["worker"]; ok {
		t.Fatalf("stale worker status after relay transition: %#v", final.RoleStatuses)
	}
	if _, ok := final.RoleStatuses["coder"]; !ok {
		t.Fatalf("missing coder status after relay transition: %#v", final.RoleStatuses)
	}
	if !fileExists(filepath.Join(final.RunDir, "scout-round-1.json")) {
		t.Fatal("expected scout to run after escalation")
	}
	var decision OrchestrationDecision
	readJSONFile(t, filepath.Join(final.RunDir, orchestrationDecisionArtifact), &decision)
	if decision.AppliedTransition == nil || decision.AppliedTransition.From != ModeSupervisor || decision.AppliedTransition.To != ModeRelay {
		t.Fatalf("decision = %#v", decision)
	}
	if len(decision.Advisories) != 2 {
		t.Fatalf("advisories = %#v", decision.Advisories)
	}
}

func TestRunLoop_AdvisoryFailureFallsBackToOriginalMode(t *testing.T) {
	t.Setenv("TAGTEAM_TEST_ORCH_ADVISORY", "invalid")
	installFakeBinaries(t, map[string]string{
		"agy":    fakeAgyScript,
		"claude": fakeClaudeScript,
	})

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "hello\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	app := NewApp(DefaultConfig())
	final, err := app.Run(context.Background(), RunOptions{
		Prompt:             "add a feature",
		Workdir:            repo,
		Mode:               ModeRelay,
		Scout:              RoleTarget{Adapter: "agy", Model: "gemini-3.5-flash-low"},
		Coder:              RoleTarget{Adapter: "claude"},
		Adversary:          RoleTarget{Adapter: "claude"},
		ScoutMode:          "recon",
		PostScoutMode:      "polish",
		ScoutRetrieval:     false,
		ScoutFailurePolicy: "continue",
		Rounds:             1,
		Timeout:            10 * time.Second,
		EnvOverlay:         map[string]string{"TAGTEAM_TEST_ORCH_ADVISORY": "invalid"},
	})
	if err == nil {
		t.Fatal("expected blocking review error from fake supervisor")
	}
	if final.Mode != ModeRelay {
		t.Fatalf("mode = %q", final.Mode)
	}
	if !fileExists(filepath.Join(final.RunDir, "scout-round-1.json")) {
		t.Fatal("expected original relay mode to continue after advisory failure")
	}
	var decision OrchestrationDecision
	readJSONFile(t, filepath.Join(final.RunDir, orchestrationDecisionArtifact), &decision)
	if !decision.Degraded || decision.FinalMode != ModeRelay {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestRunLoop_RelayModeDisabledScoutRetrievalSkipsArtifact(t *testing.T) {
	installFakeBinaries(t, map[string]string{
		"agy":    fakeAgyScript,
		"claude": fakeClaudeScript,
	})

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	app := NewApp(DefaultConfig())
	final, err := app.Run(context.Background(), RunOptions{
		Prompt:             "add a feature",
		Workdir:            repo,
		Mode:               ModeRelay,
		Scout:              RoleTarget{Adapter: "agy", Model: "gemini-3.5-flash-low"},
		Coder:              RoleTarget{Adapter: "claude"},
		Adversary:          RoleTarget{Adapter: "claude"},
		ScoutMode:          "recon",
		PostScoutMode:      "polish",
		ScoutRetrieval:     false,
		ScoutFailurePolicy: "fail",
		Rounds:             1,
		Timeout:            10 * time.Second,
	})
	if err == nil {
		t.Fatal("expected blocking-findings error from fake supervisor review")
	}
	if fileExists(filepath.Join(final.RunDir, "retrieval-round-1.json")) {
		t.Fatal("did not expect retrieval artifact when retrieval is disabled")
	}
	if !fileExists(filepath.Join(final.RunDir, "scout-context-round-1.json")) {
		t.Fatal("expected scout context artifact")
	}
	if !fileExists(filepath.Join(final.RunDir, "scout-execution-round-1.json")) {
		t.Fatal("expected scout execution artifact")
	}
	if !fileExists(filepath.Join(final.RunDir, "scout-round-1.json")) {
		t.Fatal("expected normal scout artifact")
	}
}

func TestRunLoop_RelayModeRetrievalUnavailableStillRunsScout(t *testing.T) {
	installFakeBinaries(t, map[string]string{
		"agy":    fakeAgyScript,
		"claude": fakeClaudeScript,
	})
	oldLookPath := execLookPath
	execLookPath = func(file string) (string, error) {
		if file == "rg" {
			return "", exec.ErrNotFound
		}
		return oldLookPath(file)
	}
	t.Cleanup(func() { execLookPath = oldLookPath })

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "hello\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	app := NewApp(DefaultConfig())
	final, err := app.Run(context.Background(), RunOptions{
		Prompt:             "add a feature",
		Workdir:            repo,
		Mode:               ModeRelay,
		Scout:              RoleTarget{Adapter: "agy", Model: "gemini-3.5-flash-low"},
		Coder:              RoleTarget{Adapter: "claude"},
		Adversary:          RoleTarget{Adapter: "claude"},
		ScoutMode:          "recon",
		PostScoutMode:      "polish",
		ScoutRetrieval:     true,
		ScoutFailurePolicy: "continue",
		Rounds:             1,
		Timeout:            10 * time.Second,
	})
	if err == nil {
		t.Fatal("expected blocking-findings error from fake supervisor review")
	}
	var retrieval RetrievalArtifact
	readJSONFile(t, filepath.Join(final.RunDir, "retrieval-round-1.json"), &retrieval)
	if retrieval.Status != "unavailable" {
		t.Fatalf("retrieval = %#v", retrieval)
	}
	var status ScoutExecutionArtifact
	readJSONFile(t, filepath.Join(final.RunDir, "scout-execution-round-1.json"), &status)
	if !status.RetrievalDegraded || !status.ScoutRan || !status.ScoutSucceeded {
		t.Fatalf("scout status = %#v", status)
	}
}

func TestRunLoop_RelayModeScoutInvocationFailureContinue(t *testing.T) {
	installFakeBinaries(t, map[string]string{
		"agy":    fakeAgyFailScript,
		"claude": fakeClaudeScript,
	})

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "hello\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	app := NewApp(DefaultConfig())
	final, err := app.Run(context.Background(), RunOptions{
		Prompt:             "add a feature",
		Workdir:            repo,
		Mode:               ModeRelay,
		Scout:              RoleTarget{Adapter: "agy"},
		Coder:              RoleTarget{Adapter: "claude"},
		Adversary:          RoleTarget{Adapter: "claude"},
		ScoutMode:          "recon",
		PostScoutMode:      "polish",
		ScoutRetrieval:     false,
		ScoutFailurePolicy: "continue",
		Rounds:             1,
		Timeout:            10 * time.Second,
	})
	if err == nil {
		t.Fatal("expected blocking-findings error after continuing without scout")
	}
	if !fileExists(filepath.Join(final.RunDir, "coder-round-1.md")) {
		t.Fatal("expected coder to run after scout failure")
	}
	if !fileExists(filepath.Join(final.RunDir, "supervisor-review-round-1.json")) {
		t.Fatal("expected supervisor review to run after scout failure")
	}
	if !final.Degraded || final.DegradedReason == "" {
		t.Fatalf("expected final degradation metadata, final=%#v", final)
	}
	if len(final.RoleLosses) == 0 {
		t.Fatalf("expected role loss metadata, final=%#v", final)
	}
	var status ScoutExecutionArtifact
	readJSONFile(t, filepath.Join(final.RunDir, "scout-execution-round-1.json"), &status)
	if !status.ScoutRan || status.ScoutSucceeded || status.FailureClass != scoutFailureClassInvocation || !status.ContinuedWithoutScoutContext {
		t.Fatalf("scout status = %#v", status)
	}
	var postStatus ScoutExecutionArtifact
	readJSONFile(t, filepath.Join(final.RunDir, "post-scout-execution-round-1.json"), &postStatus)
	if !postStatus.ScoutRan || postStatus.ScoutSucceeded || postStatus.FailureClass != scoutFailureClassInvocation || !postStatus.ContinuedWithoutScoutContext {
		t.Fatalf("post-scout status = %#v", postStatus)
	}
}

func TestRunLoop_RelayModeScoutInvocationFailureFail(t *testing.T) {
	installFakeBinaries(t, map[string]string{
		"agy":    fakeAgyFailScript,
		"claude": fakeClaudeScript,
	})

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "hello\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	app := NewApp(DefaultConfig())
	final, err := app.Run(context.Background(), RunOptions{
		Prompt:             "add a feature",
		Workdir:            repo,
		Mode:               ModeRelay,
		Scout:              RoleTarget{Adapter: "agy"},
		Coder:              RoleTarget{Adapter: "claude"},
		Adversary:          RoleTarget{Adapter: "claude"},
		ScoutMode:          "recon",
		PostScoutMode:      "polish",
		ScoutRetrieval:     false,
		ScoutFailurePolicy: "fail",
		Rounds:             1,
		Timeout:            10 * time.Second,
	})
	if err == nil {
		t.Fatal("expected strict scout failure")
	}
	if !strings.Contains(err.Error(), "scout_failure_policy=fail") {
		t.Fatalf("error = %v", err)
	}
	if fileExists(filepath.Join(final.RunDir, "coder-round-1.md")) {
		t.Fatal("coder should not run after strict scout failure")
	}
	var status ScoutExecutionArtifact
	readJSONFile(t, filepath.Join(final.RunDir, "scout-execution-round-1.json"), &status)
	if !status.ScoutRan || status.ScoutSucceeded || status.FailureClass != scoutFailureClassInvocation || status.ContinuedWithoutScoutContext {
		t.Fatalf("scout status = %#v", status)
	}
}

func TestRunLoop_RelayModeScoutOutputFailureContinue(t *testing.T) {
	installFakeBinaries(t, map[string]string{
		"agy":    fakeAgyInvalidScript,
		"claude": fakeClaudeScript,
	})

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "hello\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	app := NewApp(DefaultConfig())
	final, err := app.Run(context.Background(), RunOptions{
		Prompt:             "add a feature",
		Workdir:            repo,
		Mode:               ModeRelay,
		Scout:              RoleTarget{Adapter: "agy"},
		Coder:              RoleTarget{Adapter: "claude"},
		Adversary:          RoleTarget{Adapter: "claude"},
		ScoutMode:          "recon",
		PostScoutMode:      "polish",
		ScoutRetrieval:     false,
		ScoutFailurePolicy: "continue",
		Rounds:             1,
		Timeout:            10 * time.Second,
	})
	if err == nil {
		t.Fatal("expected blocking-findings error after continuing without scout")
	}
	var status ScoutExecutionArtifact
	readJSONFile(t, filepath.Join(final.RunDir, "scout-execution-round-1.json"), &status)
	if status.FailureClass != scoutFailureClassOutput || !status.ContinuedWithoutScoutContext {
		t.Fatalf("scout status = %#v", status)
	}
}

func TestRunLoop_RelayModeScoutContextUnknownKeepsRetrieval(t *testing.T) {
	installFakeBinaries(t, map[string]string{
		"agy":    fakeAgyScript,
		"claude": fakeClaudeScript,
	})
	agyLogPath := filepath.Join(t.TempDir(), "agy.log")

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "hello codex\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	app := NewApp(DefaultConfig())
	final, err := app.Run(context.Background(), RunOptions{
		Prompt:         "add codex feature",
		Workdir:        repo,
		Mode:           ModeRelay,
		Scout:          RoleTarget{Adapter: "agy", Model: "gemini-3.5-flash-low"},
		Coder:          RoleTarget{Adapter: "claude"},
		Adversary:      RoleTarget{Adapter: "claude"},
		ScoutMode:      "recon",
		PostScoutMode:  "polish",
		ScoutRetrieval: true,
		Rounds:         1,
		Timeout:        10 * time.Second,
		EnvOverlay:     map[string]string{"AGY_ARGS_LOG": agyLogPath},
	})
	if err == nil {
		t.Fatal("expected blocking-findings error from fake supervisor review")
	}
	var budget ScoutContextBudgetArtifact
	readJSONFile(t, filepath.Join(final.RunDir, "scout-context-round-1.json"), &budget)
	if budget.Status != scoutContextStatusUnknown || !budget.NoConfiguredLimit {
		t.Fatalf("budget = %#v", budget)
	}
	agyLog, err := os.ReadFile(agyLogPath)
	if err != nil {
		t.Fatalf("read agy log: %v", err)
	}
	if !strings.Contains(string(agyLog), "Host retrieval evidence") {
		t.Fatalf("retrieval should be unchanged for unknown budget:\n%s", string(agyLog))
	}
}
