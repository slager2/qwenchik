# Web research and evidence contract

## Providers

- Discovery uses the optional local SearXNG JSON endpoint configured by `SEARXNG_URL`; it is disabled by default and configuration accepts only loopback hosts.
- `search` returns bounded title, URL, score, publication time, and snippet data. Search snippets are untrusted and are not evidence by themselves.
- `fetch_url` accepts only a URL present in the task/user messages or returned by a prior search action.
- Direct HTTP fetch supports static HTML, XHTML, plain text, and JSON. HTML is parsed locally; `script`, `style`, navigation, and form content are excluded from extracted text.
- PDF bytes are recognized, but text extraction is explicitly unavailable pending a parser security review. JS-only pages and Playwright are not implemented.

## Network policy

The fetcher uses no proxy, allows only HTTP/HTTPS on ports 80/443, rejects URL credentials and credential-like query parameters, enforces an optional `ALLOWED_DOMAINS` suffix allowlist, checks all DNS answers, and re-resolves/revalidates immediately before dialing each redirect target. Loopback, private, link-local, reserved, multicast, unspecified, and non-global IP ranges are denied. Redirect depth, request time, response bytes, and MIME types are bounded.

## Provenance

Each fetched source receives a SHA-256 content hash, retrieval time, original and canonical URL, provider/query, title, HTTP status, extraction method, local raw/text artifact references, excerpt, quote span, quality flags, and taint label. Duplicate `(task, canonical URL, content hash)` records are deduplicated. The finalizer may cite only evidence IDs present in the current task state; backend citation validation rejects unknown or missing source IDs when research evidence was used.

Artifacts are written atomically below the configured local artifact root with task-owned IDs and size limits. SQLite stores metadata and references only; API/UI expose metadata and excerpts, and the artifact download route resolves task-scoped IDs rather than filesystem paths.
