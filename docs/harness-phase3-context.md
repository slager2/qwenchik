# Harness Phase 3: durable role context

## Contract and data flow

Role inputs previously rebuilt dates, optional memories and evidence from live state on restart. An abandoned invocation could consequently acquire a different operation identity or consume observations from an earlier plan attempt.

The worker now builds `ContextSnapshot` before calculating the remaining prompt budget, then pins the canonical role input in the existing checkpoint JSON. Planner, planner correction, actor, verifier, finalizer, citation critic and chat all use this path. The snapshot and role inputs remain task-scoped. Chat constraints follow only the active message ancestry.

The durable path is:

`task + active checkpoint + owned evidence → ContextSnapshot → bounded role input → immutable role pin → invocation intent/reservation → local inference`

Role identity includes the role, current step, plan attempt, completed steps, failures and user constraints. Nil and empty completed-step lists have the same identity. Invocation identity uses that role slot; the actual request hash must still match. A prompt/schema change cannot silently create a fresh recovery retry. Format repair uses a separate bounded invocation slot.

All user updates remain chronological constraints. Protected goal content exceeding 32 KiB fails before inference rather than silently dropping earlier instructions. Evidence comes only from the active attempt's tool results, and must reference owned successful, unflagged, nonempty extracted sources. An attempt change clears old working notes, questions, contradictions and failed-attempt summaries; historical checkpoints and evidence are retained.

## Bounds and authority

- ContextSnapshot: 32 KiB.
- Role input: 128 KiB; at most 128 pins and 1 MiB total per task.
- Artifact reads: existing task-owned bounded range tools, at most 64 KiB per read.
- Tool results are compacted only after reserving the snapshot and other mandatory input fields.
- Existing typed output schemas and the single inference slot remain authoritative.
- Snapshot notes, memory and external content are untrusted reference data. They cannot grant tools or change policy, approvals or budgets.
- Research budgets are projected from the durable research session by storage, never supplied by working-set notes.

No SQL migration, new table or configuration flag is introduced by the role integration. Snapshot and role-pin persistence uses existing checkpoints and internal events. `context.role_pinned` contains hashes and sizes, not raw prompts or model reasoning.

## Failure and recovery

Corrupt checkpoint hashes, missing embedded snapshots, changed pinned requests, pin overflow and ownership violations fail closed. An older execution containing invocation history but no snapshot requires recovery review before acquiring a new role identity. Completed compatible JSON invocations replay without another model call or budget charge; abandoned JSON invocations retain the existing one-retry ceiling. Unknown streams retain the existing recovery-required behavior.

For a recovery-required run, inspect its durable error/status and checkpoint before issuing an explicit retry. Do not delete invocation history or reset budgets to force continuation. Keep the current binary available when upgrading an installation with paused work. Rolling back to a binary that does not understand the optional `plan_attempt` snapshot field is not supported for newly written snapshots; use the verified binary to finish or cancel those runs first.

Checkpoint validation still checks the complete chain, hashes and new-pin events. Byte-identical role-pin data inherited from an already verified parent is not reparsed repeatedly. Snapshot decoding reads the snapshot field without repeatedly serializing every embedded prompt. This removed race-test deadline failures without extending runtime deadlines or weakening corruption checks.

## Verification and remaining Phase 3 acceptance

Integration tests cover role builders, immutable replay across reopen, repair-slot separation, two-crash retry ceilings, prompt/schema drift, resource-pause pin stability, old-invocation upgrade handling, all user constraints, goal overflow, chat branch isolation and corrupt role payloads. Existing manual-replan tests cover fresh evidence after retry.

The complete Go suite, vet and race suite pass with `sqlite_fts5`. Frontend tests (34), typecheck, production build and Chrome smoke pass.

This completes role-input wiring, not the entire research harness. Phase 3 still needs its runtime compaction event path assessed against preservation of done criteria, pending actions and contradictions. The existing storage API emits `context.compacted` only for a genuine reduction; ordinary role-boundary saves are internal `context.snapshot_saved` events. PDF extraction remains dependent on a configured isolated extractor; download success alone is not extraction acceptance.
