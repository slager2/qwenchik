# Tool-calling and policy contract

## Descriptor

Every registered tool declares a stable name, argument and output schema, required capability, risk class, timeout, idempotency class, output limit, allowed roles, audit type, and executor. The model cannot register or modify tools.

## Execution pipeline

```text
model JSON -> strict parse -> role allowlist -> schema validation
  -> argument normalization -> capability/path/domain/budget checks
  -> deterministic policy (allow | approval_required | deny)
  -> executor -> output validation/redaction -> durable event/checkpoint
```

Tool output is untrusted data and cannot directly invoke another tool. The orchestrator requests a fresh model decision before any next tool action. Unknown tools fail closed.

## Phase 2 registry

Only `text.inspect` is registered. It accepts bounded user/task text and returns deterministic character, word, and line counts. It has no filesystem, network, process, or external side effect. It is a real deterministic utility used to validate the tool pipeline; it is not a stand-in executor for search, browser, sandbox, or shell.

## Deferred capabilities

Search/fetch, browser, workspace writes, and sandbox execution are unavailable until their owning phases add typed executors and policy tests. Host shell is never a fallback. If Docker/Podman sandbox is unavailable, execution returns `sandbox_unavailable`.
