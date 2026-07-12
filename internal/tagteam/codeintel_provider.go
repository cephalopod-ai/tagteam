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

type CodeIntelRequest struct {
	Workdir string `json:"workdir"`
	Prompt  string `json:"prompt"`
	Context string `json:"context,omitempty"`
}

type CodeIntelProvider interface {
	Name() string
	Probe(ctx context.Context, workdir string) error
	Observe(ctx context.Context, req CodeIntelRequest) (CodeIntelArtifact, error)
}

type CommandCodeIntelProvider struct {
	command string
}

func NewCommandCodeIntelProvider(command string) (*CommandCodeIntelProvider, error) {
	if strings.TrimSpace(command) == "" {
		return nil, fmt.Errorf("code-intel command is empty")
	}
	if _, err := shlex.Split(command); err != nil {
		return nil, fmt.Errorf("parse code_intel_command: %w", err)
	}
	return &CommandCodeIntelProvider{command: strings.TrimSpace(command)}, nil
}

func (p *CommandCodeIntelProvider) Name() string {
	return "command"
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
	observeCtx, cancel := context.WithTimeout(ctx, codeIntelTimeout)
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
	artifact, err := runCodeIntel(ctx, opts.Workdir, opts.Prompt, runDir, opts.CodeIntelCommand)
	if err != nil {
		logProgress(opts, "code-intelligence sensor degraded status=%s error=%q", artifact.Status, err.Error())
		return artifact, err
	}
	logProgress(opts, "code-intelligence sensor completed status=%s observations=%d", artifact.Status, len(artifact.Observations))
	return artifact, nil
}

func codeIntelArtifactPath(runDir string) string {
	return filepath.Join(runDir, "code-intel-round-1.json")
}
