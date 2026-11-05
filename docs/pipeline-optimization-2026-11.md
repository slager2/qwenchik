# LLM pipeline optimization — 2026-11

Branch `track/pipeline`. Model: Qwen3.5-4B Q4_K_M on llama.cpp `b10298-2.0.0`
(build 11426, commit `cda0f9256`), Vulkan RTX 3050 6 GB, `--parallel 1`, 32k context.
Raw evidence: [`experiments/pipeline-2026-11/`](experiments/pipeline-2026-11/)
(receipts, per-call timings, the logging proxy and replay scripts).

## Method

- A separate app instance (own data dir, `127.0.0.1:8090`) ran against the live
  model server through a logging proxy (`proxy.py`) that records every model
  HTTP call with llama.cpp `timings` (`prompt_n`, `cache_n`, `predicted_n`).
- Offline benchmark (`qagent lab benchmark --variant adaptive`), the same 3 cases
  before/after: `dev-fasta`, `dev-rnaseq`, `heldout-citations`
  ([protocol](experiments/pipeline-2026-11/protocol.json)). Synthetic corpus,
  so search/fetch variance is zero; only the binary changed.
- "Before" = `main` + the resume fix below (without it, every run that hit a
  thermal pause failed, see Findings). "After" = branch head.
- Controlled A/B on identical inputs: recorded requests replayed against the
  model with one change applied (`replay_alias.py`), and prompt-cache behaviour
  measured on a second llama-server instance (iGPU `Vulkan0`, port 8002, the
  generated-unit flags `--cache-ram 512 --ctx-checkpoints 4`) with `cachetest.py`.
- Caveat: the laptop is thermally limited. CPU Tctl reached 97–99 °C while
  other test suites ran; the resource guard (95 °C) paused runs. Wall time is
  noisy; token counts are the robust metric. Sample size is small (n=3 per side,
  temperature 0.2), so quality comparisons are indicative only.

## Results

Offline benchmark, unpaused runs:

| case | before: status / wall / LLM calls | after: status / wall / LLM calls |
|---|---|---|
| dev-fasta | failed (planner correction) / 44 s / 2 | completed partial / 62 s / 5 |
| dev-rnaseq | completed partial / 80 s / 5 | completed partial / 68 s / 5 |
| heldout-citations | completed **completed** / 126 s / 11 | completed partial / 88 s / 5 |

Proxy scores (expected-source recall, lexical concept hit, citation validity)
were 1/1/1 for every completed run on both sides.

Live web research task (SearXNG/Tavily, the same FASTA/FASTQ/Phred question, one run each,
two fetched sources and 32 search hits on both sides):

| | wall | LLM calls | prompt tok | output tok | LLM s | verifier out tok / s | outcome |
|---|---|---|---|---|---|---|---|
| before | 120 s | 8 | 25 623 | 2 725 | 105 | 1 047 / 41.4 | completed, partial |
| after | 110 s | 7 | 22 403 | 2 466 | 94 | 696 / 30.5 | completed, partial |

Per-role generated tokens (the dominant cost: ~30 tok/s generation vs
~725 tok/s prompt processing):

| verifier call | prompt tokens | output tokens | seconds |
|---|---|---|---|
| before (6 unpaused calls, two runs) | 5 793–6 555 | 926–1 455 (mean ≈1 090) | 33–50 |
| after (3 calls) | 5 407–5 715 | 590–915 (mean ≈720) | 23–33 |
| same 3 inputs replayed, alias only | −9 % (6036→5466, 5990→5459, 5793→5233) | −34…−38 % (1091→674, 934→603, 996→697) | 37→25, 33→23, 36→26 |

Prompt cache (second instance with `--ctx-checkpoints 4 --cache-ram 512`; the
live unit has both at 0, so `cache_n` was 0 for all 33 benchmark calls):

| request after the previous one | prompt_n | cache_n |
|---|---|---|
| identical planner request (retry/resume) | 4 | 788 of 792 |
| repair as continuation (append assistant + user) | 29 | 788 |
| planner, same system prompt, different user | 504 | 276 |
| verifier after planner (role switch) | 6 036 | 0 |
| planner again after the verifier | 792 | 0 |
| second verifier, same system prompt | 5 990 | 0 |

Round trips: each completion still does `/apply-template` + `/tokenize`
(1–5 ms and 1–11 ms measured), i.e. < 0.1 % of call time.

## What changed (commits)

1. `build(runtime)`: `runtimeModelPolicyArgs` is the single source for llama-server
   tuning flags. compose.yaml now passes `--cache-ram 512 --ctx-checkpoints 4
   --reasoning off --no-webui` like the systemd unit; a test keeps them in sync.
2. `fix(research)`: resumed runs failed with `failed_recovery_required`. Since
   2e68ea9 only the first request of a session client carries the runtime
   fingerprint, and it went into the invocation hash. After a resource pause the
   first resumed call (e.g. the verifier) had a different hash from its recorded
   attempt. The hash now uses the pinned session identity (`RuntimeIdentity`).
3. `feat(llm)`: optional `top_k`, `min_p`, `presence_penalty`. Nil fields are
   omitted from the body and from the invocation hash, so old runs resume as before.
4. `feat(orchestrator)`: Qwen3.5 non-thinking samplers. JSON roles: `top_k=20,
   min_p=0`, no presence penalty (it would penalize repeated JSON keys and quotes).
   Chat: plus `presence_penalty=1.5`. Temperature and top_p stay pinned by the
   session snapshot (LLM_TEMPERATURE/LLM_TOP_P); existing tests encode that contract.
5. `perf(verifier)`: evidence UUIDs are shown to the model as `E1..En` (input,
   criterion catalog, schema enum) and mapped back before validation. Pinned
   pre-change inputs keep raw IDs and the old schema.
6. `perf(orchestrator)`: format repair is a continuation
   (`system, user, assistant(bad), user(error)`). It no longer re-embeds the
   original request under a different system prompt. The Qwen template renders
   the prefix identically up to `<|im_start|>assistant\n`, which is where llama.cpp
   puts its prompt-end checkpoint. Measured: 788/792 cached.
7. `perf(citations)`: if the finalizer already used its correction call, the
   critic gets 1 attempt instead of 2. Finalization is bounded at 3 calls instead of 4.

## Findings and what was skipped

- **The installed `qwenchik-model.service` is stale.** It runs
  `--cache-ram 0 --ctx-checkpoints 0 --no-cache-idle-slots`. The current
  generator emits 512 / 4. With 0 checkpoints, a hybrid DeltaNet model cannot
  roll back its recurrent state, so every retry or resume reprocesses the whole
  prompt (`cache_n` was 0 for all calls). Re-running `qagent runtime install`
  fixes this; I did not touch the unit.
- **Stable-prefix refactor (step 3): skipped, measured first.** This llama.cpp
  build creates checkpoints only at prompt end −4 and −(4+n_ubatch)
  (`checkpoint_offsets = {4 + n_ubatch, 4}`), not at user-message starts. Upstream
  master adds user-start checkpoints. So two different prompts share cache only
  when they diverge in the last ~516 tokens (276 cached in the test above). Also,
  one prompt-cache entry for this model is 214–302 MiB, so `--cache-ram 512`
  holds a single entry and every role switch evicts it. Moving variable text out
  of system prompts would save roughly 360–600 prompt tokens (~0.5–0.8 s) per
  call, and only after a llama.cpp upgrade. Not worth the prompt churn now.
  Source: `tools/server/server-context.cpp` (ggml-org/llama.cpp master).
- **`--cache-reuse`: not added.** It needs KV shifting, and llama.cpp disables it
  when `llama_memory_can_shift` is false ("cache_reuse is not supported by this
  context"), which is the case for recurrent/hybrid memory. `-b`/`-ub` were left
  at the defaults: there was no VRAM for a second GPU instance to benchmark them
  (2.4 GB free, the model is 2.7 GB).
- **Compact JSON: rejected.** An instruction to minify output removed newlines
  but did not reduce output tokens (verifier 1056→1011, planner 482→486 and once
  1016). Indentation tokens are cheap; the cost was the copied UUIDs.
- **Prompt compression (step 4): not done.** The verifier and planner system
  prompts are 599 and 363 tokens, about 10 % of the verifier prompt and 0 % of
  generation. The risk to a 4B model's rule-following outweighs ~0.4 s per call.
- **Skipping `/apply-template` + `/tokenize` (step 5): not done.** It would save
  ≤ 15 ms per call and drop an exact admission check. The schema bytes counted in
  `requestInputBytes` only matter when the 100 KB input budget binds, which did
  not happen in these runs (inputs 25–35 KB).
- **Temperature 0 for JSON roles: not forced.** Research sessions pin temperature
  in the snapshot, Qwen discourages greedy decoding, and the default is already 0.2.
- `/props` + `/v1/models` are polled about 1×/s by API context inspection, because a
  pinned fingerprint forces a refresh. These are not generation calls, but each
  `/props` returns the full chat template. Left as is.

## Risks

- The alias change shifts verifier judgments. On the replayed sample the aliased
  verifier marked deliverable criteria unmet with content-based rationales
  ("sources do not explain…"). The baseline marked them met because "two sources
  were fetched", which the prompt forbids. `heldout-citations` went from
  `completed` to `partial`. This looks more faithful on a 769-byte synthetic
  corpus, but it can mean more `partial` outcomes and continuation rounds.
  Watch it on real web runs.
- New prompts and inputs change invocation hashes. As with earlier prompt changes,
  a run paused mid-call under the old binary may need recovery after upgrade.
  `PromptVersion` was not bumped: prompts are not version-gated in code, and
  `validateResearchModel` accepts only `local-research-v1`.
- Chat now sends `presence_penalty=1.5`. This follows the model card for
  non-thinking general tasks, but long chat answers were not measured.

## Sources

- Qwen3.5-4B model card, recommended sampling: https://huggingface.co/Qwen/Qwen3.5-4B
- llama.cpp server README (`--cache-reuse`, `--ctx-checkpoints`, `--checkpoint-min-step`,
  `--cache-ram`, `top_k`, `min_p`, `presence_penalty`):
  https://github.com/ggml-org/llama.cpp/blob/master/tools/server/README.md
- llama.cpp checkpoint placement/restore and cache-reuse gating:
  https://github.com/ggml-org/llama.cpp/blob/master/tools/server/server-context.cpp
- Local `llama-server --help` of the unit's binary (all flags above verified present).
