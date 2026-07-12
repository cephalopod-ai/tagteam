# Control Plane Contract

**Status:** draft producer contract and local read-only MCP stdio transport are
implemented. Mutating controller operations remain disabled.

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
- `Status` projects the authoritative `RunSnapshot` assembled from existing
  run artifacts and supplies a stable snapshot digest.
- `Plan` and `Findings` return bounded cursor pages from `plan.json` and
  `findings.json`.
- `Diagnostics` verifies repository identity and resolves the state root
  without creating runtime state.

The capability list intentionally excludes `start`, `prepare_resume`, `resume`,
and `cancel`. Their request/result types are defined so consumers can review the
draft shape, but no handler returns canned success or delegates to arbitrary
shell input.

`tagteam mcp` implements MCP protocol revision `2025-11-25` over stdio. It
advertises exactly the implemented read tools and returns both structured JSON
and bounded text content. The server does not write repository or run state.

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
- Approval records are action-bound, expiring values. Enforcement and durable
  nonce replay protection must exist before mutating operations are advertised.

## Deferred transport and lifecycle work

The MCP adapter is a thin transport over this boundary. Before it can advertise
mutating tools, Tagteam still needs one durable lifecycle owner,
immediate run-handle semantics, idempotency storage, action-bound approval
verification, cancellation ownership, and non-mutating resume assessment.
Unknown contract versions and malformed persisted artifacts must fail with
typed, recoverable errors rather than inferred success.

A scout may add bounded symlink-topology observations to its reconnaissance so
the user and reviewer can understand indirection in the selected scope. That
evidence remains advisory: canonical real-path resolution and boundary
enforcement stay host-owned and cannot depend on a model response.
