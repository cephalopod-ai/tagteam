package tagteam

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// Resolve through the same config and registry path as a run. Bare adapter
// targets use current defaults, while explicit model pins reach the CLI intact.
func TestModelDefaultsAndExplicitPinsReachAdapterCommands(t *testing.T) {
	for _, tc := range []struct {
		name, target, want string
		role               Role
	}{
		{"default supervisor", "", "claude-opus-5-5", RoleSupervisor},
		{"default codex", "codex", "gpt-6-sol", RoleCoder},
		{"default claude", "claude", "claude-sonnet-5-5", RoleSupervisor},
		{"default grok", "grok", "grok-4.7", RoleCoder},
		{"selected sol 6.1", "codex:gpt-6.1-sol", "gpt-6.1-sol", RoleCoder},
		{"pinned sol", "codex:gpt-5.6-sol", "gpt-5.6-sol", RoleCoder},
		{"pinned opus", "claude:claude-opus-5", "claude-opus-5", RoleSupervisor},
		{"pinned sonnet", "claude:claude-sonnet-5", "claude-sonnet-5", RoleSupervisor},
		{"pinned grok", "grok:grok-4.6", "grok-4.6", RoleCoder},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			flags := FlagInputs{Timeout: 15 * time.Minute}
			changed := map[string]bool{}
			if tc.target != "" {
				if tc.role == RoleCoder {
					flags.Worker, changed["worker"] = tc.target, true
				} else {
					flags.Supervisor, changed["supervisor"] = tc.target, true
				}
			}
			opts, err := ResolveOptions(cfg, nil, flags, changed, "inspect the repository")
			if err != nil {
				t.Fatalf("ResolveOptions() error = %v", err)
			}
			target := opts.Adversary
			if tc.role == RoleCoder {
				target = opts.Coder
			}
			if tc.target != "" && roleTargetString(target) != tc.target {
				t.Fatalf("resolved target = %q, want explicit %q", roleTargetString(target), tc.target)
			}
			spec, err := Registry(cfg, opts)[target.Adapter].BuildCmd(tc.role, Request{
				Model: target.Model, Workdir: t.TempDir(), Prompt: opts.Prompt,
			})
			if err != nil {
				t.Fatalf("BuildCmd() error = %v", err)
			}
			modelFlag, effortFlag, effort := "--model", "--reasoning-effort", "high"
			switch target.Adapter {
			case "codex":
				modelFlag, effortFlag, effort = "-m", "-c", `model_reasoning_effort="high"`
			case "claude":
				effortFlag = "--effort"
			}
			for flag, want := range map[string]string{modelFlag: tc.want, effortFlag: effort} {
				index := slices.Index(spec.Argv, flag)
				if index < 0 || index+1 >= len(spec.Argv) || spec.Argv[index+1] != want {
					t.Fatalf("argv = %#v; want %s %q", spec.Argv, flag, want)
				}
			}
		})
	}
}

func TestCurrentClaudeModelsRemainReviewOnlyDuringResolution(t *testing.T) {
	for _, model := range []string{"claude-opus-5-5", "claude-sonnet-5-5"} {
		for _, slot := range []string{"worker", "scout", "editing supervisor"} {
			t.Run(model+"/"+slot, func(t *testing.T) {
				target := "claude:" + model
				flags := FlagInputs{Timeout: 15 * time.Minute}
				changed := map[string]bool{}
				switch slot {
				case "worker":
					flags.Worker, changed["worker"] = target, true
				case "scout":
					flags.Mode, changed["mode"] = "relay", true
					flags.Scout, changed["scout"] = target, true
				case "editing supervisor":
					flags.Supervisor, changed["supervisor"] = target, true
					flags.SupervisorCanEdit, changed["supervisor-can-edit"] = true, true
				}
				_, err := ResolveOptions(DefaultConfig(), nil, flags, changed, "inspect the repository")
				if err == nil || !strings.Contains(err.Error(), "claude") || !strings.Contains(err.Error(), "read-only") {
					t.Fatalf("ResolveOptions() error = %v, want Claude role rejection", err)
				}
			})
		}
	}
}

func TestCurrentRoutingTargetsPreserveRoleBudgets(t *testing.T) {
	roster, err := ResolveAgentRoster(DefaultConfig())
	if err != nil {
		t.Fatalf("ResolveAgentRoster() error = %v", err)
	}
	for _, tc := range []struct {
		key, target string
		context     int
		slots       []RoleSlot
	}{
		{"opus", "claude:claude-opus-5-5", 200000, []RoleSlot{SlotReviewer}},
		{"sonnet", "claude:claude-sonnet-5-5", 200000, []RoleSlot{SlotReviewer}},
		{"gpt-sol", "codex:gpt-6-sol", 400000, []RoleSlot{SlotEditor, SlotReviewer}},
		{"grok", "grok:grok-4.7", 256000, []RoleSlot{SlotEditor, SlotReviewer}},
		{"gpt-terra", "codex:gpt-5.6-terra", 256000, []RoleSlot{SlotEditor, SlotReviewer}},
	} {
		index := slices.IndexFunc(roster, func(card AgentCard) bool { return card.Key == tc.key })
		if index < 0 {
			t.Fatalf("routing roster is missing %q", tc.key)
		}
		card := roster[index]
		if roleTargetString(card.Target) != tc.target || card.ContextTokens != tc.context || !slices.Equal(card.Slots, tc.slots) {
			t.Errorf("routing card %s = target %q, context %d, slots %v; want %q, %d, %v", tc.key, roleTargetString(card.Target), card.ContextTokens, card.Slots, tc.target, tc.context, tc.slots)
		}
	}
}
