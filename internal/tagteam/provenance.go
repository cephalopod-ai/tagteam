package tagteam

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const capabilityBaselineName = "capability-baseline.json"

// CapabilityProvenance fingerprints the control-plane capability surface a host
// approved: the producer (Tagteam binary) version, the contract version, and a
// digest of the advertised MCP tool schemas. Drift in any of these — a new
// binary, a changed contract, or a changed/added tool schema — moves the surface
// outside the approved baseline and is quarantined.
type CapabilityProvenance struct {
	SchemaVersion    int    `json:"schema_version"`
	ProducerVersion  string `json:"producer_version"`
	ContractVersion  int    `json:"contract_version"`
	ToolSchemaDigest string `json:"tool_schema_digest"`
}

// ComputeCapabilityProvenance derives the provenance for a producer version and
// its advertised tool set. The digest is stable for a given binary because
// encoding/json marshals map keys in sorted order.
func ComputeCapabilityProvenance(producerVersion string, tools []map[string]any) (CapabilityProvenance, error) {
	payload, err := json.Marshal(tools)
	if err != nil {
		return CapabilityProvenance{}, fmt.Errorf("encode capability tools: %w", err)
	}
	digest := sha256.Sum256(payload)
	return CapabilityProvenance{
		SchemaVersion:    ControlContractVersion,
		ProducerVersion:  normalizedProducerVersion(producerVersion),
		ContractVersion:  ControlContractVersion,
		ToolSchemaDigest: hex.EncodeToString(digest[:]),
	}, nil
}

// Equal reports whether two provenance records describe the same surface.
func (p CapabilityProvenance) Equal(other CapabilityProvenance) bool {
	return p.SchemaVersion == other.SchemaVersion &&
		p.ProducerVersion == other.ProducerVersion &&
		p.ContractVersion == other.ContractVersion &&
		p.ToolSchemaDigest == other.ToolSchemaDigest
}

func readCapabilityBaseline(path string) (CapabilityProvenance, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return CapabilityProvenance{}, false, nil
	}
	if err != nil {
		return CapabilityProvenance{}, false, fmt.Errorf("read capability baseline: %w", err)
	}
	var provenance CapabilityProvenance
	if err := json.Unmarshal(data, &provenance); err != nil {
		return CapabilityProvenance{}, false, fmt.Errorf("decode capability baseline: %w", err)
	}
	return provenance, true, nil
}

func writeCapabilityBaseline(path string, provenance CapabilityProvenance) error {
	return writeJSONDurable(path, provenance, true, true)
}

// runtimeCapabilityProvenance is the provenance of this runtime's advertised
// lifecycle tool surface.
func (r *ControlRuntime) runtimeCapabilityProvenance() (CapabilityProvenance, error) {
	return ComputeCapabilityProvenance(r.service.ProducerVersion, mcpControlTools(true))
}

// verifyCapabilityBaseline enforces capability/version provenance before a
// lifecycle mutation. On first use it records the current surface as the
// approved baseline (trust on first use). If a baseline exists and the current
// surface differs, it returns a quarantine error so the mutation fails closed
// until an operator re-approves by removing the baseline file. A malformed
// baseline also quarantines rather than being silently trusted.
func (r *ControlRuntime) verifyCapabilityBaseline(repoRoot string) error {
	current, err := r.runtimeCapabilityProvenance()
	if err != nil {
		return err
	}
	path := filepath.Join(repoRoot, capabilityBaselineName)
	baseline, present, err := readCapabilityBaseline(path)
	if err != nil {
		return err
	}
	if !present {
		return writeCapabilityBaseline(path, current)
	}
	if !baseline.Equal(current) {
		return fmt.Errorf("capability surface changed outside the approved baseline (binary %q vs %q or tool-schema drift); re-approve by removing %s", baseline.ProducerVersion, current.ProducerVersion, capabilityBaselineName)
	}
	return nil
}
