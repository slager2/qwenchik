# Qwenchik

Local-first Kanban, chat, and durable task runner for one local Qwen model. The backend is a Go modular monolith; task state, checkpoints, and event history live in SQLite/WAL. The React + TypeScript UI is served from the same process.

## Current implementation

The first vertical slice is implemented:

- task create/list/show and lifecycle controls through the API and CLI;
- SQLite migrations, atomic status/event writes, sequential durable worker, recovery, and checkpoint history;
- one local OpenAI-compatible LLM adapter with JSON mode and SSE streaming support;
- strict planner validation and one safe `text.inspect` tool; unknown tools are denied;
- SSE event replay using `Last-Event-ID`;
- responsive Kanban, task inspector, chat, live event timeline, and keyboard command palette.

Search providers, citations/evidence extraction, browser automation, approval decisions, code sandbox, and durable memory retrieval are later phases and are not exposed as working features yet. The only available runtime tool reads bounded task text and returns counts. No host shell or cloud LLM fallback is registered.

## Requirements

- Go 1.23+ with CGO compiler support (the SQLite driver uses CGO);
- Node.js 20+ and npm, needed when rebuilding the embedded frontend;
- local `llama-server` exposing the OpenAI-compatible API. The app uses `http://127.0.0.1:8000/v1` by default.

The server was not present in the development environment during the initial audit. `qagent doctor` reports it as an error until it is running.

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

Set `LLM_CONTEXT_LENGTH` to the context configured on the model server. The example file follows the API contract default of `131072`; for the conservative `-c 8192` llama.cpp command above, export `LLM_CONTEXT_LENGTH=8192`. The worker derives a bounded task-input budget from that value and the output-token reserve.

## CLI

```sh
./qagent task create "Compare two local options" --title "Comparison"
./qagent task list
./qagent task run TASK_ID
./qagent task show TASK_ID
./qagent events TASK_ID --follow
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
