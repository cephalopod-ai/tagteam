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

## Resolution status

The propagation fix is tracked in the active repair branch. The quarantined
feature patch is intentionally left untouched until the repair is validated
and a fresh relay/recovery decision can safely use the configured limit.

