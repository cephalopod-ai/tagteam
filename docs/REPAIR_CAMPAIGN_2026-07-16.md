# Defect Repair Campaign — 2026-07-16

Source audit: [Full Repository Audit — 2026-07-16](AUDIT_REPORT_2026-07-16.md)

Campaign branch: `repair/audit-2026-07-16`

Baseline: `b965ecc` (`main`), local Go checks green; current GitHub `main` CI
red in the MCP socket lifecycle test documented by AUD-001. This campaign uses
local stage commits and does not push or rewrite history.

## Gate 0 — orientation and safety

- Canonical contract: root `AGENTS.md`; development guidance:
  `CONTRIBUTING.md`.
- Initial worktree: clean. The audit report and documentation corrections were
  already committed as `b965ecc`.
- Validation inventory: `gofmt -l .`, Go file line gate, targeted Go tests,
  `go test ./...`, `go test -race ./...`, `go vet ./...`, `go build ./...`,
  `go mod verify`, workflow syntax/static checks, and live GitHub readback for
  platform settings.
- File-size rule: the repository enforces 800 lines per Go file. No source file
  enters the campaign skill's 1001–1999-line opportunistic modularization band;
  all repairs patch in place.
- Deferred items outside AUD-001–AUD-012 remain protected and out of scope.

## Gate 1 — verified defect inventory

| ID | Domain | Priority | Complexity | Required touch set | Minimum proof |
|---|---|---|---|---|---|
| AUD-001 | reliability/concurrency | P0 | high | `mcp_stdio.go`, `mcp_daemon.go`, `control_runtime.go`, CLI wiring and MCP tests; socket session→runtime→run-worker lifecycle | disconnect does not cancel daemon jobs; shutdown joins workers; repeated race test |
| AUD-002 | build/CI governance | P0 | medium | GitHub branch/ruleset settings and required check identities | live settings readback; direct/red merge path denied |
| AUD-003 | correctness/config | P1 | high | `config.go`, `steward_model.go`, `control_steward.go`, runtime state and config/Steward tests; TOML→runtime→per-run budget path | trusted config reaches model; untrusted authority stripped; repeated calls share budget |
| AUD-004 | data integrity/recovery | P0 | medium | preflight cleanup contract, run-mode callers, Git stash identity, recovery artifact and tests | conflict/wrong-index scenarios surface error and preserve original stash |
| AUD-005 | data integrity/reliability | P0 | high | state/event persistence, runner/review/resume/control terminal paths and fault tests | mandatory write faults reach caller; snapshot/journal policy stays truthful |
| AUD-006 | security/build | P1 | medium | CI/release workflows and action pins/permissions | immutable pins; only publisher has write authority |
| AUD-007 | security/operations | P1 | medium | GitHub scanning/security settings, CODEOWNERS, `SECURITY.md` | live readback and usable private-reporting path |
| AUD-008 | release security | P1 | medium | release workflow, GoReleaser config, SBOM/signature/provenance verification docs | config validation plus clean release/snapshot dry run where possible |
| AUD-009 | local security | P2 | low | unix-listener permission setup and socket tests | forced chmod failure returns no live listener/socket |
| AUD-010 | repository hygiene | P3 | low | tracked `bin/` artifacts and `.gitignore` | artifacts absent; clean source build/launch path remains |
| AUD-011 | maintainability/correctness | P3 | low | 11 repository-unreferenced internal functions across tagteam/TUI | references absent; full compile/tests pass after deletion |
| AUD-012 | config correctness | P2 | medium | `mergeEnvConfig`, header parsing, config call sites/tests | every present malformed typed env value returns a field-specific error |

All findings are in scope. The user explicitly reopened the entire audited
backlog, including repository hardening and cleanup findings. No candidate was
reclassified as feature work or intentionally deferred.

## Gate 2 — locality grouping and campaign order

### Stage 1 — control runtime and configuration boundaries

- Defects: AUD-001 (P0/high), AUD-003 (P1/high), AUD-009 (P2/low), AUD-012
  (P2/medium).
- Shared surfaces: `ControlRuntime`, MCP transport ownership, config loading,
  Steward construction, and operator-facing control tests.
- Data paths: socket session lifecycle; daemon-owned worker lifecycle;
  `[steward]` TOML to runtime; per-run advisory budgets; typed environment
  parsing; socket filesystem permissions.
- Modularization: none; every touched file is at or below 800 lines.
- Regression surface: MCP stdio/socket, control runtime, config layering,
  Steward model/fallback, CLI MCP wiring.
- Commit: `fix: harden control runtime and config boundaries`.

### Stage 2 — persistence and recovery truth

- Defects: AUD-004 (P0/medium), AUD-005 (P0/high).
- Shared surfaces: preflight/run cleanup, state transitions, mandatory run
  artifacts, error finalization, resume/recovery/control persistence.
- Data paths: dirty worktree→stash→restore; phase transition→state snapshot→
  event journal; quality gate/final artifact→CLI/MCP/TUI status.
- Modularization: none; every touched file is at or below 800 lines.
- Regression surface: runner modes, review, resume, recovery, state machine,
  control terminal errors, injected write failures.
- Commit: `fix: make recovery and run persistence fail closed`.

### Stage 3 — tracked artifact and dead-code cleanup

- Defects: AUD-010 (P3/low), AUD-011 (P3/low).
- Shared surfaces: unreachable source/non-source artifacts and repository build
  hygiene.
- Data paths: source checkout→developer build; internal call graph.
- Modularization: none.
- Regression surface: full build/tests, TUI snapshots, bridge/config packages,
  reference scan, clean build output behavior.
- Commit: `chore: remove stale artifacts and dead internal code`.

### Stage 4 — supply chain and GitHub governance

- Defects: AUD-002 (P0/medium), AUD-006 (P1/medium), AUD-007 (P1/medium),
  AUD-008 (P1/medium).
- Shared surfaces: `.github`, GoReleaser, release assets, GitHub repository
  security/governance settings, ownership and disclosure.
- Data paths: reviewed source→CI→tag release→published artifact→consumer;
  secret/vulnerability signal→owner/private response.
- Modularization: not applicable.
- Regression surface: action syntax and immutable refs, effective permissions,
  GoReleaser validation, release snapshot, live settings readback.
- Ordering constraint: apply branch and release governance only after Stages
  1–3 and the repaired CI-equivalent checks are green.
- Commit: `ci: harden release provenance and repository policy`.

## Stage results

Results, adversarial dispositions, validation evidence, and local commit hashes
are appended here at each stage gate.

### Stage 1 — control runtime and configuration boundaries

Status: implemented and locally validated. Checkpoint: `84c07e3`
(`fix: harden control runtime and config boundaries`).

- AUD-001: stdio explicitly owns its process-scoped runtime; socket sessions
  borrow a daemon-owned runtime. Daemon shutdown closes once, cancels jobs, and
  joins tracked workers. Start and shutdown are lifecycle-serialized so an
  approval cannot be consumed after closure begins.
- AUD-003: trusted `[steward]` TOML is merged and validated; untrusted repository
  Steward authority is stripped. One budget wrapper is cached per run, bounded
  to 1,024 entries with deterministic fallback at capacity.
- AUD-009: unix socket creation fails closed when mode `0600` cannot be set or
  verified, closing the listener and removing its path.
- AUD-012: malformed boolean, integer, shell-argument, context-budget, and
  header-pair environment values now return field-specific load errors.
- Repository line gate: the added runtime lifecycle seam was moved to
  `control_runtime_lifecycle.go`; `control_runtime.go` is 747 lines.

Validation:

- Focused MCP/config/Steward suite: pass.
- Socket/runtime stress test (`-count=30`): pass in 19.974s.
- Focused race stress (`-race -count=10`): pass in 9.913s.
- `go test ./...`: pass; `internal/tagteam` completed in 117.361s.
- `go vet ./...`, `go build ./...`, `go mod verify`, formatting, Go file line
  gate, and `git diff --check`: pass.

Adversarial review disposition: the first pass found two repair-introduced
risks—an 826-line source file and an unbounded per-runtime Steward map. Both
were corrected and regression-tested. No unresolved Stage 1 blocker remains.
Abrupt process death still cannot execute an in-process join; persisted recovery
continues to cover that separate restart boundary.

### Stage 2 — persistence and recovery truth

Status: implemented and locally validated. Checkpoint: `86763e8`
(`fix: make recovery and run persistence fail closed`).

- AUD-004: autostash creation uses a unique message and resolves the created
  immutable object ID rather than `stash@{0}`. Cleanup applies that object even
  when another stash is pushed later. Restore conflicts fail the run and write
  `autostash-recovery.json` with the untouched object identity and safe recovery
  commands. Successful apply deliberately retains the stash recovery point
  because Git only drops by a movable positional selector.
- AUD-005: ignored mandatory state, quality-gate, recovery, resume, and terminal
  writes now propagate. `events.jsonl` is explicitly rebuildable and is fsynced
  before the atomic, canonical `state.json` replacement. Shared terminal
  persistence rewrites attempted success to `persistence_failed`, retries the
  failed projection, and always returns the original write error.
- MCP resume workers now use the same tracked runtime worker path as starts;
  shutdown joins them, and resume rejects new work after runtime closure.
- Asynchronous MCP start/resume terminal-persistence errors are retained in the
  runtime and surfaced through status when no readable terminal state exists.

Validation:

- Autostash/persistence fault suite: pass, including newer-stash, conflict,
  interruption, journal-before-snapshot, terminal retry, error-finalization,
  and quality-gate write failures.
- Focused stress (`-count=10`): pass in 13.133s.
- Focused race stress (`-race -count=5`): pass in 12.445s.
- Broad run/review/resume/recovery suite: pass in 67.102s.
- `go test ./...`: pass; `internal/tagteam` completed in 174.653s.
- `go vet ./...`, `go build ./...`, `go mod verify`, formatting, Go file line
  gate, and `git diff --check`: pass.

Adversarial review disposition: resolving `refs/stash` after `stash push` still
allowed a newer external stash to steal the identity, so creation now searches
for a cryptographically unique stash message. Dropping by a previously resolved
`stash@{n}` remained position-racy; successful restores therefore retain the
immutable recovery point instead of risking deletion of unrelated user work.
The first terminal helper also failed to replace an existing running MCP resume
diagnostic when `state.json` was removed; the missing-state recovery case was
restored and regression-tested. No unresolved Stage 2 blocker remains.

### Stage 3 — tracked artifact and dead-code cleanup

Status: implemented and locally validated. Checkpoint: `cbe9df6`
(`chore: remove stale artifacts and dead internal code`).

- AUD-010: removed the tracked 8.2 MB Mach-O development binary and the
  workstation-specific launcher containing an absolute checkout path. Root
  `bin/` is ignored so local build output cannot be recommitted accidentally.
  Repository history was not rewritten.
- AUD-011: a repository-wide exact-reference scan confirmed that all 11 audited
  functions had definitions but no callers. They were deleted with their
  orphaned Alexandria consumption-event type; the README now describes only
  the Alexandria observation envelope that remains implemented.

Validation:

- Exact production/reference scan: no audited symbol remains outside the
  historical audit and campaign records.
- `go test ./internal/tagteam ./internal/tui`: pass; `internal/tagteam`
  completed in 174.757s and `internal/tui` in 0.826s.
- A source-only native build launches `--help`; Linux amd64 and Windows amd64
  cross-builds also pass with output written outside the repository.
- `git ls-files bin` is empty in the resulting checkpoint.

Adversarial review disposition: the original README still advertised the dead
Alexandria consumption-event path after its implementation was removed. That
claim and its now-orphaned event type were removed. No import, build-tag, test,
CLI, TUI, or documentation caller was found for any other deleted symbol. No
unresolved Stage 3 blocker remains.

### Stage 4 — supply chain and GitHub governance

Status: implemented; local configuration and live GitHub policy are validated.
Checkpoint: `b48b224`
(`ci: harden release provenance and repository policy`).

- AUD-002: active repository ruleset `19084807` protects the default branch
  with pull requests, one code-owner approval, stale-review dismissal,
  last-push approval, resolved review threads, strict Linux/macOS checks from
  GitHub Actions, and deletion/force-push prevention. The sole maintainer's
  bypass is `pull_request` only, so it cannot authorize a direct push. Separate
  active rulesets restrict `v*` tag creation to that maintainer (`19084809`)
  and permit no bypass for tag update, deletion, or force-push rules
  (`19084810`).
- AUD-006: every third-party action is pinned to a reviewed 40-character commit
  SHA with its release version in a comment. Workflow permissions default to
  `contents: read`; only `Publish signed release` receives `contents: write`,
  `id-token: write`, and `attestations: write`. GoReleaser, Syft, and Cosign
  tool versions are pinned as well.
- AUD-007: secret scanning, push protection, Dependabot alerts/security
  updates, private vulnerability reporting, and managed CodeQL default setup
  are enabled. `.github/CODEOWNERS` routes all changes to the sole current
  maintainer, and `SECURITY.md` links directly to the private advisory form.
- AUD-008: GoReleaser produces one SPDX JSON SBOM per archive and keylessly
  signs both `checksums.txt` and every SBOM with Sigstore bundles. The publish
  job creates repository-bound GitHub attestations for checksummed archives and
  SBOMs. Archive smoke tests remain and the obsolete `macos-13` x86 runner was
  replaced by `macos-15-intel`. Consumer verification is documented in
  `docs/RELEASE_SECURITY.md`.

Validation:

- `actionlint` v1.7.12: pass for all workflows; Ruby YAML parsing and mutable
  `uses: ...@vN` scan: pass.
- GoReleaser v2.17.0 `check`: pass.
- GoReleaser snapshot with Syft v1.48.0: pass in 2m26s, producing four archives
  and four SPDX JSON SBOMs after the full Go test/vet hooks. Publishing and
  signing were skipped because GitHub OIDC exists only in the tag workflow.
- Final `go test ./...`: pass; `internal/tagteam` completed in 122.760s. Final
  `go test -race ./...`: pass; `internal/tagteam` completed in 123.664s.
- Final coverage: CLI 42.7%, tagteam 73.0%, and TUI 64.4%. `go vet ./...`,
  `go build ./...`, `go mod verify`, `go mod tidy -diff`, formatting, shell
  syntax, Go file line gate, and `git diff --check`: pass.
- Live readback: default branch reports protected; effective rules include PR,
  strict status checks, deletion protection, and non-fast-forward protection;
  all three rulesets are active with the intended bypass modes. Secret scanning
  and push protection are enabled, Dependabot automated security fixes are
  enabled and unpaused, private reporting is enabled, and CodeQL default setup
  is configured for weekly `actions` and Go analysis with the default query
  suite. Initial setup run `29554541644` passed both analyzers.

Adversarial review disposition: immutable action pins alone still left mutable
GoReleaser/Syft/Cosign downloads, so the tool versions were pinned. A first
workflow lint found that GitHub had retired the existing `macos-13` label; the
replacement preserves the Darwin amd64 smoke path. The release rehearsal was
intended for a temporary copy but initially ran in the primary checkout and
replaced ignored `dist/` output. The pre-existing `0.1.0-SNAPSHOT-5263ae2`
artifact set was regenerated from commit `5263ae2` and restored; tracked files
were unaffected. A real tag release has not been created, so OIDC signature,
attestation publication, and consumer verification remain operationally
unproven until the next release even though the supported configuration and
non-OIDC release pipeline validate.

## Campaign disposition

All AUD-001–AUD-012 repair items are implemented and their TODO entries are
closed. The four locality checkpoints are `84c07e3`, `86763e8`, `cbe9df6`, and
`b48b224`. GitHub policy/security settings are live; source and workflow changes
remain local on `repair/audit-2026-07-16` and have not been pushed by this
campaign. No history was rewritten and no deferred non-audit TODO was changed.

The only outstanding operational evidence is a real post-repair tag release.
That event must prove GitHub OIDC keyless signing, attestation upload, release
asset publication, and the documented consumer verification commands. This is
an explicit runtime-validation boundary, not an omitted repository repair.
