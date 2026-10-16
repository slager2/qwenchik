# Tool-calling and policy contract

## Descriptor

Every registered tool declares a stable name, argument and output schema, required capability, risk class, timeout, idempotency class, output limit, allowed roles, audit type, and executor. The model cannot register or modify tools.

## Execution pipeline

```text
model JSON -> strict parse -> role allowlist -> schema validation
  -> argument normalization -> capability/path/domain/budget checks
  -> deterministic policy (allow | approval_required | deny)
  -> executor -> output validation/redaction -> durable event/checkpoint
```

Tool output is untrusted data and cannot directly invoke another tool. The orchestrator requests a fresh model decision before any next tool action. Unknown tools fail closed.

## Implemented registry

`text.inspect` is always registered. It accepts bounded user/task text and returns deterministic character, word, and line counts. When SearXNG or Tavily is configured, `search` is added; with both, providers are queried concurrently and merged under a global result cap. Queries are sent to configured external search services and SearXNG's enabled public engines. `fetch_url` is added when the local artifact/evidence store is available; it only accepts URLs found in user task text or prior search results, then validates SSRF policy again before dialing. `fetch_url` stores raw and extracted artifacts and an evidence record; external text is labeled `untrusted_external_content`.

## Conditional and deferred capabilities

PDF text extraction, dynamic browser content, Brave search, and direct workspace writes are not registered. Tavily search is optional and enabled only by `TAVILY_API_KEY`; the key is never included in provider diagnostics. `run_command_sandbox` and `run_tests_sandbox` invoke only a verified local rootless Docker daemon, use no network and a task workspace, and fail with `sandbox_unavailable` when the daemon/image is unavailable. Podman is not wired in this build. A fetched PDF receives an explicit `pdf_extractor_unavailable` error; there is no host-command fallback. Host shell is never registered.
