# Qwenchik

Local-first Kanban, chat, and durable task runner for one local Qwen model. The backend is a Go modular monolith; task state, checkpoints, and event history live in SQLite/WAL. The React + TypeScript UI is served from the same process.

## Current implementation

The current implementation includes:

- task create/list/show and lifecycle controls through the API and CLI;
- SQLite migrations, atomic status/event writes, sequential durable worker, recovery, and checkpoint history;
- one local OpenAI-compatible LLM adapter with strict JSON Schema generation for planner/action/verifier, JSON mode, and SSE streaming support;
- deterministic capability/risk evaluation, per-tool role gates, and internal policy audit events;
- strict planner validation and typed tools; unknown tools are denied and external side effects fail closed;
- optional SearXNG and Tavily search, DNS-rebinding/SSRF-safe public HTTP fetch, and local HTML extraction;
- fetched content stored as local artifacts with content hashes, provenance, excerpts, quote spans, and citation-ID validation;
- local durable memories with candidate review, explicit acceptance, task/global scopes, and bounded lexical retrieval;
- code commands are exposed only through verified rootless Docker, with a read-only container root, task-only workspace mount, disabled network, and resource/output limits;
- SSE event replay using `Last-Event-ID`;
- responsive Kanban, task inspector, chat, live event timeline, sources/artifacts panels, and keyboard command palette.

Search is optional and disabled by default. A request that clearly requires web research fails with `search_unavailable` instead of being silently answered from model knowledge if neither SearXNG nor Tavily is configured. When both providers are configured, searches run concurrently and merge bounded, deduplicated results; one provider can still supply results if the other is unavailable. Without a search provider, the worker can still fetch a public URL included in the task; an arbitrary model-invented URL is rejected. The sandbox tool fails with `sandbox_unavailable` unless rootless Docker and the configured local image are available; it never falls back to host execution. Approval records, one-time decisions, API/CLI actions, and UI cards are implemented, but no external side-effect executor is registered by default. Memory entries are user-created candidates and are retrieved only after explicit acceptance; retrieval is local lexical matching, not semantic/embedding search. PDF extraction and JS/browser automation remain later phases. PDF input fails with `pdf_extractor_unavailable`. Host shell and cloud LLM fallback are not registered.

## Requirements

- Go 1.23+ with CGO compiler support (the SQLite driver uses CGO);
- Node.js 20+ and npm, needed when rebuilding the embedded frontend;
- local `llama-server` exposing the OpenAI-compatible API. The app uses `http://127.0.0.1:8000/v1` by default.
- rootless Docker with the configured image available locally, needed for code execution. No daemon/image is assumed or pulled automatically.
- Docker Compose is optional and used only for the bundled local SearXNG search service.

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

Optionally configure Tavily by exporting `TAVILY_API_KEY` from your local secret store before starting `qagent serve`. The key is only used by the search adapter; CLI/API diagnostics expose configured status, not the key. With both providers enabled, task search queries are sent to Tavily and to the enabled public search engines through SearXNG. Search results remain untrusted leads and must be fetched before citation. Restart `qagent serve` after changing either search setting; configuration is read at process startup. The bundled SearXNG container is bound to loopback and enables the JSON endpoint. Check `/api/doctor` and `/api/providers` to confirm configured status. Stop it with `docker compose -f compose.searxng.yml down` while `SEARXNG_SECRET` is set in the shell. `ALLOWED_DOMAINS=example.org,docs.example.com` optionally narrows fetch and search results. The fetcher rejects loopback, private, link-local, reserved, credential-bearing, nonstandard-port, and redirect-to-private URLs.

This repository includes a loopback-only SearXNG Compose service with JSON output enabled:

```sh
export SEARXNG_SECRET="$(openssl rand -hex 32)"
docker compose -f compose.searxng.yml up -d
export SEARXNG_URL=http://127.0.0.1:8081
```

Start or restart `qagent serve` in that environment; the app reads configuration at startup. Check `/api/providers` for each enabled provider. `/api/doctor` checks SearXNG locally but does not make a billable Tavily request; Tavily is reported as configured and is exercised by a research task. SearXNG runs locally but forwards queries to its enabled public search engines.

Web-research tasks without a user-provided URL require an enabled search tool and a plan with three distinct search questions followed by fetches from discovered sources. Query text is generated from each task's questions; no topic or region-specific query templates are injected. If all search providers are unavailable, the task fails with `search_unavailable` instead of silently attempting analysis-only research. Verification checks the user's acceptance criteria and reports unmet evidence rather than treating a requested gap list as proof of completion. An incomplete persisted plan can be replaced on retry only when previously started actions were idempotent and no approval was consumed; search/fetch budgets and prior observations are preserved.

For sandbox execution, start a rootless Docker daemon and make the configured image available locally (for example, `docker pull golang:1.23-alpine` in that rootless context). Qwenchik uses `--pull=never`, `--network=none`, a read-only container root, and only the per-task workspace mount.

Set `LLM_CONTEXT_LENGTH` to the context configured on the model server. The example file follows the API contract default of `131072`; for the conservative `-c 8192` llama.cpp command above, export `LLM_CONTEXT_LENGTH=8192`. The worker derives a bounded task-input budget from that value and the output-token reserve.

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
go test ./...
go vet ./...
go build ./...
npm --prefix frontend ci
npm --prefix frontend run typecheck
npm --prefix frontend run build
```

`make test` runs Go tests and frontend type-checking. `make build` also rebuilds the embedded frontend assets before compiling the executable.

## Configuration and security

Copy `.env.example` as a starting point and export the values in your shell; the binary does not parse dotenv files. The defaults are local-only: SQLite under `./data`, artifacts under `./data/artifacts`, one LLM slot, and a loopback model URL. API keys are never printed by `qagent config` or `doctor`.

See `docs/architecture/adr-001-system.md`, `docs/contracts/`, and `docs/architecture/repository-audit.md` for decisions and contracts. The default UI and API are unauthenticated and therefore must remain bound to loopback.
