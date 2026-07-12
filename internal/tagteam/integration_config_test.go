package tagteam

import (
	"bytes"
	"testing"
)

func TestIntegrationMarkedBlockPreservesUserContent(t *testing.T) {
	original := []byte("# user configuration\nsetting = true\n")
	plan, err := PlanIntegration("codex", original)
	if err != nil || !plan.Changed {
		t.Fatalf("plan = %#v, %v", plan, err)
	}
	if !bytes.Contains(plan.Content, original) {
		t.Fatalf("unowned content changed: %q", plan.Content)
	}
	doctor := DoctorIntegration("codex", plan.Content)
	if doctor.Status != "installed" {
		t.Fatalf("doctor = %#v", doctor)
	}
	removed, err := UninstallIntegration("codex", plan.Content)
	if err != nil || !bytes.Equal(removed.Content, original) {
		t.Fatalf("uninstall = %q, %v", removed.Content, err)
	}
	if DoctorIntegration("codex", []byte("# BEGIN tagteam managed integration\n")).Status != "corrupt" {
		t.Fatal("corrupt markers not detected")
	}
}

func TestIntegrationJSONPreservesUnknownKeys(t *testing.T) {
	plan, err := PlanIntegration("mcp-json", []byte(`{"other":{"keep":true},"mcpServers":{"existing":{"command":"x"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(plan.Content, []byte(`"other"`)) || !bytes.Contains(plan.Content, []byte(`"tagteam"`)) {
		t.Fatalf("JSON plan = %s", plan.Content)
	}
}

func TestIntegrationRoundTripsEveryTargetAndRefusesCorruption(t *testing.T) {
	for _, target := range []string{"codex", "claude", "cursor", "vscode", "mcp-json"} {
		t.Run(target, func(t *testing.T) {
			original := []byte("# user setting\n")
			if integrationUsesJSON(target) {
				original = []byte(`{"unknown":{"keep":true},"mcpServers":{"other":{"command":"other"}}}`)
			}
			installed, err := PlanIntegration(target, original)
			if err != nil || !installed.Changed || DoctorIntegration(target, installed.Content).Status != "installed" {
				t.Fatalf("install = %#v, %v", installed, err)
			}
			if integrationUsesJSON(target) && (!bytes.Contains(installed.Content, []byte(`"version": 1`)) || !bytes.Contains(installed.Content, []byte(`"unknown"`))) {
				t.Fatalf("structured config lost version or user key: %s", installed.Content)
			}
			removed, err := UninstallIntegration(target, installed.Content)
			if err != nil || DoctorIntegration(target, removed.Content).Status != "absent" {
				t.Fatalf("uninstall = %#v, %v", removed, err)
			}
		})
	}
	if _, err := PlanIntegration("cursor", []byte("{broken")); err == nil {
		t.Fatal("invalid JSON was replaced")
	}
	if _, err := UninstallIntegration("codex", []byte("# BEGIN tagteam managed integration\n")); err == nil {
		t.Fatal("corrupt markers were removed")
	}
	if got := DoctorIntegration("mcp-json", nil); got.Status != "absent" {
		t.Fatalf("empty JSON config = %#v", got)
	}
	for _, target := range []string{"claude cursor", "codex claude", "cursor/extra"} {
		if ValidIntegrationTargetForCLI(target) {
			t.Fatalf("invalid target was accepted: %q", target)
		}
	}
}
