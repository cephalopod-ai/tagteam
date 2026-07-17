# Full Repository Audit — 2026-07-16

Audit target: `github.com/cephalopod-ai/tagteam` at
`28cb102d793a12c3607504d0bf57501479c8cdcc` (`main`).

This is a read-only implementation and repository-posture audit. The audit did
not repair production code or change GitHub settings. Documentation was updated
to stop claiming behavior that the audited implementation does not provide.

## Repair status

The findings below preserve the evidence and repository posture observed at the
audited commit. A local repair campaign began on 2026-07-16; its live evidence
and checkpoints are recorded in
[Defect Repair Campaign — 2026-07-16](REPAIR_CAMPAIGN_2026-07-16.md).
AUD-001, AUD-003 through AUD-005, AUD-009, and AUD-012 are implemented and
locally validated on the campaign branch. The remaining findings retain their
original status until their campaign stages pass.

## Executive summary

The repository has strong local path-boundary, durable-write, redaction,
subprocess-argument, and approval-ledger controls. The local Go checks also pass,
including the race-enabled suite. Those controls are undermined by two major
contract gaps:

1. A unix-socket MCP client session closes the daemon's shared runtime when that
   client disconnects. This cancels every registered job and returns before the
   background run goroutines have drained. The current `main` CI has failed on
   both Linux and macOS while the affected test's run was still writing into a
   temporary directory.
2. The public default branch has no branch protection or ruleset. A failing
   cross-platform CI state can therefore coexist with direct changes to the
   release source of truth.

The audit records 12 canonical findings:

| Severity | Count | Findings |
|---|---:|---|
| High | 2 | AUD-001, AUD-002 |
| Medium | 6 | AUD-003 through AUD-008 |
| Low | 4 | AUD-009 through AUD-012 |
| Critical | 0 | — |

No high-confidence secret signatures were found in the tracked tree or its 112
reachable commits. No dependency vulnerability scanner was installed, so this
report does **not** claim that the dependency graph is vulnerability-free.

## Scope and method

The audit covered all 186 tracked files, 91 production Go files, 57 Go test
files, both GitHub Actions workflows, release configuration, repository policy
documents, Git history, current GitHub repository settings, open dependency
updates, and the current/recent `main` CI runs. The Go tree contains 44,602 lines
and 529 test functions.

The private `agent-skills` catalog was used before analysis. Twenty-two relevant
audit skills were applied: orchestration, Go hardening, architecture drift,
dead-code, LLM security, classic security, repository posture, fail-safe
readiness, dependency criticality, recovery/idempotency, operator signal,
reliability, all six dataflow lenses, negative space, architecture seams,
invariant synchronization, and GUI/CLI workflow parity. The language-specific
Python/Node/DB security skill was inspected and rejected as structurally
inapplicable. The repository-posture workflow superseded the narrower posture
triage workflow.

There is no `.architecture/` contract directory. Architecture-drift checks were
therefore run in bootstrap mode using `AGENTS.md`, `docs/ARCHITECTURE.md`, the
control-plane contract, tests, CLI behavior, and release configuration as
evidence. Findings do not rely on inferred architecture alone.

### System and trust map

| Boundary | Higher-authority side | Lower-authority or untrusted side | Enforcement inspected |
|---|---|---|---|
| CLI/config | flags, user config, explicit repo trust | environment, `.env`, untrusted repo config | merge order, sanitization, validation |
| Control plane | host-derived repository/run identity | MCP request and model-provided fields | canonical paths, approval digest/nonces, bounded schemas |
| Orchestration | deterministic runner and scopes | vendor-agent prompts and output | argv construction, output contracts, Git diff/scope gates |
| Persistence | external state root and durable writers | interrupted/partial writes, concurrent readers | atomic writes, state/event ordering, ignored errors |
| Recovery | recorded baseline, run lock, recovery artifacts | retries, crashes, dirty worktree | idempotency, autostash, resume gates |
| Distribution | protected source and release workflow | third-party actions, mutable tags, artifacts | workflow permissions, pinning, checksums, provenance |
| Repository platform | maintainers and required checks | direct pushes, dependency/supply-chain changes | branch rules, scanning, ownership and alert routing |

### Lifecycle traced

The audit traced CLI and MCP entry points through config resolution, preflight,
run registration, agent invocation, diff capture, quality gates, tests, review,
state/event persistence, finalization, cancellation, resume, and release. It also
traced the socket daemon lifecycle across two client sessions and daemon
shutdown.

## Validation evidence

| Check | Result on 2026-07-16 |
|---|---|
| Toolchain | local Go 1.26.5; GitHub CI resolves the `go 1.23` module to Go 1.23.12 |
| `gofmt -l .` | pass; empty output |
| `scripts/check-go-file-lines.sh` | pass |
| `go vet ./...` | pass |
| `go build ./...` | pass |
| `go test ./...` | pass locally |
| `go test -race ./...` | pass locally; `internal/tagteam` 122.430s |
| `go test -cover ./...` | pass; CLI 42.7%, tagteam 72.6%, TUI 62.7% |
| focused socket test, `-count=30` | pass locally |
| focused socket test, `-race -count=10` | pass locally |
| `go mod verify` | pass |
| `go mod tidy -diff` | pass; empty diff |
| Windows package compile checks with `go test -c` | pass for tagteam, CLI, and TUI |
| shell syntax checks | pass for tracked shell/zsh scripts |
| `git fsck --no-dangling` | pass |
| high-confidence current/history secret-signature scan | no matching tracked paths |
| GitHub `main` CI run 29382764126 | **fail**, macOS socket lifecycle test |
| GitHub `main` CI run 29257383105 | **fail**, Linux socket lifecycle test |

The local/CI difference is not treated as a pass. It is evidence of a timing-
sensitive lifecycle defect that clean runners expose.

## Findings

### AUD-001 — Socket clients close the shared runtime and daemon shutdown does not drain jobs

- Severity: **High**
- Confidence: **Confirmed**
- Evidence basis: static lifecycle trace plus observed clean-runner failures
- Primary lenses: concurrency, cascade, temporal, reliability
- Corroborating lenses: orchestration, fail-safe readiness, recovery,
  architecture seams, invariant synchronization, negative space

`MCPStdioServer.Serve` unconditionally defers `runtime.Close()` when a runtime is
attached (`internal/tagteam/mcp_stdio.go:37`). The socket daemon attaches the
same runtime to every accepted session (`internal/tagteam/mcp_daemon.go:66`). A
normal client hangup therefore invokes `ControlRuntime.Close`, which copies and
cancels **all** registered jobs and clears the job map
(`internal/tagteam/control_runtime.go:142`). Starts run asynchronously in an
untracked goroutine (`internal/tagteam/control_runtime.go:301`), and `Close`
does not wait for those goroutines.

The durability test only proves that a fresh client can read a matching run ID;
it does not prove that the run remained live or reached a terminal artifact
(`internal/tagteam/mcp_daemon_test.go:111`). The test can return while the run is
still writing. GitHub runs
[29382764126](https://github.com/cephalopod-ai/tagteam/actions/runs/29382764126)
and
[29257383105](https://github.com/cephalopod-ai/tagteam/actions/runs/29257383105)
then failed on macOS and Linux respectively with `TempDir RemoveAll cleanup` /
`directory not empty` while this test's run was still starting.

Operational impact: disconnecting any socket client can cancel unrelated live
runs owned by other clients. Daemon/test shutdown can return while run code is
still mutating state, producing flaky cleanup, incomplete artifacts, and a
false durable-ownership contract.

Required remediation:

1. Make runtime ownership explicit. Stdio transport may own and close its
   runtime; socket sessions must never close the daemon-owned shared runtime.
2. Have `ServeMCPSocket` close the runtime exactly once during daemon shutdown.
3. Track every start/resume worker in a wait group and make shutdown cancel then
   join them before returning.
4. Keep job registration until the worker actually exits.

Required tests:

- Client A starts a deliberately delayed run, disconnects, and client B proves
  the run is still live and then reaches a terminal state.
- Two clients start independent runs; disconnecting either client cancels
  neither run.
- Daemon cancellation returns only after no worker can write another artifact.
- Repeat and race variants assert final state/effect counts, not only run-ID
  visibility.

### AUD-002 — The public release branch is unprotected while required CI is red

- Severity: **High**
- Confidence: **Confirmed**
- Evidence basis: live GitHub repository configuration and Actions history
- Primary lens: repository posture / branch governance
- Corroborating lenses: reliability, operator signal, release automation

The public repository's default branch is `main`. GitHub reports `Branch not
protected`, and the repository has no rulesets. The current HEAD CI run failed,
as did the preceding `main` run, while the workflow is intended to gate tests on
Linux and macOS (`.github/workflows/ci.yml:3`).

Operational impact: direct or accidental changes can become the release source
of truth without review, required checks, signed/linear history, or the audit's
failing lifecycle test being resolved. Tag pushes can then trigger a publishing
workflow from that state.

Required remediation:

- Protect `main` with pull requests, required successful Linux and macOS checks,
  conversation resolution, no force pushes/deletions, and an explicit policy
  for administrator bypass.
- Add a release/tag ruleset so a tag cannot publish from an unreviewed or red
  source commit.
- Make the exact required-check names stable before enforcing them.

Required tests/checks: attempt a direct push with a test account or dry-run
ruleset evaluation, verify a red PR cannot merge, and verify the documented
release path still succeeds from a green protected commit.

### AUD-003 — Steward configuration is discarded and per-run budgets reset per request

- Severity: **Medium**
- Confidence: **Confirmed**
- Evidence basis: end-to-end config/runtime trace
- Primary lenses: invariant synchronization, architecture seams, LLM security
- Corroborating lenses: workflow parity, operator signal, negative space

`Config` declares a TOML `[steward]` section and defaults
(`internal/tagteam/types.go:642`, `internal/tagteam/config.go:156`). Config files
are decoded and passed into `mergeConfig`, but that function merges defaults,
profiles, adapters, code intelligence, and presets without ever merging
`src.Steward` (`internal/tagteam/config.go:435`). The decoded user/repository
Steward settings are therefore discarded, leaving the model tier disabled.

Independently, each `ControlRuntime.Advise` call constructs a new Steward
(`internal/tagteam/control_steward.go:13`). `BudgetedSteward` stores call count,
dedup signature, and cache only in that newly allocated instance
(`internal/tagteam/steward_model.go:147`). Even if configuration is wired, the
advertised per-run call/dedup budgets reset on every request. Existing tests
exercise `BuildSteward` and `BudgetedSteward` directly, not config loading plus
repeated runtime calls.

Operational impact: documented configuration cannot enable the model tier, and
the intended protection against advisory call amplification is not present on
the real request path. The deterministic advisory path remains functional.

Required remediation:

- Merge and validate trusted Steward settings. Treat endpoint and API-key
  selectors as authority-bearing and strip them from untrusted repo config.
- Cache one budget state per run in `ControlRuntime`, with bounded eviction at a
  terminal state/runtime shutdown.
- Keep the lease and deterministic fallback behavior.

Required tests: load a temporary user TOML and prove its Steward settings reach
`ControlRuntime`; reject/strip an untrusted repo endpoint; call `Advise` past the
cap and within the dedup interval through the real runtime; assert a second run
has an independent budget.

### AUD-004 — Autostash restoration can fail silently or restore the wrong stash

- Severity: **Medium**
- Confidence: **Confirmed**
- Evidence basis: static recovery trace
- Primary lenses: recovery/idempotency, data integrity
- Corroborating lenses: temporal, fail-safe readiness, Go hardening

`gitAutostash` pushes the worktree and returns the positional reference
`stash@{0}` (`internal/tagteam/runner_part07.go:527`). Preflight returns a
no-error cleanup closure whose `git stash pop` result is explicitly discarded
(`internal/tagteam/runner_part06.go:708`). All run modes defer that closure and
cannot incorporate restoration failure into their result.

Operational impact: overlapping agent edits can make `stash pop` conflict while
the run still reports its unrelated outcome. A concurrent external stash can
also move `stash@{0}`, so cleanup may target the wrong entry. Preexisting user
changes can remain in a stash or conflict state without a durable recovery
record.

Required remediation: record the created stash object ID, make cleanup return an
error, merge that error into the named terminal result, and persist an explicit
recovery artifact containing the untouched stash identity and safe operator
commands. Never drop the stash on a partial restore.

Required tests: force a restore conflict, insert another stash between save and
restore, exercise an interrupted run, and assert the original changes remain
recoverable and the terminal result cannot report clean success.

### AUD-005 — Authoritative state and terminal artifact errors are broadly discarded

- Severity: **Medium**
- Confidence: **Confirmed**
- Evidence basis: exhaustive ignored-error scan and state-transition trace
- Primary lenses: state transition, reliability, operator signal
- Corroborating lenses: data integrity, recovery, fail-safe readiness, temporal

Production code contains 23 ignored `writeRunState` results, six ignored
`persistFinal` results, two ignored quality-gate artifact results, and one
ignored direct `persistRunState` result. Representative paths include round
transitions and quality gates (`internal/tagteam/runner_loop_helpers.go:143`),
normal finalization (`internal/tagteam/runner_loop_helpers.go:335`), error
finalization (`internal/tagteam/runner_part02.go:101`), solo/reviewed runs,
resume, recovery, and MCP preflight failure persistence.

`persistRunState` first replaces `state.json` and only then appends
`events.jsonl` (`internal/tagteam/state_machine.go:87`). An event append failure
can therefore leave the snapshot and journal split; most callers discard even
that error. This conflicts with the architecture's use of these artifacts as
the authoritative status/resume surface.

Operational impact: disk-full, permission, filesystem, or durability failures
can leave the TUI/MCP status stale, omit a blocking quality gate, split state
from its event journal, or hide a terminal failure while execution continues or
returns a different result. Some final paths correctly propagate
`persistFinal`, so the defect is broad but not universal.

Required remediation:

- Classify artifacts as mandatory or advisory. Propagate mandatory state,
  quality, recovery, and final errors; explicitly mark optional telemetry as
  degraded.
- Define an atomic/commit protocol for the state snapshot plus event journal, or
  make one canonical and the other rebuildable.
- Centralize terminal finalization so every mode reports persistence failure in
  the same way.

Required tests: inject failures before/after state replacement and event append,
at quality-gate persistence, and during error finalization; assert persisted
state plus CLI/MCP/TUI output agree and never claim success from missing
mandatory evidence.

### AUD-006 — The privileged release workflow trusts mutable action tags and grants write broadly

- Severity: **Medium**
- Confidence: **Confirmed**
- Evidence basis: workflow configuration
- Primary lens: repository posture / workflow supply chain
- Corroborating lenses: classic security, dependency criticality

The release workflow grants `contents: write` at workflow scope
(`.github/workflows/release.yml:8`), so verify jobs receive write authority they
do not need. It invokes checkout, setup-go, and GoReleaser through mutable major
tags, including `goreleaser/goreleaser-action@v7` in the publishing job
(`.github/workflows/release.yml:59`). CI uses the same mutable action-tag
pattern (`.github/workflows/ci.yml:20`).

Operational impact: compromise, retargeting, or unexpected change in a
third-party major tag can execute with repository-write authority during a tag
release. The unnecessarily broad token enlarges the affected surface.

Required remediation: pin every action to a reviewed full commit SHA with a
version comment; set workflow default to `contents: read`; grant
`contents: write` only to the publishing job; consider a protected release
environment and attestations with OIDC where supported.

Required checks: run a dependency-action update PR through both workflows,
inspect effective job permissions, and verify the verify/archive jobs cannot
write repository contents.

### AUD-007 — Security detection and ownership have no complete response chain

- Severity: **Medium**
- Confidence: **Confirmed**
- Evidence basis: live GitHub settings plus repository policy files
- Primary lens: repository posture / alerts and ownership
- Corroborating lenses: classic security, operator signal, negative space

GitHub reports secret scanning, push protection, validity checks, and Dependabot
security updates disabled. Code scanning default setup is `not-configured`.
There are no current open Dependabot alerts, but no code-scanning analysis and
no secret-scanning alert surface exist. The repo has weekly Dependabot version
updates (`.github/dependabot.yml:1`) but no CODEOWNERS file, reviewer routing, or
security labels. `SECURITY.md` tells reporters to contact the maintainer
privately but publishes no usable private channel (`SECURITY.md:5`).

Operational impact: a secret can be pushed without prevention, vulnerable code
has no first-party static gate, and alerts/disclosures have no explicit owner or
private intake path. This is a detection and response weakness, not evidence of
a current secret or vulnerability.

Required remediation: enable secret scanning and push protection; enable CodeQL
for Go and Actions; enable Dependabot security updates; add CODEOWNERS and
reviewer routing; publish GitHub private vulnerability reporting or another
tested private contact in `SECURITY.md`.

Required checks: use harmless platform test patterns to verify push protection,
confirm a CodeQL analysis completes, and submit/read back a private test report
through the documented channel.

### AUD-008 — Release artifacts have checksums but no signed provenance or SBOM

- Severity: **Medium**
- Confidence: **Confirmed**
- Evidence basis: release configuration and latest published asset inventory
- Primary lens: repository posture / artifact provenance
- Corroborating lenses: dependency criticality, classic security

GoReleaser generates a checksum and per-binary hash manifest, and the workflow
smoke-tests four archive targets (`.goreleaser.yaml:31`,
`.github/workflows/release.yml:68`). Those are positive controls. The release
config generates no signature, SLSA-style provenance/attestation, or SBOM. The
latest release assets contain only four archives and `checksums.txt`.

Operational impact: consumers can detect accidental corruption if they obtain
the checksum from a trusted channel, but cannot cryptographically bind an
archive to this repository/workflow or inspect a published component inventory.

Required remediation: generate an SPDX or CycloneDX SBOM per release, publish a
keyless signature and GitHub artifact attestation/provenance, and document
verification. Keep the existing checksums and archive smoke tests.

Required checks: verify a released archive, its SBOM, and provenance from a
clean machine; prove tampering and a mismatched repository identity fail.

### AUD-009 — Unix-socket permission hardening fails open

- Severity: **Low**
- Confidence: **Confirmed**
- Evidence basis: static boundary trace
- Primary lens: classic security
- Corroborating lenses: fail-safe readiness, Go hardening

`ListenMCPUnixSocket` documents owner-only permissions but discards the
`os.Chmod(path, 0o600)` error and returns a usable listener
(`internal/tagteam/mcp_daemon.go:77`). MCP approvals are caller-supplied records,
not a separate authenticated identity, so filesystem access is the principal
local transport boundary.

Operational impact: on a multi-user host, an unusual filesystem/permission
failure can leave a broader-than-intended socket active without warning. The
normal successful `chmod` path remains owner-only.

Required remediation: fail closed on `chmod`, close the listener, remove the
socket when safe, and verify the resulting mode before serving. Test a forced
chmod failure and a restrictive-umask/supported-platform success path.

### AUD-010 — A stale local binary and absolute-path launcher are tracked as source

- Severity: **Low**
- Confidence: **Confirmed**
- Evidence basis: tracked artifact, binary metadata, and Git history
- Primary lenses: dead-code/non-code artifacts, architecture drift
- Corroborating lenses: repository posture, Go hardening

`bin/tagteam` is an 8,154,722-byte tracked Mach-O arm64 executable. Its embedded
metadata identifies a dirty Go 1.26.5 build from commit `9ec431b`, while the
audited source is `28cb102`; it reports version
`0.1.4-local.9ec431b-patched`. The tracked `bin/run-tui.command` changes to the
absolute path `/Users/eric/Documents/team-cli` before executing that binary.
Neither file participates in GoReleaser. Historical binary blobs dominate the
repository's 32.76 MiB pack.

Operational impact: only one developer/architecture can use the launcher; the
binary can appear authoritative despite being stale and dirty; repeated binary
commits inflate clone/history size.

Required remediation: remove both artifacts from the tracked source tree,
ignore local build outputs, and replace the launcher only if needed with a
relative, build-on-demand developer command. History rewriting is optional and
must be a separately approved destructive operation.

Required checks: clean clone on supported architectures, build/launch from
source, and `git status` remains clean after the developer workflow.

### AUD-011 — Eleven internal production functions have no repository references

- Severity: **Low**
- Confidence: **Confirmed**
- Evidence basis: definition/reference scan plus package/build review
- Primary lens: dead-code cleanup
- Corroborating lenses: architecture seams, Go hardening

The following functions occur only at their definitions across all Go files:

`ExportAlexandriaConsumption`, `ReadFinalForCLI`, `codeIntelExcluded`,
`countExisting`, `ensureRunRootIgnore`, `extractFailureIdentities`, `overlayBox`,
`policyDegrades`, `primaryTarget`, `sectionPane`, and `wrapText`.

They are all in `internal/` packages, so they are not a supported external API.
The set includes unused bridge/CLI shims, superseded helpers, and dormant TUI
rendering code.

Operational impact: the dead paths increase review/test surface and make
partially implemented features appear reachable. No runtime failure was tied to
them.

Required remediation: validate each symbol against intended near-term work,
then delete the dead functions and any imports/tests made obsolete in one
focused cleanup. If a bridge is intended, first wire a real caller and behavior
test rather than retaining a placeholder.

Required checks: rerun reference analysis, all Go checks, TUI snapshots, and
bridge integration tests; each removed function must have no dynamic/interface
entry path.

### AUD-012 — Malformed environment configuration is silently ignored

- Severity: **Low**
- Confidence: **Confirmed**
- Evidence basis: config parser comparison
- Primary lenses: input/output path, Go hardening
- Corroborating lenses: reliability, workflow parity, operator signal

`mergeEnvConfig` silently keeps defaults when positive integers, booleans, or
shell-like argument lists fail to parse. Examples include role invocation caps,
rounds, Claude serialization, and adapter argument strings
(`internal/tagteam/config_part02.go:246`, `:268`, `:285`, `:301`). Malformed
OpenAI-compatible header entries are silently dropped
(`internal/tagteam/config_part03.go:671`). Equivalent command-line parsing paths
return errors.

Operational impact: a typo can run with an unintended model argument, round
count, serialization policy, or limit while the operator sees no warning. This
is most significant where the ignored setting was meant to constrain cost or
concurrency.

Required remediation: return field-specific validation errors for present but
invalid `TAGTEAM_*` values, including malformed header pairs; preserve the
current behavior only for absent/empty values. Add CLI/env parity tests for each
typed setting.

## Repository posture matrix

| Category | Status | Evidence / finding |
|---|---|---|
| Secret controls | Fail | scanning/push protection disabled; local signature scan clean; AUD-007 |
| Dependency controls | Partial | `go.sum` verified, weekly PRs and no open alerts; no vulnerability scanner/security updates; AUD-007 |
| Workflow controls | Fail | current `main` CI red; mutable action pins and broad release permission; AUD-001, AUD-006 |
| Automation hygiene | Partial | small explicit workflows and archive smoke; no CODEOWNERS/reviewer routing; AUD-007 |
| Branch governance | Fail | no protection or rulesets; AUD-002 |
| Runner governance | Pass for observed scope | GitHub-hosted runners only; no self-hosted runner surface found |
| Artifact provenance | Partial | checksums and smoke tests present; no signature/SBOM/provenance; AUD-008 |
| Alert ownership | Fail | no CodeQL/secret alert surface and no actionable private disclosure route; AUD-007 |

## Audit-skill coverage and dispositions

The code ranges below are inclusive. A row disposes every check in that range
through the referenced findings, verified non-findings, a stated residual risk,
or structural non-applicability. This compressed table avoids repeating the
same evidence hundreds of times while preserving exhaustive coverage.

| Skill | Checks disposed | Finding disposition | Pass / N/A disposition |
|---|---|---|---|
| Agent orchestration | AOC-001–AOC-030 | AUD-001, AUD-003, AUD-005 | bounded roles/schemas, host authority, approvals, output caps, stop conditions inspected |
| Go repository hardening | GOH-001–GOH-015 | AUD-001, AUD-004, AUD-005, AUD-009–AUD-012 | build tags cross-compiled; vet/format/build/mod checks pass |
| Architecture drift | AID-001–AID-014 | AUD-001, AUD-003, AUD-005, AUD-010, AUD-011 | bootstrap mode; dependency direction and documented package boundaries match |
| Dead-code cleanup | DEAD-001–DEAD-032 | AUD-010, AUD-011 | entry points, packages, build tags, tests, configs, scripts, docs, and non-code artifacts inspected |
| LLM security | LLM-001–LLM-014 | AUD-003 | model output does not directly construct shell commands; bounded observation/output schemas verified |
| Classic security | SEC-001–SEC-015 | AUD-006–AUD-009 | canonical paths, argv execution, redaction, scope gates, and durable approval ledger verified |
| Repository posture | RSP-SCR/DEP/WFL/AUT/BRN/RUN/ART/ALT | AUD-002, AUD-006–AUD-008 | posture matrix above; self-hosted runner surface N/A |
| Fail-safe readiness | FSR-001–FSR-016 | AUD-001, AUD-004, AUD-005, AUD-009 | Unix process-group cancellation, run locks, bounded output, and durable writes verified |
| Dependency criticality | DEP-001–DEP-014 | AUD-006–AUD-008 | direct dependency roles mapped; `go mod verify` passes; vulnerability result not observable |
| Recovery/idempotency | REC-001–REC-012 | AUD-001, AUD-004, AUD-005 | approval/idempotency ledger, resume gates, checkpoints, and run locks inspected |
| Operator signal | SIG-001–SIG-013 | AUD-001, AUD-003, AUD-005, AUD-012 | typed control errors and redacted bounded diagnostics verified |
| Reliability | REL-001–REL-015 | AUD-001, AUD-004, AUD-005, AUD-012 | timeouts, cancellation, retries, locks, durable writes, degraded states inspected |
| Input/output dataflow | IOP-001–IOP-015 | AUD-004, AUD-005, AUD-009, AUD-012 | path escape/symlink gates, caps, redaction, and artifact migration checks pass |
| State transitions | STT-001–STT-012 | AUD-001, AUD-005 | start/resume/cancel state machine and terminal invariants traced |
| Data integrity | DAT-001–DAT-015 | AUD-004, AUD-005 | baseline/diff checksums, scope gates, transfer and migration integrity inspected |
| Concurrency | CON-001–CON-018 | AUD-001 | run/approval locks and concurrent-start idempotency pass; socket ownership fails |
| Cascade | CAS-001–CAS-015 | AUD-001 | shared-runtime cancellation fan-out is the confirmed cascade; fallback chains bounded |
| Temporal | TMP-001–TMP-015 | AUD-001, AUD-004, AUD-005 | stale owner, timeout, watcher, cleanup, and state/event ordering inspected |
| Negative space | NEG-001–NEG-015 | AUD-001, AUD-003–AUD-005 | missing disconnect, config-to-runtime, restore-failure, and persistence-fault tests identified |
| Architecture seams | ARC-001–ARC-025 | AUD-001, AUD-003, AUD-005, AUD-010, AUD-011 | CLI→tagteam and TUI→tagteam dependency direction verified |
| Invariant sync | INV-001–INV-015 | AUD-001, AUD-003, AUD-005 | docs/tests/config/runtime state claims cross-checked end to end |
| GUI/CLI workflow | WFG-001–WFG-015 | AUD-003, AUD-005, AUD-012 | TUI/CLI share `App.Run` and snapshot path; affected status/config parity documented |

### Skill escalation and deduplication

| Finding | Primary skill | Corroborating skills | Canonical resolution |
|---|---|---|---|
| AUD-001 | concurrency | cascade, temporal, reliability, orchestration | one lifecycle/ownership finding, not separate CI-flake findings |
| AUD-003 | invariant sync | LLM security, architecture seams, workflow parity | one end-to-end Steward finding covering config and budget lifetime |
| AUD-004 | recovery/idempotency | integrity, temporal | one restoration finding covering error and stash identity |
| AUD-005 | state transition | operator signal, integrity, reliability | one persistence-policy finding across modes |
| AUD-006–AUD-008 | repository posture | security, dependency criticality | separate workflow, detection/ownership, and artifact-provenance controls |
| AUD-010–AUD-011 | dead-code | architecture drift, Go hardening | separate non-code artifact and source-symbol cleanup findings |

## Verified non-findings and positive controls

- Control-plane repository, run-directory, and allowed-path identities are
  canonicalized and revalidated across start/resume/cancel boundaries.
- Untrusted repo config strips command-bearing adapter args, external bridge
  paths, and named test presets. Steward authority-bearing settings must receive
  the same treatment when AUD-003 is repaired.
- Subprocesses are constructed with argument arrays. Shell execution is limited
  to explicitly configured test/lint/provider commands and is not synthesized
  from model output.
- Output capture is bounded and persisted artifacts use overlay-aware secret
  redaction.
- Durable JSON/byte writers use temporary files, sync, rename, and directory
  sync; AUD-005 concerns callers discarding their errors and state/event
  transaction semantics, not the atomic-file primitive.
- Artifact migration checks checksums and refuses symlinked boundaries.
- Unix cancellation targets process groups with TERM then KILL. Windows code
  compiled, but Windows is not a published release target and was not runtime
  tested.
- No generic MCP shell tool or unrestricted artifact reader is exposed.
- No tracked high-confidence access-key, private-key, GitHub token, OpenAI key,
  or Slack-token signature was found in current or historical text blobs.

## Residual risk and limitations

- `govulncheck`, `staticcheck`, `golangci-lint`, `gosec`, `gitleaks`, `syft`,
  `grype`, `trivy`, `osv-scanner`, and `deadcode` were unavailable. Equivalent
  built-in/reference checks were run where possible; dependency vulnerability
  and full semantic dead-code conclusions remain limited.
- Vendor-backed agent execution and authentication were not exercised. Tests use
  fake adapters/local HTTP fixtures, so third-party CLI behavior remains a
  documented external dependency.
- Destructive disk-full, permission-loss, process-kill, and stash-conflict fault
  injection was not performed in the user's worktree. The relevant findings are
  confirmed by reachable error-discard paths; their regression tests remain to
  be implemented in isolated fixtures.
- GitHub settings are a point-in-time observation from 2026-07-16 and can change
  independently of this repository.
- No self-hosted runner, cloud deployment, or authenticated remote MCP surface
  was found. Those checks are N/A until such a surface exists.

## Remediation order

1. **P0 — runtime truth:** repair AUD-001 and make its clean, repeated, and race
   tests required before restoring a green branch.
2. **P0 — source-of-truth governance:** implement AUD-002 after stable check
   names exist.
3. **P1 — persistence and recovery:** repair AUD-005 and AUD-004 with injected
   failure tests.
4. **P1 — release and security chain:** repair AUD-006, AUD-007, and AUD-008.
5. **P2 — advertised capability truth:** repair AUD-003 before documenting or
   enabling model Steward use.
6. **P2 — fail-closed/local hygiene:** repair AUD-009 through AUD-012 in focused
   changes, without bundling history rewriting.

The actionable backlog is mirrored in [TODO.md](TODO.md). Test evidence and
known gaps are mirrored in [TEST_LEDGER.md](TEST_LEDGER.md).
