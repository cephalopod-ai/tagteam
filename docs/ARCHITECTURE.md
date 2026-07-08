# Architecture

How `tagteam` is put together. This describes the *implemented* architecture;
where a detail is intended-but-partial it is marked.

## Summary

`tagteam` is a single-binary Go CLI that orchestrates one or more headless
coding-agent CLIs (adapters) through a run loop, captures deterministic Git
diffs and review artifacts, and writes machine-readable run state. The command
surface lives in `internal/cli`; all orchestration logic lives in one cohesive
package, `internal/tagteam`.

## Component map

| Component | File(s) | Responsibility |
|---|---|---|
| Entry point | `main.go` | Wires cobra root command, invokes `internal/cli`. |
| CLI surface | `internal/cli/root.go` | Defines commands (`run`, `review`, `fix`, `status`, `plan`, `transcript`, `doctor`, `init`), flag parsing, output formatting. |
| App / run loop | `internal/tagteam/runner.go` | `App` type; `Run`, `Review`, `Fix`, `Doctor`; the round loop, role dispatch, env policy, artifact writing. |
| Config resolution | `internal/tagteam/config.go` | Layered config (flags > shell env > `.env` overlay > repo `.tagteam.toml` > user config > defaults), profiles, `ResolveOptions`. |
| Adapters | `internal/tagteam/adapters.go` | Adapter interface + `codex`, `codex-oss`, `claude`, `agy`, `gosling`, `openai-compatible`; `Registry`, command construction, capability sets. |
| Types | `internal/tagteam/types.go` | `Mode`, `Role`, `ReasonCode`, `RunOptions`, `FinalRun`, `RunState`, exit codes, JSON contracts. |
| Run state / reasons | `internal/tagteam/run_state.go` | Failure classification, exit→reason mapping, role status/loss records, budget state, redacted persistence helpers. |
| Orchestration decision | `internal/tagteam/orchestration.go` | Host-owned single advisory adjustment (relay↔supervisor) before implementation. |
| Scout retrieval | `internal/tagteam/retrieval.go` | Bounded, local-only pre-scout retrieval evidence for relay `recon`. |
| Scout context budget | `internal/tagteam/context_budget.go` | Deterministic `ceil(prompt_bytes/3)` context estimate + policy. |
| Scout status | `internal/tagteam/scout_status.go` | Scout execution/failure classification. |
| Prompts | `internal/tagteam/prompts.go` | Role/system/brief/report prompt construction. |
| Schema | `internal/tagteam/schema.go` | JSON schemas for review / work-plan output contracts. |
| Redaction | `internal/tagteam/redact.go` | Overlay-aware secret redaction for persisted artifacts. |
| Bounded writer | `internal/tagteam/bounded_writer.go` | Capped output capture. |
| Process control | `internal/tagteam/process_{unix,windows}.go` | Platform process-group handling. |
| CLI exports | `internal/tagteam/cli_exports.go` | Symbols surfaced to the `internal/cli` layer. |

## Run modes

- **supervisor** (default): read-only supervisor writes a brief, worker
  implements, supervisor reviews; optional work-plan slicing.
- **relay**: read-only scout recon → coder implements → read-only supervisor
  reviews/arbitrates.
- **solo**: one implementation agent, no reviewer.
- **adversarial** (legacy): coder implements, adversary reviews.

## Execution flow (reviewed modes)

1. Preflight: resolve baseline, run dir, adapters; role availability checks.
2. Optional host-owned orchestration decision (one bounded adjustment).
3. Optional relay pre-scout retrieval + context-budget check + scout pass.
4. Round loop: editor/coder implements → deterministic diff capture → tests →
   reviewer/supervisor review. Findings loop back until pass, test failure, or
   round limit.
5. On round-limit exhaustion: collect final "what remains / what is disputed"
   reports from both agents.
6. Finalize: compute exit code, classify blocking/degraded reason, write
   `final.json` / `state.json` (redacted).

## Data model / persistence

Per run, artifacts are written under `.tagteam/runs/<run-id>/` (briefs, diffs,
reviews, tests, scout artifacts, `final.json`, `state.json`). Diffs are captured
through a temporary Git index, always excluding `.tagteam/`. `final.json` /
`state.json` carry machine-readable `status`, `degraded`, `blocking_reason`,
`role_statuses`, `role_losses`, `budgets`, `exit_code`. See the README
"Run Artifacts" section for the full field contract and reason-code vocabulary.

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

## Known architecture risks

- `internal/tagteam` is one large package; `runner.go` (~3.5k lines) is a
  natural candidate for a post-release split by concern.
- Adapter behavior depends on third-party CLI stability (documented in README
  "Compatibility Issues And Known Rough Edges").
- Supervisor slicing is more format-sensitive than the schema-validated final
  review path.

## Diagrams

See `docs/IMPLEMENTATION_DIAGRAMS.md`. One implementation diagram is also linked
from the root `README.md`.
