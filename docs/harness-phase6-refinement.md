# Phase 6: bounded local harness refinement

## Admission and scope

Implemented after the phase 1–5 backend/frontend regression suites and live disconnected-client, research, resource telemetry, notification and SSE checks. The live research cases produced honest `partial` and `blocked` outcomes rather than unsupported success. Their durable error codes are useful refinement inputs; a four-billion-parameter model is not assumed to satisfy every prompt.

Refinement is explicitly requested, not an unbounded background optimizer. It runs as a normal durable task (`mode=refinement`) in the existing sequential worker and uses the same guarded local Qwen client. It does not train weights, execute generated code, use a shell/REPL, invoke a cloud teacher or create another inference slot.

The v1 `CandidatePatch` can change only two bounded, non-authoritative text hints: actor query selection and finalizer citation formatting. Unknown JSON fields are rejected. Tool contracts, Go policy, security controls, model endpoints, secrets, budgets, approvals and verifier authority cannot be changed through the patch. Other forms of refinement are not enabled.

## Data flow and bounds

`completed source tasks → bounded error-code clusters → pinned proposal JSON → CandidatePatch validation → baseline/candidate held-out evaluation → rejection or approval request → explicit promotion → new run snapshots`

Only error-code counts are passed from source traces; raw prompts, responses, credentials, cookies and fetched page content are excluded. Source task IDs and the baseline/model/deadline are immutable checkpoint provenance. Proposal/evaluation inputs are pinned through the existing role-context path, and inference intents/outcomes use the existing invocation journal.

- At most eight source task IDs.
- One proposal, at most one format-only retry.
- Four held-out cases, evaluated against baseline and candidate: eight evaluations, temperature zero.
- At most ten logical model invocation intents and twenty physical HTTP reservations, both durable before I/O, including adapter fallbacks.
- At most 16 KiB input and 512 output tokens per call.
- Five-minute model-work deadline; a separate twenty-minute task deadline permits a bounded human approval window.
- Model calls remain serial. Resource guard, cancel and lease fencing retain priority.

## Held-out gate

`heldout-v1` checks typed tool arguments, rejection of page instructions, exact evidence selection/quote/citation formatting, and unsupported-claim rejection. Text and evidence identifiers are seeded per job and source ordering varies; proposal generation does not receive the held-out cases. Both variants see identical cases. The candidate must pass all four critical checks, score no worse than baseline, and stay within twice baseline inference latency plus a two-second allowance. Latency is read from durable invocation timestamps so cached replay does not substitute cache-read time for inference latency.

This is a small, explicit regression fixture set, not evidence of benchmark superiority or general factual correctness. Backend evidence/citation checks remain active after promotion. Evaluation cannot manufacture accepted research evidence.

## Persistence and migration 011

`011_refinement.sql` adds one necessary table, `harness_bundles`, for immutable baseline/candidate specifications, parent/source lineage, patch provenance, evaluation results, approval state and the active-version pointer. Tasks remain runs. No competing scheduler/run table is added. Migration is idempotent and does not backfill or rewrite user tasks, messages, evidence, memories or FTS structures.

Bundle hashes include operational hints. Refined bundles retain the compiled baseline identity and source prompt hash. Existing runs keep their own immutable bundle/model/context snapshot; activation changes only defaults for new runs. A binary with incompatible compiled baseline identity rejects the run before inference.

Candidate creation and its recovery pointer commit together. Failed/cancelled tasks quarantine unpromoted candidates. A completed evaluation survives restart without repeating model calls. Promotion atomically verifies a passed gate, a matching unconsumed user approval, the active parent, and task/lease/deadline state; it then activates the version and consumes the approval in the same transaction. Fault injection proves that neither activation nor approval consumption escapes rollback. Promotion is idempotent after a crash before final task completion.

Rollback selects only a previously approved version in the same compiled-baseline family and records provenance. It preserves newer versions and their evaluations. On server restart, the active approved version is reloaded before accepting new runs.

Before a binary downgrade, cancel/finish maintenance tasks and roll back any refined active version through the compatible binary. Keep a SQLite backup. Dropping `harness_bundles` would remove version/provenance state and is not an automatic downgrade operation. No user research table needs reversal.

## API and approval workflow

1. `POST /api/refinement`, with `Idempotency-Key` and `{"source_task_ids":["..."]}`, returns a queued task. Repeating the same command returns the same task even if server defaults later change.
2. Observe ordinary task events/checkpoints or `GET /api/runs/{task_id}/runtime`; `refinement_candidate_hash` identifies the candidate after proposal.
3. `GET /api/bundles/{hash}` returns specification, patch, lineage and held-out results.
4. A rejected candidate never changes the active version. A passed candidate creates the existing durable `harness.promote_bundle` approval and waits.
5. Review the candidate through the link in the workspace approval card, then approve/reject through the existing approval API/UI. No model or evaluator can approve itself.
6. `POST /api/bundles/rollback` with `{"bundle_hash":"previous-approved-hash"}` is an explicit user rollback command. It updates the active defaults without mutating existing run snapshots.

Public journal events are `refinement.evaluated`, `refinement.promoted`, `refinement.rollback`; bounded safe projections omit role inputs and raw model responses. Proposal details are available in the explicit candidate-review resource.

## Verification

Tests cover valid and deliberately regressing candidates, schema/policy-field rejection, proposal retry ceilings, model/HTTP ceilings, immutable job metadata across generic checkpoint writes, restart after evaluation, no activation before approval, no extra model calls after approval, atomic promotion rollback, real rollback restoration, approved-hint task scoping, working-set protection, command idempotency and migration from schema 010 without data/FTS changes. The full Go test/vet/race suite and frontend typecheck/tests/build/Chrome smoke pass. Experimental phase 7 features remain disabled and unimplemented.
