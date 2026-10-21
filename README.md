# Qwenchik

Local-first conversations, Deep Research, and a durable workspace for one local Qwen model. The backend is a Go modular monolith; conversations, messages, runs, checkpoints, and events live in SQLite/WAL. The React + TypeScript UI is served from the same process. Chats open at `#/chat`; the existing task board, approvals, and memory remain at `#/workspace`.

Historical Chat/PDF verification: Go tests/vet/race, 33 frontend tests, embedded build, mocked Chrome, real Chat Stop/Retry/Continue, and checkpoint-restart smoke passed in the earlier implementation reports. The gated live PDF test verified a file-backed download only; rootless extraction was unavailable, and research run `52f28017-840c-47e9-8297-70a4a5580941` ended `verification_incomplete`, not `complete`. A separate HTTP/3 research run retained real sources/evidence but used explicit excerpt fallback, not fully verified synthesized claims. See the [14-section PDF repair report](docs/pdf-limits-repair.md) and [12-section chat report](docs/chat-research-implementation.md) for run IDs, commands, and limits. Current Phase 1 tests/vet/race, frontend tests/build/browser checks and a bounded deployed snapshot smoke also passed; their distinct scope and evidence are documented in [Research Phase 1](docs/research-phase1.md#verification-results).

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
- persisted conversation/message/run APIs, explicit `chat` and `research` modes, stop, retry, and answer continuation;
- Phase 1 research sessions with immutable profile/policy/model snapshots, persisted absolute deadlines, and durable pre-call budget reservations on the existing fixed-plan execution path;
- SSE replay using `Last-Event-ID` or an initial `after` cursor, with canonical persisted message snapshots;
- conversation sidebar, safe Markdown/GFM and evidence links, independent research details, mobile drawers, plus the retained Kanban, inspector, approvals, memories, and command palette.

Search adapters are configured separately from live health. DuckDuckGo is enabled by default for convenience and sends queries to an external service; set `SEARCH_ENABLE_DDG=false` for privacy-first operation. It can be blocked or change its markup, so enabled does not mean healthy. SearXNG and Tavily are optional additions. Results, valid empty responses, challenge pages, malformed responses, rate limits, invalid credentials, and policy filtering have separate diagnostic outcomes. Discovery is never simulated with `text.inspect`, and every result URL still passes through allowlist/SSRF checks and must be fetched before it becomes evidence. The sandbox tool fails with `sandbox_unavailable` unless rootless Docker and its configured image are available; it never falls back to host execution. The PDF repair policy requires isolated Poppler by default; the process-bounded pure-Go helper is available only with explicit permission and is not an OS sandbox. JS/browser research tools, OCR, cloud LLM fallback and semantic/embedding memory search are not registered.

## Requirements

- Go 1.23+ with CGO compiler support (the SQLite driver uses CGO);
- Node.js 20+ and npm, needed when rebuilding the embedded frontend;
- local `llama-server` exposing the OpenAI-compatible API. The app uses `http://127.0.0.1:8000/v1` by default.
- rootless local Docker with configured images available locally, needed for code execution and optional PDF extraction. No image is pulled automatically at runtime.
- Docker Compose is optional and used only for the bundled local SearXNG search service.

SQLite memory retrieval uses the driver's bundled FTS5 module. `make test`, `make build`, `make run`, and `make vet` enable it with the `sqlite_fts5` Go build tag. Direct Go commands must include `-tags sqlite_fts5` as well (for example, `CGO_ENABLED=1 go test -tags sqlite_fts5 ./...`).

`qagent doctor` runs a bounded strict-JSON-schema smoke request against the configured model, in addition to checking the endpoint. A server that only answers `/v1/models` but cannot produce the required structured output is reported as unavailable for agent work.

## Run locally

From a fresh checkout, install the requirements above and run `cp -n .env.example .env` in the repository root. Review `.env` locally; keep `LLM_CONTEXT_LENGTH` consistent with the server and store provider credentials only in local configuration. Existing `.env` files must not be overwritten.

1. Start a compatible local model server on port `8000`. For llama.cpp, adapt the model path and flags to the installed build:

   ```sh
   llama-server \
     -m /models/Qwen3.5-4B-Q4_K_M.gguf \
     --host 127.0.0.1 --port 8000 --alias local-qwen \
     --jinja --parallel 1 -c 32768 -fa on \
     -ctk q8_0 -ctv q8_0
   ```

2. Check `curl http://127.0.0.1:8000/v1/models`, then set `LLM_CONTEXT_LENGTH=32768` to match the server. Use an accelerated build/device when available: a CPU-only 4B model can exceed the request timeout while generating a plan. The runtime audit on this machine used the existing Vulkan build with `--device Vulkan1 -ngl 99` on an RTX 3050; these device flags are machine-specific. The server remains single-slot.

3. In the repository root, install frontend dependencies and build the embedded UI and executable:

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

For frontend development, keep the backend running on `8080`, then run `make frontend-dev` in another terminal. Open <http://127.0.0.1:5173>; Vite proxies `/api` to the backend. The model API is <http://127.0.0.1:8000/v1>; optional SearXNG is <http://127.0.0.1:8081>. `make build` embeds frontend assets before compiling Go; restart the app after rebuilding to serve the new UI.

Optional local web search can be started with the included Compose configuration:

```sh
export SEARXNG_SECRET="$(openssl rand -hex 32)"
docker compose -f compose.searxng.yml up -d
export SEARXNG_URL=http://127.0.0.1:8081
```

Optionally configure Tavily by setting `TAVILY_API_KEY` in `.env` or the process environment before starting `qagent serve`. The key is only used by the search adapter; CLI/API diagnostics expose configured status, not the key. When enabled, Tavily and SearXNG run alongside built-in DuckDuckGo, and results are merged/deduplicated. Search results remain untrusted leads and must be fetched before citation. Restart `qagent serve` after changing search settings; configuration is read at process startup. The bundled SearXNG container is bound to loopback and enables the JSON endpoint. Check `/api/doctor` and `/api/providers` to inspect configuration; doctor does not make a Tavily request. Stop SearXNG with `docker compose -f compose.searxng.yml down` while `SEARXNG_SECRET` is set in the shell. `ALLOWED_DOMAINS=example.org,docs.example.com` optionally narrows fetch and search results. The fetcher rejects loopback, private, link-local, reserved, credential-bearing, nonstandard-port, and redirect-to-private URLs.

`SEARXNG_SECRET` must remain available to Compose commands that interpolate the configuration. Keep it in your local `.env` or secret store, never in a commit. The top-level `use_default_settings` YAML setting enables the pinned image's defaults. Docker Desktop can run the loopback search service, but it does not satisfy the rootless requirement for code sandboxes or isolated PDF extraction. SearXNG forwards search queries to its enabled public search engines.

`make doctor` does a bounded local readiness check. `make search-smoke` performs one local SearXNG probe and reports counts/outcomes without invoking Tavily or DuckDuckGo. Explicitly use `./qagent search-smoke --allow-external --json` to probe configured external providers; it warns that Tavily may be billable. `/api/providers` and `providers --live` expose last actual provider outcomes, including timestamps and latency. Before the first request, health is unknown, not healthy.

`make providers` reads those last observed outcomes from the running API and fails if it is unavailable; it does not initiate a provider search. Collection and URL validation have separate code-level budgets: up to 12 seconds for collection, then up to 5 seconds for validation, with 1 second per URL and at most 4 validators. Parent deadlines can shorten these limits. `MAX_SEARCH_QUERIES=12` is a separate per-task query-count budget, not a timeout; no environment knobs exist for the 12/5/1-second constants or validator concurrency.

Web-research tasks without a user-provided URL require an enabled search tool, 2–3 distinct search facets, and evidence from 2–4 distinct source sites. A single conditional broadened-query retry runs only when the initial query returns zero hits; additional facets are planned only when needed for the request. Once the evidence threshold is met, remaining optional searches and fetches are recorded as skipped. Intent detection covers common entity/location lookups and informational queries in Russian and English; analysis-only plans are rejected for recognized research requests. Query text is generated from each task's facets; no topic-specific query templates are injected. If all search providers are unavailable, the task fails with `search_unavailable`. Verification checks the user's acceptance criteria and reports unmet evidence rather than treating a requested gap list as proof of completion. An incomplete persisted plan can be replaced on retry only when previously started actions were idempotent and no approval was consumed; search/fetch budgets and prior observations are preserved.

For sandbox execution, start a rootless Docker daemon and make the configured image available locally (for example, `docker pull golang:1.23-alpine` in that rootless context). Qwenchik uses `--pull=never`, `--network=none`, a read-only container root, and only the per-task workspace mount.

Manual replanning archives the previous attempt's observations and query history, then starts fresh active tool results and URL permissions. Historical evidence stays available in task history but cannot satisfy the new plan's source threshold or enter its verifier input. Query and fetched-byte budgets remain cumulative across attempts. Repeating the original discovery query is allowed on a new manual attempt; duplicate facets within the same attempt are rejected. Requests to read a supplied URL without additional search do not receive mandatory discovery steps.

To enable PDF extraction, build the dedicated Alpine/Poppler image in the local Docker context and configure its immutable local image ID:

```sh
docker pull alpine:3.23@sha256:85fe1e81d6758c208f3e1eed4338a1997e19d4be002d4dd32d3100c9a8c010a0
make pdf-extractor-image
export PDF_EXTRACTOR_IMAGE="$(docker image inspect --format '{{.Id}}' qagent-pdf-extractor:local)"
```

`PDF_EXTRACTOR_IMAGE` accepts an immutable local image ID or digest reference and rejects mutable tags. The base Alpine image is digest-pinned. Each isolated PDF extraction requires rootless Docker, no network, a read-only root, dropped capabilities, non-root UID, and CPU/memory/PID limits. Rootful Docker does not satisfy this requirement. With `PDF_ALLOW_UNSANDBOXED_FALLBACK=false`, an unavailable isolated runtime must yield `pdf_extractor_unavailable`, rather than silently invoking the pure-Go helper. Explicitly permitted pure-Go processing is process-bounded, not an OS sandbox; a Go memory target is not a hard memory boundary.

The repair profile separates the cumulative task budget (`MAX_FETCH_BYTES=134217728`, 128 MiB), ordinary source limit (`MAX_SOURCE_BYTES=16777216`, 16 MiB), and PDF limit (`MAX_PDF_BYTES=67108864`, 64 MiB). PDF parsing still limits pages to 20 and extracted text to 50,000 Unicode characters. Extraction/download deadlines are 30/120 seconds; the isolated profile uses 512 MiB memory, 64 MiB tmpfs, 1 CPU and 32 PIDs, with bounded stdout/stderr. Artifact, sandbox-file and temporary-disk quotas are separate. These values do not establish that a PDF is parseable: a 40,534,667-byte document is 40.535 MB / 38.657 MiB and fits the 64 MiB byte limit, but can still exceed page/text/resource limits.

An oversized or otherwise unusable search source receives a terminal source failure and may be followed by another authorized candidate. Header-only rejection reads zero body bytes; streaming overflow records actual bytes read and creates no partial evidence. Task cancellation, unknown outcome, exhausted cumulative budget, policy rejection and a plan requiring the exact user URL must not silently substitute a different source. Existing accepted evidence remains visible when another source fails. Research details show the exact code, sizes in bytes/MB/MiB, body-read status, remaining candidates and timestamp. “Next source permitted” does not mean fallback already happened. Partial source coverage is displayed explicitly; citations come only from accepted evidence.

Check `/api/doctor` for `research_limits` and `pdf_extractor`: effective limits, isolated runtime availability and explicit fallback permission, without secrets or raw runtime errors. The integrated doctor reports task/source/PDF limits of 128/16/64 MiB (134,217,728 / 16,777,216 / 67,108,864 bytes), pages/text 20/50,000, extraction/download deadlines 30/120 seconds, memory/tmpfs 512/64 MiB, CPU/PIDs 1/32, and unsandboxed fallback false. Artifact cap is 67,108,864 bytes, sandbox-file cap 5,000,000 bytes, and fetch-temp task/global quotas 134,217,728 / 268,435,456 bytes. In the recorded environment `PDF_EXTRACTOR_IMAGE` was unset and isolated extraction was unavailable. Doctor checks runtime readiness, not PDF parsing. The [14-section repair report](docs/pdf-limits-repair.md) records the verified file-backed download and the `verification_incomplete` research result; it makes no claim that the PDF was parsed. Restart the app after configuration changes; for installed services, restart `qwenchik-app.service` and inspect the running API's effective policy, since process environment overrides `.env`.

Set `LLM_CONTEXT_LENGTH` to the actual server context, not the model's training capacity. The example uses `32768`; if the local `/v1/models` metadata reports a lower runtime `n_ctx`, startup caps the budget to it and logs the mismatch. Byte budgeting remains an estimate, not exact tokenization. Tool-result compaction reserves IDs/URLs/titles and minimum snippets for every result before distributing remaining space; insufficient space raises a budget error rather than sending an empty evidence set to verification.

The planner uses a bounded step-array schema plus backend validation/normalization. The verifier evaluates the user's acceptance criteria against fetched evidence, with backend-owned assessment keys and per-step evidence-ID constraints. Conversation research uses the existing bounded citation critic and exact-excerpt checks. The typed Phase 1 budget allows at most 8 citation sources and 8 short claim paragraphs, with quick limited to 4 sources and 8 claims. These are limits for the legacy citation path; section-based report generation is not implemented. Unsupported or invalid synthesis falls back to explicitly labelled verbatim excerpts with limitations. Legacy task finalization can append a deterministic source list when markers are absent; that list alone does not establish sentence-level support. The critic uses the same local model and is not independent factual verification.

## Research Phase 1: fixed-plan sessions

`RESEARCH_DEFAULT_PROFILE=standard` selects the default budget for a new research send. `research_settings.profile` can select `quick`, `standard`, `deep_laptop`, `long_laptop`, or `overnight_laptop`. `RESEARCH_ADAPTIVE_ENABLED=false` keeps the existing fixed-plan worker active; setting it to `true` is rejected until Phase 3. The larger profiles do not enable adaptive rounds or promise hours of execution.

Canonical nominal budgets are defined once in `internal/domain/research_defaults.go`. Query ceilings are additionally clamped by the trusted server's `MAX_SEARCH_QUERIES`, which defaults to 12:

| Profile | Wall ceiling, seconds | Rounds ceiling | Queries nominal / default effective | Fetches | Logical LLM calls | Evidence | Questions created / accepted | Stagnant rounds ceiling |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| `quick` | 300 | 1 | 4 / 4 | 4 | 10 | 4 | 6 / 2 | 1 |
| `standard` | 1800 | 3 | 8 / 8 | 8 | 24 | 8 | 16 / 6 | 2 |
| `deep_laptop` | 7200 | 5 | 16 / 12 | 12 | 40 | 12 | 30 / 10 | 2 |
| `long_laptop` | 14400 | 8 | 32 / 12 | 24 | 72 | 24 | 60 / 20 | 2 |
| `overnight_laptop` | 21600 | 10 | 48 / 12 | 36 | 96 | 32 | 80 / 24 | 2 |

All profiles have a nominal 12-step ceiling, further bounded by `MAX_STEPS`. Round/question/stagnation fields are future-facing budget and checkpoint data, not claims that Phase 1 executes an adaptive loop. `research_settings.time_budget_seconds` accepts integral 60–21600 seconds and can shorten or extend only the wall ceiling; it does not increase query, fetch, LLM-call, evidence, or output-token limits. The absolute deadline starts at submission, so queued time and downtime consume it. Session-backed research uses that deadline rather than a fresh `TASK_TIMEOUT_SECONDS` interval on restart.

Migration `009_research_sessions.sql` adds exactly one table, `research_sessions`, plus its conversation index. New research sends create the task, messages, and session atomically. Logical model calls and initial HTTP-attempt reservations are persisted before invocation; an allowed JSON-schema-to-JSON-mode fallback reserves one additional HTTP attempt, with model HTTP attempts bounded by twice `MaxLLMCalls`. Failed or interrupted calls do not refund reservations. Existing tasks/runs without a session are not backfilled and keep the legacy execution path. Round/question frontier data belongs in versioned checkpoint JSON, not additional Phase 1 tables.

The immutable session model snapshot pins the local endpoint and model identifier, effective context, output limit, sampling settings, empty stop sequences, `PromptVersion=local-research-v1`, and schema version 1. On execution or recovery, incompatible snapshotted fetch/PDF resource settings or a smaller available model context produce `research_runtime_changed` instead of silently adopting a different policy. Configuration is loaded at startup; this is an execution/recovery compatibility check, not hot reload. The worker constructor and config share the 128 MiB `DefaultTaskFetchBudgetBytes` default; the default fetch policy uses the same cumulative task limit.

See [Research Phase 1](docs/research-phase1.md) for snapshot application, role-specific sampling, reservation accounting, migration compatibility and verification results. The existing web/PDF policy still requires rootless PDF isolation unless unsandboxed fallback is explicitly allowed. Adaptive research, section generation, an external judge, notifications, sleep/power guards, and session-specific API/UI exposure are outside Phase 1; session exposure belongs to Phase 6.

## Conversations and run semantics

`GET/POST /api/conversations`, `GET/PATCH/DELETE /api/conversations/:id`, and `GET/POST /api/conversations/:id/messages` persist chat history. Sending a turn returns the user message, assistant placeholder, and a durable run; one conversation accepts one active run. Use a stable `Idempotency-Key` when retrying the same accepted operation. Deletion archives the conversation rather than erasing its history.

`GET /api/runs/:id` reads canonical state; `POST` to `/cancel`, `/retry`, or `/continue` controls it. Stop cancels the run and preserves persisted partial text. Retry creates a new run/assistant message for the latest terminal turn. Continue also creates a new run, requires nonempty prior assistant text, and links to that answer; it is not task `/resume`. Earlier messages remain. `/events`, `/events/stream?after=N`, `/plan`, `/sources`, and `/evidence` are independent run-detail endpoints. Browser disconnects do not cancel the worker; polling and replay reconcile persisted state after reconnect.

After process restart, research/tasks requeue and reconcile checkpoints; uncertain non-idempotent outcomes require review rather than blind replay. A chat interrupted after text was persisted is marked failed with `generation_interrupted` and the message `interrupted`; use Continue or Retry explicitly. Empty interrupted chat runs may requeue. This does not promise exact token-level continuation after a model restart.

The mode is `chat` or `research` in the API (`Deep Research` in the UI); profiles are budgets within research, not additional modes. Chat uses a separate execution path without research tools. `research_settings: {"web_enabled": false}` blocks research submit/retry and is checked again at the worker claim boundary; it is not an environment flag or merely a model prompt. For new Chat and research sends, `model_profile.temperature`, `top_p`, and `max_output_tokens` are validated and frozen into the run's model snapshot, then applied to completion and streaming requests. Explicit temperature `0` is preserved. Research stores the snapshot in `research_sessions`; Chat stores it in versioned existing checkpoints and creates no research-session row. Output overrides must fit both the configured output cap and context reserve; unsupported model, endpoint, credential, and stop-sequence overrides are rejected. Legacy runs without snapshots retain prior behavior; their new API retries resolve current trusted defaults once. Research evidence and accepted user memory do not update model weights.

Retry and Continue for a session-backed research run create a new run/session with an exact copy of the prior immutable profile, policy, budget, and model snapshot, fresh counters, and a new deadline from the copied wall allowance. A same-run restart instead retains the existing snapshot, reservations, and absolute deadline. Conversation setting changes apply to a new send, not a cloned retry/continue snapshot. Current LLM credentials are supplied by the running client and are not stored in the session.

Worker generation and API doctor inference use a shared, single-slot model guard. Busy doctor inference is deferred rather than counted as a failed model probe. CLI doctor delegates to a running app; standalone inference first acquires the database worker lock. These boundaries do not serialize unrelated clients, separate databases/processes, or direct requests to llama.cpp.

CLI doctor first checks local app health. If the app responds, it makes one bounded `/api/doctor` request and returns the running process's sanitized `research_limits`, PDF runtime snapshot and check statuses. It does not substitute newly edited local policy or run a second model/runtime/storage probe. Delegation has a 34-second timeout, honors caller cancellation, disables proxies and redirects, and bounds the response body. Unavailable/malformed diagnostics produce a warning with unknown policy rather than a guessed local value. Without an app, standalone checks report local effective policy and retain the worker-ownership guard before inference.

## Persistent login-user runtime

Build the app with `make build`, then install persistent model and app services from the repository root:

```sh
./qagent runtime install \
  --model-bin /home/slager/Documents/Codex/2026-10-13/v/artifacts/runtime-vulkan/build/bin/llama-server \
  --model-path /home/slager/Documents/Codex/2026-10-13/v/artifacts/models/Qwen3.5-4B-Q4_K_M.gguf \
  --device Vulkan1 --context 32768 --enable
./qagent runtime start
./qagent runtime status
```

Both model paths are required; adapt them and the GPU device to your machine. Quote paths containing spaces. `--device` defaults to `Vulkan1` and `--context` to `32768`. The installed Vulkan build must support GPU offload, Jinja templates, and q8_0 K/V caches. Keep its shared libraries alongside the executable.

Installation rejects a context that leaves fewer than 2304 tokens after `LLM_MAX_OUTPUT_TOKENS`, and rejects temporary `go run` executable paths. Use a persistent built binary.

Installation creates `qwenchik-model.service` and `qwenchik-app.service` under `os.UserConfigDir()/systemd/user` (normally `~/.config/systemd/user`, or `$XDG_CONFIG_HOME/systemd/user`). The app uses the current working directory as its workspace and the current executable's absolute path, so install using the built `./qagent` from the repository root, rather than `go run`. Identical units are left intact. Differing units or unit symlinks cause an error before any unit is written; review and move them aside explicitly before reinstalling with changed paths or settings.

`install` never starts services. `--enable` reloads the user manager and enables both units for future login autostart. Without it, use `runtime start` for the current session; that command reloads units before starting both services. These are persistent files, unlike collected transient `systemd-run` units.

The app reads the existing workspace `.env` itself. Credentials are not copied into units, an `EnvironmentFile`, or command arguments. Units set only a basic executable `PATH`, matching `LLM_CONTEXT_LENGTH`, and `SEARCH_ENABLE_DDG=false`; other nonempty process environment settings take precedence over `.env`. The model uses loopback port 8000, the configured `LLM_MODEL` alias, one slot, and the selected context. Keep the app's configured LLM endpoint at `http://127.0.0.1:8000/v1`. The app waits up to approximately three minutes for model `/health` before starting, with a 240-second startup timeout. Both services restart on failure. The app remains running if the model later stops, so its API can report the outage.

This installs login-user services without root access or changes to lingering. With `Linger=no`, autostart occurs when the user manager starts at login; startup before login and continued operation after the final logout are not guaranteed. Check with `loginctl show-user "$USER" -p Linger`. Docker Desktop, SearXNG, and rootless sandbox dependencies are managed separately.

Make equivalents are available; `runtime-install` rebuilds the Go executable using the already-built frontend:

```sh
make runtime-install MODEL_BIN="/path with spaces/llama-server" MODEL_PATH="/models/model.gguf" ENABLE=1
make runtime-start
make runtime-status
```

After starting, check readiness and structured-generation support:

```sh
curl --noproxy '*' --fail http://127.0.0.1:8000/health
curl --noproxy '*' --fail http://127.0.0.1:8080/api/health
LLM_CONTEXT_LENGTH=32768 SEARCH_ENABLE_DDG=false ./qagent doctor --json
./qagent providers --live --json
```

Inspect login-user service status/logs without printing local credentials:

```sh
systemctl --user status qwenchik-model.service qwenchik-app.service --no-pager
journalctl --user -u qwenchik-app.service -n 100 --no-pager
```

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
CGO_ENABLED=1 go test -race -tags sqlite_fts5 ./...
CGO_ENABLED=1 go build -tags sqlite_fts5 ./...
npm --prefix frontend ci
npm --prefix frontend run typecheck
npm --prefix frontend test
npm --prefix frontend run build
```

`make test` runs tagged Go tests, frontend dependency installation, type-checking, and deterministic state/render tests. `make test-race` runs tagged Go race tests. `npm --prefix frontend run test:browser` uses system Chrome with mocked API/SSE; it is not a live-model test. `make build` rebuilds embedded assets and compiles the executable. The Phase 1 full Go tests/vet/race, frontend tests 33/33, typecheck/build and mocked Chrome checks passed. The [Phase 1 report](docs/research-phase1.md#verification-results) records a separate deployed local-model snapshot smoke. The [chat report](docs/chat-research-implementation.md) records earlier real-model and restart evidence; the [PDF repair report](docs/pdf-limits-repair.md) records the live download-only result and unavailable extractor.

The following opt-in real-browser smoke passed on the earlier deployed Chat implementation:

```sh
RUN_LIVE_BROWSER=1 LIVE_EXTENDED=1 LIVE_TIMEOUT_MS=90000 BASE_URL=http://localhost:8080 npm --prefix frontend run test:browser:live
```

It creates only a runner-owned chat, checks real SSE/Stop/reload plus bounded Retry/Continue, and soft-deletes its conversation during cleanup. Omit `LIVE_EXTENDED=1` for the shorter send/Stop scenario; `CHROME_BIN` overrides system Chrome. The runner aborts `/api/doctor` to avoid background probes and does not mock chat responses. A separate supplied-URL research run survived app restart from checkpoint seq 20 with one fetch, zero searches, one evidence, and no repeated fetch; its absolute deadline was preserved. The app was redeployed with fresh Go executable and corrected embedded CSS/fonts. Full doctor and the final post-deploy CSP-browser confirmation are tracked separately from these passed scenarios in the report.

## Configuration and security

Copy `.env.example` as a starting point. `qagent` reads `.env` from its working directory (or executable directory if no value is already set); non-empty process environment values take precedence. The defaults are local-only: SQLite under `./data`, artifacts under `./data/artifacts`, one LLM slot, and a loopback model URL. The PDF repair profile disables unsandboxed fallback by default and uses a local immutable extractor image with rootless Docker. API keys are never printed by `qagent config` or `doctor`. The running API's `research_limits` is authoritative for its injected policy; do not infer runtime settings from a newly edited environment example.

See `docs/architecture/adr-001-system.md`, `docs/contracts/`, and `docs/architecture/repository-audit.md` for decisions and contracts. The default UI and API are unauthenticated and therefore must remain bound to loopback.
