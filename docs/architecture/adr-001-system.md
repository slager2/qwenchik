# ADR-001: Go local-first modular monolith

**Status:** Accepted  
**Date:** 2026-10-14

## Context

The current master prompt requires a Go backend, local Qwen-compatible inference, durable tasks, a Kanban UI, and backend-enforced security. The architecture context document describes a Python/FastAPI option; that backend choice conflicts with the current prompt and is therefore not used. There is no existing codebase to preserve or frontend stack to extend.

## Decisions

1. Implement one Go modular-monolith process with REST/SSE, application services, a sequential durable worker, policy and typed tool boundaries.
2. Use SQLite with WAL, foreign keys, a busy timeout, transactionally persisted task state/events, and a filesystem artifact directory.
3. Use one OpenAI-compatible local LLM endpoint, defaulting to `http://127.0.0.1:8000/v1`; never silently fall back to a cloud LLM.
4. Bind the application to `127.0.0.1:8080`. Non-loopback binds fail closed until authentication for explicit LAN mode exists.
5. Use React + TypeScript + Vite for the UI. Until the UI is integrated, the API remains independently usable.
6. Keep browser and code execution behind separate providers. An unavailable sandbox must fail with `sandbox_unavailable`; it must never fall back to host shell.
7. Use a single worker loop and serialize LLM calls. Runtime roles are prompts over one model, not parallel model instances.
8. Default model context configuration is 131072 as requested by the current prompt; runtime operation must still fit the configured server and use bounded context/compaction. The conservative 8K hardware profile in the architecture context is documented as a deployment recommendation, not as a change to the config contract.

## Consequences

- SQLite and one worker keep v1 deployable on one local machine and make recovery auditable.
- Browser, remote search providers, approvals for external side effects, and sandbox execution are not implied by their future interfaces; each requires an explicit implementation and test gate.
- A local inference server is an external runtime dependency. Health checks must report it as unavailable rather than fabricate model output.
- Python-specific implementation suggestions in the context document are replaced by Go packages while retaining the modular boundaries and product goals.

## Initial implementation gate

The first vertical slice covers task creation/list/show, messages, durable lifecycle/events/checkpoints, one sequential worker, a local OpenAI-compatible LLM adapter, typed planner validation, a safe registered tool, cancellation/pause boundaries, and SSE replay. UI and richer tools follow after those contracts pass tests.
