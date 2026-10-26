# Laboratory Phase 1 — foundation and test contract

Based on `15deaf5`; integrated sequentially after parallel read-only audits.

## Changes

- Exact regression: «сделай роадмап по биоинформатике, химии и информатике».
  A scripted four-search/no-fetch response preserves every facet, adds fetches,
  commits a canonical plan and reaches the provider after durable reservation.
- Bounded raw proposal admission is separate from the final execution step limit.
  Configured `MaxSteps` and inference concurrency are unchanged.
- Planner/domain/packing string bounds consistently count Unicode characters.
  Local schema validation enforces identifier patterns; invalid IDs remain
  structural `invalid_model_output`, not semantic plan failures.
- Safe diagnostic catalogue preserves old byte-limit reasons for event replay.
- Public-only transcript events, bounded REST diagnostic history after resync or
  unavailable SSE, sticky diagnostic anchors and final terminal reconciliation.
- Inspector checks loaded plan data before claiming a plan was not saved.
- Research-context continuation requires current saved text, verification and
  usable cited owned evidence, independently of Details visibility.

## Verification

Passed: `CGO_ENABLED=1 go test -tags sqlite_fts5 ./...`,
`go vet -tags sqlite_fts5 ./...`, frontend `npm test -- --runInBand` (77 tests),
`npx tsc --noEmit --pretty false`, and mocked Chrome browser regressions.
Embedded frontend was rebuilt and embedded API checks passed.

## Limits

The roadmap regression uses deterministic model/provider fixtures; it proves
execution boundaries, not the quality of a live generated roadmap. The deliberate
provider failure is not a successful external search. No overnight soak, model
weight change, adaptive mode or new persistence subsystem is part of Phase 1.
REST history recovery is bounded to 20 pages and retains a bounded activity window
plus diagnostic anchors; excessive history surfaces an explicit synchronization
error. Existing single-slot inference, leases, fences and source policies remain.
