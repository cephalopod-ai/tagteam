# TUI Repair Campaign

Repair session: 2026-07-09

Scope: the supplied TUI audit findings in `internal/tui`. This was a focused
repair campaign, not a new feature pass.

## Baseline

- Branch and baseline: `main` at `3d2a528`.
- Existing TUI/dashboard worktree changes were already present and treated as
  the in-scope implementation under review.
- Commit and push policy: no commit or push was authorized.

## Inventory And Stage

One locality group covered the shared `state.go` / `render.go` / `tui.go` data
path. `state.go` was 1,716 lines and required coordinated edits, so terminal
input decoding was extracted to `input.go` as a cohesive, behavior-preserving
seam.

| ID | Defect | Disposition |
|---|---|---|
| TUI-1 | Detail scroll state was ignored by rendering. | Fixed |
| TUI-2 | Settings and command overlays could exceed the terminal or hide the selected item. | Fixed |
| TUI-3 | Palette navigation/completion was advertised but not implemented. | Fixed |
| TUI-4 | Initial CLI flags could override contrary TUI settings at launch. | Fixed |
| TUI-5 | Switching to relay from Settings could leave the scout target empty. | Fixed |
| TUI-6 | Role-specific slash commands could silently mutate the wrong role slot. | Fixed |
| TUI-7 | Split escape sequences and UTF-8 input could be decoded incorrectly. | Fixed |

## Validation

- `go test ./internal/tui` passed after each repair iteration.
- `go test ./...` passed.
- `go vet ./...` passed.
- `go build ./...` passed.
- `gofmt` and `git diff --check` completed cleanly.
- Manual TTY smoke check verified a bounded command palette, arrow selection,
  Tab completion, a deep selected Settings row, and alternate-screen cleanup.

## Residual Risks

- Automated coverage still does not run the full-screen TUI in a real terminal.
- Terminal display-cell width for wide Unicode glyphs is not modeled; rendering
  is bounded by rune count.
