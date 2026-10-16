# Event and SSE contract

## Durable envelope

```json
{
  "id": "event_uuid",
  "task_id": "task_uuid",
  "seq": 42,
  "type": "task.status_changed",
  "schema_version": 1,
  "created_at": "RFC3339",
  "stage": "planning",
  "visibility": "user",
  "payload": {}
}
```

`seq` is monotonically increasing within a task. Events are append-only. A task state mutation and its event are committed in the same transaction. Internal/audit events are not sent to user-facing SSE without explicit redaction.

Public event types include `task.created`, `task.status_changed`, `task.recovered`, `message.created`, `message.delta`, `plan.created`, `step.started`, `step.completed`, `step.skipped`, `tool.started`, `tool.completed`, `evidence.created`, `artifact.created`, `approval.requested`, `approval.approved`, `approval.rejected`, `approval.expired`, `task.completed`, `task.failed`, and `task.cancelled`. `step.skipped` records a conditional plan step that was intentionally not executed. `checkpoint.saved`, `policy.decision`, `approval.consumed`, and `memory.retrieved` remain internal/audit. This list is additive; existing event types and payloads retain their meanings.

`message.delta` is emitted only for the final user-facing synthesis and only with `visibility: "user"`. Its payload is `{ "stream_id": string, "message_id": string, "chunk_index": number, "delta": string }`. Chunk indexes start at zero and increase consecutively for a stream. Each persisted delta is bounded; stream IDs are fresh for each synthesis attempt, including citation corrections. Deltas are provisional text, not a finalized assistant message or lifecycle authority. Only after citation validation succeeds does `CompleteTask` persist the canonical assistant message and its `message.created` event, using the final stream's `message_id`.

## SSE

- `GET /api/tasks/{task_id}/events/stream`
- `id:` equals durable task `seq`; `event:` is an allowlisted event type; `data:` is the envelope JSON.
- `Last-Event-ID` replays events after the supplied sequence in ascending order.
- Heartbeat comments are emitted while waiting; stream buffering and replay are bounded.
- A newly connected client can fetch events with `GET /api/tasks/{task_id}/events?after=N` before subscribing.
- Terminal task events are persisted and delivered like all other events.

No model-generated token or progress event is trusted as lifecycle state. Streamed model text is exposed only as provisional `message.delta` events and is not an event authority.
