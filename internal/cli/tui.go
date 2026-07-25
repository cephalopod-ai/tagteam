package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/cephalopod-ai/tagteam/internal/tagteam"
	"github.com/cephalopod-ai/tagteam/internal/tui"
)

func newTUICommand(shared *flagState) *cobra.Command {
	return &cobra.Command{
		Use:   "tui [RUN_ID]",
		Short: "Interactive terminal dashboard for launching and inspecting tagteam runs",
		Long: `tui opens an interactive dashboard with a compose-first home screen, an on-demand settings panel, a recent-runs picker, and a scrollable run-detail view.

With no RUN_ID it starts in compose mode, surfaces the active/latest run as context, and lets you open run details explicitly through recent runs or slash commands. Passing a RUN_ID opens that run immediately.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			workdir, err := filepath.Abs(shared.Workdir)
			if err != nil {
				return err
			}
			runDir, err := resolveTUIRunDir(workdir, args)
			if err != nil {
				return err
			}
			ctx, stop := commandSignalContext(cmd.Context())
			defer stop()
			mutationBlocked := ""
			if _, verifyErr := verifyInstallation(shared.AllowDevBuild); verifyErr != nil {
				mutationBlocked = verifyErr.Error()
			}
			inspectOnStart := shouldInspectTUIRun(workdir, runDir, len(args) > 0)
			return tui.Run(ctx, tui.RunOptions{
				Workdir:         workdir,
				InitialRunDir:   runDir,
				InspectOnStart:  inspectOnStart,
				Flags:           shared.FlagInputs,
				Changed:         collectChangedFlags(cmd),
				TrustRepoConfig: shared.TrustRepoConfig && collectChangedFlags(cmd)["trust-repo-config"],
				MutationBlocked: mutationBlocked,
			}, os.Stdout, os.Stdin)
		},
	}
}

func shouldInspectTUIRun(workdir, runDir string, explicit bool) bool {
	if explicit {
		return true
	}
	if runDir == "" {
		return false
	}
	active, err := tagteam.ReadActiveRunForCLI(workdir)
	return err == nil && active.Status == "running" && active.RunDir == runDir
}

// resolveTUIRunDir picks the run directory tui should display: an explicit
// RUN_ID, otherwise the active (running) run if one exists, otherwise the
// most recent completed run.
func resolveTUIRunDir(workdir string, args []string) (string, error) {
	if len(args) > 0 {
		runID := args[0]
		runDir, resolveErr := tagteam.RunDirForCLI(workdir, runID)
		if resolveErr != nil {
			return "", resolveErr
		}
		info, err := os.Stat(runDir)
		if err != nil || !info.IsDir() {
			return "", fmt.Errorf("run %q not found under %s", runID, tagteam.RunsRootForCLI(workdir))
		}
		return runDir, nil
	}
	if active, err := tagteam.ReadActiveRunForCLI(workdir); err == nil && active.Status == "running" && active.RunDir != "" {
		return active.RunDir, nil
	}
	latest, err := tagteam.ReadLatestForCLI(workdir)
	if err != nil {
		return "", nil
	}
	return latest.RunDir, nil
}
