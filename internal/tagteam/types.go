package tagteam

import (
	"context"
	"encoding/json"
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
	Context        context.Context
	Prompt         string
	SystemPrompt   string
	EnvOverlay     map[string]string
	Model          string
	Workdir        string
	RunDir         string
	OutputPath     string
	SchemaPath     string
	Timeout        time.Duration
	MaxOutputBytes int64
	Passthrough    []string
	ResumeID       string
	Stdin          []byte
	InputMode      string
	Phase          string
	Quiet          bool
	Verbose        bool
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
	Text      string   `json:"text,omitempty"`
	Review    *Review  `json:"review,omitempty"`
	Scout     *Scout   `json:"scout,omitempty"`
	SessionID string   `json:"session_id,omitempty"`
	CostUSD   float64  `json:"cost_usd,omitempty"`
	Raw       []byte   `json:"-"`
	Command   []string `json:"command,omitempty"`
}

type WorkPlan struct {
	SchemaVersion   int           `json:"schema_version,omitempty"`
	Summary         string        `json:"summary"`
	Packages        []WorkPackage `json:"packages"`
	SelectedPackage string        `json:"selected_package"`
	Defer           []string      `json:"defer,omitempty"`
}

type WorkPackage struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Goal         string   `json:"goal"`
	AllowedScope []string `json:"allowed_scope"`
	Acceptance   []string `json:"acceptance"`
	Validation   []string `json:"validation"`
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
	SchemaVersion   int       `json:"schema_version,omitempty"`
	Verdict         string    `json:"verdict"`
	Summary         string    `json:"summary"`
	Findings        []Finding `json:"findings"`
	TestSuggestions []string  `json:"test_suggestions,omitempty"`
}

type Finding struct {
	Severity string `json:"severity"`
	File     string `json:"file"`
	Line     int    `json:"line,omitempty"`
	Issue    string `json:"issue"`
	Fix      string `json:"fix"`
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
	if r.SchemaVersion != 0 && r.SchemaVersion != ArtifactSchemaVersion {
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
	return nil
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

type Config struct {
	Defaults   DefaultsConfig           `toml:"defaults"`
	Profiles   map[string]ProfileConfig `toml:"profiles"`
	Adapters   AdapterConfigSet         `toml:"adapters"`
	EnvOverlay map[string]string        `toml:"-"`
}

type DefaultsConfig struct {
	Mode                    string `toml:"mode"`
	Coder                   string `toml:"coder"`
	Adversary               string `toml:"adversary"`
	Worker                  string `toml:"worker"`
	Scout                   string `toml:"scout"`
	Supervisor              string `toml:"supervisor"`
	ScoutMode               string `toml:"scout_mode"`
	PostScoutMode           string `toml:"post_scout_mode"`
	ScoutRetrieval          *bool  `toml:"scout_retrieval"`
	SupervisorSlicing       *bool  `toml:"supervisor_slicing"`
	MaxPackages             int    `toml:"max_packages"`
	Package                 string `toml:"package"`
	AutoNextPackage         *bool  `toml:"auto_next_package"`
	RespectRepoInstructions *bool  `toml:"respect_repo_instructions"`
	DecisionMemory          *bool  `toml:"decision_memory"`
	MaxFindings             int    `toml:"max_findings"`
	MaxOutputBytes          int64  `toml:"max_output_bytes"`
	MaxWallTime             string `toml:"max_wall_time"`
	Rounds                  int    `toml:"rounds"`
	Test                    string `toml:"test"`
	GitSafety               string `toml:"git_safety"`
}

type ProfileConfig struct {
	Mode                    string `toml:"mode"`
	Coder                   string `toml:"coder"`
	Adversary               string `toml:"adversary"`
	Worker                  string `toml:"worker"`
	Scout                   string `toml:"scout"`
	Supervisor              string `toml:"supervisor"`
	ScoutMode               string `toml:"scout_mode"`
	PostScoutMode           string `toml:"post_scout_mode"`
	ScoutRetrieval          *bool  `toml:"scout_retrieval"`
	SupervisorSlicing       *bool  `toml:"supervisor_slicing"`
	MaxPackages             int    `toml:"max_packages"`
	Package                 string `toml:"package"`
	AutoNextPackage         *bool  `toml:"auto_next_package"`
	RespectRepoInstructions *bool  `toml:"respect_repo_instructions"`
	DecisionMemory          *bool  `toml:"decision_memory"`
	MaxFindings             int    `toml:"max_findings"`
	MaxOutputBytes          int64  `toml:"max_output_bytes"`
	MaxWallTime             string `toml:"max_wall_time"`
	Rounds                  int    `toml:"rounds"`
	Test                    string `toml:"test"`
}

type AdapterConfigSet struct {
	Codex            CodexConfig            `toml:"codex"`
	Claude           ClaudeConfig           `toml:"claude"`
	CodexOSS         CodexConfig            `toml:"codex-oss"`
	Agy              AgyConfig              `toml:"agy"`
	Gosling          GoslingConfig          `toml:"gosling"`
	OpenAICompatible OpenAICompatibleConfig `toml:"openai_compatible"`
}

type CodexConfig struct {
	DefaultModel         string   `toml:"default_model"`
	MaxContextTokens     *int     `toml:"max_context_tokens"`
	ReservedOutputTokens *int     `toml:"reserved_output_tokens"`
	ExtraArgs            []string `toml:"extra_args"`
}

type ClaudeConfig struct {
	DefaultModel         string   `toml:"default_model"`
	MaxContextTokens     *int     `toml:"max_context_tokens"`
	ReservedOutputTokens *int     `toml:"reserved_output_tokens"`
	CoderAllowedTools    []string `toml:"coder_allowed_tools"`
	ExtraArgs            []string `toml:"extra_args"`
	Bare                 bool     `toml:"bare"`
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

type OpenAICompatibleConfig struct {
	BaseURL              string            `toml:"base_url"`
	APIKeyEnv            string            `toml:"api_key_env"`
	DefaultModel         string            `toml:"default_model"`
	MaxContextTokens     *int              `toml:"max_context_tokens"`
	ReservedOutputTokens *int              `toml:"reserved_output_tokens"`
	ExtraHeaders         map[string]string `toml:"extra_headers"`
	ExtraArgs            []string          `toml:"extra_args"`
}

type FlagInputs struct {
	Mode                    string
	Solo                    string
	Relay                   bool
	Coder                   string
	CoderRole               string
	Adversary               string
	Worker                  string
	Scout                   string
	ScoutMode               string
	PostScoutMode           string
	NoScoutRetrieval        bool
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
	Profile                 string
	Workdir                 string
	Rounds                  int
	Test                    string
	NoTest                  bool
	JSON                    bool
	DryRun                  bool
	ShowReview              bool
	FailOnReview            bool
	AllowDirty              bool
	Autostash               bool
	Timeout                 time.Duration
	Quiet                   bool
	Verbose                 bool
	CodexArgsRaw            string
	ClaudeArgsRaw           string
	AgyArgsRaw              string
	GoslingArgsRaw          string
	OpenAICompatibleArgsRaw string
}

type RunOptions struct {
	Prompt  string
	Workdir string
	Mode    Mode
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
	// --solo/-mc/--worker or -ma/--supervisor/--reviewer for this invocation.
	CoderExplicit             bool
	AdversaryExplicit         bool
	ScoutExplicit             bool
	CoderExplicitMode         Mode
	AdversaryExplicitMode     Mode
	ScoutExplicitMode         Mode
	ScoutMode                 string
	PostScoutMode             string
	ScoutRetrieval            bool
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
	Rounds                    int
	TestCmd                   string
	NoTest                    bool
	JSON                      bool
	DryRun                    bool
	ShowReview                bool
	FailOnReview              bool
	AllowDirty                bool
	Autostash                 bool
	Timeout                   time.Duration
	Quiet                     bool
	Verbose                   bool
	GitSafety                 string
	CodexArgs                 []string
	ClaudeArgs                []string
	AgyArgs                   []string
	GoslingArgs               []string
	OpenAICompatibleArgs      []string
	EnvOverlay                map[string]string
	ConfigSources             []string
	Baseline                  string
	SkipDirtyCheck            bool
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
	Command string `json:"command"`
	Output  string `json:"output,omitempty"`
	Passed  bool   `json:"passed"`
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
	RunDir        string `json:"run_dir"`
	Workdir       string `json:"workdir"`
	Baseline      string `json:"baseline"`
	Mode          Mode   `json:"mode,omitempty"`
	// Coder persists the resolved implementation target used for this run.
	// Adversary persists the resolved review target for reviewed modes; it is
	// empty in solo mode. `tagteam fix` uses these saved targets to resume
	// with the same mode and adapters instead of re-resolving from current
	// defaults/flags.
	Coder             RoleTarget         `json:"coder,omitempty"`
	Adversary         RoleTarget         `json:"adversary,omitempty"`
	Scout             RoleTarget         `json:"scout,omitempty"`
	SupervisorCanEdit bool               `json:"supervisor_can_edit,omitempty"`
	WorkPlan          *WorkPlan          `json:"work_plan,omitempty"`
	Plan              *PlanSummary       `json:"plan,omitempty"`
	SelectedPackage   *WorkPackage       `json:"selected_package,omitempty"`
	RemainingPackages []string           `json:"remaining_packages,omitempty"`
	Verdict           string             `json:"verdict"`
	Summary           string             `json:"summary"`
	DegradedReason    string             `json:"degraded_reason,omitempty"`
	ExitCode          int                `json:"exit_code"`
	Caps              RunCaps            `json:"caps,omitempty"`
	RoundsRequested   int                `json:"rounds_requested"`
	RoundsCompleted   int                `json:"rounds_completed"`
	ChangedFiles      []string           `json:"changed_files,omitempty"`
	LatestDiffPath    string             `json:"latest_diff_path,omitempty"`
	LatestNumstatPath string             `json:"latest_numstat_path,omitempty"`
	LatestFilesPath   string             `json:"latest_files_path,omitempty"`
	LatestSHA256Path  string             `json:"latest_sha256_path,omitempty"`
	LatestDiffSHA256  string             `json:"latest_diff_sha256,omitempty"`
	LatestReviewPath  string             `json:"latest_review_path,omitempty"`
	RoundLimitReached bool               `json:"round_limit_reached,omitempty"`
	RoundLimitReports []RoundLimitReport `json:"round_limit_reports,omitempty"`
	Review            *Review            `json:"review,omitempty"`
	Tests             []TestRun          `json:"tests,omitempty"`
	Costs             map[string]float64 `json:"costs,omitempty"`
	Adapters          map[string]string  `json:"adapters,omitempty"`
	Models            map[string]string  `json:"models,omitempty"`
	StartedAt         time.Time          `json:"started_at"`
	FinishedAt        time.Time          `json:"finished_at"`
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
}

type DeliveryRecord struct {
	SchemaVersion       int       `json:"schema_version"`
	Role                Role      `json:"role"`
	Adapter             string    `json:"adapter"`
	Phase               string    `json:"phase,omitempty"`
	PromptPath          string    `json:"prompt_path,omitempty"`
	SchemaPath          string    `json:"schema_path,omitempty"`
	StdinBytes          int       `json:"stdin_bytes,omitempty"`
	StdinSHA256         string    `json:"stdin_sha256,omitempty"`
	OutputPath          string    `json:"output_path,omitempty"`
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
	Error               string    `json:"error,omitempty"`
}

type RunState struct {
	SchemaVersion    int       `json:"schema_version"`
	RunID            string    `json:"run_id"`
	Mode             Mode      `json:"mode,omitempty"`
	Status           string    `json:"status"`
	Phase            string    `json:"phase,omitempty"`
	CurrentRound     int       `json:"current_round,omitempty"`
	LatestDiffPath   string    `json:"latest_diff_path,omitempty"`
	LatestReviewPath string    `json:"latest_review_path,omitempty"`
	ExitCode         int       `json:"exit_code,omitempty"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func (f FinalRun) MarshalJSON() ([]byte, error) {
	type alias FinalRun
	return json.Marshal(alias(f))
}
