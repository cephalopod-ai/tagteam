# tagteam

[![CI](https://github.com/cephalopod-ai/tagteam/actions/workflows/ci.yml/badge.svg)](https://github.com/cephalopod-ai/tagteam/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

`tagteam` is a standalone Go CLI that runs one or more headless coding agents as one command.

By default it runs in **supervisor mode**:

- a `supervisor` agent that writes a compact implementation brief, then reviews the resulting diff (it does not edit files by default)
- a `worker` agent that implements the brief and fixes findings

It can also run **relay mode** (`--relay` / `--mode relay`):

- a cheap read-only `scout` agent performs reconnaissance
- a write-enabled `coder` implements
- a stronger read-only `supervisor` reviews and arbitrates

For quick baseline runs it can run **solo mode** (`--solo <adapter[:model]>` / `--mode solo`):

- one implementation agent edits the repo
- no reviewer, supervisor, adversary, or scout runs
- optional tests still run, and output explicitly reports `review=none`

And it can run the original **adversarial mode** (`--mode adversarial`):

- a `coder` agent that edits the repo
- an `adversary` agent that reviews the resulting diff

In reviewed modes the tool loops findings back into the editor role until the change passes review, tests fail, or the user-defined round limit is reached. When the limit is reached with unresolved blocker/major findings, `tagteam` stops asking for edits and asks both agents for final "what remains incomplete / what do you dispute" reports instead of continuing indefinitely. Solo mode runs once and does not pretend to be reviewed.

## Vision

The goal is to make adversarial review and supervisor-worker coding transparent in a CLI.

Instead of hiding the interaction inside a vendor UI, `tagteam` makes the roles explicit, saves the brief/review/diff/test artifacts for each run, and keeps the handoff between "build" and "criticize" inspectable from the repository.

## Architecture at a glance

Reviewed modes run an implement → diff → test → review loop, feeding findings
back to the editor until the change passes, tests fail, or the round limit is
reached:

```mermaid
flowchart TD
    start([tagteam run]) --> pre[Preflight: baseline, run dir, adapter checks]
    pre --> impl[Editor / coder implements]
    impl --> diff[Deterministic diff capture]
    diff --> tests[Run tests]
    tests --> review[Reviewer / supervisor review]
    review --> pass{Pass?}
    pass -- yes --> final[Finalize + write final.json/state.json]
    pass -- no --> limit{Round limit reached?}
    limit -- no --> impl
    limit -- yes --> reports[Collect final reports from both agents]
    reports --> final
    final --> done([exit code + reason])
```

Full documentation — architecture, more diagrams, and the test ledger — is
indexed in [docs/INDEX.md](docs/INDEX.md).

## Status

This repository is an early implementation of the v2 design. The core run loop, adapter abstraction, persisted run artifacts, and main command surface are in place. Some hardening and release work is still pending.

Recent additions in this repo:

- supervisor/worker mode is now the default flow
- relay scout/coder/supervisor mode is available with `--relay`
- solo mode is available with `--solo <adapter[:model]>`
- adversarial coder/adversary mode remains available for backward compatibility
- saved run artifacts include briefs, diffs, reviews, tests, and final summaries
- command surface now includes `review`, `fix`, `status`, `transcript`, `doctor`, and `init`
- config layering supports repo config, user config, env overrides, flags, and named profiles
- explicit repo instruction files are loaded by default and appended to role prompts
- machine-readable output and dry-run support make the CLI easier to script and debug

Current commands:

- `tagteam "<prompt>"`
- `tagteam review`
- `tagteam fix`
- `tagteam status`
- `tagteam plan [RUN_ID]`
- `tagteam transcript [RUN_ID]`
- `tagteam doctor`
- `tagteam init`

## Requirements

- Go 1.23+
- Git
- At least one supported agent CLI on `PATH`

Supported adapters in this repo today:

- `codex`
- `codex-oss`
- `claude`
- `agy`
- `gosling` (coder-only)
- `openai-compatible` / `oai` (read-only reviewer/scout first cut)

## Authentication

Each vendor CLI adapter (`codex`, `claude`, `agy`, `gosling`, etc.) must already be logged in on your machine before you run `tagteam`. `tagteam` does not run vendor login flows, store credentials, or proxy/inject API keys for those CLIs. If an adapter is not authenticated, the run will fail with that CLI's own auth error.

This note applies to the vendor CLI adapters; the separate `openai-compatible` adapter uses its documented `api_key_env` setting.

`tagteam` also reads a repo-local `.env` file from the selected workdir as a scoped overlay. It does not mutate the global process environment; exported shell variables still take precedence, and `.env` values are passed only to tagteam's config resolver and invoked adapters/tests. A starter template is included as [`.env_template`](.env_template).

## Compatibility Issues And Known Rough Edges

`tagteam` depends on third-party agent CLIs and compatible HTTP backends whose behavior can change without warning. Expect some adapter-specific rough edges, especially as upstream tools evolve.

Current caveats:

- Vendor CLI flag drift can break adapters. `codex`, `codex-oss`, `claude`, `agy`, `gosling`, and similar tools may rename flags, change output formats, or alter auth behavior between releases.
- Authentication is adapter-specific. CLI-backed adapters usually rely on the vendor's own login/session flow; `openai-compatible` / `oai` relies on explicit environment/config values.
- Supervisor slicing is more format-sensitive than the final review pass. The final review path is schema-validated, but some supervisor planning/instruction steps still depend on adapter output being reasonably well-formed.
- Different adapters do not expose identical capabilities. Some support schema-constrained output, stdin, or session resume; others do not. `tagteam` degrades where possible, but behavior is not perfectly uniform.
- Local `.env` loading is a convenience feature, not a secret-management system. It helps with local runs, but shell-exported environment variables still take precedence.
- Repo-local `.tagteam.toml` is partially trusted by default: low-authority defaults such as roles/models can be read, but shell tests, adapter passthrough args, Claude permission/tool widening, and `openai-compatible` endpoints/headers are ignored unless you pass `--trust-repo-config`.
- Published binaries are broader than real-world manual validation. Releases may include targets that pass Go-level CI but have not been exercised end-to-end with every supported vendor CLI.

Practical guidance:

- Prefer `tagteam doctor` before blaming orchestration logic.
- Use `--dry-run` to inspect resolved invocations when an adapter behaves unexpectedly.
- Start with small prompts and targeted tests when trying a new adapter/model pairing.
- Treat new modes, new adapters, and unusual cross-vendor combinations as experimental until you have run them in your own environment.

## Install

With a Go toolchain (1.23+):

```bash
go install github.com/cephalopod-ai/tagteam@latest
```

Or download a prebuilt archive for your platform from GitHub Releases, then put
the `tagteam` binary on your `PATH`.

Binary releases are published for:

- macOS (`darwin/amd64`, `darwin/arm64`)
- Linux (`linux/amd64`, `linux/arm64`)
- Windows (`windows/amd64`, `windows/arm64`)

Platform note: CI runs formatting, tests, and vet on macOS, Linux, and Windows
before release packaging. Real end-to-end vendor CLI behavior has only been
manually exercised on macOS 26 Tahoe and Ubuntu so far; Windows artifacts should
be treated as Go-level verified but operationally experimental until the vendor
CLI adapters are exercised there in real use.

Create a release by pushing a tag such as `v0.1.0`; GitHub Actions runs
cross-platform Go checks, then GoReleaser attaches archives plus
`checksums.txt` to the release.

Build from source:

```bash
go build -o tagteam .
```

Run locally:

```bash
go run . "add OAuth login"
```

## Quick Start

Default run (supervisor mode, built-in worker `agy:Gemini 3.5 Flash (High)` and supervisor `claude:opus`):

```bash
tagteam "add OAuth login"
```

Supervisor mode slices work by default before the worker edits. The supervisor writes a bounded work plan, selects one package, and the worker implements only that package. If packages remain, `tagteam` stops after the selected package passes and reports the next packages unless `--auto-next-package` is set.

```bash
tagteam --slice --max-packages 5 --package P1 "add OAuth login"
```

Choose explicit worker/supervisor adapters, rounds, and a test command:

```bash
tagteam \
  --worker codex:gpt-5-codex \
  --supervisor claude:opus \
  -r 3 \
  -t "go test ./..." \
  "refactor billing flow"
```

The supervisor is read-only by default (it writes the brief and review findings but does not edit files). Allow it to make small exploratory edits with `--supervisor-can-edit`.

Supervisor slicing also creates a run checklist. `plan.json` records package
status, and `plan-events.jsonl` records status transitions and review-added
items. `tagteam status` shows the latest checklist when present; use
`tagteam plan [RUN_ID]` to print a run's checklist directly.

Supervisor and relay runs may perform one bounded orchestration adjustment
before implementation starts. Agents can emit compact advisory signals, but
`tagteam` owns the decision: relay may simplify to supervisor mode for small
tasks, and supervisor mode may escalate to relay only when the worker reports
insufficient context and the supervisor agrees. There is no back-and-forth
replanning loop.

### Solo mode

Solo mode runs exactly one implementation agent and no reviewer. It is useful as a quick baseline for comparing cost, speed, and quality against supervisor, relay, or adversarial runs.

```bash
tagteam --solo codex:gpt-5.5 "rename UserSvc to UserService"
tagteam --mode solo --worker claude:sonnet -t "go test ./..." "make a small README edit"
```

In solo mode, legacy `-mc` and preferred `--worker` both select the implementation agent. Reviewer flags such as `-ma`, `--reviewer`, and `--supervisor` are invalid.

### Relay mode

Relay mode runs a cost-aware three-agent pipeline: read-only scout reconnaissance, supervisor brief, supervisor-condensed worker instructions, coder implementation, deterministic diff capture, tests, post-implementation scout advisory pass, and strict supervisor review.

```bash
tagteam --relay "add OAuth login"
```

For small tasks, relay can simplify to supervisor mode before scout runs when
the supervisor advises that the direct worker/review path is enough. The host
records the decision and skips scout-heavy relay setup; the supervisor review
remains the authoritative gate.

Relay mode is a full-run workflow. It does not currently have a review-only
variant: `tagteam review` remains adversary-only and does not run scout or
supervisor relay steps.

For relay mode, the scout should have a strong context window. A practical
recommendation is `256k` or more, and ideally at least as much context as the
relay coder and supervisor. Small-context scouts tend to lose most of the
benefit of relay reconnaissance once repo instructions, retrieval evidence, and
task context are included.

Relay pre-scout `recon` uses bounded local retrieval by default before the
scout model runs. Retrieval is host-owned, local-only, advisory, and does not
use embeddings, network search, persistent indexes, daemons, or background
caches. It writes `retrieval-round-1.json` with status/evidence metadata, then
passes only a compact bounded summary into the scout prompt. Disable this layer
with `--no-scout-retrieval`:

```bash
tagteam --relay --no-scout-retrieval "add OAuth login"
```

The built-in relay profile uses:

```toml
[profiles.relay]
mode = "relay"
scout = "agy:gemini-3.5-flash-low"
coder = "codex:gpt-5.4-mini"
supervisor = "claude:sonnet"
scout_mode = "recon"
scout_retrieval = true
scout_failure_policy = "continue"
post_scout_mode = "polish"
rounds = 2
```

Override relay roles explicitly:

```bash
tagteam \
  --mode relay \
  --scout agy:gemini-3.5-flash-low \
  --scout-mode recon \
  --post-scout-mode polish \
  --coder codex:gpt-5.4-mini \
  --supervisor claude:sonnet \
  "refactor billing flow"
```

In relay mode, legacy `-mc` selects the coder and `-ma` selects the supervisor.
Scout modes are task-typed: `recon`, `lint`, `polish`, `tests`, or `risk`. Scout findings are advisory context only; only the supervisor review can fail a run with blocker/major findings.

Retrieval runs only for relay pre-scout `scout_mode = "recon"` and never for
post-scout, supervisor mode, adversarial mode, or solo mode. If `rg` is
missing, retrieval times out, or no useful matches are found, tagteam records
that status and continues with normal scout reconnaissance. Configure it with
`scout_retrieval = true|false` or `TAGTEAM_SCOUT_RETRIEVAL=false`; flags still
have highest precedence.

If an adapter has explicit context limits configured, relay pre-scout `recon`
also writes `scout-context-round-1.json` before calling the scout. The check is
deterministic and conservative (`ceil(prompt_bytes/3)`), not provider metadata.
Statuses are `unknown`, `ok`, `near_limit`, or `exceeds_limit`. Near-limit runs
compact retrieval more aggressively; retrieval is disabled if it alone would
push the scout prompt over the configured usable context.
Use `scout_context_policy = "warn" | "skip" | "block"` or
`--scout-context-policy` to decide whether a too-small configured scout context
only warns, skips/degrades the scout pass, or blocks before scout invocation.

Scout model failures are explicit and configurable. By default,
`scout_failure_policy = "continue"` warns, writes
`scout-execution-round-1.json`, and continues without scout context so the coder
and supervisor can still run. Use `--strict-scout` or
`scout_failure_policy = "fail"` when evaluation or reproducibility should abort
before coder edits if the scout invocation, scout JSON contract, or scout
context-budget check fails. Retrieval unavailable/timeout/empty/degraded states
are separate from scout model failure and continue into the scout pass where
possible.

For finer control, `loss_policy` can be configured per non-primary role:
`block`, `degrade`, `replace_then_block`, or `replace_then_degrade`. Replacement
is bounded to preflight fallback selection; tagteam does not loop or replay an
already-started role invocation. Fallback chains are ordered, deduped, capped at
five targets, and recorded in `final.json`.

### Adversarial mode (backward compatible)

The original coder/adversary loop is still available via `--mode adversarial`. The legacy `-mc`/`-ma` flags keep working and map onto the active mode's roles: `-mc` selects the worker in supervisor mode and the coder in adversarial mode; `-ma` selects the supervisor in supervisor mode and the adversary in adversarial mode.

```bash
tagteam --mode adversarial \
  -mc codex:gpt-5-codex \
  -ma claude:opus \
  -r 3 \
  -t "go test ./..." \
  "refactor billing flow"
```

`--reviewer` is an adversarial-mode-flavored alias for `-ma`/`--supervisor`:

```bash
tagteam --mode adversarial -mc agy --reviewer claude:sonnet "clean up the CLI help"
```

Use Agy with its configured default Gemini model:

```bash
tagteam --worker agy --supervisor claude:sonnet "clean up the CLI help"
```

The built-in `agy` default model is `gemini-3.5-flash`; override it with `agy:<model>`.

### OpenAI-compatible reviewers

`openai-compatible` adds a small HTTP adapter for OpenAI-compatible `/chat/completions` APIs such as Featherless.ai, OpenRouter, and local gateways. This first cut is read-only: use it as the adversary/reviewer or relay scout, not as the coder/worker.

Featherless.ai:

```toml
[adapters.openai_compatible]
base_url = "https://api.featherless.ai/v1"
api_key_env = "FEATHERLESS_API_KEY"
default_model = "openai/gpt-oss-120b"
```

Create a local `.env` first:

```bash
cp .env_template .env
```

Then set:

```bash
FEATHERLESS_API_KEY=your-key-here
```

```bash
tagteam \
  --mode adversarial \
  -mc claude:sonnet \
  -ma openai-compatible:openai/gpt-oss-120b \
  --show-review \
  "make a tiny README wording cleanup"
```

OpenRouter:

```toml
[adapters.openai_compatible]
base_url = "https://openrouter.ai/api/v1"
api_key_env = "OPENROUTER_API_KEY"
default_model = "openai/gpt-oss-120b"
extra_headers = { "HTTP-Referer" = "https://github.com/your/repo", "X-Title" = "tagteam" }
```

Equivalent environment overrides are available for `base_url`, `api_key_env`, model, and simple comma-separated headers via `TAGTEAM_OPENAI_COMPATIBLE_BASE_URL`, `TAGTEAM_OPENAI_COMPATIBLE_API_KEY_ENV`, `TAGTEAM_OPENAI_COMPATIBLE_MODEL`, and `TAGTEAM_OPENAI_COMPATIBLE_HEADERS`.

Review the current diff only:

```bash
tagteam review --fail-on-review
```

Apply fixes from the latest saved review:

```bash
tagteam fix
```

## Configuration

Configuration precedence is:

`flags > shell TAGTEAM_* env > workdir .env TAGTEAM_* overlay > repo .tagteam.toml > user config > built-in defaults`

If a `.env` file exists in the selected workdir, `tagteam` parses it as a small, line-oriented dotenv subset: `KEY=VALUE`, optional `export`, inline comments outside quotes, single-quoted raw values, and double-quoted escape sequences such as `\n`. `.env` is a convenience source for local development; it is not a full shell parser, and explicit shell exports still win.

Repo-local `.tagteam.toml` is loaded in untrusted mode by default. It can set ordinary role/model defaults, but high-authority settings such as `defaults.test`, `git_safety`, adapter `extra_args`, Claude `coder_allowed_tools` / `bare`, and `openai-compatible` `base_url`, `api_key_env`, `extra_headers`, or `extra_args` require `--trust-repo-config`.

User config path:

- macOS/Linux: `~/.config/tagteam/config.toml`

Starter config:

```bash
tagteam init
```

Relevant `defaults` keys:

- `mode` — `supervisor` (default), `solo`, `adversarial`, or `relay`
- `worker` — `adapter[:model]` target used in solo mode
- `worker` / `supervisor` — `adapter[:model]` targets used in supervisor mode
- `coder` / `adversary` — `adapter[:model]` targets used in adversarial mode
- `scout` / `coder` / `supervisor` — `adapter[:model]` targets used in relay mode
- `scout_mode` / `post_scout_mode` — relay scout task modes: `recon`, `lint`, `polish`, `tests`, or `risk`
- `scout_retrieval` — enable bounded local retrieval for relay pre-scout `recon` (default `true`; disable with `--no-scout-retrieval` or `TAGTEAM_SCOUT_RETRIEVAL=false`)
  Relay scouts work best with `256k+` context and ideally at least as much context as the relay coder/supervisor.
- `scout_failure_policy` — relay scout model failure handling: `continue` (default) or `fail`; `--strict-scout` maps to `fail`, and `TAGTEAM_SCOUT_FAILURE_POLICY` can override config
- `scout_context_policy` — relay scout configured-context behavior: `warn` (default), `skip`, or `block`; `--scout-context-policy` overrides it
- `supervisor_slicing` — split supervisor-mode work into bounded packages before implementation
- `max_packages` — maximum package count for supervisor slicing
- `package` — selected package ID to execute from the work plan
- `auto_next_package` — continue into additional packages while the normal round cap allows it
- `respect_repo_instructions` — load explicit repo instruction files and append them to role prompts
- `rounds` — hard cap on implementation/review cycles; exhausted runs stop and collect final reports from both agents
- `max_role_invocations` — optional hard cap on adapter calls in one run; `--max-role-invocations` overrides it
- `test`, `git_safety`

Profiles may override `mode`, `scout`, `scout_mode`, `scout_retrieval`,
`scout_failure_policy`, `scout_context_policy`, `loss_policy`, `fallbacks`,
`post_scout_mode`, `worker`, `supervisor`, `coder`, `adversary`, `rounds`, and
`test`. A profile that sets `coder`/`adversary` but omits `mode` resolves as an
adversarial-mode profile, so profiles written before `mode` existed keep
working unchanged:

```toml
[defaults]
mode = "supervisor"
worker = "agy:Gemini 3.5 Flash (High)"
supervisor = "claude:opus"
supervisor_slicing = true
max_packages = 5
rounds = 2

[profiles.relay.loss_policy]
scout = "replace_then_degrade"
supervisor = "block"

[profiles.relay.fallbacks]
scout = ["openai-compatible:gpt-oss-120b"]
supervisor = ["claude:sonnet", "codex:gpt-5.4"]

[profiles.fast]
coder = "codex:gpt-5-codex-mini"
adversary = "claude:haiku"
rounds = 1
```

Adapter configs may optionally declare deterministic context budgets used by
relay pre-scout `recon`:

```toml
[adapters.openai_compatible]
base_url = "https://api.example.test/v1"
api_key_env = "EXAMPLE_API_KEY"
max_context_tokens = 32768
reserved_output_tokens = 2048
```

For `openai-compatible`, environment overrides are
`TAGTEAM_OPENAI_COMPATIBLE_MAX_CONTEXT_TOKENS` and
`TAGTEAM_OPENAI_COMPATIBLE_RESERVED_OUTPUT_TOKENS`. Omitted limits mean
`unknown` and preserve existing relay behavior.

Repo instructions are loaded from the selected workdir, then from the Git root
when different, in this exact file order: `AGENTS.md`, `agent.md`,
`.tagteam/AGENTS.md`, `.codex/AGENTS.md`, `.claude/AGENTS.md`,
`.agy/AGENTS.md`. Only those exact files are read; vendor skill/plugin
directories are not recursively ingested. Disable this layer with
`--no-repo-instructions`.

## Run Artifacts

Each run writes artifacts under:

```text
.tagteam/runs/<run-id>/
```

Typical contents include:

- `meta.json`
- `input.md`
- `repo-instructions.md`
- `repo-instructions.json`
- `orchestration-decision.json` (supervisor/relay host-owned advisory decision)
- `plan.json` / `plan-events.jsonl` (supervisor mode with slicing)
- `solo-round-1.md` (solo mode)
- `supervisor-work-plan.json` (supervisor mode with slicing)
- `supervisor-brief.md` (supervisor or relay mode, round 1)
- `retrieval-round-1.json` (relay pre-scout `recon` when retrieval is enabled)
- `scout-context-round-1.json` (relay pre-scout `recon` context-budget check)
- `scout-execution-round-1.json` (relay scout host-owned success/failure/degraded status)
- `scout-round-1.json` (relay mode)
- `supervisor-instructions.md` (relay mode)
- `worker-round-N.md` (supervisor mode) / `coder-round-N.md` (adversarial or relay mode)
- `diff-round-N.patch`
- `diff-round-N.numstat`
- `diff-round-N.files.json`
- `diff-round-N.sha256`
- `bundle-<role>-round-N/` (host-owned review/supervisor input bundle)
- `test-round-N.txt`
- `post-scout-execution-round-N.json` (relay post-scout host-owned success/failure status)
- `post-scout-round-N.json` (relay mode)
- `supervisor-round-N.json` (supervisor mode) / `adversary-round-N.json` (adversarial mode) / `supervisor-review-round-N.json` (relay mode)
- `worker-final-report.md` / `coder-final-report.md` and `supervisor-final-report.md` / `adversary-final-report.md` when the round limit is exhausted
- `final.json`
- `state.json`

Diff artifacts are captured through a temporary Git index, not the real staging
area. The canonical patch includes tracked changes, deletions, renames, binary
patches, and untracked files, while always excluding `.tagteam/`.

`final.json` and `state.json` include machine-readable status fields such as
`status`, `degraded`, `degraded_reason`, `blocking_reason`, `role_statuses`,
`role_losses`, `budgets`, and `exit_code`. Text output prints degraded/blocking
state when present so summaries do not silently disagree with artifacts.

`blocking_reason` (and the per-role `reason_code` in `role_statuses` /
`role_losses`) draws from a fixed vocabulary: `blocking_findings` (reviewer
found blocker/major findings), `rounds_exhausted` (round limit reached with
unresolved findings), `test_failed`, `worker_timeout`, `worker_unavailable`
(coder/worker adapter unavailable or failed for a non-timeout reason),
`reviewer_unavailable`, `supervisor_unavailable`, `reviewer_json_invalid`,
`scout_unavailable`, `scout_context_too_small`, `budget_exceeded`,
`artifact_missing`, and `fallback_used`. When a run is blocked by the
invocation budget,
`budgets.exhausted` is `true` and `budgets.reason_code` is `budget_exceeded`.

Diagnostic output, delivery records, copied prompts, and raw/validation-error
artifacts redact values from sensitive shell environment keys and the scoped
`.env` overlay. Prompts, diffs, and model outputs are still persisted for
inspectability, so do not paste secrets into task prompts or source files.

## Development

Format and test:

```bash
gofmt -w main.go internal/cli/root.go internal/tagteam/*.go
go test ./...
go vet ./...
```

## Scope

This is not intended to be:

- a vendor CLI shim
- a general multi-agent framework
- a credential manager
- a raw model runner

## License

MIT
