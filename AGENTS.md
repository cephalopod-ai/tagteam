# AGENTS

This file is the canonical repo-wide instruction contract for coding agents.

## Canonical Filename

`AGENTS.md` (uppercase, plural) is the single canonical repo-wide agent
contract. If tool-specific adapter files exist, this file wins on conflict.

### Adapter Files And Precedence

Tool-specific files such as `CLAUDE.md`, `CODEX.md`, `GEMINI.md`, `.claude/AGENTS.md`,
`.codex/AGENTS.md`, or similar may add tool-specific preferences, but they must
not redefine or weaken anything in `AGENTS.md`.

Recommended read order for non-trivial changes:

1. `AGENTS.md`
2. The active tool's adapter file, if present and relevant
3. `README.md`
4. Task-relevant docs, tests, and nearby implementation files

## Core Rules

1. Inspect existing code, tests, docs, and conventions before writing new code.
2. Do not invent APIs, files, config keys, commands, or paths without verifying they fit the repo.
3. Preserve existing naming, formatting, architecture, and test patterns unless the task explicitly changes them.
4. Execute one coherent task per run. Do not bundle unrelated fixes or opportunistic cleanup.
5. Prefer the smallest coherent change that satisfies the task and preserves existing behavior.
6. Surface conflicting patterns instead of averaging them together. Choose the least risky local convention and note the conflict.
7. Never silently fail. Report partial success, blocked work, degraded behavior, skipped checks, and visible errors.
8. Do not hide or suppress errors to make tests, logs, or UX look clean.
9. Do not delete or overwrite user work or existing code without explicit instruction.
10. Run the relevant checks before declaring completion, and state exactly what passed, failed, or was not run.

## Working Posture

Default posture:

- Prefer inspection before modification.
- Prefer minimal diffs over rewrites.
- Prefer extending existing patterns over inventing parallel ones.
- Prefer real wiring over mock structure.
- Prefer validation evidence over claims.

Push back when:

- implementation is requested before the relevant architecture or workflow is understood;
- UI or CLI behavior is added before the backend or execution path is clear;
- contracts, persistence, or external integration behavior is unclear;
- work is declared complete without end-to-end validation for the changed path.

Pushback should be advisory unless the task explicitly asks for a hard stop.

## Sequence Guidance

Before large or non-obvious changes:

1. Identify the entry points, contracts, and main execution path.
2. Inspect relevant files and nearby tests.
3. State assumptions, dependencies, and likely failure modes.
4. Implement only after the dependency chain is clear enough to act safely.
5. Validate the result against the changed path, not just static structure.

## Anti-Fake Implementation Policy

Treat the following as incomplete unless explicitly requested:

- buttons or commands with no real handler path;
- API routes or commands that return canned success without performing the action;
- services that are only wrappers around TODOs;
- mock or placeholder data presented as real output;
- “security” that only hides fields in UI without backend enforcement;
- “workflow support” that does not execute end-to-end;
- tests that only assert trivial truth or import success.

If any of these exist in touched scope, call them out explicitly.

## Testing Guidance

Add tests when the change introduces:

- reusable logic;
- non-trivial branching behavior;
- cross-module interactions;
- data transformations;
- bug fixes that should not regress.

Tests are optional when:

- the code is exploratory or throwaway;
- the behavior is trivial and fully validated through direct execution;
- the change is purely presentational scaffolding without logic.

Test expectations:

- Validate behavior, not just existence.
- Prefer focused tests over broad, fragile ones.
- A test should fail if the core behavior breaks.

## Completion Language

Do not say:

- `done` if validation is partial;
- `fully working` if only static checks were run;
- `wired` if only references were added;
- `secure` without boundary review;
- `production-ready` without operational evidence.

Prefer:

- `implemented`
- `partially validated`
- `statically consistent`
- `runtime-verified for the tested path`
- `residual risks remain`

## Scope And Safety

- Stay within the requested task boundary.
- If adjacent changes are necessary, disclose them explicitly.
- Stop at destructive actions, unresolved ambiguity, or conflicting repo state and report the next safe step.
