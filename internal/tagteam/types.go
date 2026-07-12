package tagteam

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Role string

const (
	RoleCoder      Role = "coder"
	RoleAdversary  Role = "adversary"
	RoleSupervisor Role = "supervisor"
	RoleReporter   Role = "reporter"
	RoleScout      Role = "scout"
)

type Mode string

const (
	ModeSolo        Mode = "solo"
	ModeSupervisor  Mode = "supervisor"
	ModeAdversarial Mode = "adversarial"
	ModeRelay       Mode = "relay"
)

type RunStatus string

const (
	RunStatusRunning     RunStatus = "running"
	RunStatusPassed      RunStatus = "passed"
	RunStatusFailed      RunStatus = "failed"
	RunStatusDegraded    RunStatus = "degraded"
	RunStatusBlocked     RunStatus = "blocked"
	RunStatusCancelled   RunStatus = "cancelled"
	RunStatusQuarantined RunStatus = "quarantined"
)

type ReasonCode string

const (
	ReasonNone                  ReasonCode = ""
	ReasonScoutUnavailable      ReasonCode = "scout_unavailable"
	ReasonScoutContextTooSmall  ReasonCode = "scout_context_too_small"
	ReasonReviewerJSONInvalid   ReasonCode = "reviewer_json_invalid"
	ReasonReviewerUnavailable   ReasonCode = "reviewer_unavailable"
	ReasonSupervisorUnavailable ReasonCode = "supervisor_unavailable"
	ReasonWorkerTimeout         ReasonCode = "worker_timeout"
	ReasonWorkerUnavailable     ReasonCode = "worker_unavailable"
	ReasonWorkerOutputInvalid   ReasonCode = "worker_output_invalid"
	ReasonBlockingFindings      ReasonCode = "blocking_findings"
	ReasonTestFailed            ReasonCode = "test_failed"
	ReasonRoundsExhausted       ReasonCode = "rounds_exhausted"
	ReasonArtifactMissing       ReasonCode = "artifact_missing"
	ReasonFallbackUsed          ReasonCode = "fallback_used"
	ReasonBudgetExceeded        ReasonCode = "budget_exceeded"
	ReasonJSONRepairUsed        ReasonCode = "json_repair_used"
	ReasonCancelled             ReasonCode = "cancelled"
	ReasonStalled               ReasonCode = "stalled"
	ReasonQuarantined           ReasonCode = "quarantined"
)

type LossPolicy string

const (
	LossPolicyBlock              LossPolicy = "block"
	LossPolicyDegrade            LossPolicy = "degrade"
	LossPolicyReplaceThenBlock   LossPolicy = "replace_then_block"
	LossPolicyReplaceThenDegrade LossPolicy = "replace_then_degrade"
)

type RoleLossPolicies struct {
	Worker     LossPolicy `json:"worker,omitempty" toml:"worker"`
	Reviewer   LossPolicy `json:"reviewer,omitempty" toml:"reviewer"`
	Supervisor LossPolicy `json:"supervisor,omitempty" toml:"supervisor"`
	Scout      LossPolicy `json:"scout,omitempty" toml:"scout"`
}

type RoleFallbacks struct {
	Worker     []string `json:"worker,omitempty" toml:"worker"`
	Reviewer   []string `json:"reviewer,omitempty" toml:"reviewer"`
	Supervisor []string `json:"supervisor,omitempty" toml:"supervisor"`
	Scout      []string `json:"scout,omitempty" toml:"scout"`
}

type TargetFallbacks map[string][]string

type RoleStatus struct {
	Role          string     `json:"role"`
	Adapter       string     `json:"adapter,omitempty"`
	Model         string     `json:"model,omitempty"`
	Status        string     `json:"status"`
	ReasonCode    ReasonCode `json:"reason_code,omitempty"`
	Message       string     `json:"message,omitempty"`
	Attempts      []string   `json:"attempts,omitempty"`
	Selected      string     `json:"selected,omitempty"`
	LastUpdatedAt time.Time  `json:"last_updated_at"`
}

type RoleLossRecord struct {
	Role            string     `json:"role"`
	Policy          LossPolicy `json:"policy"`
	AttemptedAction string     `json:"attempted_action,omitempty"`
	Outcome         string     `json:"outcome"`
	ReasonCode      ReasonCode `json:"reason_code,omitempty"`
	Message         string     `json:"message,omitempty"`
}

type BudgetState struct {
	MaxRounds          int           `json:"max_rounds,omitempty"`
	MaxRoleInvocations int           `json:"max_role_invocations,omitempty"`
	MaxWallClock       time.Duration `json:"max_wall_clock,omitempty"`
	RoleInvocations    int           `json:"role_invocations"`
	Exhausted          bool          `json:"exhausted"`
	ReasonCode         ReasonCode    `json:"reason_code,omitempty"`
}

// ParseMode validates a raw --mode/config value, defaulting an empty value to
// ModeSupervisor.
func ParseMode(raw string) (Mode, error) {
	switch strings.TrimSpace(raw) {
	case "", string(ModeSupervisor):
		return ModeSupervisor, nil
	case string(ModeSolo):
		return ModeSolo, nil
	case string(ModeAdversarial):
		return ModeAdversarial, nil
	case string(ModeRelay):
		return ModeRelay, nil
	default:
		return "", fmt.Errorf("invalid mode %q (want %q, %q, %q, or %q)", raw, ModeSolo, ModeSupervisor, ModeAdversarial, ModeRelay)
	}
}

const (
	ArtifactSchemaVersion = 1

	ExitSuccess          = 0
	ExitBlockingFindings = 1
	ExitTestsFailed      = 2
	ExitAdapterFailure   = 3
	ExitInvalidArguments = 4
	ExitPreflightFailed  = 5
)

type ExitError struct {
	Code int
	Err  error
}

type OutputContractError struct {
	Err error
}

func (e *ExitError) Error() string {
	if e == nil || e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (e *ExitError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *OutputContractError) Error() string {
	if e == nil || e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (e *OutputContractError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func ExitCode(err error) int {
	if err == nil {
		return ExitSuccess
	}
	var exitErr *ExitError
	if errors.As(err, &exitErr) {
		return exitErr.Code
	}
	return ExitAdapterFailure
}

func IsOutputContractError(err error) bool {
	var contractErr *OutputContractError
	return errors.As(err, &contractErr)
}

type CapabilitySet struct {
	SupportsSchema bool
	SupportsResume bool
	SupportsStdin  bool
	// SerializeInvocations requests the cross-process invocation lock for
	// CLIs that misbehave when multiple copies run concurrently.
	SerializeInvocations bool
}

type VersionInfo struct {
	Found    bool   `json:"found"`
	Version  string `json:"version,omitempty"`
	Auth     string `json:"auth,omitempty"`
	Binary   string `json:"binary,omitempty"`
	Hint     string `json:"hint,omitempty"`
	Runnable bool   `json:"runnable"`
}

type Request struct {
	Context               context.Context
	Prompt                string
	SystemPrompt          string
	EnvOverlay            map[string]string
	Model                 string
	Workdir               string
	RunDir                string
	OutputPath            string
	SchemaPath            string
	Timeout               time.Duration
	WatchdogTimeout       time.Duration
	MaxOutputBytes        int64
	Passthrough           []string
	ResumeID              string
	Stdin                 []byte
	InputMode             string
	Phase                 string
	ProgressRole          Role
	Quiet                 bool
	Verbose               bool
	Budget                *InvocationBudget
	RequireWorkerContract bool
	InvocationID          string
	ProgressStdout        *invocationStream
	ProgressStderr        *invocationStream
	ProgressLastActivity  *time.Time
}

type InvocationBudget struct {
	Max  int
	Used int
}

type Adapter interface {
	ID() string
	Detect(ctx context.Context) (VersionInfo, error)
	Capabilities() CapabilitySet
	BuildCmd(role Role, req Request) (*CommandSpec, error)
	ParseResult(role Role, raw []byte) (Result, error)
}

type DirectAdapter interface {
	RunDirect(role Role, req Request) (Result, error)
}

type CommandSpec struct {
	Argv   []string `json:"argv"`
	Dir    string   `json:"dir"`
	Env    []string `json:"env,omitempty"`
	Stdin  []byte   `json:"-"`
	Output string   `json:"output"`
}

type Result struct {
	Text      string        `json:"text,omitempty"`
	Review    *Review       `json:"review,omitempty"`
	Scout     *Scout        `json:"scout,omitempty"`
	Worker    *WorkerResult `json:"worker,omitempty"`
	SessionID string        `json:"session_id,omitempty"`
	CostUSD   float64       `json:"cost_usd,omitempty"`
	Raw       []byte        `json:"-"`
	Command   []string      `json:"command,omitempty"`
}

type WorkPlan struct {
	SchemaVersion   int           `json:"schema_version,omitempty"`
	Summary         string        `json:"summary"`
	Packages        []WorkPackage `json:"packages"`
	SelectedPackage string        `json:"selected_package"`
	Defer           []string      `json:"defer,omitempty"`
}

type WorkPackage struct {
	ID               string   `json:"id"`
	Title            string   `json:"title"`
	Goal             string   `json:"goal"`
	EstimatedSeconds int      `json:"estimated_seconds"`
	AllowedScope     []string `json:"allowed_scope"`
	Acceptance       []string `json:"acceptance"`
	Validation       []string `json:"validation"`
}

type OrchestrationAdvisory struct {
	SchemaVersion  int    `json:"schema_version,omitempty"`
	Source         string `json:"source,omitempty"`
	Recommendation string `json:"recommendation"`
	TargetMode     Mode   `json:"target_mode,omitempty"`
	Reason         string `json:"reason"`
	Confidence     string `json:"confidence"`
}

type OrchestrationTransition struct {
	From   Mode   `json:"from"`
	To     Mode   `json:"to"`
	Reason string `json:"reason"`
}

type OrchestrationDecision struct {
	SchemaVersion           int                      `json:"schema_version"`
	RunID                   string                   `json:"run_id"`
	InitialMode             Mode                     `json:"initial_mode"`
	FinalMode               Mode                     `json:"final_mode"`
	Status                  string                   `json:"status"`
	Advisories              []OrchestrationAdvisory  `json:"advisories"`
	AppliedTransition       *OrchestrationTransition `json:"applied_transition,omitempty"`
	TransitionLimitConsumed bool                     `json:"transition_limit_consumed"`
	Degraded                bool                     `json:"degraded,omitempty"`
	DegradedReason          string                   `json:"degraded_reason,omitempty"`
	HostReason              string                   `json:"host_reason"`
}

type PlanStatus string

const (
	PlanStatusPending          PlanStatus = "pending"
	PlanStatusInProgress       PlanStatus = "in_progress"
	PlanStatusBlocked          PlanStatus = "blocked"
	PlanStatusPassed           PlanStatus = "passed"
	PlanStatusFailed           PlanStatus = "failed"
	PlanStatusSkipped          PlanStatus = "skipped"
	PlanStatusDeferred         PlanStatus = "deferred"
	PlanStatusNeedsArbitration PlanStatus = "needs_arbitration"
)

type ExecutionPlan struct {
	SchemaVersion int         `json:"schema_version"`
	RunID         string      `json:"run_id"`
	Mode          Mode        `json:"mode,omitempty"`
	Status        string      `json:"status"`
	Summary       string      `json:"summary,omitempty"`
	Items         []PlanItem  `json:"items"`
	Events        []PlanEvent `json:"events"`
	CreatedAt     time.Time   `json:"created_at"`
	UpdatedAt     time.Time   `json:"updated_at"`
}

type PlanItem struct {
	ID           string     `json:"id"`
	Title        string     `json:"title"`
	Status       PlanStatus `json:"status"`
	Owner        string     `json:"owner,omitempty"`
	Source       string     `json:"source"`
	Reason       string     `json:"reason,omitempty"`
	AllowedScope []string   `json:"allowed_scope,omitempty"`
	Acceptance   []string   `json:"acceptance,omitempty"`
	Validation   []string   `json:"validation,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

type PlanEvent struct {
	Type    string     `json:"type"`
	ItemID  string     `json:"item_id,omitempty"`
	By      string     `json:"by"`
	At      time.Time  `json:"at"`
	From    PlanStatus `json:"from,omitempty"`
	To      PlanStatus `json:"to,omitempty"`
	Message string     `json:"message,omitempty"`
}

type PlanSummary struct {
	Path        string `json:"path,omitempty"`
	Status      string `json:"status"`
	Total       int    `json:"total"`
	Pending     int    `json:"pending,omitempty"`
	InProgress  int    `json:"in_progress,omitempty"`
	Blocked     int    `json:"blocked,omitempty"`
	Passed      int    `json:"passed,omitempty"`
	Failed      int    `json:"failed,omitempty"`
	Skipped     int    `json:"skipped,omitempty"`
	Deferred    int    `json:"deferred,omitempty"`
	Arbitration int    `json:"needs_arbitration,omitempty"`
}

func (p WorkPlan) Selected() (WorkPackage, bool) {
	selected := strings.TrimSpace(p.SelectedPackage)
	for _, pkg := range p.Packages {
		if strings.TrimSpace(pkg.ID) == selected {
			return pkg, true
		}
	}
	if len(p.Packages) > 0 && selected == "" {
		return p.Packages[0], true
	}
	return WorkPackage{}, false
}

func (p WorkPlan) RemainingPackageTitles() []string {
	remaining := []string{}
	selected := strings.TrimSpace(p.SelectedPackage)
	for _, pkg := range p.Packages {
		if strings.TrimSpace(pkg.ID) == selected {
			continue
		}
		label := strings.TrimSpace(pkg.ID)
		if strings.TrimSpace(pkg.Title) != "" {
			if label != "" {
				label += ": "
			}
			label += strings.TrimSpace(pkg.Title)
		}
		if label != "" {
			remaining = append(remaining, label)
		}
	}
	return remaining
}

type Scout struct {
	SchemaVersion      int             `json:"schema_version,omitempty"`
	Mode               string          `json:"mode,omitempty"`
	Summary            string          `json:"summary,omitempty"`
	RelevantFiles      []string        `json:"relevant_files"`
	LikelyEntryPoints  []string        `json:"likely_entry_points"`
	ExistingPatterns   []string        `json:"existing_patterns"`
	Risks              []string        `json:"risks"`
	SuggestedTests     []string        `json:"suggested_tests"`
	RetrievalQueries   []string        `json:"retrieval_queries,omitempty"`
	Evidence           []ScoutEvidence `json:"evidence,omitempty"`
	RetrievalStatus    string          `json:"retrieval_status,omitempty"`
	RetrievalTruncated bool            `json:"retrieval_truncated,omitempty"`
	Items              []ScoutItem     `json:"items,omitempty"`
	DoNotBlock         bool            `json:"do_not_block,omitempty"`
}

type ScoutEvidence struct {
	File   string `json:"file"`
	Line   int    `json:"line,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Reason string `json:"reason"`
}

type ScoutItem struct {
	Severity   string `json:"severity"`
	File       string `json:"file"`
	Line       int    `json:"line,omitempty"`
	Issue      string `json:"issue"`
	Suggestion string `json:"suggestion"`
}

type Review struct {
	SchemaVersion            int                  `json:"schema_version,omitempty"`
	Verdict                  string               `json:"verdict"`
	Summary                  string               `json:"summary"`
	Findings                 []Finding            `json:"findings"`
	TestSuggestions          []string             `json:"test_suggestions"`
	DataLossChecks           *DataLossChecks      `json:"data_loss_checks,omitempty"`
	PriorFindingDispositions []FindingDisposition `json:"prior_finding_dispositions"`
}

type Finding struct {
	ID       string `json:"id,omitempty"`
	Severity string `json:"severity"`
	File     string `json:"file"`
	Line     int    `json:"line,omitempty"`
	Issue    string `json:"issue"`
	Fix      string `json:"fix"`
}

const ReviewSchemaVersion = 2

type DataLossCheck struct {
	Status   string `json:"status"`
	Evidence string `json:"evidence"`
}

type DataLossChecks struct {
	MalformedInputPreservation DataLossCheck `json:"malformed_input_preservation"`
	AnnotationHistoryRetention DataLossCheck `json:"annotation_history_retention"`
	AmbiguousIdentityHandling  DataLossCheck `json:"ambiguous_identity_handling"`
	ReadOnlyNonMutation        DataLossCheck `json:"read_only_non_mutation"`
}

type FindingDisposition struct {
	FindingID string `json:"finding_id"`
	Status    string `json:"status"`
	Evidence  string `json:"evidence"`
}

func (r Review) HasBlockingFindings() bool {
	for _, finding := range r.Findings {
		if finding.Severity == "blocker" || finding.Severity == "major" {
			return true
		}
	}
	return false
}

func (r Review) OnlyMinorOrNit() bool {
	if len(r.Findings) == 0 {
		return true
	}
	for _, finding := range r.Findings {
		if finding.Severity != "minor" && finding.Severity != "nit" {
			return false
		}
	}
	return true
}

func (r Review) Validate() error {
	if r.SchemaVersion != 0 && r.SchemaVersion != ArtifactSchemaVersion && r.SchemaVersion != ReviewSchemaVersion {
		return fmt.Errorf("unsupported review schema_version %d", r.SchemaVersion)
	}
	if r.Verdict != "pass" && r.Verdict != "needs_changes" {
		return fmt.Errorf("invalid verdict %q", r.Verdict)
	}
	for i, finding := range r.Findings {
		switch finding.Severity {
		case "blocker", "major", "minor", "nit":
		default:
			return fmt.Errorf("finding %d has invalid severity %q", i, finding.Severity)
		}
		if strings.TrimSpace(finding.File) == "" {
			return fmt.Errorf("finding %d missing file", i)
		}
		if strings.TrimSpace(finding.Issue) == "" {
			return fmt.Errorf("finding %d missing issue", i)
		}
		if strings.TrimSpace(finding.Fix) == "" {
			return fmt.Errorf("finding %d missing fix", i)
		}
	}
	if r.Verdict == "pass" && r.HasBlockingFindings() {
		return fmt.Errorf("pass verdict cannot include blocker or major findings")
	}
	// Schema v0/v1 artifacts remain readable for status, transcript, and
	// migration. Live reviewer invocations call ValidateCurrent after parsing.
	if r.SchemaVersion == 0 || r.SchemaVersion == ArtifactSchemaVersion {
		return nil
	}
	if r.DataLossChecks == nil {
		return fmt.Errorf("missing data_loss_checks")
	}
	checks := []struct {
		name  string
		check DataLossCheck
	}{
		{"malformed_input_preservation", r.DataLossChecks.MalformedInputPreservation},
		{"annotation_history_retention", r.DataLossChecks.AnnotationHistoryRetention},
		{"ambiguous_identity_handling", r.DataLossChecks.AmbiguousIdentityHandling},
		{"read_only_non_mutation", r.DataLossChecks.ReadOnlyNonMutation},
	}
	failedDataLossCheck := false
	for _, item := range checks {
		switch item.check.Status {
		case "pass", "fail", "not_applicable":
		default:
			return fmt.Errorf("data-loss check %s has invalid status %q", item.name, item.check.Status)
		}
		if strings.TrimSpace(item.check.Evidence) == "" {
			return fmt.Errorf("data-loss check %s missing evidence", item.name)
		}
		if item.check.Status == "fail" && r.Verdict == "pass" {
			return fmt.Errorf("pass verdict cannot include failed data-loss check %s", item.name)
		}
		if item.check.Status == "fail" {
			failedDataLossCheck = true
		}
	}
	if failedDataLossCheck && !r.HasBlockingFindings() {
		return fmt.Errorf("failed data-loss checks require a blocker or major finding")
	}
	for i, disposition := range r.PriorFindingDispositions {
		if strings.TrimSpace(disposition.FindingID) == "" || strings.TrimSpace(disposition.Evidence) == "" {
			return fmt.Errorf("prior finding disposition %d missing finding_id or evidence", i)
		}
		if disposition.Status != "fixed" && disposition.Status != "disputed_with_evidence" {
			return fmt.Errorf("prior finding disposition %d has invalid status %q", i, disposition.Status)
		}
	}
	return nil
}

func (r Review) ValidateCurrent() error {
	if r.SchemaVersion != ReviewSchemaVersion {
		return fmt.Errorf("live review requires schema_version %d", ReviewSchemaVersion)
	}
	return r.Validate()
}

type RoleTarget struct {
	Adapter string `json:"adapter"`
	Model   string `json:"model,omitempty"`
}

func ParseRoleTarget(raw string) (RoleTarget, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return RoleTarget{}, fmt.Errorf("empty adapter target")
	}
	parts := strings.SplitN(raw, ":", 2)
	target := RoleTarget{Adapter: strings.TrimSpace(parts[0])}
	if len(parts) == 2 {
		target.Model = strings.TrimSpace(parts[1])
	}
	if target.Adapter == "" {
		return RoleTarget{}, fmt.Errorf("invalid adapter target %q", raw)
	}
	return target, nil
}

// TestPresetConfig is one named entry in the host-trusted test preset registry.
// Names resolve only from user config / built-in defaults (never raw MCP input
// or untrusted repo .tagteam.toml). Lookup is exact-match on the normalized
// name (no case folding).
type TestPresetConfig struct {
	Command       string `toml:"command"`
	IdentityRegex string `toml:"identity_regex,omitempty"`
}

type Config struct {
	Defaults    DefaultsConfig              `toml:"defaults"`
	Profiles    map[string]ProfileConfig    `toml:"profiles"`
	Adapters    AdapterConfigSet            `toml:"adapters"`
	CodeIntel   CodeIntelConfig             `toml:"code_intel"`
	TestPresets map[string]TestPresetConfig `toml:"test_presets"`
	EnvOverlay  map[string]string           `toml:"-"`
}
