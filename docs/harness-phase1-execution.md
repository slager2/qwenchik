# Canonical harness Phase 1: execution and resource recovery

This phase extends the existing Go/SQLite worker. No orchestration framework, cloud generator, second model slot or new SQL table is added. It is distinct from the earlier research-profile Phase 1.

## Contracts and patches

| Previous contract/problem | Invariant and implemented patch | Failure behavior and tests |
|---|---|---|
| Process `flock` without durable ownership | Production enables task owner/epoch/expiry leases; worker contexts carry fencing identity and renew while active | Expired/stale actors cannot write execution state; cancellation releases ownership; SQLite lease tests and a six-second stream under a five-second renewed lease |
| Checkpoint rows without integrity metadata | Parent IDs, state version/hash and event linkage are stored on existing checkpoints | Corruption/foreign parent fails closed; legacy version-zero rows retain compatibility; failed insert preserves prior checkpoint |
| Split tool outcome/step/checkpoint writes | `CommitStepResult` commits the outcome, completed step and checkpoint together | Rollback and duplicate-conflict fixtures; completed invocation recovery does not repeat provider I/O or double-charge bytes |
| In-memory-only interrupted model retry | Invocation intent/reservation/checkpoint precede I/O; output/usage metadata and checkpoint follow in one transaction | Structured completion has at most one recovery reissue, including after another restart; partial streams require explicit review instead of pretending to resume KV state |
| Model resource failure became ordinary failed run | Fakeable resource monitor checks before expensive calls and periodically; typed OOM/timeout and unhealthy resources cause durable pause | No new calls while paused; counters/deadline and partial text survive; finite probe allowance ends in blocked outcome |
| Overnight name without a sleep policy | `KEEP_AWAKE` opt-in and explicit conversation-selected overnight profile request a Linux inhibitor | Acquisition is best effort, active only after helper readiness; unavailable provider warns, cleanup releases on success/cancel/pause; no global power policy changes |

Invocation metadata/output is stored in existing internal events and checkpoint JSON, not a competing invocation/run table. Input hashes explicitly include resolved model/endpoint, sampling, stop sequences, schema and ordered messages. Invalid model text is not persisted as reasoning. Calls are conservatively charged before I/O; cancellation/crash does not refund them. An uncertain fetch or interrupted stream is not permission for a blind replay.

## Configuration

`.env.example` contains production defaults: lease 30 seconds, guard enabled, free RAM minimum 512 MiB, free VRAM minimum 256 MiB, GPU ceiling 85 C, process RSS ceiling 2 GiB, llama health timeout 2 seconds, resource checks every 5 seconds, and five cumulative recovery probes. Probe backoff starts at 5 seconds and caps at 60 seconds. Automatic pause/resume cycles do not reset probe counts. A new user-requested retry run gets a fresh budget.

Unknown telemetry remains nullable and is accompanied by warnings; it is not a measurement of zero or proof a configured metric passed. The Go heap value is a runtime measurement, not the process RSS or systemd cgroup memory. Thresholds are best-effort guards, not hard cgroup isolation of the model server.

`KEEP_AWAKE=false` is the ordinary default. Explicitly choosing `research_settings.profile=overnight_laptop` requests keep-awake for that run; setting an overnight server default alone does not imply user consent. Availability of the executable does not prove successful acquisition. The inhibitor cannot override every firmware/desktop lid-close policy, and `Linger=no` remains relevant after logout.

## Persistence and recovery

Migration `010_execution.sql` adds columns to existing tasks/checkpoints only. Lease ownership remains separate from the process-wide worker lock. Expired work is reconciled; non-idempotent unknown outcomes retain review-required state. Resource-paused jobs own no execution lease. User/manual pause is never automatically resumed by the resource poller. Cancellation and an expired absolute deadline win over healthy probes.

Task lifecycle enum remains compatible. `terminal_outcome` distinguishes blocked, needs-review/budget-exhausted/partial outcomes from ordinary workflow completion. Runtime/doctor and Run projections expose safe resource/pause information. Dedicated resource dashboard and notification delivery belong to later phases.

Rollback is not an automatic DROP/rebuild. Existing columns are additive, but new immutable policy JSON includes keep-awake metadata that an older strict snapshot reader may not accept. Prefer a forward fix or a tested pre-upgrade database backup with the matching binary. Retain immutable artifacts. Never delete checkpoints to force replay.

## Verification and limitations

Storage/domain, monitor/inhibitor, config/API/model-adapter and worker fixtures exercise lease loss, pause rollback/reopen, probe caps, cancellation races, typed OOM/model timeout, stream partial preservation, inhibitor cleanup, conservative reservations and schema-repair limits. They use local fakes/loopback fixtures, not artificial claims of a real overnight run.

PDF isolation remains fail-closed and still requires a rootless local Docker endpoint plus an immutable extractor image. Resource monitoring does not turn rootful Docker into rootless isolation. There is no permission for host-shell model tools or an unsandboxed PDF fallback.
