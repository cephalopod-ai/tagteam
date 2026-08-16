package tagteam

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// routingProbeTimeout bounds one adapter detection probe. Detection is a
// `--version`-style call, so a slow provider CLI degrades to "unavailable"
// rather than stalling routing.
const routingProbeTimeout = 10 * time.Second

// RoutingAdapters lists the distinct adapter ids referenced by the configured
// agent roster, in a stable order.
func RoutingAdapters(cfg Config) ([]string, error) {
	roster, err := ResolveAgentRoster(cfg)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	adapters := make([]string, 0, len(roster))
	for _, card := range roster {
		if card.Disabled || seen[card.Target.Adapter] {
			continue
		}
		seen[card.Target.Adapter] = true
		adapters = append(adapters, card.Target.Adapter)
	}
	sort.Strings(adapters)
	return adapters, nil
}

// ProbeAdapterAvailability detects whether each roster adapter is installed and
// runnable. It is opt-in: routing is deterministic and offline unless the
// operator explicitly asks for a probe, so a run never silently depends on
// subprocess detection.
func ProbeAdapterAvailability(ctx context.Context, cfg Config, opts RunOptions) (map[string]AdapterAvailability, error) {
	adapters, err := RoutingAdapters(cfg)
	if err != nil {
		return nil, err
	}
	registry := Registry(cfg, opts)
	availability := make(map[string]AdapterAvailability, len(adapters))
	for _, id := range adapters {
		adapter, ok := registry[id]
		if !ok {
			availability[id] = AdapterAvailability{Adapter: id, Available: false, Reason: "no adapter registered with this id"}
			continue
		}
		availability[id] = probeAdapter(ctx, id, adapter)
	}
	return availability, nil
}

func probeAdapter(ctx context.Context, id string, adapter Adapter) AdapterAvailability {
	probeCtx, cancel := context.WithTimeout(ctx, routingProbeTimeout)
	defer cancel()
	info, err := adapter.Detect(probeCtx)
	switch {
	case err != nil:
		return AdapterAvailability{Adapter: id, Available: false, Reason: fmt.Sprintf("detection failed: %v", err)}
	case !info.Found:
		return AdapterAvailability{Adapter: id, Available: false, Reason: reasonWithHint("not installed", info.Hint)}
	case !info.Runnable:
		return AdapterAvailability{Adapter: id, Available: false, Reason: reasonWithHint("installed but not runnable", info.Hint)}
	default:
		return AdapterAvailability{Adapter: id, Available: true}
	}
}

func reasonWithHint(reason, hint string) string {
	hint = strings.TrimSpace(hint)
	if hint == "" {
		return reason
	}
	return reason + "; try " + hint
}
