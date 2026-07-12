package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cephalopod-ai/tagteam/internal/tagteam"
)

func newIntegrateCommand() *cobra.Command {
	var target, path string
	root := &cobra.Command{Use: "integrate", Short: "Plan and manage non-destructive Tagteam editor integration blocks", SilenceUsage: true}
	run := func(action string) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) error {
			if !tagteam.ValidIntegrationTargetForCLI(target) {
				return fmt.Errorf("--target must be codex, claude, cursor, vscode, or mcp-json")
			}
			if path == "" {
				return fmt.Errorf("--path is required; Tagteam never guesses or rewrites agent configuration")
			}
			existing, err := os.ReadFile(path)
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			var result tagteam.IntegrationResult
			switch action {
			case "plan", "install":
				result, err = tagteam.PlanIntegration(target, existing)
			case "doctor":
				result = tagteam.DoctorIntegration(target, existing)
			case "uninstall":
				result, err = tagteam.UninstallIntegration(target, existing)
			}
			if err != nil {
				return err
			}
			if action == "install" || action == "uninstall" {
				if result.Changed {
					if err := tagteam.WriteFileDurableForCLI(path, result.Content, 0o644); err != nil {
						return err
					}
				}
			}
			if action == "install" {
				if result.Changed {
					result.Status = "installed"
				} else {
					result.Status = "unchanged"
				}
			}
			if action == "plan" {
				fmt.Fprintln(cmd.OutOrStdout(), unifiedDiff(existing, result.Content))
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		}
	}
	for _, action := range []string{"plan", "install", "doctor", "uninstall"} {
		root.AddCommand(&cobra.Command{Use: action, RunE: run(action)})
	}
	root.PersistentFlags().StringVar(&target, "target", "", "Integration target: codex, claude, cursor, vscode, or mcp-json")
	root.PersistentFlags().StringVar(&path, "path", "", "Explicit configuration file path")
	return root
}

func unifiedDiff(before, after []byte) string {
	if bytes.Equal(before, after) {
		return "(no changes)"
	}
	beforeLines := splitDiffLines(before)
	afterLines := splitDiffLines(after)
	lengths := make([][]int, len(beforeLines)+1)
	for i := range lengths {
		lengths[i] = make([]int, len(afterLines)+1)
	}
	for i := len(beforeLines) - 1; i >= 0; i-- {
		for j := len(afterLines) - 1; j >= 0; j-- {
			if beforeLines[i] == afterLines[j] {
				lengths[i][j] = lengths[i+1][j+1] + 1
			} else if lengths[i+1][j] >= lengths[i][j+1] {
				lengths[i][j] = lengths[i+1][j]
			} else {
				lengths[i][j] = lengths[i][j+1]
			}
		}
	}
	var out bytes.Buffer
	out.WriteString("--- existing\n+++ planned\n")
	for i, j := 0, 0; i < len(beforeLines) || j < len(afterLines); {
		if i < len(beforeLines) && j < len(afterLines) && beforeLines[i] == afterLines[j] {
			out.WriteString(" " + beforeLines[i] + "\n")
			i++
			j++
		} else if j < len(afterLines) && (i == len(beforeLines) || lengths[i][j+1] >= lengths[i+1][j]) {
			out.WriteString("+" + afterLines[j] + "\n")
			j++
		} else {
			out.WriteString("-" + beforeLines[i] + "\n")
			i++
		}
	}
	if bytes.HasSuffix(before, []byte("\n")) != bytes.HasSuffix(after, []byte("\n")) {
		out.WriteString("\\ No newline at end of file\n")
	}
	return out.String()
}

func splitDiffLines(data []byte) []string {
	text := string(data)
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}
