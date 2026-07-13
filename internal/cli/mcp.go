package cli

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/cephalopod-ai/tagteam/internal/tagteam"
)

func newMCPCommand(shared *flagState) *cobra.Command {
	return &cobra.Command{
		Use:          "mcp",
		Short:        "Serve bounded Tagteam control tools over local MCP stdio",
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
			server := tagteam.NewMCPStdioServer(service, cmd.InOrStdin(), cmd.OutOrStdout())
			if err := requireVerifiedInstallation(shared); err == nil {
				changed := collectChangedFlags(cmd)
				cfg, sources, configErr := tagteam.LoadConfigWithOptions(repository.CanonicalRoot, tagteam.LoadConfigOptions{
					TrustRepoConfig: shared.TrustRepoConfig && changed["trust-repo-config"],
				})
				if configErr != nil {
					return fmt.Errorf("load MCP start configuration: %w", configErr)
				}
				server.WithRuntime(tagteam.NewControlRuntime(service, cfg, sources))
			}
			return server.Serve(cmd.Context())
		},
	}
}
