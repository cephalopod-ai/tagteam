# TODO

## Code-intelligence relay recovery

- [x] Rerun the quarantined full-phase code-intelligence work as a fresh,
  checkpointed `--allow-dirty` continuation after the output-cap repair. Run
  `2026-07-12T085150.470640000Z` passed its two completed relay rounds.
- [ ] Add an integration test that drives a relay editor above the default
  2 MiB output size while `--max-output-bytes` is higher, proving the CLI
  value reaches the editor request.
- [ ] Harden recovery-decision parsing for Claude envelope output so a valid
  embedded decision can continue with the configured fallback rather than
  unnecessarily quarantining an otherwise verified patch.
- [ ] Design an explicit, operator-approved retry path for a quarantined
  recovery decision. Preserve the current idempotency guard unless the retry
  records a new recovery attempt and its relationship to the original.
