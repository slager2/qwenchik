# Qwenchik

Local-first Kanban, chat, and durable task runner for one local Qwen model. The backend is a Go modular monolith; task state, checkpoints, and event history live in SQLite/WAL. The React + TypeScript UI is served from the same process.

## Current implementation

The current implementation includes:

- task create/list/show and lifecycle controls through the API and CLI;
- SQLite migrations, atomic status/event writes, sequential durable worker, recovery, and checkpoint history;
- one local OpenAI-compatible LLM adapter with strict JSON Schema generation for planner/action/verifier, JSON mode, and SSE streaming support;
- deterministic capability/risk evaluation, per-tool role gates, and internal policy audit events;
- strict planner validation and typed tools; unknown tools are denied and external side effects fail closed;
- optional SearXNG and Tavily search, DNS-rebinding/SSRF-safe public HTTP fetch, local HTML extraction, and bounded isolated PDF text extraction;
- fetched content stored as local artifacts with content hashes, provenance, excerpts, quote spans, and citation-ID validation;
- local durable memories with candidate review, explicit acceptance, task/global scopes, and bounded FTS5 retrieval;
- code commands are exposed only through verified rootless Docker, with a read-only container root, task-only workspace mount, disabled network, and resource/output limits;
- SSE event replay using `Last-Event-ID`;
- responsive Kanban, task inspector, chat, live event timeline, sources/artifacts panels, and keyboard command palette.

Web search has a built-in DuckDuckGo HTML provider, so it can run without Docker, SearXNG, or a paid API key. Optional SearXNG and Tavily providers are aggregated with DuckDuckGo; `search_unavailable` is returned only when all provider requests fail. Search queries are sent to DuckDuckGo by default, so do not submit sensitive query text unless external search is intended. Discovery is never simulated with `text.inspect`, and every result URL still passes through allowlist/SSRF checks and must be fetched before it becomes evidence. The sandbox tool fails with `sandbox_unavailable` unless rootless Docker and its configured image are available; it never falls back to host execution. PDF extraction prefers the isolated Poppler container, then uses a bounded pure-Go helper process if that container is unavailable. The helper has a wall-clock timeout and resource guards but does not provide the same OS isolation as the Docker path. Approval records, one-time decisions, API/CLI actions, and UI cards are implemented, but no external side-effect executor is registered by default. Memory entries are user-created candidates and are retrieved only after explicit acceptance; retrieval is local FTS5 lexical matching, not semantic/embedding search. JS/browser automation remains deferred. Host shell and cloud LLM fallback are not registered.

## Requirements

- Go 1.23+ with CGO compiler support (the SQLite driver uses CGO);
- Node.js 20+ and npm, needed when rebuilding the embedded frontend;
- local `llama-server` exposing the OpenAI-compatible API. The app uses `http://127.0.0.1:8000/v1` by default.
- rootless local Docker with configured images available locally, needed for code execution and optional PDF extraction. No image is pulled automatically at runtime.
- Docker Compose is optional and used only for the bundled local SearXNG search service.

SQLite memory retrieval uses the driver's bundled FTS5 module. `make test`, `make build`, `make run`, and `make vet` enable it with the `sqlite_fts5` Go build tag. Direct Go commands must include `-tags sqlite_fts5` as well (for example, `CGO_ENABLED=1 go test -tags sqlite_fts5 ./...`).

`qagent doctor` runs a bounded strict-JSON-schema smoke request against the configured model, in addition to checking the endpoint. A server that only answers `/v1/models` but cannot produce the required structured output is reported as unavailable for agent work.

## Run locally

1. Start a compatible local model server on port `8000`. For llama.cpp, adapt the model path and flags to the installed build:

   ```sh
   llama-server \
     -m /models/Qwen3.5-4B-Q4_K_M.gguf \
     --host 127.0.0.1 --port 8000 \
     --jinja --parallel 1 -c 8192 -fa on \
     -ctk q8_0 -ctv q8_0
   ```

2. In the repository root, build the frontend and executable:

   ```sh
   make build
   ```

3. Start the app (its default bind is loopback only):

   ```sh
   ./qagent serve
   ```

4. Open <http://127.0.0.1:8080>. Check dependencies with:

   ```sh
   ./qagent doctor
   ```

The backend and model server intentionally use different ports. The API rejects non-loopback app binds and remote LLM URLs; authenticated LAN mode is not implemented.
The server holds an OS lock for its database so a second `qagent serve` process cannot recover or run the same queue concurrently.

Optional local web search can be started with the included Compose configuration:

```sh
export SEARXNG_SECRET="$(openssl rand -hex 32)"
docker compose -f compose.searxng.yml up -d
export SEARXNG_URL=http://127.0.0.1:8081
```

Optionally configure Tavily by setting `TAVILY_API_KEY` in `.env` or the process environment before starting `qagent serve`. The key is only used by the search adapter; CLI/API diagnostics expose configured status, not the key. When enabled, Tavily and SearXNG run alongside built-in DuckDuckGo, and results are merged/deduplicated. Search results remain untrusted leads and must be fetched before citation. Restart `qagent serve` after changing search settings; configuration is read at process startup. The bundled SearXNG container is bound to loopback and enables the JSON endpoint. Check `/api/doctor` and `/api/providers` to inspect configuration; doctor does not make a Tavily request. Stop SearXNG with `docker compose -f compose.searxng.yml down` while `SEARXNG_SECRET` is set in the shell. `ALLOWED_DOMAINS=example.org,docs.example.com` optionally narrows fetch and search results. The fetcher rejects loopback, private, link-local, reserved, credential-bearing, nonstandard-port, and redirect-to-private URLs.

This repository includes a loopback-only SearXNG Compose service with JSON output enabled:

```sh
export SEARXNG_SECRET="$(openssl rand -hex 32)"
docker compose -f compose.searxng.yml up -d
export SEARXNG_URL=http://127.0.0.1:8081
```

Start or restart `qagent serve` in that environment; the app reads configuration at startup. Check `/api/providers` for each enabled provider. `/api/doctor` checks SearXNG locally but does not make a billable Tavily request; Tavily is reported as configured and is exercised by a research task. SearXNG runs locally but forwards queries to its enabled public search engines.

Web-research tasks without a user-provided URL require an enabled search tool, 2–3 distinct search facets, and evidence from 2–4 distinct source sites. A single conditional broadened-query retry runs only when the initial query returns zero hits; additional facets are planned only when needed for the request. Once the evidence threshold is met, remaining optional searches and fetches are recorded as skipped. Intent detection covers common entity/location lookups and informational queries in Russian and English; analysis-only plans are rejected for recognized research requests. Query text is generated from each task's facets; no topic-specific query templates are injected. If all search providers are unavailable, the task fails with `search_unavailable`. Verification checks the user's acceptance criteria and reports unmet evidence rather than treating a requested gap list as proof of completion. An incomplete persisted plan can be replaced on retry only when previously started actions were idempotent and no approval was consumed; search/fetch budgets and prior observations are preserved.

For sandbox execution, start a rootless Docker daemon and make the configured image available locally (for example, `docker pull golang:1.23-alpine` in that rootless context). Qwenchik uses `--pull=never`, `--network=none`, a read-only container root, and only the per-task workspace mount.

To enable PDF extraction, build the dedicated Alpine/Poppler image in the local Docker context and configure its immutable local image ID:

```sh
docker pull alpine:3.23@sha256:85fe1e81d6758c208f3e1eed4338a1997e19d4be002d4dd32d3100c9a8c010a0
make pdf-extractor-image
export PDF_EXTRACTOR_IMAGE="$(docker image inspect --format '{{.Id}}' qagent-pdf-extractor:local)"
```

`PDF_EXTRACTOR_IMAGE` accepts an immutable local image ID or digest reference and rejects mutable tags. The base Alpine image is digest-pinned. When available, each PDF runs in a rootless-only container with no network, read-only root, no capabilities, non-root UID, and CPU/memory/PID limits. Without it, extraction uses a separate pure-Go helper process with a five-second wall-clock timeout. Both paths cap input at 5 MB, pages at 20, and extracted text at 50,000 Unicode characters; encrypted, malformed, empty-text, or oversized documents produce no evidence. The helper process is bounded, but does not provide the container's OS-level isolation. `qagent doctor` reports whether isolated PDF extraction is available.

Set `LLM_CONTEXT_LENGTH` to the context configured on the model server. The example file follows the API contract default of `131072`; for the conservative `-c 8192` llama.cpp command above, export `LLM_CONTEXT_LENGTH=8192`. The worker estimates an input byte budget from the available input tokens (4 bytes per token, reserving 2,048 prompt tokens), then checks the serialized system prompt, user data, and schema against that budget. This estimate scales with the configured context rather than using a fixed 20 KB ceiling.

## CLI

```sh
./qagent task create "Compare two local options" --title "Comparison"
./qagent task list
./qagent task run TASK_ID
./qagent task show TASK_ID
./qagent events TASK_ID --follow
./qagent evidence TASK_ID
./qagent artifacts TASK_ID
./qagent checkpoints TASK_ID
./qagent approvals
./qagent approve APPROVAL_ID
./qagent reject APPROVAL_ID
./qagent memories
./qagent memory add "Prefer concise answers" --kind preference
./qagent memory accept MEMORY_ID
./qagent memory search "answer style"
./qagent task pause TASK_ID
./qagent task resume TASK_ID
./qagent task cancel TASK_ID
./qagent --json task list
```

`qagent run "TASK"` creates and queues a task. Keep `qagent serve` running so its single worker can process the queue. Ctrl-C stops the server cleanly; an interrupted in-flight task is recovered from its durable checkpoint on the next start.

## Development checks

```sh
CGO_ENABLED=1 go test -tags sqlite_fts5 ./...
CGO_ENABLED=1 go vet -tags sqlite_fts5 ./...
CGO_ENABLED=1 go build -tags sqlite_fts5 ./...
npm --prefix frontend ci
npm --prefix frontend run typecheck
npm --prefix frontend run build
```

`make test` runs Go tests and frontend type-checking. `make build` also rebuilds the embedded frontend assets before compiling the executable.

## Configuration and security

Copy `.env.example` as a starting point. `qagent` reads `.env` from its working directory (or executable directory if no value is already set); non-empty process environment values take precedence. The defaults are local-only: SQLite under `./data`, artifacts under `./data/artifacts`, one LLM slot, and a loopback model URL. PDF extraction uses the pure-Go helper fallback unless `PDF_EXTRACTOR_IMAGE` selects a local immutable image ID or digest for isolated Poppler. API keys are never printed by `qagent config` or `doctor`.

See `docs/architecture/adr-001-system.md`, `docs/contracts/`, and `docs/architecture/repository-audit.md` for decisions and contracts. The default UI and API are unauthenticated and therefore must remain bound to loopback.
