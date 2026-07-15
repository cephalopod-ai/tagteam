# TODO

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

**Status:** Producer contract, local MCP transport, approved idempotent start,
resume, and cancel, and non-mutating resume assessment are implemented. The
remaining lifecycle work is bounded ownership and transport hardening.

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
- [x] Bind start approvals to the normalized launch, including repository
  identity, selected roles, scope, budgets, and test preset; bind resume and
  cancel approvals to the canonical repository and immutable run identity.
  All lifecycle approvals require an expiry and single-use nonce. A changed
  action requires a new approval.
- [ ] Add an optional local-first Run Steward interface. Feed it only bounded,
  sanitized `RunObservation` records and require a schema-validated advisory
  result with an enum action such as wait, inspect, notify, prepare-resume,
  ask-user, or report-issue.
- [ ] Make deterministic status and error templates the final fallback. A
  missing, invalid, slow, or rate-limited steward must never delay or alter the
  Tagteam run.
- [ ] Default the steward tier to a separately configured local Ollama model.
  Cloud or CLI-backed stewards are optional escalation targets and must run at
  lower priority with call, token, timeout, and deduplication budgets so they
  do not contend with worker or reviewer invocations.
- [ ] Prevent recursion and duplicate observers: one steward lease per run,
  no Tagteam invocation from the steward, and no inherited arbitrary MCP or
  repository-write tools.
- [x] Add contract, process-lifecycle, hostile-output, approval-replay,
  concurrency, cancellation, restart, malformed-JSON, and weak-model
  playtests. MCP failures now carry `code`, bounded `reason`, and
  `recoverable`; direct-run and MCP terminal records are compared for the same
  normalized start input.

### Future vision

- [ ] Separate durable process ownership into a local `tagteamd`-style service
  with the MCP endpoint as a thin client transport. Add leases, reconnectable
  event streams, multi-client arbitration, and safe cancellation after the
  originating host exits.
- [ ] Add capability/version provenance and quarantine when tool schemas,
  side effects, or the Tagteam binary change outside the approved baseline.
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
- Killing an attached MCP host leaves either a cleanly cancelled run or an
  explicitly recoverable persisted state; no run is reported as successful
  from missing or malformed evidence.
- Gosling and other hosts consume this contract rather than duplicating
  Tagteam profiles, model catalogs, reason codes, or recovery rules.
