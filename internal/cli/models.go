package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/cephalopod-ai/tagteam/internal/tagteam"
)

var discoverModelCatalogs = tagteam.DiscoverModelCatalogs

func newModelsCommand(shared *flagState) *cobra.Command {
	discoveryTimeout := 20 * time.Second
	cmd := &cobra.Command{
		Use:          "models",
		Short:        "List configured and provider-discovered model IDs",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			workdir, err := filepath.Abs(shared.Workdir)
			if err != nil {
				return err
			}
			changed := collectChangedFlags(cmd)
			cfg, _, err := tagteam.LoadConfigWithOptions(workdir, tagteam.LoadConfigOptions{
				TrustRepoConfig: shared.TrustRepoConfig && changed["trust-repo-config"],
			})
			if err != nil {
				return err
			}
			ctx, stop := commandSignalContext(context.Background())
			defer stop()
			if discoveryTimeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, discoveryTimeout)
				defer cancel()
			}
			catalog := discoverModelCatalogs(ctx, cfg, workdir)
			return renderModelCatalog(cmd, shared.JSON, catalog)
		},
	}
	cmd.Flags().DurationVar(&discoveryTimeout, "discovery-timeout", discoveryTimeout, "Maximum time for concurrent live provider discovery (0 disables the limit)")
	return cmd
}

func renderModelCatalog(cmd *cobra.Command, asJSON bool, catalog []tagteam.ModelCatalogEntry) error {
	if asJSON {
		payload, err := json.MarshalIndent(catalog, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), string(payload))
		return nil
	}
	for _, entry := range catalog {
		defaultLabel := ""
		if entry.Default != "" {
			defaultLabel = " default=" + entry.Default
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s\tsource=%s%s\n", entry.Adapter, entry.Source, defaultLabel)
		for _, model := range entry.Models {
			marker := "  - "
			if model == entry.Default {
				marker = "  * "
			}
			fmt.Fprintln(cmd.OutOrStdout(), marker+model)
		}
		if warning := strings.TrimSpace(entry.Error); warning != "" {
			fmt.Fprintln(cmd.OutOrStdout(), "  warning: "+warning)
		}
	}
	return nil
}
