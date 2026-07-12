# TODO

## Code-intelligence relay recovery

- [ ] After the output-cap propagation repair is merged, resume or rerun the
  quarantined full-phase code-intelligence relay
  `2026-07-12T082001.248813000Z` from its preserved run artifacts.
- [ ] Add an integration test that drives a relay editor above the default
  2 MiB output size while `--max-output-bytes` is higher, proving the CLI
  value reaches the editor request.
- [ ] Harden recovery-decision parsing for Claude envelope output so a valid
  embedded decision can continue with the configured fallback rather than
  unnecessarily quarantining an otherwise verified patch.

