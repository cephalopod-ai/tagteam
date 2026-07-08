# Test Ledger

Derived from the test files and a local validation run. 198 test functions
across 8 files.

| Test Area | Command / Evidence | Last Known Result | Source | Coverage Meaning | Gaps |
|---|---|---|---|---|---|
| Run loop / modes | `go test ./internal/tagteam/` (`runner_test.go`, 83 tests) | pass except 2 (see below) | `runner_test.go` | Supervisor/relay/solo/adversarial flows, slicing, round limits, env policy, artifacts | 2 scout-context tests fail (pre-existing) |
| Config resolution | `go test ./internal/tagteam/` (`config_test.go`, 59 tests) | pass | `config_test.go` | Layered precedence, profiles, `ResolveOptions`, per-role policy | — |
| Adapters | `go test ./internal/tagteam/` (`adapters_test.go`, 34 tests) | pass | `adapters_test.go` | argv construction, capabilities, per-role env/sandbox | — |
| Run state / reasons | `go test ./internal/tagteam/` (`run_state_test.go`, 5 tests) | pass | `run_state_test.go` | Failure classification, exit→reason, budget wiring, overlay redaction | — |
| Redaction | `go test ./internal/tagteam/` (`redact_test.go`, 6 tests) | pass | `redact_test.go` | Overlay-aware secret scrubbing | — |
| Scout context budget | `go test ./internal/tagteam/` (`context_budget_test.go`, 4 tests) | pass | `context_budget_test.go` | Deterministic estimate + policy | — |
| Scout retrieval | `go test ./internal/tagteam/` (`retrieval_test.go`, 6 tests) | pass | `retrieval_test.go` | Bounded local retrieval evidence | — |
| CLI | `go test ./internal/cli/` (`root_test.go`, 1 test) | pass | `root_test.go` | Command wiring | Thin coverage of CLI layer |
| Formatting | `gofmt -l .` | pass (empty) | CI | CI gate | — |
| Vet | `go vet ./...` | pass | CI | CI gate | — |

## Known failures

- `TestRunLoop_RelayModeScoutContextNearLimitCompactsRetrieval`
- `TestRunLoop_RelayModeScoutContextExceedsDisablesRetrieval`

Root cause: the fake prompt's estimated token count (~687) does not exceed the
tests' configured `MaxContextTokens` (2100 / 1300), so the compaction/disable
branch under test never fires. The fix belongs in the test fixtures' token
budgets or the estimator. **These should be green before public release.**

## Validation commands

- `gofmt -l .` → clean
- `go vet ./...` → clean
- `go build ./...` → ok
- `go test ./...` → all pass except the 2 known failures above
