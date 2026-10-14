# HTTP API contract

Base path: `/api`. JSON errors use `{"error":{"code":"...","message":"...","retryable":false}}`; implementation details and secrets are not returned.

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/tasks` | Create an inbox task; supports `Idempotency-Key` |
| `GET` | `/tasks` | List tasks |
| `GET` | `/tasks/{task_id}` | Task and current durable state |
| `POST` | `/tasks/{task_id}/messages` | Persist user message and queue work |
| `POST` | `/tasks/{task_id}/run` | Queue task; supports `Idempotency-Key` |
| `POST` | `/tasks/{task_id}/pause` | Request pause at a safe worker boundary |
| `POST` | `/tasks/{task_id}/resume` | Resume a paused task |
| `POST` | `/tasks/{task_id}/cancel` | Cancel and prevent future tool actions |
| `GET` | `/tasks/{task_id}/events?after=N` | Bounded event replay |
| `GET` | `/tasks/{task_id}/events/stream` | Reconnecting SSE |
| `GET` | `/tasks/{task_id}/checkpoints` | Checkpoint history |
| `GET` | `/tasks/{task_id}/messages` | Conversation history |
| `GET` | `/health` | Process and storage health |
| `GET` | `/doctor` | Runtime/provider checks, with no secret values |

Mutations are idempotent when an idempotency key is supplied. Unknown task IDs return 404. Invalid lifecycle transitions return 409; malformed request bodies return 400. Create returns 201; accepted worker commands return 202.

The server defaults to `127.0.0.1:8080`. Non-loopback binding is rejected unless a future explicit LAN mode also provides authentication.
