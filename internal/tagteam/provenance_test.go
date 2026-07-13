package tagteam

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestComputeCapabilityProvenanceIsStableAndBinds(t *testing.T) {
	tools := mcpControlTools(true)
	first, err := ComputeCapabilityProvenance("v1.0.0", tools)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ComputeCapabilityProvenance("v1.0.0", tools)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Equal(second) || len(first.ToolSchemaDigest) != 64 {
		t.Fatalf("provenance not stable: %#v %#v", first, second)
	}
	// A different binary version is a different surface.
	otherVersion, err := ComputeCapabilityProvenance("v2.0.0", tools)
	if err != nil {
		t.Fatal(err)
	}
	if first.Equal(otherVersion) {
		t.Fatal("provenance did not bind the producer version")
	}
	// A different tool schema (read-only surface) is a different surface.
	readOnly, err := ComputeCapabilityProvenance("v1.0.0", mcpControlTools(false))
	if err != nil {
		t.Fatal(err)
	}
	if first.Equal(readOnly) {
		t.Fatal("provenance did not bind the tool schema surface")
	}
}

func TestVerifyCapabilityBaselineTOFUThenQuarantinesDrift(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	stateRoot := t.TempDir()
	runtime := NewControlRuntime(ControlService{RepositoryRoot: repo, StateRoot: stateRoot, ProducerVersion: "test"}, DefaultConfig(), nil)
	locator, err := resolveStateLocator(repo, stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := locator.Prepare(); err != nil {
		t.Fatal(err)
	}
	// Trust on first use records the baseline.
	if err := runtime.verifyCapabilityBaseline(locator.RepoRoot); err != nil {
		t.Fatalf("first verification failed: %v", err)
	}
	path := filepath.Join(locator.RepoRoot, capabilityBaselineName)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("baseline not recorded: %v", err)
	}
	// The same surface verifies without error.
	if err := runtime.verifyCapabilityBaseline(locator.RepoRoot); err != nil {
		t.Fatalf("matching verification failed: %v", err)
	}
	// Drift in the recorded baseline is quarantined.
	drifted := CapabilityProvenance{SchemaVersion: ControlContractVersion, ProducerVersion: "old-binary", ContractVersion: ControlContractVersion, ToolSchemaDigest: "deadbeef"}
	if err := writeCapabilityBaseline(path, drifted); err != nil {
		t.Fatal(err)
	}
	if err := runtime.verifyCapabilityBaseline(locator.RepoRoot); err == nil {
		t.Fatal("capability drift was not quarantined")
	}
}

func TestControlRuntimeStartQuarantinesCapabilityDrift(t *testing.T) {
	repo, _ := createResumeFixtureRepo(t)
	stateRoot := t.TempDir()
	service := ControlService{RepositoryRoot: repo, StateRoot: stateRoot, ProducerVersion: "test"}
	runtime := NewControlRuntime(service, DefaultConfig(), nil)
	locator, err := resolveStateLocator(repo, stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := locator.Prepare(); err != nil {
		t.Fatal(err)
	}
	// A baseline recorded for a different binary quarantines the start.
	drifted := CapabilityProvenance{SchemaVersion: ControlContractVersion, ProducerVersion: "different-binary", ContractVersion: ControlContractVersion, ToolSchemaDigest: "0000"}
	if err := writeCapabilityBaseline(filepath.Join(locator.RepoRoot, capabilityBaselineName), drifted); err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Start(context.Background(), controlStartFixture(t, repo))
	assertControlStartError(t, err, "capability_quarantined")

	// The quarantined start must not consume an approval or reserve a run.
	ledger, err := readControlApprovalLedger(filepath.Join(locator.RepoRoot, controlApprovalLedgerName))
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger.Starts) != 0 {
		t.Fatalf("quarantined start consumed an approval: %#v", ledger.Starts)
	}
}
