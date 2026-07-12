# 2026-07-12: Relay output-cap propagation and recovery failure

## Observed failure

Relay run `2026-07-12T082001.248813000Z` selected
`--max-output-bytes 8388608`, but its primary Codex editor invocation was
rejected at the 2 MiB default after writing 2,678,239 bytes to stderr. The
run's `state.json` recorded `worker_unavailable` with
`codex output exceeded max_output_bytes=2097152`.

The invocation completed with a captured 21-file patch, but the recovery
supervisor returned an invalid contract envelope. Tagteam therefore
quarantined the partial work rather than silently applying or discarding it.

Authoritative run artifacts remain under:

`~/.local/state/tagteam/5332546f5caa4cd1dc9d5651/runs/2026-07-12T082001.248813000Z/`

## Root cause

`ResolveOptions` preserved the CLI output limit in `RunOptions`, but the
primary editor request in `runner_part04.go` did not copy
`opts.MaxOutputBytes` into its `Request`. The adapter consequently used its
2 MiB fallback. Several non-editor relay requests had the same omission.

The repaired runtime now carries the run-level limit in the invocation context,
so both fresh runs and resumed runs inherit it unless a request explicitly sets
its own stricter limit.

## Resume follow-up

After the output-cap repair, an explicit `resume` of this quarantined run was
rejected with `resume quarantined: recovery decision already exists for round
1`. This is an intentional idempotency guard in the current recovery flow: it
prevents silently re-running a prior recovery decision. It does, however, mean
that an operator must start a new `--allow-dirty` continuation to preserve the
captured patch as a checkpointed baseline.

## Resolution status

The propagation repair was integrated with fresh-run and resume inheritance.
A new checkpointed `--allow-dirty` continuation,
`2026-07-12T085150.470640000Z`, completed two relay rounds successfully with
the requested 8 MiB limit and produced the full code-intelligence integration
contracts. Its nonblocking review findings were fixed before integration.

The original quarantined artifacts remain preserved for audit. The retry guard
remains intentionally unchanged; an explicit operator-approved retry design
is retained in `docs/TODO.md`.
