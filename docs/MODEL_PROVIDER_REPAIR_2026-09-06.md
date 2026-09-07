# Model and Provider Compatibility Repair — 2026-09-06
## Scope and baseline
The operator requested GPT-6 Astra and Claude Fable 5.1 support, current model discovery, provider-contract verification, live Grok/Mistral/Antigravity playtests, and an independent Sonnet check. Baseline was clean `main` at `829e412ea99c9139083b9f901e2659241711b8ef`; `go test ./...` and installed-provider help/model lists passed before edits. In-scope repairs and provider runs were authorized; destructive or outward-facing actions were not.

## Frozen finding inventory
| ID | Priority | Finding and evidence | Disposition |
|---|---|---|---|
| MPC-001 | P1 security | Grok used `--single <prompt>`; live `ps` exposed the task and diff in argv. | Fixed/runtime-verified: Unix/macOS uses `--prompt-file /dev/stdin`; live `ps` showed no payload. |
| MPC-002 | P1 correctness | Tagteam used obsolete Mistral ACP `session/set_model` and ignored its error; Vibe 2.24.5 advertises a `model` config option. | Fixed/runtime-verified: explicit `devstral-small` review completed through `session/set_config_option`; selection errors fail visibly. |
| MPC-003 | P2 compatibility | Routing/TUI omitted Astra/Fable and pinned older live-discoverable Grok/Gemini entries. | Fixed/verified: shared current entries plus source-labeled live Agy, Grok, and Mistral discovery. |
| MPC-004 | P1 correctness | Relay `2026-09-06T220626.530611000Z` passed a contradictory `needs_changes`/zero-findings review. | Fixed/runtime-verified: host-side `ValidateCurrent` requires a finding; a later Grok review cleanly passed. |

## Repair and verification

The coherent repair moves Grok's Unix/macOS prompt to stdin, selects Mistral models through shared ACP session configuration, adds read-only warning-preserving model discovery and current shared choices without changing supervisor/worker defaults, and validates cross-field review consistency after provider output. The `main -> internal/cli -> internal/tagteam` direction, role permissions, and legacy schema-v0/v1 readability remain unchanged; external schemas avoid unsupported conditional keywords.

- Final checks passed: `go test ./...`, `go build ./...`, `go vet ./...`, `gofmt -l .`, `git diff --check`, and the source-file line gate. Five uncached `internal/tagteam` runs took 129.984, 128.582, 128.827, 128.806, and 129.766 seconds.
- Grok unit/live checks verified stdin isolation and bounded Windows fallback. Mistral tests/live review verified discovery, explicit selection, failure paths, and unadvertised-model rejection. Live catalog/routing output contained Astra, Fable, Grok 4.6, Gemini 3.8, and the current Mistral ACP choices.
- A disposable Agy/Gemini 3.8 scout → GPT-6 Astra coder → Grok 4.6 reviewer relay found MPC-004; the corrected small Grok review passed. A full-tree Grok review crossed the safe boundary but hit its four-minute provider timeout, leaving large-diff latency provider-dependent.
- Repeated independent Sonnet 5 reviews drove record/churn, schema, `.env`, ACP duplication, restricted-environment, redaction, config-headroom, catalog-test, and Windows/Unix prompt-limit fixes. The final patch stays within both churn gates.

## Audit coverage and residual risks

AOC-001–005 map to MPC-001/002/004; AOC-006–013 retained visible retry/persistence/timeout/error behavior and removed the Grok argv leak; AOC-014–016 map to MPC-003 and warning-preserving discovery; AOC-017–024 preserved roles/isolation and verified routing/TUI/CLI surfaces; AOC-025–030 retain explicit experimental-risk language and are covered by the indexed record, README, tests, and full-suite evidence.

- Simultaneous processes using different `--state-root` values for one repository can race over `.tagteam/repo.json`; the initial parallel playtest observed pointer quarantine. Final runs were serialized. This pre-existing persistence concern belongs in a separate repair.
- Windows retains Grok's bounded positional path because `/dev/stdin` is not portable. Provider availability is account- and time-dependent; maintained Codex/Claude entries are labels, not entitlement claims.

No code markers or stored findings require migration. This indexed file is the durable source record; serialized review artifacts remain in the external Tagteam state root.
