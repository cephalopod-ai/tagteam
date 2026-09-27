# Implementation Diagrams

Mermaid diagrams of the implemented `tagteam` architecture. Each carries an
evidence note listing the source files the diagram was derived from.

## Component map

```mermaid
flowchart TD
    main["main.go"] --> cli["internal/cli (commands, flags)"]
    cli --> tui["internal/tui (interactive terminal dashboard)"]
    cli --> app["internal/tagteam App (runner.go)"]
    app --> config["config.go (layered resolution)"]
    app --> adapters["adapters.go (codex/claude/agy/gosling/openai)"]
    app --> store["artifact_store.go (external authoritative state)"]
    app --> active["active_run.go (external active.json)"]
    app --> snapshot["snapshot.go (RunSnapshot assembly)"]
    app --> runstate["run_state.go (reasons, status, budgets)"]
    app --> orch["orchestration.go (host decision)"]
    app --> scout["retrieval.go / context_budget.go / scout_status.go"]
    app --> prompts["prompts.go / schema.go"]
    app --> redact["redact.go (persist-time redaction)"]
    snapshot --> tui
    active --> snapshot
    app --> artifacts["~/.local/state/tagteam/&lt;repo-id&gt;/runs/&lt;run-id&gt;/"]
    artifacts --> snapshot
```

**Evidence:** `main.go`, `internal/cli/root.go`, `internal/tagteam/runner.go`,
`internal/cli/tui.go`, `active_run.go`, `snapshot.go`, `config.go`,
`adapters.go`, `run_state.go`, `orchestration.go`, `retrieval.go`,
`context_budget.go`, `scout_status.go`, `prompts.go`, `schema.go`, `redact.go`,
`internal/tui/*.go`.

## Live status / TUI data flow

```mermaid
flowchart LR
    pointer[".tagteam/repo.json"] --> runner["runner.go"]
    runner --> active["external active.json"]
    runner --> state["run state.json"]
    runner --> final["run final.json"]
    runner --> plan["run plan.json"]
    active --> snap["BuildRunSnapshot"]
    state --> snap
    final --> snap
    plan --> snap
    snap --> status["status-style readers"]
    snap --> tui["tagteam tui"]
```

**Evidence:** `internal/tagteam/runner.go`, `active_run.go`, `snapshot.go`,
`types.go`, `internal/cli/tui.go`, `internal/tui/tui.go`.

## Reviewed-mode run loop

```mermaid
flowchart TD
    start([tagteam run]) --> pre[Preflight: baseline, run dir, adapter checks]
    pre --> decide{Host orchestration decision}
    decide --> scoutq{Relay mode?}
    scoutq -- yes --> retr[Pre-scout retrieval + context budget]
    retr --> scoutrun[Scout recon pass]
    scoutq -- no --> brief
    scoutrun --> brief[Supervisor brief / work plan]
    brief --> impl[Editor / coder implements]
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

**Evidence:** `internal/tagteam/runner.go` (`Run`, `Review`, `runLoop`,
`collectRoundLimitReports`), `run_state.go` (`finalizeRunState`,
`classifyRoleFailure`, `reasonForExit`), `orchestration.go`.

## Failure classification → reason code

```mermaid
flowchart LR
    err[Adapter / run error] --> c{classifyRoleFailure}
    c -- output contract --> rj[reviewer_json_invalid]
    c -- budget exceeded --> bx[budget_exceeded]
    c -- scout context sentinel --> sc[scout_context_too_small]
    c -- role=scout --> su[scout_unavailable]
    c -- role=worker/coder, deadline --> wt[worker_timeout]
    c -- role=worker/coder, other --> wu[worker_unavailable]
    c -- role=supervisor --> sv[supervisor_unavailable]
    c -- default --> rv[reviewer_unavailable]
    exit[Exit code] --> re{reasonForExit}
    re -- blocking findings --> bf[blocking_findings]
    re -- tests failed --> tf[test_failed]
```

**Evidence:** `internal/tagteam/run_state.go`
(`classifyRoleFailure`, `reasonForExit`), `internal/tagteam/types.go`
(`ReasonCode`, `Exit*`), `context_budget.go` (`errScoutContextTooSmall`).

## Deterministic adapter-operation lifecycle

```mermaid
stateDiagram-v2
    [*] --> prepared: persist request hash + idempotency key
    prepared --> in_flight: dispatch begins
    in_flight --> committed: persist result + integrity digest
    in_flight --> failed: confirmed failure
    in_flight --> in_doubt: restart finds uncertain effect
    in_doubt --> committed: provider reconciliation finds result
    in_doubt --> in_flight: explicit retry retains duplicate risk
    committed --> [*]: exact result replay
    failed --> [*]
```

Every transition atomically replaces the same sequenced operation record.
Resume accepts only the exact unresolved tail or an exact committed tail;
missing records, changed requests, stale workflow revisions, and illegal state
transitions fail closed. `in_doubt` is not an exactly-once claim: reconciliation
by idempotency key precedes any retry, and an unreconciled retry remains visibly
at risk of a duplicate external effect.

**Evidence:** `internal/tagteam/replay_contract.go`
(`prepareOperation`, `transitionOperation`, `reconcileOperation`),
`internal/tagteam/replay_contract_test.go`.

## Failed invocation recovery and transfer

```mermaid
flowchart LR
    invoke[Worker invocation] --> failure{timeout / stall / nonzero / invalid contract?}
    failure -- yes --> checkpoint[Sync streams + capture partial patch + isolated tests]
    checkpoint --> decision{Recovery supervisor}
    decision --> repair[Resume same worker]
    decision --> fallback[Continue configured fallback]
    decision --> quarantine[Quarantine without reverting]
    repair --> gates[Tests + scope/churn + independent review]
    fallback --> gates
    gates --> transfer{Explicit transfer command}
    transfer --> verify[Patch hash + clean matching target + lint/regression/findings]
    verify --> apply[git apply --check then git apply]
```

**Evidence:** `internal/tagteam/recovery.go`, `invocation_stream.go`,
`timeout_calibration.go`, `quality_gates.go`, `findings.go`, `transfer.go`.

## Notes

Diagrams are intentionally simple (`flowchart TD/LR`) and do not model
unverified internals. Update them when the run loop, adapter set, or reason-code
vocabulary changes.
