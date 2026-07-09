# Test Ledger

Derived from the current test files and a local validation run on 2026-07-09.
250 test functions across 14 files.

| Test Area | Command / Evidence | Last Known Result | Source | Coverage Meaning | Gaps |
|---|---|---|---|---|---|
| Run loop / modes | `go test ./internal/tagteam/` (`runner_test.go`, 85 tests) | pass | `runner_test.go` | Supervisor/relay/solo/adversarial flows, slicing, round limits, env policy, artifacts, fallback/repair paths | Large surface still concentrated in one file |
| Config resolution | `go test ./internal/tagteam/` (`config_test.go`, 61 tests) | pass | `config_test.go` | Layered precedence, profiles, `ResolveOptions`, JSON repair/fallback config, per-role policy | — |
| Adapters | `go test ./internal/tagteam/` (`adapters_test.go`, 35 tests) | pass | `adapters_test.go` | argv construction, capabilities, schema wiring, per-role env/sandbox | — |
| Run state / reasons | `go test ./internal/tagteam/` (`run_state_test.go`, 5 tests) | pass | `run_state_test.go` | Failure classification, exit→reason, budget wiring, overlay redaction | — |
| Active run lifecycle | `go test ./internal/tagteam/` (`active_run_test.go`, 8 tests) | pass | `active_run_test.go` | `.tagteam/active.json` creation, cleanup, stale-pointer safety, post-failure inspection | — |
| Snapshot / live status | `go test ./internal/tagteam/` (`snapshot_test.go`, 9 tests) | pass | `snapshot_test.go` | `RunSnapshot` assembly from `active.json`, `state.json`, `final.json`, `plan.json`; compatibility regressions | — |
| TUI | `go test ./internal/tui/` (`render_test.go`, 9 tests; `state_test.go`, 10 tests; `tui_test.go`, 5 tests) | pass | `internal/tui/*_test.go` | Compose/config resolution, profiles, mode defaults, bounded overlays, detail scrolling, command completion, split terminal input, non-interactive behavior, and missing-run handling | Automated coverage does not execute a full vendor-backed launch in a real terminal |
| CLI | `go test ./internal/cli/` (`root_test.go`, 1 test; `tui_test.go`, 6 tests) | pass | `internal/cli/*_test.go` | Command wiring, TUI run resolution, completed-run command behavior | General CLI layer coverage remains thinner than runner coverage |
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
