# Resource guard API (canonical Phase 1)

`DefaultPolicy()` enables the laptop guard with 512 MiB minimum free RAM,
256 MiB minimum free GPU VRAM, 85°C maximum GPU temperature, 2 GiB maximum
application RSS, a 2 s llama health timeout, a 5 s check interval, five resume
attempts and 5–60 s exponential backoff. `Policy.Validate()` rejects invalid
thresholds, nonfinite temperatures and invalid durations. `policy.Backoff(n)`
is one-based and saturates without duration overflow; attempts ≤1 use the base.
The worker must enforce `MaxResumeAttempts` and its durable budgets separately.

Construct `NewHostProvider(llamaBaseURL, policy)` and
`NewMonitor(provider, policy)`; both return a value and an error. `Provider`
has `Snapshot(ctx) (Snapshot, error)`. `Monitor` has
`Check(ctx) (Snapshot, *Violation, error)` and `LastSnapshot() Snapshot`.
`ResourceMonitor` satisfies `Monitor`. Every check probes afresh; the worker
owns the periodic loop, invocation gates, cancellation, lease and persistence.
`LastSnapshot` returns a synchronized deep copy; its zero value means no check.

All numeric telemetry and health fields are nullable JSON pointers. RAM uses
Linux `MemAvailable`, RSS uses `/proc/self/status`, and heap is Go `HeapAlloc`.
`SnapshotAt` is the UTC collection start. GPU collection uses a fixed
`nvidia-smi` command, capped output and a 2 s timeout. Telemetry executables
are resolved only under `/usr/bin` and `/bin`, not the caller's PATH. Multiple GPUs are
conservatively represented by minimum free memory and maximum temperature;
an unavailable reading on any device leaves that metric unknown. No OOM or
restart history is inferred. UPower `OnBattery` uses a fixed `busctl` call
and a 30 s cache, including unavailable results. Power is `unknown`, `AC`, or
`battery` and does not override resource thresholds.

Only numeric loopback addresses and `localhost` are accepted. `localhost` is
normalized to `127.0.0.1`, without DNS. Empty, root and `/v1[/]` base paths
normalize to `GET /health`. Credentials, queries, fragments, other paths and
remote hosts are rejected. There is no proxy or redirect following. Health
requires HTTP 200 and bounded JSON `{"status":"ok"}`. Failed health sets
`LlamaHealthy=false` plus a safe reason and causes `llama_unhealthy`.
Parent cancellation returns a context error, including during telemetry;
it is never successful unknown telemetry. Optional telemetry timeouts are
unknown, and do not claim healthy hardware.

`Check(snapshot, policy)` is deterministic and has no I/O. Violation codes:
`low_ram`, `high_rss`, `low_vram`, `gpu_temperature`, `llama_unhealthy`.
Available memory must be at least its minimum; RSS/temperature may equal the
maximum. Zero disables an individual numeric threshold. Disabled policy
suppresses violations, while an explicitly called provider still collects.
Unavailable threshold telemetry is **not proof that the threshold passed**:
provider and monitor report explicit warnings and keep the metric null, but
do not pause solely because optional telemetry is missing. The worker can
surface these warnings. Provider errors must not be treated as clearance.

`ClassifyModelHTTPError(status, boundedBody)` accepts at most 4096 bytes of
structured error JSON for HTTP 500/503/507 and recognizes OOM codes/types or
an explicit out-of-memory message. It returns an error matching both
`ErrModelOOM` and `ErrModelResource`, without retaining/logging the body.
Unclassified responses return nil; adapter/worker retain their ordinary
error handling. This package imports no model adapter or orchestrator.

The worker must gate every costly invocation, atomically persist resource
pause/checkpoint/counters, release its lease, and schedule finite durable
resume attempts using this policy. These packages do not perform inference,
persist state, change lifecycle status or run background monitoring loops.
