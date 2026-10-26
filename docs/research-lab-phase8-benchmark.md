# Laboratory Phase 8 — fixed offline benchmark

The checked-in, explicitly **synthetic** corpus and manifest cover 12 tasks:
8 development and 4 held-out. Topics include bioinformatics, chemistry, computing,
cross-domain roadmaps, recovery, provenance and missing evidence. Held-out cases
are fixed in the manifest, not selected after seeing results. No self-training,
online learning or CandidatePatch promotion is involved.

Prepare the exact corpus from `internal/cli/testdata/lab-corpus.json` or obtain its
identical bytes with `qagent lab benchmark --print-corpus`. Configure the **running
server** with `RESEARCH_PRIVACY_MODE=offline`, that corpus path, and
`RESEARCH_SECTION_REPORT_ENABLED=true`. Use the same model and budgets in both
experiments; baseline has `RESEARCH_ADAPTIVE_ENABLED=false`, adaptive has `true`.
Restart/redeploy between those configurations and wait for each experiment to
finish. Commands emit JSONL to stdout for normal operator capture:

```sh
qagent lab benchmark --variant baseline --split all
qagent lab benchmark --variant adaptive --split all
qagent lab compare BASELINE.jsonl ADAPTIVE.jsonl
```

`--split development` and `--split heldout` permit separate evaluation; the default
all-split experiment has all 12 cases. Each case is submitted through the existing
conversation API and sequential worker. No alternate queue/model execution path.
The command checks the server's mode/corpus/variant/report policy before creating
work and validates each saved session again. A finite per-case timeout (default
20m, 1m..1h) cancels only its own unfinished run. Partial/failed outcomes remain
recorded; infrastructure/policy failures stop the experiment rather than silently
submitting more work. API redirects are disabled and response sizes bounded.

Metrics: saved status/outcome, wall time, durable LLM/search/fetch/evidence counters,
sampled process RSS peak (nullable), output bytes, report sections/unrepresented
criteria, expected-source recall, lexical concept hit rate, and citation-marker
ownership validity (nullable without markers). Dataset/corpus/model-profile/budget
hashes are attached to each row; compare rejects mismatched snapshots/case sets.
Only terminal rows with `scoring_complete=true`, nonempty snapshot hashes and no
collection error can be compared. Missing assistant/evidence data produces an
incomplete row, not a zero-quality result. Ambiguous submission failures clean up
the benchmark-owned conversation so committed work cannot escape cancellation;
cleanup failure is surfaced explicitly.
Split labels remain in comparisons. These are transparent proxies: lexical hits
are not semantic goal coverage; retrieved sources are not independent truth;
owned citation IDs do not prove a claim. Total output tokens and independently
judged factual accuracy remain **null**, not fabricated zero or same-model scores.
Human review is required to judge factual support, coherent synthesis, limitations
and substantive improvement on held-out tasks.

Verified: 12-case/four-held-out manifest, all gold source identities in the actual
loaded corpus, byte-exact corpus export, owned/foreign citation scoring, nullable
unknown metrics, model mismatch rejection and cancellation on session policy
mismatch; CLI/config/API/SQLite tests passed. The command was invoked against the
current older running service and rejected its missing offline/corpus precondition
before task creation. **No live baseline/adaptive 12-task result or quality gain is
claimed.** Scripted worker integration from Phase 6 tests the pipeline separately.

Files: `internal/cli/lab_benchmark.go`, tests and `testdata/lab-{cases,corpus}.json`;
safe doctor policy fields and runtime immutable model-profile hash/report flag.
