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

Initial public event types: `task.created`, `task.status_changed`, `message.created`, `plan.created`, `step.started`, `step.completed`, `tool.started`, `tool.completed`, `evidence.created`, `artifact.created`, `approval.requested`, `task.completed`, `task.failed`, `task.cancelled`. `checkpoint.saved` and `policy.decision` remain internal.

## SSE

- `GET /api/tasks/{task_id}/events/stream`
- `id:` equals durable task `seq`; `event:` is an allowlisted event type; `data:` is the envelope JSON.
- `Last-Event-ID` replays events after the supplied sequence in ascending order.
- Heartbeat comments are emitted while waiting; stream buffering and replay are bounded.
- A newly connected client can fetch events with `GET /api/tasks/{task_id}/events?after=N` before subscribing.
- Terminal task events are persisted and delivered like all other events.

No model-generated token or progress event is trusted as lifecycle state. Streamed model text, when enabled, is a message delta only and is not an event authority.
