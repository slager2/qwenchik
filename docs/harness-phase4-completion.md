# Phase 4: bounded completion and verifier baseline

## Contract

Previously, evidence-readiness failure immediately failed a task. The worker now supports one productive targeted research continuation by default on newly snapshotted standard/deep/long/overnight profiles. Quick and older snapshots with absent/zero `max_continuations` retain their fixed-plan behavior. No minimum wall time is imposed.

The backend appends a bounded search/fetch pair, preserving the original plan, completed step rows, evidence, pending state and lifetime reservations. This pair addresses the bounded unmet criteria carried in `context_snapshot.open_questions`; it cannot replace the user goal. Model-generated `continue_` step IDs are rejected. The continuation event, extended plan, step rows, round transition and checkpoint commit in one transaction. Network calls occur afterward.

The governor fingerprints normalized queries, executed tool/step transitions and evidence IDs. Model narrative, summaries and changing dates are not progress. Repeating the same failure or making no progress blocks further continuation. Cancel, pause, unknown effects, deadlines and hard ceilings take precedence. The run's original profile/budgets remain immutable. At most three local verifier/critic invocation intents are admitted, including recovery/format attempts; cache replay does not consume another reservation.

## Verification and outcomes

The existing deterministic execution/criterion/citation checks remain authoritative. The local verifier can now report up to eight brief contradictions. Unresolved recorded contradictions prevent readiness and remain in durable context. `verification.completed` records the backend readiness verdict (`PASS`, `RESEARCH`, `UNCERTAIN`; the storage contract also admits `REPAIR`/`BLOCK`) with a bounded summary and missing criteria. It is a readiness verdict, not a declaration that the final report already passed citation review. Final citation verification and deterministic excerpt fallback retain their existing bounded repair behavior.

Tasks remain the run storage. Existing status enums are retained; the API exposes `terminal_outcome` for honest `partial`, `blocked` and `budget_exhausted` outcomes. Deadline, step-budget and research-budget exhaustion cannot be reported as completion. Unmet criteria, exhausted continuation, unknown effects and invalid structured outputs project as blocked. Deterministic citation fallback projects as partial.

## Persistence and recovery

No SQL migration or new table. `max_continuations` lives in immutable policy JSON; completion state lives in the existing checkpoint JSON, backed by append-only continuation events. The storage count of committed continuation events is authoritative, so a repeated command cannot reset the ceiling. Role identities include the continuation number only when nonzero, retaining the existing initial-role identity format.

On restart after `continuation.requested`, the appended steps resume using existing invocation fencing and replay. A committed continuation is not appended a second time. Counters, evidence and input pins remain intact. Upgrades should be performed while no tasks are active; downgrading an active governed run to a binary without governor support is not supported.

## Checks

The full Go test, vet and race suites pass with `sqlite_fts5`. Integration tests interrupt immediately after the continuation transaction, reopen SQLite, resume real worker execution with fake providers/model, and verify success or a finite blocked outcome. Storage fault injection covers event/checkpoint rollback and cancellation precedence. Tests also cover the durable three-review ceiling and narrative-independent progress fingerprints. No cloud inference, arbitrary shell, extra inference slot or orchestration framework is introduced.
