# Implementation Diagrams

Mermaid diagrams of the implemented `tagteam` architecture. Each carries an
evidence note listing the source files the diagram was derived from.

## Component map

```mermaid
flowchart TD
    main["main.go"] --> cli["internal/cli (commands, flags)"]
    cli --> app["internal/tagteam App (runner.go)"]
    app --> config["config.go (layered resolution)"]
    app --> adapters["adapters.go (codex/claude/agy/gosling/openai)"]
    app --> runstate["run_state.go (reasons, status, budgets)"]
    app --> orch["orchestration.go (host decision)"]
    app --> scout["retrieval.go / context_budget.go / scout_status.go"]
    app --> prompts["prompts.go / schema.go"]
    app --> redact["redact.go (persist-time redaction)"]
    app --> artifacts[".tagteam/runs/&lt;run-id&gt;/ (final.json, state.json, diffs, reviews)"]
```

**Evidence:** `main.go`, `internal/cli/root.go`, `internal/tagteam/runner.go`,
`config.go`, `adapters.go`, `run_state.go`, `orchestration.go`, `retrieval.go`,
`context_budget.go`, `scout_status.go`, `prompts.go`, `schema.go`, `redact.go`.

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

**Evidence:** `internal/tagteam/runner.go` (`Run`, `runLoop`,
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

## Notes

Diagrams are intentionally simple (`flowchart TD/LR`) and do not model
unverified internals. Update them when the run loop, adapter set, or reason-code
vocabulary changes.
