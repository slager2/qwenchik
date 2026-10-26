# Laboratory Phase 7 — overnight prerequisites and staged observation

Existing worker leases, journal reservations, resource guard/backoff, persisted
deadlines, sleep inhibitor and notification retry mechanisms remain the runtime.
The new CLI observes that same API/worker; it creates neither an alternate worker
nor a second queue. Run IDs must refer to an already-started research workload.

```sh
qagent --json lab readiness --run-id RUN_ID
qagent lab soak --stage 15m --run-id RUN_ID
qagent lab soak --stage 60m --run-id RUN_ID
qagent lab soak --stage 4h --run-id RUN_ID
```

Run stages in order and keep each JSONL transcript; a later receipt does not erase
an earlier failed stage. The CLI does not fabricate predecessor receipts or certify
stages it did not observe. Readiness checks application/worker/storage/model/search
and an active runtime snapshot. PDF capability is still separately reported by
doctor; live PDF fixture smoke remains a separate prerequisite for PDF workloads.

Soak uses real elapsed time, samples every 15 seconds, rejects gaps over 45 seconds,
and measures the required interval between successful samples, excluding initial
request latency. A stale final sample cannot certify the interval. It rejects
unknown/stale RAM/RSS/VRAM/GPU temperature, unconfirmed AC, inactive sleep inhibitor,
exceeded resource limits, unknown/terminal state, approval stalls, changed/unknown
bundle, regressed event cursor, missing progress and observer/API interruption.
Only a persisted research session matching the worker's active run is eligible.
Thresholds come from the running worker and are pinned throughout observation;
loosening the CLI environment cannot relax them. Administrative/probe events are
not research progress: counters/completed work must advance, with a ten-minute
no-work ceiling. Current search probe failure takes precedence over old successes.
There is no shortened-duration CLI mode. A terminal run before the interval ends
produces a failed receipt. Stopping this observer does not cancel a user-owned run.
An accepted receipt is an observation, not a guarantee against later failures.
`lid_behavior_proven` stays false; lid/logout/firmware behavior needs an explicit
machine-specific test. Notifications and crash recovery have their existing tests
but are not automatically certified by a soak receipt.

Fresh tests: CLI tracker acceptance/rejection (virtual timestamps, explicitly not
elapsed live soak), bounded HTTP/redirect/run-ownership handling, CLI/API packages.
Live `go run -tags sqlite_fts5 ./cmd/qagent --json lab readiness` was executed against
the running older service: app/worker/DB/artifacts/model/fetch-temp OK; search not
ready, no active run selected, isolated PDF unavailable, no active inhibitor or
fresh resource snapshot. `prerequisites_ready=false` is expected and truthful.

**Live acceptance status: 15m NOT RUN; 60m NOT RUN; 4h NOT RUN.** Current changes
have not been deployed. The cumulative 12-step execution ceiling can terminate a
useful research run long before a multi-hour wall-time allowance; profiles do not
promise a four-hour workload or ten realized rounds.

Files: `internal/cli/lab.go`, `lab_test.go`, root command registration and safe
doctor forwarding; offline doctor search projection in `internal/api/server.go`.
