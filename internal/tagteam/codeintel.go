package tagteam

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	maxCodeIntelObservations = 20
	maxCodeIntelErrors       = 20
	maxCodeIntelSummaryBytes = 240
	maxCodeIntelPathBytes    = 500
	maxCodeIntelPromptBytes  = 12 * 1024
	maxCodeIntelArtifactSize = 256 * 1024
)

var codeIntelRevisionPattern = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

const (
	codeIntelStatusOK                  = "ok"
	codeIntelStatusStale               = "stale"
	codeIntelStatusProviderUnavailable = "provider_unavailable"
	codeIntelStatusDisabled            = "disabled"
	codeIntelStatusError               = "error"
	codeIntelStalenessFresh            = "fresh"
	codeIntelStalenessDirty            = "dirty"
	codeIntelStalenessStale            = "stale"
	codeIntelStalenessUnknown          = "unknown"
)

type CodeIntelObservation struct {
	SchemaVersion int                 `json:"schema_version"`
	Provider      string              `json:"provider"`
	Revision      string              `json:"revision"`
	Kind          string              `json:"kind"`
	Subject       string              `json:"subject"`
	Summary       string              `json:"summary"`
	Evidence      []RetrievalEvidence `json:"evidence,omitempty"`
	Confidence    float64             `json:"confidence"`
	GeneratedAt   time.Time           `json:"generated_at"`
	Staleness     string              `json:"staleness,omitempty"`
}

type CodeIntelArtifact struct {
	SchemaVersion int                    `json:"schema_version"`
	Status        string                 `json:"status"`
	Observations  []CodeIntelObservation `json:"observations"`
	Staleness     string                 `json:"staleness"`
	Errors        []string               `json:"errors,omitempty"`
	Truncated     bool                   `json:"truncated"`
	GeneratedAt   time.Time              `json:"generated_at"`
}

func (a CodeIntelArtifact) Validate(workdir string) error {
	if a.SchemaVersion != ArtifactSchemaVersion {
		return fmt.Errorf("code-intel schema_version %d, want %d", a.SchemaVersion, ArtifactSchemaVersion)
	}
	if !validCodeIntelStatus(a.Status) {
		return fmt.Errorf("invalid code-intel status %q", a.Status)
	}
	if !validCodeIntelStaleness(a.Staleness) {
		return fmt.Errorf("invalid code-intel staleness %q", a.Staleness)
	}
	if len(a.Observations) > maxCodeIntelObservations {
		return fmt.Errorf("code-intel observations exceed %d", maxCodeIntelObservations)
	}
	if len(a.Errors) > maxCodeIntelErrors {
		return fmt.Errorf("code-intel errors exceed %d", maxCodeIntelErrors)
	}
	for _, observation := range a.Observations {
		if err := observation.validate(workdir); err != nil {
			return err
		}
	}
	data, err := json.Marshal(a)
	if err != nil {
		return fmt.Errorf("marshal code-intel artifact: %w", err)
	}
	if len(data) > maxCodeIntelArtifactSize {
		return fmt.Errorf("code-intel artifact exceeds %d bytes", maxCodeIntelArtifactSize)
	}
	return nil
}

func (o CodeIntelObservation) validate(workdir string) error {
	if o.SchemaVersion != ArtifactSchemaVersion {
		return fmt.Errorf("code-intel observation schema_version %d, want %d", o.SchemaVersion, ArtifactSchemaVersion)
	}
	if strings.TrimSpace(o.Provider) == "" {
		return fmt.Errorf("code-intel observation provider is required")
	}
	if !codeIntelRevisionPattern.MatchString(strings.TrimSpace(o.Revision)) {
		return fmt.Errorf("code-intel observation revision must be a 40-character SHA")
	}
	if strings.TrimSpace(o.Kind) == "" {
		return fmt.Errorf("code-intel observation kind is required")
	}
	if strings.TrimSpace(o.Subject) == "" {
		return fmt.Errorf("code-intel observation subject is required")
	}
	if len([]byte(o.Subject)) > maxCodeIntelPathBytes {
		return fmt.Errorf("code-intel observation subject exceeds %d bytes", maxCodeIntelPathBytes)
	}
	if err := validateCodeIntelSubject(workdir, o.Subject); err != nil {
		return err
	}
	if len([]byte(o.Summary)) > maxCodeIntelSummaryBytes {
		return fmt.Errorf("code-intel observation summary exceeds %d bytes", maxCodeIntelSummaryBytes)
	}
	if math.IsNaN(o.Confidence) || math.IsInf(o.Confidence, 0) || o.Confidence < 0 || o.Confidence > 1 {
		return fmt.Errorf("code-intel observation confidence must be between 0 and 1")
	}
	if o.GeneratedAt.IsZero() {
		return fmt.Errorf("code-intel observation generated_at is required")
	}
	for _, evidence := range o.Evidence {
		if strings.TrimSpace(evidence.File) == "" {
			return fmt.Errorf("code-intel evidence file is required")
		}
		if filepath.IsAbs(evidence.File) {
			return fmt.Errorf("code-intel evidence path must be relative")
		}
		if _, ok := safeRelPath(workdir, filepath.Join(workdir, evidence.File)); !ok {
			return fmt.Errorf("code-intel evidence path escapes workdir: %q", evidence.File)
		}
		if len([]byte(evidence.File)) > maxCodeIntelPathBytes || len([]byte(evidence.Reason)) > maxCodeIntelSummaryBytes {
			return fmt.Errorf("code-intel evidence exceeds bounded field length")
		}
	}
	return nil
}

func validCodeIntelStatus(status string) bool {
	switch status {
	case codeIntelStatusOK, codeIntelStatusStale, codeIntelStatusProviderUnavailable, codeIntelStatusDisabled, codeIntelStatusError:
		return true
	default:
		return false
	}
}

func validCodeIntelStaleness(staleness string) bool {
	switch staleness {
	case codeIntelStalenessFresh, codeIntelStalenessDirty, codeIntelStalenessStale, codeIntelStalenessUnknown:
		return true
	default:
		return false
	}
}

func normalizeCodeIntelArtifact(ctx context.Context, workdir string, artifact CodeIntelArtifact) (CodeIntelArtifact, error) {
	if artifact.SchemaVersion != ArtifactSchemaVersion {
		return CodeIntelArtifact{}, fmt.Errorf("code-intel schema_version %d, want %d", artifact.SchemaVersion, ArtifactSchemaVersion)
	}
	if artifact.GeneratedAt.IsZero() {
		artifact.GeneratedAt = time.Now().UTC()
	}
	artifact.Observations = append([]CodeIntelObservation(nil), artifact.Observations...)
	artifact.Errors = append([]string(nil), artifact.Errors...)
	valid := make([]CodeIntelObservation, 0, minInt(len(artifact.Observations), maxCodeIntelObservations))
	for index, observation := range artifact.Observations {
		if index == maxCodeIntelObservations {
			artifact.Truncated = true
			artifact.Errors = appendCodeIntelError(artifact.Errors, "provider returned more observations than allowed")
			break
		}
		if err := observation.validate(workdir); err != nil {
			artifact.Errors = appendCodeIntelError(artifact.Errors, err.Error())
			continue
		}
		observation.Provider = strings.TrimSpace(observation.Provider)
		observation.Subject = filepath.ToSlash(strings.TrimSpace(observation.Subject))
		observation.Summary = redactSecrets(strings.TrimSpace(observation.Summary))
		observation.Staleness = classifyCodeIntelStaleness(ctx, workdir, observation.Revision)
		for evidenceIndex := range observation.Evidence {
			observation.Evidence[evidenceIndex].File = trimPathForEvidence(observation.Evidence[evidenceIndex].File)
			observation.Evidence[evidenceIndex].Reason = sanitizeCodeIntelText(observation.Evidence[evidenceIndex].Reason, maxCodeIntelSummaryBytes)
		}
		valid = append(valid, observation)
	}
	artifact.Observations = valid
	for index := range artifact.Errors {
		artifact.Errors[index] = sanitizeCodeIntelText(artifact.Errors[index], maxCodeIntelSummaryBytes)
	}
	artifact.Errors = capCodeIntelErrors(artifact.Errors, &artifact.Truncated)
	artifact.Staleness = aggregateCodeIntelStaleness(artifact.Observations)
	if len(artifact.Observations) == 0 {
		if len(artifact.Errors) > 0 && artifact.Status == "" {
			artifact.Status = codeIntelStatusError
		}
		if artifact.Status == "" {
			artifact.Status = codeIntelStatusOK
		}
	} else if artifact.Staleness != codeIntelStalenessFresh {
		artifact.Status = codeIntelStatusStale
	} else if len(artifact.Errors) > 0 {
		artifact.Status = codeIntelStatusError
	} else {
		artifact.Status = codeIntelStatusOK
	}
	if artifact.Staleness == "" {
		artifact.Staleness = codeIntelStalenessUnknown
	}
	for len(mustMarshalCodeIntel(artifact)) > maxCodeIntelArtifactSize && len(artifact.Observations) > 0 {
		artifact.Observations = artifact.Observations[:len(artifact.Observations)-1]
		artifact.Truncated = true
	}
	if err := artifact.Validate(workdir); err != nil {
		return CodeIntelArtifact{}, err
	}
	return artifact, nil
}

func classifyCodeIntelStaleness(ctx context.Context, workdir, revision string) string {
	if !codeIntelRevisionPattern.MatchString(strings.TrimSpace(revision)) {
		return codeIntelStalenessUnknown
	}
	if _, err := runCommand(ctx, workdir, "git", "symbolic-ref", "--quiet", "--short", "HEAD"); err != nil {
		return codeIntelStalenessUnknown
	}
	head, err := runCommand(ctx, workdir, "git", "rev-parse", "--verify", "HEAD")
	if err != nil || strings.TrimSpace(head) == "" {
		return codeIntelStalenessUnknown
	}
	if !strings.EqualFold(strings.TrimSpace(head), strings.TrimSpace(revision)) {
		return codeIntelStalenessStale
	}
	dirty, err := runCommand(ctx, workdir, "git", "status", "--porcelain")
	if err != nil {
		return codeIntelStalenessUnknown
	}
	if strings.TrimSpace(dirty) != "" {
		return codeIntelStalenessDirty
	}
	return codeIntelStalenessFresh
}

func aggregateCodeIntelStaleness(observations []CodeIntelObservation) string {
	if len(observations) == 0 {
		return codeIntelStalenessUnknown
	}
	result := codeIntelStalenessFresh
	for _, observation := range observations {
		switch observation.Staleness {
		case codeIntelStalenessUnknown:
			return codeIntelStalenessUnknown
		case codeIntelStalenessDirty:
			result = codeIntelStalenessDirty
		case codeIntelStalenessStale:
			if result == codeIntelStalenessFresh {
				result = codeIntelStalenessStale
			}
		}
	}
	return result
}

func CompactCodeIntelForPrompt(artifact CodeIntelArtifact) string {
	if artifact.Staleness != codeIntelStalenessFresh {
		return ""
	}
	compact := artifact
	compact.Errors = nil
	compact.Observations = append([]CodeIntelObservation(nil), artifact.Observations...)
	for index := range compact.Observations {
		compact.Observations[index].Summary = redactSecrets(compact.Observations[index].Summary)
		for evidenceIndex := range compact.Observations[index].Evidence {
			compact.Observations[index].Evidence[evidenceIndex].Reason = redactSecrets(compact.Observations[index].Evidence[evidenceIndex].Reason)
		}
	}
	return compactCodeIntel(compact, maxCodeIntelPromptBytes)
}

func CompactCodeIntelForPromptAggressive(artifact CodeIntelArtifact) string {
	if artifact.Staleness != codeIntelStalenessFresh {
		return ""
	}
	compact := artifact
	for index := range compact.Observations {
		compact.Observations[index].Summary = redactSecrets(compact.Observations[index].Summary)
		for evidenceIndex := range compact.Observations[index].Evidence {
			compact.Observations[index].Evidence[evidenceIndex].Reason = redactSecrets(compact.Observations[index].Evidence[evidenceIndex].Reason)
		}
	}
	return compactCodeIntel(compact, maxCodeIntelPromptBytes/8)
}

func compactCodeIntel(compact CodeIntelArtifact, maxBytes int) string {
	if maxBytes <= 0 {
		maxBytes = maxCodeIntelPromptBytes
	}
	compact.Errors = nil
	data, err := json.MarshalIndent(compact, "", "  ")
	if err != nil {
		return ""
	}
	for len(data) > maxBytes && len(compact.Observations) > 0 {
		compact.Observations = compact.Observations[:len(compact.Observations)-1]
		compact.Truncated = true
		data, err = json.MarshalIndent(compact, "", "  ")
		if err != nil {
			return ""
		}
	}
	if len(data) > maxBytes {
		return ""
	}
	return string(data)
}

func appendCodeIntelError(errors []string, message string) []string {
	return append(errors, sanitizeCodeIntelText(message, maxCodeIntelSummaryBytes))
}

func capCodeIntelErrors(errors []string, truncated *bool) []string {
	if len(errors) <= maxCodeIntelErrors {
		return errors
	}
	if truncated != nil {
		*truncated = true
	}
	return errors[:maxCodeIntelErrors]
}

func sanitizeCodeIntelText(value string, maxBytes int) string {
	value = strings.ReplaceAll(strings.TrimSpace(redactSecrets(value)), "\n", " ")
	if len([]byte(value)) <= maxBytes {
		return value
	}
	return string([]byte(value)[:maxBytes])
}

func validateCodeIntelSubject(workdir, subject string) error {
	subject = strings.TrimSpace(subject)
	pathPart := subject
	if colon := strings.Index(subject, ":"); colon > 0 && strings.Contains(subject[:colon], "/") {
		pathPart = subject[:colon]
	}
	if filepath.IsAbs(pathPart) {
		return fmt.Errorf("code-intel subject path must be relative")
	}
	if _, ok := safeRelPath(workdir, filepath.Join(workdir, pathPart)); !ok {
		return fmt.Errorf("code-intel subject path escapes workdir: %q", subject)
	}
	return nil
}

func mustMarshalCodeIntel(artifact CodeIntelArtifact) []byte {
	data, _ := json.Marshal(artifact)
	return data
}
