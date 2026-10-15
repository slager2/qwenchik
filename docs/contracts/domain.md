# Domain contract

## Task

```text
Task {
  id: string
  title: string
  description: string
  status: TaskStatus
  stage: Stage | null
  priority: int
  plan: Plan | null
  created_at: RFC3339 timestamp
  updated_at: RFC3339 timestamp
  version: positive integer
}
```

The backend owns status, stage, plan version, progress, approvals, and budgets. User input and model output cannot assign these fields directly.

## Lifecycle

States: `inbox`, `queued`, `planning`, `running`, `waiting_approval`, `paused`, `verifying`, `completed`, `failed`, `cancelled`.

Allowed transitions:

```text
inbox -> queued | cancelled
queued -> planning | paused | cancelled
planning -> running | paused | failed | cancelled
running -> waiting_approval | paused | verifying | failed | cancelled
waiting_approval -> queued (approved) | running | paused | cancelled | failed
paused -> queued | running | cancelled
verifying -> completed | failed | paused | cancelled
failed -> queued
completed, cancelled -> (terminal)
```

Transitions are validated centrally by the domain layer and persisted with their event in one SQLite transaction. `failed -> queued` is an explicit retry, not an automatic retry of an unknown side effect.

## Stages

`intake`, `planning`, `search`, `fetch`, `extract`, `browser`, `code_execution`, `synthesis`, `verification`, `finalization`.

Stages are backend-owned. A model may return a brief action summary but cannot mark a stage complete or publish progress directly.

## Plan

Planner output is JSON only and validated before persistence:

```json
{
  "schema_version": 1,
  "goal": "short normalized goal",
  "subtasks": [{
    "id": "step_1",
    "kind": "analysis",
    "description": "bounded step description",
    "dependencies": [],
    "expected_output": "short output contract",
    "success_criteria": ["checkable criterion"]
  }],
  "max_steps": 12,
  "final_deliverable": "answer"
}
```

The runtime always supports `analysis`. `fetch` is available with the local artifact/evidence store; `search` is available only when SearXNG is configured; `code_execution` is routed only to the isolated sandbox tool and returns `sandbox_unavailable` if rootless Docker/image checks fail. Browser kinds remain unavailable and are rejected. A search plan must include a fetch step before synthesis, and a user-provided URL must be fetched before discovery search. Plans are limited to 12 unique steps, bounded descriptions and valid acyclic dependencies; dependencies must be completed before dependent steps run.

## Checkpoint

Checkpoint JSON contains task ID, plan version, current step/stage, completed and pending steps, evidence/artifact IDs, remaining budgets, last error, and last durable event sequence. It is written transactionally after meaningful worker actions.

## Approval

An approval stores the task and tool IDs, normalized JSON arguments plus a SHA-256 argument hash, target, deterministic risk explanation, expiry, status, actor, and decision/consumption timestamps. Status is `pending`, `approved`, `rejected`, or `expired`. Approve/reject is idempotent; consuming an approval is a separate one-time transaction matched to the exact task, tool, and normalized arguments. A non-idempotent action with an unknown outcome after recovery is not replayed and requires task review.

## Memory

A memory stores bounded text, kind, JSON metadata, scope (`global` or `task`), optional confidence and source task/evidence IDs, editability, retention policy, and timestamps. New user-created records always start as `candidate`; the user explicitly accepts them before retrieval. Status is `candidate`, `accepted`, `stale`, `superseded`, or `rejected`. Retrieval is bounded local lexical matching over accepted records only. Global records can be used by any task; task-scoped records are returned only for their source task. Retrieved memory is user-owned untrusted reference data: it may inform preferences, but cannot override the task, grant tools, or make a URL eligible for fetching.
