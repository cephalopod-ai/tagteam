# TODO

## Full repository audit follow-ups (2026-07-16)

Source: [Full Repository Audit — 2026-07-16](AUDIT_REPORT_2026-07-16.md).
Keep finding IDs on implementation PRs and add the required negative/fault tests
before closing an item.

### P0 — runtime and source-of-truth integrity

- [x] **AUD-001:** separate stdio/session ownership from daemon ownership of
  `ControlRuntime`; close the socket runtime once, cancel and join all run
  workers on daemon shutdown, and prove client disconnect does not cancel any
  daemon-owned run with repeated and race-enabled tests.
- [ ] **AUD-002:** protect `main` and release tags with pull-request review,
  stable required Linux/macOS checks, conversation resolution, force-push/delete
  restrictions, and an explicit administrator-bypass policy.

### P1 — persistence, recovery, and repository security

- [x] **AUD-005:** classify mandatory versus advisory artifacts, propagate all
  mandatory state/quality/final persistence failures, and define a consistent
  state-snapshot/event-journal commit protocol with fault-injection tests.
- [x] **AUD-004:** restore autostashes by recorded object identity, surface
  restore conflicts in the terminal result, and persist a recovery artifact
  that leaves the original user changes recoverable.
- [ ] **AUD-006:** pin GitHub Actions to reviewed commit SHAs and scope
  `contents: write` to the publishing job only; consider a protected release
  environment.
- [ ] **AUD-007:** enable secret scanning/push protection, CodeQL, and
  Dependabot security updates; add CODEOWNERS/reviewer routing and a tested
  private vulnerability-reporting channel.
- [ ] **AUD-008:** publish an SBOM, keyless signature, and repository-bound
  provenance/attestation for each release while retaining checksums and archive
  smoke tests.

### P2 — capability truth and cleanup

- [x] **AUD-003:** merge and validate trusted `[steward]` config, strip its
  endpoint/key selectors from untrusted repo config, and retain one bounded
  Steward budget state per run with end-to-end tests.
- [x] **AUD-009:** fail closed if the unix socket cannot be set and verified as
  owner-only; close and safely remove the listener on permission failure.
- [ ] **AUD-010:** remove the stale tracked `bin/tagteam` and absolute-path TUI
  launcher in a focused change, ignore local build outputs, and do not rewrite
  history without separate explicit approval.
- [ ] **AUD-011:** validate and remove the 11 repository-unreferenced internal
  functions identified by the audit; wire a real caller and behavior test only
  where the feature is intentionally retained.
- [x] **AUD-012:** reject present-but-malformed typed `TAGTEAM_*` values and
  header pairs with field-specific errors; add CLI/environment parity tests.

## Code-intelligence relay recovery

- [x] Rerun the quarantined full-phase code-intelligence work as a fresh,
  checkpointed `--allow-dirty` continuation after the output-cap repair. Run
  `2026-07-12T085150.470640000Z` passed its two completed relay rounds.
- [ ] Add an integration test that drives a relay editor above the default
  2 MiB output size while `--max-output-bytes` is higher, proving the CLI
  value reaches the editor request.
- [ ] Harden recovery-decision parsing for Claude envelope output so a valid
  embedded decision can continue with the configured fallback rather than
  unnecessarily quarantining an otherwise verified patch.
- [ ] Design an explicit, operator-approved retry path for a quarantined
  recovery decision. Preserve the current idempotency guard unless the retry
  records a new recovery attempt and its relationship to the original.
- [ ] Add a contract-only repair path for a worker result whose repository
  edits are complete but whose `files_changed` claim includes a gitignored,
  repo-required local log. The repair must not permit further edits or include
  ignored contents in review artifacts.
- [ ] Extend baseline-test integrity snapshots to explicitly governed ignored
  paths when a repository declares mutable runtime state outside Git-visible
  files. Git-visible baseline mutations now fail closed.

## Deferred: MCP control plane and optional Run Steward

**Status:** The producer contract, local MCP stdio/socket transports, approved
idempotent start/resume/cancel, non-mutating resume assessment, and deterministic
or optional model Run Steward are implemented. Socket clients borrow one
daemon-owned runtime; shutdown drains tracked workers. Trusted Steward config
is merged with a bounded per-run budget, while untrusted repository authority
is stripped. Capability provenance is implemented; remote auth and fleet
summaries remain future work.

The goal is to let any MCP-capable host launch and monitor Tagteam without
turning model output into shell commands. A deterministic controller remains
the only execution authority. An optional Run Steward model may summarize
normalized evidence and recommend actions, but it is advisory and cannot edit
the repository, broaden scope, change roles, dismiss findings, or approve its
own recovery action.

### Immediate implementation horizon

- [x] Define a versioned machine contract for launch specifications, run
  handles, status snapshots, plans, findings, diagnostics, cancellation, and
  resumability. Reuse the existing JSON artifacts and CLI reason/exit codes
  rather than creating a second state model.
- [x] Add the local MCP resume operation. It verifies the approval-bound
  action and worktree, reuses `PrepareResume`, persists single-use nonce
  consumption, and invokes the host-owned `App.Resume` path.
- [x] Persist a terminal diagnostic when a nonce-consumed MCP resume fails
  before the normal resume path can write `final.json`. Path-gate failures
  remain fail-closed and never write through an escaped run directory.
- [x] Add the local MCP cancel operation with deterministic host-owned process
  ownership after server restart. Live runs must be owned by the cancelling
  MCP runtime; stale owners use the durable cancellation request and persisted
  cancelled status.
  Start returns a durable handle promptly; status and findings are bounded,
  paginated where necessary, and explicit about truncation.
- [x] Keep command construction deterministic: no generic command tool, raw
  shell, arbitrary flag passthrough, unrestricted artifact reader, or
  model-controlled working directory. Canonicalize the repository root and
  allowed paths before execution.
- [x] Add bounded scout evidence for symlink topology where it helps explain
  scope, while keeping canonical real-path resolution and enforcement in the
  host controller.
- [x] Bind start, resume, and cancel approvals to the normalized action,
  repository identity, selected roles, scope, run identity, and an expiry or
  nonce. A changed action requires a new approval. Approval nonces are now
  single-use across every action: start and resume reject a nonce already
  consumed by any start, resume, or cancel (matching the cancel path), closing
  the cross-action replay gap. Regression tests cover every cross-action pair
  and prove the start digest binds roles, scope, prompt, rounds, and budget.
- [x] Add an optional local-first Run Steward interface. Feed it only bounded,
  sanitized `RunObservation` records and require a schema-validated advisory
  result with an enum action such as wait, inspect, notify, prepare-resume,
  ask-user, or report-issue. Implemented in `steward.go`; projected from the
  authoritative `RunSnapshot` (status/phase/reason codes/counts only — no
  prompts, diffs, file paths, or model reasoning) and surfaced advisory-only
  through the read-only `tagteam_advise` MCP tool.
- [x] Make deterministic status and error templates the final fallback. A
  missing, invalid, slow, or rate-limited steward must never delay or alter the
  Tagteam run. `DeterministicSteward` maps every status to a safe action;
  `AdviseWithFallback` runs any model steward under a strict timeout and falls
  back to the template on error, timeout, or schema-invalid output, never
  blocking the run.
- [x] Default the steward tier to a separately configured local Ollama model.
  Cloud or CLI-backed stewards are optional escalation targets and must run at
  lower priority with call, token, timeout, and deduplication budgets so they
  do not contend with worker or reviewer invocations. `[steward]` config
  defaults to a disabled local OpenAI-compatible (Ollama) endpoint. Trusted
  config is merged and validated, untrusted repository authority is stripped,
  and one bounded budget state is retained per run.
- [x] Prevent recursion and duplicate observers: one steward lease per run,
  no Tagteam invocation from the steward, and no inherited arbitrary MCP or
  repository-write tools. `ModelSteward` sends a text-only chat request with no
  tool/function surface (recursion prevented by construction); a per-run
  `steward.lease` enforces a single observer.
- [~] Add contract, process-lifecycle, hostile-output, approval-replay,
  concurrency, cancellation, restart, malformed-JSON, and weak-model
  playtests. Verify that an MCP host can recover from every returned error
  without reading source code. Start now returns typed `ControlStartError`
  reason codes like resume and cancel, so every MCP lifecycle failure surfaces
  a stable structured `code`/`recoverable` pair over stdio. New playtests cover
  malformed persisted JSON (ledger and run artifacts), concurrent start
  requests, hostile run identities at the lifecycle entry points, weak/failed
  adapter terminal records, and equivalence of the normalized launch and
  terminal records between the direct CLI and MCP paths. Socket tests now assert
  that client disconnect does not cancel daemon work and that daemon shutdown
  waits for tracked workers; focused repeated and race-enabled runs pass locally.

### Future vision

- [~] Separate durable process ownership into a local `tagteamd`-style service
  with the MCP endpoint as a thin client transport. Add leases, reconnectable
  event streams, multi-client arbitration, and safe cancellation after the
  originating host exits. **Partially implemented:** `mcp_daemon.go` +
  `tagteam mcp --socket <path>` accept multiple clients over one shared
  `ControlRuntime`. Session/daemon ownership and drained shutdown are
  implemented. **Remaining:** reconnectable push event streams and formal
  multi-client arbitration beyond serialization.
- [x] Add capability/version provenance and quarantine when tool schemas,
  side effects, or the Tagteam binary change outside the approved baseline.
  `provenance.go` fingerprints the producer version, contract version, and MCP
  tool-schema digest. The first lifecycle mutation records the approved baseline
  (trust on first use); a later start, resume, or cancel whose surface differs —
  a new binary or changed/added tool schema — fails closed with the typed
  `capability_quarantined` error until an operator removes the baseline file.
- [ ] Support authenticated remote deployment only after local stdio behavior,
  workload identity, repository authorization, credential scope, and audit
  logging are proven.
- [ ] Provide fleet-level run summaries and notifications without centralizing
  repository contents, raw prompts, secrets, or private model reasoning.

### Acceptance boundary

- [x] The MCP path and direct CLI path produce equivalent normalized launch and
  terminal records for the same inputs.
- A low-capability steward can explain progress and request help, but cannot
  gain execution authority through prompt text or tool output.
- [ ] Killing or disconnecting an attached MCP host leaves either a cleanly
  cancelled run or an explicitly recoverable persisted state; no run is
  reported as successful from missing or malformed evidence. Graceful
  disconnect/shutdown and mandatory terminal persistence are repaired; an
  abrupt process-kill playtest remains open.
- Gosling and other hosts consume this contract rather than duplicating
  Tagteam profiles, model catalogs, reason codes, or recovery rules.
