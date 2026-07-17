package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cephalopod-ai/tagteam/internal/tagteam"
)

func newMCPCommand(shared *flagState) *cobra.Command {
	var socketPath string
	cmd := &cobra.Command{
		Use:          "mcp",
		Short:        "Serve bounded Tagteam control tools over local MCP stdio or a unix socket",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			workdir, err := filepath.Abs(shared.Workdir)
			if err != nil {
				return fmt.Errorf("resolve MCP workdir: %w", err)
			}
			// Bind the server to the real Git worktree root, never a model- or
			// flag-supplied execution directory that has not been canonicalized.
			repository, err := tagteam.ResolveControlRepository(workdir)
			if err != nil {
				return fmt.Errorf("resolve MCP repository: %w", err)
			}
			service := tagteam.ControlService{
				RepositoryRoot:  repository.CanonicalRoot,
				StateRoot:       shared.StateRoot,
				ProducerVersion: Version,
			}
			var runtime *tagteam.ControlRuntime
			if err := requireVerifiedInstallation(shared); err == nil {
				changed := collectChangedFlags(cmd)
				cfg, sources, configErr := tagteam.LoadConfigWithOptions(repository.CanonicalRoot, tagteam.LoadConfigOptions{
					TrustRepoConfig: shared.TrustRepoConfig && changed["trust-repo-config"],
				})
				if configErr != nil {
					return fmt.Errorf("load MCP start configuration: %w", configErr)
				}
				runtime = tagteam.NewControlRuntime(service, cfg, sources)
			}

			// Socket mode makes the process a small local daemon that owns runs;
			// the MCP endpoint is a thin client transport, so a client can
			// disconnect and reconnect (or another attach) without ending a run.
			if strings.TrimSpace(socketPath) != "" {
				listener, err := tagteam.ListenMCPUnixSocket(socketPath)
				if err != nil {
					return fmt.Errorf("listen on MCP socket: %w", err)
				}
				defer listener.Close()
				fmt.Fprintf(cmd.ErrOrStderr(), "tagteam mcp: serving on unix socket %s\n", socketPath)
				if err := tagteam.ServeMCPSocket(cmd.Context(), listener, service, runtime); err != nil && !errors.Is(err, context.Canceled) {
					return err
				}
				return nil
			}

			server := tagteam.NewMCPStdioServer(service, cmd.InOrStdin(), cmd.OutOrStdout())
			if runtime != nil {
				server.WithOwnedRuntime(runtime)
			}
			return server.Serve(cmd.Context())
		},
	}
	cmd.Flags().StringVar(&socketPath, "socket", "", "Serve over a unix domain socket at this path instead of stdio")
	return cmd
}
