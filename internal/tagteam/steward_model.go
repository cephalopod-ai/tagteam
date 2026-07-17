package tagteam

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// StewardConfig configures the optional advisory Run Steward. It is disabled by
// default: when disabled, missing, or misconfigured, the deterministic template
// steward is the only tier used. The default tier is a separately-configured
// local OpenAI-compatible endpoint (e.g. Ollama); cloud or CLI stewards are
// optional escalation targets constrained by the same budgets.
type StewardConfig struct {
	Enabled            *bool  `toml:"enabled"`
	BaseURL            string `toml:"base_url"`
	APIKeyEnv          string `toml:"api_key_env"`
	Model              string `toml:"model"`
	TimeoutSeconds     int    `toml:"timeout_seconds"`
	MaxCallsPerRun     int    `toml:"max_calls_per_run"`
	MinIntervalSeconds int    `toml:"min_interval_seconds"`
}

// StewardTimeout returns the per-advisory timeout, bounded to a sane default so
// a slow steward can never stall the run beyond it.
func (c StewardConfig) StewardTimeout() time.Duration {
	if c.TimeoutSeconds <= 0 {
		return stewardDefaultTimeout
	}
	return time.Duration(c.TimeoutSeconds) * time.Second
}

const stewardSystemPrompt = "You are an advisory Run Steward for the Tagteam orchestrator. " +
	"You receive a bounded, sanitized JSON observation of one run and must respond with a single JSON object " +
	`{"action": <one of wait|inspect|notify|prepare_resume|ask_user|report_issue>, "reason": <short string>}. ` +
	"You are strictly advisory: you cannot edit the repository, change scope or roles, dismiss findings, run commands, " +
	"or approve recovery. Never request tools. Respond with JSON only."

// ModelSteward is a Run Steward backed by a local-first OpenAI-compatible chat
// endpoint. It sends only the sanitized observation as text and never advertises
// tools, so the model cannot invoke Tagteam, acquire MCP or repository-write
// tools, or otherwise gain execution authority — recursion is prevented by
// construction.
type ModelSteward struct {
	BaseURL    string
	Model      string
	APIKey     string
	HTTPClient *http.Client
}

func (m *ModelSteward) client() *http.Client {
	if m.HTTPClient != nil {
		return m.HTTPClient
	}
	return http.DefaultClient
}

// Advise implements Steward by querying the configured chat endpoint.
func (m *ModelSteward) Advise(ctx context.Context, observation RunObservation) (StewardAdvisory, error) {
	base := strings.TrimRight(strings.TrimSpace(m.BaseURL), "/")
	if base == "" || strings.TrimSpace(m.Model) == "" {
		return StewardAdvisory{}, fmt.Errorf("steward model tier is not configured")
	}
	observationJSON, err := json.Marshal(observation)
	if err != nil {
		return StewardAdvisory{}, err
	}
	// Text-only payload: no "tools"/"functions" field is ever included.
	payload := map[string]any{
		"model":       m.Model,
		"temperature": 0,
		"messages": []map[string]string{
			{"role": "system", "content": stewardSystemPrompt},
			{"role": "user", "content": "Run observation:\n" + string(observationJSON) + "\nRespond with the advisory JSON object only."},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return StewardAdvisory{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return StewardAdvisory{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(m.APIKey) != "" {
		request.Header.Set("Authorization", "Bearer "+m.APIKey)
	}
	response, err := m.client().Do(request)
	if err != nil {
		return StewardAdvisory{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return StewardAdvisory{}, fmt.Errorf("steward endpoint returned status %d", response.StatusCode)
	}
	var decoded struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return StewardAdvisory{}, fmt.Errorf("decode steward response: %w", err)
	}
	if len(decoded.Choices) == 0 {
		return StewardAdvisory{}, fmt.Errorf("steward response had no choices")
	}
	return parseStewardAdvisoryContent(decoded.Choices[0].Message.Content)
}

func parseStewardAdvisoryContent(content string) (StewardAdvisory, error) {
	trimmed := strings.TrimSpace(content)
	// Accept a bare JSON object, tolerating a leading/trailing code fence.
	if start := strings.IndexByte(trimmed, '{'); start >= 0 {
		if end := strings.LastIndexByte(trimmed, '}'); end >= start {
			trimmed = trimmed[start : end+1]
		}
	}
	var parsed struct {
		Action string `json:"action"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return StewardAdvisory{}, fmt.Errorf("steward response was not advisory JSON: %w", err)
	}
	return StewardAdvisory{
		SchemaVersion: StewardContractVersion,
		Action:        StewardAction(strings.TrimSpace(parsed.Action)),
		Reason:        strings.TrimSpace(parsed.Reason),
		Source:        "model",
	}, nil
}

// BudgetedSteward wraps a primary steward with per-run call, timeout, and
// deduplication budgets so advisory work never contends with worker or reviewer
// invocations. When the budget is exhausted it errors, letting the caller fall
// back to the deterministic template rather than blocking the run.
type BudgetedSteward struct {
	primary     Steward
	maxCalls    int
	minInterval time.Duration
	now         func() time.Time

	mu       sync.Mutex
	calls    int
	lastSig  string
	lastAt   time.Time
	lastAdv  StewardAdvisory
	hasCache bool
}

// NewBudgetedSteward builds a budgeted wrapper. A non-positive maxCalls means
// unlimited; a non-positive minInterval disables deduplication.
func NewBudgetedSteward(primary Steward, maxCalls int, minInterval time.Duration) *BudgetedSteward {
	return &BudgetedSteward{primary: primary, maxCalls: maxCalls, minInterval: minInterval, now: time.Now}
}

// Advise implements Steward with budget enforcement.
func (b *BudgetedSteward) Advise(ctx context.Context, observation RunObservation) (StewardAdvisory, error) {
	signature := observationSignature(observation)
	now := b.now()

	b.mu.Lock()
	if b.hasCache && b.lastSig == signature && b.minInterval > 0 && now.Sub(b.lastAt) < b.minInterval {
		cached := b.lastAdv
		b.mu.Unlock()
		return cached, nil
	}
	if b.maxCalls > 0 && b.calls >= b.maxCalls {
		b.mu.Unlock()
		return StewardAdvisory{}, fmt.Errorf("steward call budget exhausted for this run")
	}
	b.calls++
	b.mu.Unlock()

	advisory, err := b.primary.Advise(ctx, observation)
	if err != nil {
		return StewardAdvisory{}, err
	}
	b.mu.Lock()
	b.lastSig, b.lastAt, b.lastAdv, b.hasCache = signature, now, advisory, true
	b.mu.Unlock()
	return advisory, nil
}

func observationSignature(observation RunObservation) string {
	return strings.Join([]string{
		observation.RunID, observation.Status, observation.Phase,
		observation.BlockingReason, observation.DegradedReason,
		observation.RecoveryStatus, observation.NoProgressFor,
		fmt.Sprintf("%d", observation.CurrentRound),
	}, "|")
}

// BuildSteward composes the configured steward chain. When the steward is
// disabled or unconfigured it returns the deterministic template steward so the
// run always has a safe advisory tier.
func BuildSteward(cfg StewardConfig, envOverlay map[string]string) Steward {
	if cfg.Enabled == nil || !*cfg.Enabled || strings.TrimSpace(cfg.Model) == "" || strings.TrimSpace(cfg.BaseURL) == "" {
		return DeterministicSteward{}
	}
	apiKey := ""
	if strings.TrimSpace(cfg.APIKeyEnv) != "" {
		apiKey = envValue(envOverlay, cfg.APIKeyEnv)
	}
	model := &ModelSteward{BaseURL: cfg.BaseURL, Model: cfg.Model, APIKey: apiKey}
	interval := time.Duration(cfg.MinIntervalSeconds) * time.Second
	return NewBudgetedSteward(model, cfg.MaxCallsPerRun, interval)
}

const stewardLeaseName = "steward.lease"

// stewardLease is a per-run advisory lease. Only one steward observer may hold
// it at a time, preventing duplicate observers on the same run. It reuses the
// run-lock record shape and treats a dead owner's lease as stale.
type stewardLease struct {
	path string
	pid  int
}

func acquireStewardLease(runDir string) (*stewardLease, error) {
	path := filepath.Join(runDir, stewardLeaseName)
	if data, err := os.ReadFile(path); err == nil {
		var existing runLockRecord
		if json.Unmarshal(data, &existing) == nil && existing.PID > 0 && processAlive(existing.PID) {
			return nil, fmt.Errorf("run already has a steward observer (pid %d)", existing.PID)
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	record := runLockRecord{PID: os.Getpid(), CreatedAt: time.Now().UTC()}
	data, err := marshalJSON(record, true)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("acquire steward lease: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	return &stewardLease{path: path, pid: record.PID}, nil
}

func (l *stewardLease) Release() error {
	if l == nil || l.path == "" {
		return nil
	}
	data, err := os.ReadFile(l.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var existing runLockRecord
	if json.Unmarshal(data, &existing) != nil || existing.PID != l.pid {
		return nil
	}
	return os.Remove(l.path)
}
