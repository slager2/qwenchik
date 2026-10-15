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
| `GET` | `/tasks/{task_id}/steps` | Backend-owned plan step states |
| `GET` | `/tasks/{task_id}/artifacts` | Artifact metadata (bytes stay in the local artifact store) |
| `GET` | `/tasks/{task_id}/artifacts/{artifact_id}` | Download one task-owned artifact |
| `GET` | `/tasks/{task_id}/evidence` | Source provenance, excerpts, quote spans, and citations |
| `GET` | `/tasks/{task_id}/messages` | Conversation history |
| `GET` | `/health` | Process and storage health |
| `GET` | `/doctor` | Runtime/provider checks, with no secret values |
| `GET` | `/providers` | Configured local and optional provider status |
| `GET` | `/approvals` | Pending approval requests |
| `GET` | `/approvals/{approval_id}` | One approval request |
| `POST` | `/approvals/{approval_id}/approve` | Approve the exact stored action |
| `POST` | `/approvals/{approval_id}/reject` | Reject the pending action |
| `GET` | `/tasks/{task_id}/approvals` | Approval history for one task |
| `GET` | `/memories` | List memories; optionally filter by `status` and `scope` |
| `POST` | `/memories` | Create a candidate memory (never accepted implicitly) |
| `GET` | `/memories/{memory_id}` | Read one memory |
| `PUT` | `/memories/{memory_id}` | Edit kind, text, and metadata |
| `POST` | `/memories/{memory_id}/accept` | Explicitly accept a candidate or stale memory |
| `POST` | `/memories/{memory_id}/reject` | Reject or remove a memory from retrieval |
| `POST` | `/memories/{memory_id}/stale` | Mark an accepted memory stale |
| `POST` | `/memories/{memory_id}/supersede` | Mark a memory as replaced |
| `GET` | `/memories/search?q=...&task_id=...&limit=...` | Bounded lexical retrieval of accepted global and task-scoped memories |

Task creation/queueing and approval decisions are idempotent when an `Idempotency-Key` is supplied. Memory status changes are idempotent by current state; candidate creation has no idempotency-key support. Unknown resource IDs return 404. Invalid lifecycle transitions return 409; malformed request bodies return 400. Create returns 201; accepted worker commands and approval decisions return 202. Approval decisions bind to the stored normalized action arguments and cannot be replayed for different arguments.

New memories always start as `candidate`. Only `accepted` records are returned by memory search; `stale`, `superseded`, and `rejected` records are excluded. `scope=task` records are only eligible when their source task ID matches the query's `task_id`.

The server defaults to `127.0.0.1:8080`. Non-loopback binding is rejected unless a future explicit LAN mode also provides authentication.
