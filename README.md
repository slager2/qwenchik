# Qwenchik

Local-first Kanban, chat, and durable task runner for one local Qwen model. The backend is a Go modular monolith; task state, checkpoints, and event history live in SQLite/WAL. The React + TypeScript UI is served from the same process.

## Current implementation

The current implementation includes:

- task create/list/show and lifecycle controls through the API and CLI;
- SQLite migrations, atomic status/event writes, sequential durable worker, recovery, and checkpoint history;
- one local OpenAI-compatible LLM adapter with strict JSON Schema generation for planner/action/verifier, JSON mode, and SSE streaming support;
- deterministic capability/risk evaluation, per-tool role gates, and internal policy audit events;
- strict planner validation and typed tools; unknown tools are denied and external side effects fail closed;
- SearXNG, optional Tavily and configurable DuckDuckGo search with classified outcomes, SSRF-safe fetch, structured/hydration/semantic HTML extraction, and bounded PDF extraction;
- fetched content stored as local artifacts with content hashes, provenance, excerpts, quote spans, and citation-ID validation;
- local durable memories with candidate review, explicit acceptance, task/global scopes, and bounded FTS5 retrieval;
- code commands are exposed only through verified rootless Docker, with a read-only container root, task-only workspace mount, disabled network, and resource/output limits;
- SSE event replay using `Last-Event-ID`;
- responsive Kanban, task inspector, chat, live event timeline, sources/artifacts panels, and keyboard command palette.

Search adapters are configured separately from live health. DuckDuckGo is enabled by default for convenience and sends queries to an external service; set `SEARCH_ENABLE_DDG=false` for privacy-first operation. It can be blocked or change its markup, so enabled does not mean healthy. SearXNG and Tavily are optional additions. Results, valid empty responses, challenge pages, malformed responses, rate limits, invalid credentials, and policy filtering have separate diagnostic outcomes. Discovery is never simulated with `text.inspect`, and every result URL still passes through allowlist/SSRF checks and must be fetched before it becomes evidence. The sandbox tool fails with `sandbox_unavailable` unless rootless Docker and its configured image are available; it never falls back to host execution. PDF extraction prefers isolated Poppler, then a process-bounded pure-Go helper without container isolation. JS/browser automation, cloud LLM fallback and semantic/embedding memory search are not registered.

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
      --jinja --parallel 1 -c 32768 -fa on \
     -ctk q8_0 -ctv q8_0
   ```

2. Check `curl http://127.0.0.1:8000/v1/models`, then set `LLM_CONTEXT_LENGTH=32768` to match the server. Use an accelerated build/device when available: a CPU-only 4B model can exceed the request timeout while generating a plan. The runtime audit on this machine used the existing Vulkan build with `--device Vulkan1 -ngl 99` on an RTX 3050; these device flags are machine-specific. The server remains single-slot.

3. In the repository root, build the frontend and executable:

   ```sh
   make build
   ```

4. Configure a search provider as below, then start the app (its default bind is loopback only):

   ```sh
   ./qagent serve
   ```

5. Open <http://127.0.0.1:8080>. Check dependencies and observed provider health with:

   ```sh
    ./qagent doctor --json
    ./qagent providers --live --json
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

`SEARXNG_SECRET` must remain available to Compose commands that interpolate the configuration. Keep it in your local `.env` or secret store, never in a commit. The top-level `use_default_settings` YAML setting enables the pinned image's defaults. Docker Desktop can run the loopback search service, but it does not satisfy the rootless requirement for code sandboxes or isolated PDF extraction. SearXNG forwards search queries to its enabled public search engines.

`make doctor` does a bounded local readiness check. `make search-smoke` performs one local SearXNG probe and reports counts/outcomes without invoking Tavily or DuckDuckGo. Explicitly use `./qagent search-smoke --allow-external --json` to probe configured external providers; it warns that Tavily may be billable. `/api/providers` and `providers --live` expose last actual provider outcomes, including timestamps and latency. Before the first request, health is unknown, not healthy.

Web-research tasks without a user-provided URL require an enabled search tool, 2–3 distinct search facets, and evidence from 2–4 distinct source sites. A single conditional broadened-query retry runs only when the initial query returns zero hits; additional facets are planned only when needed for the request. Once the evidence threshold is met, remaining optional searches and fetches are recorded as skipped. Intent detection covers common entity/location lookups and informational queries in Russian and English; analysis-only plans are rejected for recognized research requests. Query text is generated from each task's facets; no topic-specific query templates are injected. If all search providers are unavailable, the task fails with `search_unavailable`. Verification checks the user's acceptance criteria and reports unmet evidence rather than treating a requested gap list as proof of completion. An incomplete persisted plan can be replaced on retry only when previously started actions were idempotent and no approval was consumed; search/fetch budgets and prior observations are preserved.

For sandbox execution, start a rootless Docker daemon and make the configured image available locally (for example, `docker pull golang:1.23-alpine` in that rootless context). Qwenchik uses `--pull=never`, `--network=none`, a read-only container root, and only the per-task workspace mount.

To enable PDF extraction, build the dedicated Alpine/Poppler image in the local Docker context and configure its immutable local image ID:

```sh
docker pull alpine:3.23@sha256:85fe1e81d6758c208f3e1eed4338a1997e19d4be002d4dd32d3100c9a8c010a0
make pdf-extractor-image
export PDF_EXTRACTOR_IMAGE="$(docker image inspect --format '{{.Id}}' qagent-pdf-extractor:local)"
```

`PDF_EXTRACTOR_IMAGE` accepts an immutable local image ID or digest reference and rejects mutable tags. The base Alpine image is digest-pinned. When available, each PDF runs in a rootless-only container with no network, read-only root, no capabilities, non-root UID, and CPU/memory/PID limits. Without it, extraction uses a separate pure-Go helper process with a five-second wall-clock timeout. Both paths cap input at 5 MB, pages at 20, and extracted text at 50,000 Unicode characters; encrypted, malformed, empty-text, or oversized documents produce no evidence. The helper process is bounded, but does not provide the container's OS-level isolation. `qagent doctor` reports whether isolated PDF extraction is available.

Set `LLM_CONTEXT_LENGTH` to the actual server context, not the model's training capacity. The example uses `32768`; if the local `/v1/models` metadata reports a lower runtime `n_ctx`, startup caps the budget to it and logs the mismatch. Byte budgeting remains an estimate, not exact tokenization. Tool-result compaction reserves IDs/URLs/titles and minimum snippets for every result before distributing remaining space; insufficient space raises a budget error rather than sending an empty evidence set to verification.

The planner uses a simple bounded step-array schema plus backend validation/normalization. Search/fetch criteria describe tool execution; the verifier evaluates the user's actual request using combined fetched evidence, rather than treating invented planner counts or extra fields as user requirements. Assessment keys are backend-owned `c0`, `c1`, etc., with per-step evidence-ID constraints. Final text is streamed provisionally; fabricated citation IDs still fail validation. If the model omits all inline markers, a deterministic section lists the actual sources supplied to the finalizer. This list is not a proof that every individual sentence is entailed by a source.

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
npm --prefix frontend test
npm --prefix frontend run build
```

`make test` runs tagged Go tests, frontend type-checking and twelve deterministic observability/component-render tests. These include polling/SSE race regressions and React source/failure cards, but are not real-browser end-to-end tests. `make build` rebuilds embedded assets and compiles the executable. See `docs/research-repair-2026-10-17.md` for the incident reproduction, fixture/live evidence, and remaining runtime limits.

## Configuration and security

Copy `.env.example` as a starting point. `qagent` reads `.env` from its working directory (or executable directory if no value is already set); non-empty process environment values take precedence. The defaults are local-only: SQLite under `./data`, artifacts under `./data/artifacts`, one LLM slot, and a loopback model URL. PDF extraction uses the pure-Go helper fallback unless `PDF_EXTRACTOR_IMAGE` selects a local immutable image ID or digest for isolated Poppler. API keys are never printed by `qagent config` or `doctor`.

See `docs/architecture/adr-001-system.md`, `docs/contracts/`, and `docs/architecture/repository-audit.md` for decisions and contracts. The default UI and API are unauthenticated and therefore must remain bound to loopback.
