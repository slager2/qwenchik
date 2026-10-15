# Implemented security gates (P2–P7)

## Implemented controls

- `APP_BIND_ADDR` accepts only loopback addresses; there is no unauthenticated LAN mode.
- HTTP requests with a non-loopback `Host` are rejected, reducing DNS-rebinding access to the unauthenticated local API.
- `LLM_BASE_URL` accepts only loopback hosts, rejects URL user info/query/fragment, and must use a port different from the app.
- The local LLM adapter does not follow redirects, does not log request/response bodies, bounds response size, and never falls back to a cloud provider.
- One worker calls the model sequentially. Lifecycle transitions and progress events are backend-owned.
- A deterministic capability evaluator is the single allow/approval/deny decision point for registered tools. Every proposed tool gets an internal durable `policy.decision` audit event before execution.
- `external_side_effect` returns `approval_required` without calling the executor. Durable approval decisions are one-time/idempotent and resume only with matching normalized arguments; no external side-effect executor is registered by default.
- `qagent serve` holds an OS-level database lock so two local server processes cannot concurrently recover or run the same queue.
- Planner JSON is strictly decoded and validated for enum, size, unique IDs, dependencies, and cycles; malformed JSON gets one repair attempt.
- Tool execution goes through a fixed registry, JSON argument/output schemas, a role allowlist, and deterministic capabilities. Unknown tools and invalid arguments are denied before execution.
- Optional SearXNG uses a loopback-only endpoint and safe search. HTTP fetching rejects private/reserved targets, revalidates DNS at dial and redirect time, disables proxies, limits response bytes/MIME/ports, and requires task-known URLs.
- HTML extraction drops executable and navigation content. External results remain tainted and cannot select tools; evidence IDs are backend-generated and citation IDs are validated.
- Code execution calls only the Docker client with an argv array after verifying a local Unix socket, rootless mode, and a local image. `doctor` also runs a fixed isolated `true` smoke container. The task container has no network, a read-only root, one task-workspace mount, dropped capabilities, no-new-privileges, PID/CPU/memory limits, bounded output, and cleanup on client failure. No host shell fallback exists.
- Tool results cannot trigger nested tools. The worker performs a fresh model call before another tool decision.
- Host shell, browser, and direct workspace-write tools are absent. Sandbox command tools fail with `sandbox_unavailable` when rootless Docker or the image is unavailable. Search is registered only when SearXNG is configured.
- SQLite state transitions and public events are transactionally persisted. SSE replays by monotonic task sequence and emits only allowlisted user-visible events.
- Embedded UI responses carry same-origin CSP, `X-Frame-Options: DENY`, `nosniff`, and `no-referrer` headers.

## Tests

- `internal/config`: rejects wildcard bind and remote model URLs.
- `internal/domain`: rejects illegal lifecycle transitions, plan cycles, and unsupported kinds.
- `internal/storage/sqlite`: verifies atomic failed transitions, event sequence, persistence/recovery, approval expiration, idempotent decisions, and one-time approval consumption.
- `internal/tools`: rejects unknown tool names, disallowed roles, and extra argument fields.
- `internal/tools`: validates JSON schemas before policy execution; denies unknown tools and unknown URLs; verifies evidence/artifact persistence and per-task fetch budgets.
- `internal/orchestrator`: completes mocked analysis and research tasks with citations; pauses for approval and executes a test side-effect exactly once after approval; rejects `run_command_sandbox` recovery replay without starting a host process.
- `internal/providers/research`: tests private IPv4/IPv6, metadata IP, restricted ports, sensitive query parameters, redirect-to-private, DNS rebinding, MIME and byte limits, SearXNG response filtering, and HTML script/nav removal.
- `internal/api`: verifies strict request decoding, SSE replay with internal event redaction, evidence/artifact metadata, task-scoped artifact download, and approval decisions.

## Deferred gates

Browser isolation, real-daemon sandbox smoke/escape review, Tavily/Brave adapters, PDF parsing, JS-only extraction, and durable memory provenance remain gated on later phases. The current sandbox tests inspect the exact Docker arguments with a fake runner; no live rootless Docker daemon was available during implementation.
