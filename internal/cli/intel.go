package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cephalopod-ai/tagteam/internal/tagteam"
)

func newIntelCommand(shared *flagState) *cobra.Command {
	var runDir string
	intel := &cobra.Command{Use: "intel", Short: "Run opt-in code-intelligence providers and local bridge contracts", SilenceUsage: true}
	for _, operation := range []string{"orient", "find", "trace", "impact", "resume", "recall", "evidence"} {
		op := operation
		intel.AddCommand(&cobra.Command{Use: op + " [prompt]", Args: cobra.ArbitraryArgs, RunE: func(cmd *cobra.Command, args []string) error {
			cfg, workdir, err := loadIntelConfig(cmd, shared)
			if err != nil {
				return err
			}
			result := tagteam.RunCodeIntelGateway(context.Background(), cfg, workdir, joinArgs(args), op)
			data, err := tagteam.CodeIntelGatewayJSON(result)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), string(data))
			return nil
		}})
	}
	intel.AddCommand(&cobra.Command{Use: "bench", Short: "Write a deterministic configured-provider comparison artifact", RunE: func(cmd *cobra.Command, args []string) error {
		cfg, workdir, err := loadIntelConfig(cmd, shared)
		if err != nil {
			return err
		}
		target := runDir
		if target == "" {
			target = filepath.Join(tagteam.RunsRootForCLI(workdir), "intel-bench")
		}
		artifact, err := tagteam.RunCodeIntelBench(context.Background(), cfg, workdir, target)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(artifact)
	}})
	intel.AddCommand(&cobra.Command{Use: "status", Short: "Inspect recent code-intel artifacts without parsing final.json", RunE: func(cmd *cobra.Command, args []string) error {
		_, workdir, err := loadIntelConfig(cmd, shared)
		if err != nil {
			return err
		}
		root := tagteam.RunsRootForCLI(workdir)
		artifacts, err := tagteam.ReadCodeIntelStatus(root)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(artifacts)
	}})
	intel.AddCommand(&cobra.Command{Use: "checkpoint", Short: "Write a versioned, opt-in Dory checkpoint envelope", RunE: func(cmd *cobra.Command, args []string) error {
		cfg, workdir, err := loadIntelConfig(cmd, shared)
		if err != nil {
			return err
		}
		if runDir == "" {
			return fmt.Errorf("--run-dir is required")
		}
		artifact, err := readIntelArtifact(runDir)
		if err != nil {
			return err
		}
		if !tagteam.CodeIntelRepoAllowedForCLI(workdir, cfg.CodeIntel.AllowedRepos) {
			return fmt.Errorf("repository is not in code_intel.allowed_repos")
		}
		path, err := tagteam.WriteDoryCheckpoint(context.Background(), workdir, runDir, cfg.CodeIntel.Dory, artifact)
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), path)
		return nil
	}})
	intel.AddCommand(&cobra.Command{Use: "handoff", Short: "Validate and read a versioned Dory checkpoint envelope", RunE: func(cmd *cobra.Command, args []string) error {
		if runDir == "" {
			return fmt.Errorf("--run-dir is required")
		}
		envelope, artifact, err := tagteam.ReadDoryHandoff(filepath.Join(runDir, "dory-checkpoint.json"))
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
			Envelope tagteam.CodeIntelEnvelope `json:"envelope"`
			Artifact tagteam.CodeIntelArtifact `json:"artifact"`
		}{envelope, artifact})
	}})
	intel.PersistentFlags().StringVar(&runDir, "run-dir", "", "Run directory for bridge or benchmark artifacts")
	return intel
}

func loadIntelConfig(cmd *cobra.Command, shared *flagState) (tagteam.Config, string, error) {
	workdir, err := filepath.Abs(shared.Workdir)
	if err != nil {
		return tagteam.Config{}, "", err
	}
	changed := collectChangedFlags(cmd)
	cfg, _, err := tagteam.LoadConfigWithOptions(workdir, tagteam.LoadConfigOptions{TrustRepoConfig: shared.TrustRepoConfig && changed["trust-repo-config"]})
	return cfg, workdir, err
}

func readIntelArtifact(runDir string) (tagteam.CodeIntelArtifact, error) {
	data, err := os.ReadFile(filepath.Join(runDir, "code-intel-round-1.json"))
	if err != nil {
		return tagteam.CodeIntelArtifact{}, err
	}
	var artifact tagteam.CodeIntelArtifact
	return artifact, json.Unmarshal(data, &artifact)
}
func joinArgs(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return strings.Join(args, " ")
}
