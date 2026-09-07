// Package sharedcatalog reads the fleet's canonical model/adapter roster.
//
// This file and the model_catalog.json beside it are VENDORED, byte-identical,
// from e3742526/control-hooks (shared/go/sharedcatalog/catalog.go and
// shared/model_catalog.json). Do not edit either copy in a consuming
// repository: change the canonical source, then re-vendor and refresh
// SHA256SUMS. Each consumer ships a test that fails when a vendored copy
// drifts from the recorded digest, so an in-place edit is a build failure
// rather than a silent fork.
//
// The package deliberately has no dependency outside the standard library and
// no reference to any consuming repository, so the same source compiles
// unchanged wherever it is vendored.
//
// The roster is a maintained label set, not an entitlement claim: a model
// listed here is not proof the logged-in account can reach it. Where an
// adapter exposes live discovery (see Adapter.Discovery), that discovery is
// the source of truth and this roster is only the fallback.
package sharedcatalog

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

//go:embed model_catalog.json
var catalogJSON []byte

// Discovery describes how an adapter enumerates its own models at runtime.
// Kind is "cli" (run Argv and parse Format) or "acp" (read the session
// configuration option named by ConfigOption).
type Discovery struct {
	Kind         string   `json:"kind"`
	Argv         []string `json:"argv,omitempty"`
	Format       string   `json:"format,omitempty"`
	ConfigOption string   `json:"config_option,omitempty"`
}

// Adapter is one provider integration: the binary or transport a consumer
// drives, the roles the fleet permits it to hold, and how it discovers models.
type Adapter struct {
	ID                  string     `json:"id"`
	DisplayName         string     `json:"display_name"`
	Kind                string     `json:"kind"`
	Binary              string     `json:"binary"`
	Family              string     `json:"family"`
	Roles               []string   `json:"roles"`
	Headless            string     `json:"headless"`
	PromptTransport     string     `json:"prompt_transport"`
	PromptTransportNote string     `json:"prompt_transport_note,omitempty"`
	Discovery           *Discovery `json:"discovery"`
	DiscoveryNote       string     `json:"discovery_note,omitempty"`
}

// Model is one maintained model ID and the adapters that can reach it.
type Model struct {
	ID            string   `json:"id"`
	Family        string   `json:"family"`
	Adapters      []string `json:"adapters"`
	Series        string   `json:"series,omitempty"`
	EffortTier    string   `json:"effort_tier,omitempty"`
	ContextWindow int      `json:"context_window,omitempty"`
	MaxOutput     int      `json:"max_output,omitempty"`
	Reasoning     bool     `json:"reasoning,omitempty"`
	Maintained    bool     `json:"maintained,omitempty"`
	Notes         string   `json:"notes,omitempty"`
}

// Catalog is the decoded canonical roster.
type Catalog struct {
	SchemaVersion   int       `json:"schema_version"`
	ContractVersion string    `json:"contract_version"`
	Updated         string    `json:"updated"`
	SourceRepo      string    `json:"source_repo"`
	SourcePath      string    `json:"source_path"`
	Notes           string    `json:"notes"`
	Adapters        []Adapter `json:"adapters"`
	Models          []Model   `json:"models"`
}

var (
	loadOnce sync.Once
	loaded   Catalog
	loadErr  error
)

// Load decodes the embedded catalog once. A malformed or internally
// inconsistent vendored file is a hard error rather than an empty roster,
// because a silently empty roster would look like "this provider has no
// models" to every caller downstream.
func Load() (Catalog, error) {
	loadOnce.Do(func() {
		decoder := json.NewDecoder(strings.NewReader(string(catalogJSON)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&loaded); err != nil {
			loadErr = fmt.Errorf("decode shared model catalog: %w", err)
			return
		}
		loadErr = validate(loaded)
	})
	return loaded, loadErr
}

// MustLoad is Load for callers that cannot report an error. The embedded file
// ships with the binary, so a failure here is a build-time packaging defect,
// not a runtime condition a caller could recover from.
func MustLoad() Catalog {
	catalog, err := Load()
	if err != nil {
		panic(err)
	}
	return catalog
}

func validate(catalog Catalog) error {
	if catalog.SchemaVersion != 1 {
		return fmt.Errorf("shared model catalog schema_version %d is unsupported", catalog.SchemaVersion)
	}
	if strings.TrimSpace(catalog.ContractVersion) == "" {
		return fmt.Errorf("shared model catalog is missing contract_version")
	}
	adapters := map[string]bool{}
	for _, adapter := range catalog.Adapters {
		if strings.TrimSpace(adapter.ID) == "" {
			return fmt.Errorf("shared model catalog has an adapter with no id")
		}
		if adapters[adapter.ID] {
			return fmt.Errorf("shared model catalog has duplicate adapter %q", adapter.ID)
		}
		adapters[adapter.ID] = true
	}
	models := map[string]bool{}
	for _, model := range catalog.Models {
		if strings.TrimSpace(model.ID) == "" {
			return fmt.Errorf("shared model catalog has a model with no id")
		}
		if models[model.ID] {
			return fmt.Errorf("shared model catalog has duplicate model %q", model.ID)
		}
		models[model.ID] = true
		if len(model.Adapters) == 0 {
			return fmt.Errorf("shared model catalog model %q names no adapter", model.ID)
		}
		for _, id := range model.Adapters {
			if !adapters[id] {
				return fmt.Errorf("shared model catalog model %q names unknown adapter %q", model.ID, id)
			}
		}
	}
	return nil
}

// AdapterIDs returns every adapter id in declaration order.
func AdapterIDs() []string {
	catalog := MustLoad()
	ids := make([]string, 0, len(catalog.Adapters))
	for _, adapter := range catalog.Adapters {
		ids = append(ids, adapter.ID)
	}
	return ids
}

// LookupAdapter returns the adapter with the given id.
func LookupAdapter(id string) (Adapter, bool) {
	for _, adapter := range MustLoad().Adapters {
		if adapter.ID == id {
			return adapter, true
		}
	}
	return Adapter{}, false
}

// LookupModel returns the model with the given id.
func LookupModel(id string) (Model, bool) {
	for _, model := range MustLoad().Models {
		if model.ID == id {
			return model, true
		}
	}
	return Model{}, false
}

// MaintainedModelsFor returns the maintained model ids reachable through one
// adapter, in catalog order. Adapters with no maintained entry return nil so a
// caller can distinguish "roster-backed" from "configuration-only".
func MaintainedModelsFor(adapterID string) []string {
	var models []string
	for _, model := range MustLoad().Models {
		if !model.Maintained {
			continue
		}
		for _, id := range model.Adapters {
			if id == adapterID {
				models = append(models, model.ID)
				break
			}
		}
	}
	return models
}

// MaintainedTargets returns every maintained "<adapter><sep><model>" target in
// catalog order. Tagteam addresses roles as "adapter:model" and Tribunal
// composes panels as "adapter/model"; both read the same roster through sep.
func MaintainedTargets(sep string) []string {
	var targets []string
	for _, model := range MustLoad().Models {
		if !model.Maintained {
			continue
		}
		for _, adapter := range model.Adapters {
			targets = append(targets, adapter+sep+model.ID)
		}
	}
	return targets
}

// SeriesModels returns the models of one series ordered weakest reasoning
// effort first (low, medium, high), then by id for entries with no tier. This
// is the order model pickers present, which is not the roster's own order.
func SeriesModels(series string) []string {
	rank := map[string]int{"low": 0, "medium": 1, "high": 2}
	type entry struct {
		id   string
		tier int
	}
	var entries []entry
	for _, model := range MustLoad().Models {
		if model.Series != series {
			continue
		}
		tier, ok := rank[model.EffortTier]
		if !ok {
			tier = len(rank)
		}
		entries = append(entries, entry{id: model.ID, tier: tier})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].tier != entries[j].tier {
			return entries[i].tier < entries[j].tier
		}
		return entries[i].id < entries[j].id
	})
	ids := make([]string, 0, len(entries))
	for _, item := range entries {
		ids = append(ids, item.id)
	}
	return ids
}

// AdapterAllowsRole reports whether the fleet roster permits an adapter to
// hold a role. It is an advisory prior for defaults and pickers; each consumer
// still enforces its own role boundary at the adapter call site.
func AdapterAllowsRole(adapterID, role string) bool {
	adapter, ok := LookupAdapter(adapterID)
	if !ok {
		return false
	}
	for _, candidate := range adapter.Roles {
		if candidate == role {
			return true
		}
	}
	return false
}
