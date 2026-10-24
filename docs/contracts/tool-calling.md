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

PDF extraction is available through `fetch_url`. When `PDF_EXTRACTOR_IMAGE` names an available local immutable image and Docker verifies as local and rootless, Poppler runs in a digest-pinned Alpine container with no network, read-only root, no capabilities, no-new-privileges, non-root UID, CPU/memory/PID limits and bounded output. The repair profile permits 64 MiB PDF input separately from the 128 MiB cumulative task budget and 16 MiB ordinary source cap; it retains 20 pages, 50,000 Unicode text characters, a shared 30-second metadata/text extraction deadline and a 120-second download deadline. The isolated resource profile is 512 MiB memory, 64 MiB tmpfs, 1 CPU and 32 PIDs. No incomplete input/output becomes evidence.

Missing rootless isolation yields `pdf_extractor_unavailable` unless `PDF_ALLOW_UNSANDBOXED_FALLBACK=true` explicitly permits a re-executed pure-Go helper with bounded input/output and wall-clock timeout. That helper is not an OS sandbox; its Go memory target is not a hard resource boundary. Rootful Docker must not be reported as isolated readiness. Source byte/page/text/resource failures may try another individually authorized candidate; task budget exhaustion, cancellation, policy rejection, unknown outcome and an exact-URL requirement must not silently substitute one. Failure events use the [typed public source contract](research.md#source-failure-projection-and-replay), with zero bytes for header-only rejection and actual bytes for bounded streaming failure. Same-URL retry, another-source selection and user task retry are distinct operations.

URLs with a `.pdf` path receive an extractor-capability preflight before the fetcher runs. Other URLs are preflighted once the response declares `application/pdf` or a bounded first-1024-byte preview identifies the PDF signature, before the rest of the body is downloaded or written to fetch-temp storage.

Dynamic browser content, Brave search, and direct workspace writes remain deferred. Tavily search is optional and enabled only by `TAVILY_API_KEY`; the key is never included in provider diagnostics. `run_command_sandbox` and `run_tests_sandbox` invoke only a verified local rootless Docker daemon, use no network and a task workspace, and fail with `sandbox_unavailable` when the daemon/image is unavailable. Sandbox file limits remain independent from research download limits. Podman is not wired in this build. Host shell is never registered. Effective policy and runtime availability are exposed by API doctor; the [repair report](../pdf-limits-repair.md) records the effective policy and verified live-execution boundary.

## Recorded live PDF result (2026-10-20)

The current doctor profile reports a 67,108,864-byte PDF cap and `PDF_ALLOW_UNSANDBOXED_FALLBACK=false`. In the recorded research run, the 40,534,667-byte source failed with `pdf_extractor_unavailable` (declared/effective/fetched: 40,534,667 / 67,108,864 / 40,534,667). No same-step fallback candidate was attempted; later planned searches fetched two HTML sources and yielded two evidence records, but the run ended `verification_incomplete`. The real gated test passed file-backed download validation and skipped `RootlessExtractor` because `PDF_EXTRACTOR_IMAGE` was unset and the rootless runtime check was unavailable. No true parsing result is claimed; see the [14-section repair report](../pdf-limits-repair.md).
