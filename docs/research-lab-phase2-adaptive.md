# Laboratory Phase 2 — opt-in adaptive research

`RESEARCH_ADAPTIVE_ENABLED=false` remains the default. Trusted opt-in profiles
snapshot the flag and a finite continuation allowance derived from MaxRounds.
Fixed-plan runs retain their existing one-continuation behavior.

The existing worker/controller and SQLite checkpoint transactions implement:

- assessment of each round (goal, accepted IDs, completed steps, critical gaps,
  stable goal/criterion questions, failed sources, fingerprint, budgets, frontier);
- two independent targeted searches and two dependent new-source fetch steps in
  each subsequent round; duplicate queries are rejected before provider dispatch;
- monotonic lifetime budgets, round admission and per-round review caps;
- substantive evidence/criterion progress and finite stagnation stopping;
- persisted question history, no acceptance-budget refund on resolution;
- canonical adaptive-state authority protected against stale ordinary checkpoints;
- idempotent assessment identity, transaction rollback and reserved partial output;
- current round and assessed history in runtime/Inspector.

Tests exercise two rounds, new queries/sources, reopen after transition and after
assessment, stale checkpoint preservation, assessment conflict/idempotency,
rollback, stagnation, and saturated inference budget without unreserved synthesis.
The ordinary full Go suite also passed during integration.

Limits: the existing cumulative twelve-step ceiling remains. It can stop an
adaptive run before a larger profile's MaxRounds; no budget is silently expanded.
Questions are constrained by the user's original goal and accepted criterion
addresses, not an unrestricted autonomous topic-generation agent. Optional
network providers and real hardware remain deployment dependencies. This is
deterministic integration evidence, not a multi-hour live acceptance result.
