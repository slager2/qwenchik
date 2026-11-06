# Qwenchik

Local-first conversations, Deep Research, and a durable workspace for one local Qwen model. The backend is a Go modular monolith; conversations, messages, runs, checkpoints, and events live in SQLite/WAL. The React + TypeScript UI is served from the same process. Chats open at `#/chat`; the existing task board, approvals, and memory remain at `#/workspace`.

Historical Chat/PDF verification: Go tests/vet/race, 33 frontend tests, embedded build, mocked Chrome, real Chat Stop/Retry/Continue, and checkpoint-restart smoke passed in the earlier implementation reports. The gated live PDF test verified a file-backed download only; rootless extraction was unavailable, and research run `52f28017-840c-47e9-8297-70a4a5580941` ended `verification_incomplete`, not `complete`. A separate HTTP/3 research run retained real sources/evidence but used explicit excerpt fallback, not fully verified synthesized claims. See the [14-section PDF repair report](docs/pdf-limits-repair.md) and [12-section chat report](docs/chat-research-implementation.md) for run IDs, commands, and limits. Current Phase 1 tests/vet/race, frontend tests/build/browser checks and a bounded deployed snapshot smoke also passed; their distinct scope and evidence are documented in [Research Phase 1](docs/research-phase1.md#verification-results).

## Current implementation

The laboratory roadmap now adds opt-in bounded adaptive rounds, compact owned
source notes, checkpointed section reports/JSON+Markdown artifacts, three explicit
privacy contracts, offline text imports, and `lab readiness` / `lab soak` /
`lab benchmark` / `lab compare`. See the [laboratory status and verification
boundaries](docs/research-lab-phase9-status.md). Live isolated-PDF extraction,
15m/60m/4h soak, and successful real-model research are **not established**
by the local regression suite. The [Phase 3 offline comparison](docs/deep-research-phase3-offline-benchmark.md)
measured all 12 tasks in both variants: 23 model-RSS pauses, one failed/blocked
task, and zero delivered reports. Measurement completeness is not research success.

The [Phase 5 frontend](docs/deep-research-phase5-frontend.md) adds a task-first
research overview, readable saved reports with section navigation, next-run
settings, explicit mobile Details, shared pause explanations and passive viewing
without automatic Doctor generation. The shared [design system](design.md) keeps
the existing Cobalt direction. Its UI verification does not establish research
quality; Phase 4 was explicitly skipped.

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
- research sessions with immutable profile/privacy/model snapshots, persisted absolute deadlines, durable pre-call reservations and an opt-in adaptive controller on the existing worker;
- SSE replay using `Last-Event-ID` or an initial `after` cursor, with canonical persisted message snapshots;
- typed host/inference telemetry in Chat Details and Workspace, with provenance/freshness labels, live SSE updates, and bounded final inference summaries;
- conversation sidebar, safe Markdown/GFM and evidence links, independent research details, mobile drawers, plus the retained Kanban, inspector, approvals, memories, and command palette.

See [runtime telemetry](docs/runtime-telemetry.md) for metric provenance, platform limitations, persistence boundaries, and the local smoke procedure.

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
     -ctk q8_0 -ctv q8_0 \
     --cache-ram 512 --ctx-checkpoints 4 \
     --threads 4 --threads-batch 4 --poll 0 --poll-batch 0
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

### Docker all-in-one launch

To avoid manually starting the app and llama-server, use the root `compose.yaml`. It builds the embedded frontend and Qwenchik image, starts the official Vulkan llama.cpp server, waits for its healthcheck, and then starts the app. The two containers share a network namespace so the app keeps its loopback-only LLM policy; only `127.0.0.1:8080` is published. Full setup and GPU caveats are in [docs/docker.md](docs/docker.md).

```sh
mkdir -p models
# copy a GGUF into ./models, or set MODEL_FILE/MODELS_DIR in local .env
docker compose config -q
docker compose up -d --build
```

The Docker stack is intentionally conservative (`parallel=1`, context `32768`, bounded CPU/RAM/PIDs). Docker does not remove thermal limits: check `sensors` before starting a model. The app container does not receive `docker.sock`; sandbox and isolated PDF extraction therefore remain unavailable in this first containerized profile.

Optional local web search can be started with the included Compose configuration:

```sh
export SEARXNG_SECRET="$(openssl rand -hex 32)"
docker compose -f compose.searxng.yml up -d
export SEARXNG_URL=http://127.0.0.1:8081
```

Optionally configure Tavily with `TAVILY_API_KEY` before starting `qagent serve`; diagnostics expose configured status, not the key. `SEARCH_PROVIDER_POLICY=local_primary` is the default routing policy; explicit `parallel` merges/deduplicates configured providers. `RESEARCH_PRIVACY_MODE=local_searxng` uses only the local gateway for search, whose engines still access the Web; `offline` uses only a SHA-pinned local text corpus and no network search/fetch. See [privacy contracts/import format](docs/research-lab-phase6-privacy.md). Search hits remain untrusted leads and must be fetched before citation. Configuration is read at startup. The bundled SearXNG service is loopback-bound and enables JSON; doctor does not call Tavily. Stop it with `docker compose -f compose.searxng.yml down` while `SEARXNG_SECRET` is set. `ALLOWED_DOMAINS` narrows search/fetch; the web fetcher rejects private/reserved/credential-bearing/nonstandard-port targets and unsafe redirects.

`SEARXNG_SECRET` must remain available to Compose commands that interpolate the configuration. Keep it in your local `.env` or secret store, never in a commit. The top-level `use_default_settings` YAML setting enables the pinned image's defaults. Docker Desktop can run the loopback search service, but it does not satisfy the rootless requirement for code sandboxes or isolated PDF extraction. SearXNG forwards search queries to its enabled public search engines.

`make doctor` does a bounded local readiness check. `make search-smoke` performs one local SearXNG probe and reports counts/outcomes without invoking Tavily or DuckDuckGo. Explicitly use `./qagent search-smoke --allow-external --json` to probe configured external providers; it warns that Tavily may be billable. `/api/providers` and `providers --live` expose last actual provider outcomes, including timestamps and latency. Before the first request, health is unknown, not healthy.

`make providers` reads those last observed outcomes from the running API and fails if it is unavailable; it does not initiate a provider search. Collection and URL validation have separate code-level budgets: up to 12 seconds for collection (longer when a provider such as agentSearcher declares its own budget), then up to 5 seconds for validation shared by at most 4 validators (each URL gets the budget divided by the number of validator waves, at least 1 second); when no hit validates in time but the task already has evidence or candidate URLs, the search counts as empty instead of failing the run. Parent deadlines can shorten these limits. `MAX_SEARCH_QUERIES=12` is a separate per-task query-count budget, not a timeout; no environment knobs exist for the 12/5/1-second constants or validator concurrency.

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

Set `LLM_CONTEXT_LENGTH` to the actual single-slot server runtime, not training
capacity. The source default is now 32768. Doctor/run diagnostics keep configured,
server-reported and saved-session context distinct, with separate output and
2048-token system reserves. Mismatch/unverified capacity blocks dispatch rather
than silently adopting a configured number. Production admission freshly checks
`/props` and matching model metadata, renders the complete request with
`/apply-template`, and counts `/tokenize` tokens before generation. Existing byte
compaction/persistence bounds remain separate resource limits. Saved context and
thinking settings are not silently enlarged on recovery.

`MODEL_RSS_MAX_BYTES` separately guards llama-server RSS (default 5 GiB); it is
not the Go process RSS cap. Resource problems preserve checkpoints and use bounded
recovery. `CONTEXT_PROBE_ENABLED=false` keeps the opt-in fixed synthetic load probe
disabled for normal serving. `lab context-check` records one operator-configured
rung through the existing shared guard and worker. See the measured
[Phase 2 context report](docs/deep-research-phase2-context-ladder.md): 32K/64K load
probes passed, 96K/128K hit the fixed 120s timeout, and multi-call research remained
model-RSS-limited. These are not overnight or general 128K-support claims.

The supported production default remains **32768**. A **65536 experimental
opt-in** is available for controlled single-slot measurements only. It is not a
claim that 64K multi-call research is stable on this laptop: the recorded 64K
retention probe passed, but its research run paused on `high_model_rss` and
reached 81°C GPU temperature. Do not enable it while another heavy workload is
running or while CPU, GPU, or RAM sensors are in alarm. Keep `--parallel 1`,
q8_0 K/V caches, the bounded 512 MiB prompt cache, and the existing resource
guards. Do not use 96K or 128K as a normal runtime configuration.

Changing only `.env` is insufficient. The installed model unit's `-c` value and
the app unit's `LLM_CONTEXT_LENGTH` must both be 65536; `runtime install
--context 65536` renders that matched pair. The installer deliberately refuses
to overwrite different existing units, so archive and review the old unit files
before installing the experimental pair. Keep the 32K units available for an
immediate rollback. After starting, verify `/api/doctor` reports configured and
server context 65536 before sending any inference. A mismatch or unavailable
runtime observation fails closed.

Existing conversations and research sessions retain their immutable model
snapshot. Enabling the experimental runtime does not rewrite a saved 32K session
to 64K; retry/continue preserves its original context. Start a new session for a
64K experiment. Validate it in stages: three cold retention probes, a bounded
15-minute multi-call research run, then a 60-minute soak. Roll back to 32K on any
OOM/restart, `high_model_rss`, sensor alarm, missing output, or context mismatch.

### Controlled offline comparison

`lab benchmark` now requires `--protocol-sha256`, the SHA256 of a frozen
experiment manifest. It uses passive `GET /api/lab/policy` discovery instead of
the inference-capable doctor. Configure the running server for the exact embedded
offline corpus, section reports and the selected adaptive setting. The CLI's
environment does not reconfigure that server.

```sh
./qagent lab benchmark --variant baseline --split all --case-timeout 35m \
  --protocol-sha256 "$PROTOCOL_SHA256" --output baseline.jsonl
./qagent lab benchmark --variant adaptive --split all --case-timeout 35m \
  --protocol-sha256 "$PROTOCOL_SHA256" --output adaptive.jsonl
./qagent lab compare baseline.jsonl adaptive.jsonl
```

`--case ID` selects one fixed task for controlled per-case process starts.
`--stop-on-pause` requires the offline-only `LAB_HOLD_RESOURCE_PAUSES=true` server
setting, retains checkpoints, writes its receipt and returns nonzero on a pause.
It does not enable the context load probe. Receipts are exclusive-create and
separate collected text, report delivery, successful outcomes, missing metrics
and app/model RSS. Comparison requires all 12 cases per variant and matching
experiment-wide snapshots; complete accounting does not imply successful
research. Source/lexical/citation-ownership scores are proxies, and independent
factual accuracy remains null. The [Phase 3 protocol](docs/experiments/phase3/protocol.json)
declares the cold-process, alternating-order experiment and its failure rules.
The [Phase 3 report](docs/deep-research-phase3-offline-benchmark.md) records the
24 real attempts, development/held-out populations and remaining unknowns.

The planner uses bounded structural/semantic validation and lossless facet normalization. New version-two sessions preserve substantive user-deliverable criteria independently of acquisition steps; report coverage cannot substitute “source stored” for those criteria. Saved version-one sessions retain their original budgets. New section reports have explicit section/paragraph/claim/byte budgets and at most one correction per section; byte exhaustion produces deterministic partial excerpts and bounded JSON/Markdown artifacts. A source list or same-model critic does not establish independent factual truth. See [Deep Research Phase 1](docs/deep-research-phase1-round-budgets.md).

## Research profiles and saved sessions

`RESEARCH_DEFAULT_PROFILE=standard` selects the default budget for a new research send. `research_settings.profile` can select `quick`, `standard`, `deep_laptop`, `long_laptop`, or `overnight_laptop`. `RESEARCH_ADAPTIVE_ENABLED=false` is the compatibility default; `true` enables bounded durable evidence-driven continuation rounds. `RESEARCH_SECTION_REPORT_ENABLED=true` independently enables section reports. Both flags are saved at submission. Larger wall ceilings do not promise hours of execution or a particular report length. Each profile also saves an initial fetch breadth (`fetch_target`: quick 2, standard 6, deep_laptop 8, long/overnight 9, capped by its fetch and evidence budgets): the backend adds deterministic fetch steps up to it and stops fetching once that many distinct sources are stored. The request's minimum of 2 sources still decides failure; extra fetch steps are skipped when no unused validated URL remains.

New session budgets are defined in `internal/domain/round_plans.go`; historical
version-one defaults remain in `research_defaults.go` for recovery compatibility.
`MAX_SEARCH_QUERIES` now defaults to 48, so longer profiles are not silently clamped
to 12 queries. All values below are ceilings, not work quotas:

| Profile | Wall seconds | Rounds | Total steps | Search | Fetch | LLM calls | Evidence | Questions created/accepted |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| `quick` | 300 | 1 | 12 | 4 | 4 | 24 | 4 | 8 / 8 |
| `standard` | 1800 | 3 | 20 | 8 | 8 | 48 | 8 | 24 / 16 |
| `deep_laptop` | 7200 | 5 | 32 | 16 | 12 | 64 | 12 | 40 / 24 |
| `long_laptop` | 14400 | 8 | 64 | 32 | 24 | 96 | 24 | 64 / 32 |
| `overnight_laptop` | 21600 | 10 | 96 | 48 | 36 | 128 | 32 | 80 / 40 |

For version-two sessions, the initial plan stays in `tasks.plan_json`; bounded
round plans live in the existing checkpoint, and `task_steps`/events retain their
execution history. Initial and individual round plans default to at most 12 steps;
there is no cumulative 12-step plan bottleneck. Round sizes use remaining budgets
and remaining rounds; an explicit lower `MAX_ROUND_STEPS` remains respected.
`MAX_STEPS` is a compatibility/initial-plan alias, not the total-run limit.
`MAX_TOTAL_STEPS` and the other explicit caps in `.env.example` can reduce profiles.
Reaching readiness stops work immediately; time, resource, stagnation and evidence
budgets can stop it earlier. `research_settings.time_budget_seconds` changes only
the wall ceiling. The deadline starts at submission and includes downtime; restart
does not refund consumed calls, operations or admitted steps. Saved v1 sessions
are not silently upgraded and keep their original 12-step allowance.

**No artificial plan-step ceiling. Still bounded by explicit time, search, fetch,
LLM, evidence and resource budgets.** This is implemented/scripted-test scope;
real-model/context-ladder/overnight acceptance remains separate.

Migration `009_research_sessions.sql` adds exactly one table, `research_sessions`, plus its conversation index. New research sends create the task, messages, and session atomically. Logical model calls and initial HTTP-attempt reservations are persisted before invocation; an allowed JSON-schema-to-JSON-mode fallback reserves one additional HTTP attempt, with model HTTP attempts bounded by twice `MaxLLMCalls`. Failed or interrupted calls do not refund reservations. Existing tasks/runs without a session are not backfilled and keep the legacy execution path. Round/question frontier data belongs in versioned checkpoint JSON, not additional Phase 1 tables.

The immutable session model snapshot pins the local endpoint and model identifier, effective context, output limit, sampling settings, empty stop sequences, `PromptVersion=local-research-v1`, and schema version 1. On execution or recovery, incompatible snapshotted fetch/PDF resource settings or a smaller available model context produce `research_runtime_changed` instead of silently adopting a different policy. Configuration is loaded at startup; this is an execution/recovery compatibility check, not hot reload. The worker constructor and config share the 128 MiB `DefaultTaskFetchBudgetBytes` default; the default fetch policy uses the same cumulative task limit.

See the historical [Research Phase 1](docs/research-phase1.md) for snapshot and migration foundations; its phase exclusions are not today's capability inventory. Current adaptive/report/privacy and acceptance boundaries are documented in the [laboratory status](docs/research-lab-phase9-status.md). Web/PDF policy still requires rootless PDF isolation unless unsandboxed fallback is explicitly allowed. There is no external factual judge. Production `serve` freezes refinement creation, candidate/evaluation/promotion and bundle rollback writes; historical reads remain available.

## Conversations and run semantics

`GET/POST /api/conversations`, `GET/PATCH/DELETE /api/conversations/:id`, and `GET/POST /api/conversations/:id/messages` persist chat history. Sending a turn returns the user message, assistant placeholder, and a durable run; one conversation accepts one active run. Use a stable `Idempotency-Key` when retrying the same accepted operation. Deletion archives the conversation rather than erasing its history.

`GET /api/runs/:id` reads canonical state; `POST` to `/cancel`, `/retry`, or `/continue` controls it. Stop cancels the run and preserves persisted partial text. Retry creates a new run/assistant message for the latest terminal turn. Continue also creates a new run, requires nonempty prior assistant text, and links to that answer; it is not task `/resume`. Earlier messages remain. `/events`, `/events/stream?after=N`, `/plan`, `/sources`, and `/evidence` are independent run-detail endpoints. Browser disconnects do not cancel the worker; polling and replay reconcile persisted state after reconnect.

After process restart, research/tasks requeue and reconcile checkpoints; uncertain non-idempotent outcomes require review rather than blind replay. A chat interrupted after text was persisted is marked failed with `generation_interrupted` and the message `interrupted`; use Continue or Retry explicitly. Empty interrupted chat runs may requeue. This does not promise exact token-level continuation after a model restart.

The mode is `chat` or `research` in the API (`Deep Research` in the UI); profiles are budgets within research, not additional modes. Chat uses a separate execution path without research tools. `research_settings: {"web_enabled": false}` blocks research submit/retry and is checked again at the worker claim boundary; it is not an environment flag or merely a model prompt. For new Chat and research sends, `model_profile.temperature`, `top_p`, and `max_output_tokens` are validated and frozen into the run's model snapshot, then applied to completion and streaming requests. Explicit temperature `0` is preserved. Research stores the snapshot in `research_sessions`; Chat stores it in versioned existing checkpoints and creates no research-session row. Output overrides must fit both the configured output cap and context reserve; unsupported model, endpoint, credential, and stop-sequence overrides are rejected. Legacy runs without snapshots retain prior behavior; their new API retries resolve current trusted defaults once. Research evidence and accepted user memory do not update model weights.

Retry and Continue for a session-backed research run create a new run/session with an exact copy of the prior immutable profile, policy, budget, and model snapshot, fresh counters, and a new deadline from the copied wall allowance. A same-run restart instead retains the existing snapshot, reservations, and absolute deadline. Conversation setting changes apply to a new send, not a cloned retry/continue snapshot. Current LLM credentials are supplied by the running client and are not stored in the session.

Worker generation and API doctor inference use a shared, single-slot model guard. Busy doctor inference is deferred rather than counted as a failed model probe. CLI doctor delegates to a running app; standalone inference first acquires the database worker lock. These boundaries do not serialize unrelated clients, separate databases/processes, or direct requests to llama.cpp.

CLI doctor first checks local app health. If the app responds, it makes one bounded `/api/doctor` request and returns the running process's sanitized `research_limits`, PDF runtime snapshot and check statuses. It does not substitute newly edited local policy or run a second model/runtime/storage probe. Delegation has a 34-second timeout, honors caller cancellation, disables proxies and redirects, and bounds the response body. Unavailable/malformed diagnostics produce a warning with unknown policy rather than a guessed local value. Without an app, standalone checks report local effective policy and retain the worker-ownership guard before inference.

## On-demand launch (recommended)

The model holds ~1.6 GB RAM and ~3.3 GB VRAM while running, so it should not autostart. Install the units **without** `--enable` (see below), then use the launcher:

```sh
ln -sfn "$PWD/scripts/qwenchik" ~/.local/bin/qwenchik
qwenchik start            # model + app natively (GPU/Vulkan), opens the browser
qwenchik start --search   # also SearXNG in Docker; starts Docker Desktop only if it was not running
qwenchik stop             # stops everything start brought up and frees RAM/VRAM
qwenchik status | open | logs [model|app|search]
qwenchik install-search   # one-time: install agentSearcher (local search service)
```

The model and app run natively because GPU offload is unavailable inside the Docker Desktop VM. Only SearXNG runs in a container (`compose.searxng.yml`). Its secret is generated once into `~/.config/qwenchik/searxng.env`. Without SearXNG, search falls back to the other configured providers. When run without a terminal (for example from a desktop menu entry with `Exec=~/.local/bin/qwenchik start`), the launcher reports results through desktop notifications.

### Local search with agentSearcher (optional)

[agentSearcher](https://github.com/peakQuick/agentSearcher) (AGPL-3.0) is a separate local service: for each query it searches public engines, fetches and renders the result pages and returns ranked passages (up to three ~600-character chunks per page) instead of short snippets. qagent never bundles its code; it only calls its Tavily-compatible HTTP API on loopback.

- `qwenchik install-search` installs it idempotently into `~/.local/share/qwenchik/agentsearcher/venv` from a pinned commit and runs `agentsearcher setup` (headless Chromium in `~/.cache/ms-playwright`, ~120 MB reranker in `~/.cache/huggingface`). It needs `python3-venv`; the script never uses sudo.
- If installed, `qwenchik start` also starts it in the background on `127.0.0.1:8484` (PID in `~/.local/state/qwenchik/agentsearcher.pid`, log in `agentsearcher.log` there), waits for `GET /health`, and `qwenchik stop` stops it. `qwenchik status` shows it. It idles at ~75 MB and unloads Chromium and the model after two idle minutes.
- Enable it for the app with `AGENTSEARCHER_URL=http://127.0.0.1:8484` in `.env` (loopback only, like `SEARXNG_URL`). It then becomes the primary provider under `SEARCH_PROVIDER_POLICY=local_primary`; SearXNG/Tavily/DuckDuckGo stay fallbacks for errors, empty answers or single-site answers. `AGENTSEARCHER_SEARCH_DEPTH` (`advanced` default, or `basic`/`fast`/`ultra-fast`) and `AGENTSEARCHER_TIMEOUT_SECONDS` (45 default, 5..120) tune it; the search tool timeout follows that budget. A measured `advanced` search takes ~10-21 s, `basic` ~4 s.
- agentSearcher still forwards queries to public search engines, so it is an external-search provider (`RESEARCH_PRIVACY_MODE=local_searxng` and `offline` do not use it). It blocks private/loopback page URLs itself; qagent still validates every result URL with its own SSRF policy, and passages remain untrusted leads that must be fetched before citation. `qagent doctor` checks only its `/health`.

### Windows

`qagent runtime install` uses systemd and is Linux-only. On Windows, use `scripts/qwenchik.ps1` (Windows PowerShell 5.1 or PowerShell 7) with a Vulkan build of `llama-server.exe`:

```powershell
.\scripts\qwenchik.ps1 setup -ModelBin C:\llama\llama-server.exe -ModelPath C:\models\Qwen3.5-4B-Q4_K_M.gguf -Device Vulkan0
.\scripts\qwenchik.ps1 build          # qagent.exe with the embedded UI (needs Go)
.\scripts\qwenchik.ps1 start          # or the "Qwenchik" desktop shortcut created by setup
.\scripts\qwenchik.ps1 stop           # or the "Qwenchik - stop" shortcut
.\scripts\qwenchik.ps1 status | open | logs model|app
```

Run `llama-server.exe --list-devices` to pick `-Device`. Processes run hidden; logs and PIDs are kept in `%LOCALAPPDATA%\qwenchik`. On Windows, keep-awake and desktop notifications are no-ops, and the PDF extraction sandbox is untested. CI runs the Go test suite and launcher smoke checks on `windows-latest`.

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

Both model paths are required; adapt them and the GPU device to your machine. Quote paths containing spaces. `--device` defaults to `Vulkan1` and `--context` to the production default `32768`; `--context 65536` is an experimental opt-in subject to the staged acceptance and rollback rules above. The installed Vulkan build must support GPU offload, Jinja templates, and q8_0 K/V caches. Keep its shared libraries alongside the executable.

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

The `sqlite_fts5` tag is required for every Go command that touches storage: without it the SQLite driver lacks FTS5, `internal/storage/sqlite` tests stop immediately with "build with -tags sqlite_fts5 (see Makefile)", and other packages fail opening the store with `no such module: fts5` plus the same hint.

`make test` runs tagged Go tests, frontend dependency installation, type-checking, and deterministic state/render tests. `make test-race` runs tagged Go race tests. `npm --prefix frontend run test:browser` uses system Chrome with mocked API/SSE; it is not a live-model test. `make build` rebuilds embedded assets and compiles the executable. The Phase 1 full Go tests/vet/race, frontend tests 33/33, typecheck/build and mocked Chrome checks passed. The [Phase 1 report](docs/research-phase1.md#verification-results) records a separate deployed local-model snapshot smoke. The [chat report](docs/chat-research-implementation.md) records earlier real-model and restart evidence; the [PDF repair report](docs/pdf-limits-repair.md) records the live download-only result and unavailable extractor.

The following opt-in real-browser smoke passed on the earlier deployed Chat implementation:

```sh
RUN_LIVE_BROWSER=1 LIVE_EXTENDED=1 LIVE_TIMEOUT_MS=90000 BASE_URL=http://localhost:8080 npm --prefix frontend run test:browser:live
```

It creates only a runner-owned chat, checks real SSE/Stop/reload plus bounded Retry/Continue, and soft-deletes its conversation during cleanup. Omit `LIVE_EXTENDED=1` for the shorter send/Stop scenario; `CHROME_BIN` overrides system Chrome. The runner aborts `/api/doctor` to avoid background probes and does not mock chat responses. A separate supplied-URL research run survived app restart from checkpoint seq 20 with one fetch, zero searches, one evidence, and no repeated fetch; its absolute deadline was preserved. The app was redeployed with fresh Go executable and corrected embedded CSS/fonts. Full doctor and the final post-deploy CSP-browser confirmation are tracked separately from these passed scenarios in the report.

## Configuration and security

Copy `.env.example` as a starting point. `qagent` reads `.env` from its working directory (or executable directory if no value is already set); non-empty process environment values take precedence. The defaults are local-only: SQLite under `./data`, artifacts under `./data/artifacts`, one LLM slot, and a loopback model URL. The PDF repair profile disables unsandboxed fallback by default and uses a local immutable extractor image with rootless Docker. API keys are never printed by `qagent config` or `doctor`. The running API's `research_limits` is authoritative for its injected policy; do not infer runtime settings from a newly edited environment example.

See `docs/architecture/adr-001-system.md`, `docs/contracts/`, and `docs/architecture/repository-audit.md` for decisions and contracts. The default UI and API are unauthenticated and therefore must remain bound to loopback.
