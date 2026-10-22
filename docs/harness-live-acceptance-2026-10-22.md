# Harness live acceptance, 2026-10-22

## Delivered and deployed

- Phase 3 role snapshots/compaction: `b4e8fac`, `635813a`.
- Phase 4 completion governor/verifier: `ea3f2c3`.
- Phase 5 runtime/SSE/event-backed notifications: `a152878`.
- Source-hashed baseline bundles: `d7bd966`.
- Bounded citation-format correction: `b9f49bc`.
- Phase 6 local refinement/gate/approval/rollback: `e687a59`.

The local application was rebuilt and restarted only after checking that no queued, executing, paused or approval-waiting tasks existed. SQLite backups were made before deployment/migration:

- `/tmp/opencode/qwenchik-before-phase45.sqlite`
- `/tmp/opencode/qwenchik-before-phase6.sqlite`

Migration 011 applied successfully. The migration ledger contains eleven entries and `PRAGMA foreign_key_check` returned no violations. The existing Docker Desktop user service was started; `ai-searxng-1` is again serving local search on port 8081.

## Observed live behavior

### Disconnected client and notifications

Run `8d7745d2-95b8-44c8-b30d-4d1452b2784e` received an SSE snapshot, then the client deliberately cancelled the stream connection. The server worker continued and completed the local-Qwen answer. Bundle/context hashes were persisted. An ahead-of-history cursor produced snapshot/resync. The native desktop provider accepted the terminal notification on attempt one.

The resource guard was enabled. A measured snapshot reported 10,197,037,056 free RAM bytes and 2,451 MiB free GPU VRAM. These are measurements from that check, not current or guaranteed values. Keep-awake remained opt-in and was not enabled by this smoke test.

### Research outcomes

Run `ed322c5b-bdc3-4f1a-8315-e8fd2031c275` completed in 67 seconds with two searches, two fetched independent sources, two accepted evidence records and four durable compaction events. Readiness verification passed, but the model omitted required citation IDs from its draft. The final deterministic check delivered labelled verbatim excerpts as `partial`, with a delivered desktop notification. Unsupported synthesis was not promoted to verified output.

The follow-up added exactly one format correction for newly bundled runs. Tests prove a corrected answer proceeds to the critic, while a second invalid answer falls back without a third generation. Subsequent live run `7739a68e-ae79-4460-b934-dd14b0e782a9` stopped before fetching because the actor repeated a search query; its outcome was correctly `blocked`, after three model calls. This was not reported as a successful research answer.

### Real local refinement

Task `58d1c8dd-ea8c-4acc-9908-f609bc4de2e0` used the two preceding runs as bounded failure-cluster provenance. The real local Qwen proposed a typed patch, completed baseline/candidate evaluation, and stopped at explicit approval:

- Baseline bundle: `34ee56e4b46b99e6e4068da6405100445d58fb7ed7a76fd5f9952877c1a04811`
- Candidate: `2a3aa7709a5d5e2183ed863a1d851476dc8748f566d5da16ebe3866481a470e0`
- Baseline: 4/4 held-out cases, 6,939 ms recorded inference latency.
- Candidate: 4/4 held-out cases, 7,386 ms recorded inference latency.
- Gate: passed; candidate `ready`, not active.
- Approval: `f9fa73e2-2e49-4080-911c-b9e39e1daf3f`, `harness.promote_bundle`, initially pending.

The proposed hints were simply `search-query-diversity` and `citation-formatting`. Equal 4/4 scores establish passage of this small non-regression fixture set, not a demonstrated quality improvement. No promotion was performed by the coding agent. The approval has the normal finite expiry; inspect current state before making a decision.

## Verification

The complete Go suite, `go vet`, and full race suite passed with `sqlite_fts5`; final targeted race checks also passed after tightening approval admission, runtime projection and citation fixture checks. Frontend: 36 tests, typecheck, production build and Chrome smoke passed. Tests cover restart/fencing/budgets, source/PDF failure handling, context preservation, no-progress ceilings, citation refusal, SSE replay/resync, outbox failure isolation, held-out rejection, approval-only promotion and rollback.

## Remaining operational limits

- The configured isolated PDF extractor is unavailable; unsandboxed fallback remains disabled. A successful download is not claimed as extraction. Source failure/fallback is tested, but a live isolated parse is not established in this environment.
- A multi-hour/overnight soak was not performed during these bounded checks. Inhibitor and resource failure/recovery paths have dedicated tests; laptop lid/firmware behavior is not inferred from them.
- Qwen can still return duplicate queries, omit citations or produce unsupported claims. The harness bounds and exposes these failures; it does not turn every request into a successful report.
- Phase 7 REPL/external-teacher experiments are not enabled. No cloud LLM fallback, second inference slot, arbitrary model-controlled shell, weight training or external orchestration framework was introduced.
