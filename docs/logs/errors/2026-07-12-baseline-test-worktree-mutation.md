# 2026-07-12: Baseline test contaminated the Muninn worktree

## Observed failure

Muninn run `2026-07-12T144004.489591000Z` configured the full pytest suite as
its baseline command. That suite rewrote two tracked governed-memory registries
before any editor invocation:

- `muninn/memory/archived/registry.yaml`
- `muninn/memory/working/registry.yaml`

The resulting delta was approximately 4,738 additions and 2,098 deletions.
Tagteam did not compare the worktree before and after the baseline test, so the
mutation appeared in live progress during the read-only scout phase. The run
was manually cancelled and its worktree remains preserved as quarantine
evidence.

## Repair

`runBaselineTest` now captures Git-visible worktree snapshots immediately before
and after the command. Any delta returns an integrity violation naming the
mutated paths and stops the run before orchestration or editor work.

Ignored runtime files remain outside Git's ordinary status surface. Extending
the check to explicitly governed ignored paths is retained in `docs/TODO.md`.
