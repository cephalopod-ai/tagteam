package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"tagteam/internal/tagteam"
)

type flagState struct {
	tagteam.FlagInputs
}

func NewRootCommand() *cobra.Command {
	flags := &flagState{}
	root := &cobra.Command{
		Use:           "tagteam [flags] <prompt>",
		Short:         "Run supervisor/worker, relay, or coder/adversary agent loops over a repository",
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
	root.AddCommand(newTranscriptCommand(flags))
	root.AddCommand(newInitCommand(flags))
	root.AddCommand(newDoctorCommand(flags))
	return root
}

func bindSharedFlags(cmd *cobra.Command, flags *flagState) {
	flagSet := cmd.PersistentFlags()
	flagSet.StringVar(&flags.Mode, "mode", "", "Orchestration mode: supervisor (default), solo, adversarial, or relay")
	flagSet.StringVar(&flags.Solo, "solo", "", "Run one implementation adapter[:model] with no reviewer")
	flagSet.BoolVar(&flags.Relay, "relay", false, "Convenience alias for --mode relay")
	flagSet.StringVar(&flags.Coder, "mc", "", "Editor adapter[:model] (coder in adversarial mode, worker in supervisor mode)")
	flagSet.StringVar(&flags.CoderRole, "coder", "", "Coder adapter[:model] (adversarial or relay mode)")
	flagSet.StringVar(&flags.Adversary, "ma", "", "Reviewer adapter[:model] (adversary in adversarial mode, supervisor in supervisor mode)")
	flagSet.StringVar(&flags.Worker, "worker", "", "Worker/coder adapter[:model] (supervisor or relay mode; alias for --mc)")
	flagSet.StringVar(&flags.Scout, "scout", "", "Scout adapter[:model] (relay mode only)")
	flagSet.StringVar(&flags.ScoutMode, "scout-mode", "", "Pre-scout task mode: recon, lint, polish, tests, or risk")
	flagSet.StringVar(&flags.PostScoutMode, "post-scout-mode", "", "Post-scout task mode: recon, lint, polish, tests, or risk")
	flagSet.StringVar(&flags.Supervisor, "supervisor", "", "Supervisor adapter[:model] (supervisor or relay mode; alias for --ma)")
	flagSet.StringVar(&flags.Reviewer, "reviewer", "", "Reviewer adapter[:model] (adversarial mode only; alias for --ma)")
	flagSet.BoolVar(&flags.SupervisorCanEdit, "supervisor-can-edit", false, "Allow the supervisor to edit files while writing its brief (default: read-only)")
	flagSet.BoolVar(&flags.Slice, "slice", false, "Ask the supervisor to split supervisor-mode work into small packages before implementation")
	flagSet.BoolVar(&flags.NoSlice, "no-slice", false, "Disable supervisor-mode work-package slicing")
	flagSet.IntVar(&flags.MaxPackages, "max-packages", 0, "Maximum supervisor work packages when slicing (default 5)")
	flagSet.StringVar(&flags.Package, "package", "", "Work package ID to execute from the supervisor plan")
	flagSet.BoolVar(&flags.AutoNextPackage, "auto-next-package", false, "Continue through remaining work packages instead of stopping after the selected package")
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
	final, err := app.Run(context.Background(), opts)
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
			final, err := app.Review(context.Background(), opts, "")
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
			final, err := app.Fix(context.Background(), opts)
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
			status, err := app.Doctor(context.Background(), opts)
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
	cfg, sources, err := tagteam.LoadConfig(workdir)
	if err != nil {
		return tagteam.RunOptions{}, tagteam.Config{}, err
	}
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
	opts, err := tagteam.ResolveOptions(cfg, sources, flags.FlagInputs, changed, prompt)
	if err != nil {
		return tagteam.RunOptions{}, tagteam.Config{}, err
	}
	return opts, cfg, nil
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
	fmt.Fprintf(cmd.OutOrStdout(), "run=%s verdict=%s exit=%d rounds=%d/%d\n", final.RunID, final.Verdict, final.ExitCode, final.RoundsCompleted, final.RoundsRequested)
	if final.Summary != "" {
		fmt.Fprintln(cmd.OutOrStdout(), final.Summary)
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
	if opts.ShowReview && final.Review != nil {
		for _, finding := range final.Review.Findings {
			fmt.Fprintf(cmd.OutOrStdout(), "- [%s] %s:%d %s\n", finding.Severity, finding.File, finding.Line, finding.Issue)
		}
	}
}
