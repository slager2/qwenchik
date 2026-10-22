# Phase 5: runtime projection, SSE and notifications

## Runtime and UI

`GET /api/runs/{id}/runtime` reads a consistent SQLite projection: phase/step, deadline and elapsed/remaining time, immutable budget and durable counters, context identity, readiness verdict, contradictions, continuation/compaction counts, resource pause and notification state. Runtime telemetry comes from the existing host monitor and sleep provider. Unknown measurements stay unknown. Role inputs, private working notes, task prompts, model output and provider credentials are excluded from this projection.

The research panel shows runtime/budget/resource status, explicit partial/blocked/budget-exhausted outcomes, readiness verdicts, contradictions, a three-column step timeline and a journal of committed compaction/continuation/verification/resource transitions. Manual pause/resume uses existing task commands. Resource-guard and recovery-required pauses cannot be bypassed through the resume button. Cancel remains available. Closing the browser does not own or cancel the server worker.

## SSE contract

The stream sends a canonical runtime `snapshot`, replays committed events after `Last-Event-ID` (or `after`), then polls live committed SQLite events. Message text is reconciled through the existing canonical conversation-message snapshots and chunk-aware reducer. Snapshot frames do not advance the replay cursor. An invalid/foreign/ahead cursor or a missing sequence triggers `snapshot` plus `resync` with a committed cursor; the client reloads canonical state rather than concatenating potentially missing text.

Public event pagination scans past internal-only pages instead of falsely returning an empty history. A bounded scan ceiling returns an explicit synchronization error. New public events are `context.compacted`, `continuation.requested`, `verification.completed`; resource events retain their existing allowlist. New event projections expose only bounded counters, hashes, enum fields, step IDs and missing criteria. Internal invocation inputs and notification claims remain private.

## Event-backed notification outbox

No SQL table or migration is added. A newly committed terminal event contains `notification_schema_version=1` and is itself the outbox item. Older terminal history is not retroactively notified. Notification delivery runs independently of the inference worker and cannot change task status, evidence, counters or terminal outcome.

`notification.attempted` commits an exclusive 30-second claim before external I/O. Delivery and acknowledgement occur outside that transaction. `notification.failed` schedules bounded backoff; `notification.delivered` or `notification.abandoned` settles the item. There are at most three attempts. Claim identity fences late acknowledgements after restart/claim expiry. Switching channels does not resend a settled item. No exactly-once external-delivery guarantee is made: a crash after delivery but before acknowledgement can duplicate a notification.

Notification content is a static outcome sentence. It contains no task title, user prompt, raw answer, source content, cookies or credentials. Desktop delivery uses the native `notify-send` executable directly, without a shell or any model-supplied arguments. A missing desktop provider is an ordinary bounded delivery failure.

Configuration:

| Variable | Default | Contract |
| --- | --- | --- |
| `NOTIFICATION_CHANNEL` | `desktop` | `desktop`, `none`, or explicit opt-in `ntfy` |
| `NTFY_URL` | empty | HTTPS topic URL without embedded credentials, query or fragment |
| `NTFY_TOKEN` | empty | Optional authorization token; never included in event/error payloads |

The ntfy client has a bounded timeout, refuses redirects and ignores remote response content. Telegram/Apprise adapters are not enabled. `none` disables dispatch; queued versioned events remain durable.

## Recovery and verification

Restart the server normally to resume pending notification claims after their lease/backoff. Do not edit events to force a resend. Failed delivery is visible in runtime status and does not downgrade or retry the research task. Downgrade can ignore the new fields/events; it will stop dispatching this outbox until a compatible binary returns. No database rollback is required.

Full Go tests, vet and race pass with `sqlite_fts5`. The frontend has 36 passing tests, typecheck, production build and Chrome smoke at widths 320/375/414/768/1440. Tests cover outbox atomic rollback, concurrent claims, restart, unknown delivery, late acknowledgement fencing, finite failure escalation, provider-error isolation and bounded static ntfy payloads. API tests verify private-context exclusion, snapshot/resync, safe event projection and replay through internal-only pages. UI tests verify unknown telemetry, guard pauses, unsafe-resume prevention, contradiction escaping and independent delivery failure.

Versioned AgentBundle/refinement acceptance is tracked separately; these changes do not fabricate a bundle hash or claim successful isolated PDF extraction when the extractor is unavailable.
