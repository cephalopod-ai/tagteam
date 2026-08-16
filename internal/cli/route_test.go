package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func runRouteCommand(t *testing.T, workdir string, args ...string) (string, error) {
	t.Helper()
	cmd := NewRootCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{"route", "-C", workdir}, args...))
	err := cmd.Execute()
	return out.String(), err
}

func isolatedWorkdir(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	// Keep the test off the operator's real user config.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "config"))
	return tmp
}

func TestRouteListShowsJobsAndAgents(t *testing.T) {
	out, err := runRouteCommand(t, isolatedWorkdir(t), "--list")
	if err != nil {
		t.Fatalf("route --list: %v\n%s", err, out)
	}
	for _, want := range []string{"jobs:", "scoped_patch", "audit", "deep_scan", "agents:", "gpt-terra", "opus"} {
		if !strings.Contains(out, want) {
			t.Fatalf("route --list output is missing %q:\n%s", want, out)
		}
	}
}

func TestRouteExplainsASelectedJob(t *testing.T) {
	out, err := runRouteCommand(t, isolatedWorkdir(t), "--job", "audit")
	if err != nil {
		t.Fatalf("route --job audit: %v\n%s", err, out)
	}
	for _, want := range []string{"job:", "audit", "workflow: adversarial", "coder", "adversary", "reason:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("route output is missing %q:\n%s", want, out)
		}
	}
}

func TestRouteJSONEmitsTheDecisionContract(t *testing.T) {
	out, err := runRouteCommand(t, isolatedWorkdir(t), "--job", "scoped_patch", "--json")
	if err != nil {
		t.Fatalf("route --json: %v\n%s", err, out)
	}
	var decision struct {
		SchemaVersion int    `json:"schema_version"`
		Job           string `json:"job"`
		Workflow      string `json:"workflow"`
		Roles         []struct {
			Slot     string `json:"slot"`
			Selected string `json:"selected"`
			Source   string `json:"source"`
		} `json:"roles"`
	}
	if err := json.Unmarshal([]byte(out), &decision); err != nil {
		t.Fatalf("route --json emitted invalid JSON: %v\n%s", err, out)
	}
	if decision.SchemaVersion == 0 || decision.Job != "scoped_patch" || decision.Workflow != "supervisor" {
		t.Fatalf("unexpected decision payload: %+v", decision)
	}
	if len(decision.Roles) != 2 {
		t.Fatalf("expected two staffed slots, got %d", len(decision.Roles))
	}
	for _, role := range decision.Roles {
		if role.Selected == "" || role.Source == "" {
			t.Fatalf("slot %s is incomplete: %+v", role.Slot, role)
		}
	}
}

func TestRouteRequiresAJobOrList(t *testing.T) {
	out, err := runRouteCommand(t, isolatedWorkdir(t))
	if err == nil {
		t.Fatalf("expected an error without --job or --list:\n%s", out)
	}
	if !strings.Contains(err.Error(), "--job") {
		t.Fatalf("error should mention --job, got %v", err)
	}
}

func TestRouteExcludeIsHonored(t *testing.T) {
	workdir := isolatedWorkdir(t)
	out, err := runRouteCommand(t, workdir, "--job", "scoped_patch", "--route-exclude", "gpt-terra")
	if err != nil {
		t.Fatalf("route with exclusion: %v\n%s", err, out)
	}
	if strings.Contains(out, "target:    codex:gpt-5.6-terra") {
		t.Fatalf("excluded agent was still selected:\n%s", out)
	}
	if !strings.Contains(out, `excluded by "gpt-terra"`) {
		t.Fatalf("exclusion reason is not reported:\n%s", out)
	}
}
