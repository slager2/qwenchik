# Planner semantic repair

The reported run `72270073-e763-483e-a114-445a30467489` stopped before
`plan.created`: both planner responses were structurally valid JSON containing
four searches and no fetches. The normalizer returned early above the three-facet
limit, and the correction path wrapped the subsequent semantic validation error
as `invalid_model_output`.

## Deterministic repair

The search limit remains three and the configured `MAX_STEPS` is unchanged.
Normalization packs original search descriptions into at most three groups,
preserving their complete wording and expected-output text. Packing is
deterministic and bounded by the twelve-step input schema. It can use two groups
when the existing step limit leaves room for only two searches and two fetches.
Text is never truncated to make a merge fit: impossible packing enters one
semantic correction and then fails as `research_plan_incomplete` if unresolved.

For the four reported short facets, an ordinary research plan becomes:

```json
{
  "schema_version": 1,
  "goal": "Исследовать направления обучения",
  "max_steps": 5,
  "subtasks": [
    {"id":"search_1","kind":"search","description":"математика\n\nбиоинженерия","dependencies":[]},
    {"id":"search_2","kind":"search","description":"информатика и программирование","dependencies":[]},
    {"id":"search_3","kind":"search","description":"химия и биология","dependencies":[]},
    {"id":"fetch_1","kind":"fetch","dependencies":["search_1","search_2","search_3"]},
    {"id":"fetch_2","kind":"fetch","dependencies":["search_1","search_2","search_3"]}
  ]
}
```

This is a compact illustration: actual steps also contain expected outputs and
execution success criteria; descriptions receive user-goal guidance when it fits
without shortening the original facet. A user-provided URL is fetched first;
that first fetch has no search dependencies, followed by independent discovery
searches and a further fetch depending on them all.

Required kinds and dependencies are backend semantic checks, not an assumption
about JSON Schema array length. Self-contained text analysis never gains search
steps during normalization. Missing search capability is `search_unavailable`.
Malformed JSON/schema output remains `invalid_model_output`.

## Bounded correction and diagnostics

There is one semantic correction at most. Each role retains the existing single
JSON/schema-format repair; these are separate bounds. Correction input includes
the full prior plan, safe backend reason, allowed kinds, required search/fetch
counts, and the step limit. Pinned correction context includes those constraints
in its identity. Corrected plans are normalized and validated again; original
search facets cannot disappear from the correction.

`task.failed` carries `failure_stage`, `failure_role`, `validation_error`,
`planner_attempt`, `plan_summary`, and `error_code`. Reasons use a finite backend
catalogue and plan summaries contain counts only. REST and SSE project the same
safe fields. The UI distinguishes planning failure from unavailable details,
shows matching-run model/search/fetch counters, and offers Retry. Continue remains
disabled when the current run has no assistant text.

The integration regression proves that a realistic four-facet plan is saved and
the search provider is actually invoked afterward. It does not claim that a real
provider/model research run necessarily succeeds: later network, source, budget,
and evidence verification failures remain independent outcomes.
