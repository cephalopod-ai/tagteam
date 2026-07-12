package tagteam

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/shlex"
)

const codeIntelTimeout = 10 * time.Second

type CodeIntelCapabilities struct {
	SchemaVersion int      `json:"schema_version"`
	Operations    []string `json:"operations"`
}

type CodeIntelRequest struct {
	Workdir string `json:"workdir"`
	Prompt  string `json:"prompt"`
	Context string `json:"context,omitempty"`
}

type CodeIntelProvider interface {
	Name() string
	Capabilities() CodeIntelCapabilities
	Probe(ctx context.Context, workdir string) error
	Observe(ctx context.Context, req CodeIntelRequest) (CodeIntelArtifact, error)
}

type CommandCodeIntelProvider struct {
	command string
	name    string
	timeout time.Duration
}

func NewCommandCodeIntelProvider(command string) (*CommandCodeIntelProvider, error) {
	if strings.TrimSpace(command) == "" {
		return nil, fmt.Errorf("code-intel command is empty")
	}
	if _, err := shlex.Split(command); err != nil {
		return nil, fmt.Errorf("parse code_intel_command: %w", err)
	}
	return newNamedCommandCodeIntelProvider("command", command, codeIntelTimeout)
}

func newNamedCommandCodeIntelProvider(name, command string, timeout time.Duration) (*CommandCodeIntelProvider, error) {
	if strings.TrimSpace(command) == "" {
		return nil, fmt.Errorf("code-intel command is empty")
	}
	if _, err := shlex.Split(command); err != nil {
		return nil, fmt.Errorf("parse code-intel command: %w", err)
	}
	if timeout <= 0 {
		timeout = codeIntelTimeout
	}
	return &CommandCodeIntelProvider{command: strings.TrimSpace(command), name: name, timeout: timeout}, nil
}

func (p *CommandCodeIntelProvider) Name() string {
	return p.name
}

func (p *CommandCodeIntelProvider) Capabilities() CodeIntelCapabilities {
	operations := []string{"orient", "find", "trace", "impact", "resume", "recall", "evidence"}
	return CodeIntelCapabilities{SchemaVersion: ArtifactSchemaVersion, Operations: operations}
}

func (p *CommandCodeIntelProvider) Probe(ctx context.Context, workdir string) error {
	parts, err := shlex.Split(p.command)
	if err != nil {
		return fmt.Errorf("parse code_intel_command: %w", err)
	}
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		return fmt.Errorf("code_intel_command has no executable")
	}
	if _, err := exec.LookPath(parts[0]); err != nil {
		return fmt.Errorf("code-intel provider %q unavailable: %w", parts[0], err)
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if strings.TrimSpace(workdir) == "" {
		return fmt.Errorf("code-intel provider workdir is empty")
	}
	return nil
}

func (p *CommandCodeIntelProvider) Observe(ctx context.Context, req CodeIntelRequest) (CodeIntelArtifact, error) {
	artifact := CodeIntelArtifact{
		SchemaVersion: ArtifactSchemaVersion,
		Status:        codeIntelStatusError,
		Observations:  []CodeIntelObservation{},
		Staleness:     codeIntelStalenessUnknown,
		GeneratedAt:   time.Now().UTC(),
	}
	parts, err := shlex.Split(p.command)
	if err != nil {
		artifact.Errors = []string{sanitizeCodeIntelText("parse code_intel_command: "+err.Error(), maxCodeIntelSummaryBytes)}
		return artifact, err
	}
	input, err := json.Marshal(req)
	if err != nil {
		artifact.Errors = []string{sanitizeCodeIntelText("marshal provider request: "+err.Error(), maxCodeIntelSummaryBytes)}
		return artifact, err
	}
	observeCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	cmd := exec.CommandContext(observeCtx, parts[0], parts[1:]...)
	cmd.Dir = req.Workdir
	cmd.Env = mergeRestrictedCommandEnv(nil, nil)
	cmd.Stdin = bytes.NewReader(input)
	stdout := newBoundedBuffer(maxCodeIntelArtifactSize)
	stderr := newBoundedBuffer(maxCodeIntelSummaryBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		message := sanitizeCodeIntelText(stderr.String(), maxCodeIntelSummaryBytes)
		if observeCtx.Err() != nil {
			message = "code-intel provider timed out"
		} else if message == "" {
			message = sanitizeCodeIntelText(err.Error(), maxCodeIntelSummaryBytes)
		}
		artifact.Errors = []string{message}
		if stdout.Exceeded() {
			artifact.Errors = []string{outputLimitError("code-intel provider", maxCodeIntelArtifactSize).Error()}
		}
		return artifact, fmt.Errorf("code-intel provider: %s", message)
	}
	if stdout.Exceeded() {
		message := outputLimitError("code-intel provider", maxCodeIntelArtifactSize).Error()
		artifact.Errors = []string{message}
		return artifact, errors.New(message)
	}
	if err := json.Unmarshal(stdout.Bytes(), &artifact); err != nil {
		message := sanitizeCodeIntelText("invalid code-intel provider JSON: "+err.Error(), maxCodeIntelSummaryBytes)
		artifact.Errors = []string{message}
		return artifact, errors.New(message)
	}
	normalized, err := normalizeCodeIntelArtifact(observeCtx, req.Workdir, artifact)
	if err != nil {
		artifact.SchemaVersion = ArtifactSchemaVersion
		artifact.Status = codeIntelStatusError
		artifact.Staleness = codeIntelStalenessUnknown
		artifact.Observations = []CodeIntelObservation{}
		artifact.Errors = []string{sanitizeCodeIntelText(err.Error(), maxCodeIntelSummaryBytes)}
		return artifact, err
	}
	for i := range normalized.Observations {
		normalized.Observations[i].Provider = p.Name()
	}
	return normalized, nil
}

func unavailableCodeIntelArtifact(message string) CodeIntelArtifact {
	return CodeIntelArtifact{
		SchemaVersion: ArtifactSchemaVersion,
		Status:        codeIntelStatusProviderUnavailable,
		Observations:  []CodeIntelObservation{},
		Staleness:     codeIntelStalenessUnknown,
		Errors:        []string{sanitizeCodeIntelText(message, maxCodeIntelSummaryBytes)},
		GeneratedAt:   time.Now().UTC(),
	}
}

func runCodeIntel(ctx context.Context, workdir, prompt, runDir, command string) (CodeIntelArtifact, error) {
	provider, err := NewCommandCodeIntelProvider(command)
	if err != nil {
		artifact := unavailableCodeIntelArtifact(err.Error())
		writeErr := writeJSONWithNewline(codeIntelArtifactPath(runDir), artifact)
		if writeErr != nil {
			return artifact, writeErr
		}
		return artifact, err
	}
	if err := provider.Probe(ctx, workdir); err != nil {
		artifact := unavailableCodeIntelArtifact(err.Error())
		writeErr := writeJSONWithNewline(codeIntelArtifactPath(runDir), artifact)
		if writeErr != nil {
			return artifact, writeErr
		}
		return artifact, err
	}
	artifact, observeErr := provider.Observe(ctx, CodeIntelRequest{Workdir: workdir, Prompt: prompt})
	if artifact.SchemaVersion == 0 {
		artifact = unavailableCodeIntelArtifact("provider returned no artifact")
	}
	writeErr := writeJSONWithNewline(codeIntelArtifactPath(runDir), artifact)
	if writeErr != nil {
		return artifact, writeErr
	}
	return artifact, observeErr
}

func runConfiguredCodeIntel(ctx context.Context, opts RunOptions, runDir string) (CodeIntelArtifact, error) {
	logProgress(opts, "code-intelligence sensor started")
	if !codeIntelRepoAllowed(opts.Workdir, opts.CodeIntel.AllowedRepos) {
		artifact := unavailableCodeIntelArtifact("code-intel disabled: repository is not in allowed_repos")
		artifact.Status = codeIntelStatusDisabled
		_ = writeJSONWithNewline(codeIntelArtifactPath(runDir), artifact)
		return artifact, nil
	}
	providers, err := configuredCodeIntelProviders(opts)
	if err != nil {
		return unavailableCodeIntelArtifact(err.Error()), err
	}
	if len(providers) == 0 {
		artifact := unavailableCodeIntelArtifact("no configured code-intel providers")
		return artifact, writeJSONWithNewline(codeIntelArtifactPath(runDir), artifact)
	}
	artifact := aggregateCodeIntelProviders(ctx, opts.Workdir, opts.Prompt, providers)
	err = writeJSONWithNewline(codeIntelArtifactPath(runDir), artifact)
	if err != nil {
		logProgress(opts, "code-intelligence sensor degraded status=%s error=%q", artifact.Status, err.Error())
		return artifact, err
	}
	logProgress(opts, "code-intelligence sensor completed status=%s observations=%d", artifact.Status, len(artifact.Observations))
	return artifact, nil
}

func configuredCodeIntelProviders(opts RunOptions) ([]CodeIntelProvider, error) {
	timeout := codeIntelTimeout
	if opts.CodeIntel.Timeout != "" {
		var err error
		timeout, err = time.ParseDuration(opts.CodeIntel.Timeout)
		if err != nil {
			return nil, err
		}
	}
	providers := []CodeIntelProvider{}
	if opts.CodeIntelCommand != "" {
		p, err := newNamedCommandCodeIntelProvider("command", opts.CodeIntelCommand, timeout)
		if err != nil {
			return nil, err
		}
		providers = append(providers, p)
	}
	for _, name := range []string{"codebase-memory", "gitnexus"} {
		if raw := opts.CodeIntel.Providers[name].Command; strings.TrimSpace(raw) != "" {
			p, err := newNamedCommandCodeIntelProvider(name, raw, timeout)
			if err != nil {
				return nil, err
			}
			providers = append(providers, p)
		}
	}
	return providers, nil
}

func aggregateCodeIntelProviders(ctx context.Context, workdir, prompt string, providers []CodeIntelProvider) CodeIntelArtifact {
	// Leave Status unset until normalization has considered every provider's
	// observations and errors. In particular, an all-provider failure is an
	// error rather than a successful empty result.
	result := CodeIntelArtifact{SchemaVersion: ArtifactSchemaVersion, Observations: []CodeIntelObservation{}, Staleness: codeIntelStalenessUnknown, GeneratedAt: time.Now().UTC()}
	for _, provider := range providers {
		if err := provider.Probe(ctx, workdir); err != nil {
			result.Errors = appendCodeIntelError(result.Errors, provider.Name()+": "+err.Error())
			continue
		}
		artifact, err := provider.Observe(ctx, CodeIntelRequest{Workdir: workdir, Prompt: prompt})
		if err != nil {
			result.Errors = appendCodeIntelError(result.Errors, provider.Name()+": "+err.Error())
		}
		result.Observations = append(result.Observations, artifact.Observations...)
		result.Errors = append(result.Errors, artifact.Errors...)
		if artifact.Truncated {
			result.Truncated = true
		}
	}
	normalized, err := normalizeCodeIntelArtifact(ctx, workdir, result)
	if err != nil {
		return unavailableCodeIntelArtifact(err.Error())
	}
	return normalized
}

func codeIntelArtifactPath(runDir string) string {
	return filepath.Join(runDir, "code-intel-round-1.json")
}
