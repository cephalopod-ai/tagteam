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
	Context      context.Context
	Prompt       string
	SystemPrompt string
	Model        string
	Workdir      string
	RunDir       string
	OutputPath   string
	SchemaPath   string
	Timeout      time.Duration
	Passthrough  []string
	ResumeID     string
	Stdin        []byte
	Phase        string
	Quiet        bool
	Verbose      bool
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
	Mode              string      `json:"mode,omitempty"`
	Summary           string      `json:"summary,omitempty"`
	RelevantFiles     []string    `json:"relevant_files"`
	LikelyEntryPoints []string    `json:"likely_entry_points"`
	ExistingPatterns  []string    `json:"existing_patterns"`
	Risks             []string    `json:"risks"`
	SuggestedTests    []string    `json:"suggested_tests"`
	Items             []ScoutItem `json:"items,omitempty"`
	DoNotBlock        bool        `json:"do_not_block,omitempty"`
}

type ScoutItem struct {
	Severity   string `json:"severity"`
	File       string `json:"file"`
	Line       int    `json:"line,omitempty"`
	Issue      string `json:"issue"`
	Suggestion string `json:"suggestion"`
}

type Review struct {
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
	Defaults DefaultsConfig           `toml:"defaults"`
	Profiles map[string]ProfileConfig `toml:"profiles"`
	Adapters AdapterConfigSet         `toml:"adapters"`
}

type DefaultsConfig struct {
	Mode              string `toml:"mode"`
	Coder             string `toml:"coder"`
	Adversary         string `toml:"adversary"`
	Worker            string `toml:"worker"`
	Scout             string `toml:"scout"`
	Supervisor        string `toml:"supervisor"`
	ScoutMode         string `toml:"scout_mode"`
	PostScoutMode     string `toml:"post_scout_mode"`
	SupervisorSlicing *bool  `toml:"supervisor_slicing"`
	MaxPackages       int    `toml:"max_packages"`
	Package           string `toml:"package"`
	AutoNextPackage   *bool  `toml:"auto_next_package"`
	Rounds            int    `toml:"rounds"`
	Test              string `toml:"test"`
	GitSafety         string `toml:"git_safety"`
}

type ProfileConfig struct {
	Mode              string `toml:"mode"`
	Coder             string `toml:"coder"`
	Adversary         string `toml:"adversary"`
	Worker            string `toml:"worker"`
	Scout             string `toml:"scout"`
	Supervisor        string `toml:"supervisor"`
	ScoutMode         string `toml:"scout_mode"`
	PostScoutMode     string `toml:"post_scout_mode"`
	SupervisorSlicing *bool  `toml:"supervisor_slicing"`
	MaxPackages       int    `toml:"max_packages"`
	Package           string `toml:"package"`
	AutoNextPackage   *bool  `toml:"auto_next_package"`
	Rounds            int    `toml:"rounds"`
	Test              string `toml:"test"`
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
	DefaultModel string   `toml:"default_model"`
	ExtraArgs    []string `toml:"extra_args"`
}

type ClaudeConfig struct {
	DefaultModel      string   `toml:"default_model"`
	CoderAllowedTools []string `toml:"coder_allowed_tools"`
	ExtraArgs         []string `toml:"extra_args"`
	Bare              bool     `toml:"bare"`
}

type AgyConfig struct {
	DefaultModel string   `toml:"default_model"`
	ExtraArgs    []string `toml:"extra_args"`
}

type GoslingConfig struct {
	DefaultModel string   `toml:"default_model"`
	ExtraArgs    []string `toml:"extra_args"`
}

type OpenAICompatibleConfig struct {
	BaseURL      string            `toml:"base_url"`
	APIKeyEnv    string            `toml:"api_key_env"`
	DefaultModel string            `toml:"default_model"`
	ExtraHeaders map[string]string `toml:"extra_headers"`
	ExtraArgs    []string          `toml:"extra_args"`
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
	Supervisor              string
	Reviewer                string
	SupervisorCanEdit       bool
	Slice                   bool
	NoSlice                 bool
	MaxPackages             int
	Package                 string
	AutoNextPackage         bool
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
	// Coder and Adversary hold the resolved editor and reviewer targets for
	// whichever mode is active (worker/supervisor in ModeSupervisor, coder/
	// adversary in ModeAdversarial, coder/supervisor in ModeRelay).
	Coder     RoleTarget
	Adversary RoleTarget
	Scout     RoleTarget
	// CoderExplicit and AdversaryExplicit mirror ModeExplicit for the role
	// targets: true when the caller passed -mc/--worker or
	// -ma/--supervisor/--reviewer for this invocation.
	CoderExplicit             bool
	AdversaryExplicit         bool
	ScoutExplicit             bool
	CoderExplicitMode         Mode
	AdversaryExplicitMode     Mode
	ScoutExplicitMode         Mode
	ScoutMode                 string
	PostScoutMode             string
	SupervisorCanEdit         bool
	SupervisorCanEditExplicit bool
	SupervisorSlicing         bool
	SupervisorSlicingExplicit bool
	MaxPackages               int
	Package                   string
	AutoNextPackage           bool
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
	ConfigSources             []string
	Baseline                  string
	SkipDirtyCheck            bool
}

type Meta struct {
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
	Baseline    string     `json:"baseline"`
	Head        string     `json:"head"`
	GeneratedAt time.Time  `json:"generated_at"`
	DiffSHA256  string     `json:"diff_sha256"`
	Files       []DiffFile `json:"files"`
}

type FinalRun struct {
	RunID    string `json:"run_id"`
	RunDir   string `json:"run_dir"`
	Workdir  string `json:"workdir"`
	Baseline string `json:"baseline"`
	Mode     Mode   `json:"mode,omitempty"`
	// Coder and Adversary persist the resolved editor/reviewer targets used
	// for this run so that `tagteam fix` can resume with the same mode and
	// adapters instead of re-resolving from the (possibly different)
	// current defaults/flags.
	Coder             RoleTarget         `json:"coder,omitempty"`
	Adversary         RoleTarget         `json:"adversary,omitempty"`
	Scout             RoleTarget         `json:"scout,omitempty"`
	SupervisorCanEdit bool               `json:"supervisor_can_edit,omitempty"`
	WorkPlan          *WorkPlan          `json:"work_plan,omitempty"`
	SelectedPackage   *WorkPackage       `json:"selected_package,omitempty"`
	RemainingPackages []string           `json:"remaining_packages,omitempty"`
	Verdict           string             `json:"verdict"`
	Summary           string             `json:"summary"`
	ExitCode          int                `json:"exit_code"`
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
	Role    string `json:"role"`
	Adapter string `json:"adapter"`
	Path    string `json:"path,omitempty"`
	Text    string `json:"text,omitempty"`
}

type LatestRun struct {
	RunID     string    `json:"run_id"`
	RunDir    string    `json:"run_dir"`
	FinalPath string    `json:"final_path"`
	Verdict   string    `json:"verdict"`
	ExitCode  int       `json:"exit_code"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (f FinalRun) MarshalJSON() ([]byte, error) {
	type alias FinalRun
	return json.Marshal(alias(f))
}
