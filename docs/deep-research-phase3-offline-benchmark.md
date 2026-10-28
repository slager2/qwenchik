# Deep Research Phase 3 — real local offline baseline/adaptive benchmark

Date: 2026-10-28. Base commit: `68dbebf9a26ff3eab329bc6c8f6bd93a051bd720`.

## Verdict

**All 12 cases were measured once per variant; neither variant delivered a
finished answer or report.** Baseline had 11 `high_model_rss` pauses and one
failed/blocked `invalid_model_output` task. Adaptive had 12 `high_model_rss`
pauses. The full-set comparison validates measurement consistency, not research
success. Adaptive quality benefit and stable multi-call research remain
**Not established**.

## Scope and evidence

This phase runs the real local model on the existing fixed **12-task synthetic
offline benchmark**, once under each variant: 24 prescribed slots. Eight tasks
are development cases and four are held-out. Both variants use the same model,
context, output reservation, imported corpus and budget ceilings. Parallel agents
audited and reviewed the implementation; inference remained sequential through
the existing shared slot.

The frozen [protocol](experiments/phase3/protocol.json) has SHA256
`974eef5c61a044f796113a8bd6c7cb9093dd81ae2f0384fceb6bf170312e7d8e`.
The [operator script](experiments/phase3/run.mjs) has SHA256
`8a94e3914a2eea151028bcd3e6820c0b62985357c960450789c0bce061ae7e24`.
It invokes the existing Go CLI/API/worker and does not introduce another research
queue, database or production orchestrator. Raw per-slot evidence is retained in
[`experiments/phase3/`](experiments/phase3/).

## Implemented

- Version 2 benchmark receipts record protocol identity, timestamps, owned
  conversation/run IDs, pinned model/budget hashes, context observations and
  latest durable status/counters, including on timeout/error.
- Passive `GET /api/lab/policy` reports allowlisted configuration without doctor
  readiness checks, network probes or model generation.
- Offline-only `LAB_HOLD_RESOURCE_PAUSES=true` retains the existing manual pause
  independently of `CONTEXT_PROBE_ENABLED`, which remains false.
- `--case`, `--protocol-sha256`, `--stop-on-pause` and exclusive-create `--output`
  support prescribed per-case process starts and durable once-only receipts.
- Collection, nonempty text scoring, finished report delivery and successful
  outcomes are distinct. Missing coverage and unknown counters are null, not
  zero. Unfinished report state without artifact IDs is not delivered coverage.
- Receipts preserve bounded answer text/hash, assistant-message status and owned
  evidence URLs/hashes/artifact IDs. Report artifact hashes are captured through
  the separate artifact metadata evidence, when present.
- Application and model RSS are separately labeled. Receipt samples use an
  explicit five-second observation-age rule; external two-second traces provide
  the fuller resource record. Neither is an allocation peak.
- Comparison requires every one of the 12 prescribed cases in both variants,
  correct split labels, unique run IDs and common model/budget/protocol hashes
  across the experiment. Known runtime/policy drift and missing runtime
  measurements are rejected. Legitimately paused/failed measurements are
  retained without being relabeled as successful research.
- Development and held-out summaries include all-case denominators, output and
  report-delivery counts, and conditional proxy-score populations.

Infrastructure failures consume their scheduled slot. The separate accounting
ledger records unattempted or unconfirmed slots if the schedule stops. Such a
ledger is not coerced into a successful full comparison by inventing missing
identity fields or repeating attempts.

## Frozen execution conditions

| Setting | Value |
| --- | --- |
| App SHA256 | `9a1b1e8b48486d3e05294de57eef51efd35074b11f91eabd7c400f258881b9fb` |
| Model | Qwen3.5-4B-Q4_K_M.gguf, alias local-qwen |
| GGUF SHA256 | `00fe7986ff5f6b463e62455821146049db6f9313603938a70800d1fb69ef11a4` |
| llama binary SHA256 | `d4678f9f08b7de435c9f317a7023900f2ce7b3a4ee4aff49da4d59f5ab5a141f` |
| Runtime | llama build 11426 / cda0f9256, Vulkan1, RTX 3050 6GB Laptop |
| Context/output | 32768 / 4096; system/headroom reserve 2048; thinking off |
| Sampling | temperature 0.2, top_p 0.8; seed not pinned |
| Model launch | parallel 1, GPU layers 99, flash attention, q8_0 K/V, context shift off |
| Corpus SHA256 | `9814cb53c048545010e91bb6ee7e50be66e8c3852eb3380c8c435a86f03fd43b` |
| Dataset SHA256 | `dfdc60e7736bfbd2d68a6fc712884883961ae670493ab67c5542d261b3589086` |
| Model cgroup | 4 GiB memory, swap 0, restart=no |
| App cgroup | 1 GiB memory, swap 0, restart=no |
| Resource guard | model RSS 3 GiB; app RSS 2 GiB; minimum free RAM 512 MiB / VRAM 256 MiB; GPU maximum 85°C; check every 1s |
| Research | standard, section report enabled, offline only, notifications none |
| Timeouts | generation 120s; persisted session 1800s; collector 2100s |
| Start envelope | AC online, GPU ≤60°C, available RAM ≥2 GiB; maximum wait 300s |
| Pause behavior | first observed pause stops measurement; manual checkpoint retained, no resume/retry |

The standard v2 budget is three rounds, 20 total steps, 48 LLM reservations,
eight searches/fetches/evidence items, six report sections, 16 paragraphs/claims,
and 524288 report bytes. Common budget hash:
`272a77b55c286ad89f1006163d6401b0d49174d97c3439123ea1caca72c714cc`.
Baseline is the current bounded fixed/completion policy with up to one
continuation; adaptive permits up to two. It is not a historical single-plan-only
baseline. Other ceilings remain equal.

The app was built from dirty Phase 3 source on the stated base commit. The exact
measured executable is hashed; reports and analysis scripts added afterward do
not make the observations a different committed-binary run. The runtime metadata
fingerprint is not GGUF weight attestation; the actual GGUF was independently
hashed before measurement and is checked again by the analysis calculator.

Each slot starts a fresh model and application process, preserving the existing
database and manual-paused runs. This resets process/KV history between slots,
not the OS page cache. Normal within-case cache behavior and llama startup warmup
remain enabled. There is no doctor/chat/load-probe generation before submission.
Case order follows the fixed manifest, alternating baseline/adaptive ordering
between adjacent cases. No held-out tuning or replacement attempts are allowed.

## Results — Real-model verified

Execution ran from **15:03:17Z to 15:56:18Z** (20:03–20:56 at +05:00), including
process starts, cooling waits and restoration. All 24 scheduled slots were
measured; none were replaced or retried. Final accounting has no execution or
restoration errors. All captured model/app process PIDs remained unchanged
within their slot. No context mismatch occurred.

The [baseline JSONL](experiments/phase3/baseline.jsonl),
[adaptive JSONL](experiments/phase3/adaptive.jsonl),
[comparison](experiments/phase3/comparison.json),
[analysis summary](experiments/phase3/summary.json), and
[final accounting](experiments/phase3/accounting.final.json) retain identities,
denominators and raw-file references. Analysis rehashed the actual app, model,
llama binary and corpus and checked all 24 pinned snapshots. Common model-profile
hash: `1b3d9bd1717af420d357a0aaef2b79a9e80c4f65a5e5f8a3f07e2dbfbb65e4c3`.

| Measurement | Baseline, 12 tasks | Adaptive, 12 tasks |
| --- | ---: | ---: |
| Collected attempts | 12/12 | 12/12 |
| Resource-paused tasks | 11 | 12 |
| Failed/blocked tasks | 1 | 0 |
| Nonempty answers / finished reports | 0 / 0 | 0 / 0 |
| Successful run outcomes | 0/12 | 0/12 |
| Sum collector wall seconds | 1332.55 | 1476.53 |
| Mean collector wall seconds | 111.05 | 123.04 |
| LLM reservations | 117 | 121 |
| Search reservations | 26 | 25 |
| Fetch reservations / evidence items | 22 / 22 | 24 / 24 |
| Compactions | 45 | 51 |
| Retrieval-only macro source recall | 0.9167 | 0.9028 |
| Pooled expected URL recall | 22/25 = 0.88 | 22/25 = 0.88 |
| Answer lexical/citation/source proxies | null | null |
| Independent factual accuracy | null | null |

Counters count durable reservations, not independently established successful
actions. Lower wall time here is not a better result: baseline's early roadmap
failure lowers its mean. Wall measurements include collection but exclude
per-slot model startup/cooling. The sum is 2809.08 seconds across both variants;
the overall operator interval was about 53 minutes.

### All paired tasks

Every task delivered zero answer bytes. All outcomes are `paused/high_model_rss`
except baseline `dev-roadmap`, which failed/blocked. `B/A` denotes
baseline/adaptive. URL matches count exact expected URLs, not arbitrary fetched
sources or supported claims.

| Case | B/A wall seconds | B/A LLM reservations | B/A expected URL matches |
| --- | --- | --- | --- |
| dev-fasta | 128.06 / 136.05 | 11 / 10 | 2/2 / 2/2 |
| dev-rnaseq | 96.06 / 106.03 | 9 / 9 | 2/2 / 2/2 |
| dev-buffer | 118.05 / 140.07 | 10 / 12 | 2/2 / 2/2 |
| dev-catalyst | 142.06 / 130.05 | 11 / 10 | 2/2 / 2/2 |
| dev-sort | 108.02 / 88.02 | 10 / 10 | 2/2 / 2/2 |
| dev-wal | 100.02 / 116.02 | 10 / 9 | 2/2 / 2/2 |
| dev-roadmap | 20.03 / 128.02 | 3 / 9 | 0/3 / 1/3 |
| dev-recovery | 104.05 / 112.02 | 9 / 9 | 2/2 / 2/2 |
| heldout-units | 132.02 / 120.05 | 11 / 10 | 2/2 / 1/2 |
| heldout-citations | 134.06 / 150.07 | 12 / 12 | 2/2 / 2/2 |
| heldout-missing | 144.11 / 128.09 | 11 / 10 | 2/2 / 2/2 |
| heldout-protocol | 106.02 / 122.04 | 10 / 11 | 2/2 / 2/2 |

### Development and held-out populations

| Split / variant | Cases | RSS-paused / failed | Mean wall s | LLM reservations | Retrieval macro recall | Finished reports |
| --- | ---: | --- | ---: | ---: | ---: | --- |
| Development / baseline | 8 | 7 / 1 | 102.04 | 73 | 0.8750 | 0/8 |
| Development / adaptive | 8 | 8 / 0 | 119.53 | 78 | 0.9167 | 0/8 |
| Held-out / baseline | 4 | 4 / 0 | 129.05 | 44 | 1.0000 | 0/4 |
| Held-out / adaptive | 4 | 4 / 0 | 130.06 | 43 | 0.8750 | 0/4 |

These retrieval-only differences do not establish useful reports or generalize
beyond the fixed corpus. Quality scoring populations are empty in both splits.

### Resource and inference observations

- **1428 external samples**, two missed target intervals, zero collection
  exceptions/per-sample errors. The two-second period is a target, not a promise
  of exact spacing. AC was verified before launch and sampled during trials.
- Peak sampled model RSS **3498262528 bytes** (about 3.26 GiB); peak app RSS
  **98705408 bytes**. Minimum available RAM **2354069504 bytes**; minimum free
  GPU memory **2424 MiB**; peak sampled GPU temperature **73°C**.
- Model cgroup peak **4294971392 bytes**, approximately the 4 GiB configured cap.
  There were **7091 `memory.events max` events**, indicating cgroup pressure,
  not OOM events. Both model/app `oom`, `oom_kill`, `oom_group_kill` and
  `NRestarts` were zero in every captured process interval. This does not
  establish spare memory or stable continued research.
- All **238 unique inference summaries** were collected: 215 transport-completed
  and 23 failed invocations. These are different from the one failed task.
  A transport-completed response can still fail application-level validation.
- Known output tokens from 215 completed invocations total **68170**
  (baseline 32374; adaptive 35796). This is an internal-call subtotal, not
  delivered-answer tokens or total actual generation. Usage from 23 interrupted
  invocations is unavailable; total actual output remains null.
- Conditional, unweighted provider-reported means over those 215 measured calls:
  prompt rate **941.15 tokens/s**, generation **32.17 tokens/s**. Within-case
  caching is normal; these are not uncached full-context capacity measurements
  or statistical throughput superiority claims.
- Recovery probe attempts remained zero. All 23 measured resource pauses were
  held manually, not automatically resumed to obtain a successful outcome.

### Roadmap failure and source coverage

Baseline run `602e81b7-ff10-49e9-b636-f111f24c7f5e` has durable
`task.failed` event 49 with `invalid_model_output`. Its plan was accepted before
the failure. Two dispatched Russian searches returned zero results against the
English lexical corpus; the saved search-reservation count is three. Failure
occurred before fetch/evidence/report creation. The exact action-validation
subcause is not captured; duplicate-query rejection must not be asserted as the
proven cause. This is not a demonstrated PDF failure.

Adaptive run `176d9d0f-f6b8-471c-9ae4-4b74442fe113` collected two source items
but matched only one of the three exact expected URLs, then paused on model RSS.
The held-out units adaptive case also matched only one of two expected URLs.
No answer was delivered in either case. Internal verifier PASS or an unfinished
report-state outcome does not substitute for persisted artifacts and a delivered
answer. The baseline terminal task error is in diagnostics/events; its collector
error is empty because collection itself succeeded.

## Live acceptance verified — narrow restoration checks

The operator stopped experimental units and restored the ordinary model/app
services. Their existing persistent unit files and memory properties were not
rewritten. The final accounting records no restoration errors. API health after
restoration reported database ok, fresh attached idle worker and no active run.
The measured manual pauses and their evidence/checkpoints remain retained.

The ordinary app was then rebuilt/restarted from the measured Phase 3 source;
its executable SHA256 still matched `9a1b1e8b…81b9fb`. Passive policy reported
`hold_resource_pauses=false` and the original external-search configuration.
Doctor observed matched 32768/32768 context and an attached worker, while its
existing PDF/sandbox/search readiness warnings remained visible.

This verifies guarded pause preservation, slot drain and service restoration;
it is not completed research acceptance or a staged soak.

## Interpretation boundaries

This is **real local inference on synthetic imported material**, not a real-world
scientific research benchmark. The corpus has ten short documents, reuses source
pairs across cases, and the held-out tasks are visible in the repository. A
document titled "independent reference" is not an independently verified
authority. The lexical proxy uses substring matching, including a weak
one-character `R` concept. Citation ownership does not prove claim entailment,
and word overlap does not establish useful synthesis or correct abstention.

Expected-source retrieval recall can be calculated from task-owned evidence
independently of answer delivery; it must not be presented as an answer-quality
score. Provider rates and known completed-call token subtotals exclude missing
or interrupted work. Total actual output tokens remain null unless reconciled.
Independent factual accuracy is **null** throughout.

A first-resource-pause experiment measures progress and cost up to interruption;
it does not measure eventual recovery-assisted completion. Conditional means over
different nonempty-output populations cannot establish overall adaptive benefit.
Resource pauses, empty reports and unknown scores are retained rather than
filtered out. Model RSS growth is an observed limitation, not a proven leak
diagnosis.

## Scripted-test verified

- Full `CGO_ENABLED=1 go test -tags sqlite_fts5 ./... -timeout=20m` — PASS.
- `go vet -tags sqlite_fts5 ./...` — PASS.
- Targeted race tests for CLI, API and config — PASS.
- Tests cover full-case-set comparison, mixed-snapshot/drift rejection,
  missing/null runtime observations, unknown-versus-zero serialization,
  paused/timeout collection, nullable report coverage, passive policy behavior,
  offline-only hold configuration and escaped 512 KiB answer round-tripping.
- Operator and analysis scripts pass `node --check`.

These checks establish implementation behavior. Real-model and live verification
are the separately recorded experiment, not consequences of scripted tests.

## Phase boundary

This phase does not establish production readiness, overnight operation,
independent factual accuracy or general 128K support. Useful real-world research
belongs to Phase 4; full UI work and staged 15m → 60m → 4h acceptance remain later
gates. Stop after the Phase 3 report and commit; await **`Proceed to Phase 4`**.
