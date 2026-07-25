package tagteam

import (
	"fmt"
	"strings"
)

const maxAdapterDiagnosticLineBytes = 512

func firstAdapterDiagnosticLine(message, fallback string) string {
	for _, line := range strings.Split(strings.TrimSpace(message), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			if len(line) > maxAdapterDiagnosticLineBytes {
				return strings.ToValidUTF8(line[:maxAdapterDiagnosticLineBytes], "") + "..."
			}
			return line
		}
	}
	return fallback
}

// conciseAdapterResultError preserves output-contract classification while
// keeping provider echoes out of user-facing retries, status, and final state.
// The complete redacted diagnostic remains in the persisted validation artifact.
func conciseAdapterResultError(adapterID string, cause error, validationPath string, envOverlay map[string]string) error {
	if cause == nil {
		return nil
	}
	message := firstAdapterDiagnosticLine(redactSecretsWithOverlay(cause.Error(), envOverlay), "adapter result validation failed")
	if validationPath != "" {
		message += "\nfull validation error: " + validationPath
	}
	if IsOutputContractError(cause) {
		return &OutputContractError{Err: fmt.Errorf("%s output contract error: %s", adapterID, message)}
	}
	return fmt.Errorf("%s result processing failed: %s", adapterID, message)
}
