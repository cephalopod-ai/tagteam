# Control Plane Contract

**Status:** draft producer contract, local MCP stdio transport, approved
idempotent start and resume, and non-mutating resume assessment are
implemented. Cancel remains disabled.

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

The base capability list intentionally excludes `resume` and `cancel`; the
enabled lifecycle runtime adds `start` and `resume` only when those handlers
are available. Cancel remains gated. No handler returns canned success or
delegates to arbitrary shell input.

`tagteam mcp` implements MCP protocol revision `2025-11-25` over stdio. It
advertises exactly the implemented tools and returns both structured JSON and
bounded text content. `tagteam_prepare_start` and `tagteam_prepare_resume` are
read-only; `tagteam_start` and `tagteam_resume` are marked destructive and
idempotent for MCP clients. An unverified binary keeps the server read-only
unless the operator explicitly passes `--allow-dev-build`.

## Authority and validation

- The canonical Git worktree root and Tagteam-derived repository ID are the
  repository identity. A caller-supplied mismatched ID is rejected.
- Each MCP server is bound to the worktree it was started for. Launch and
  lifecycle preparation for another repository are rejected rather than
  returning a handle this server cannot monitor.
- Repository, run, and artifact paths are resolved to full canonical paths.
  Symlinks are usable only when their real target remains inside the expected
  repository or run-state boundary; escaping or broken links fail closed.
- Allowed paths use Tagteam's existing deterministic scope validator. Absolute
  paths, traversal, globs, backslashes, blanks, and normalized duplicates are
  rejected.
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
Cancel still requires deterministic host-owned process ownership after server
restart and remains deferred.
Unknown contract versions and malformed persisted artifacts must fail with
typed, recoverable errors rather than inferred success.

A scout may add bounded symlink-topology observations to its reconnaissance so
the user and reviewer can understand indirection in the selected scope. That
evidence remains advisory: canonical real-path resolution and boundary
enforcement stay host-owned and cannot depend on a model response.
