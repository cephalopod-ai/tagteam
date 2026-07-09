package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestNewRootCommandHelpIncludesModeModelAndFlags(t *testing.T) {
	cmd := NewRootCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"-h"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute help: %v", err)
	}

	help := out.String()
	checks := []string{
		"Modes",
		"supervisor (default)",
		"relay",
		"solo",
		"adversarial",
		"Role flags by mode",
		"--mode",
		"--model",
		"--worker",
		"--supervisor",
		"--mc",
		"--ma",
		"--reviewer",
		"--scout",
		"--no-scout-retrieval",
		"tagteam --solo codex:gpt-5.6-terra",
		"tagteam run -m claude:claude-sonnet-5",
		"tagteam --relay --no-scout-retrieval",
		"tagteam --mode adversarial -mc codex:gpt-5.6-terra -ma claude:claude-opus-4-8",
	}
	for _, want := range checks {
		if !strings.Contains(help, want) {
			t.Fatalf("help output missing %q\nfull output:\n%s", want, help)
		}
	}
}

func TestRunCommandAndModelFlagUseExistingRunSurface(t *testing.T) {
	cmd := NewRootCommand()
	run, _, err := cmd.Find([]string{"run"})
	if err != nil {
		t.Fatalf("find run command: %v", err)
	}
	if run == nil || run.Use != "run <prompt>" {
		t.Fatalf("run command = %#v", run)
	}
	model := cmd.PersistentFlags().Lookup("model")
	if model == nil || model.Shorthand != "m" {
		t.Fatalf("model flag = %#v", model)
	}
}

func TestVersionCommandAndFlag(t *testing.T) {
	t.Run("version subcommand", func(t *testing.T) {
		cmd := NewRootCommand()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{"version"})

		if err := cmd.Execute(); err != nil {
			t.Fatalf("execute version command: %v", err)
		}

		got := strings.TrimSpace(out.String())
		if got != Version {
			t.Errorf("version command output got %q, want %q", got, Version)
		}
	})

	t.Run("--version flag", func(t *testing.T) {
		cmd := NewRootCommand()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{"--version"})

		if err := cmd.Execute(); err != nil {
			t.Fatalf("execute version flag: %v", err)
		}

		got := strings.TrimSpace(out.String())
		if !strings.Contains(got, Version) {
			t.Errorf("version flag output got %q, should contain %q", got, Version)
		}
	})
}
