# tagteam

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
- machine-readable output and dry-run support make the CLI easier to script and debug

Current commands:

- `tagteam "<prompt>"`
- `tagteam review`
- `tagteam fix`
- `tagteam status`
- `tagteam transcript [RUN_ID]`
- `tagteam doctor`
- `tagteam init`

## Requirements

- Go 1.22+
- Git
- At least one supported agent CLI on `PATH`

Supported adapters in this repo today:

- `codex`
- `codex-oss`
- `claude`
- `agy`
- `gosling` (coder-only)
- `openai-compatible` / `oai` (review-only first cut)

## Authentication

Each vendor CLI adapter (`codex`, `claude`, `agy`, `gosling`, etc.) must already be logged in on your machine before you run `tagteam`. `tagteam` does not run vendor login flows, store credentials, or proxy/inject API keys for those CLIs. If an adapter is not authenticated, the run will fail with that CLI's own auth error.

This note applies to the vendor CLI adapters; the separate `openai-compatible` adapter uses its documented `api_key_env` setting.

## Compatibility Risks

`tagteam` depends on third-party agent CLIs whose command-line contracts are not stable. Flags, output formats, auth flows, and model-selection syntax may change upstream without warning.

That means adapters in this repository can break when tools like `codex`, `claude`, `agy`, `codex-oss`, or `gosling` change their flags or behavior. Expect periodic adapter maintenance as those CLIs evolve.

## Install

Download a prebuilt archive for your platform from GitHub Releases, then put
the `tagteam` binary on your `PATH`.

Binary releases are published for:

- macOS (`darwin/amd64`, `darwin/arm64`)
- Linux (`linux/amd64`, `linux/arm64`)
- Windows (`windows/amd64`, `windows/arm64`)

Platform note: the developer has only tested `tagteam` on macOS 26 Tahoe and
Ubuntu. Other targets may build and release successfully but should be treated
as unverified until they are exercised in real use.

Create a release by pushing a tag such as `v0.1.0`; GitHub Actions runs
GoReleaser and attaches archives plus `checksums.txt` to the release.

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

The built-in relay profile uses:

```toml
[profiles.relay]
mode = "relay"
scout = "agy:gemini-3.5-flash-low"
coder = "codex:gpt-5.4-mini"
supervisor = "claude:sonnet"
scout_mode = "recon"
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

`openai-compatible` adds a small HTTP adapter for OpenAI-compatible `/chat/completions` APIs such as Featherless.ai, OpenRouter, and local gateways. This first cut is review-only: use it as the adversary/reviewer, not as the coder/worker.

Featherless.ai:

```toml
[adapters.openai_compatible]
base_url = "https://api.featherless.ai/v1"
api_key_env = "FEATHERLESS_API_KEY"
default_model = "gpt-oss-120b"
```

```bash
FEATHERLESS_API_KEY=... tagteam \
  --mode adversarial \
  -mc claude:sonnet \
  -ma openai-compatible:gpt-oss-120b \
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

`flags > TAGTEAM_* env > repo .tagteam.toml > user config > built-in defaults`

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
- `supervisor_slicing` — split supervisor-mode work into bounded packages before implementation
- `max_packages` — maximum package count for supervisor slicing
- `package` — selected package ID to execute from the work plan
- `auto_next_package` — continue into additional packages while the normal round cap allows it
- `rounds` — hard cap on implementation/review cycles; exhausted runs stop and collect final reports from both agents
- `test`, `git_safety`

Profiles may override `mode`, `scout`, `scout_mode`, `post_scout_mode`, `worker`, `supervisor`, `coder`, `adversary`, `rounds`, and `test`. A profile that sets `coder`/`adversary` but omits `mode` resolves as an adversarial-mode profile, so profiles written before `mode` existed keep working unchanged:

```toml
[defaults]
mode = "supervisor"
worker = "agy:Gemini 3.5 Flash (High)"
supervisor = "claude:opus"
supervisor_slicing = true
max_packages = 5
rounds = 2

[profiles.fast]
coder = "codex:gpt-5-codex-mini"
adversary = "claude:haiku"
rounds = 1
```

## Run Artifacts

Each run writes artifacts under:

```text
.tagteam/runs/<run-id>/
```

Typical contents include:

- `meta.json`
- `input.md`
- `solo-round-1.md` (solo mode)
- `supervisor-work-plan.json` (supervisor mode with slicing)
- `supervisor-brief.md` (supervisor or relay mode, round 1)
- `scout-round-1.json` (relay mode)
- `supervisor-instructions.md` (relay mode)
- `worker-round-N.md` (supervisor mode) / `coder-round-N.md` (adversarial or relay mode)
- `diff-round-N.patch`
- `diff-round-N.numstat`
- `diff-round-N.files.json`
- `diff-round-N.sha256`
- `test-round-N.txt`
- `post-scout-round-N.json` (relay mode)
- `supervisor-round-N.json` (supervisor mode) / `adversary-round-N.json` (adversarial mode) / `supervisor-review-round-N.json` (relay mode)
- `worker-final-report.md` / `coder-final-report.md` and `supervisor-final-report.md` / `adversary-final-report.md` when the round limit is exhausted
- `final.json`

Diff artifacts are captured through a temporary Git index, not the real staging
area. The canonical patch includes tracked changes, deletions, renames, binary
patches, and untracked files, while always excluding `.tagteam/`.

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
