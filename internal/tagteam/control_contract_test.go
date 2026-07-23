package tagteam

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func controlLaunchFixture(t *testing.T, repo string) ControlLaunchSpec {
	t.Helper()
	return ControlLaunchSpec{
		SchemaVersion: ControlContractVersion,
		Repository:    ControlRepository{CanonicalRoot: repo},
		Prompt:        "repair the parser",
		Team: ControlTeamSpec{
			Mode:       ModeSupervisor,
			Worker:     &ControlRoleTarget{Adapter: "agy", Model: "gemini-3.6-flash-medium"},
			Supervisor: &ControlRoleTarget{Adapter: "codex", Model: "gpt-5.6-sol"},
		},
		AllowedPaths: []string{"./internal/", "README.md"},
		Rounds:       2,
		TimeBudget: ControlTimeBudget{
			InvocationTimeoutSeconds: 900,
			WatchdogTimeoutSeconds:   300,
			WallTimeoutSeconds:       3600,
		},
		TestPreset:     "go-test",
		RecoveryPolicy: "assist",
	}
}

func controlServiceFixture(t *testing.T) (ControlService, string, string) {
	t.Helper()
	repo, _ := createResumeFixtureRepo(t)
	stateRoot := t.TempDir()
	locator, err := resolveStateLocator(repo, stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	runID := "2026-07-12T120000.000000000Z"
	runDir, err := locator.RunDir(runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return ControlService{RepositoryRoot: repo, StateRoot: stateRoot, ProducerVersion: "test"}, runID, runDir
}

func TestNormalizeControlLaunchCanonicalizesAndDigestsDeterministically(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	spec := controlLaunchFixture(t, repo)
	normalized, err := NormalizeControlLaunch(spec)
	if err != nil {
		t.Fatal(err)
	}
	canonicalRepo, err := canonicalPath(repo, true)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Repository.RepoID == "" || normalized.Repository.CanonicalRoot != canonicalRepo {
		t.Fatalf("repository = %#v", normalized.Repository)
	}
	if got := normalized.AllowedPaths[0]; got != "internal/" {
		t.Fatalf("allowed path = %q, want internal/", got)
	}
	first, err := ControlActionDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ControlActionDigest(normalized)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || len(first) != 64 {
		t.Fatalf("digests = %q %q", first, second)
	}
}

func TestControlMutatingDigestsBindOperationAndIdentity(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	launch := controlLaunchFixture(t, repo)
	start := ControlStartRequest{SchemaVersion: ControlContractVersion, Launch: launch, IdempotencyKey: "session-1-generation-1"}
	first, err := ControlStartActionDigest(start)
	if err != nil {
		t.Fatal(err)
	}
	start.IdempotencyKey = "session-1-generation-2"
	second, err := ControlStartActionDigest(start)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("start digest did not bind idempotency key")
	}
	repository, err := resolveControlRepository(repo)
	if err != nil {
		t.Fatal(err)
	}
	resume, err := ControlResumeActionDigest(ControlResumeRequest{SchemaVersion: ControlContractVersion, Repository: repository, RunID: "run-1"})
	if err != nil {
		t.Fatal(err)
	}
	cancel, err := ControlCancelActionDigest(ControlCancelRequest{SchemaVersion: ControlContractVersion, Repository: repository, RunID: "run-1"})
	if err != nil {
		t.Fatal(err)
	}
	otherRun, err := ControlResumeActionDigest(ControlResumeRequest{SchemaVersion: ControlContractVersion, Repository: repository, RunID: "run-2"})
	if err != nil {
		t.Fatal(err)
	}
	if resume == cancel || resume == otherRun {
		t.Fatal("run action digest did not bind operation and run identity")
	}
}

func TestControlStartActionDigestBindsEveryMutableLaunchField(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	request := ControlStartRequest{
		SchemaVersion:  ControlContractVersion,
		Launch:         controlLaunchFixture(t, repo),
		IdempotencyKey: "session-1-generation-1",
	}
	baseline, err := ControlStartActionDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		edit func(*ControlStartRequest)
	}{
		{name: "prompt", edit: func(request *ControlStartRequest) { request.Launch.Prompt = "repair a different parser" }},
		{name: "worker model", edit: func(request *ControlStartRequest) { request.Launch.Team.Worker.Model = "gemini-3.5-pro" }},
		{name: "supervisor model", edit: func(request *ControlStartRequest) { request.Launch.Team.Supervisor.Model = "gpt-5.6-terra" }},
		{name: "scope", edit: func(request *ControlStartRequest) { request.Launch.AllowedPaths = []string{"README.md"} }},
		{name: "rounds", edit: func(request *ControlStartRequest) { request.Launch.Rounds = 1 }},
		{name: "invocation timeout", edit: func(request *ControlStartRequest) { request.Launch.TimeBudget.InvocationTimeoutSeconds-- }},
		{name: "test preset", edit: func(request *ControlStartRequest) { request.Launch.TestPreset = "" }},
		{name: "idempotency key", edit: func(request *ControlStartRequest) { request.IdempotencyKey = "session-1-generation-2" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := request
			candidate.Launch.AllowedPaths = append([]string(nil), request.Launch.AllowedPaths...)
			candidate.Launch.Team.Worker = &ControlRoleTarget{Adapter: request.Launch.Team.Worker.Adapter, Model: request.Launch.Team.Worker.Model}
			candidate.Launch.Team.Supervisor = &ControlRoleTarget{Adapter: request.Launch.Team.Supervisor.Adapter, Model: request.Launch.Team.Supervisor.Model}
			test.edit(&candidate)
			digest, err := ControlStartActionDigest(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if digest == baseline {
				t.Fatalf("start digest did not bind %s", test.name)
			}
		})
	}
}

func TestControlApprovalValidatorsRejectMismatchedAndExpiredActions(t *testing.T) {
	now := time.Now().UTC()
	valid := ControlApproval{ActionDigest: "expected", ApprovedAt: now.Add(-time.Second), ExpiresAt: now.Add(time.Minute), Nonce: "approved-once"}
	tests := []struct {
		name string
		edit func(*ControlApproval)
	}{
		{name: "action mismatch", edit: func(approval *ControlApproval) { approval.ActionDigest = "other" }},
		{name: "missing nonce", edit: func(approval *ControlApproval) { approval.Nonce = "" }},
		{name: "expired", edit: func(approval *ControlApproval) { approval.ExpiresAt = now.Add(-time.Second) }},
		{name: "lifetime exceeded", edit: func(approval *ControlApproval) {
			approval.ExpiresAt = approval.ApprovedAt.Add(ControlApprovalMaxLifetime + time.Second)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			approval := valid
			test.edit(&approval)
			if err := validateControlApproval(approval, "expected"); err == nil {
				t.Fatal("start approval was accepted")
			}
			if err := validateControlResumeApproval(approval, "expected"); err == nil {
				t.Fatal("resume approval was accepted")
			}
			if err := validateControlCancelApproval(approval, "expected"); err == nil {
				t.Fatal("cancel approval was accepted")
			}
		})
	}
}

func TestPrepareControlStartReturnsTheBoundStartDigest(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	request := ControlStartRequest{SchemaVersion: ControlContractVersion, Launch: controlLaunchFixture(t, repo), IdempotencyKey: "session-1-generation-1"}
	prepared, err := PrepareControlStart(request)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := ControlStartActionDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.ActionDigest != expected || prepared.ApprovalMaxLifetimeSeconds != int64(ControlApprovalMaxLifetime/time.Second) {
		t.Fatalf("prepared start = %#v", prepared)
	}
}

func TestControlServiceBindsLaunchPreparationToItsWorktree(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	otherRepo, _ := createResumeFixtureRepo(t)
	service := ControlService{RepositoryRoot: repo, ProducerVersion: "test"}
	if _, err := service.ValidateLaunch(controlLaunchFixture(t, otherRepo)); err == nil || !strings.Contains(err.Error(), "must match the MCP server worktree") {
		t.Fatalf("launch validation error = %v", err)
	}
	request := ControlStartRequest{SchemaVersion: ControlContractVersion, Launch: controlLaunchFixture(t, otherRepo), IdempotencyKey: "session-1-generation-1"}
	if _, err := service.PrepareStart(request); err == nil || !strings.Contains(err.Error(), "must match the MCP server worktree") {
		t.Fatalf("start preparation error = %v", err)
	}
}

func TestNormalizeControlLaunchRejectsTraversalAndRoleConfusion(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	spec := controlLaunchFixture(t, repo)
	spec.AllowedPaths = []string{"../outside"}
	if _, err := NormalizeControlLaunch(spec); err == nil || !strings.Contains(err.Error(), "parent traversal") {
		t.Fatalf("traversal error = %v", err)
	}
	spec = controlLaunchFixture(t, repo)
	spec.Team.Reviewer = &ControlRoleTarget{Adapter: "codex"}
	if _, err := NormalizeControlLaunch(spec); err == nil || !strings.Contains(err.Error(), "does not allow role reviewer") {
		t.Fatalf("role error = %v", err)
	}
	spec = controlLaunchFixture(t, repo)
	spec.Repository.RepoID = "caller-controlled"
	if _, err := NormalizeControlLaunch(spec); err == nil || !strings.Contains(err.Error(), "repo_id does not match") {
		t.Fatalf("repo identity error = %v", err)
	}
}

func TestControlStatusUsesAuthoritativeSnapshotAndBoundsContent(t *testing.T) {
	service, runID, runDir := controlServiceFixture(t)
	changed := make([]string, controlMaxChangedFiles+1)
	for i := range changed {
		changed[i] = filepath.ToSlash(filepath.Join("internal", strings.Repeat("x", 10)))
	}
	state := RunState{SchemaVersion: runStateSchemaVersion, RunID: runID, Mode: ModeSupervisor, Status: "running", Phase: string(PhaseImplementing), UpdatedAt: time.Now().UTC(), RoleStatuses: map[string]RoleStatus{"worker": {Role: "worker", Status: "running", Message: strings.Repeat("m", controlMaxTextBytes+1)}}}
	if err := writeJSONWithNewline(filepath.Join(runDir, "state.json"), state); err != nil {
		t.Fatal(err)
	}
	final := FinalRun{SchemaVersion: ArtifactSchemaVersion, RunID: runID, RunDir: runDir, Mode: ModeSupervisor, Status: RunStatusPassed, Verdict: "pass", ChangedFiles: changed, FinishedAt: time.Now().UTC()}
	if err := writeJSONWithNewline(filepath.Join(runDir, "final.json"), final); err != nil {
		t.Fatal(err)
	}
	status, err := service.Status(runID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Run.Status != string(RunStatusPassed) || len(status.Run.ChangedFiles) != controlMaxChangedFiles || !status.Truncated || len(status.SnapshotID) != 64 {
		t.Fatalf("status = %#v", status)
	}
	if _, err := service.Status("../escape"); err == nil || !strings.Contains(err.Error(), "invalid run id") {
		t.Fatalf("invalid run error = %v", err)
	}
}

func TestControlPlanAndFindingsArePaginatedAndBounded(t *testing.T) {
	service, runID, runDir := controlServiceFixture(t)
	plan := ExecutionPlan{SchemaVersion: ArtifactSchemaVersion, RunID: runID, Status: "running", Items: []PlanItem{{ID: "P1", Title: "inspect", Status: PlanStatusPassed}, {ID: "P2", Title: "repair", Status: PlanStatusPending}}}
	if err := writeJSONWithNewline(filepath.Join(runDir, "plan.json"), plan); err != nil {
		t.Fatal(err)
	}
	ledger := FindingsLedger{SchemaVersion: ArtifactSchemaVersion, RunID: runID, Entries: []FindingEntry{{ID: "F1", Severity: "major", Status: "open", Issue: strings.Repeat("i", controlMaxTextBytes+1)}, {ID: "F2", Severity: "minor", Status: "closed", Issue: "fixed"}}}
	if err := writeJSONWithNewline(filepath.Join(runDir, findingsLedgerFilename), ledger); err != nil {
		t.Fatal(err)
	}
	first, err := service.Plan(runID, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Plan(runID, first.NextCursor, 1)
	if err != nil {
		t.Fatal(err)
	}
	if first.Items[0].ID != "P1" || second.Items[0].ID != "P2" || second.NextCursor != "" {
		t.Fatalf("plan pages = %#v %#v", first, second)
	}
	findings, err := service.Findings(runID, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings.Items) != 2 || !findings.Truncated || !strings.HasSuffix(findings.Items[0].Issue, "...[truncated]") {
		t.Fatalf("findings = %#v", findings)
	}
	if _, err := service.Plan(runID, "bad", 1); err == nil || err.Error() != "invalid page cursor" {
		t.Fatalf("cursor error = %v", err)
	}
}

func TestControlDiagnosticsIsReadOnly(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	service := ControlService{RepositoryRoot: repo, StateRoot: filepath.Join(t.TempDir(), "state"), ProducerVersion: "test"}
	diagnostics, err := service.Diagnostics()
	if err != nil {
		t.Fatal(err)
	}
	if diagnostics.Status != "ready" || diagnostics.Repository.RepoID == "" || diagnostics.Completeness != ControlComplete {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
	if _, err := os.Stat(filepath.Join(repo, ".tagteam")); !os.IsNotExist(err) {
		t.Fatalf("diagnostics created repository runtime state: %v", err)
	}
}

func TestControlServiceResolvesInternalSymlinksAndRejectsEscapes(t *testing.T) {
	service, runID, runDir := controlServiceFixture(t)
	realPlan := filepath.Join(runDir, "plan-real.json")
	plan := ExecutionPlan{SchemaVersion: ArtifactSchemaVersion, RunID: runID, Status: "running", Items: []PlanItem{{ID: "P1", Title: "inspect", Status: PlanStatusPending}}}
	if err := writeJSONWithNewline(realPlan, plan); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realPlan, filepath.Join(runDir, "plan.json")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	page, err := service.Plan(runID, "", 10)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("internal symlink plan = %#v, err=%v", page, err)
	}

	if err := os.Remove(filepath.Join(runDir, "plan.json")); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside-plan.json")
	if err := writeJSONWithNewline(outside, plan); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(runDir, "plan.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Plan(runID, "", 10); err == nil || !strings.Contains(err.Error(), "escapes the canonical run directory") {
		t.Fatalf("escaping symlink error = %v", err)
	}
}
