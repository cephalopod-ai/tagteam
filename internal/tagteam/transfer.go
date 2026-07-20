package tagteam

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type TransferRecord struct {
	SchemaVersion int       `json:"schema_version"`
	RunID         string    `json:"run_id"`
	Source        string    `json:"source"`
	Target        string    `json:"target"`
	Baseline      string    `json:"baseline"`
	PatchPath     string    `json:"patch_path"`
	PatchSHA256   string    `json:"patch_sha256"`
	TargetDiffSHA string    `json:"target_diff_sha256,omitempty"`
	FocusedTest   TestRun   `json:"focused_test"`
	Lint          TestRun   `json:"lint"`
	Status        string    `json:"status"`
	RolledBack    bool      `json:"rolled_back,omitempty"`
	Error         string    `json:"error,omitempty"`
	StartedAt     time.Time `json:"started_at"`
	FinishedAt    time.Time `json:"finished_at"`
}

func TransferRun(ctx context.Context, opts RunOptions, runID, targetOverride string) (record TransferRecord, err error) {
	runDir, err := runDirForWorkdir(opts.Workdir, runID)
	if err != nil {
		return record, err
	}
	final, err := readFinal(filepath.Join(runDir, "final.json"))
	if err != nil {
		return record, fmt.Errorf("read final run: %w", err)
	}
	record = TransferRecord{
		SchemaVersion: ArtifactSchemaVersion,
		RunID:         runID,
		Source:        final.Workdir,
		Baseline:      final.Baseline,
		PatchPath:     final.LatestDiffPath,
		PatchSHA256:   final.LatestDiffSHA256,
		Status:        "checking",
		StartedAt:     time.Now().UTC(),
	}
	defer func() {
		record.FinishedAt = time.Now().UTC()
		if err != nil {
			record.Status = "rejected"
			record.Error = err.Error()
		}
		_ = writeJSONWithNewline(filepath.Join(runDir, "transfer.json"), record)
	}()
	if final.Status == RunStatusCancelled || final.Status == RunStatusQuarantined || final.Status == RunStatusFailed || final.RoundLimitReached {
		return record, fmt.Errorf("run is not in a transferable passed state")
	}
	if final.Review == nil || final.Review.Verdict != "pass" {
		return record, fmt.Errorf("final independent review did not pass")
	}
	if validateErr := final.Review.Validate(); validateErr != nil {
		return record, fmt.Errorf("final review contract is invalid: %w", validateErr)
	}
	ledger, ledgerErr := loadFindingsLedger(runDir)
	if ledgerErr != nil {
		return record, fmt.Errorf("read findings ledger: %w", ledgerErr)
	}
	currentFindings := summarizeFindings(filepath.Join(runDir, findingsLedgerFilename), ledger)
	if currentFindings.OpenBlockerOrMajor > 0 {
		return record, fmt.Errorf("%d blocker or major findings remain open", currentFindings.OpenBlockerOrMajor)
	}
	if final.Regression == nil || final.Regression.Status == "new_failures" || final.Regression.Status == "unknown" {
		return record, fmt.Errorf("baseline-aware regression gate is missing or unresolved")
	}
	if len(final.QualityGates) == 0 {
		return record, fmt.Errorf("quality gates were not recorded")
	}
	latestGate := final.QualityGates[len(final.QualityGates)-1]
	if len(latestGate.AllowedScope) == 0 || (len(latestGate.AllowedScope) == 1 && latestGate.AllowedScope[0] == ".") {
		return record, fmt.Errorf("transfer requires an explicit bounded scope allowlist")
	}
	for _, finding := range latestGate.Findings {
		if finding.Gate == "scope" {
			return record, fmt.Errorf("scope gate is unresolved: %s", finding.Message)
		}
	}
	if !hasConfiguredTests(opts) {
		return record, fmt.Errorf("transfer requires a focused test command")
	}
	if strings.TrimSpace(opts.LintCmd) == "" {
		return record, fmt.Errorf("transfer requires a lint command")
	}
	testOpts := opts
	testOpts.Workdir = final.Workdir
	if err := validateConfiguredTestCommands(testOpts); err != nil {
		return record, err
	}
	if err := validateTestCommand(final.Workdir, opts.LintCmd); err != nil {
		return record, fmt.Errorf("lint preflight: %w", err)
	}
	patch, err := os.ReadFile(final.LatestDiffPath)
	if err != nil {
		return record, fmt.Errorf("read final patch: %w", err)
	}
	if sha256Sum(patch) != final.LatestDiffSHA256 {
		return record, fmt.Errorf("final patch checksum does not match final state")
	}
	currentPatch, err := deterministicDiffPatch(ctx, final.Workdir, final.Baseline, filepath.Join(runDir, "transfer-source.index"))
	if err != nil {
		return record, fmt.Errorf("capture source diff: %w", err)
	}
	if sha256Sum(currentPatch) != final.LatestDiffSHA256 {
		return record, fmt.Errorf("source worktree changed after final review")
	}
	target := strings.TrimSpace(targetOverride)
	if target == "" {
		target, err = inferPrimaryCheckout(ctx, final.Workdir)
		if err != nil {
			return record, err
		}
	}
	target, err = canonicalPath(target, true)
	if err != nil {
		return record, err
	}
	record.Target = target
	if target == final.Workdir {
		return record, fmt.Errorf("source worktree is already the primary checkout")
	}
	sourceCommon, err := gitCommonDirectory(final.Workdir)
	if err != nil {
		return record, err
	}
	targetCommon, err := gitCommonDirectory(target)
	if err != nil || targetCommon != sourceCommon {
		return record, fmt.Errorf("target does not share the source Git common directory")
	}
	targetHead, err := ensureGitRepo(target)
	if err != nil || targetHead != final.Baseline {
		return record, fmt.Errorf("target HEAD does not match run baseline")
	}
	status, statusErr := runGitCommandBytes(ctx, target, []string{"LC_ALL=C"}, "status", "--porcelain=v1", "--untracked-files=all")
	if statusErr != nil || len(bytes.TrimSpace(status)) != 0 {
		return record, fmt.Errorf("target worktree is not clean")
	}
	record.FocusedTest, err = runConfiguredTestCommands(ctx, testOpts, filepath.Join(runDir, "transfer-test.txt"))
	if err != nil || !record.FocusedTest.Passed {
		return record, fmt.Errorf("focused transfer test failed")
	}
	record.Lint, err = runTestCommand(ctx, final.Workdir, opts.LintCmd, opts.Timeout, filepath.Join(runDir, "transfer-lint.txt"), false, opts.EnvOverlay, opts.MaxOutputBytes, "")
	if err != nil || !record.Lint.Passed {
		return record, fmt.Errorf("transfer lint failed")
	}
	if err = applyPatch(ctx, target, patch, true); err != nil {
		return record, fmt.Errorf("git apply --check: %w", err)
	}
	if err = applyPatch(ctx, target, patch, false); err != nil {
		return record, fmt.Errorf("git apply: %w", err)
	}
	targetPatch, err := deterministicDiffPatch(ctx, target, final.Baseline, filepath.Join(runDir, "transfer-target.index"))
	if err != nil {
		record.RolledBack = rollbackAppliedPatch(ctx, target, patch)
		return record, fmt.Errorf("capture transferred target diff: %w", err)
	}
	record.TargetDiffSHA = sha256Sum(targetPatch)
	if record.TargetDiffSHA != final.LatestDiffSHA256 {
		record.RolledBack = rollbackAppliedPatch(ctx, target, patch)
		return record, fmt.Errorf("target diff checksum differs after transfer")
	}
	record.Status = "transferred"
	return record, nil
}

func rollbackAppliedPatch(ctx context.Context, target string, patch []byte) bool {
	cmd := exec.CommandContext(ctx, "git", "apply", "--reverse", "--whitespace=nowarn")
	cmd.Dir = target
	cmd.Stdin = bytes.NewReader(patch)
	return cmd.Run() == nil
}

func inferPrimaryCheckout(ctx context.Context, workdir string) (string, error) {
	out, err := runGitCommandBytes(ctx, workdir, []string{"LC_ALL=C"}, "worktree", "list", "--porcelain")
	if err != nil {
		return "", err
	}
	candidates := []string{}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "worktree ") {
			continue
		}
		path := strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
		info, statErr := os.Stat(filepath.Join(path, ".git"))
		if statErr == nil && info.IsDir() {
			candidates = append(candidates, path)
		}
	}
	if len(candidates) != 1 {
		return "", fmt.Errorf("primary checkout is ambiguous; pass --to PATH")
	}
	return candidates[0], nil
}

func applyPatch(ctx context.Context, target string, patch []byte, check bool) error {
	args := []string{"apply", "--whitespace=nowarn"}
	if check {
		args = append(args, "--check")
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = target
	cmd.Stdin = bytes.NewReader(patch)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
