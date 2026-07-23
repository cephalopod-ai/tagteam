# 2026-07-23: Relay topology guard and Grok doctor visibility

**Status:** completed_verified

## Input findings

1. A real relay invocation explicitly selected the Gemini 3.6 Flash scout and
   `--strict-scout`, but a supervisor advisory simplified the run to supervisor
   mode before scout execution. This bypassed the caller-selected role and made
   the strict-scout contract ineffective.
2. `App.Doctor` probed the Grok adapter but the CLI renderer omitted its result,
   so an operator could not tell whether the requested Grok worker was ready.

## Repair

- Relay simplification is now skipped when a caller explicitly selects a scout,
  sets `scout_failure_policy = "fail"` / `--strict-scout`, or uses a blocking
  scout loss policy. The saved orchestration decision records the host reason.
- Unconstrained relay runs retain the existing one-time host optimization.
- The core doctor probe and CLI renderer share one adapter list, including
  `grok`, preventing a future probe/display mismatch.

## Verification

- `go test -timeout 3m ./internal/tagteam -count=1` passed in `122.021s`.
- `go test ./internal/cli ./internal/tui -count=1` passed.
- `go vet ./...`, `gofmt -l .`, `./scripts/check-go-file-lines.sh`, and
  `git diff --check` passed.
- `go run . doctor | rg '^grok\\t'` reported the installed Grok CLI
  (`grok 0.2.111`).

## Architecture and contract gate

The authoritative behavior is the relay/strict-scout contract in `README.md`,
the host-owned orchestration boundary in `docs/ARCHITECTURE.md`, and the run
contracts in `internal/tagteam`. The pre-repair state had an implementation
gap: an automatic transition could contradict a caller-owned scout role. The
post-repair state preserves that role while retaining the bounded transition
for unconstrained runs. No public persisted schema changed and no new drift was
introduced.

## Record closure and residual risk

The source finding was a live run observation rather than an existing issue
ledger. `README.md`, `docs/ARCHITECTURE.md`, and `docs/TEST_LEDGER.md` now
describe the corrected behavior. The tests use hermetic adapters; the next
live relay run is still required to validate vendor CLI behavior with Gemini
3.6 Flash, Grok, and Sonnet 5.
