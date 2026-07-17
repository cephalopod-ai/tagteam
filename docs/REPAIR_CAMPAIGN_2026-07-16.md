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

Status: implemented and locally validated; checkpoint commit follows this
documentation update.

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
