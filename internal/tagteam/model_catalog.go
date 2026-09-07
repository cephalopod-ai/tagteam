package tagteam

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

// ModelCatalogEntry describes the models a configured adapter exposes. Live
// provider discovery is preferred; maintained or configured entries remain
// visible with an explicit warning when discovery is unavailable.
type ModelCatalogEntry struct {
	Adapter string   `json:"adapter"`
	Source  string   `json:"source"`
	Default string   `json:"default,omitempty"`
	Models  []string `json:"models,omitempty"`
	Error   string   `json:"error,omitempty"`
}

type modelDiscovery struct {
	Source  string
	Default string
	Models  []string
}

type modelDiscoverer interface {
	DiscoverModels(ctx context.Context, workdir string) (modelDiscovery, error)
}

// DiscoverModelCatalogs inspects every configured adapter. Providers with a
// native model-list surface are queried concurrently so one slow provider does
// not serially delay the entire catalog.
func DiscoverModelCatalogs(ctx context.Context, cfg Config, workdir string) []ModelCatalogEntry {
	entries := []ModelCatalogEntry{
		fallbackModelCatalog("codex", cfg.Adapters.Codex.DefaultModel),
		fallbackModelCatalog("codex-oss", cfg.Adapters.CodexOSS.DefaultModel),
		fallbackModelCatalog("claude", cfg.Adapters.Claude.DefaultModel),
		fallbackModelCatalog("agy", cfg.Adapters.Agy.DefaultModel),
		fallbackModelCatalog("gosling", cfg.Adapters.Gosling.DefaultModel),
		fallbackModelCatalog("grok", cfg.Adapters.Grok.DefaultModel),
		fallbackModelCatalog("openai-compatible", cfg.Adapters.OpenAICompatible.DefaultModel),
		fallbackModelCatalog("mistral-acp", cfg.Adapters.MistralAcp.DefaultModel),
	}
	discoverers := map[string]modelDiscoverer{
		"agy":  &AgyAdapter{DefaultModel: cfg.Adapters.Agy.DefaultModel, EnvOverlay: cfg.EnvOverlay},
		"grok": &GrokAdapter{DefaultModel: cfg.Adapters.Grok.DefaultModel, EnvOverlay: cfg.EnvOverlay},
		"mistral-acp": &MistralAcpAdapter{
			Binary:       cfg.Adapters.MistralAcp.Binary,
			SessionMode:  cfg.Adapters.MistralAcp.SessionMode,
			DefaultModel: cfg.Adapters.MistralAcp.DefaultModel,
			ExtraArgs:    cfg.Adapters.MistralAcp.ExtraArgs,
			EnvOverlay:   cfg.EnvOverlay,
		},
	}

	var wg sync.WaitGroup
	for index := range entries {
		discoverer, ok := discoverers[entries[index].Adapter]
		if !ok {
			continue
		}
		wg.Add(1)
		go func(index int, discoverer modelDiscoverer) {
			defer wg.Done()
			live, err := discoverer.DiscoverModels(ctx, workdir)
			if err != nil {
				entries[index].Error = redactSecretsWithOverlay(err.Error(), cfg.EnvOverlay)
				return
			}
			entries[index].Source = live.Source
			entries[index].Default = live.Default
			entries[index].Models = uniqueModels(live.Models, live.Default)
		}(index, discoverer)
	}
	wg.Wait()
	return entries
}

func fallbackModelCatalog(adapter, configuredDefault string) ModelCatalogEntry {
	models := maintainedModelsForAdapter(adapter)
	source := "maintained"
	if len(models) == 0 {
		source = "config"
	}
	return ModelCatalogEntry{
		Adapter: adapter,
		Source:  source,
		Default: strings.TrimSpace(configuredDefault),
		Models:  uniqueModels(models, configuredDefault),
	}
}

func maintainedModelsForAdapter(adapter string) []string {
	var models []string
	for _, raw := range MaintainedModelTargets() {
		target, err := ParseRoleTarget(raw)
		if err == nil && target.Adapter == adapter {
			models = append(models, target.Model)
		}
	}
	return models
}

func uniqueModels(models []string, extras ...string) []string {
	result := make([]string, 0, len(models)+len(extras))
	seen := map[string]bool{}
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			return
		}
		seen[value] = true
		result = append(result, value)
	}
	for _, model := range models {
		add(model)
	}
	for _, model := range extras {
		add(model)
	}
	return result
}

func (a *AgyAdapter) DiscoverModels(ctx context.Context, workdir string) (modelDiscovery, error) {
	cmd := newModelListCommand(ctx, "agy", workdir, a.EnvOverlay, "models")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return modelDiscovery{}, modelListError("agy", out, err)
	}
	models := parseAgyModelList(out)
	if len(models) == 0 {
		return modelDiscovery{}, fmt.Errorf("agy models returned no parseable model IDs")
	}
	return modelDiscovery{Source: "cli", Default: a.DefaultModel, Models: models}, nil
}

func parseAgyModelList(raw []byte) []string {
	var models []string
	for _, line := range strings.Split(string(raw), "\n") {
		model, _, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if ok {
			models = append(models, strings.TrimSpace(model))
		}
	}
	return uniqueModels(models)
}

func (a *GrokAdapter) DiscoverModels(ctx context.Context, workdir string) (modelDiscovery, error) {
	cmd := newModelListCommand(ctx, "grok", workdir, a.EnvOverlay, "models")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return modelDiscovery{}, modelListError("grok", out, err)
	}
	models, defaultModel := parseGrokModelList(out)
	if len(models) == 0 {
		return modelDiscovery{}, fmt.Errorf("grok models returned no parseable model IDs")
	}
	if defaultModel == "" {
		defaultModel = a.DefaultModel
	}
	return modelDiscovery{Source: "cli", Default: defaultModel, Models: models}, nil
}

func newModelListCommand(ctx context.Context, binary, workdir string, overlay map[string]string, args ...string) *exec.Cmd {
	cmd := execCommandContext(ctx, binary, args...)
	prepareProcessTree(cmd)
	cmd.Dir = workdir
	cmd.Env = mergeRestrictedCommandEnv(overlay, nil)
	return cmd
}

func parseGrokModelList(raw []byte) ([]string, string) {
	var models []string
	defaultModel := ""
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if value, ok := strings.CutPrefix(line, "Default model:"); ok {
			defaultModel = strings.TrimSpace(value)
			continue
		}
		if !strings.HasPrefix(line, "* ") && !strings.HasPrefix(line, "- ") {
			continue
		}
		model := strings.TrimSpace(line[2:])
		model = strings.TrimSpace(strings.TrimSuffix(model, "(default)"))
		models = append(models, model)
	}
	return uniqueModels(models), defaultModel
}

func modelListError(adapter string, output []byte, err error) error {
	detail := strings.TrimSpace(string(output))
	if len(detail) > 512 {
		detail = detail[:512] + "..."
	}
	if detail == "" {
		return fmt.Errorf("%s model discovery failed: %w", adapter, err)
	}
	return fmt.Errorf("%s model discovery failed: %w: %s", adapter, err, detail)
}

func (a *MistralAcpAdapter) DiscoverModels(ctx context.Context, workdir string) (modelDiscovery, error) {
	procCtx, stopProc := context.WithCancel(ctx)
	defer stopProc()
	argv := append([]string{a.binary()}, a.ExtraArgs...)
	cmd := execCommandContext(procCtx, argv[0], argv[1:]...)
	prepareProcessTree(cmd)
	cmd.Dir = workdir
	cmd.Env = mergeRestrictedCommandEnv(a.EnvOverlay, nil)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return modelDiscovery{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return modelDiscovery{}, err
	}
	if err := cmd.Start(); err != nil {
		return modelDiscovery{}, fmt.Errorf("mistral-acp model discovery could not start: %w", err)
	}

	rpc := newACPRPC(stdin)
	serveDone := make(chan error, 1)
	go func() { serveDone <- rpc.serve(stdout) }()
	defer func() {
		stopProc()
		_ = stdin.Close()
		<-serveDone
		_ = cmd.Wait()
	}()

	newSession, err := a.newACPSession(procCtx, rpc, workdir, a.EnvOverlay)
	if err != nil {
		return modelDiscovery{}, fmt.Errorf("mistral-acp model discovery %w", err)
	}
	defaultModel, models := acpModelConfig(newSession.ConfigOptions)
	if len(models) == 0 {
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return modelDiscovery{}, fmt.Errorf("mistral-acp session advertised no model options: %s", detail)
		}
		return modelDiscovery{}, fmt.Errorf("mistral-acp session advertised no model options")
	}
	return modelDiscovery{Source: "acp", Default: defaultModel, Models: models}, nil
}
