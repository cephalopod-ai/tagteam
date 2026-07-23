package tagteam

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func readRunPrompt(runDir, fallback string) (string, error) {
	inputPath := filepath.Join(runDir, "input.md")
	if fileExists(inputPath) {
		data, err := os.ReadFile(inputPath)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	metaPath := filepath.Join(runDir, "meta.json")
	if fileExists(metaPath) {
		meta, err := readMeta(metaPath)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(meta.Prompt) != "" {
			return meta.Prompt, nil
		}
	}
	if strings.TrimSpace(fallback) != "" {
		return fallback, nil
	}
	return "", fmt.Errorf("run prompt not found in %s", runDir)
}

// writeNormalizedOutput keeps the primary role artifact safe for later role
// prompts. Vendor envelopes belong only in the redacted diagnostic sidecar.
func writeNormalizedOutput(req Request, role Role, result Result) error {
	if req.OutputPath == "" {
		return nil
	}
	if err := guardControlResumeWritePath(req.controlResumeGate, req.OutputPath); err != nil {
		return &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	switch role {
	case RoleCoder:
		if result.Worker != nil {
			return writeJSONWithNewline(req.OutputPath, result.Worker)
		}
	case RoleAdversary:
		if result.Review != nil {
			return writeJSONWithNewline(req.OutputPath, result.Review)
		}
	case RoleScout:
		if result.Scout != nil {
			return writeJSONWithNewline(req.OutputPath, result.Scout)
		}
	}
	return writeRedactedBytes(req.OutputPath, []byte(result.Text), req.EnvOverlay)
}

// writeRecoveryOutput retains sanitized invalid output only when the existing
// JSON-repair path needs it. Successful role artifacts always use normalized
// contract data through writeNormalizedOutput.
func writeRecoveryOutput(req Request, raw []byte) error {
	if req.OutputPath == "" {
		return nil
	}
	if err := guardControlResumeWritePath(req.controlResumeGate, req.OutputPath); err != nil {
		return &ExitError{Code: ExitPreflightFailed, Err: err}
	}
	return writeRedactedBytes(req.OutputPath, raw, req.EnvOverlay)
}
