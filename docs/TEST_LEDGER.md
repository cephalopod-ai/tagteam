# Test Ledger

Derived from the current test files and a local validation run on 2026-07-10.
327 test functions across 32 files (large suites are mechanically split to
keep every Go source file within the 800-line gate).

| Test Area | Command / Evidence | Last Known Result | Source | Coverage Meaning | Gaps |
|---|---|---|---|---|---|
| Run loop / modes | `go test ./internal/tagteam/` (`runner_test.go`, 85 tests) | pass | `runner_test.go` | Supervisor/relay/solo/adversarial flows, slicing, round limits, env policy, artifacts, fallback/repair paths | Large surface still concentrated in one file |
| Config resolution | `go test ./internal/tagteam/` (`config_test.go`, 61 tests) | pass | `config_test.go` | Layered precedence, profiles, `ResolveOptions`, JSON repair/fallback config, per-role policy | — |
| Adapters | `go test ./internal/tagteam/` (`adapters_test.go`, 35 tests) | pass | `adapters_test.go` | argv construction, capabilities, schema wiring, per-role env/sandbox | — |
| Claude output hardening | `go test ./internal/tagteam/` (`adapters_part02_test.go`, 10 tests) | pass | `adapters_part02_test.go` | Claude envelope `is_error`/`error_*` surfacing, embedded-JSON candidate recovery (fenced blocks, decoy objects, unmatched braces) across worker/review contracts | Real Claude prose variants are sampled, not exhaustive |
| Claude invocation serialization | `go test ./internal/tagteam/` (`invocation_lock_test.go`, 6 tests) | pass | `invocation_lock_test.go`, `internal/tui/render_test.go` | flock-based cross-process lock acquire/release, live-holder wait with the onWait live-status callback, fail-closed timeout/cancellation, state-root derivation, a re-exec multi-process mutual-exclusion test, and TUI rendering of the queued `waiting` state | Windows PID-file fallback path is compile-checked, not executed, in CI |
| Run state / reasons | `go test ./internal/tagteam/` (`run_state_test.go`, 5 tests) | pass | `run_state_test.go` | Failure classification, exit→reason, budget wiring, overlay redaction | — |
| External state / lifecycle | `go test ./internal/tagteam/` | pass | `artifact_store_test.go`, `active_run_test.go`, `testmain_test.go` | Repository identity, isolated external state, active/latest lifecycle, legacy reconciliation, and pointer-publication fault recovery | — |
| Resilience / integrity | `go test ./internal/tagteam/` | pass | `integrity_test.go`, runner tests | Read-only mutation rejection, protected pointer restoration, dirty-worktree checkpoint branches, durable streams/contracts, recovery/state paths | Real vendor timeout behavior remains adapter-dependent |
| Quality gates / findings | `go test ./internal/tagteam/` | pass | `findings_test.go`, runner tests | Persistent major findings, evidence disposition, operator deferral, scope/churn/regression wiring | Transfer uses host-only Git fixtures rather than vendor agents |
| Snapshot / live status | `go test ./internal/tagteam/` (`snapshot_test.go`, 11 tests) | pass | `snapshot_test.go` | `RunSnapshot` assembly from `active.json`, `state.json`, `final.json`, `plan.json`; active-before-latest CLI resolution; compatibility regressions | — |
| TUI | `go test ./internal/tui/` | pass | `internal/tui/*_test.go` | Compose/config resolution, role/profile selection, scoped paths, lint/timeouts, bounded overlays, detail scrolling, command completion, split terminal input, non-interactive behavior, and missing-run handling | Automated coverage does not execute a full vendor-backed launch in a real terminal |
| CLI | `go test ./internal/cli/` | pass | `internal/cli/*_test.go` | Command wiring, live status rendering, TUI run resolution, completed-run command behavior | General CLI layer coverage remains thinner than runner coverage |
| Redaction | `go test ./internal/tagteam/` (`redact_test.go`, 6 tests) | pass | `redact_test.go` | Overlay-aware secret scrubbing | — |
| Scout context budget | `go test ./internal/tagteam/` (`context_budget_test.go`, 4 tests) | pass | `context_budget_test.go` | Deterministic estimate + policy | — |
| Scout retrieval | `go test ./internal/tagteam/` (`retrieval_test.go`, 6 tests) | pass | `retrieval_test.go` | Bounded local retrieval evidence | — |
| Formatting | `gofmt -l .` | pass (empty) | CI | CI gate | — |
| Vet | `go vet ./...` | pass | CI | CI gate | — |
| Build | `go build ./...` | pass | local validation | Buildability across packages | — |
| Full suite | `go test ./...` | pass | local validation | End-to-end package-level regression signal | Does not exercise real vendor CLIs |

## Known gaps

- The TUI suite covers the key decoder and state transitions, and this repair
  campaign included a manual TTY smoke check. There is still no automated
  full-screen terminal integration harness.
- CLI integration coverage is still lighter than orchestration coverage; most
  behavioral confidence lives in `internal/tagteam`.
- The test suite uses fake adapters and local fixtures. It validates host
  orchestration and persistence logic, not third-party vendor CLI stability.

## Validation commands

- `gofmt -l .` → clean
- `go vet ./...` → clean
- `go build ./...` → ok
- `go test ./...` → all pass
