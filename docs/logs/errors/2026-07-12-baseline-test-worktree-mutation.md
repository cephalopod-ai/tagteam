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

## Scout integrity follow-up

Retry run `2026-07-12T144805.567563000Z` used a non-mutating focused baseline,
then its read-only Gemini scout invoked repository verification that rewrote the
same two registries. `runAdapter` correctly returned an integrity violation,
but the relay scout failure policy treated it like an ordinary unavailable
scout and continued without scout context. The contaminated retry was manually
cancelled and preserved.

Pre- and post-scout integrity violations now override the configured scout-loss
policy and abort into the run's quarantine path. Ordinary timeouts, unavailable
models, and output-contract failures retain their configured degrade-or-block
behavior.
