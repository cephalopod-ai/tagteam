package tagteam

import (
	"encoding/json"
	"time"
)

type DefaultsConfig struct {
	StateRoot               string           `toml:"state_root"`
	WatchdogTimeout         string           `toml:"watchdog_timeout"`
	Mode                    string           `toml:"mode"`
	Coder                   string           `toml:"coder"`
	RelayCoder              string           `toml:"relay_coder"`
	Adversary               string           `toml:"adversary"`
	Worker                  string           `toml:"worker"`
	Scout                   string           `toml:"scout"`
	Supervisor              string           `toml:"supervisor"`
	ScoutMode               string           `toml:"scout_mode"`
	PostScoutMode           string           `toml:"post_scout_mode"`
	ScoutFailurePolicy      string           `toml:"scout_failure_policy"`
	LossPolicy              RoleLossPolicies `toml:"loss_policy"`
	Fallbacks               RoleFallbacks    `toml:"fallbacks"`
	FallbacksByTarget       TargetFallbacks  `toml:"fallbacks_by_target"`
	ScoutRetrieval          *bool            `toml:"scout_retrieval"`
	CodeIntelCommand        string           `toml:"code_intel_command"`
	ScoutContextPolicy      string           `toml:"scout_context_policy"`
	SupervisorSlicing       *bool            `toml:"supervisor_slicing"`
	MaxPackages             int              `toml:"max_packages"`
	Package                 string           `toml:"package"`
	AutoNextPackage         *bool            `toml:"auto_next_package"`
	RespectRepoInstructions *bool            `toml:"respect_repo_instructions"`
	DecisionMemory          *bool            `toml:"decision_memory"`
	MaxFindings             int              `toml:"max_findings"`
	MaxOutputBytes          int64            `toml:"max_output_bytes"`
	MaxWallTime             string           `toml:"max_wall_time"`
	MaxRoleInvocations      int              `toml:"max_role_invocations"`
	JSONRepair              string           `toml:"json_repair"`
	Rounds                  int              `toml:"rounds"`
	Test                    string           `toml:"test"`
	Tests                   []string         `toml:"tests"`
	Lint                    string           `toml:"lint"`
	TestIdentityRegex       string           `toml:"test_identity_regex"`
	Churn                   ChurnThresholds  `toml:"churn"`
	GitSafety               string           `toml:"git_safety"`
}

type ProfileConfig struct {
	StateRoot               string           `toml:"state_root"`
	WatchdogTimeout         string           `toml:"watchdog_timeout"`
	Mode                    string           `toml:"mode"`
	Coder                   string           `toml:"coder"`
	Adversary               string           `toml:"adversary"`
	Worker                  string           `toml:"worker"`
	Scout                   string           `toml:"scout"`
	Supervisor              string           `toml:"supervisor"`
	ScoutMode               string           `toml:"scout_mode"`
	PostScoutMode           string           `toml:"post_scout_mode"`
	ScoutFailurePolicy      string           `toml:"scout_failure_policy"`
	LossPolicy              RoleLossPolicies `toml:"loss_policy"`
	Fallbacks               RoleFallbacks    `toml:"fallbacks"`
	FallbacksByTarget       TargetFallbacks  `toml:"fallbacks_by_target"`
	ScoutRetrieval          *bool            `toml:"scout_retrieval"`
	CodeIntelCommand        string           `toml:"code_intel_command"`
	ScoutContextPolicy      string           `toml:"scout_context_policy"`
	SupervisorSlicing       *bool            `toml:"supervisor_slicing"`
	MaxPackages             int              `toml:"max_packages"`
	Package                 string           `toml:"package"`
	AutoNextPackage         *bool            `toml:"auto_next_package"`
	RespectRepoInstructions *bool            `toml:"respect_repo_instructions"`
	DecisionMemory          *bool            `toml:"decision_memory"`
	MaxFindings             int              `toml:"max_findings"`
	MaxOutputBytes          int64            `toml:"max_output_bytes"`
	MaxWallTime             string           `toml:"max_wall_time"`
	MaxRoleInvocations      int              `toml:"max_role_invocations"`
	JSONRepair              string           `toml:"json_repair"`
	Rounds                  int              `toml:"rounds"`
	Test                    string           `toml:"test"`
	Tests                   []string         `toml:"tests"`
	Lint                    string           `toml:"lint"`
	TestIdentityRegex       string           `toml:"test_identity_regex"`
	Churn                   ChurnThresholds  `toml:"churn"`
}

type ChurnThresholds struct {
	MaxFiles             int     `toml:"max_files" json:"max_files"`
	MaxChangedLines      int     `toml:"max_changed_lines" json:"max_changed_lines"`
	MaxFixtureFiles      int     `toml:"max_fixture_files" json:"max_fixture_files"`
	WhitespaceRatio      float64 `toml:"whitespace_ratio" json:"whitespace_ratio"`
	MinimumSemanticRatio float64 `toml:"minimum_semantic_ratio" json:"minimum_semantic_ratio"`
}

type AdapterConfigSet struct {
	Codex            CodexConfig            `toml:"codex"`
	Claude           ClaudeConfig           `toml:"claude"`
	CodexOSS         CodexConfig            `toml:"codex-oss"`
	Agy              AgyConfig              `toml:"agy"`
	Gosling          GoslingConfig          `toml:"gosling"`
	Grok             GrokConfig             `toml:"grok"`
	OpenAICompatible OpenAICompatibleConfig `toml:"openai_compatible"`
}

type CodexConfig struct {
	DefaultModel         string   `toml:"default_model"`
	ReasoningEffort      string   `toml:"reasoning_effort"`
	MaxContextTokens     *int     `toml:"max_context_tokens"`
	ReservedOutputTokens *int     `toml:"reserved_output_tokens"`
	ExtraArgs            []string `toml:"extra_args"`
}

type ClaudeConfig struct {
	DefaultModel         string   `toml:"default_model"`
	Effort               string   `toml:"effort"`
	MaxContextTokens     *int     `toml:"max_context_tokens"`
	ReservedOutputTokens *int     `toml:"reserved_output_tokens"`
	CoderAllowedTools    []string `toml:"coder_allowed_tools"`
	ExtraArgs            []string `toml:"extra_args"`
	Bare                 bool     `toml:"bare"`
	// Serialize gates the cross-process claude invocation lock; nil means
	// enabled. Concurrent claude CLI processes can stall or remain pending.
	Serialize *bool `toml:"serialize"`
}

type AgyConfig struct {
	DefaultModel         string   `toml:"default_model"`
	MaxContextTokens     *int     `toml:"max_context_tokens"`
	ReservedOutputTokens *int     `toml:"reserved_output_tokens"`
	ExtraArgs            []string `toml:"extra_args"`
}

type GoslingConfig struct {
	DefaultModel         string   `toml:"default_model"`
	MaxContextTokens     *int     `toml:"max_context_tokens"`
	ReservedOutputTokens *int     `toml:"reserved_output_tokens"`
	ExtraArgs            []string `toml:"extra_args"`
}

type GrokConfig struct {
	DefaultModel         string   `toml:"default_model"`
	ReasoningEffort      string   `toml:"reasoning_effort"`
	MaxContextTokens     *int     `toml:"max_context_tokens"`
	ReservedOutputTokens *int     `toml:"reserved_output_tokens"`
	ExtraArgs            []string `toml:"extra_args"`
}

type OpenAICompatibleConfig struct {
	BaseURL              string            `toml:"base_url"`
	APIKeyEnv            string            `toml:"api_key_env"`
	DefaultModel         string            `toml:"default_model"`
	MaxContextTokens     *int              `toml:"max_context_tokens"`
	ReservedOutputTokens *int              `toml:"reserved_output_tokens"`
	ExtraHeaders         map[string]string `toml:"extra_headers"`
	ExtraArgs            []string          `toml:"extra_args"`
}

// CodeIntelConfig contains only local subprocess and file-contract settings.
// It deliberately has no endpoint or credential value fields.
type CodeIntelConfig struct {
	Providers    map[string]CodeIntelProviderConfig `toml:"providers"`
	AllowedRepos []string                           `toml:"allowed_repos"`
	ExcludePaths []string                           `toml:"exclude_paths"`
	Timeout      string                             `toml:"timeout"`
	Dory         CodeIntelFileBridgeConfig          `toml:"dory"`
	Alexandria   CodeIntelFileBridgeConfig          `toml:"alexandria"`
	Muninn       CodeIntelFileBridgeConfig          `toml:"muninn"`
}

type CodeIntelProviderConfig struct {
	Command string `toml:"command"`
}

// CodeIntelFileBridgeConfig is opt-in. APIKeyEnv is an environment variable
// name for an out-of-repo transport, never a secret consumed by Tagteam.
type CodeIntelFileBridgeConfig struct {
	Enabled   bool   `toml:"enabled"`
	Path      string `toml:"path"`
	APIKeyEnv string `toml:"api_key_env"`
}

type FlagInputs struct {
	Mode                    string
	Solo                    string
	Relay                   bool
	Coder                   string
	CoderRole               string
	Model                   string
	Adversary               string
	Worker                  string
	Scout                   string
	ScoutMode               string
	PostScoutMode           string
	StrictScout             bool
	NoScoutRetrieval        bool
	ScoutContextPolicy      string
	TrustRepoConfig         bool
	Supervisor              string
	Reviewer                string
	SupervisorCanEdit       bool
	Slice                   bool
	NoSlice                 bool
	MaxPackages             int
	Package                 string
	AutoNextPackage         bool
	RespectRepoInstructions bool
	NoRepoInstructions      bool
	DecisionMemory          bool
	MaxFindings             int
	MaxOutputBytes          int64
	MaxWallTime             time.Duration
	MaxRoleInvocations      int
	RepairJSONWithWorker    bool
	Profile                 string
	Workdir                 string
	StateRoot               string
	AllowedPaths            []string
	WatchdogTimeout         time.Duration
	Rounds                  int
	Test                    string
	Tests                   []string
	TestIdentityRegex       string
	Lint                    string
	NoTest                  bool
	JSON                    bool
	DryRun                  bool
	ShowReview              bool
	FailOnReview            bool
	AllowDirty              bool
	AllowDevBuild           bool
	Autostash               bool
	Timeout                 time.Duration
	Quiet                   bool
	Verbose                 bool
	CodexArgsRaw            string
	ClaudeArgsRaw           string
	AgyArgsRaw              string
	GoslingArgsRaw          string
	GrokArgsRaw             string
	OpenAICompatibleArgsRaw string
}

type RunOptions struct {
	Prompt  string
	Workdir string
	// RunID is an optional host-reserved identifier. Normal CLI runs leave it
	// empty and receive a timestamp identifier from the runner.
	RunID string
	// StateRoot overrides the default ~/.local/state/tagteam artifact root.
	StateRoot    string
	ResumedFrom  string
	AllowedPaths []string
	Mode         Mode
	// ModeExplicit is true when the caller explicitly requested Mode for
	// this invocation (via --mode or a --profile), as opposed to it being
	// left at the config/built-in default. Fix uses this to decide whether
	// to keep the caller's mode or resume the saved run's mode.
	ModeExplicit bool
	// Coder holds the resolved implementation target for whichever mode is
	// active: solo in ModeSolo, worker in ModeSupervisor, coder in
	// ModeAdversarial/ModeRelay. Adversary holds the resolved review target
	// for reviewed modes only: supervisor in ModeSupervisor/ModeRelay and
	// adversary in ModeAdversarial. It is empty in ModeSolo.
	Coder     RoleTarget
	Adversary RoleTarget
	Scout     RoleTarget
	// CoderExplicit and AdversaryExplicit mirror ModeExplicit for the
	// implementation/review targets: true when the caller passed
	// --model/--solo/-mc/--worker or -ma/--supervisor/--reviewer for this invocation.
	CoderExplicit             bool
	AdversaryExplicit         bool
	ScoutExplicit             bool
	CoderExplicitMode         Mode
	AdversaryExplicitMode     Mode
	ScoutExplicitMode         Mode
	ScoutMode                 string
	PostScoutMode             string
	ScoutFailurePolicy        string
	LossPolicy                RoleLossPolicies
	Fallbacks                 RoleFallbacks
	FallbacksByTarget         TargetFallbacks
	ScoutRetrieval            bool
	CodeIntelCommand          string
	CodeIntel                 CodeIntelConfig
	ScoutContextPolicy        string
	TrustRepoConfig           bool
	SupervisorCanEdit         bool
	SupervisorCanEditExplicit bool
	SupervisorSlicing         bool
	SupervisorSlicingExplicit bool
	MaxPackages               int
	Package                   string
	AutoNextPackage           bool
	RespectRepoInstructions   bool
	DecisionMemory            bool
	MaxFindings               int
	MaxOutputBytes            int64
	MaxWallTime               time.Duration
	MaxRoleInvocations        int
	JSONRepair                string
	Rounds                    int
	TestCmd                   string
	TestCommands              []string
	LintCmd                   string
	TestIdentityRegex         string
	Churn                     ChurnThresholds
	NoTest                    bool
	JSON                      bool
	DryRun                    bool
	ShowReview                bool
	FailOnReview              bool
	AllowDirty                bool
	Autostash                 bool
	Timeout                   time.Duration
	WatchdogTimeout           time.Duration
	Quiet                     bool
	Verbose                   bool
	GitSafety                 string
	CodexArgs                 []string
	ClaudeArgs                []string
	AgyArgs                   []string
	GoslingArgs               []string
	GrokArgs                  []string
	OpenAICompatibleArgs      []string
	EnvOverlay                map[string]string
	ConfigSources             []string
	Baseline                  string
	SkipDirtyCheck            bool
	InvocationBudget          *InvocationBudget
}

type Meta struct {
	SchemaVersion int               `json:"schema_version"`
	RunID         string            `json:"run_id"`
	Workdir       string            `json:"workdir"`
	Baseline      string            `json:"baseline"`
	Command       string            `json:"command"`
	Prompt        string            `json:"prompt"`
	StartedAt     time.Time         `json:"started_at"`
	Adapters      map[string]string `json:"adapters"`
	Models        map[string]string `json:"models"`
	ConfigSources []string          `json:"config_sources,omitempty"`
}

type TestRun struct {
	Command           string           `json:"command"`
	Output            string           `json:"output,omitempty"`
	Passed            bool             `json:"passed"`
	FailureIdentities []string         `json:"failure_identities,omitempty"`
	StateRoot         string           `json:"state_root,omitempty"`
	TempDir           string           `json:"temp_dir,omitempty"`
	Commands          []TestCommandRun `json:"commands,omitempty"`
}

// TestCommandRun records one command within an explicitly parallel test set.
// Single-command runs keep the legacy TestRun shape without Commands.
type TestCommandRun struct {
	Command           string   `json:"command"`
	Output            string   `json:"output,omitempty"`
	Passed            bool     `json:"passed"`
	FailureIdentities []string `json:"failure_identities,omitempty"`
	StateRoot         string   `json:"state_root,omitempty"`
	TempDir           string   `json:"temp_dir,omitempty"`
	OutputPath        string   `json:"output_path,omitempty"`
}

type DiffFile struct {
	Path      string `json:"path"`
	OldPath   string `json:"old_path,omitempty"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Binary    bool   `json:"binary,omitempty"`
}

type DiffFilesMetadata struct {
	SchemaVersion int        `json:"schema_version"`
	Baseline      string     `json:"baseline"`
	Head          string     `json:"head"`
	GeneratedAt   time.Time  `json:"generated_at"`
	DiffSHA256    string     `json:"diff_sha256"`
	Files         []DiffFile `json:"files"`
}

type FinalRun struct {
	SchemaVersion int    `json:"schema_version"`
	RunID         string `json:"run_id"`
	ResumedFrom   string `json:"resumed_from,omitempty"`
	RunDir        string `json:"run_dir"`
	Workdir       string `json:"workdir"`
	Baseline      string `json:"baseline"`
	Mode          Mode   `json:"mode,omitempty"`
	// Coder persists the resolved implementation target used for this run.
	// Adversary persists the resolved review target for reviewed modes; it is
	// empty in solo mode. `tagteam fix` uses these saved targets to resume
	// with the same mode and adapters instead of re-resolving from current
	// defaults/flags.
	Coder             RoleTarget            `json:"coder,omitempty"`
	Adversary         RoleTarget            `json:"adversary,omitempty"`
	Scout             RoleTarget            `json:"scout,omitempty"`
	SupervisorCanEdit bool                  `json:"supervisor_can_edit,omitempty"`
	WorkPlan          *WorkPlan             `json:"work_plan,omitempty"`
	Plan              *PlanSummary          `json:"plan,omitempty"`
	SelectedPackage   *WorkPackage          `json:"selected_package,omitempty"`
	RemainingPackages []string              `json:"remaining_packages,omitempty"`
	Verdict           string                `json:"verdict"`
	Summary           string                `json:"summary"`
	Status            RunStatus             `json:"status,omitempty"`
	Phase             string                `json:"phase,omitempty"`
	Degraded          bool                  `json:"degraded"`
	DegradedReason    string                `json:"degraded_reason,omitempty"`
	BlockingReason    string                `json:"blocking_reason,omitempty"`
	RoleStatuses      map[string]RoleStatus `json:"role_statuses,omitempty"`
	RoleLosses        []RoleLossRecord      `json:"role_losses,omitempty"`
	Budgets           BudgetState           `json:"budgets,omitempty"`
	ExitCode          int                   `json:"exit_code"`
	Caps              RunCaps               `json:"caps,omitempty"`
	RoundsRequested   int                   `json:"rounds_requested"`
	RoundsCompleted   int                   `json:"rounds_completed"`
	ChangedFiles      []string              `json:"changed_files,omitempty"`
	LatestDiffPath    string                `json:"latest_diff_path,omitempty"`
	LatestNumstatPath string                `json:"latest_numstat_path,omitempty"`
	LatestFilesPath   string                `json:"latest_files_path,omitempty"`
	LatestSHA256Path  string                `json:"latest_sha256_path,omitempty"`
	LatestDiffSHA256  string                `json:"latest_diff_sha256,omitempty"`
	LatestReviewPath  string                `json:"latest_review_path,omitempty"`
	RoundLimitReached bool                  `json:"round_limit_reached,omitempty"`
	RoundLimitReports []RoundLimitReport    `json:"round_limit_reports,omitempty"`
	Review            *Review               `json:"review,omitempty"`
	Tests             []TestRun             `json:"tests,omitempty"`
	BaselineTest      *TestRun              `json:"baseline_test,omitempty"`
	Regression        *RegressionResult     `json:"regression,omitempty"`
	QualityGates      []QualityGateResult   `json:"quality_gates,omitempty"`
	Findings          FindingsSummary       `json:"findings_ledger,omitempty"`
	Costs             map[string]float64    `json:"costs,omitempty"`
	Adapters          map[string]string     `json:"adapters,omitempty"`
	Models            map[string]string     `json:"models,omitempty"`
	StartedAt         time.Time             `json:"started_at"`
	FinishedAt        time.Time             `json:"finished_at"`

	// envOverlay is the resolved env overlay for the run. It is unexported so it
	// is never serialized (FinalRun.MarshalJSON copies via a struct alias, which
	// drops unexported fields), and serves as the single source of truth for
	// overlay-aware secret redaction in setRoleStatus/appendRoleLoss.
	envOverlay map[string]string
}

type RoundLimitReport struct {
	SchemaVersion int    `json:"schema_version,omitempty"`
	Role          string `json:"role"`
	Adapter       string `json:"adapter"`
	Path          string `json:"path,omitempty"`
	Text          string `json:"text,omitempty"`
}

type LatestRun struct {
	RunID     string    `json:"run_id"`
	RunDir    string    `json:"run_dir"`
	FinalPath string    `json:"final_path"`
	Verdict   string    `json:"verdict"`
	ExitCode  int       `json:"exit_code"`
	UpdatedAt time.Time `json:"updated_at"`
}

type RunCaps struct {
	MaxFindings        int   `json:"max_findings,omitempty"`
	MaxOutputBytes     int64 `json:"max_output_bytes,omitempty"`
	TimeoutSeconds     int64 `json:"timeout_seconds,omitempty"`
	MaxWallTimeSeconds int64 `json:"max_wall_time_seconds,omitempty"`
	MaxRoleInvocations int   `json:"max_role_invocations,omitempty"`
}

type DeliveryRecord struct {
	SchemaVersion       int       `json:"schema_version"`
	InvocationID        string    `json:"invocation_id"`
	Role                Role      `json:"role"`
	Adapter             string    `json:"adapter"`
	Phase               string    `json:"phase,omitempty"`
	PromptPath          string    `json:"prompt_path,omitempty"`
	SchemaPath          string    `json:"schema_path,omitempty"`
	StdinBytes          int       `json:"stdin_bytes,omitempty"`
	StdinSHA256         string    `json:"stdin_sha256,omitempty"`
	OutputPath          string    `json:"output_path,omitempty"`
	StdoutPath          string    `json:"stdout_path,omitempty"`
	StderrPath          string    `json:"stderr_path,omitempty"`
	StdoutBytes         int64     `json:"stdout_bytes,omitempty"`
	StderrBytes         int64     `json:"stderr_bytes,omitempty"`
	StdoutTruncated     bool      `json:"stdout_truncated,omitempty"`
	StderrTruncated     bool      `json:"stderr_truncated,omitempty"`
	RawOutputPath       string    `json:"raw_output_path,omitempty"`
	ParsedPath          string    `json:"parsed_path,omitempty"`
	ValidationErrorPath string    `json:"validation_error_path,omitempty"`
	Argv                []string  `json:"argv,omitempty"`
	Model               string    `json:"model,omitempty"`
	Timeout             string    `json:"timeout,omitempty"`
	InputMode           string    `json:"input_mode,omitempty"`
	PromptInlined       bool      `json:"prompt_inlined"`
	DryRun              bool      `json:"dry_run,omitempty"`
	StartedAt           time.Time `json:"started_at"`
	FinishedAt          time.Time `json:"finished_at"`
	Status              string    `json:"status"`
	ProcessExitCode     int       `json:"process_exit_code,omitempty"`
	CancellationCause   string    `json:"cancellation_cause,omitempty"`
	Error               string    `json:"error,omitempty"`
}

type RunState struct {
	SchemaVersion    int                   `json:"schema_version"`
	RunID            string                `json:"run_id"`
	Mode             Mode                  `json:"mode,omitempty"`
	Status           string                `json:"status"`
	Phase            string                `json:"phase,omitempty"`
	CompletedPhase   RunPhase              `json:"completed_phase,omitempty"`
	RepoID           string                `json:"repo_id,omitempty"`
	Workdir          string                `json:"workdir,omitempty"`
	BaselineSHA      string                `json:"baseline_sha,omitempty"`
	DiffHash         string                `json:"diff_hash,omitempty"`
	Role             string                `json:"role,omitempty"`
	Adapter          string                `json:"adapter,omitempty"`
	Model            string                `json:"model,omitempty"`
	InvocationID     string                `json:"invocation_id,omitempty"`
	RecoveryStatus   string                `json:"recovery_status,omitempty"`
	Degraded         bool                  `json:"degraded"`
	DegradedReason   string                `json:"degraded_reason,omitempty"`
	BlockingReason   string                `json:"blocking_reason,omitempty"`
	RoleStatuses     map[string]RoleStatus `json:"role_statuses,omitempty"`
	CurrentRound     int                   `json:"current_round,omitempty"`
	LatestDiffPath   string                `json:"latest_diff_path,omitempty"`
	LatestReviewPath string                `json:"latest_review_path,omitempty"`
	ExitCode         int                   `json:"exit_code,omitempty"`
	UpdatedAt        time.Time             `json:"updated_at"`
}

// RunSnapshot is a compact, read-only view of a run assembled from whichever
// of active.json/state.json/final.json/plan.json exist on disk. It is the
// common status surface consumed by `tagteam status --json` and the TUI, so
// neither has to reverse-engineer the full set of run artifacts.
type RunSnapshot struct {
	SchemaVersion    int                   `json:"schema_version"`
	RunID            string                `json:"run_id"`
	RunDir           string                `json:"run_dir"`
	Mode             Mode                  `json:"mode,omitempty"`
	Status           string                `json:"status,omitempty"`
	Phase            string                `json:"phase,omitempty"`
	CompletedPhase   RunPhase              `json:"completed_phase,omitempty"`
	RepoID           string                `json:"repo_id,omitempty"`
	StateRoot        string                `json:"state_root,omitempty"`
	InvocationID     string                `json:"invocation_id,omitempty"`
	DiffHash         string                `json:"diff_hash,omitempty"`
	RecoveryStatus   string                `json:"recovery_status,omitempty"`
	Verdict          string                `json:"verdict,omitempty"`
	ExitCode         int                   `json:"exit_code"`
	Degraded         bool                  `json:"degraded"`
	DegradedReason   string                `json:"degraded_reason,omitempty"`
	BlockingReason   string                `json:"blocking_reason,omitempty"`
	CurrentRound     int                   `json:"current_round,omitempty"`
	RoundsCompleted  int                   `json:"rounds_completed,omitempty"`
	RoundsRequested  int                   `json:"rounds_requested,omitempty"`
	RoleStatuses     map[string]RoleStatus `json:"role_statuses,omitempty"`
	LiveProgress     *LiveProgress         `json:"live_progress,omitempty"`
	HostActivity     *HostActivity         `json:"host_activity,omitempty"`
	PlanSummary      *PlanSummary          `json:"plan_summary,omitempty"`
	LatestDiffPath   string                `json:"latest_diff_path,omitempty"`
	LatestReviewPath string                `json:"latest_review_path,omitempty"`
	LatestTestPath   string                `json:"latest_test_path,omitempty"`
	ChangedFiles     []string              `json:"changed_files,omitempty"`
	PreexistingFiles []string              `json:"preexisting_files,omitempty"`
	FindingsCount    int                   `json:"findings_count"`
	OpenMajorCount   int                   `json:"open_major_count"`
	UpdatedAt        time.Time             `json:"updated_at"`
}

func (f FinalRun) MarshalJSON() ([]byte, error) {
	type alias FinalRun
	return json.Marshal(alias(f))
}
