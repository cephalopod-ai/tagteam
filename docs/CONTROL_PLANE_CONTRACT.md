# Control Plane Contract

**Status:** draft producer contract, local MCP stdio transport, approved
idempotent start, resume, and cancel, and non-mutating resume assessment are
implemented.

Tagteam owns a versioned control-plane contract in
`internal/tagteam/control_contract.go`. It is the anti-corruption boundary for
future MCP hosts such as Gosling. It projects the existing Tagteam artifact and
state model rather than introducing a second run state machine.

## Implemented producer operations

- `Capabilities` returns the contract and producer version plus only operations
  that have real handlers.
- `NormalizeControlLaunch` validates and canonicalizes a proposed launch.
- `ControlActionDigest` binds the normalized launch, repository identity,
  mode-specific roles, write scope, budgets, test preset, and recovery policy.
- Dedicated start, resume, and cancel digest constructors additionally bind the
  operation, idempotency key, or run identity so approvals cannot be replayed
  across actions.
- `PrepareControlStart` returns the start-specific digest and maximum approval
  lifetime, so MCP clients never have to reconstruct approval hashing.
- `PrepareResume` validates the exact persisted state, artifact integrity,
  baseline, and deterministic worktree diff without changing repository or run
  artifacts. It reports a bounded reason code instead of quarantining a run.
- `Status` projects the authoritative `RunSnapshot` assembled from existing
  run artifacts and supplies a stable snapshot digest.
- `Plan` and `Findings` return bounded cursor pages from `plan.json` and
  `findings.json`.
- `Diagnostics` verifies repository identity and resolves the state root
  without creating runtime state.
- `tagteam_prepare_start` exposes that validation to MCP clients without
  creating runtime state.
- `Resume` verifies deterministic preconditions, consumes a matching
  short-lived approval nonce, persists the nonce under the resolved state root,
  and invokes the existing host-owned `App.Resume` path. Exact retries return
  the persisted run handle; a nonce cannot be reused for another action.
- `Start` reserves a durable run ID, consumes a matching short-lived approval
  nonce, launches Tagteam through its normal configuration and runner, and
  persists a terminal artifact if preflight fails before the runner can do so.
  Every start failure surfaces a typed `ControlStartError` with a stable reason
  code and `recoverable` flag, mirroring resume and cancel, so an MCP host can
  recover without parsing prose.
- `Cancel` consumes a matching short-lived approval nonce and cancels a live run
  only when this MCP runtime owns its cancellation context. A live run owned by
  another runtime returns the typed `run_not_owned` error instead of reporting
  success; a stale process owner is handled through the durable cancellation
  request and persisted cancelled status.
- `Advise` returns a bounded, advisory Run Steward recommendation for one run. It
  projects the authoritative snapshot into a sanitized `RunObservation` (status,
  phase, reason codes, and counts only — never prompts, diffs, file paths, or
  model reasoning) and returns a schema-validated advisory action (`wait`,
  `inspect`, `notify`, `prepare_resume`, `ask_user`, or `report_issue`). It is
  strictly read-only and advisory: a missing, slow, invalid, or over-budget
  steward falls back to a deterministic template, and the controller never gates
  execution on the advisory. Exposed as the read-only `tagteam_advise` MCP tool
  when the lifecycle runtime is enabled. The optional model tier is a local-first
  OpenAI-compatible endpoint queried with a text-only request (no tool surface),
  bounded by per-run call/timeout/dedup budgets and a single-observer lease, so
  the steward cannot invoke Tagteam or inherit MCP/repository-write tools.

The base capability list intentionally excludes lifecycle mutations; the
enabled lifecycle runtime adds `start`, `resume`, and `cancel` only when those
handlers are available. No handler returns canned success or delegates to
arbitrary shell input.

`tagteam mcp` implements MCP protocol revision `2025-11-25` over stdio. It
advertises exactly the implemented tools and returns both structured JSON and
bounded text content. `tagteam_prepare_start` and `tagteam_prepare_resume` are
read-only; `tagteam_start`, `tagteam_resume`, and `tagteam_cancel` are marked
destructive and idempotent for MCP clients. An unverified binary keeps the
server read-only unless the operator explicitly passes `--allow-dev-build`.

## Authority and validation

- The canonical Git worktree root and Tagteam-derived repository ID are the
  repository identity. A caller-supplied mismatched ID is rejected.
- Each MCP server is bound to the worktree it was started for. Launch and
  lifecycle preparation for another repository are rejected rather than
  returning a handle this server cannot monitor.
- The MCP server binds once to the real Git worktree root derived from its
  start path (subdirectory and symlink aliases collapse to that root). Every
  prepare/start/resume/cancel request must match that host-derived repository
  identity. Request or model input is never used as a process working
  directory.
- Repository, run, and artifact paths are resolved to full canonical paths
  through one control-plane resolver. Symlinks are usable only when their real
  target remains inside the expected repository or run-state boundary;
  escaping or broken links fail closed before any read or write. The host-derived
  runs root is always `<state-root>/<repo-id>/runs`: a symlinked `runs`
  directory or `repo-id` parent that escapes that repository state directory is
  rejected before start prepares a run and before resume/cancel I/O, so an
  escaping link never becomes the trust boundary. Prepare-resume, resume, and
  cancel use control-safe artifact readers/writers for `state.json`,
  `meta.json`, `final.json`, `run.lock`, `events.jsonl`, `input.md`,
  cancel-request files, and MCP-resume auxiliary artifacts (`plan.json`,
  `supervisor-brief.md`, scout/work-plan JSON). Cancel and the cancellation
  watcher re-resolve the run directory under the runs-root boundary immediately
  before cancel-request I/O so a replaced run directory cannot redirect writes.
  MCP resume carries the canonical runs-root boundary throughout resumed
  execution: it re-resolves the run directory under that boundary immediately
  before lock acquisition, again after lock, immediately before each resumed-run
  artifact read (meta, final, prior review, diff/review verification), and again
  immediately before every host mutation and adapter request construction/dispatch
  (including shared delivery/progress/output helpers and post-scout/review/
  finalization paths reached during resume). The same gate also covers JSON
  repair (source reads, prompt/output/failure mkdir+writes, worker dispatch,
  and side-artifacts), independent `repo-instructions.md`/`.json` and
  `plan.json`/`plan-events.jsonl` persistence, baseline-test host-activity and
  test-output paths, multi-artifact prompt/relay/plan optional readers, and
  coder/reviewer contract-retry prompt writes. Path-gate failures surface as
  `ExitPreflightFailed` without writing through a stale path. Shared adapter
  requests carry an optional control-resume gate so a replacement after an
  earlier rebind cannot redefine the trust root. Resumed relay/plan reads stay
  on the control-safe artifact API; deferred failure persistence and quarantine
  are fail-closed when the run directory has escaped. Nil gate preserves ordinary
  CLI resume/helper behavior. Residual limitation: pure syscall-level TOCTOU
  remains possible after the final revalidation and before the subsequent
  read/write/exec.
- Allowed paths keep the existing syntax validator (absolute paths, traversal,
  globs, backslashes, blanks, and lexical duplicates are rejected), then are
  resolved through real paths under the canonical repository. Broken or
  escaping scope links and real-path duplicates (including root aliases of
  `.`) fail closed. On start, the host revalidates the approved canonical
  `allowed_paths` list itself (not a freshly retargeted result) and passes
  that approved list unchanged into `RunOptions`.
- Team fields are mode-specific. Supervisor, relay, adversarial, and solo
  cannot carry roles that do not exist in that mode.
- Prompts, role identifiers, time budgets, rounds, changed-file lists, status
  messages, plan entries, findings, and page sizes are bounded.
- Recovery policy is `assist`; model-authored commands and raw test commands
  are not part of the contract. Tests are selected by a named `test_preset`
  that resolves only from host-trusted configuration (`[test_presets]` in user
  config / built-in defaults, or trusted repo config with
  `--trust-repo-config`). Untrusted repo `.tagteam.toml` cannot define or
  influence presets. Lookup is exact-match on the normalized preset name (no
  case folding). The approval digest binds the preset **name**, not the
  resolved command.
- Start approvals bind the normalized launch plus operation and idempotency key,
  expire within 30 minutes, and are retained under the resolved state root to
  reject nonce replay across server restarts. The MCP host remains responsible
  for collecting explicit user confirmation before it sends an approval record;
  Tagteam verifies the record's scope and single use but cannot attest to its
  human origin.
- Approval nonces are single-use across every action. Start, resume, and cancel
  each consult the whole approval ledger, so a nonce already consumed by any
  start, resume, or cancel is refused (`approval_nonce_replayed`) rather than
  replayed for a different action. Resume and cancel digests bind the operation,
  canonical repository identity, and run identity; the persisted run fixes the
  roles and scope those actions inherit. A changed action always produces a new
  digest and therefore requires a new approval.
- A persisted, unfinalized start reservation blocks another start for the same
  worktree until that approval expires. This closes the gap before the runner
  has written `active.json`.
- A start with an empty `test_preset` uses Tagteam's normal trusted config
  defaults for the test command. A non-empty name is looked up in the trusted
  registry: unknown names fail deterministically (`unknown test_preset "…"`)
  without leaking registry contents; known names set the run's test command
  (and optional identity regex) from the preset entry only.

## Deferred transport and lifecycle work

The MCP adapter is a thin transport over this boundary. `prepare_resume` and
the resume runtime deliberately refuse live or stale run locks and active-run
pointers rather than altering ownership. The approval-ledger lock fails closed
if a stale owner remains after an abnormal process exit; this is surfaced as a
recoverable lifecycle error rather than launching without replay protection.
The cancellation watcher is scoped to active jobs and stops before the owning
runtime's terminal run returns, so a completed run does not leave a background
watcher behind.
Unknown contract versions and malformed persisted artifacts must fail with
typed, recoverable errors rather than inferred success.

A scout may add bounded symlink-topology observations to its reconnaissance so
the user and reviewer can understand indirection in the selected scope. That
evidence remains advisory: canonical real-path resolution and boundary
enforcement stay host-owned and cannot depend on a model response.
