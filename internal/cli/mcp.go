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
			server := tagteam.NewMCPStdioServer(tagteam.ControlService{
				RepositoryRoot:  workdir,
				StateRoot:       shared.StateRoot,
				ProducerVersion: Version,
			}, cmd.InOrStdin(), cmd.OutOrStdout())
			return server.Serve(cmd.Context())
		},
	}
}
