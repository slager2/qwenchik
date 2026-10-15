# Repository audit

## Baseline

- Audited on 2026-10-14 before product-code changes.
- Repository was an empty directory: no source files, tests, migrations, `go.mod`, `package.json`, `README.md`, or `AGENTS.md` existed.
- No pre-existing Git repository or user changes were present.
- Environment: Linux, Go 1.26.5, CGO enabled, GCC available, Node.js 22.23.2 and npm available.
- Docker is installed. `llama-server` is not available on `PATH`; no local inference runtime was detected.
- GitHub SSH push to `git@github.com:slager2/qwenchik.git` succeeded for the initial `README.md` commit.

## Constraints and decisions

- The master prompt is the current implementation contract. Its Go requirement takes precedence over the Python backend described by the architecture context document.
- Preserve local-first behavior: API binds to loopback, default LLM endpoint is loopback on a port different from the app, and no cloud fallback is configured.
- Start with a modular monolith, SQLite/WAL, one sequential worker, durable task events, and versioned checkpoints.
- Keep browser, search providers, external side effects, and code execution out of the initial slice. No model-controlled host shell is registered.
- Use React + TypeScript + Vite for the UI once the first backend contracts are verified.

## Phase ownership

| Phase | Files/components | Gate |
|---|---|---|
| P0/P1 audit and contracts | `docs/architecture/`, `docs/contracts/` | Go, state, API, event and tool contracts are documented |
| P2 backend vertical slice | `cmd/qagent/`, `internal/{api,app,config,domain,llm,orchestrator,storage}` | SQLite persistence, task lifecycle, one worker, local LLM contract and SSE tests |
| P3 policy/tools | `internal/{policy,tools}` | Unknown tools rejected; policy tests pass |
| P4 research | `internal/providers/`, `internal/provenance/` | SSRF, injection and citation tests pass |
| P5 browser | `internal/runtime/`, `internal/tools/` | deferred until browser traffic can be forced through a public-IP-pinning path; see `docs/security/browser-gate.md` |
| P6 sandbox | `internal/runtime/sandbox/`, `internal/tools/` | rootless Docker command contract and fail-closed fake-runner tests pass; live daemon gate remains unverified |
| P7 UI/CLI and approvals | `frontend/`, `internal/cli/`, API, worker | event-driven UI, backend-owned lifecycle, durable one-time approval and resume flow |
| P8/P9 recovery, memory, and hardening | storage, worker, tests, docs | accepted-only durable lexical memory retrieval; restart/replay/security tests pass; live provider and daemon checks remain environment-dependent |

## Initial verification

The environment has Go and the required local compiler toolchain. A local Qwen endpoint and a browser driver have not been verified. Their absence must be reported by `qagent doctor`, not hidden by a fake response or cloud fallback.
