# Deep Research Phase 2 — context admission and measured ladder

Measurements: 2026-10-27. Final regression/restoration check: 2026-10-28.
Base commit: `8ee986a29a63b90eb52de3c796759414add666ce`.

## Verdict

**Context diagnostics and admission are implemented. No rung established stable
multi-call research acceptance.** The 32K and 64K synthetic load probes passed;
their subsequent offline research runs paused on `high_model_rss`. The 96K and
128K probes timed out at the unchanged 120-second generation limit. All four
primary receipts have `accepted=false`.

Use **32768 context / 4096 output / thinking off / one inference slot** as the
conservative measured starting point. This is a successful short-chat and
synthetic-load configuration, not an established overnight research profile.
Training context 262144 does not establish operational capacity on this laptop.

## Implemented

- `ContextBudget` distinguishes configured context, current server capacity,
  training context and saved-session context. Output reservation is separate
  from the 2048-token system/headroom reservation. Unknown observations are null.
- Production discovery requires `/props`, one slot, matching `/v1/models`
  identity and consistent context metadata. Missing discovery blocks inference;
  configuration is not substituted for observation and mismatches are not capped
  silently. The source default changed from 131072 to 32768.
- Every generation attempt renders the complete request through `/apply-template`
  and counts `/tokenize` tokens, including special tokens, before dispatch.
  Admission checks `rendered_prompt <= context - output - 2048`. The rendered
  prompt already includes system messages; the extra reservation is deliberate
  headroom. Schema-format fallback is independently recounted.
- Session snapshots pin observed runtime identity, context and thinking mode.
  Recovery revalidates those settings; it does not upgrade saved context. A
  changed template/build/model metadata fingerprint blocks dispatch. The
  fingerprint binds metadata, not GGUF file contents: the ladder independently
  hashes the actual local model file.
- Predispatch context failures are distinguished in the invocation journal and
  may be retried within the existing two-attempt bound. Already-dispatched or
  unknown streams retain recovery-required behavior. Committed idempotent turn
  replays do not depend on a fresh model metadata request.
- Byte compaction and persistence bounds remain resource limits, not token
  measurements. Chat history's extra division by four was corrected. Telemetry's
  request context hint is classified as estimated; server observations live in
  the diagnostic object.
- `MODEL_RSS_MAX_BYTES=3221225472` independently guards model-process RSS,
  separate from Go application RSS. Unknown model RSS blocks admission;
  recovery uses hysteresis. Sampling is not a hard allocation limit and can
  observe an overshoot before the next check.
- Doctor, run runtime and the composer/Inspector expose context mismatch.
  `CONTEXT_PROBE_ENABLED=false` disables the load endpoint during normal serving.
  The opt-in endpoint accepts only a fixed synthetic fixture and allowed rung,
  uses the existing shared guard and checks resources before/during/after load.
- `lab context-check` samples resources, fingerprints the GGUF, runs the fixed
  offline research case through the existing queue, and saves bounded receipts
  plus paginated inference summaries. Incomplete measurements return nonzero.
  Receipt files are exclusive-create. Diagnostic pauses reuse durable manual
  pauses and preserve checkpoints without automatic probing/resume.

The existing modular monolith, SQLite/WAL, queue, orchestrator, evidence ledger
and single inference slot remain the execution path. No database migration was
needed for the optional snapshot fields.

## Measurement protocol and provenance

Raw receipts and launch observations are in
[`experiments/context-ladder/`](experiments/context-ladder/), particularly
[`deployment-manifest.json`](experiments/context-ladder/deployment-manifest.json).

| Item | Recorded value |
| --- | --- |
| Model | Qwen3.5-4B-Q4_K_M.gguf |
| GGUF SHA256 | `00fe7986ff5f6b463e62455821146049db6f9313603938a70800d1fb69ef11a4` |
| llama-server | `b10298-2.0.0`, build 11426, commit `cda0f9256`; `/props`: `b11426-cda0f9256` |
| Hardware | RTX 3050 6GB Laptop GPU, 6144 MiB; approximately 15 GiB host RAM |
| Backend | Vulkan1, driver 595.91.07, GPU layers 99, flash attention, K/V q8_0 |
| Execution | parallel 1, context shift off, thinking off, no cloud fallback |
| Model cgroup | MemoryMax 4294967296 bytes, swap 0, restart=no for primary trials |
| App cgroup | MemoryMax 1073741824 bytes; private API `127.0.0.1:18080` |
| Guard/sampling | model RSS 3 GiB; guard check 1s; external samples every 2s |
| Output reservation | 4096 tokens at every rung |
| Synthetic probe | temperature 0, top_p 1, cache_prompt=false |
| Research | standard profile, temperature 0.2, top_p 0.8, adaptive=false, section report=true |
| Corpus | offline only; SHA256 `9814cb53c048545010e91bb6ee7e50be66e8c3852eb3380c8c435a86f03fd43b` |

Each rung used a fresh controlled model process and the same fixed synthetic
fixture, with a source-ID/quote anchor at the beginning, middle and end. A
tokenizer-based binary search filled almost all available input allowance.
Retention requires all three exact ID/quote pairs and strict JSON. Provider
prompt-token counts independently matched admission counts at the passing rungs;
reported cached tokens were zero. This is a synthetic retention test, not a test
of independent factual accuracy. The research case was `dev-recovery`, using the
same existing database/queue, and was attempted only after a usable load probe.

The measured executable was an **intermediate dirty Phase 2 build**, SHA256
`051b7de34690386b22d698e958cfc96d0b16c62a187358bbb12ea1882e57b1ec`, built on
the base commit above. Later admission, collector, retry and diagnostic validation
fixes were regression-tested; the full ladder was not repeated on the final
source. In particular final code emits null provider counts when unavailable,
labels the request context hint estimated, and requires observed zero cache reuse
for probe PASS. Original receipts are preserved, not retroactively rewritten.

Seeds were not pinned, there was one primary trial per rung, and probes/research
have different documented sampling parameters. This is a capacity diagnostic,
not a statistical quality comparison or the Phase 3 baseline/adaptive benchmark.
Receipt timestamps use UTC `Z`; some embedded telemetry uses `+05:00`.

## Primary ladder results — Real-model verified

| Context | Target input | Prepared tokens | Provider prompt/output | Prefill tok/s | Generation tok/s | Probe wall s | Retention | Research |
| --- | ---: | ---: | --- | ---: | ---: | ---: | --- | --- |
| 32768 | 26624 | 26597 | 26597 / 99 | 931.37 | 28.21 | 32.732 | 3/3, PASS | paused: high_model_rss |
| 65536 | 59392 | 59367 | 59367 / 99 | 736.31 | 22.51 | 87.117 | 3/3, PASS | paused: high_model_rss |
| 98304 | 92160 | 92137 | unavailable | unavailable | unavailable | 121.293 | not established, timeout | not run |
| 131072 | 124928 | 124865 | unavailable | unavailable | unavailable | 122.399 | not established, timeout | not run |

32K provider timings: prefill 28556.873 ms, generation 3509.719 ms.
64K provider timings: prefill 80627.913 ms, generation 4397.245 ms.
Wall time includes preparation/admission and request overhead. Timeouts occurred
at the configured 120-second generation request limit. Prepared tokenizer counts
for 96K/128K do not prove those tokens were evaluated. Their original top-level
provider counters serialize zero, while the inference telemetry is unavailable;
interpret them as **unknown, not zero work**.

Resource extrema below cover each complete collection interval (probe plus any
research), rather than only the probe. Values are sampled, not allocation peaks.

| Context | Samples | Min free RAM bytes | Peak model RSS bytes | Min GPU free MiB | Peak GPU °C | Model cgroup peak bytes |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 32768 | 59 | 3241054208 | 3445248000 | 2434 | 80 | 4294967296 |
| 65536 | 68 | 2828005376 | 3312402432 | 1857 | 81 | 4294967296 |
| 98304 | 62 | 4997365760 | 950448128 | 1286 | 81 | 2813911040 |
| 131072 | 63 | 5080039424 | 891899904 | 710 | 79 | 2777833472 |

Cgroup accounting includes more than process RSS. No model restart or OOM was
observed in the four primary trials. Reaching the 4 GiB cgroup peak is not a
memory headroom claim. Higher-rung RSS samples are from timed-out probes without
subsequent multi-call research and are not evidence of lower research memory use.

### Research details

| Context | Run | Wall s | LLM reservations | Search/fetch/evidence | Compactions | Output/sections |
| --- | --- | ---: | ---: | --- | ---: | --- |
| 32K | `a895d261-5cc1-48db-a684-492322fb7894` | 82.021 | 7 | 2 / 2 / 2 | 4 | 0 bytes / 0 |
| 64K | `55cb6b97-e599-4d06-bbf1-8cddee9f0b69` | 44.032 | 6 | 2 / 2 / 2 | 4 | 0 bytes / 0 |

These are durable paused runs with evidence/checkpoints, not completed reports.
The LLM counters count reservations, not independently proven successful calls.
Expected-source recall was 1 in this tiny case, but no report was delivered;
this metric cannot establish completion or factual quality. Independent factual
accuracy remains **null**. The reason for model RSS growth across calls is not
established; no memory-leak diagnosis is claimed.

### Preserved negative pretrials

- `32k-memory3g.json`: synthetic load passed and research eventually returned a
  partial six-section result, but model OOM at `2026-10-27T14:54:47+05:00`, one
  automatic restart and pause/resume events invalidate a clean PASS.
- `32k-memory4g-before-rss-guard.json`: load passed, followed by model OOM at
  `2026-10-27T15:03:57+05:00`, no automatic restart, and failed research/report.

These have different resource/guard conditions and are excluded from the primary
comparison. They motivated the separate model RSS guard rather than a relaxed
limit to obtain a nominal success.

## Live acceptance verified — bounded checks

[`live-context-mismatch.json`](experiments/context-ladder/live-context-mismatch.json)
records app context 131072 against server/saved session 32768:

- New turn returned HTTP 409 `context_mismatch`; zero runs created.
- Explicit resume of the saved 32K run was accepted for worker validation, then
  paused with `context_mismatch` before generation. LLM/search/fetch/evidence
  remained 7/2/2/2; deadline and saved context snapshot hash were unchanged.
- This establishes live admission/recovery blocking, not completed recovery.

Ordinary model/app services were restored on context 32768 with diagnostic probe
disabled and the original model service memory properties restored. API health
reported database ok, fresh attached worker and no active run. Doctor observed
configured/server context 32768/32768 with output reserve 4096 and input allowance
26624. Existing PDF/sandbox/search readiness warnings remain visible.

Real local chat smoke in conversation `b88fc936-dc59-45d9-8117-50e61d9be300`:

- Send `784fdb9f-6533-438b-81ed-8d3c2db922de`: completed, about 2.31 seconds,
  nonempty Russian answer defining a checkpoint.
- Retry `50bdce40-9bb6-4f39-a353-697c6147e322`: completed, about 2.09 seconds.
- Continue `631b7306-0d06-4469-bb97-e8678aa2b6b3`: completed, about 2.06 seconds.

The services and completed continuation were checked again on 2026-10-28.
This short smoke does not establish research quality or sustained operation.

## Scripted-test verified

- `CGO_ENABLED=1 go test -tags sqlite_fts5 ./... -timeout=20m` — PASS
  (orchestrator 352.870s on the final full-suite run).
- `go vet -tags sqlite_fts5 ./...` — PASS.
- Targeted race checks: API, OpenAI client, CLI, resources, application and SQLite
  — PASS, including the final uncached-prefill validation change.
- Frontend: 78 tests PASS, `npx tsc --noEmit --pretty false` PASS,
  `npm run build` PASS, `npm run test:browser` PASS with mock-backed Chrome
  interactions, mobile widths, SSE/replay and existing navigation.
- Admission tests cover exact boundaries, missing tokenizer, metadata/template
  drift, thinking snapshots and fallback recount. Other tests cover safe
  predispatch retry, stream restrictions, committed idempotent replay, model RSS
  hysteresis/unknown telemetry, diagnostic holds, incomplete receipt rejection,
  shared-slot probe exclusion and cached/unknown-prefill rejection.

Browser smoke is not live-model UI acceptance. Generated embedded frontend assets
were rebuilt. No full redesign was performed in this phase.

## Not established and phase gate

Stable multi-call research, general 96K/128K operation, independent factual
accuracy, a 12-task baseline/adaptive comparison, useful real-world research,
and 15m → 60m → 4h acceptance remain unestablished. The timeout and model-RSS
findings are explicit inputs to later work, not overridden to create a PASS.

Phase 2 ends with its code, receipts and report. Further benchmark work requires
the user's **`Proceed to Phase 3`** gate.
