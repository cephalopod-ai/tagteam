# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Authoritative instructions

`AGENTS.md` is the canonical repo-wide contract for coding agents and wins on
any conflict with this file. Read it before non-trivial changes — it defines
the working posture (minimal diffs, inspect before modifying, no fake/stub
implementations, precise completion language) that this repo expects.

## What this project is

`tagteam` is a standalone Go CLI (Go 1.23+, single static binary, no CGO) that
orchestrates one or more headless coding-agent CLIs (`claude`, `codex`, `agy`,
`gosling`, `openai-compatible`) through an implement → diff → test → review
loop. It is an orchestration CLI, not a vendor CLI shim — avoid cloning vendor
flag surfaces unless `tagteam` owns the concept.

## Commands

```bash
go build ./...                                  # build
go test ./...                                   # all tests
go test ./internal/tagteam -run TestName        # single test
go vet ./...                                    # vet (CI-enforced)
gofmt -w main.go internal/cli/*.go internal/tagteam/*.go internal/tui/*.go   # format
scripts/check-go-file-lines.sh                  # enforce 800-line-per-file cap
```

CI (`.github/workflows/ci.yml`, ubuntu + macos) fails on: unformatted files
(`gofmt -l .` must be empty), any `.go` file over 800 lines, `go test ./...`
failures, and `go vet ./...` findings. Run all four locally before declaring
a change complete.

## Architecture

Strict one-way dependency chain: `main.go` → `internal/cli` → `internal/tagteam`.
`internal/tui` reads run state and invokes `App.Run` from `internal/tagteam`.
No reverse dependencies. Symbols exposed to the CLI layer go through
`internal/tagteam/cli_exports.go`.

- **`internal/cli`** — cobra command surface (`run`, `resume`, `transfer`,
  `findings defer`, `verify-install`, `tui`, `status`, `doctor`, ...), flag
  parsing, output formatting.
- **`internal/tagteam`** — everything else: the `App` run loop (`runner.go` +
  `runner_partNN.go`), layered config resolution (`config*.go`; precedence is
  flags > shell `TAGTEAM_*` env > workdir `.env` overlay > repo `.tagteam.toml`
  > user config > defaults), the `Adapter` interface and registry
  (`adapters*.go`), JSON contracts and exit codes (`types*.go`, `schema.go`),
  artifact persistence (`artifact_store.go`, `durable_io.go`), resilience
  (`state_machine.go`, `run_lock*.go`, `recovery.go`, `resume*.go`), quality
  gates (`quality_gates.go`, `findings.go`, `transfer.go`), prompts
  (`prompts.go`), secret redaction (`redact.go`), and live-status snapshots
  (`snapshot.go`).
- **`internal/tui`** — interactive dashboard; consumes `RunSnapshot` rather
  than parsing `final.json`/`state.json` directly. New live-status consumers
  should do the same.

**Run modes:** `supervisor` (default: supervisor briefs/reviews, worker
implements), `relay` (scout → coder → supervisor), `solo` (one agent, no
review), `adversarial` (legacy: coder + adversary). In reviewed modes, review
findings loop back to the editor role until pass, test failure, or round
limit; on exhaustion both agents produce final "what remains / disputed"
reports.

**Persistence:** authoritative run artifacts (briefs, diffs, reviews, tests,
`final.json`, `state.json`, `plan.json`, `active.json`) live externally under
`~/.local/state/tagteam/<repo-id>/runs/<run-id>/`; the only in-worktree
runtime pointer is `.tagteam/repo.json`. Diffs are captured via a temporary
Git index and always exclude `.tagteam/`.

**Extension points:** new adapter → implement `Adapter` and register in
`Registry`; new mode/role → extend `Mode`/`Role` and run-loop dispatch; new
reason code → extend `ReasonCode` and the classifiers in `run_state.go`.

## Conventions

- **800-line hard cap per `.go` file** (CI-enforced). When a file grows past
  it, split into `<name>_partNN.go` siblings in the same package — this is the
  established pattern (`runner_part02.go`–`runner_part08.go`,
  `config_part02.go`–`config_part04.go`, etc.), not new sub-packages.
- Keep dependencies minimal: currently only cobra/pflag, BurntSushi/toml,
  google/shlex, golang.org/x/term. The only network client is the
  `openai-compatible` adapter; vendor CLIs run as subprocesses with their own
  auth. Non-coder roles run under a restricted env (see
  `mergeRestrictedCommandEnv`).
- When changing adapters: update argv-construction tests, preserve clear
  preflight failures for missing/unrunnable CLIs, and document new config or
  examples in `README.md`.
- Changes affecting prompts, run artifacts, or config resolution need focused
  tests for those paths.
- One coherent task per change; no unrelated cleanup bundled in.
- Never silently degrade: report partial success, skipped checks, and use
  AGENTS.md's completion language (e.g. "runtime-verified for the tested
  path", not "fully working").

## Documentation map

`docs/INDEX.md` is the entry point. `README.md` is the user manual (modes,
config keys, run artifacts, reason codes); `docs/ARCHITECTURE.md` maps every
component to its files; `docs/TEST_LEDGER.md` tracks test areas and known
gaps — update it when test coverage materially changes.
