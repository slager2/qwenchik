# Web research and evidence contract

## Providers

- Discovery uses optional providers: local SearXNG JSON through `SEARXNG_URL` and Tavily through `TAVILY_API_KEY`. Both are disabled by default. SearXNG accepts only loopback hosts; `compose.searxng.yml` provides a loopback-only instance with JSON enabled. Tavily uses a fixed HTTPS endpoint, and its key is never returned by diagnostics or included in error messages.
- When both providers are configured, queries run concurrently and results are round-robin merged, filtered against `ALLOWED_DOMAINS`, deduplicated by canonical URL, and capped globally. A successful empty result or a failure from one provider does not discard successful results from the other; `search_unavailable` is returned only if every configured provider fails.
- Research queries are generated from the task's bounded questions; the backend does not inject topic-specific query templates or authority-domain lists. `ALLOWED_DOMAINS` remains a hard restriction.
- Query terms are sent to Tavily when configured and to SearXNG's enabled public search engines. Do not send sensitive query data unless that external processing is intended. `qagent doctor` does not make a Tavily request; it reports configuration without spending a search request.
- `search` returns bounded title, URL, score, publication time, and snippet data. Search snippets are untrusted and are not evidence by themselves.
- Search-intent tasks with no user URL require the search tool and a plan containing both `search` and `fetch`; an unavailable provider is surfaced as `search_unavailable`, not treated as an empty successful search.
- Generic web research plans 2–3 independent search facets and 2–4 distinct source fetches. The first facet may trigger one durable broader retry only after a successful zero-hit result; this retry is not a planned facet. Optional remaining search/fetch steps are skipped after at least two distinct source sites are fetched and the search facets/minimum evidence floor are met. The backend checks the user/task acceptance criteria and requires evidence before source-backed claims.
- Search connection/HTTP failures and oversized search responses are surfaced with search-specific error codes; fetch-size failures remain distinct.
- `fetch_url` accepts only a URL present in the task/user messages or returned by a prior search action.
- Direct HTTP fetch supports static HTML, XHTML, plain text, JSON, and bounded PDF input. HTML is parsed locally; `script`, `style`, navigation, and form content are excluded from extracted text.
- PDF parsing prefers the isolated local Docker path: Poppler in a digest-pinned Alpine-based image, verified rootless local Docker, `--pull=never`, no network, read-only root, dropped capabilities, non-root UID, and strict CPU/memory/PID limits. A single five-second command deadline covers `pdfinfo` plus `pdftotext`. If the immutable `PDF_EXTRACTOR_IMAGE` is unset or unavailable, a pure-Go parser runs in a re-executed helper process with a five-second wall-clock deadline and Go runtime memory guard. Both paths cap input at 5 MB, page count at 20, and text at 50,000 Unicode characters; encrypted, malformed, empty-text, oversized, timed-out, or output-overflowing PDFs produce no partial evidence. The helper process does not provide the Docker path's OS-level filesystem/network isolation; it is a development fallback. JS-only pages and Playwright are not implemented.

## Network policy

The fetcher uses no proxy, allows only HTTP/HTTPS on ports 80/443, rejects URL credentials and credential-like query parameters, enforces an optional `ALLOWED_DOMAINS` suffix allowlist, checks all DNS answers, and re-resolves/revalidates immediately before dialing each redirect target. Loopback, private, link-local, reserved, multicast, unspecified, and non-global IP ranges are denied. Redirect depth, request time, response bytes, and MIME types are bounded.

## Provenance

Each fetched source receives a SHA-256 content hash, retrieval time, original and canonical URL, provider/query, title, HTTP status, extraction method, local raw/text artifact references, excerpt, quote span, quality flags, and taint label. Duplicate `(task, canonical URL, content hash)` records are deduplicated. The finalizer may cite only evidence IDs present in the current task state; backend citation validation rejects unknown or missing source IDs when research evidence was used.

Artifacts are written atomically below the configured local artifact root with task-owned IDs and size limits. SQLite stores metadata and references only; API/UI expose metadata and excerpts, and the artifact download route resolves task-scoped IDs rather than filesystem paths.
