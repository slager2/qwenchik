# Research Phase 1: immutable sessions and bounded fixed-plan execution

## Scope and status

Phase 1 adds persisted research-session policy/model snapshots, absolute deadlines, and durable operation reservations around the existing fixed-plan research worker. It does not replace that worker with an adaptive research loop. `RESEARCH_ADAPTIVE_ENABLED=false` is the supported configuration; `true` fails configuration loading with a Phase 3 availability error. Larger named profiles are ceilings, not a promise to perform that many searches, rounds, questions, or hours of work.

Phase 1 verification completed on 21 October 2026. Full Go tests, vet and race, frontend tests/build/typecheck and mocked Chrome checks passed. The deployed application applied migration 009 and completed a bounded local-model snapshot smoke described below. The earlier [Chat report](chat-research-implementation.md) and [PDF repair report](pdf-limits-repair.md) retain their historical results and limitations; they do not establish adaptive-research acceptance.

## Canonical profiles and trusted server caps

The single numeric source is [internal/domain/research_defaults.go](../internal/domain/research_defaults.go):

- `ResearchBudgetForProfile(name) (ResearchBudget, bool)` defines the five nominal budgets.
- `DefaultResearchPolicy() ResearchPolicy` builds the standard policy and fallback.
- `DefaultTaskFetchBudgetBytes` is 134,217,728 bytes, or 128 MiB.

Config delegates to those functions through `ResearchProfile(name)` and `ResearchPolicies()`; app fallback delegates to the same standard policy. The effective policy combines the canonical budget with validated server fetch/PDF settings. Returned policies and per-run parameters are value copies; modifying caller-owned maps or a previous run's values does not change the service's defaults.

| Profile | Wall ceiling, seconds | Rounds ceiling | Nominal queries | Queries with server default 12 | Fetches | Logical LLM calls | Evidence | Questions created / accepted | Stagnant rounds ceiling |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| `quick` | 300 | 1 | 4 | 4 | 4 | 10 | 4 | 6 / 2 | 1 |
| `standard` | 1800 | 3 | 8 | 8 | 8 | 24 | 8 | 16 / 6 | 2 |
| `deep_laptop` | 7200 | 5 | 16 | 12 | 12 | 40 | 12 | 30 / 10 | 2 |
| `long_laptop` | 14400 | 8 | 32 | 12 | 24 | 72 | 24 | 60 / 20 | 2 |
| `overnight_laptop` | 21600 | 10 | 48 | 12 | 36 | 96 | 32 | 80 / 24 | 2 |

Every profile has `MaxSteps=12`, further clamped by trusted `MAX_STEPS`. Effective search capacity is `min(nominal queries, MAX_SEARCH_QUERIES)`, with the server default 12. In particular, long/overnight nominal limits are **32/48**, not 12. Raising the trusted server cap can expose those nominal ceilings but does not create an adaptive loop. Fetch/LLM/evidence limits are not reduced merely because a fixed plan may finish with fewer operations.

The typed citation envelope stays within the legacy synthesis path: `MaxCitationSources=8` except quick's 4, and `MaxCitationClaims=8` for every profile. These fields do not implement section-based report generation or Phase 4 reporting. Round/question/stagnation fields are budget/checkpoint data for later algorithms; Phase 1 does not claim actual adaptive iterations or automatic question-frontier expansion.

## Configuration and conversation settings

Relevant server settings in [.env.example](../.env.example):

```dotenv
RESEARCH_DEFAULT_PROFILE=standard
RESEARCH_ADAPTIVE_ENABLED=false
MAX_STEPS=12
MAX_SEARCH_QUERIES=12
TASK_TIMEOUT_SECONDS=7200
MAX_FETCH_BYTES=134217728
```

Config is read at startup. `TASK_TIMEOUT_SECONDS` accepts 1–21600 seconds and remains the legacy task timeout; a session-backed research run is governed by its stored absolute deadline instead. The worker constructor's default `MaxFetchBytes` and config both use `domain.DefaultTaskFetchBudgetBytes` (128 MiB); the default fetch-policy constructor uses the same cumulative limit. Ordinary source/PDF defaults are still 16/64 MiB, and explicit valid PDF/resource settings are copied into the policy rather than replaced by a named profile. Implicit source/PDF byte defaults can shrink to fit a smaller task budget; explicit impossible source/task relationships are rejected.

The existing conversation settings surface accepts:

- `research_settings.web_enabled`: boolean; false prevents web research through backend checks.
- `research_settings.profile`: one of the five profile names.
- `research_settings.time_budget_seconds`: integral 60–21600 seconds.
- `model_profile.temperature`: finite 0–2, including explicit zero.
- `model_profile.top_p`: finite value in `(0, 1]`.
- `model_profile.max_output_tokens`: integral 1–32768, also bounded by the configured output cap and the context's 2304-token reserve for research.

For example, this selects a long profile with a six-hour wall allowance while retaining its other budget ceilings and the server's effective 12-query cap:

```json
{
  "mode": "research",
  "model_profile": {
    "temperature": 0,
    "top_p": 0.8,
    "max_output_tokens": 2048
  },
  "research_settings": {
    "web_enabled": true,
    "profile": "long_laptop",
    "time_budget_seconds": 21600
  }
}
```

The optional time setting replaces only the wall allowance. It can shorten or extend a profile's default wall ceiling within 60–21600 seconds, but it does not increase searches, fetches, logical/HTTP model calls, evidence, questions, citations, or output tokens. Longer wall allowance is not a request for additional work after the fixed plan has completed.

Accepted modes remain `chat` and `research`; profile names are not new modes. New research sends use the research snapshot path after resolving the requested/conversation mode. Chat keeps its existing tool-free generation path. Profiles do not grant capabilities: web-enabled checks, registered-tool role/risk gates, allowlists, and source authorization still govern execution. Conversation settings cannot select arbitrary model names, endpoints, credentials, or stop sequences.

## Model snapshot and request application

Serve construction calls `SetResearchRunDefaults(defaultProfile, policies, model)` before serving requests, after capping the configured context to server-reported runtime capacity. A new research send freezes:

- profile, effective policy, and budget;
- model identifier and credential-free loopback `BaseURL`;
- effective context length and maximum output tokens;
- temperature, top-p, and empty stop sequences;
- model snapshot `Version=1`, `PromptVersion=local-research-v1`, and `SchemaVersion=1`.

The endpoint/model identifier are pinned for the session; this is not a checksum of model weights or a saved model KV cache. Credentials are not part of the snapshot or checkpoint. Authentication comes from the currently configured running model client, so current LLM credentials are never persisted with the session.

The worker loads the persisted session and wraps its model client with `sessionModelClient`. Its `prepare` method runs before both `Complete` and `Stream`: it reserves the logical call, sets the saved model/base URL/stop sequences, bounds requested output by the saved cap, applies saved sampling defaults, and sets `TemperatureSet`/`TopPSet`. The HTTP adapter uses these flags to distinguish an explicit zero temperature from an unspecified default. New Chat runs also apply immutable model settings, stored in existing checkpoint JSON rather than in research-session rows; Chat remains tool-free and does not acquire research counters. Session-less legacy runs without a stored snapshot retain their prior behavior.

Role-specific requests may use a smaller output allowance and an explicit sampling override. The citation critic now uses `Temperature: 0, TemperatureSet: true`; regression tests confirm that the run's nonzero saved temperature cannot replace this zero. Budget rejection during a critic repair is preserved as `research_budget_exceeded`, not converted into a successful citation fallback.

## Durable budgets: logical calls and model HTTP attempts

Reservations are written atomically to session counters and internal `research.operation_reserved` events before work is invoked. Deadline/cancellation/terminal-state checks and budget checks can reject an operation before dispatch. Logical calls include planner/action/verification/synthesis/critic invocations through the session client; a repair that invokes the client again is another logical call.

`ReserveResearchLLMCall` charges one `LLMCalls` and one initial `HTTPAttempts`. The OpenAI-compatible adapter can make at most one extra request for a recognized unsupported JSON-schema response, falling back to JSON mode. The session callback reserves this extra HTTP attempt before the fallback request. The bounds are:

```text
LLMCalls <= budget.MaxLLMCalls
HTTPAttempts <= 2 * budget.MaxLLMCalls
```

The second bound counts model completion HTTP attempts, not web search-provider requests or source-download HTTP traffic. A transport/model failure or process restart does not refund reservations; a reservation can remain charged even if the process stops before dispatch. This is conservative accounting, not a statement that every reservation produced an HTTP request. The existing single-slot inference guard remains the concurrency mechanism.

Search/fetch operations also reserve their count before tool execution. Cumulative fetched bytes include failed download/extraction work under the existing source policy; evidence capacity remains separately bounded. Session counters, reservations, existing tool events, and checkpoints are reconciled monotonically so a stale checkpoint cannot reset spent budgets. `research_budget_exceeded` identifies a research reservation limit; `run_deadline` identifies the persisted research deadline.

## New sends, retries, and same-run restart

| Operation | Snapshot | Counters and deadline |
| --- | --- | --- |
| New research send | Build from the configured default or selected profile, trusted effective policy, and validated conversation model overrides. | Fresh counters; absolute deadline from submission time plus the effective wall allowance. |
| Retry / Continue of a session-backed run | Clone the prior immutable profile, policy, budget, and model snapshot exactly into a new task/run/session. | Fresh counters, round/plan state, and a new deadline using the cloned wall allowance. |
| Same-run recovery after process restart | Reuse the same session and snapshot. | Retain reservations/counters and the original absolute deadline; queue time and downtime consume the allowance. |

Retry/Continue retain the existing latest-terminal-run and conversation ownership checks. Continue requires nonempty prior assistant text and links to it. Storage clones the old snapshot transactionally, so later conversation/default changes cannot silently alter a retry/continue. A new send is the point at which changed conversation settings are selected. `web_enabled=false` still blocks a research retry/continue; snapshot cloning does not bypass current web permission.

At execution or recovery entry, the worker compares the snapshotted fetch/PDF resource/security policy with the startup-constructed runtime policy, excluding the selected profile/budget from that comparison. A mismatch, or runtime context too small for the saved model context, produces the typed `research_runtime_changed` failure. The worker does not silently replace the saved source/PDF limits or fallback permission. Editing `.env` alone does not hot-reload an active process; this check applies when the constructed runtime is used for execution/recovery, including after a restart with changed configuration.

## Persistence and migration 009

[009_research_sessions.sql](../internal/storage/sqlite/migrations/009_research_sessions.sql) adds **one table**, `research_sessions`, and `idx_research_sessions_conversation`. It does not create separate round, question, or run tables.

| Field group | Stored columns |
| --- | --- |
| Identity and goal | `id`, unique `task_id` referencing `tasks`, `conversation_id` referencing `conversations`, `goal` |
| Immutable run settings | `profile`, `policy_json`, `budget_json`, `model_snapshot_json` |
| Execution state | `status`, `counters_json`, `current_round`, `current_phase`, `plan_version`, `stop_reason` |
| Lifecycle | `started_at`, absolute `deadline_at`, `heartbeat_at`, `created_at`, `updated_at` |

A new research task, messages, idempotency record, and session are accepted in the same submit transaction. Session JSON is versioned, bounded, and validated; task identity, snapshot versions/profile, finite model values, and counter ranges are checked on reads as well as writes. Sessionless Chat does not acquire a research session or research counters.

Checkpoints use the existing `checkpoints` table and JSON state, with `research_schema_version=1` and `session_id`. The session row is authoritative for immutable settings and durable reservations; checkpoint values cannot replace those with caller-provided settings or lower spent counts. Unknown future frontier/question fields are preserved through compatible checkpoint merges. Full adaptive round/question frontier state belongs in checkpoint JSON rather than additional Phase 1 tables; preserving such fields does not activate the future algorithm. Foreign session IDs and unsupported research envelope versions are rejected.

Existing tasks and conversation research runs without a session are **not backfilled** by migration or by read/reservation helpers. The worker keeps the session-less legacy behavior for those existing runs. A newly submitted API retry/continue of a legacy run resolves current trusted application defaults once when no prior snapshot exists; it does not invent a historical snapshot or use a mismatched direct-storage model context. Direct storage callers without supplied defaults retain a validated canonical standard fallback. Once a snapshot exists, retry/continue clone it rather than resolving changed conversation/default settings.

Compatibility assertions are encoded in `internal/storage/sqlite/migration_test.go`:

- `TestResearchSessionMigrationAddsExactlyOneTableAndPreservesAllData` compares existing table contents and schema objects, checks that only `research_sessions` was added, verifies no session backfill or read-triggered backfill, and checks repeat migration, FTS schema/triggers, and foreign keys.
- `TestResearchSessionMigrationUpgradesEveryExistingVersion` covers migration from versions 1 through 8 while retaining existing task/message/step/checkpoint and FTS behavior.

These migration assertions passed, including upgrades from every version 001–008. Lifecycle/reservation/clone/checkpoint assertions also passed in `internal/storage/sqlite/conversations_test.go`. Chat snapshots use `model_snapshot_schema_version=1` and `model_snapshot` in existing checkpoints; terminal completion, retry/continue and database reopening preserve them without creating Chat session rows.

## Existing web/PDF security and phase boundaries

The existing [web research contract](contracts/research.md) continues to govern discovery, authorized source URLs, SSRF/allowlists, provenance, and accepted evidence. The default model endpoint is loopback-only; source fetching still rejects loopback/private destinations. Named profiles do not relax either boundary.

The PDF repair defaults remain: 128 MiB task bytes, 16 MiB ordinary source bytes, 64 MiB PDF bytes, 20 pages, 50,000 extracted Unicode characters, 30-second extraction and 120-second download deadlines, 512 MiB memory, 64 MiB temporary memory filesystem, 1 CPU, and 32 PIDs. Artifact/sandbox-file/fetch-temp quotas are independent. Rootless Docker and a configured immutable local extractor image are required for isolated PDF extraction. Rootful Docker is insufficient. With unsandboxed fallback disabled, missing isolation yields `pdf_extractor_unavailable`; Phase 1 does not silently enable the helper or establish that previously blocked PDFs are now parseable. The historical download-only PDF result remains described in the repair report.

Phase 1 does not implement adaptive gap/question rounds, section-based generation, an external independent judge, notifications, or sleep/power/runtime-lifecycle guards. It adds no session-specific HTTP routes or UI controls. Session API/UI exposure belongs to Phase 6, outside this contract; the existing conversation/run interfaces continue to be the submission and display surfaces.

## Verification results

All commands completed successfully:

```sh
CGO_ENABLED=1 go test -tags sqlite_fts5 ./...
go vet -tags sqlite_fts5 ./...
CGO_ENABLED=1 go test -race -tags sqlite_fts5 ./...
npm --prefix frontend test
npm --prefix frontend run typecheck
npm --prefix frontend run build
npm --prefix frontend run test:browser
git diff --check
```

Frontend reported 33 tests. Browser checks used mocked API/SSE with system Chrome, including replay/cancellation, independent panels and mobile widths; these are not new live adaptive-research checks. New worker tests cover the actual snapshot at all research call paths, explicit critic zero, denial before inference/provider execution, failed-call charging, extra HTTP fallback charging, same-run recovery, budget persistence, legacy requests, Chat snapshot application and fail-closed runtime incompatibility. No race-detector failures were reported.

After confirming no active queued/planning/running/verifying runs, the executable was rebuilt and `qwenchik-app.service` restarted. `/api/health` reported database `ok`, attached worker and fresh heartbeat. A controlled run `3c589efc-c656-4ae1-8a28-641767f9a09a` in conversation `1d73cf14-996a-46c6-805b-a5e1c73b478c` used research mode with the simple greeting “Привет”, quick profile and an explicitly selected 60-second allowance. It completed through the existing greeting/Chat execution path, without web tools. Its durable checkpoint contained:

- `research_schema_version=1`, matching session/run ID;
- `profile=quick`, `max_llm_calls=10`, `max_search_queries=4`, fixed 60-second absolute allowance;
- saved model `local-qwen`, loopback endpoint, actual context 32768, output cap 128, `temperature=0`, `top_p=0.4`;
- one logical LLM call and one reserved model HTTP attempt;
- zero searches, fetches, evidence, questions and rounds;
- `current_phase=completed`.

This smoke confirms deployment, persistence and one real local-model completion. It does not demonstrate adaptive intake/discovery/gap/second-round behavior. The previously diagnosed duplicate-ID planning failure is not repaired by this phase and remains a separate planner contract issue. PDF isolation availability and external schema-enforcement guarantees remain unchanged.
