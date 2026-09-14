# Deterministic replay contract (ADR-0001)

Status: accepted; artifact contract version 1.

## Decision

New executions freeze `execution-snapshot.json` before entering `running`. The
snapshot contains normalized workflow topology, arguments, resolved role
targets, policies, budgets, routing selection, input references, and digests of
secret reference names (never credential values). Canonical JSON uses sorted
object keys, UTF-8, explicit defaults, and SHA-256. Running execution treats
this artifact as immutable authority; changed workflow or arguments produce a
typed divergence rather than adopting current configuration.

Provider/model and agent subprocess calls are the currently centralized
external boundary. `runAdapter` prepares `operations/<sequence>.json` before
dispatch and moves it through `prepared`, `in_flight`, and `committed` or
`failed`. Records include frozen revision, versioned operation type, complete
request hash, bounded metadata, stable idempotency key, attempt, timestamps,
and fencing generation. The unique sequence filename enforces `(run,
sequence)` and durable atomic replacement commits transitions.

Replay consumes only the unresolved tail or an exact committed tail. Sequence,
operation type, request digest, canonical version, and workflow revision must
match. It never scans forward, renumbers, skips, or makes a fresh call on a
mismatch. `divergence.json` contains hashes and types, not request content.

`panel-budget.json` separately accounts for non-renewable cumulative work and
renewable live concurrency. Admission reserves the whole panel before any
member starts. Concurrent admissions serialize and use generation fencing.
Starting moves work from reserved to consumed; completion releases only live
capacity; cancellation refunds only never-started reservations. A frozen panel
digest prevents degraded substitution after admission.

## External-effect guarantee

> Tagteam deterministically replays its durable control history. External operations without a durably recorded commit may execute again. Resume is therefore at-least-once at uncertain external-effect boundaries, not magically exactly-once.

An `in_flight` record found after restart becomes `in_doubt`. Its stable
idempotency key must be used for provider-native reconciliation first. A found
provider result can be committed without dispatch. Otherwise retries visibly
set `duplicate_effect_risk`; high-impact non-idempotent effects require an
operator decision. No result is manufactured.

## Migration, rollout, and operation

There is no fabricated backfill. Runs without `execution-snapshot.json` are
reported as `legacy_non_replayable`; they may be inspected or finish under old
semantics, but operators must start a new run for deterministic replay. New
artifacts are additive, so rollback uses the previous binary, which ignores
them. Never delete operation artifacts to unblock a run. For
`diverged_blocked`, compare sanitized hashes and start a new run for intended
changes. For `in_doubt`, reconcile by idempotency key and assess duplicate
impact before any unreconciled retry.

Status JSON exposes `replay_status`, `replay_guarantee`, and
`uncertain_effects`; it intentionally never promises exactly-once.

## Traceability

| Invariant | Implementation | Tests |
|---|---|---|
| Frozen definition/arguments | `freezeExecutionSnapshot` | `TestExecutionSnapshotRejectsWorkflowAndArgumentMutation` |
| Complete canonical request | `CanonicalRequestEnvelope`, `adapterEnvelope` | `TestCanonicalRequestGoldenVectors`, `TestEverySemanticEnvelopeFieldChangesDigest` |
| Sequence and exact replay | `prepareOperation`, `transitionOperation` | `TestExactCommittedReplayDoesNotInvokeAgain`, `TestUnresolvedMismatchAndCorruptionFailClosed` |
| Uncertain effects | `OperationInDoubt`, `reconcileOperation` | `TestOperationCrashPoliciesAndReconciliation` |
| Atomic panels | `admitPanel`, generation fence | `TestPanelAdmissionIsAtomicAndConcurrent` |
| Separate budgets/restart | `PanelBudgetLedger` | `TestPanelLedgersRemainSeparateAndRestartIdempotent`, `TestCancelNeverStartedReleasesOnlyUnusedReservation`, `TestRetryAdmissionChecksWorkAndConcurrencyIndependently` |
| Legacy labeling | `BuildRunSnapshot` | `TestLegacyRunStatusCannotClaimDeterministicReplay` |

## Version 1 limitation

Host Git/filesystem mutation, test subprocess, code-intelligence, artifact
publication, and control-callback boundaries still use existing fail-closed
durable guards but do not yet support deterministic result replay. Provider
native reconciliation is exposed as a contract hook; adapter-specific lookup
implementations depend on external platforms and are not verified here. Tests
use local fake results and require no credentials or network.
