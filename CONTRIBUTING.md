# Contributing

## Scope

Keep changes small and coherent. `tagteam` is an orchestration CLI, not a vendor CLI shim. Avoid unrelated cleanup in the same change.

## Development

Requirements:

- Go 1.22+
- Git
- Any adapter CLIs needed for local manual testing

Common commands:

```bash
gofmt -w main.go internal/cli/root.go internal/tagteam/*.go
go test ./...
go vet ./...
```

## Adapter changes

When changing adapters:

- update argv construction tests;
- preserve clear preflight failures for missing or unrunnable CLIs;
- avoid cloning vendor flag surfaces unless `tagteam` owns the concept;
- document new config and examples in `README.md`.

## Pull requests

PRs should include:

- the user-visible behavior change;
- exact validation commands run;
- residual risks or known gaps.

If a change affects prompts, run artifacts, or config resolution, add focused tests for those paths.
