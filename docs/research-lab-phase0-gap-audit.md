# Research laboratory — Phase 0 baseline and gap audit

Baseline: `15deaf5d29b30b1bd01d5d9fc3f2cb98c3bc5009` (`main`).
Scope: the user's new laboratory roadmap, **Phase 0 only**. Older documents use
different phase numbers. Four parallel read-only audits were reconciled against
current code; no runtime code, configuration, schema or tests were changed.

## Baseline verification

Executed against this checkout:

| Command | Result |
| --- | --- |
| `CGO_ENABLED=1 go test -tags sqlite_fts5 ./...` | PASS, all Go packages; some package results cached |
| `go vet -tags sqlite_fts5 ./...` | PASS |
| `npm test -- --runInBand` in `frontend/` | PASS, 74 tests using the existing Node test runner |
| `npx tsc --noEmit --pretty false` in `frontend/` | PASS |

No sandbox/IPv6/httptest environment failure occurred in these checks. Passing
fake-runner/mocked tests does not establish rootless Docker, real PDF extraction,
search-provider health, hardware temperature safety, or overnight operation.
No new live research, provider probe, inhibitor acquisition or soak was run.

## Implemented

- Go modular monolith, SQLite/WAL, sequential durable worker, task leases/fencing,
  invocation intent/reservations, checkpoint integrity and restart reconciliation.
- Local llama.cpp-compatible inference. Config rejects `LLM_MAX_CONCURRENCY != 1`;
  `GuardedClient` has one shared slot for worker/API doctor. It cannot serialize
  unrelated clients calling the model server directly.
- Chat/Research separation; semantic planner repair, lossless bounded facet merge,
  required fetch/dependency validation, safe planning-failure diagnostics and UI.
- Search-provider adapters, URL ledger, SSRF/DNS/redirect guards, HTML extraction,
  PDF preflight/policy, raw/text artifacts, hashes, evidence and quote spans.
- Evidence-readiness criteria, citation ownership/format/excerpt checks, explicit
  partial/fallback delivery, Retry/Continue and Research Context Bridge.
- Resource pause/probes/backoff, telemetry, opt-in Linux sleep inhibitor,
  event-backed notification claims/retries independent of final task status.

Sources: `internal/orchestrator/{worker.go,plan.go,planner_failure.go}`,
`internal/llm/guard.go`, `internal/config/config.go:361`,
`internal/storage/sqlite/{invocations.go,checkpoint_integrity.go,leases.go}`,
`internal/tools/research.go`, `internal/runtime/{resources,keepawake,notifications}`.

## Actual lifecycle

`queued -> planning -> running -> verifying -> completed/failed/cancelled` is the
status path. **Search/fetch/extract/verification/finalization are stages, not extra
task status enums.** Pause and approval are additional status branches.

1. `ClaimNext` claims queued work and enters planning, assigning a lease if enabled.
2. `executeAttempt` creates/normalizes/validates and durably saves the plan.
3. Saved steps execute through the registered search/fetch tools and policy gates.
4. `fetch_url` performs download, then the extraction-stage observer and extractor;
   successful raw/text metadata and evidence commit through `SaveResearchSource`.
5. Execution coverage/source floors precede `verify`; only then finalization and
   `CompleteTask` commit the answer and outcome. Exhausted verification continuation
   can commit an explicitly limited partial answer.

Read: `internal/storage/sqlite/store.go` (`ClaimNext`, `CompleteTask`, recovery),
`internal/orchestrator/worker.go` (`runTask`, `execute`, `executeAttempt`),
`internal/tools/research.go`, `internal/storage/sqlite/research_source.go`.

## What is actually persisted

There are **11 migration files**. These are source-schema findings, not a fresh
inspection of the running database's applied-migration ledger.

| Store | Persisted fields/content |
| --- | --- |
| `tasks` | Request/title/description; status/stage/priority; `plan_json`, version/timestamps/idempotency; conversation/user/assistant linkage, mode/operation/continue/retry; cancel flag, heartbeat/start/completion/deadline; error code/message; lease owner/epoch/expiry; pause/resource reason, resume time/probe attempts; terminal outcome |
| `events` | `id, task_id, seq, type, schema_version, created_at, stage, visibility, payload_json`; unique task/sequence; invocation, reservation, verification, continuation, terminal and notification records live in JSON payloads |
| `checkpoints` | `id, task_id, plan_version, state_json, last_event_seq, created_at, parent_id, state_schema_version, state_hash, context_snapshot_hash` |
| `research_sessions` | `id, task_id, conversation_id, goal, status, profile`; policy/budget/model snapshot/counter JSON; `current_round, current_phase, plan_version, stop_reason`; start/deadline/heartbeat/create/update times |
| `evidence` | Task/provider/query; original/canonical URL, title/language/retrieval/HTTP status; content SHA-256, extraction method, raw/text artifact IDs, quote-span/quality-flag JSON, excerpt; dedup by task/URL/content hash |
| `artifacts` | `id, task_id, name, mime_type, sha256, size_bytes, storage_path, created_at`; actual bytes in task-owned filesystem storage, not SQLite blobs |

Checkpoint JSON additionally carries completed/skipped steps, pending tool and
approval state, tool results, query histories, consumed bytes, failed/rejected URLs,
source failures, partial flags and completion fingerprint/missing criteria.
`context_snapshot` contains bounded working-set strings, accepted evidence IDs,
open-question strings, contradictions, constraints and remaining budgets; role
input pins live in the same existing checkpoints.

Questions/frontier can survive as opaque future JSON, but this is **not** a typed,
validated adaptive frontier. A text artifact has its own hash; there is no
first-class source-note record unifying raw/text hashes with claims/coverage.
Evidence taint is reconstructed by readers, not a separate SQL column.

Submission, step-result, verification/continuation and terminal writes have
transactional boundaries. Source metadata commits atomically in SQLite, but files
are written before metadata: rollback may leave immutable orphan files. External
notification delivery is retryable/event-backed, not exactly-once.

Read: migrations `001`, `002`, `003`, `008`, `009`, `010`, `011`;
`research_sessions.go`, `context_snapshot.go`, `context_inputs.go`,
`research_source.go`, `notifications.go`, `internal/storage/artifacts/store.go`.

## Partially implemented

| Area | Existing capability | Gap against the laboratory goal |
| --- | --- | --- |
| Adaptive research | One targeted completion continuation appends a search/fetch pair; fingerprints queries, executed steps and evidence; checks remaining budgets | Not an evidence-driven multi-round state machine; no typed question frontier/selection or sustained stagnation protocol |
| Coverage | Addressed plan criteria, owned evidence, supplementary/critical misses and contradictions | No question/topic coverage lifecycle; execution criteria are not automatically research-subject coverage |
| Compact working set | Bounded excerpts, role compaction/pins/context snapshots; scoped evidence/artifact lookup tools | No automatic structured source notes or research-round note selection/update pipeline |
| PDF | Real pure-Go synthetic-PDF parsing tests; isolated fake-runner tests; capability preflight and failure fallback | No freshly proven live rootless fixture extraction or capability-state acceptance in this audit |
| Overnight | Persisted deadlines, recovery, inhibitor API, resource backoff, telemetry, notifications | No 15-minute/60-minute/4-hour staged acceptance or lid/firmware guarantee |
| Inspector | Steps, sources, evidence, readiness gaps, partial/failure and telemetry | No explicit round/frontier/question history or structured-report artifact UI |

Key proof: `internal/orchestrator/completion.go:100` and
`internal/storage/sqlite/completion.go:43` enforce **at most one continuation**.
The latter appends exactly two steps. `RESEARCH_ADAPTIVE_ENABLED=true` is still
rejected by `internal/config/research.go:29`. Profile round/question/stagnation
ceilings are not an implemented adaptive algorithm.

## Missing

- Opt-in adaptive controller with durable round decisions, frontier/open-question
  transitions, criterion progress, new targeted queries and finite stagnation stop.
- Typed source notes linking excerpts, source metadata/hashes, claims, supported
  and unresolved criteria, quality and failures; automatic compact working set.
- Section outline/generation/validation/checkpoint assembly and `research_report`
  artifact with methodology, limitations, evidence coverage and section statuses.
  The current finalizer explicitly requests short paragraphs and forbids headings
  (`internal/orchestrator/citations.go:145-154`).
- Explicit `local_inference_external_search`, `local_searxng`, and `offline`
  privacy contracts. `local_primary` is provider routing, not an offline mode;
  local SearXNG can still call public engines.
- Broad 10–20-task offline research benchmark/held-out comparison. Existing
  refinement evaluation has four synthetic microcases, not laboratory benchmarks.
- Exact regression request: «сделай роадмап по биоинформатике, химии и информатике».
  Current Russian four-facet tests cover university/grant discovery and preserve
  facets; the worker integration deliberately stops at a fake search failure.
- Verified end-to-end overnight acceptance satisfying all fourteen user criteria.

## Misleading or stale documentation

- README's concurrent provider-merging description conflicts with current default
  `SEARCH_PROVIDER_POLICY=local_primary`; only explicit `parallel` uses that policy.
- Historical Research Phase 1 scope exclusions for guards/notifications/session UI
  are no longer a complete current capability inventory.
- `docs/harness-phase4-completion.md` omits newer `PARTIAL` readiness and describes
  exhausted continuation as blocked without the newer partial-delivery qualification.
- Old phase numbering, including “adaptive unavailable until Phase 3”, does not
  match the user's new roadmap. API route inventory is incomplete.
- Historical “33 tests”, PDF download and short Chat/research acceptance records
  must not be read as today's full-suite count or proof of overnight reports.
- Inspector counts skipped steps as processed progress; this is not evidence
  coverage. “No saved plan / before search” currently relies on event history and
  planning diagnostics, not an independently queried durable round history.

These documents are left unchanged in Phase 0; this report records the mismatch.

## Proof boundaries and next gate

**Freshly tested:** the baseline commands above, including existing planner,
partial-answer, PDF-policy, ownership, recovery and guard regressions.
**Historical live evidence only:** reports describe real Chat/restart/notification
smokes and a 67-second two-source partial research run; the incident PDF test
proved download, not isolated extraction. No fresh live laboratory run here.
**Not established:** adaptive research quality, sectioned reports, multi-hour soak,
real lid/logout handling, external-provider availability or useful report length.

Qwen 4B remains constrained by context/output limits, sequential inference time,
duplicate queries, semantic/citation errors and same-model critic limitations.
Backend bounds and exact-excerpt checks cannot guarantee independent factual truth.

Existing refinement/CandidatePatch submission/promotion surfaces are present.
Scope control requires freezing their expansion; do not use them to substitute
for the adaptive-research and report work. No refinement job was started here.

Next authorized step is **Phase 1: stable foundation and test contract**, including
the exact Russian roadmap regression. Preserve the user's phase order; implement
neither adaptive mode nor the report writer during this audit. Wait for
`Proceed to Phase 1`.
