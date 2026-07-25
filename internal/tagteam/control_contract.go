package tagteam

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	ControlContractVersion = 1
	controlMaxPromptBytes  = 128 * 1024
	controlMaxRoleBytes    = 256
	controlMaxAllowedPaths = 128
	controlMaxRounds       = 20
	controlMaxTimeout      = 24 * time.Hour
	controlMaxPageSize     = 100
	controlMaxChangedFiles = 128
	controlMaxTextBytes    = 4096
	// ControlApprovalMaxLifetime keeps MCP approval records short-lived while
	// allowing a host to collect an explicit confirmation after preparation.
	ControlApprovalMaxLifetime = 30 * time.Minute
)

type ControlCompleteness string

const (
	ControlComplete ControlCompleteness = "complete"
	ControlPartial  ControlCompleteness = "partial"
)

// ControlRepository identifies the Git repository that owns a run. RepoID is
// derived by Tagteam and is never accepted as caller authority.
type ControlRepository struct {
	CanonicalRoot string `json:"canonical_root"`
	RepoID        string `json:"repo_id"`
}

type ControlRoleTarget struct {
	Adapter string `json:"adapter"`
	Model   string `json:"model,omitempty"`
}

type ControlTeamSpec struct {
	Mode       Mode               `json:"mode"`
	Worker     *ControlRoleTarget `json:"worker,omitempty"`
	Coder      *ControlRoleTarget `json:"coder,omitempty"`
	Supervisor *ControlRoleTarget `json:"supervisor,omitempty"`
	Reviewer   *ControlRoleTarget `json:"reviewer,omitempty"`
	Scout      *ControlRoleTarget `json:"scout,omitempty"`
}

type ControlTimeBudget struct {
	InvocationTimeoutSeconds int64 `json:"invocation_timeout_seconds"`
	WatchdogTimeoutSeconds   int64 `json:"watchdog_timeout_seconds"`
	WallTimeoutSeconds       int64 `json:"wall_timeout_seconds"`
}

type ControlLaunchSpec struct {
	SchemaVersion  int               `json:"schema_version"`
	Repository     ControlRepository `json:"repository"`
	Prompt         string            `json:"prompt"`
	Team           ControlTeamSpec   `json:"team"`
	AllowedPaths   []string          `json:"allowed_paths"`
	Rounds         int               `json:"rounds"`
	TimeBudget     ControlTimeBudget `json:"time_budget"`
	TestPreset     string            `json:"test_preset,omitempty"`
	RecoveryPolicy string            `json:"recovery_policy"`
}

type ControlRunHandle struct {
	SchemaVersion   int    `json:"schema_version"`
	RunID           string `json:"run_id"`
	ProducerVersion string `json:"producer_version"`
}

type ControlLaunchValidation struct {
	SchemaVersion int               `json:"schema_version"`
	Normalized    ControlLaunchSpec `json:"normalized"`
	LaunchDigest  string            `json:"launch_digest"`
}

type ControlStartPreparation struct {
	SchemaVersion              int               `json:"schema_version"`
	Normalized                 ControlLaunchSpec `json:"normalized"`
	ActionDigest               string            `json:"action_digest"`
	ApprovalMaxLifetimeSeconds int64             `json:"approval_max_lifetime_seconds"`
}

type ControlStartRequest struct {
	SchemaVersion  int               `json:"schema_version"`
	Launch         ControlLaunchSpec `json:"launch"`
	IdempotencyKey string            `json:"idempotency_key"`
	Approval       ControlApproval   `json:"approval"`
}

type ControlResumeRequest struct {
	SchemaVersion int               `json:"schema_version"`
	Repository    ControlRepository `json:"repository"`
	RunID         string            `json:"run_id"`
	Approval      ControlApproval   `json:"approval"`
}

type ControlCancelRequest struct {
	SchemaVersion int               `json:"schema_version"`
	Repository    ControlRepository `json:"repository"`
	RunID         string            `json:"run_id"`
	Approval      ControlApproval   `json:"approval"`
}

type ControlApproval struct {
	ActionDigest string    `json:"action_digest"`
	ApprovedAt   time.Time `json:"approved_at"`
	ExpiresAt    time.Time `json:"expires_at"`
	Nonce        string    `json:"nonce"`
}

type ControlRecoveryAssessment struct {
	SchemaVersion int    `json:"schema_version"`
	RunID         string `json:"run_id"`
	Resumable     bool   `json:"resumable"`
	ReasonCode    string `json:"reason_code"`
	Reason        string `json:"reason"`
	ActionDigest  string `json:"action_digest,omitempty"`
}

type ControlCapabilitySet struct {
	SchemaVersion   int      `json:"schema_version"`
	ProducerVersion string   `json:"producer_version"`
	Capabilities    []string `json:"capabilities"`
}

type ControlStatus struct {
	SchemaVersion int                 `json:"schema_version"`
	SnapshotID    string              `json:"snapshot_id"`
	Completeness  ControlCompleteness `json:"completeness"`
	Truncated     bool                `json:"truncated"`
	Run           RunSnapshot         `json:"run"`
}

type ControlPage[T any] struct {
	SchemaVersion int                 `json:"schema_version"`
	Items         []T                 `json:"items"`
	NextCursor    string              `json:"next_cursor,omitempty"`
	Completeness  ControlCompleteness `json:"completeness"`
	Truncated     bool                `json:"truncated"`
}

type ControlPlanItem struct {
	ID     string     `json:"id"`
	Title  string     `json:"title"`
	Status PlanStatus `json:"status"`
	Owner  string     `json:"owner,omitempty"`
	Reason string     `json:"reason,omitempty"`
}

type ControlFinding struct {
	ID       string `json:"id"`
	Source   string `json:"source"`
	Severity string `json:"severity"`
	Status   string `json:"status"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Issue    string `json:"issue"`
	Fix      string `json:"fix,omitempty"`
}

type ControlDiagnostics struct {
	SchemaVersion int                 `json:"schema_version"`
	Status        string              `json:"status"`
	Repository    ControlRepository   `json:"repository"`
	StateRoot     string              `json:"state_root"`
	Details       []string            `json:"details"`
	Completeness  ControlCompleteness `json:"completeness"`
}

type ControlService struct {
	RepositoryRoot  string
	StateRoot       string
	ProducerVersion string
}

func (s ControlService) Capabilities() ControlCapabilitySet {
	return ControlCapabilitySet{
		SchemaVersion:   ControlContractVersion,
		ProducerVersion: normalizedProducerVersion(s.ProducerVersion),
		Capabilities:    []string{"capabilities", "validate_launch", "prepare_start", "prepare_resume", "status", "plan", "findings", "diagnostics"},
	}
}

func (s ControlService) ValidateLaunch(spec ControlLaunchSpec) (ControlLaunchValidation, error) {
	normalized, err := NormalizeControlLaunch(spec)
	if err != nil {
		return ControlLaunchValidation{}, err
	}
	if err := s.requireRepository(normalized.Repository); err != nil {
		return ControlLaunchValidation{}, err
	}
	digest, err := ControlActionDigest(normalized)
	if err != nil {
		return ControlLaunchValidation{}, err
	}
	return ControlLaunchValidation{SchemaVersion: ControlContractVersion, Normalized: normalized, LaunchDigest: digest}, nil
}

func PrepareControlStart(request ControlStartRequest) (ControlStartPreparation, error) {
	if request.SchemaVersion != ControlContractVersion {
		return ControlStartPreparation{}, fmt.Errorf("unsupported control schema_version %d (want %d)", request.SchemaVersion, ControlContractVersion)
	}
	normalized, err := NormalizeControlLaunch(request.Launch)
	if err != nil {
		return ControlStartPreparation{}, err
	}
	request.Launch = normalized
	digest, err := ControlStartActionDigest(request)
	if err != nil {
		return ControlStartPreparation{}, err
	}
	return ControlStartPreparation{SchemaVersion: ControlContractVersion, Normalized: normalized, ActionDigest: digest, ApprovalMaxLifetimeSeconds: int64(ControlApprovalMaxLifetime / time.Second)}, nil
}

// PrepareStart validates a start request for this server's configured
// worktree. The standalone function above remains useful to local callers
// that need the transport-independent digest contract.
func (s ControlService) PrepareStart(request ControlStartRequest) (ControlStartPreparation, error) {
	prepared, err := PrepareControlStart(request)
	if err != nil {
		return ControlStartPreparation{}, err
	}
	if err := s.requireRepository(prepared.Normalized.Repository); err != nil {
		return ControlStartPreparation{}, err
	}
	return prepared, nil
}

// NormalizeControlLaunch validates caller input and returns the canonical
// action whose JSON representation is suitable for approval binding.
func NormalizeControlLaunch(spec ControlLaunchSpec) (ControlLaunchSpec, error) {
	if spec.SchemaVersion != ControlContractVersion {
		return ControlLaunchSpec{}, fmt.Errorf("unsupported control schema_version %d (want %d)", spec.SchemaVersion, ControlContractVersion)
	}
	if len(spec.Prompt) == 0 || len(spec.Prompt) > controlMaxPromptBytes || strings.TrimSpace(spec.Prompt) == "" {
		return ControlLaunchSpec{}, fmt.Errorf("prompt must be non-empty and at most %d bytes", controlMaxPromptBytes)
	}
	repository, err := resolveControlRepository(spec.Repository.CanonicalRoot)
	if err != nil {
		return ControlLaunchSpec{}, err
	}
	if spec.Repository.RepoID != "" && spec.Repository.RepoID != repository.RepoID {
		return ControlLaunchSpec{}, fmt.Errorf("repository repo_id does not match canonical_root")
	}
	if err := validateControlTeam(spec.Team); err != nil {
		return ControlLaunchSpec{}, err
	}
	allowed, err := canonicalizeControlAllowedPaths(repository.CanonicalRoot, spec.AllowedPaths)
	if err != nil {
		return ControlLaunchSpec{}, err
	}
	if spec.Rounds < 1 || spec.Rounds > controlMaxRounds {
		return ControlLaunchSpec{}, fmt.Errorf("rounds must be between 1 and %d", controlMaxRounds)
	}
	if err := validateControlTimeBudget(spec.TimeBudget); err != nil {
		return ControlLaunchSpec{}, err
	}
	if strings.TrimSpace(spec.TestPreset) != spec.TestPreset || len(spec.TestPreset) > controlMaxRoleBytes || containsControl(spec.TestPreset) {
		return ControlLaunchSpec{}, fmt.Errorf("test_preset must be a normalized identifier no longer than %d bytes", controlMaxRoleBytes)
	}
	if spec.RecoveryPolicy != "assist" {
		return ControlLaunchSpec{}, fmt.Errorf("recovery_policy must be %q", "assist")
	}
	spec.Repository = repository
	spec.AllowedPaths = allowed
	return spec, nil
}

func ControlActionDigest(spec ControlLaunchSpec) (string, error) {
	normalized, err := NormalizeControlLaunch(spec)
	if err != nil {
		return "", err
	}
	return digestControlValue(normalized)
}

func ControlStartActionDigest(request ControlStartRequest) (string, error) {
	if request.SchemaVersion != ControlContractVersion {
		return "", fmt.Errorf("unsupported control schema_version %d (want %d)", request.SchemaVersion, ControlContractVersion)
	}
	if request.IdempotencyKey == "" || strings.TrimSpace(request.IdempotencyKey) != request.IdempotencyKey || len(request.IdempotencyKey) > controlMaxRoleBytes || containsControl(request.IdempotencyKey) {
		return "", fmt.Errorf("idempotency_key must be a normalized identifier no longer than %d bytes", controlMaxRoleBytes)
	}
	normalized, err := NormalizeControlLaunch(request.Launch)
	if err != nil {
		return "", err
	}
	return digestControlValue(struct {
		SchemaVersion  int               `json:"schema_version"`
		Operation      string            `json:"operation"`
		Launch         ControlLaunchSpec `json:"launch"`
		IdempotencyKey string            `json:"idempotency_key"`
	}{ControlContractVersion, "start", normalized, request.IdempotencyKey})
}

func ControlResumeActionDigest(request ControlResumeRequest) (string, error) {
	return digestControlRunAction("resume", request.SchemaVersion, request.Repository, request.RunID)
}

func ControlCancelActionDigest(request ControlCancelRequest) (string, error) {
	return digestControlRunAction("cancel", request.SchemaVersion, request.Repository, request.RunID)
}

func digestControlRunAction(operation string, schemaVersion int, repository ControlRepository, runID string) (string, error) {
	if schemaVersion != ControlContractVersion {
		return "", fmt.Errorf("unsupported control schema_version %d (want %d)", schemaVersion, ControlContractVersion)
	}
	if err := validateRunID(runID); err != nil {
		return "", err
	}
	normalizedRepository, err := resolveControlRepository(repository.CanonicalRoot)
	if err != nil {
		return "", err
	}
	if repository.RepoID != "" && repository.RepoID != normalizedRepository.RepoID {
		return "", fmt.Errorf("repository repo_id does not match canonical_root")
	}
	return digestControlValue(struct {
		SchemaVersion int               `json:"schema_version"`
		Operation     string            `json:"operation"`
		Repository    ControlRepository `json:"repository"`
		RunID         string            `json:"run_id"`
	}{ControlContractVersion, operation, normalizedRepository, runID})
}

func digestControlValue(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode normalized control action: %w", err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func (s ControlService) Status(runID string) (ControlStatus, error) {
	runDir, err := s.runDir(runID)
	if err != nil {
		return ControlStatus{}, err
	}
	if err := validateControlRunArtifacts(runDir); err != nil {
		return ControlStatus{}, err
	}
	snapshot, err := BuildRunSnapshot(s.RepositoryRoot, runDir)
	if err != nil {
		return ControlStatus{}, fmt.Errorf("read run status: %w", err)
	}
	bounded, truncated := boundControlSnapshot(snapshot)
	payload, err := json.Marshal(bounded)
	if err != nil {
		return ControlStatus{}, fmt.Errorf("encode run status: %w", err)
	}
	digest := sha256.Sum256(payload)
	completeness := ControlComplete
	if bounded.Status == "" || bounded.UpdatedAt.IsZero() {
		completeness = ControlPartial
	}
	return ControlStatus{SchemaVersion: ControlContractVersion, SnapshotID: hex.EncodeToString(digest[:]), Completeness: completeness, Truncated: truncated, Run: bounded}, nil
}

func (s ControlService) Plan(runID, cursor string, limit int) (ControlPage[ControlPlanItem], error) {
	runDir, err := s.runDir(runID)
	if err != nil {
		return ControlPage[ControlPlanItem]{}, err
	}
	if err := validateControlArtifact(runDir, "plan.json"); err != nil {
		return ControlPage[ControlPlanItem]{}, err
	}
	plan, err := readExecutionPlan(runDir)
	if err != nil {
		return ControlPage[ControlPlanItem]{}, fmt.Errorf("read run plan: %w", err)
	}
	items := make([]ControlPlanItem, 0, len(plan.Items))
	truncated := false
	for _, item := range plan.Items {
		projected := ControlPlanItem{ID: boundControlText(item.ID), Title: boundControlText(item.Title), Status: item.Status, Owner: boundControlText(item.Owner), Reason: boundControlText(item.Reason)}
		truncated = truncated || projected.ID != item.ID || projected.Title != item.Title || projected.Owner != item.Owner || projected.Reason != item.Reason
		items = append(items, projected)
	}
	return controlPage(items, cursor, limit, truncated)
}

func (s ControlService) Findings(runID, cursor string, limit int) (ControlPage[ControlFinding], error) {
	runDir, err := s.runDir(runID)
	if err != nil {
		return ControlPage[ControlFinding]{}, err
	}
	if err := validateControlArtifact(runDir, findingsLedgerFilename); err != nil {
		return ControlPage[ControlFinding]{}, err
	}
	ledger, err := loadFindingsLedger(runDir)
	if err != nil {
		return ControlPage[ControlFinding]{}, fmt.Errorf("read run findings: %w", err)
	}
	items := make([]ControlFinding, 0, len(ledger.Entries))
	truncated := false
	for _, finding := range ledger.Entries {
		projected := ControlFinding{ID: boundControlText(finding.ID), Source: boundControlText(finding.Source), Severity: boundControlText(finding.Severity), Status: boundControlText(finding.Status), File: boundControlText(finding.File), Line: finding.Line, Issue: boundControlText(finding.Issue), Fix: boundControlText(finding.Fix)}
		truncated = truncated || projected.ID != finding.ID || projected.Source != finding.Source || projected.Severity != finding.Severity || projected.Status != finding.Status || projected.File != finding.File || projected.Issue != finding.Issue || projected.Fix != finding.Fix
		items = append(items, projected)
	}
	return controlPage(items, cursor, limit, truncated)
}

func (s ControlService) Diagnostics() (ControlDiagnostics, error) {
	repository, err := resolveControlRepository(s.RepositoryRoot)
	if err != nil {
		return ControlDiagnostics{}, err
	}
	locator, err := resolveStateLocator(repository.CanonicalRoot, s.StateRoot)
	if err != nil {
		return ControlDiagnostics{}, fmt.Errorf("resolve state root: %w", err)
	}
	return ControlDiagnostics{SchemaVersion: ControlContractVersion, Status: "ready", Repository: repository, StateRoot: locator.StateRoot, Details: []string{"repository identity verified", "state root resolved", "approved start, resume, and cancel require an enabled runtime"}, Completeness: ControlComplete}, nil
}

func (s ControlService) runDir(runID string) (string, error) {
	runDir, _, err := resolveControlRunDirectory(s.RepositoryRoot, s.StateRoot, runID)
	return runDir, err
}

func validateControlRunArtifacts(runDir string) error {
	for _, name := range []string{"state.json", "final.json", "plan.json", findingsLedgerFilename, liveProgressArtifact, hostActivityArtifact, preexistingWorktreeArtifact} {
		path := filepath.Join(runDir, name)
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			continue
		}
		if err := validateControlArtifact(runDir, name); err != nil {
			return err
		}
	}
	return nil
}

func validateControlArtifact(runDir, name string) error {
	path := filepath.Join(runDir, name)
	root, err := canonicalPath(runDir, true)
	if err != nil {
		return fmt.Errorf("resolve control run directory: %w", err)
	}
	realPath, err := canonicalPath(path, true)
	if err != nil {
		return fmt.Errorf("resolve control artifact %s: %w", name, err)
	}
	if !pathWithin(root, realPath) {
		return fmt.Errorf("control artifact %s escapes the canonical run directory", name)
	}
	info, err := os.Stat(realPath)
	if err != nil {
		return fmt.Errorf("inspect control artifact %s: %w", name, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("control artifact %s must resolve to a regular file", name)
	}
	return nil
}

// requireRepository keeps an MCP server scoped to the worktree it was started
// for. Without this check a returned handle could be unqueryable by the same
// server after a caller supplied a different repository root.
func (s ControlService) requireRepository(repository ControlRepository) error {
	expected, err := resolveControlRepository(s.RepositoryRoot)
	if err != nil {
		return err
	}
	if repository.CanonicalRoot != expected.CanonicalRoot || repository.RepoID != expected.RepoID {
		return fmt.Errorf("control repository must match the MCP server worktree")
	}
	return nil
}

func validateControlTeam(team ControlTeamSpec) error {
	roles := []struct {
		name   string
		target *ControlRoleTarget
	}{{"worker", team.Worker}, {"coder", team.Coder}, {"supervisor", team.Supervisor}, {"reviewer", team.Reviewer}, {"scout", team.Scout}}
	for _, role := range roles {
		name, target := role.name, role.target
		if target == nil {
			continue
		}
		if strings.TrimSpace(target.Adapter) != target.Adapter || target.Adapter == "" || len(target.Adapter) > controlMaxRoleBytes || containsControl(target.Adapter) || strings.TrimSpace(target.Model) != target.Model || len(target.Model) > controlMaxRoleBytes || containsControl(target.Model) {
			return fmt.Errorf("team role %s has an invalid adapter or model identifier", name)
		}
	}
	require := func(name string, target *ControlRoleTarget) error {
		if target == nil {
			return fmt.Errorf("%s mode requires role %s", team.Mode, name)
		}
		return nil
	}
	forbid := func(name string, target *ControlRoleTarget) error {
		if target != nil {
			return fmt.Errorf("%s mode does not allow role %s", team.Mode, name)
		}
		return nil
	}
	var checks []error
	switch team.Mode {
	case ModeSupervisor:
		checks = []error{require("worker", team.Worker), require("supervisor", team.Supervisor), forbid("coder", team.Coder), forbid("reviewer", team.Reviewer), forbid("scout", team.Scout)}
	case ModeRelay:
		checks = []error{require("coder", team.Coder), require("supervisor", team.Supervisor), require("scout", team.Scout), forbid("worker", team.Worker), forbid("reviewer", team.Reviewer)}
	case ModeAdversarial:
		checks = []error{require("coder", team.Coder), require("reviewer", team.Reviewer), forbid("worker", team.Worker), forbid("supervisor", team.Supervisor), forbid("scout", team.Scout)}
	case ModeSolo:
		checks = []error{require("worker", team.Worker), forbid("coder", team.Coder), forbid("supervisor", team.Supervisor), forbid("reviewer", team.Reviewer), forbid("scout", team.Scout)}
	default:
		return fmt.Errorf("invalid control team mode %q", team.Mode)
	}
	for _, err := range checks {
		if err != nil {
			return err
		}
	}
	return nil
}

func validateControlTimeBudget(budget ControlTimeBudget) error {
	values := []struct {
		name    string
		seconds int64
	}{{"invocation_timeout_seconds", budget.InvocationTimeoutSeconds}, {"watchdog_timeout_seconds", budget.WatchdogTimeoutSeconds}, {"wall_timeout_seconds", budget.WallTimeoutSeconds}}
	for _, value := range values {
		if value.seconds <= 0 || value.seconds > int64(controlMaxTimeout/time.Second) {
			return fmt.Errorf("%s must be between 1 and %d", value.name, int64(controlMaxTimeout/time.Second))
		}
	}
	return nil
}

func containsControl(value string) bool {
	return strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0
}

func boundControlSnapshot(snapshot RunSnapshot) (RunSnapshot, bool) {
	truncated := false
	bound := func(value *string) {
		bounded := boundControlText(*value)
		truncated = truncated || bounded != *value
		*value = bounded
	}
	bound(&snapshot.RunID)
	bound(&snapshot.RunDir)
	bound(&snapshot.Status)
	bound(&snapshot.Phase)
	bound(&snapshot.RepoID)
	bound(&snapshot.StateRoot)
	bound(&snapshot.InvocationID)
	bound(&snapshot.DiffHash)
	bound(&snapshot.RecoveryStatus)
	bound(&snapshot.Verdict)
	bound(&snapshot.DegradedReason)
	bound(&snapshot.BlockingReason)
	bound(&snapshot.LatestDiffPath)
	bound(&snapshot.LatestReviewPath)
	bound(&snapshot.LatestTestPath)
	mode := string(snapshot.Mode)
	bound(&mode)
	snapshot.Mode = Mode(mode)
	completedPhase := string(snapshot.CompletedPhase)
	bound(&completedPhase)
	snapshot.CompletedPhase = RunPhase(completedPhase)
	roleKeys := make([]string, 0, len(snapshot.RoleStatuses))
	for role := range snapshot.RoleStatuses {
		roleKeys = append(roleKeys, role)
	}
	sort.Strings(roleKeys)
	if len(roleKeys) > 16 {
		roleKeys = roleKeys[:16]
		truncated = true
	}
	roleStatuses := make(map[string]RoleStatus, len(roleKeys))
	for _, role := range roleKeys {
		roleStatuses[boundControlText(role)] = snapshot.RoleStatuses[role]
		truncated = truncated || boundControlText(role) != role
	}
	snapshot.RoleStatuses = roleStatuses
	snapshot.ChangedFiles, truncated = boundControlTextList(snapshot.ChangedFiles, controlMaxChangedFiles, truncated)
	snapshot.PreexistingFiles, truncated = boundControlTextList(snapshot.PreexistingFiles, controlMaxChangedFiles, truncated)
	if len(snapshot.QualityGateBlockers) > controlMaxChangedFiles {
		snapshot.QualityGateBlockers = snapshot.QualityGateBlockers[:controlMaxChangedFiles]
		truncated = true
	}
	gateBlockers := make([]GateFinding, len(snapshot.QualityGateBlockers))
	for i, finding := range snapshot.QualityGateBlockers {
		bound(&finding.ID)
		bound(&finding.Gate)
		bound(&finding.Severity)
		bound(&finding.Message)
		bound(&finding.Path)
		gateBlockers[i] = finding
	}
	snapshot.QualityGateBlockers = gateBlockers
	for role, status := range snapshot.RoleStatuses {
		bound(&status.Role)
		bound(&status.Adapter)
		bound(&status.Model)
		bound(&status.Status)
		reasonCode := string(status.ReasonCode)
		bound(&reasonCode)
		status.ReasonCode = ReasonCode(reasonCode)
		bound(&status.Message)
		bound(&status.Selected)
		status.Attempts, truncated = boundControlTextList(status.Attempts, controlMaxChangedFiles, truncated)
		snapshot.RoleStatuses[role] = status
	}
	if snapshot.LiveProgress != nil {
		live := *snapshot.LiveProgress
		bound(&live.InvocationID)
		bound(&live.Phase)
		role := string(live.Role)
		bound(&role)
		live.Role = Role(role)
		bound(&live.Status)
		bound(&live.WaitingFor)
		bound(&live.Elapsed)
		bound(&live.DiffHash)
		bound(&live.NoProgressFor)
		live.ChangedFiles, truncated = boundControlTextList(live.ChangedFiles, controlMaxChangedFiles, truncated)
		snapshot.LiveProgress = &live
	}
	if snapshot.HostActivity != nil {
		activity := *snapshot.HostActivity
		bound(&activity.Actor)
		bound(&activity.Phase)
		bound(&activity.Status)
		bound(&activity.Command)
		bound(&activity.OutputPath)
		bound(&activity.Elapsed)
		bound(&activity.Error)
		activity.ChangedFiles, truncated = boundControlTextList(activity.ChangedFiles, controlMaxChangedFiles, truncated)
		snapshot.HostActivity = &activity
	}
	if snapshot.PlanSummary != nil {
		plan := *snapshot.PlanSummary
		bound(&plan.Path)
		bound(&plan.Status)
		snapshot.PlanSummary = &plan
	}
	return snapshot, truncated
}

func boundControlTextList(values []string, limit int, truncated bool) ([]string, bool) {
	if len(values) > limit {
		values = values[:limit]
		truncated = true
	}
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = boundControlText(value)
		truncated = truncated || result[i] != value
	}
	return result, truncated
}

func boundControlText(value string) string {
	if len(value) <= controlMaxTextBytes {
		return value
	}
	end := controlMaxTextBytes
	for end > 0 && !utf8Boundary(value, end) {
		end--
	}
	return value[:end] + "...[truncated]"
}

func utf8Boundary(value string, index int) bool {
	return index == len(value) || index == 0 || value[index]&0xc0 != 0x80
}

func controlPage[T any](items []T, cursor string, limit int, contentTruncated bool) (ControlPage[T], error) {
	if limit <= 0 || limit > controlMaxPageSize {
		return ControlPage[T]{}, fmt.Errorf("limit must be between 1 and %d", controlMaxPageSize)
	}
	start := 0
	if cursor != "" {
		parsed, err := strconv.Atoi(cursor)
		if err != nil || parsed < 0 || parsed > len(items) {
			return ControlPage[T]{}, fmt.Errorf("invalid page cursor")
		}
		start = parsed
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	next := ""
	if end < len(items) {
		next = strconv.Itoa(end)
	}
	return ControlPage[T]{SchemaVersion: ControlContractVersion, Items: append([]T(nil), items[start:end]...), NextCursor: next, Completeness: ControlComplete, Truncated: contentTruncated}, nil
}

func normalizedProducerVersion(version string) string {
	if strings.TrimSpace(version) == "" {
		return "dev"
	}
	return strings.TrimSpace(version)
}
