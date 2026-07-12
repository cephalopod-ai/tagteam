package tagteam

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	controlApprovalLedgerName = "control-approvals.json"
	controlMaxApprovalRecords = 1024
)

type ControlRuntime struct {
	service ControlService
	config  Config
	sources []string

	mu   sync.Mutex
	jobs map[string]context.CancelFunc
}

type controlApprovalLedger struct {
	SchemaVersion int                  `json:"schema_version"`
	Starts        []controlStartRecord `json:"starts"`
}

type controlStartRecord struct {
	IdempotencyKey string    `json:"idempotency_key"`
	ActionDigest   string    `json:"action_digest"`
	Nonce          string    `json:"nonce"`
	RunID          string    `json:"run_id"`
	CreatedAt      time.Time `json:"created_at"`
	ExpiresAt      time.Time `json:"expires_at"`
}

func NewControlRuntime(service ControlService, cfg Config, sources []string) *ControlRuntime {
	return &ControlRuntime{service: service, config: cfg, sources: append([]string(nil), sources...), jobs: map[string]context.CancelFunc{}}
}

func (r *ControlRuntime) Capabilities() ControlCapabilitySet {
	capabilities := r.service.Capabilities()
	capabilities.Capabilities = append(capabilities.Capabilities, "start")
	return capabilities
}

func (r *ControlRuntime) Status(runID string) (ControlStatus, error) {
	status, err := r.service.Status(runID)
	if err == nil {
		return status, nil
	}
	locator, locatorErr := resolveStateLocator(r.service.RepositoryRoot, r.service.StateRoot)
	if locatorErr != nil {
		return ControlStatus{}, err
	}
	ledger, ledgerErr := readControlApprovalLedger(filepath.Join(locator.RepoRoot, controlApprovalLedgerName))
	if ledgerErr != nil {
		return ControlStatus{}, ledgerErr
	}
	for _, record := range ledger.Starts {
		if record.RunID != runID {
			continue
		}
		runDir, runDirErr := locator.RunDir(runID)
		if runDirErr != nil {
			return ControlStatus{}, runDirErr
		}
		snapshot := RunSnapshot{SchemaVersion: ArtifactSchemaVersion, RunID: runID, RunDir: runDir, Status: string(RunStatusRunning), Phase: "preflight", UpdatedAt: record.CreatedAt}
		payload, marshalErr := json.Marshal(snapshot)
		if marshalErr != nil {
			return ControlStatus{}, marshalErr
		}
		return ControlStatus{SchemaVersion: ControlContractVersion, SnapshotID: sha256Hex(payload), Completeness: ControlPartial, Run: snapshot}, nil
	}
	return ControlStatus{}, err
}

func (r *ControlRuntime) Start(ctx context.Context, request ControlStartRequest) (ControlRunHandle, error) {
	if request.SchemaVersion != ControlContractVersion {
		return ControlRunHandle{}, fmt.Errorf("unsupported control schema_version %d (want %d)", request.SchemaVersion, ControlContractVersion)
	}
	normalized, err := NormalizeControlLaunch(request.Launch)
	if err != nil {
		return ControlRunHandle{}, err
	}
	request.Launch = normalized
	digest, err := ControlStartActionDigest(request)
	if err != nil {
		return ControlRunHandle{}, err
	}
	if err := validateControlApproval(request.Approval, digest); err != nil {
		return ControlRunHandle{}, err
	}
	opts, err := r.optionsForLaunch(normalized)
	if err != nil {
		return ControlRunHandle{}, err
	}
	locator, err := resolveStateLocator(opts.Workdir, opts.StateRoot)
	if err != nil {
		return ControlRunHandle{}, fmt.Errorf("resolve start state root: %w", err)
	}
	if err := locator.Prepare(); err != nil {
		return ControlRunHandle{}, fmt.Errorf("prepare start state root: %w", err)
	}
	if active, err := readActiveAt(filepath.Join(locator.RepoRoot, "active.json")); err == nil && active.Status == string(RunStatusRunning) {
		return ControlRunHandle{}, fmt.Errorf("run %q is already active for this worktree", active.RunID)
	}

	lock, err := acquireRunLock(locator.RepoRoot, false)
	if err != nil {
		return ControlRunHandle{}, fmt.Errorf("control approval ledger is locked: %w", err)
	}
	defer lock.Release()
	ledgerPath := filepath.Join(locator.RepoRoot, controlApprovalLedgerName)
	ledger, err := readControlApprovalLedger(ledgerPath)
	if err != nil {
		return ControlRunHandle{}, err
	}
	ledger.Starts = pruneControlStartRecords(ledger.Starts, time.Now().UTC())
	for _, record := range ledger.Starts {
		if record.IdempotencyKey != request.IdempotencyKey {
			continue
		}
		if record.ActionDigest != digest {
			return ControlRunHandle{}, fmt.Errorf("idempotency_key is already bound to a different start action")
		}
		return ControlRunHandle{SchemaVersion: ControlContractVersion, RunID: record.RunID, ProducerVersion: normalizedProducerVersion(r.service.ProducerVersion)}, nil
	}
	for _, record := range ledger.Starts {
		if record.Nonce == request.Approval.Nonce {
			return ControlRunHandle{}, fmt.Errorf("approval nonce has already been consumed")
		}
	}
	for _, record := range ledger.Starts {
		active, activeErr := controlStartRecordActive(locator, record, time.Now().UTC())
		if activeErr != nil {
			return ControlRunHandle{}, activeErr
		}
		if active {
			return ControlRunHandle{}, fmt.Errorf("run %q is already pending or active for this worktree", record.RunID)
		}
	}
	runID, err := nextControlRunID(locator)
	if err != nil {
		return ControlRunHandle{}, err
	}
	ledger.Starts = append(ledger.Starts, controlStartRecord{
		IdempotencyKey: request.IdempotencyKey,
		ActionDigest:   digest,
		Nonce:          request.Approval.Nonce,
		RunID:          runID,
		CreatedAt:      time.Now().UTC(),
		ExpiresAt:      request.Approval.ExpiresAt,
	})
	if len(ledger.Starts) > controlMaxApprovalRecords {
		return ControlRunHandle{}, fmt.Errorf("control approval ledger reached its maximum retained records")
	}
	if err := writeJSONDurable(ledgerPath, ledger, false, true); err != nil {
		return ControlRunHandle{}, fmt.Errorf("persist consumed control approval: %w", err)
	}

	opts.RunID = runID
	runContext, cancel := context.WithCancel(ctx)
	r.mu.Lock()
	r.jobs[runID] = cancel
	r.mu.Unlock()
	go r.runStart(runContext, opts, runID)
	return ControlRunHandle{SchemaVersion: ControlContractVersion, RunID: runID, ProducerVersion: normalizedProducerVersion(r.service.ProducerVersion)}, nil
}

func (r *ControlRuntime) runStart(ctx context.Context, opts RunOptions, runID string) {
	defer func() {
		r.mu.Lock()
		delete(r.jobs, runID)
		r.mu.Unlock()
	}()
	final, err := NewApp(r.config).Run(ctx, opts)
	if err == nil || final.RunID != "" {
		return
	}
	// The normal runner creates its directory after preflight. Persist an
	// observable terminal artifact when preflight itself fails.
	r.persistStartFailure(opts, runID, err)
}

func (r *ControlRuntime) persistStartFailure(opts RunOptions, runID string, cause error) {
	runDir, err := createRunDir(opts.Workdir, opts.StateRoot, runID)
	if err != nil {
		return
	}
	message := redactSecretsWithOverlay(cause.Error(), opts.EnvOverlay)
	final := FinalRun{
		SchemaVersion:  ArtifactSchemaVersion,
		RunID:          runID,
		RunDir:         runDir,
		Workdir:        opts.Workdir,
		Mode:           opts.Mode,
		Verdict:        "error",
		Summary:        message,
		Status:         RunStatusFailed,
		BlockingReason: string(reasonForExit(ExitCode(cause))),
		ExitCode:       ExitCode(cause),
		StartedAt:      time.Now().UTC(),
		FinishedAt:     time.Now().UTC(),
	}
	if final.BlockingReason == "" {
		final.BlockingReason = string(ReasonWorkerUnavailable)
	}
	_ = writeRunState(runDir, RunState{SchemaVersion: runStateSchemaVersion, RunID: runID, Mode: opts.Mode, Status: string(final.Status), Phase: "preflight", BlockingReason: final.BlockingReason, ExitCode: final.ExitCode})
	_ = NewApp(r.config).persistFinal(opts.Workdir, final)
}

func (r *ControlRuntime) optionsForLaunch(spec ControlLaunchSpec) (RunOptions, error) {
	flags := FlagInputs{
		Mode:            string(spec.Team.Mode),
		Workdir:         spec.Repository.CanonicalRoot,
		StateRoot:       r.service.StateRoot,
		AllowedPaths:    append([]string(nil), spec.AllowedPaths...),
		Rounds:          spec.Rounds,
		Timeout:         time.Duration(spec.TimeBudget.InvocationTimeoutSeconds) * time.Second,
		WatchdogTimeout: time.Duration(spec.TimeBudget.WatchdogTimeoutSeconds) * time.Second,
		MaxWallTime:     time.Duration(spec.TimeBudget.WallTimeoutSeconds) * time.Second,
	}
	changed := map[string]bool{
		"mode":             true,
		"allow-path":       true,
		"rounds":           true,
		"watchdog-timeout": true,
		"max-wall-time":    true,
	}
	if strings.TrimSpace(r.service.StateRoot) != "" {
		changed["state-root"] = true
	}
	switch spec.Team.Mode {
	case ModeSupervisor:
		flags.Worker = controlRoleTargetString(*spec.Team.Worker)
		flags.Supervisor = controlRoleTargetString(*spec.Team.Supervisor)
		changed["worker"] = true
		changed["supervisor"] = true
	case ModeRelay:
		flags.CoderRole = controlRoleTargetString(*spec.Team.Coder)
		flags.Supervisor = controlRoleTargetString(*spec.Team.Supervisor)
		flags.Scout = controlRoleTargetString(*spec.Team.Scout)
		changed["coder"] = true
		changed["supervisor"] = true
		changed["scout"] = true
	case ModeAdversarial:
		flags.CoderRole = controlRoleTargetString(*spec.Team.Coder)
		flags.Reviewer = controlRoleTargetString(*spec.Team.Reviewer)
		changed["coder"] = true
		changed["reviewer"] = true
	case ModeSolo:
		flags.Solo = controlRoleTargetString(*spec.Team.Worker)
		changed["solo"] = true
	default:
		return RunOptions{}, fmt.Errorf("unsupported control mode %q", spec.Team.Mode)
	}
	if spec.TestPreset != "" {
		return RunOptions{}, fmt.Errorf("test_preset %q is unavailable until trusted preset resolution is implemented", spec.TestPreset)
	}
	opts, err := ResolveOptions(r.config, r.sources, flags, changed, spec.Prompt)
	if err != nil {
		return RunOptions{}, err
	}
	return opts, nil
}

func controlRoleTargetString(target ControlRoleTarget) string {
	if target.Model == "" {
		return target.Adapter
	}
	return target.Adapter + ":" + target.Model
}

func validateControlApproval(approval ControlApproval, expectedDigest string) error {
	now := time.Now().UTC()
	if approval.ActionDigest != expectedDigest {
		return fmt.Errorf("approval does not match the normalized start action")
	}
	if approval.Nonce == "" || strings.TrimSpace(approval.Nonce) != approval.Nonce || len(approval.Nonce) > controlMaxRoleBytes || containsControl(approval.Nonce) {
		return fmt.Errorf("approval nonce must be a normalized identifier no longer than %d bytes", controlMaxRoleBytes)
	}
	if approval.ApprovedAt.IsZero() || approval.ExpiresAt.IsZero() || approval.ApprovedAt.After(now) || !approval.ExpiresAt.After(now) || approval.ExpiresAt.Sub(approval.ApprovedAt) > ControlApprovalMaxLifetime {
		return fmt.Errorf("approval must be currently valid and expire within %s", ControlApprovalMaxLifetime)
	}
	return nil
}

func readControlApprovalLedger(path string) (controlApprovalLedger, error) {
	ledger := controlApprovalLedger{SchemaVersion: ControlContractVersion, Starts: []controlStartRecord{}}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ledger, nil
	}
	if err != nil {
		return controlApprovalLedger{}, fmt.Errorf("read control approval ledger: %w", err)
	}
	if err := json.Unmarshal(data, &ledger); err != nil {
		return controlApprovalLedger{}, fmt.Errorf("decode control approval ledger: %w", err)
	}
	if ledger.SchemaVersion != ControlContractVersion {
		return controlApprovalLedger{}, fmt.Errorf("unsupported control approval ledger schema_version %d", ledger.SchemaVersion)
	}
	return ledger, nil
}

func pruneControlStartRecords(records []controlStartRecord, now time.Time) []controlStartRecord {
	result := make([]controlStartRecord, 0, len(records))
	for _, record := range records {
		if record.ExpiresAt.After(now) {
			result = append(result, record)
		}
	}
	return result
}

// controlStartRecordActive closes the short gap between a durable approval
// reservation and the runner creating active.json. A terminal final artifact
// releases the worktree; an expired reservation may be retried with new input.
func controlStartRecordActive(locator StateLocator, record controlStartRecord, now time.Time) (bool, error) {
	if !record.ExpiresAt.After(now) {
		return false, nil
	}
	runDir, err := locator.RunDir(record.RunID)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(filepath.Join(runDir, "final.json"))
	if err == nil {
		return false, nil
	}
	if os.IsNotExist(err) {
		return true, nil
	}
	return false, fmt.Errorf("check control run %q terminal record: %w", record.RunID, err)
}

func nextControlRunID(locator StateLocator) (string, error) {
	for range 8 {
		entropy := make([]byte, 4)
		if _, err := rand.Read(entropy); err != nil {
			return "", fmt.Errorf("generate control run identifier: %w", err)
		}
		runID := newRunID() + "-mcp-" + hex.EncodeToString(entropy)
		runDir, err := locator.RunDir(runID)
		if err != nil {
			return "", err
		}
		if _, err := os.Lstat(runDir); os.IsNotExist(err) {
			return runID, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("unable to reserve a unique control run identifier")
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
