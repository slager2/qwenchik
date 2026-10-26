# Laboratory roadmap — integration status and proof boundaries

Implementation verification baseline: `15deaf5`. This report records the
pre-release checks, performed before commit/push/deployment while the running
service was still the earlier build. Roadmap software/tooling is implemented;
**end-to-end overnight acceptance is not complete**. A subsequent application
restart does not by itself establish the live acceptance listed below.

## Implementation by phase

| Phase | Implemented and locally verified | Report |
| --- | --- | --- |
| 0 | Architecture/schema/lifecycle and gap audit | [Audit](research-lab-phase0-gap-audit.md) |
| 1 | Russian three-domain regression, bounded planner/Unicode/schema repair, replay/terminal recovery and evidence-gated context bridge | [Foundation](research-lab-phase1-foundation.md) |
| 2 | Finite adaptive rounds/frontier, budgets, stagnation, transactional decisions and recovery | [Adaptive](research-lab-phase2-adaptive.md) |
| 3 | Compact owned source notes with raw/text hashes, excerpts and criterion links | [Notes](research-lab-phase3-source-notes.md) |
| 4 | Outline/section writer/critic, one correction, fallback, recovery, coverage and JSON/Markdown artifacts | [Reports](research-lab-phase4-reports.md) |
| 5 | Capability/outcome classification and real bounded pure-Go PDF fixture extraction | [PDF](research-lab-phase5-pdf.md) |
| 6 | Privacy modes, local-only gateway routing, pinned offline corpus and scripted worker-to-report integration | [Privacy](research-lab-phase6-privacy.md) |
| 7 | Server-policy-pinned readiness and real-interval 15m/60m/4h observation CLI | [Overnight tooling](research-lab-phase7-overnight.md) |
| 8 | Fixed synthetic 12-task benchmark, four held-out tasks, existing-worker execution and snapshot comparisons | [Benchmark](research-lab-phase8-benchmark.md) |
| 9 | Production refinement-write freeze, documentation, embedded build and final regression | This report |

Architecture remains Go modular monolith + existing SQLite/WAL + one local model
slot. No cloud LLM, Python runtime, additional queue/database/checkpoint mechanism
or new migration/table. Adaptive control and section reports independently default
to **false**; privacy defaults to `local_inference_external_search`.

## Scope freeze

Production `serve` freezes refinement before accepting requests/claiming jobs:
creation, HTTP reservations, candidate/evaluation writes, promotion and bundle
rollback. New requests receive `refinement_frozen`; already-created queued jobs
stop before inference. Existing approved baseline selection and historical reads
remain available. Storage/worker tests cover already-existing jobs and unchanged
active bundles. No online self-improvement was run or expanded.

## Fresh checks

| Check | Result |
| --- | --- |
| `CGO_ENABLED=1 go test -tags sqlite_fts5 ./...` | PASS, all packages, some cached |
| `go vet -tags sqlite_fts5 ./...` | PASS |
| frontend `npm test -- --runInBand` | PASS, 77 tests |
| `npx tsc --noEmit --pretty false` | PASS |
| `npm run test:browser` | PASS, Chrome with mocked API/SSE |
| `npm run build` | PASS, embedded assets regenerated |
| `CGO_ENABLED=1 go build -tags sqlite_fts5 -o /tmp/opencode/qagent-lab ./cmd/qagent` | PASS |
| `git diff --check` | PASS |

Targeted race runs passed for offline/FIFO handling, benchmark scoring/cleanup,
soak rejection, SQLite adaptive/freeze tests, the inference guard, adaptive
two-round/reopen recovery, and supported/report-artifact recovery. The initial
combined race command hit its six-minute tool timeout; an expanded orchestrator
selection hit its four-minute test timeout. Smaller scoped runs subsequently
passed; **no full-repository race-suite pass is claimed**.

Read-only parallel reviews led to fixes for report JSON canonicalization on
recovery, duplicate outline IDs, execution/content coverage separation, FIFO
blocking, worker/session identity, server resource thresholds, false administrative
progress, current search-probe precedence, observation timing, and incomplete or
ambiguously submitted benchmark cleanup.

## Live evidence and remaining acceptance

- Real bounded pure-Go PDF fixture extraction: **PASS**.
- Explicit isolated fixture smoke: **blocked/failed capability prerequisite**;
  rootless extraction and hard memory-limit behavior were not proven.
- Readiness against the older running service: **not ready**. App/worker/DB/
  artifacts/model/fetch-temp OK; search not ready and no active run selected.
  No active inhibitor/fresh resource snapshot was established by that invocation.
- Benchmark invocation against that service: **rejected before task submission**
  because the required offline configuration/corpus was absent.
- Real-model baseline/adaptive 12-task experiment: **NOT RUN**.
- Real 15m / 60m / 4h soak: **NOT RUN / NOT RUN / NOT RUN**.
- Lid/logout/firmware/thermal and live notification acceptance: **not established**.

Operational acceptance requires deployment, working local search or pinned offline
imports, required PDF capability, and retained real benchmark/staged-soak receipts.
Scripted integration fixtures are not substitutes for elapsed/live experiments.

## Intentional limits

The cumulative 12-step ceiling is preserved and often limits adaptive work to
two or three realized rounds. Wall-time ceilings do not promise hours of workload.
The same 8-source/8-claim allowance bounds section reports. Same-model critique,
exact quotes and owned IDs do not establish independent factual truth. Benchmark
lexical/source/citation scores are labelled proxies; independent factual accuracy
and unavailable total token counts stay null. Offline routing is an application
contract, not an OS firewall or a restriction on opening citation links manually.
