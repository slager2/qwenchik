# Deep Research reliability — Phase 1: bounded round plans and explicit budgets

Base: `d241ba0` (Phase 0 audit), implementation ancestor `6ca6379`.
Scope: **Phase 1 only**. No runtime restart, deployment, real-model experiment,
context ladder, benchmark or soak was performed. No application UI redesign.

> No artificial plan-step ceiling.
> Still bounded by explicit time, search, fetch, LLM, evidence and resource budgets.

This statement applies to new **version-two session-backed research**. Saved v1
sessions keep their immutable original allowances; they are not silently upgraded.

## 1. Implemented

### Separate initial and round plans

- `tasks.plan_json` remains the initial plan. It is not enlarged on v2 continuation.
- The existing checkpoint stores `research_plans` with its own payload version,
  immutable `initial_plan`, numbered bounded `round_plans`, and `current_round`.
- Existing `task_steps` and events retain the complete execution history. Each
  appended round has globally distinct step IDs and dependencies within that round.
- The worker selects the active checkpoint plan for execution and readiness.
  It validates the initial plan separately rather than flattening all rounds.
- Runtime projection adds `active_round_plan`, `initial_plan`, `round_plan_count`.
  The existing `/plan` route continues to return the initial plan; richer Inspector
  consumption belongs to the later UI phase. No routes were removed.

Round size is determined from remaining search/fetch/evidence allowances and
remaining rounds, bounded by `MaxRoundSteps`. It is not permanently two searches
and two fetches: that would make a 48-query profile nominal again. New rounds
retain at least two independent searches/fetches; insufficient headroom gives an
explicit stop rather than partially admitting a round. Success can stop earlier;
budget ceilings are not quotas to consume.

### Admission and recovery invariants

- Initial and round steps are reserved transactionally before becoming executable.
  `ResearchCounters.Steps` serializes as `steps_reserved`. Admitted skipped/failed
  steps are not refunded. Search, fetch and inference still use the existing
  pre-dispatch invocation reservations.
- A continuation atomically reserves steps, inserts history rows, advances the
  session round, emits every new step ID and saves the checkpoint. Failure at the
  event/checkpoint boundary rolls everything back.
- Duplicate expected-round requests conflict; a second continuation cannot appear.
- Ordinary checkpoint writes cannot replace canonical round plans, completion
  state or adaptive frontier. Stale writes from an older round are rejected.
- The session deadline stays absolute. Resume/restart neither extends it nor
  refunds operations. Unknown pending invocation/approval still blocks advancement.
- Existing v1 policy/budget/snapshot JSON remains readable and executes its legacy
  path. Runtime compatibility does not reinterpret its budget generation as v2.
- No migrations, tables, second queue, second orchestrator, or replacement
  checkpoint mechanism were added.

### Independent substantive criteria

The v2 planner schema requires bounded `deliverable_criteria`; normalization
retains them independently of search/fetch execution criteria. A backend-owned
criterion also requires substantive coverage of the exact request. The `goal`
namespace is reserved and cannot collide with a plan step.

Verifier catalogs and report critics address those stable goal criteria explicitly.
`ready_to_answer=true` or a supplementary classification cannot erase an unmet
explicit goal criterion. Execution-only acquisition criteria do not count toward
report-content coverage. Across rounds only substantive criterion identities
persist as criterion frontier questions; new execution step IDs do not themselves
constitute goal-coverage progress.

The report selector prioritizes owned evidence supporting goal criteria before
applying the source cap; selected source notes match the actual report catalogue.
The model working set contains current-round results and bounded prior fetched
evidence, not all historical discovery bodies or concatenated plans. Full history
and raw/text artifacts remain durable.

This is an acceptance contract, **not an independent factual judge**. A same-model
verifier/critic can still misjudge relevance or meaning. Real usefulness requires
the later authorized runs and rubric review.

### Explicit report budgets and partial delivery

New budgets separately constrain citation sources/claims, report sections,
factual body paragraphs and bytes. Supported sections and fallback excerpt units
share the body allowance. Metadata/methodology are not counted as factual claims.

Markdown and JSON artifacts each respect the v2 byte cap. If the full report does
not fit, deterministic compact partial delivery retains bounded cited excerpts,
the missing-coverage notice, artifact identities and a compact JSON budget receipt.
Full goal/provenance/state remains in the checkpoint and Evidence Ledger.
This does not start another inference/correction loop. Cached completed calls can
still replay at exhausted inference quota; new inadmissible report calls do not
create additional role-input pins. Report writes still consume no evidence slot.

## 2. New defaults and controls

Source defaults keep adaptive/report opt-ins false. Local ignored configuration
was not edited by this phase. `MAX_STEPS` remains a legacy/initial-plan alias;
it is no longer the cumulative run cap for policy v2.

| Budget | Global default cap |
| --- | ---: |
| `MAX_INITIAL_PLAN_STEPS` | 12 |
| `MAX_ROUND_STEPS` | 12 |
| `MAX_TOTAL_ROUNDS` | 10 |
| `MAX_TOTAL_STEPS` | 96 |
| `MAX_SEARCH_QUERIES` | 48 |
| `MAX_FETCHES` | 36 |
| `MAX_LLM_CALLS` | 128 |
| `MAX_EVIDENCE` | 32 |
| `MAX_QUESTIONS` / `MAX_QUESTIONS_ACCEPTED` | 80 / 40 |
| `MAX_CITATION_SOURCES` / `MAX_CITATION_CLAIMS` | 8 / 32 |
| `MAX_REPORT_SECTIONS` / `MAX_REPORT_PARAGRAPHS` | 8 / 32 |
| `MAX_REPORT_BYTES` | 524288 |

Caps reduce profile ceilings; they cannot raise a profile beyond its explicit
allowance. Invalid settings fail validation. Initial and individual round plans
remain deliberately bounded to at most 12 steps; total-step admission is separate.
An explicit lower round cap can limit achievable operation totals and is reported
in the saved budget. A normal minimal initial plan can reach every nominal round.

| Profile | Wall sec | Rounds | Total steps | Search | Fetch attempts | LLM calls | Evidence | Questions created/accepted |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| quick | 300 | 1 | 12 | 4 | 4 | 24 | 4 | 8 / 8 |
| standard | 1800 | 3 | 20 | 8 | 8 | 48 | 8 | 24 / 16 |
| deep_laptop | 7200 | 5 | 32 | 16 | 12 | 64 | 12 | 40 / 24 |
| long_laptop | 14400 | 8 | 64 | 32 | 24 | 96 | 24 | 64 / 32 |
| overnight_laptop | 21600 | 10 | 96 | 48 | 36 | 128 | 32 | 80 / 40 |

Report sections/paragraphs/claims: quick **4/8/8**, standard **6/16/16**, deep
**8/24/24**, long/overnight **8/32/32**. Each writer call remains capped to four
factual paragraphs and one persisted correction; profile totals span sections.
Citation sources remain quick 4, others 8. Stagnation remains quick 1, others 2.
Fetch attempts and accepted evidence are different counters: 36 fetch attempts
does not authorize 36 accepted evidence when the independent evidence cap is 32.

## 3. Checkpoint hot-path work

The extended scripted runs exposed expensive repeated full-history JSON decoding.
The first combined run timed out; an intermediate 10-round run passed in 510s.
No assertion was removed and the 10-round scenario remains in the full suite.

Changes preserve validation/history while reducing redundant work:

- Strict role-input envelope decoding rejects duplicate/unknown fields and
  invalid canonical hashes without parsing each nested input twice.
- Immutable equal future fields and unchanged pins avoid repeated merging.
- Internal latest-state readers still read/hash the whole SQL chain, but do not
  materialize every old model-input body. The public history reader returns every
  full checkpoint state as before.
- A bounded, process-local validation memo retains only snapshot metadata/pin
  hashes. Keys bind actual checkpoint bytes, all frame metadata, associated event
  type/visibility/payload, and the predecessor key. Modifying an ancestor or pin
  event invalidates descendants. No SQL permissions, budgets, leases or deadlines
  are cached. No model-input bodies are kept in this memo.
- Corruption checks, full history reads and post-cache ancestor-event tampering
  are tested. The full latest suite passed with orchestrator taking 301.875s.

History verification is still proportional to retained history bytes. This is
not a sustained-throughput claim or a completed long-run performance acceptance.

## 4. Scripted-test verified

| Requirement | Proof |
| --- | --- |
| 1 / 2 / 3 / 5 / 10 rounds | `TestBoundedRoundPlansRunOneTwoThreeFiveAndTenRounds`; real worker/storage/provider fixtures; explicit 4-step round cap; 4/8/12/20/40 total steps |
| Initial plan not expanded | Same test: `tasks.plan_json` stays four steps; `task_steps` retains all 40 at ten rounds |
| Variable bounded shapes | `TestRoundPlanStorageUsesVariableBoundedShape`; actual continuation persists a larger bounded plan and reserves its exact size |
| Profile reachability | `TestNominalRoundShapesCanReachProfileRoundsAndQueryBudgets`; independent profile golden/cap tests |
| Total/search/fetch/evidence/LLM stop | `TestRoundBudgetExhaustionPreservesUsefulPartialWithoutUnreservedInference`; explicit saved reasons, nonempty partial report, retained sources, no extra provider calls |
| Max rounds | Acquisition counterexample uses one-round policy, preserves misses and stops `max_rounds` |
| Stagnation/deadline | `TestBoundedRoundDeadlineAndStagnationDoNotDispatchOrRefund`; expired deadline blocks storage admission; no-progress rounds stop |
| Restart between rounds | Two-round worker case closes/reopens SQLite, recovers and claims; unchanged deadline, counters equal actual fixture calls, no repeated fetch |
| Stale/duplicate/atomic rollback | `TestBoundedRoundTransitionAtomicDuplicateAndStaleProtection`; event/checkpoint abort triggers, expected-round duplicate, old checkpoint rejection |
| Useful bounded byte fallback | `TestReportByteBudgetFallbackIsBoundedAndRecoveryStable`; both encodings <=4096 bytes and deterministic |
| Report artifacts after restart | `TestReportByteBudgetArtifactsResumeWithoutModelReplay`; exactly two artifacts within cap, no additional model calls |
| Acquisition cannot replace goal | `TestAcquisitionCannotEraseUnmetUserDeliverableCriteria`; even inconsistent ready/supplementary output yields partial and retained criteria/evidence |
| Relevant evidence beyond first eight | `TestGoalSupportingEvidenceSurvivesBoundedReportSelection` |
| Checkpoint integrity | Existing corruption/lease/recovery tests plus cached ancestor-event and strict decoder/cache-mutation tests |

The 1–10-round worker matrix deliberately uses explicit four-step round caps to
test round counts/recovery with a controlled workload. Nominal larger shapes and
query totals are separately tested in domain admission and transactional storage.
All source/model behavior in this matrix is scripted; a fake `supported` verdict
is not a factual-quality measurement.

## 5. Regression status and failure classification

| Check | Result |
| --- | --- |
| `CGO_ENABLED=1 go test -tags sqlite_fts5 ./... -timeout=20m` | PASS, all packages; some cached; orchestrator 301.875s |
| `go vet -tags sqlite_fts5 ./...` | PASS |
| Targeted `go test -race` on storage/domain/config round/cap/cache invariants | PASS; not a full race-suite claim |
| frontend `npm test -- --runInBand` | PASS, 77/77 |
| `npx tsc --noEmit --pretty false` | PASS |
| `npm run build` | PASS, unchanged assets |
| `npm run test:browser` | PASS, mocked API/SSE browser checks |

The first full Go run failed `TestDoctorProjectsFetchTempReadinessWithoutConfiguredPath`:
its unrelated localhost:8000 dependency was unavailable and doctor correctly
returned 503. This was a non-hermetic fixture dependency, not a fetch-temp product
failure. The test now uses a local `httptest` models endpoint; the original HTTP
200, warning projection and private-path non-disclosure assertions remain intact.
No test was skipped and no assertion was weakened. Subsequent full suite passed.

## 6. Changed files

- Domain: `research.go`, `round_plans.go`, `research_admission.go`, `task.go`,
  `research_report.go`, and admission tests.
- Config/app: `config.go`, `research.go`, new `research_caps.go`, typed defaults,
  cap/profile/env-isolation tests, `.env.example` and README.
- Storage: new `round_plans.go`/tests; continuation, session/counter/remaining-budget
  handling; report byte admission; runtime projection; existing checkpoint/context
  integrity hot path and its bounded validation memo.
- Orchestrator: round selection/working set, completion governor, planner schema,
  goal verification, report selection/budgets, deterministic delivery, context and
  legacy compatibility; worker/deliverable/report/recovery tests.
- API: only isolation of the fetch-temp doctor test's model endpoint.

Tracked user changes were absent at entry. No user data, migration, evidence,
checkpoint history, report artifact, route or service unit was deleted/overwritten.
Parallel read-only agent launches were attempted for Phase 1 but rejected by the
harness usage limit. Implementation/review used direct source inspection and tests;
no new agent review result is claimed.

## 7. Proof boundaries and stop gate

- **Implemented:** new v2 round architecture, separate budgets, substantive criteria,
  deterministic byte-limited partials and preserved v1 compatibility.
- **Scripted-test verified:** matrix above, existing regression suite and targeted
  race checks.
- **Real-model verified:** **Not established for these changes**.
- **Live acceptance verified:** **Not established for these changes**.
- **Not established:** useful real Deep Research quality, actual 10-round hardware
  execution, 32/64/96/128K ladder, real offline comparison, live new-path restart,
  UI redesign/accessibility screenshots and staged elapsed soak.

No deployment/restart, model/context configuration change or Phase 2 experiment
was performed. Legacy v1 runs retain their old explicit 12-step contract; new v2
research has no cumulative 12-step bottleneck. Explicit resource/operation/report
budgets and an absolute deadline remain mandatory. Evidence novelty is not an
independent semantic truth judgment; further quality/acceptance work stays gated.

**Stop after the Phase 1 commit. Await `Proceed to Phase 2`.**
