# Phase 2 security gate

## Implemented controls

- `APP_BIND_ADDR` accepts only loopback addresses; there is no unauthenticated LAN mode.
- HTTP requests with a non-loopback `Host` are rejected, reducing DNS-rebinding access to the unauthenticated local API.
- `LLM_BASE_URL` accepts only loopback hosts, rejects URL user info/query/fragment, and must use a port different from the app.
- The local LLM adapter does not follow redirects, does not log request/response bodies, bounds response size, and never falls back to a cloud provider.
- One worker calls the model sequentially. Lifecycle transitions and progress events are backend-owned.
- A deterministic capability evaluator is the single allow/approval/deny decision point for registered tools. Every proposed tool gets an internal durable `policy.decision` audit event before execution.
- `external_side_effect` returns `approval_required` without calling the executor; the approval runtime is not enabled yet, so the task fails closed.
- `qagent serve` holds an OS-level database lock so two local server processes cannot concurrently recover or run the same queue.
- Planner JSON is strictly decoded and validated for enum, size, unique IDs, dependencies, and cycles; malformed JSON gets one repair attempt.
- Tool execution goes through a fixed registry and role allowlist. Only bounded, deterministic `text.inspect` is enabled. Unknown tools and invalid arguments are denied before execution.
- Tool results cannot trigger nested tools. The worker performs a fresh model call before another tool decision.
- Host shell, search, browser, filesystem writes, and sandbox execution are absent from the phase 2 registry.
- SQLite state transitions and public events are transactionally persisted. SSE replays by monotonic task sequence and emits only allowlisted user-visible events.
- Embedded UI responses carry same-origin CSP, `X-Frame-Options: DENY`, `nosniff`, and `no-referrer` headers.

## Tests

- `internal/config`: rejects wildcard bind and remote model URLs.
- `internal/domain`: rejects illegal lifecycle transitions, plan cycles, and unsupported kinds.
- `internal/storage/sqlite`: verifies atomic failed transitions, event sequence, persistence, idempotent task creation, and interrupted-task recovery.
- `internal/tools`: rejects unknown tool names, disallowed roles, and extra argument fields.
- `internal/orchestrator`: completes a deterministic mocked task and rejects an attempted `run_command_sandbox` request without starting a host process.
- `internal/api`: verifies strict request decoding and SSE replay with internal event redaction.

## Deferred checks

SSRF, redirect-to-private-network, browser isolation, approval bypass, sandbox escape, evidence/citation integrity, and durable memory provenance are gated on the phases that add those capabilities. No claim is made that those future components have already been tested.
