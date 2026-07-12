# Control Plane Contract

**Status:** draft producer contract, local MCP stdio transport, and approved
idempotent start are implemented. Resume and cancel remain disabled.

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
- `Status` projects the authoritative `RunSnapshot` assembled from existing
  run artifacts and supplies a stable snapshot digest.
- `Plan` and `Findings` return bounded cursor pages from `plan.json` and
  `findings.json`.
- `Diagnostics` verifies repository identity and resolves the state root
  without creating runtime state.
- `tagteam_prepare_start` exposes that validation to MCP clients without
  creating runtime state.
- `Start` reserves a durable run ID, consumes a matching short-lived approval
  nonce, launches Tagteam through its normal configuration and runner, and
  persists a terminal artifact if preflight fails before the runner can do so.

The capability list intentionally excludes `prepare_resume`, `resume`, and
`cancel`. Their request/result types are defined so consumers can review the
draft shape, but no handler returns canned success or delegates to arbitrary
shell input.

`tagteam mcp` implements MCP protocol revision `2025-11-25` over stdio. It
advertises exactly the implemented tools and returns both structured JSON and
bounded text content. `tagteam_prepare_start` is read-only; `tagteam_start` is
marked destructive and idempotent for MCP clients, and is the only mutating
tool currently exposed. An unverified binary keeps the server read-only unless
the operator explicitly passes `--allow-dev-build`.

## Authority and validation

- The canonical Git worktree root and Tagteam-derived repository ID are the
  repository identity. A caller-supplied mismatched ID is rejected.
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
  are not part of the contract. Tests are selected by a future trusted preset
  registry.
- Start approvals bind the normalized launch plus operation and idempotency key,
  expire within 30 minutes, and are retained under the resolved state root to
  reject nonce replay across server restarts. The MCP host remains responsible
  for collecting explicit user confirmation before it sends an approval record;
  Tagteam verifies the record's scope and single use but cannot attest to its
  human origin.
- A persisted, unfinalized start reservation blocks another start for the same
  worktree until that approval expires. This closes the gap before the runner
  has written `active.json`.
- A start with no configured trusted test preset uses Tagteam's normal trusted
  config defaults. Non-empty `test_preset` references are rejected until a
  trusted preset registry exists.

## Deferred transport and lifecycle work

The MCP adapter is a thin transport over this boundary. Before it can advertise
resume or cancel, Tagteam still needs cancellation ownership after restart and
non-mutating resume assessment. The approval-ledger lock fails closed if a stale
owner remains after an abnormal process exit; this is surfaced as a recoverable
start error rather than launching without replay protection.
Unknown contract versions and malformed persisted artifacts must fail with
typed, recoverable errors rather than inferred success.

A scout may add bounded symlink-topology observations to its reconnaissance so
the user and reviewer can understand indirection in the selected scope. That
evidence remains advisory: canonical real-path resolution and boundary
enforcement stay host-owned and cannot depend on a model response.
