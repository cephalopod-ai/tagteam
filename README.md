# tagteam

[![CI](https://github.com/cephalopod-ai/tagteam/actions/workflows/ci.yml/badge.svg)](https://github.com/cephalopod-ai/tagteam/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.23%2B-00ADD8.svg)](go.mod)
[![Platforms](https://img.shields.io/badge/platforms-macOS%20%7C%20Linux-lightgrey.svg)](#install)

**A standalone Go CLI that runs one or more headless coding agents as one command.**

You already have coding agent CLIs installed — `claude`, `codex`, `agy`, whatever. Running two of them together, one writing code and one reviewing it, means babysitting a handoff: copy the diff, paste it into the other tool, feed the findings back, repeat. `tagteam` makes that combo a single command. You say what you want, it drives the whole back-and-forth, and it saves every brief, diff, review, and test result so you can see exactly what happened instead of trusting a vendor UI.

The multi-agent part is implicit. You don't wire up a pipeline; you pick a mode and go.

| | |
|---|---|
| **Default flow** | `supervisor` writes a brief + reviews → `worker` implements, loops until it passes |
| **Language / runtime** | Go 1.23+, single static binary |
| **Platforms** | macOS (`amd64`/`arm64`), Linux (`amd64`/`arm64`) |
| **Requires** | Git + at least one supported agent CLI already logged in |
| **License** | MIT |

## Contents

- [Highlights](#highlights)
- [What's New In v0.1.4](#whats-new-in-v014)
- [Modes](#modes)
- [Architecture at a glance](#architecture-at-a-glance)
- [Status](#status)
- [Requirements](#requirements)
- [Authentication](#authentication)
- [Compatibility issues & rough edges](#compatibility-issues--rough-edges)
- [Install](#install)
- [Quick start](#quick-start)
- [Configuration](#configuration)
- [Run artifacts](#run-artifacts)
- [TUI](#tui)
- [Development](#development)
- [Scope](#scope)
- [License](#license)

## Highlights

- **One command, not a config project.** Pick a mode and go — no pipeline to wire up.
- **Cost-shaped by design.** Put a cheap model on the grunt work and a stronger one on review, or point everything at frontier models and let them fight it out — the roles make the tradeoff explicit.
- **Transparent by default.** Roles are explicit and every brief, diff, review, and test result is written to disk under `.tagteam/runs/<run-id>/` — nothing is hidden behind a vendor UI.
- **Findings loop back automatically.** Reviewed modes feed review findings back to the editor role until the change passes, tests fail, or the round limit is reached.

> [!NOTE]
> **Why it exists:** a quick way to combine agent CLIs without configuring each one from scratch, that still fits enough situations to be worth reaching for by default.
>
> **Why it's simple:** if you're a serious coder who wants fine-grained control over every agent, `tagteam` is probably too simple for you — and that's fine.

## What's New In v0.1.4

- **Interactive TUI dashboard.** `tagteam tui [RUN_ID]` can now launch runs, switch modes/targets, and inspect recent runs from one terminal UI.
- **Better live-state observability.** In-progress runs now publish `.tagteam/active.json`, richer `state.json` updates, and a shared run snapshot surface that powers both the TUI and status-style views.
- **Opt-in JSON repair for malformed contract output.** `--repair-json-with-worker` and `json_repair = "worker"` explicitly allow the selected worker to act as a read-only parser workaround for invalid JSON artifacts; repaired runs are marked degraded with `json_repair_used`.
- **More resilient Claude-heavy setups.** The built-in `claude-failover` profile adds target-specific fallbacks from Claude supervisor/reviewer roles to Codex models when those invocations fail.
- **Documentation and release hardening.** The user manual, architecture docs, diagrams, and test ledger now describe the current command surface and run-state model.

## Modes

| Mode | Flag | Roles | Best for |
|---|---|---|---|
| **Supervisor** *(default)* | *(none needed)* | `supervisor` writes a brief + reviews (read-only by default) → `worker` implements | Zero-config default with a strong review loop |
| **Relay** | `--relay` / `--mode relay` | cheap read-only `scout` recon → write-enabled `coder` → stronger read-only `supervisor` reviews/arbitrates | Cost-aware pipeline with reconnaissance before editing |
| **Solo** | `--solo <adapter[:model]>` / `--mode solo` | one implementation agent, nothing else | Quick baselines — output explicitly reports `review=none` |
| **Adversarial** | `--mode adversarial` | `coder` implements → `adversary` reviews | Original mode, kept for backward compatibility |

> [!TIP]
> In every reviewed mode (supervisor, relay, adversarial), findings loop back into the editor role until the change passes review, tests fail, or the round limit is reached. When the limit hits with unresolved blocker/major findings, `tagteam` stops asking for edits and instead asks both agents for final "what remains incomplete / what do you dispute" reports. Solo mode runs once and never pretends to be reviewed.

## Architecture at a glance

Reviewed modes run an implement → diff → test → review loop, feeding findings back to the editor until the change passes, tests fail, or the round limit is reached:

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

Full documentation — architecture, more diagrams, and the test ledger — is indexed in [docs/INDEX.md](docs/INDEX.md).

## Status

This repository now reflects most of the `v0.1.4` CLI surface: the core run loop, adapter abstraction, persisted run artifacts, live status plumbing, and the main command set are implemented. The main rough edges are adapter-behavior issues and general ergonomics rather than missing core workflow support.

Recent additions in this repo:

- supervisor/worker mode is now the default flow
- relay scout/coder/supervisor mode is available with `--relay`
- solo mode is available with `--solo <adapter[:model]>`
- adversarial coder/adversary mode remains available for backward compatibility
- saved run artifacts include briefs, diffs, reviews, tests, and final summaries
- active runs publish `.tagteam/active.json` so live views can discover in-flight work
- command surface now includes `review`, `fix`, `status`, `plan`, `transcript`, `tui`, `doctor`, and `init`
- config layering supports repo config, user config, env overrides, flags, and named profiles
- explicit JSON repair is available through `--repair-json-with-worker` / `json_repair = "worker"`
- explicit repo instruction files are loaded by default and appended to role prompts
- machine-readable output and dry-run support make the CLI easier to script and debug

Current commands:

```
tagteam "<prompt>"
tagteam review
tagteam fix
tagteam status
tagteam plan [RUN_ID]
tagteam transcript [RUN_ID]
tagteam tui [RUN_ID]
tagteam doctor
tagteam init
```

## Requirements

- Go 1.23+
- Git
- At least one supported agent CLI on `PATH`

> [!IMPORTANT]
> The selected `--workdir` (the current directory, if unset) must itself be a Git repo with at least one commit — `tagteam` runs `git rev-parse --verify HEAD` there during preflight and uses that commit as the diff baseline for every round. A folder that only *contains* repos (e.g. a workspace directory with several projects as subfolders) does not qualify; point `--workdir` at the actual repo, not a parent above it. Failing this check exits with `workdir is not a git repo or has no HEAD`.

Supported adapters in this repo today:

| Adapter | Role support |
|---|---|
| `codex` | full |
| `codex-oss` | full |
| `claude` | full |
| `agy` | full |
| `gosling` | coder-only |
| `openai-compatible` / `oai` | read-only reviewer/scout (first cut) |

## Authentication

Each vendor CLI adapter (`codex`, `claude`, `agy`, `gosling`, etc.) must already be logged in on your machine before you run `tagteam`. `tagteam` does not run vendor login flows, store credentials, or proxy/inject API keys for those CLIs. If an adapter is not authenticated, the run will fail with that CLI's own auth error.

This note applies to the vendor CLI adapters; the separate `openai-compatible` adapter uses its documented `api_key_env` setting.

> [!NOTE]
> `tagteam` also reads a repo-local `.env` file from the selected workdir as a scoped overlay. It does not mutate the global process environment; exported shell variables still take precedence, and `.env` values are passed only to tagteam's config resolver and invoked adapters/tests. A starter template is included as [`.env_template`](.env_template).

## Compatibility issues & rough edges

> [!WARNING]
> `tagteam` depends on third-party agent CLIs and compatible HTTP backends whose behavior can change without warning. Expect some adapter-specific rough edges, especially as upstream tools evolve.

<details>
<summary><strong>Current caveats</strong></summary>

- Vendor CLI flag drift can break adapters. `codex`, `codex-oss`, `claude`, `agy`, `gosling`, and similar tools may rename flags, change output formats, or alter auth behavior between releases.
- Authentication is adapter-specific. CLI-backed adapters usually rely on the vendor's own login/session flow; `openai-compatible` / `oai` relies on explicit environment/config values.
- Supervisor slicing is more format-sensitive than the final review pass. The final review path is schema-validated, but some supervisor planning/instruction steps still depend on adapter output being reasonably well-formed.
- Claude Code can occasionally ignore `--output-format json` / `--json-schema` during supervisor review and return prose such as `Review...` instead of the expected JSON envelope. `tagteam` retries once and can recover when a valid review JSON object is embedded in the prose; if no valid JSON is present, the run exits as an adapter/output-contract failure and preserves the invalid output artifacts for inspection. If you intentionally want the already-selected worker to act as a read-only parser workaround, pass `--repair-json-with-worker` or set `json_repair = "worker"`; repaired runs are marked degraded with `json_repair_used`.
- Different adapters do not expose identical capabilities. Some support schema-constrained output, stdin, or session resume; others do not. `tagteam` degrades where possible, but behavior is not perfectly uniform.
- Local `.env` loading is a convenience feature, not a secret-management system. It helps with local runs, but shell-exported environment variables still take precedence.
- Repo-local `.tagteam.toml` is partially trusted by default: low-authority defaults such as roles/models can be read, but shell tests, adapter passthrough args, Claude permission/tool widening, and `openai-compatible` endpoints/headers are ignored unless you pass `--trust-repo-config`.
- Published binaries are broader than real-world manual validation. Releases may include targets that pass Go-level CI but have not been exercised end-to-end with every supported vendor CLI.

</details>

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

Or download a prebuilt archive for your platform from GitHub Releases, then put the `tagteam` binary on your `PATH`.

Binary releases are published for:

- macOS (`darwin/amd64`, `darwin/arm64`)
- Linux (`linux/amd64`, `linux/arm64`)

> [!NOTE]
> Windows is not validated. The test suite relies on POSIX shell adapters, so `tagteam` is only exercised and released on macOS and Linux. It may well build and run on Windows — if you get it working and verify it, open an issue or PR and I'm more than happy to add Windows back to CI and releases.

Create a release by pushing a tag such as `v0.1.4`; GitHub Actions runs Go checks on macOS and Linux, then GoReleaser attaches archives plus `checksums.txt` to the release.

Build from source:

```bash
go build -o tagteam .
```

Run locally:

```bash
go run . "add OAuth login"
```

## Quick start

Default run (supervisor mode, Claude Sonnet 5 worker at high effort and GPT-5.6 Sol supervisor at high reasoning effort):

```bash
tagteam "add OAuth login"
```

That's the whole thing — no flags, no config. From your repo root you just describe the change:

```bash
cd my-project
tagteam "add a --json flag to the export command and cover it with a test"
```

With no other options, `tagteam` uses the default supervisor mode: `codex:gpt-5.6-sol` writes a brief and reviews, `claude:claude-sonnet-5` implements, and findings loop back until the change passes review, tests fail, or the round limit is hit. Every brief, diff, review, and test run is written under `.tagteam/runs/<run-id>/`, and the final verdict prints to the terminal. Run `tagteam status` afterward to see the latest run, or `tagteam doctor` first if you're not sure your agent CLIs are set up.

Supervisor mode slices work by default before the worker edits. The supervisor writes a bounded work plan, selects one package, and the worker implements only that package. If packages remain, `tagteam` stops after the selected package passes and reports the next packages unless `--auto-next-package` is set.

```bash
tagteam --slice --max-packages 5 --package P1 "add OAuth login"
```

Choose explicit worker/supervisor adapters, rounds, and a test command:

```bash
tagteam \
  --worker claude:claude-sonnet-5 \
  --supervisor codex:gpt-5.6-sol \
  -r 3 \
  -t "go test ./..." \
  "refactor billing flow"
```

> [!TIP]
> The supervisor is read-only by default (it writes the brief and review findings but does not edit files). Allow it to make small exploratory edits with `--supervisor-can-edit`.

Supervisor slicing also creates a run checklist. `plan.json` records package status, and `plan-events.jsonl` records status transitions and review-added items. `tagteam status` shows the latest checklist when present; use `tagteam plan [RUN_ID]` to print a run's checklist directly.

Supervisor and relay runs may perform one bounded orchestration adjustment before implementation starts. Agents can emit compact advisory signals, but `tagteam` owns the decision: relay may simplify to supervisor mode for small tasks, and supervisor mode may escalate to relay only when the worker reports insufficient context and the supervisor agrees. There is no back-and-forth replanning loop.

### Solo mode

Solo mode runs exactly one implementation agent and no reviewer. It is useful as a quick baseline for comparing cost, speed, and quality against supervisor, relay, or adversarial runs.

```bash
tagteam --solo codex:gpt-5.6-terra "rename UserSvc to UserService"
tagteam --mode solo --worker claude:claude-sonnet-5 -t "go test ./..." "make a small README edit"
```

> [!IMPORTANT]
> In solo mode, legacy `-mc` and preferred `--worker` both select the implementation agent. Reviewer flags such as `-ma`, `--reviewer`, and `--supervisor` are invalid.

### Relay mode

Relay mode runs a cost-aware three-agent pipeline: read-only scout reconnaissance, supervisor brief, supervisor-condensed worker instructions, coder implementation, deterministic diff capture, tests, post-implementation scout advisory pass, and strict supervisor review.

```bash
tagteam --relay "add OAuth login"
```

For small tasks, relay can simplify to supervisor mode before scout runs when the supervisor advises that the direct worker/review path is enough. The host records the decision and skips scout-heavy relay setup; the supervisor review remains the authoritative gate.

Relay mode is a full-run workflow. It does not currently have a review-only variant: `tagteam review` remains adversary-only and does not run scout or supervisor relay steps.

The built-in relay profile uses:

```toml
[profiles.relay]
mode = "relay"
scout = "agy:Gemini 3.5 Flash (Medium)"
coder = "claude:claude-sonnet-5"
supervisor = "codex:gpt-5.6-sol"
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
  --scout 'agy:Gemini 3.5 Flash (Medium)' \
  --scout-mode recon \
  --post-scout-mode polish \
  --coder claude:claude-sonnet-5 \
  --supervisor codex:gpt-5.6-sol \
  "refactor billing flow"
```

In relay mode, legacy `-mc` selects the coder and `-ma` selects the supervisor. Scout modes are task-typed: `recon`, `lint`, `polish`, `tests`, or `risk`. Scout findings are advisory context only; only the supervisor review can fail a run with blocker/major findings.

> [!TIP]
> For relay mode, the scout should have a strong context window — a practical recommendation is `256k` or more, and ideally at least as much context as the relay coder and supervisor. Small-context scouts tend to lose most of the benefit of relay reconnaissance once repo instructions, retrieval evidence, and task context are included.

<details>
<summary><strong>Advanced relay configuration: retrieval, context budgets, failure policy, fallbacks</strong></summary>

Relay pre-scout `recon` uses bounded local retrieval by default before the scout model runs. Retrieval is host-owned, local-only, advisory, and does not use embeddings, network search, persistent indexes, daemons, or background caches. It writes `retrieval-round-1.json` with status/evidence metadata, then passes only a compact bounded summary into the scout prompt. Disable this layer with `--no-scout-retrieval`:

```bash
tagteam --relay --no-scout-retrieval "add OAuth login"
```

Retrieval runs only for relay pre-scout `scout_mode = "recon"` and never for post-scout, supervisor mode, adversarial mode, or solo mode. If `rg` is missing, retrieval times out, or no useful matches are found, tagteam records that status and continues with normal scout reconnaissance. Configure it with `scout_retrieval = true|false` or `TAGTEAM_SCOUT_RETRIEVAL=false`; flags still have highest precedence.

If an adapter has explicit context limits configured, relay pre-scout `recon` also writes `scout-context-round-1.json` before calling the scout. The check is deterministic and conservative (`ceil(prompt_bytes/3)`), not provider metadata. Statuses are `unknown`, `ok`, `near_limit`, or `exceeds_limit`. Near-limit runs compact retrieval more aggressively; retrieval is disabled if it alone would push the scout prompt over the configured usable context. Use `scout_context_policy = "warn" | "skip" | "block"` or `--scout-context-policy` to decide whether a too-small configured scout context only warns, skips/degrades the scout pass, or blocks before scout invocation.

Scout model failures are explicit and configurable. By default, `scout_failure_policy = "continue"` warns, writes `scout-execution-round-1.json`, and continues without scout context so the coder and supervisor can still run. Use `--strict-scout` or `scout_failure_policy = "fail"` when evaluation or reproducibility should abort before coder edits if the scout invocation, scout JSON contract, or scout context-budget check fails. Retrieval unavailable/timeout/empty/degraded states are separate from scout model failure and continue into the scout pass where possible.

For finer control, `loss_policy` can be configured per non-primary role: `block`, `degrade`, `replace_then_block`, or `replace_then_degrade`. Replacement is bounded: tagteam may replace an unavailable target during preflight, and reviewer/supervisor review calls may try the configured fallback chain once after an invocation or output-contract failure. Fallback chains are ordered, deduped, capped at five targets, and recorded in `final.json`.

The built-in `claude-failover` profile enables a small Claude-to-Codex fallback ladder for review/supervision failures:

```bash
tagteam -P claude-failover --mode adversarial --reviewer claude:claude-opus-4-8 "fix the bug"
```

It maps the current Opus target to `codex:gpt-5.6-sol` and the current Sonnet target to `codex:gpt-5.6-terra`; compatibility aliases for older Claude CLI model names remain available. Target-specific fallbacks run before role-level fallback lists.

</details>

### Adversarial mode (backward compatible)

The original coder/adversary loop is still available via `--mode adversarial`. The legacy `-mc`/`-ma` flags keep working and map onto the active mode's roles: `-mc` selects the worker in supervisor mode and the coder in adversarial mode; `-ma` selects the supervisor in supervisor mode and the adversary in adversarial mode.

```bash
tagteam --mode adversarial \
  -mc codex:gpt-5.6-terra \
  -ma claude:claude-opus-4-8 \
  -r 3 \
  -t "go test ./..." \
  "refactor billing flow"
```

`--reviewer` is an adversarial-mode-flavored alias for `-ma`/`--supervisor`:

```bash
tagteam --mode adversarial -mc agy --reviewer claude:claude-sonnet-5 "clean up the CLI help"
```

Use Agy with its configured default Gemini model:

```bash
tagteam --worker agy --supervisor codex:gpt-5.6-sol "clean up the CLI help"
```

The built-in `agy` default model is `Gemini 3.5 Flash (Medium)`; override it with `agy:<model>`.

### OpenAI-compatible reviewers

<details>
<summary><strong>Featherless.ai, OpenRouter, and other <code>/chat/completions</code> gateways</strong></summary>

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
  -mc claude:claude-sonnet-5 \
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

</details>

Review the current diff only:

```bash
tagteam review --fail-on-review
```

Apply fixes from the latest saved review:

```bash
tagteam fix
```

## Configuration

> [!NOTE]
> **Precedence:** `flags > shell TAGTEAM_* env > workdir .env TAGTEAM_* overlay > repo .tagteam.toml > user config > built-in defaults`

If a `.env` file exists in the selected workdir, `tagteam` parses it as a small, line-oriented dotenv subset: `KEY=VALUE`, optional `export`, inline comments outside quotes, single-quoted raw values, and double-quoted escape sequences such as `\n`. `.env` is a convenience source for local development; it is not a full shell parser, and explicit shell exports still win.

Repo-local `.tagteam.toml` is loaded in untrusted mode by default. It can set ordinary role/model defaults, but high-authority settings such as `defaults.test`, `git_safety`, adapter `extra_args`, Claude `coder_allowed_tools` / `bare`, and `openai-compatible` `base_url`, `api_key_env`, `extra_headers`, or `extra_args` require `--trust-repo-config`.

User config path:

- macOS/Linux: `~/.config/tagteam/config.toml`

Starter config:

```bash
tagteam init
```

<details>
<summary><strong>All <code>defaults</code> keys</strong></summary>

| Key | Meaning |
|---|---|
| `mode` | `supervisor` (default), `solo`, `adversarial`, or `relay` |
| `worker` | `adapter[:model]` target used in solo mode, and in supervisor mode alongside `supervisor` |
| `supervisor` | `adapter[:model]` target used in supervisor mode, and in relay mode alongside `scout`/`coder` |
| `coder` / `adversary` | `adapter[:model]` targets used in adversarial mode |
| `relay_coder` | relay-specific implementation target; falls back to `coder` for older configurations |
| `scout` | `adapter[:model]` target used in relay mode |
| `scout_mode` / `post_scout_mode` | relay scout task modes: `recon`, `lint`, `polish`, `tests`, or `risk` |
| `scout_retrieval` | enable bounded local retrieval for relay pre-scout `recon` (default `true`; disable with `--no-scout-retrieval` or `TAGTEAM_SCOUT_RETRIEVAL=false`). Relay scouts work best with `256k+` context and ideally at least as much context as the relay coder/supervisor |
| `scout_failure_policy` | relay scout model failure handling: `continue` (default) or `fail`; `--strict-scout` maps to `fail`, and `TAGTEAM_SCOUT_FAILURE_POLICY` can override config |
| `scout_context_policy` | relay scout configured-context behavior: `warn` (default), `skip`, or `block`; `--scout-context-policy` overrides it |
| `supervisor_slicing` | split supervisor-mode work into bounded packages before implementation |
| `max_packages` | maximum package count for supervisor slicing |
| `package` | selected package ID to execute from the work plan |
| `auto_next_package` | continue into additional packages while the normal round cap allows it |
| `respect_repo_instructions` | load explicit repo instruction files and append them to role prompts |
| `rounds` | hard cap on implementation/review cycles; exhausted runs stop and collect final reports from both agents |
| `max_role_invocations` | optional hard cap on adapter calls in one run; `--max-role-invocations` overrides it |
| `json_repair` | explicit JSON contract repair mode: `off` (default) or `worker`; `--repair-json-with-worker` enables the selected worker as a read-only parser for invalid JSON artifacts |
| `test`, `git_safety` | test command and git safety settings |

</details>

Profiles may override `mode`, `scout`, `scout_mode`, `scout_retrieval`, `scout_failure_policy`, `scout_context_policy`, `loss_policy`, `fallbacks`, `fallbacks_by_target`, `json_repair`, `post_scout_mode`, `worker`, `supervisor`, `coder`, `adversary`, `rounds`, and `test`. A profile that sets `coder`/`adversary` but omits `mode` resolves as an adversarial-mode profile, so profiles written before `mode` existed keep working unchanged:

```toml
[defaults]
mode = "supervisor"
worker = "claude:claude-sonnet-5"
supervisor = "codex:gpt-5.6-sol"
coder = "codex:gpt-5.6-terra"
relay_coder = "claude:claude-sonnet-5"
adversary = "claude:claude-opus-4-8"
scout = "agy:Gemini 3.5 Flash (Medium)"
supervisor_slicing = true
max_packages = 5
rounds = 2
json_repair = "off"

[profiles.relay.loss_policy]
scout = "replace_then_degrade"
supervisor = "block"

[profiles.relay.fallbacks]
scout = ["openai-compatible:gpt-oss-120b"]
supervisor = ["claude:claude-opus-4-8"]

[profiles.claude-failover.loss_policy]
reviewer = "replace_then_block"
supervisor = "replace_then_block"

[profiles.claude-failover.fallbacks_by_target]
"claude:claude-opus-4-8" = ["codex:gpt-5.6-sol"]
"claude:claude-sonnet-5" = ["codex:gpt-5.6-terra"]
"claude:haiku" = ["codex:gpt-5.6-terra"]

[profiles.fast]
coder = "codex:gpt-5.6-terra"
adversary = "claude:claude-sonnet-5"
rounds = 1
```

Adapter configs may optionally declare deterministic context budgets used by relay pre-scout `recon`:

```toml
[adapters.openai_compatible]
base_url = "https://api.example.test/v1"
api_key_env = "EXAMPLE_API_KEY"
max_context_tokens = 32768
reserved_output_tokens = 2048
```

Codex and Claude inference effort is configured separately from model identity, following the same provider/model/settings separation used by mature agent CLIs:

```toml
[adapters.codex]
reasoning_effort = "high"

[adapters.claude]
effort = "high"
```

The equivalent environment variables are `TAGTEAM_CODEX_REASONING_EFFORT` and `TAGTEAM_CLAUDE_EFFORT`.

For `openai-compatible`, environment overrides are `TAGTEAM_OPENAI_COMPATIBLE_MAX_CONTEXT_TOKENS` and `TAGTEAM_OPENAI_COMPATIBLE_RESERVED_OUTPUT_TOKENS`. Omitted limits mean `unknown` and preserve existing relay behavior.

Repo instructions are loaded from the selected workdir, then from the Git root when different, in this exact file order: `AGENTS.md`, `agent.md`, `.tagteam/AGENTS.md`, `.codex/AGENTS.md`, `.claude/AGENTS.md`, `.agy/AGENTS.md`. Only those exact files are read; vendor skill/plugin directories are not recursively ingested. Disable this layer with `--no-repo-instructions`.

## Run artifacts

Each run writes artifacts under:

```text
.tagteam/runs/<run-id>/
```

While a run is in progress, `.tagteam/active.json` points at it:

```json
{
  "schema_version": 1,
  "run_id": "...",
  "run_dir": ".tagteam/runs/...",
  "state_path": ".tagteam/runs/.../state.json",
  "final_path": ".tagteam/runs/.../final.json",
  "mode": "supervisor",
  "status": "running",
  "started_at": "...",
  "updated_at": "..."
}
```

`active.json` exists only while the run is running. Once the run finishes it is either removed (the common case) or, if the process aborted through an error path after the run directory was created, left behind with `status: "failed"` so the run directory can still be inspected. `.tagteam/latest.json` is unaffected by this and keeps its existing meaning: the most recent run that reached `final.json`.

<details>
<summary><strong>Typical contents</strong></summary>

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

</details>

Diff artifacts are captured through a temporary Git index, not the real staging area. The canonical patch includes tracked changes, deletions, renames, binary patches, and untracked files, while always excluding `.tagteam/`.

`final.json` and `state.json` include machine-readable status fields such as `status`, `degraded`, `degraded_reason`, `blocking_reason`, `role_statuses`, `role_losses`, `budgets`, and `exit_code`. Text output prints degraded/blocking state when present so summaries do not silently disagree with artifacts.

<details>
<summary><strong><code>blocking_reason</code> vocabulary</strong></summary>

`blocking_reason` (and the per-role `reason_code` in `role_statuses` / `role_losses`) draws from a fixed vocabulary:

| Reason code | Meaning |
|---|---|
| `blocking_findings` | reviewer found blocker/major findings |
| `rounds_exhausted` | round limit reached with unresolved findings |
| `test_failed` | tests failed |
| `worker_timeout` | worker/coder adapter timed out |
| `worker_unavailable` | coder/worker adapter unavailable or failed for a non-timeout reason |
| `reviewer_unavailable` | reviewer adapter unavailable |
| `supervisor_unavailable` | supervisor adapter unavailable |
| `reviewer_json_invalid` | reviewer output failed schema validation |
| `scout_unavailable` | relay scout adapter unavailable |
| `scout_context_too_small` | relay scout's configured context can't fit the prompt |
| `budget_exceeded` | invocation budget exhausted (`budgets.exhausted` is `true`) |
| `artifact_missing` | an expected run artifact was not produced |
| `fallback_used` | a configured fallback target was used in place of the primary |

</details>

> [!CAUTION]
> Diagnostic output, delivery records, copied prompts, and raw/validation-error artifacts redact values from sensitive shell environment keys and the scoped `.env` overlay. Prompts, diffs, and model outputs are still persisted for inspectability, so do not paste secrets into task prompts or source files.

## TUI (not yet human tested)

```bash
tagteam tui [RUN_ID]
```

`tagteam tui` is an interactive dashboard. It opens in a sparse compose-first state by default, surfaces the latest known run as context, and lets you explicitly open saved run details when you want them.

The dashboard has three main surfaces:

- a compose surface for prompt entry and run launch
- an on-demand settings panel for mode, targets, profiles, rounds, tests, and core toggles
- a scrollable run-detail view showing status, roles, plan items, findings, changed files, and artifact paths

It polls the run directory once a second while a run is active and can also launch a new run directly through the same config/runner path as the normal CLI.

With no `RUN_ID`, it still discovers the active run (`.tagteam/active.json`, if `status` is `running`) and the most recent completed run (`.tagteam/latest.json`) so the dashboard can show context and open them on demand. Passing `RUN_ID` opens that run immediately.

Core keyboard affordances:

- `j` / `k` or arrows move selection and scroll
- `g` launches the composed run
- `/` opens slash commands such as `/run`, `/profile relay`, `/mode relay`, `/model claude:claude-sonnet-5`, `/supervisor codex:gpt-5.6-sol`, `/watch latest`, `/scout-retrieval off`
- `s` opens settings
- `u` opens recent runs
- `r` refreshes
- `p`, `f`, `a`, and `t` toggle detail sections
- `q` quits

The run-detail surface still reads `active.json`, `state.json`, `final.json`, and `plan.json` from the run directory (the same sources `RunSnapshot` assembles for `tagteam status --json`), but the compose surface is intentionally primary and can invoke adapters through the normal runner path.

## Development

Format and test:

```bash
gofmt -w main.go internal/cli/*.go internal/tagteam/*.go internal/tui/*.go
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
