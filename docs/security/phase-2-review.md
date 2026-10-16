# Implemented security gates (P2–P9)

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
- Planner, action, and verifier outputs use strict JSON Schema-constrained generation with the same bounded schema on the single repair attempt, followed by local schema and strict-decoder validation. Per-tool action arguments are schema-bound; nullable optional arguments are normalized before backend validation.
- Explicit web-research tasks cannot proceed as analysis-only plans: missing search providers produce `search_unavailable`; enabled plans include distinct search questions and fetches. A failed research-only persisted plan is replaced only when prior started tools are idempotent and no approval was consumed; durable search/fetch budgets and observations are carried forward.
- Tool execution goes through a fixed registry, JSON argument/output schemas, a role allowlist, and deterministic capabilities. Unknown tools and invalid arguments are denied before execution.
- Optional SearXNG uses a loopback-only endpoint and safe search. HTTP fetching rejects private/reserved targets, revalidates DNS at dial and redirect time, disables proxies, limits response bytes/MIME/ports, and requires task-known URLs.
- HTML extraction drops executable and navigation content. External results remain tainted and cannot select tools; evidence IDs are backend-generated and citation IDs are validated.
- Memory search reads only explicitly accepted local records, filters task scope by source task, and records retrieved IDs in an internal audit event. Prompt roles treat memory text as untrusted reference data; memory content cannot grant tools or seed allowed fetch URLs.
- Code execution calls only the Docker client with an argv array after verifying a local Unix socket, rootless mode, and a local image. `doctor` also runs a fixed isolated `true` smoke container. The task container has no network, a read-only root, one task-workspace mount, dropped capabilities, no-new-privileges, PID/CPU/memory limits, bounded output, and cleanup on client failure. No host shell fallback exists.
- PDF extraction sends bytes only to a fixed Poppler wrapper in a digest-pinned Alpine-based image. It requires a verified local Unix Docker endpoint, rootless mode, and an already-present immutable image ID/digest; it uses `--pull=never`, no network, read-only root, dropped capabilities, no-new-privileges, UID 10001, CPU/memory/PID limits, a read-only single-file input mount, bounded stdout/stderr, and forced container cleanup on timeout/output failure. One five-second context covers the combined `pdfinfo`/`pdftotext` run. Input is limited to 5 MB, page count to 20, and text to 50,000 Unicode characters; encrypted, malformed, timed-out, or oversized documents fail before artifacts/evidence are persisted. No in-process parser or host fallback exists.
- Tool results cannot trigger nested tools. The worker performs a fresh model call before another tool decision.
- Host shell, browser, and direct workspace-write tools are absent. Sandbox command tools fail with `sandbox_unavailable` when rootless Docker or the image is unavailable. Search is registered only when SearXNG or Tavily is configured.
- SQLite state transitions and public events are transactionally persisted. SSE replays by monotonic task sequence and emits only allowlisted user-visible events.
- Embedded UI responses carry same-origin CSP, `X-Frame-Options: DENY`, `nosniff`, and `no-referrer` headers.

## Tests

- `internal/config`: rejects wildcard bind and remote model URLs.
- `internal/domain`: rejects illegal lifecycle transitions, plan cycles, and unsupported kinds.
- `internal/storage/sqlite`: verifies atomic failed transitions, event sequence, persistence/recovery, approval expiration, idempotent decisions, and one-time approval consumption.
- `internal/storage/sqlite`: verifies memory candidates stay out of retrieval until accepted, accepted records respect task scope, and stale records are excluded.
- `internal/tools`: rejects unknown tool names, disallowed roles, and extra argument fields.
- `internal/tools`: validates JSON schemas before policy execution; denies unknown tools and unknown URLs; verifies evidence/artifact persistence and per-task fetch budgets.
- `internal/orchestrator`: completes mocked analysis and research tasks with citations; pauses for approval and executes a test side-effect exactly once after approval; rejects `run_command_sandbox` recovery replay without starting a host process.
- `internal/orchestrator`: validates strict role schemas, repairs malformed action/plan output once, rejects research-only plans without search/fetch, and safely replaces incomplete persisted plans only before any tool starts.
- `internal/orchestrator`: checks generated role schemas, one bounded JSON-schema repair, search-unavailable preflight, and repair of a persisted research plan before any tool action.
- `internal/providers/research`: tests private IPv4/IPv6, metadata IP, restricted ports, sensitive query parameters, redirect-to-private, DNS rebinding, MIME and byte limits, SearXNG response filtering, HTML script/nav removal, and PDF delegation/fail-closed behavior.
- `internal/runtime/pdfextract`: fake-runner tests local/rootless Docker and immutable image requirements, fixed isolation flags, the shared deadline, byte/page/Unicode/output limits, output non-disclosure, and cleanup.
- `internal/api`: verifies strict request decoding, SSE replay with internal event redaction, evidence/artifact metadata, task-scoped artifact download, and approval decisions.
- `internal/api`: verifies memory creation always starts as candidate and only explicit acceptance makes it retrievable.

## Deferred gates

At the time of the original review, browser isolation, real-daemon sandbox smoke/escape review, Tavily/Brave adapters, and JS-only extraction remained gated on later phases. PDF parsing now has fake-runner coverage; live rootless-container behavior remains unverified in this environment. Memory retrieval currently uses bounded lexical matching; embedding-based semantic search and automatic candidate extraction remain deferred. The sandbox tests inspect exact Docker arguments with fake runners.

## Follow-up (2026-10-16)

The search adapter phase now includes optional Tavily support via `TAVILY_API_KEY` and concurrent SearXNG/Tavily aggregation. Tavily uses a fixed HTTPS endpoint; the key is sent only as an Authorization bearer header and is excluded from CLI/API diagnostics and provider errors. Queries are sent to Tavily when enabled, so configure it only for searches intended for that external service. Search results remain untrusted and still pass through the existing URL allowlist, SSRF-safe fetch, extraction, and evidence checks. Adapter and aggregation tests cover key redaction, provider fallback, domain filtering, URL deduplication, and global result limits. Live Tavily credential/network verification is separate from these deterministic tests.
