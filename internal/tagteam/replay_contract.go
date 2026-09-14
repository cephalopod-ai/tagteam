package tagteam

// Durable deterministic replay contracts. The JSON artifacts in this file are
// authority, not telemetry: every transition uses a durable replace and fails
// closed. Tagteam deterministically replays its durable control history.
// External operations without a durably recorded commit may execute again.
// Resume is therefore at-least-once at uncertain external-effect boundaries,
// not magically exactly-once.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const replayContractVersion = 1

type ExecutionSnapshot struct {
	SchemaVersion          int               `json:"schema_version"`
	RunID                  string            `json:"run_id"`
	WorkflowRevision       string            `json:"workflow_revision"`
	Digest                 string            `json:"digest"`
	Workflow               map[string]any    `json:"workflow"`
	Arguments              map[string]any    `json:"arguments"`
	Policies               map[string]any    `json:"policies"`
	Artifacts              map[string]string `json:"input_artifacts"`
	SecretReferenceDigests map[string]string `json:"secret_reference_digests,omitempty"`
	FrozenAt               time.Time         `json:"frozen_at"`
}

type CanonicalRequestEnvelope struct {
	CanonicalVersion      int               `json:"canonical_version"`
	OperationType         string            `json:"operation_type"`
	Destination           string            `json:"destination"`
	Provider              string            `json:"provider,omitempty"`
	Model                 string            `json:"model,omitempty"`
	Method                string            `json:"method,omitempty"`
	Path                  string            `json:"path,omitempty"`
	Arguments             any               `json:"arguments"`
	BodySHA256            string            `json:"body_sha256,omitempty"`
	Parameters            map[string]any    `json:"parameters"`
	Headers               map[string]string `json:"headers"`
	TimeoutNanos          int64             `json:"timeout_nanos"`
	RetryPolicy           string            `json:"retry_policy"`
	SandboxPolicy         string            `json:"sandbox_policy"`
	PermissionPolicy      string            `json:"permission_policy"`
	NetworkPolicy         string            `json:"network_policy"`
	InputArtifactHashes   map[string]string `json:"input_artifact_hashes"`
	IdempotencyKey        string            `json:"idempotency_key,omitempty"`
	SecretReferenceDigest string            `json:"secret_reference_digest,omitempty"`
}

func canonicalJSON(value any) ([]byte, error) {
	// encoding/json sorts map keys, uses UTF-8, and preserves null versus an
	// omitted map member. Disable HTML escaping so the wire vector is explicit.
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return nil, err
	}
	return []byte(strings.TrimSuffix(b.String(), "\n")), nil
}

func canonicalDigest(value any) (string, error) {
	b, err := canonicalJSON(value)
	if err != nil {
		return "", err
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:]), nil
}

func bytesDigest(value []byte) string { s := sha256.Sum256(value); return hex.EncodeToString(s[:]) }

func executionSnapshotFor(runID string, opts RunOptions) (ExecutionSnapshot, error) {
	secretRefs := map[string]string{}
	for key := range opts.EnvOverlay {
		secretRefs[key] = bytesDigest([]byte("env-value:v1:" + opts.EnvOverlay[key]))
	}
	workflow := map[string]any{
		"schema_version": replayContractVersion, "mode": opts.Mode, "rounds": opts.Rounds,
		"topology": []string{"planning", "implementing", "testing", "reviewing", "repairing"},
		"coder":    opts.Coder, "reviewer": opts.Adversary, "scout": opts.Scout,
		"profile_sources": append([]string(nil), opts.ConfigSources...), "job": opts.Job, "routing": opts.Routing,
	}
	arguments := map[string]any{
		"prompt": opts.Prompt, "workdir": opts.Workdir, "allowed_paths": append([]string(nil), opts.AllowedPaths...), "package": opts.Package,
		"test": opts.TestCmd, "tests": append([]string(nil), opts.TestCommands...), "lint": opts.LintCmd, "no_test": opts.NoTest,
		"allow_dirty": opts.AllowDirty, "autostash": opts.Autostash,
		"passthrough": map[string]any{"codex": opts.CodexArgs, "claude": opts.ClaudeArgs, "agy": opts.AgyArgs, "gosling": opts.GoslingArgs, "grok": opts.GrokArgs, "openai_compatible": opts.OpenAICompatibleArgs, "mistral_acp": opts.MistralAcpArgs},
	}
	policies := map[string]any{
		"timeout_nanos": opts.Timeout.Nanoseconds(), "watchdog_timeout_nanos": opts.WatchdogTimeout.Nanoseconds(), "max_wall_time_nanos": opts.MaxWallTime.Nanoseconds(),
		"max_role_invocations": opts.MaxRoleInvocations, "max_output_bytes": opts.MaxOutputBytes, "max_findings": opts.MaxFindings,
		"loss": opts.LossPolicy, "fallbacks": opts.Fallbacks, "fallbacks_by_target": opts.FallbacksByTarget, "git_safety": opts.GitSafety,
		"scout_failure": opts.ScoutFailurePolicy, "scout_retrieval": opts.ScoutRetrieval, "scout_context": opts.ScoutContextPolicy,
		"supervisor_can_edit": opts.SupervisorCanEdit, "supervisor_slicing": opts.SupervisorSlicing, "max_packages": opts.MaxPackages,
		"auto_next_package": opts.AutoNextPackage, "respect_repo_instructions": opts.RespectRepoInstructions, "decision_memory": opts.DecisionMemory,
		"json_repair": opts.JSONRepair, "churn": opts.Churn,
	}
	revision, err := canonicalDigest(workflow)
	if err != nil {
		return ExecutionSnapshot{}, err
	}
	s := ExecutionSnapshot{SchemaVersion: replayContractVersion, RunID: runID, WorkflowRevision: revision, Workflow: workflow, Arguments: arguments, Policies: policies, Artifacts: map[string]string{}, SecretReferenceDigests: secretRefs, FrozenAt: time.Now().UTC()}
	digestable := s
	digestable.Digest = ""
	digestable.FrozenAt = time.Time{}
	s.Digest, err = canonicalDigest(digestable)
	return s, err
}

func freezeExecutionSnapshot(runDir, runID string, opts RunOptions) (ExecutionSnapshot, error) {
	want, err := executionSnapshotFor(runID, opts)
	if err != nil {
		return ExecutionSnapshot{}, err
	}
	path := filepath.Join(runDir, "execution-snapshot.json")
	var have ExecutionSnapshot
	if data, readErr := os.ReadFile(path); readErr == nil {
		if json.Unmarshal(data, &have) != nil || have.SchemaVersion != replayContractVersion || have.Digest == "" {
			return ExecutionSnapshot{}, &DivergenceError{Reason: "missing_or_corrupt_snapshot"}
		}
		if want.Digest != have.Digest {
			return ExecutionSnapshot{}, &DivergenceError{Reason: "immutable_snapshot_mismatch", Expected: have.Digest, Actual: want.Digest}
		}
		return have, nil
	} else if !os.IsNotExist(readErr) {
		return ExecutionSnapshot{}, readErr
	}
	if err := writeJSONWithNewline(path, want); err != nil {
		return ExecutionSnapshot{}, fmt.Errorf("freeze execution snapshot: %w", err)
	}
	return want, nil
}

type OperationState string

const (
	OperationPrepared  OperationState = "prepared"
	OperationInFlight  OperationState = "in_flight"
	OperationCommitted OperationState = "committed"
	OperationFailed    OperationState = "failed"
	OperationInDoubt   OperationState = "in_doubt"
)

type OperationRecord struct {
	SchemaVersion       int               `json:"schema_version"`
	RunID               string            `json:"run_id"`
	WorkflowRevision    string            `json:"workflow_revision"`
	Sequence            uint64            `json:"sequence"`
	OperationType       string            `json:"operation_type"`
	RequestHash         string            `json:"request_hash"`
	RequestMetadata     map[string]string `json:"request_metadata,omitempty"`
	IdempotencyKey      string            `json:"idempotency_key,omitempty"`
	State               OperationState    `json:"state"`
	ResultHash          string            `json:"result_hash,omitempty"`
	Result              *Result           `json:"result,omitempty"`
	Attempt             int               `json:"attempt"`
	DuplicateEffectRisk bool              `json:"duplicate_effect_risk,omitempty"`
	Recovery            string            `json:"recovery,omitempty"`
	Owner               string            `json:"owner,omitempty"`
	FencingGeneration   uint64            `json:"fencing_generation"`
	PreparedAt          time.Time         `json:"prepared_at"`
	UpdatedAt           time.Time         `json:"updated_at"`
}

type DivergenceError struct{ Reason, Expected, Actual string }

func (e *DivergenceError) Error() string {
	return fmt.Sprintf("deterministic replay divergence: %s (expected=%s actual=%s)", e.Reason, e.Expected, e.Actual)
}

var replayMu sync.Mutex

func operationFiles(runDir string) ([]string, error) {
	dir := filepath.Join(runDir, "operations")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}

func readOperation(path string) (OperationRecord, error) {
	var r OperationRecord
	b, err := os.ReadFile(path)
	if err != nil {
		return r, err
	}
	err = json.Unmarshal(b, &r)
	if err == nil && (r.SchemaVersion != replayContractVersion || r.Sequence == 0 || r.RequestHash == "") {
		err = errors.New("corrupt operation record")
	}
	return r, err
}
func operationPath(runDir string, seq uint64) string {
	return filepath.Join(runDir, "operations", fmt.Sprintf("%020d.json", seq))
}

// prepareOperation atomically allocates and persists the operation before the
// external boundary. An unresolved tail is retried only on an exact match.
func prepareOperation(runDir, opType string, envelope CanonicalRequestEnvelope, metadata map[string]string) (OperationRecord, *Result, error) {
	replayMu.Lock()
	defer replayMu.Unlock()
	snapData, err := os.ReadFile(filepath.Join(runDir, "execution-snapshot.json"))
	if err != nil {
		return OperationRecord{}, nil, &DivergenceError{Reason: "missing_or_corrupt_snapshot"}
	}
	var snap ExecutionSnapshot
	if json.Unmarshal(snapData, &snap) != nil || snap.Digest == "" {
		return OperationRecord{}, nil, &DivergenceError{Reason: "missing_or_corrupt_snapshot"}
	}
	hash, err := canonicalDigest(envelope)
	if err != nil {
		return OperationRecord{}, nil, err
	}
	files, err := operationFiles(runDir)
	if err != nil {
		return OperationRecord{}, nil, err
	}
	var tail OperationRecord
	if len(files) > 0 {
		tail, err = readOperation(files[len(files)-1])
		if err != nil {
			return OperationRecord{}, nil, &DivergenceError{Reason: "missing_or_corrupt_operation"}
		}
	}
	if tail.State == OperationPrepared || tail.State == OperationInFlight || tail.State == OperationInDoubt {
		if tail.WorkflowRevision != snap.WorkflowRevision || tail.OperationType != opType || tail.RequestHash != hash {
			return OperationRecord{}, nil, persistDivergence(runDir, tail, opType, hash, "unresolved_operation_mismatch")
		}
		if tail.State == OperationInFlight {
			tail.State = OperationInDoubt
			tail.DuplicateEffectRisk = true
			tail.Recovery = "crash_after_dispatch_before_durable_commit"
			tail.UpdatedAt = time.Now().UTC()
			if err = writeJSONWithNewline(operationPath(runDir, tail.Sequence), tail); err != nil {
				return tail, nil, err
			}
		}
		tail.Attempt++
		tail.DuplicateEffectRisk = tail.State == OperationInDoubt
		tail.UpdatedAt = time.Now().UTC()
		if err = writeJSONWithNewline(operationPath(runDir, tail.Sequence), tail); err != nil {
			return tail, nil, err
		}
		return tail, nil, nil
	}
	if tail.State == OperationCommitted && tail.WorkflowRevision == snap.WorkflowRevision && tail.OperationType == opType && tail.RequestHash == hash {
		return tail, tail.Result, nil
	}
	seq := uint64(1)
	if tail.Sequence > 0 {
		seq = tail.Sequence + 1
	}
	r := OperationRecord{SchemaVersion: replayContractVersion, RunID: snap.RunID, WorkflowRevision: snap.WorkflowRevision, Sequence: seq, OperationType: opType, RequestHash: hash, RequestMetadata: metadata, IdempotencyKey: envelope.IdempotencyKey, State: OperationPrepared, Attempt: 1, FencingGeneration: seq, PreparedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err = os.MkdirAll(filepath.Join(runDir, "operations"), 0700); err != nil {
		return r, nil, err
	}
	if err = writeJSONWithNewline(operationPath(runDir, seq), r); err != nil {
		return r, nil, err
	}
	return r, nil, nil
}

func persistDivergence(runDir string, expected OperationRecord, typ, hash, reason string) error {
	d := map[string]any{"schema_version": replayContractVersion, "sequence": expected.Sequence, "reason": reason, "expected_type": expected.OperationType, "actual_type": typ, "expected_hash": expected.RequestHash, "actual_hash": hash, "at": time.Now().UTC()}
	if err := writeJSONWithNewline(filepath.Join(runDir, "divergence.json"), d); err != nil {
		return err
	}
	return &DivergenceError{Reason: reason, Expected: expected.OperationType + ":" + expected.RequestHash, Actual: typ + ":" + hash}
}

func transitionOperation(runDir string, r OperationRecord, state OperationState, result *Result, recovery string) error {
	replayMu.Lock()
	defer replayMu.Unlock()
	current, err := readOperation(operationPath(runDir, r.Sequence))
	if err != nil {
		return err
	}
	if current.FencingGeneration != r.FencingGeneration {
		return &DivergenceError{Reason: "stale_fence"}
	}
	current.State = state
	current.Recovery = recovery
	current.UpdatedAt = time.Now().UTC()
	if result != nil {
		b, _ := canonicalJSON(result)
		current.ResultHash = bytesDigest(b)
		copy := *result
		copy.Raw = nil
		current.Result = &copy
	}
	return writeJSONWithNewline(operationPath(runDir, r.Sequence), current)
}

// reconcileOperation gives provider-native lookup the first chance to resolve
// an uncertain exact operation without dispatching it again.
func reconcileOperation(runDir string, r OperationRecord, lookup func(string) (*Result, bool, error)) (*Result, error) {
	if r.State != OperationInDoubt {
		return nil, fmt.Errorf("operation is not in_doubt")
	}
	result, found, err := lookup(r.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	if result == nil {
		return nil, errors.New("reconciler returned a manufactured empty result")
	}
	if err := transitionOperation(runDir, r, OperationCommitted, result, "provider_native_reconciliation"); err != nil {
		return nil, err
	}
	return result, nil
}

func adapterEnvelope(adapter Adapter, role Role, req Request) CanonicalRequestEnvelope {
	stdinHash := ""
	if len(req.Stdin) > 0 {
		stdinHash = bytesDigest(req.Stdin)
	}
	return CanonicalRequestEnvelope{CanonicalVersion: replayContractVersion, OperationType: "provider_request.v1", Destination: adapter.ID(), Provider: adapter.ID(), Model: req.Model, Arguments: map[string]any{"role": role, "prompt": req.Prompt, "system_prompt": req.SystemPrompt, "phase": req.Phase, "input_mode": req.InputMode, "stdin_sha256": stdinHash}, Parameters: map[string]any{"max_output_bytes": maxOutputBytes(req), "watchdog_timeout_nanos": req.WatchdogTimeout.Nanoseconds(), "passthrough": req.Passthrough}, Headers: map[string]string{}, TimeoutNanos: req.Timeout.Nanoseconds(), RetryPolicy: "adapter-role-policy.v1", SandboxPolicy: fmt.Sprintf("enforce_allowed_scope=%t", req.EnforceAllowedScope), PermissionPolicy: strings.Join(req.AllowedScope, "\x00"), NetworkPolicy: "adapter-defined.v1", InputArtifactHashes: map[string]string{}, IdempotencyKey: bytesDigest([]byte(req.RunDir + "\x00" + req.Phase + "\x00" + string(role)))}
}

func prepareAdapterReplay(req Request, adapter Adapter, role Role) (OperationRecord, *Result, error) {
	envelope := adapterEnvelope(adapter, role, req)
	return prepareOperation(req.RunDir, envelope.OperationType, envelope, map[string]string{
		"role": string(role), "provider": adapter.ID(), "phase": req.Phase,
	})
}
