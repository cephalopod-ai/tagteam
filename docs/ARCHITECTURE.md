# Architecture

How `tagteam` is put together. This describes the *implemented* architecture;
where a detail is intended-but-partial it is marked.

## Summary

`tagteam` is a single-binary Go CLI that orchestrates one or more headless
coding-agent CLIs (adapters) through a run loop, captures deterministic Git
diffs and review artifacts, writes machine-readable run state, and exposes an
interactive live TUI that can both inspect persisted state and launch new runs
through the same runner/config path. The command surface lives in `internal/cli`;
orchestration logic lives in `internal/tagteam`; the TUI lives in `internal/tui`.

## Component map

| Component | File(s) | Responsibility |
|---|---|---|
| Entry point | `main.go` | Wires cobra root command, invokes `internal/cli`. |
| CLI surface | `internal/cli/root.go`, `internal/cli/tui.go` | Defines commands including `resume`, `transfer`, `findings defer`, and `verify-install`, flag parsing, output formatting, and TUI run selection. |
| App / run loop | `internal/tagteam/runner.go` | `App` type; `Run`, `Review`, `Fix`, `Doctor`; the round loop, role dispatch, env policy, artifact writing. |
| Config resolution | `internal/tagteam/config.go` | Layered config (flags > shell env > `.env` overlay > repo `.tagteam.toml` > user config > defaults), profiles, `ResolveOptions`. |
| Adapters | `internal/tagteam/adapters.go` | Adapter interface + `codex`, `codex-oss`, `claude`, `agy`, `gosling`, `openai-compatible`; `Registry`, command construction, capability sets. |
| Types | `internal/tagteam/types.go` | `Mode`, `Role`, `ReasonCode`, `RunOptions`, `FinalRun`, `RunState`, exit codes, JSON contracts. |
| Artifact store | `internal/tagteam/artifact_store.go`, `durable_io.go` | Derives repository identity, maintains `.tagteam/repo.json`, migrates legacy state, and atomically persists external artifacts. |
| Active run pointer | `internal/tagteam/active_run.go` | Persists external `active.json` for in-flight run discovery and failure cleanup. |
| Resilience | `state_machine.go`, `run_lock.go`, `invocation_lock.go`, `recovery.go`, `resume.go`, `resume_execution.go`, `resume_phases.go`, `timeout_calibration.go`, `invocation_stream.go` | Phase journaling, locks/cancellation, cross-process claude invocation serialization, partial-diff recovery, same-run phase continuation, calibrated deadlines, and durable subprocess streams. |
| Quality / transfer | `quality_gates.go`, `findings.go`, `test_hardening.go`, `transfer.go`, `integrity.go` | Scope/churn/data-loss/findings/regression gates, isolated tests, structural integrity, and explicit patch transfer. |
| Snapshot / live status | `internal/tagteam/snapshot.go` | Builds `RunSnapshot` from `active.json`, `state.json`, `final.json`, and `plan.json`. |
| Run state / reasons | `internal/tagteam/run_state.go` | Failure classification, exit→reason mapping, role status/loss records, budget state, redacted persistence helpers. |
| Orchestration decision | `internal/tagteam/orchestration.go` | Host-owned single advisory adjustment (relay↔supervisor) before implementation. |
| Scout retrieval | `internal/tagteam/retrieval.go` | Bounded, local-only pre-scout retrieval evidence for relay `recon`. |
| Code-intelligence sensor | `internal/tagteam/codeintel.go`, `codeintel_provider.go` | Opt-in, read-only command boundary; validates revision-bound observations, computes Git staleness, persists `code-intel-round-1.json`, and exposes only fresh derived evidence to the relay scout prompt. |
| Scout context budget | `internal/tagteam/context_budget.go` | Deterministic `ceil(prompt_bytes/3)` context estimate + policy. |
| Scout status | `internal/tagteam/scout_status.go` | Scout execution/failure classification. |
| Prompts | `internal/tagteam/prompts.go` | Role/system/brief/report prompt construction. |
| Schema | `internal/tagteam/schema.go` | JSON schemas for review / work-plan output contracts. |
| Redaction | `internal/tagteam/redact.go` | Overlay-aware secret redaction for persisted artifacts. |
| Bounded writer | `internal/tagteam/bounded_writer.go` | Capped output capture. |
| Process control | `internal/tagteam/process_{unix,windows}.go` | Platform process-group handling. |
| CLI exports | `internal/tagteam/cli_exports.go` | Symbols surfaced to the `internal/cli` layer. |
| Interactive TUI | `internal/tui/render.go`, `internal/tui/state.go`, `internal/tui/tui.go` | Dashboard with recent runs, compose/settings, slash commands, and a scrollable detail pane. Reads `RunSnapshot`/`plan.json` for inspection and invokes `App.Run` for TUI-launched runs. |

## Run modes

- **supervisor** (default): read-only supervisor writes a brief, worker
  implements, supervisor reviews; optional work-plan slicing.
- **relay**: read-only scout recon → coder implements → read-only supervisor
  reviews/arbitrates.
- **solo**: one implementation agent, no reviewer.
- **adversarial**: coder implements, independent adversary audits and reviews.

## Execution flow (reviewed modes)

1. Preflight: resolve baseline, run dir, adapters; role availability checks.
2. Optional host-owned orchestration decision (one bounded adjustment).
3. Optional relay pre-scout retrieval and code-intelligence sensor + context-budget check + scout pass. Both are derived evidence; only fresh code-intelligence observations enter prompt context.
4. Round loop: editor/coder implements → deterministic diff capture → tests →
   reviewer/supervisor review. Findings loop back until pass, test failure, or
   round limit.
5. On round-limit exhaustion: collect final "what remains / what is disputed"
   reports from both agents.
6. Finalize: compute exit code, classify blocking/degraded reason, write
   `final.json` / `state.json` (redacted).

## Data model / persistence

Per run, authoritative artifacts are written under
`~/.local/state/tagteam/<repo-id>/runs/<run-id>/` (briefs, streams, diffs,
reviews, tests, recovery and gate artifacts, `final.json`, `state.json`, optional
`plan.json`). `.tagteam/repo.json` is the only in-worktree runtime pointer;
`active.json` and `latest.json` live beside the external `runs/` directory.
Diffs are captured through a temporary Git index, always excluding `.tagteam/`.
`final.json` / `state.json` carry machine-readable `status`, `degraded`,
`blocking_reason`, `role_statuses`, `role_losses`, `budgets`, `exit_code`.
`snapshot.go` assembles those files into a `RunSnapshot` for live readers such
as the TUI. See the README
"Run Artifacts" section for the full field contract and reason-code vocabulary.

## Live status / TUI flow

1. The runner creates an external run directory and writes external `active.json`.
2. As the run advances, it updates `state.json` with phase, round, role status,
   and latest artifact paths.
3. On completion, it writes `final.json` and removes external `active.json` on the
   success path; aborted runs may leave `active.json` behind with
   `status = "failed"` for postmortem inspection.
4. `BuildRunSnapshot` merges `active.json`, `state.json`, `final.json`, and
   `plan.json` into one read-only `RunSnapshot`.
5. `tagteam status` resolves the active run before the latest completed run and
   renders the same snapshot, including live progress when present.
6. `tagteam tui` polls that snapshot once a second while the run is active and
   renders it in a scrollable detail pane alongside recent runs and a compose
   pane.

## Dependency boundaries

- `main` → `internal/cli` → `internal/tagteam`. No reverse dependency.
- External: cobra/pflag (CLI), BurntSushi/toml (config), google/shlex (arg
  parsing). No network client except the `openai-compatible` HTTP adapter.
- Vendor CLIs (`codex`, `claude`, `agy`, `gosling`) are invoked as subprocesses;
  they authenticate via their own sessions. Non-coder roles run under a
  restricted environment that forwards only provider auth keys plus a small
  allowlist (see `mergeRestrictedCommandEnv`).

## Extension points

- New adapter: implement the `Adapter` interface and register it in `Registry`.
- New mode/role: extend `Mode`/`Role` and the run-loop dispatch.
- New reason code: extend the `ReasonCode` enum and the classifiers in
  `run_state.go`.
- New live status consumer: prefer reading `RunSnapshot` instead of reverse-
  engineering `final.json` / `state.json` directly.

## Known architecture risks

- `internal/tagteam` remains a broad package, but runner, config, type, adapter,
  TUI-state, and test declarations are split into focused files capped at 800
  lines. Further package-boundary extraction should preserve the current
  artifact and adapter contracts.
- Adapter behavior depends on third-party CLI stability (documented in README
  "Compatibility Issues And Known Rough Edges").
- Supervisor slicing is more format-sensitive than the schema-validated final
  review path.

## Diagrams

See `docs/IMPLEMENTATION_DIAGRAMS.md`. One implementation diagram is also linked
from the root `README.md`.
