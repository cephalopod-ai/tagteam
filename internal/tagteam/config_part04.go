package tagteam

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"
)

func validateRoleLossPolicies(p RoleLossPolicies) error {
	for _, item := range []struct {
		name  string
		value LossPolicy
	}{
		{"loss_policy.reviewer", p.Reviewer},
		{"loss_policy.worker", p.Worker},
		{"loss_policy.supervisor", p.Supervisor},
		{"loss_policy.scout", p.Scout},
	} {
		switch item.value {
		case "", LossPolicyBlock, LossPolicyDegrade, LossPolicyReplaceThenBlock, LossPolicyReplaceThenDegrade:
		default:
			return fmt.Errorf("invalid %s %q (want block, degrade, replace_then_block, or replace_then_degrade)", item.name, item.value)
		}
	}
	return nil
}

func normalizeRoleFallbacks(f RoleFallbacks, editor, reviewer, scout RoleTarget) RoleFallbacks {
	return RoleFallbacks{
		Worker:     normalizeFallbackTargets(f.Worker, editor),
		Reviewer:   normalizeFallbackTargets(f.Reviewer, reviewer),
		Supervisor: normalizeFallbackTargets(f.Supervisor, reviewer),
		Scout:      normalizeFallbackTargets(f.Scout, scout),
	}
}

func normalizeTargetFallbacks(f TargetFallbacks) TargetFallbacks {
	if len(f) == 0 {
		return nil
	}
	out := TargetFallbacks{}
	for rawPrimary, rawFallbacks := range f {
		primary, err := ParseRoleTarget(rawPrimary)
		if err != nil || primary.Adapter == "" {
			continue
		}
		normalized := normalizeFallbackTargets(rawFallbacks, primary)
		if len(normalized) > 0 {
			out[roleTargetString(primary)] = normalized
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeFallbackTargets(raw []string, primary RoleTarget) []string {
	const maxFallbackTargets = 5
	primaryRaw := roleTargetString(primary)
	seen := map[string]bool{}
	if primaryRaw != "" {
		seen[primaryRaw] = true
	}
	out := []string{}
	for _, item := range raw {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		if _, err := ParseRoleTarget(item); err != nil {
			continue
		}
		seen[item] = true
		out = append(out, item)
		if len(out) == maxFallbackTargets {
			break
		}
	}
	return out
}

func cloneTargetFallbacks(src TargetFallbacks) TargetFallbacks {
	if len(src) == 0 {
		return nil
	}
	out := TargetFallbacks{}
	for primary, fallbacks := range src {
		out[primary] = append([]string{}, fallbacks...)
	}
	return out
}

func roleTargetString(target RoleTarget) string {
	if target.Adapter == "" {
		return ""
	}
	if target.Model == "" {
		return target.Adapter
	}
	return target.Adapter + ":" + target.Model
}

// sanitizeUntrustedMistralAcpConfig strips the fields that cross the same
// authority boundary as openai-compatible's base_url/extra_args in
// sanitizeUntrustedRepoConfig: Binary changes which local executable a role
// invokes, and ExtraArgs are arbitrary passthrough flags.
func sanitizeUntrustedMistralAcpConfig(cfg *MistralAcpConfig) {
	cfg.Binary = ""
	cfg.ExtraArgs = nil
}

// mergeMistralAcpConfig applies src.Adapters.MistralAcp onto dst, the same
// field-by-field precedence every other adapter config uses in mergeConfig.
// Pulled into its own function (rather than inlined in mergeConfig) purely
// to keep config.go under the 800-line file cap.
func mergeMistralAcpConfig(dst *MistralAcpConfig, src MistralAcpConfig) {
	if src.Binary != "" {
		dst.Binary = src.Binary
	}
	if src.SessionMode != "" {
		dst.SessionMode = src.SessionMode
	}
	if src.DefaultModel != "" {
		dst.DefaultModel = src.DefaultModel
	}
	mergeContextBudget(&dst.MaxContextTokens, &dst.ReservedOutputTokens, src.MaxContextTokens, src.ReservedOutputTokens)
	if len(src.ExtraArgs) > 0 {
		dst.ExtraArgs = append([]string{}, src.ExtraArgs...)
	}
}

func EncodeConfig(cfg Config) ([]byte, error) {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
