package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/cephalopod-ai/tagteam/internal/tagteam"
)

type flagState struct {
	tagteam.FlagInputs
}

func NewRootCommand() *cobra.Command {
	flags := &flagState{}
	root := &cobra.Command{
		Use:   "tagteam [flags] <prompt>",
		Short: "Run supervisor/worker, relay, or coder/adversary agent loops over a repository",
		Long: `tagteam runs a repository-local loop of explicit coding roles. Instead of hiding the edit/review cycle inside a vendor UI, the CLI keeps the supervisor, worker, coder, adversary, and scout roles visible, saves the artifacts for each run, and makes the handoff between brief, implementation, and review inspectable from the terminal.

Modes

  supervisor (default)
    The supervisor writes the brief and reviews the diff. The worker implements the brief.
  relay
    --relay / --mode relay adds a scout recon pass before implementation, then runs coder implementation and supervisor review/arbitration.
  solo
    --solo / --mode solo runs one implementation agent with no reviewer, supervisor, adversary, or scout. The run reports review=none.
  adversarial
    --mode adversarial keeps the original coder/adversary loop for backward compatibility.

Role flags by mode

  -mc / --mc
    Implementation slot. Worker in supervisor and solo modes; coder in relay and adversarial modes.
  -ma / --ma
    Review slot. Supervisor in supervisor and relay modes; adversary in adversarial mode.
  --worker
    Preferred implementation-slot name for supervisor, relay, and solo modes; alias for --mc.
  --supervisor
    Preferred review-slot name for supervisor and relay modes; alias for --ma.
  --reviewer
    Adversarial-mode review-slot name; alias for --ma.
  --scout
    Relay-mode scout slot only. Prefer a large-context scout; 256k+ is recommended, ideally at least as large as the coder/supervisor context.

Operational behavior

  Supervisor and relay runs may make one host-controlled mode adjustment before implementation: relay can simplify to supervisor, and supervisor can escalate to relay when both worker and supervisor advisory signals agree. Runs persist final.json and state.json with degraded/blocking reason codes, role statuses, budgets, and artifact paths.

  Claude supervisors have a known JSON-output rough edge. tagteam does not silently repair that output; use --repair-json-with-worker to explicitly allow the selected worker to act as a read-only parser for invalid JSON contract artifacts.
`,
		Example: `tagteam "add OAuth login"
tagteam --worker codex:gpt-5-codex --supervisor claude:opus "refactor billing flow"
tagteam --solo codex:gpt-5.5 "rename UserSvc to UserService"
tagteam --relay --no-scout-retrieval --scout agy:gemini-3.5-flash-low --worker codex:gpt-5.4-mini --supervisor claude:sonnet "add OAuth login"
tagteam --mode adversarial -mc codex:gpt-5-codex -ma claude:opus "refactor billing flow"`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				_ = cmd.Help()
				return &tagteam.ExitError{Code: tagteam.ExitInvalidArguments}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDefault(cmd, flags, strings.Join(args, " "))
		},
	}

	bindSharedFlags(root, flags)
	root.AddCommand(newReviewCommand(flags))
	root.AddCommand(newFixCommand(flags))
	root.AddCommand(newStatusCommand(flags))
	root.AddCommand(newPlanCommand(flags))
	root.AddCommand(newTranscriptCommand(flags))
	root.AddCommand(newTUICommand(flags))
	root.AddCommand(newInitCommand(flags))
	root.AddCommand(newDoctorCommand(flags))
	return root
}

func bindSharedFlags(cmd *cobra.Command, flags *flagState) {
	flagSet := cmd.PersistentFlags()
	flagSet.StringVar(&flags.Mode, "mode", "", "Select orchestration mode: supervisor, relay, solo, or adversarial")
	flagSet.StringVar(&flags.Solo, "solo", "", "Shortcut for solo mode (one editor, no reviewer)")
	flagSet.BoolVar(&flags.Relay, "relay", false, "Shortcut for relay mode (scout, coder, supervisor)")
	flagSet.StringVar(&flags.Coder, "mc", "", "Legacy implementation slot: worker in supervisor/solo, coder in relay/adversarial")
	flagSet.StringVar(&flags.CoderRole, "coder", "", "Coder adapter[:model] (adversarial or relay mode)")
	flagSet.StringVar(&flags.Adversary, "ma", "", "Legacy review slot: supervisor in supervisor/relay, adversary in adversarial")
	flagSet.StringVar(&flags.Worker, "worker", "", "Preferred implementation slot in supervisor/relay/solo; alias for --mc")
	flagSet.StringVar(&flags.Scout, "scout", "", "Relay-mode scout adapter[:model] for pre-scout recon")
	flagSet.StringVar(&flags.ScoutMode, "scout-mode", "", "Pre-scout task mode: recon, lint, polish, tests, or risk")
	flagSet.StringVar(&flags.PostScoutMode, "post-scout-mode", "", "Post-scout task mode: recon, lint, polish, tests, or risk")
	flagSet.BoolVar(&flags.StrictScout, "strict-scout", false, "Abort relay when the scout model fails instead of continuing without scout context")
	flagSet.BoolVar(&flags.NoScoutRetrieval, "no-scout-retrieval", false, "Disable relay pre-scout recon retrieval (local rg-only, host-only, advisory)")
	flagSet.StringVar(&flags.ScoutContextPolicy, "scout-context-policy", "", "Relay scout context policy when configured limits are too small: warn, skip, or block")
	flagSet.BoolVar(&flags.TrustRepoConfig, "trust-repo-config", false, "Trust repo-local .tagteam.toml for tests, passthrough args, and adapter endpoints")
	flagSet.StringVar(&flags.Supervisor, "supervisor", "", "Preferred review slot in supervisor/relay; alias for --ma")
	flagSet.StringVar(&flags.Reviewer, "reviewer", "", "Adversarial-mode review slot; alias for --ma")
	flagSet.BoolVar(&flags.SupervisorCanEdit, "supervisor-can-edit", false, "Allow the supervisor to edit files while writing its brief (default: read-only)")
	flagSet.BoolVar(&flags.Slice, "slice", false, "Ask the supervisor to split supervisor-mode work into small packages before implementation")
	flagSet.BoolVar(&flags.NoSlice, "no-slice", false, "Disable supervisor-mode work-package slicing")
	flagSet.IntVar(&flags.MaxPackages, "max-packages", 0, "Maximum supervisor work packages when slicing (default 5)")
	flagSet.StringVar(&flags.Package, "package", "", "Work package ID to execute from the supervisor plan")
	flagSet.BoolVar(&flags.AutoNextPackage, "auto-next-package", false, "Continue through remaining work packages instead of stopping after the selected package")
	flagSet.BoolVar(&flags.RespectRepoInstructions, "respect-repo-instructions", true, "Load explicit repository instruction files and append them to role prompts")
	flagSet.BoolVar(&flags.NoRepoInstructions, "no-repo-instructions", false, "Disable repository instruction loading for this run")
	flagSet.BoolVar(&flags.DecisionMemory, "decision-memory", false, "Load optional .tagteam/decisions.jsonl into reviewer context")
	flagSet.IntVar(&flags.MaxFindings, "max-findings", 0, "Maximum findings retained from a review (default 50)")
	flagSet.Int64Var(&flags.MaxOutputBytes, "max-output-bytes", 0, "Maximum raw bytes accepted from one adapter call (default 2097152)")
	flagSet.DurationVar(&flags.MaxWallTime, "max-wall-time", 0, "Maximum total run wall time recorded/enforced when non-zero")
	flagSet.IntVar(&flags.MaxRoleInvocations, "max-role-invocations", 0, "Maximum adapter invocations for one run (0 means unlimited)")
	flagSet.BoolVar(&flags.RepairJSONWithWorker, "repair-json-with-worker", false, "Explicitly allow the selected worker to repair invalid JSON contract output in read-only parser mode")
	flagSet.StringVarP(&flags.Profile, "profile", "P", "", "Named profile")
	flagSet.StringVarP(&flags.Workdir, "workdir", "C", ".", "Working directory")
	flagSet.IntVarP(&flags.Rounds, "rounds", "r", 0, "Hard cap on implementation/review rounds before final no-edit reports")
	flagSet.StringVarP(&flags.Test, "test", "t", "", "Test command")
	flagSet.BoolVar(&flags.NoTest, "no-test", false, "Skip tests")
	flagSet.BoolVar(&flags.JSON, "json", false, "Print machine-readable final result")
	flagSet.BoolVar(&flags.DryRun, "dry-run", false, "Print vendor invocations without executing")
	flagSet.BoolVar(&flags.ShowReview, "show-review", false, "Include review findings in output")
	flagSet.BoolVar(&flags.FailOnReview, "fail-on-review", false, "Exit non-zero on blocking review findings")
	flagSet.BoolVar(&flags.AllowDirty, "allow-dirty", false, "Skip clean-worktree preflight")
	flagSet.BoolVar(&flags.Autostash, "autostash", false, "Stash local changes before run")
	flagSet.DurationVar(&flags.Timeout, "timeout", 15*time.Minute, "Per-invocation timeout")
	flagSet.BoolVarP(&flags.Quiet, "quiet", "q", false, "Reduce progress output")
	flagSet.BoolVarP(&flags.Verbose, "verbose", "v", false, "Increase progress output")
	flagSet.StringVar(&flags.CodexArgsRaw, "codex-args", "", "Raw args appended to codex invocations")
	flagSet.StringVar(&flags.ClaudeArgsRaw, "claude-args", "", "Raw args appended to claude invocations")
	flagSet.StringVar(&flags.AgyArgsRaw, "agy-args", "", "Raw args appended to agy invocations")
	flagSet.StringVar(&flags.GoslingArgsRaw, "gosling-args", "", "Raw args appended to gosling invocations")
	flagSet.StringVar(&flags.OpenAICompatibleArgsRaw, "openai-compatible-args", "", "Reserved passthrough args for openai-compatible invocations")
}

func runDefault(cmd *cobra.Command, flags *flagState, prompt string) error {
	opts, cfg, err := resolve(cmd, flags, prompt)
	if err != nil {
		return err
	}
	app := tagteam.NewApp(cfg)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	final, err := app.Run(ctx, opts)
	final = withErrorExitCode(final, err)
	renderFinal(cmd, final, opts)
	return err
}

func withErrorExitCode(final tagteam.FinalRun, err error) tagteam.FinalRun {
	if err != nil && final.RunID != "" && final.ExitCode == tagteam.ExitSuccess {
		final.ExitCode = tagteam.ExitCode(err)
	}
	return final
}

func newReviewCommand(shared *flagState) *cobra.Command {
	cmd := &cobra.Command{
		Use:          "review",
		Short:        "Reviewer-only review over the current diff (supervisor or adversary depending on --mode)",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, cfg, err := resolve(cmd, shared, "")
			if err != nil {
				return err
			}
			app := tagteam.NewApp(cfg)
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
			defer stop()
			final, err := app.Review(ctx, opts, "")
			final = withErrorExitCode(final, err)
			renderFinal(cmd, final, opts)
			return err
		},
	}
	return cmd
}

func newFixCommand(shared *flagState) *cobra.Command {
	cmd := &cobra.Command{
		Use:          "fix",
		Short:        "Editor applies fixes from the latest saved review (worker or coder depending on --mode)",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, cfg, err := resolve(cmd, shared, "")
			if err != nil {
				return err
			}
			app := tagteam.NewApp(cfg)
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
			defer stop()
			final, err := app.Fix(ctx, opts)
			final = withErrorExitCode(final, err)
			renderFinal(cmd, final, opts)
			return err
		},
	}
	return cmd
}

func newStatusCommand(shared *flagState) *cobra.Command {
	return &cobra.Command{
		Use:          "status",
		Short:        "Show the latest run summary",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			workdir, _ := filepath.Abs(shared.Workdir)
			latest, err := tagteam.ReadLatestForCLI(workdir)
			if err != nil {
				return err
			}
			final, err := tagteam.ReadFinalForCLI(latest.FinalPath)
			if err != nil {
				return err
			}
			renderFinal(cmd, final, tagteam.RunOptions{JSON: shared.JSON, ShowReview: shared.ShowReview})
			if !shared.JSON {
				if plan, err := tagteam.ReadPlanForCLI(latest.RunDir); err == nil {
					renderPlan(cmd, plan)
				}
			}
			return nil
		},
	}
}

func newPlanCommand(shared *flagState) *cobra.Command {
	return &cobra.Command{
		Use:          "plan [RUN_ID]",
		Short:        "Show the persisted checklist for a run",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			workdir, _ := filepath.Abs(shared.Workdir)
			runDir := ""
			if len(args) == 0 {
				latest, err := tagteam.ReadLatestForCLI(workdir)
				if err != nil {
					return err
				}
				runDir = latest.RunDir
			} else {
				runDir = filepath.Join(workdir, ".tagteam", "runs", args[0])
			}
			plan, err := tagteam.ReadPlanForCLI(runDir)
			if err != nil {
				return err
			}
			if shared.JSON {
				payload, _ := json.MarshalIndent(plan, "", "  ")
				fmt.Fprintln(cmd.OutOrStdout(), string(payload))
				return nil
			}
			renderPlan(cmd, plan)
			return nil
		},
	}
}

func newTranscriptCommand(shared *flagState) *cobra.Command {
	return &cobra.Command{
		Use:          "transcript [RUN_ID]",
		Short:        "Print the path to a saved transcript",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			workdir, _ := filepath.Abs(shared.Workdir)
			runDir := ""
			if len(args) == 0 {
				latest, err := tagteam.ReadLatestForCLI(workdir)
				if err != nil {
					return err
				}
				runDir = latest.RunDir
			} else {
				runDir = filepath.Join(workdir, ".tagteam", "runs", args[0])
			}
			fmt.Fprintln(cmd.OutOrStdout(), runDir)
			return nil
		},
	}
}

func newDoctorCommand(shared *flagState) *cobra.Command {
	return &cobra.Command{
		Use:          "doctor",
		Short:        "Check adapter binaries and auth hints",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, cfg, err := resolve(cmd, shared, "")
			if err != nil {
				return err
			}
			app := tagteam.NewApp(cfg)
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
			defer stop()
			status, err := app.Doctor(ctx, opts)
			for _, key := range []string{"codex", "codex-oss", "claude", "agy", "gosling", "openai-compatible"} {
				item := status[key]
				fmt.Fprintf(cmd.OutOrStdout(), "%s\tfound=%t\tversion=%s\tauth=%s\thint=%s\n", key, item.Found, item.Version, item.Auth, item.Hint)
			}
			return err
		},
	}
}

func newInitCommand(shared *flagState) *cobra.Command {
	return &cobra.Command{
		Use:          "init",
		Short:        "Write a starter user config",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			workdir, _ := filepath.Abs(shared.Workdir)
			cfg, _, err := tagteam.LoadConfig(workdir)
			if err != nil {
				return err
			}
			path, err := tagteam.UserConfigPathForCLI()
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			data, err := tagteam.EncodeConfig(cfg)
			if err != nil {
				return err
			}
			if err := os.WriteFile(path, data, 0o644); err != nil {
				return err
			}
			if err := tagteam.EnsureGitignoreEntryForCLI(workdir, ".tagteam/"); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), path)
			return nil
		},
	}
}

func resolve(cmd *cobra.Command, flags *flagState, prompt string) (tagteam.RunOptions, tagteam.Config, error) {
	workdir, err := filepath.Abs(flags.Workdir)
	if err != nil {
		return tagteam.RunOptions{}, tagteam.Config{}, err
	}
	changed := collectChangedFlags(cmd)
	cfg, sources, err := tagteam.LoadConfigWithOptions(workdir, tagteam.LoadConfigOptions{
		TrustRepoConfig: flags.TrustRepoConfig && changed["trust-repo-config"],
	})
	if err != nil {
		return tagteam.RunOptions{}, tagteam.Config{}, err
	}
	opts, err := tagteam.ResolveOptions(cfg, sources, flags.FlagInputs, changed, prompt)
	if err != nil {
		return tagteam.RunOptions{}, tagteam.Config{}, err
	}
	return opts, cfg, nil
}

func collectChangedFlags(cmd *cobra.Command) map[string]bool {
	changed := map[string]bool{}
	cmd.Flags().VisitAll(func(flag *pflag.Flag) {
		if flag.Changed {
			changed[flag.Name] = true
		}
	})
	cmd.InheritedFlags().VisitAll(func(flag *pflag.Flag) {
		if flag.Changed {
			changed[flag.Name] = true
		}
	})
	return changed
}

func renderFinal(cmd *cobra.Command, final tagteam.FinalRun, opts tagteam.RunOptions) {
	if final.RunID == "" {
		return
	}
	if opts.JSON {
		payload, _ := json.MarshalIndent(final, "", "  ")
		fmt.Fprintln(cmd.OutOrStdout(), string(payload))
		return
	}
	fmt.Fprintf(cmd.OutOrStdout(), "run=%s verdict=%s status=%s exit=%d rounds=%d/%d\n", final.RunID, final.Verdict, final.Status, final.ExitCode, final.RoundsCompleted, final.RoundsRequested)
	if final.Summary != "" {
		fmt.Fprintln(cmd.OutOrStdout(), final.Summary)
	}
	if final.Degraded {
		fmt.Fprintf(cmd.OutOrStdout(), "degraded=true reason=%s\n", final.DegradedReason)
	}
	if final.BlockingReason != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "blocking_reason=%s\n", final.BlockingReason)
	}
	if final.RunDir != "" {
		fmt.Fprintln(cmd.OutOrStdout(), final.RunDir)
	}
	if final.Mode == tagteam.ModeSolo {
		fmt.Fprintln(cmd.OutOrStdout(), "review=none")
	}
	if final.SelectedPackage != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "package=%s %s\n", final.SelectedPackage.ID, final.SelectedPackage.Title)
	}
	if len(final.RemainingPackages) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "remaining-packages=%s\n", strings.Join(final.RemainingPackages, "; "))
	}
	if final.Plan != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "plan=%s status=%s items=%d passed=%d failed=%d deferred=%d arbitration=%d\n", final.Plan.Path, final.Plan.Status, final.Plan.Total, final.Plan.Passed, final.Plan.Failed, final.Plan.Deferred, final.Plan.Arbitration)
	}
	if opts.ShowReview && final.Review != nil {
		for _, finding := range final.Review.Findings {
			fmt.Fprintf(cmd.OutOrStdout(), "- [%s] %s:%d %s\n", finding.Severity, finding.File, finding.Line, finding.Issue)
		}
	}
}

func renderPlan(cmd *cobra.Command, plan tagteam.ExecutionPlan) {
	fmt.Fprintf(cmd.OutOrStdout(), "plan-run=%s mode=%s status=%s items=%d\n", plan.RunID, plan.Mode, plan.Status, len(plan.Items))
	for _, item := range plan.Items {
		fmt.Fprintf(cmd.OutOrStdout(), "[%s] %-17s %s\n", item.ID, item.Status, item.Title)
	}
}
