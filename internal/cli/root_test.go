package cli

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"

	"github.com/cephalopod-ai/tagteam/internal/tagteam"
)

func initGitWorktree(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

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
		"tagteam run -m agy:gemini-3.6-flash-medium",
		"tagteam --relay --scout openai-compatible:gemma4:latest",
		"tagteam --mode adversarial -mc codex:gpt-5.6-terra -ma codex:gpt-5.6-sol",
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

func TestTestFlagAcceptsIndependentParallelCommands(t *testing.T) {
	cmd := NewRootCommand()
	testFlag := cmd.PersistentFlags().Lookup("test")
	if testFlag == nil || testFlag.Value.Type() != "stringArray" {
		t.Fatalf("test flag = %#v, want repeatable stringArray", testFlag)
	}
	if !strings.Contains(testFlag.Usage, "concurrently") {
		t.Fatalf("test flag usage = %q", testFlag.Usage)
	}
}

func TestMCPCommandUsesLocalStdioSurface(t *testing.T) {
	cmd := NewRootCommand()
	mcp, _, err := cmd.Find([]string{"mcp"})
	if err != nil {
		t.Fatal(err)
	}
	if mcp == nil || mcp.Use != "mcp" {
		t.Fatalf("mcp command = %#v", mcp)
	}
}

func TestMCPCommandGatesStartOnVerifiedInstallation(t *testing.T) {
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
	}, "\n") + "\n"
	for _, test := range []struct {
		name      string
		args      []string
		wantStart bool
	}{
		{name: "unverified read only", args: []string{"mcp"}, wantStart: false},
		{name: "explicit development override", args: []string{"mcp", "--allow-dev-build"}, wantStart: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			workdir := t.TempDir()
			// MCP binds to a real Git worktree root at server start.
			initGitWorktree(t, workdir)
			args := append(append([]string{}, test.args...), "--workdir", workdir)
			cmd := NewRootCommand()
			var out bytes.Buffer
			cmd.SetIn(strings.NewReader(input))
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs(args)
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(out.String(), `"name":"tagteam_start"`); got != test.wantStart {
				t.Fatalf("start advertisement = %t, want %t; output=%s", got, test.wantStart, out.String())
			}
		})
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

func TestRenderRunSnapshotIncludesLiveProgress(t *testing.T) {
	cmd := NewRootCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)

	renderRunSnapshot(cmd, tagteam.RunSnapshot{
		RunID:        "run-active",
		RunDir:       "/tmp/run-active",
		Mode:         tagteam.ModeAdversarial,
		Status:       "running",
		Phase:        "reviewing",
		CurrentRound: 1,
		LiveProgress: &tagteam.LiveProgress{
			Role:          tagteam.RoleAdversary,
			Status:        "running",
			Elapsed:       "2m0s",
			NoProgressFor: "1m30s",
			FilesChanged:  3,
			Additions:     12,
			Deletions:     4,
		},
	}, false)

	got := out.String()
	for _, want := range []string{
		"run=run-active",
		"status=running",
		"phase=reviewing round=1",
		"progress role=adversary status=running elapsed=2m0s idle=1m30s files=3 +12 -4",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("status output missing %q\nfull output:\n%s", want, got)
		}
	}
}

func TestRenderRunSnapshotIncludesQueueContext(t *testing.T) {
	cmd := NewRootCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	renderRunSnapshot(cmd, tagteam.RunSnapshot{LiveProgress: &tagteam.LiveProgress{
		Role:       tagteam.RoleCoder,
		Status:     "waiting",
		WaitingFor: "claude",
		HolderPID:  4242,
		Elapsed:    "8s",
	}}, false)
	if got := out.String(); !strings.Contains(got, "queued_for=claude holder_pid=4242") {
		t.Fatalf("queue context missing from status output:\n%s", got)
	}
}

func TestRenderRunSnapshotIncludesHostActivity(t *testing.T) {
	cmd := NewRootCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	renderRunSnapshot(cmd, tagteam.RunSnapshot{HostActivity: &tagteam.HostActivity{
		Actor:        "tagteam-host",
		Phase:        "baseline-test",
		Status:       "integrity_violation",
		Elapsed:      "3s",
		Command:      "pytest -q",
		OutputPath:   "/tmp/run/baseline-test.txt",
		ChangedFiles: []string{"working/registry.yaml", "archived/registry.yaml"},
		Error:        "repository mutation",
	}}, false)

	got := out.String()
	for _, want := range []string{
		"host actor=tagteam-host phase=baseline-test status=integrity_violation elapsed=3s",
		"host_command=pytest -q output=/tmp/run/baseline-test.txt",
		"host_mutated_files=working/registry.yaml,archived/registry.yaml",
		"host_error=repository mutation",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("status output missing %q\nfull output:\n%s", want, got)
		}
	}
}
