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

`text.inspect` is always registered. It accepts bounded user/task text and returns deterministic character, word, and line counts. The built-in DuckDuckGo provider registers `search` by default; optional SearXNG and Tavily providers are queried concurrently with it and merged under a global result cap. Queries are sent to DuckDuckGo and to any configured external search services/public engines. `fetch_url` is added when the local artifact/evidence store is available; it only accepts URLs found in user task text or prior search results, then validates SSRF policy again before dialing. `fetch_url` stores raw and extracted artifacts and an evidence record; external text is labeled `untrusted_external_content`.

## Conditional and deferred capabilities

PDF extraction is available through `fetch_url`. When `PDF_EXTRACTOR_IMAGE` names an available local immutable image and Docker verifies as local and rootless, Poppler runs in a digest-pinned Alpine container with no network, read-only root, no capabilities, non-root UID, resource limits, and a shared five-second deadline for metadata and text extraction. If that container path is unavailable, a pure-Go parser runs in a re-executed helper process with bounded input/output and a five-second wall-clock deadline; this fallback is not an OS-level sandbox. Both paths reject malformed, encrypted, over-20-page, over-5-MB, over-50,000-character, or over-output-limit PDFs without partial evidence. Dynamic browser content, Brave search, and direct workspace writes remain deferred. Tavily search is optional and enabled only by `TAVILY_API_KEY`; the key is never included in provider diagnostics. `run_command_sandbox` and `run_tests_sandbox` invoke only a verified local rootless Docker daemon, use no network and a task workspace, and fail with `sandbox_unavailable` when the daemon/image is unavailable. Podman is not wired in this build. Host shell is never registered.
