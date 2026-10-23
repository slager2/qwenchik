# Runtime telemetry

Qwenchik exposes host-resource and local-inference telemetry in Chat Details and in the selected Workspace task. The same model is available in `GET /api/runs/{runID}/runtime`; per-run SSE also sends a canonical `snapshot` and coalesced `runtime_snapshot`, `inference_progress`, and `resource_warning` frames. These telemetry frames do not advance the durable event cursor. The frontend consumes the current SSE connection and does not start a telemetry polling loop.

## Metric contract

Each typed metric includes `value`, `unit`, `source`, `updated_at`, and an optional bounded error code:

| `source` | Meaning |
| --- | --- |
| `exact` | A provider returned the measurement or count directly. |
| `estimated` | A bounded estimate was derived from observed output; it is not an exact tokenizer count. |
| `unavailable` | No trustworthy value was collected; `value` is JSON `null`, never a fabricated zero. |

The snapshot's `timestamp` and `stale_after_ms` define a freshness window. The UI marks snapshots, inference status, and individual readings stale after their own `updated_at` exceeds that window; a stale value is retained as the last observation and is not shown as current. Capabilities describe which metric families the current host/provider can collect, not whether a particular reading succeeded. Provider errors are represented by bounded codes such as `unsupported`, `permission_denied`, `timeout`, or `collection_failed`; raw provider responses and exception strings are not part of the public contract.

System fields include OS/platform metadata, uptime, CPU usage and temperature, RAM used/free/total, GPU device utilization/temperature/VRAM/power, agent and llama-server RSS, power source/watts, battery state, llama-server health, and slots. Unsupported fields remain unavailable. Host-level power-draw watts are currently unavailable on both platforms; per-GPU NVIDIA `power.draw` is reported when `nvidia-smi` supports it. llama.cpp `/metrics` and slot endpoints are optional and their absence does not become a zero.

## Platform collection

- **Linux:** RAM uses `MemAvailable`, process RSS comes from procfs, CPU utilization is calculated from aggregate `/proc/stat` deltas, and CPU temperature comes from supported hwmon/thermal sensors. GPU readings use a fixed bounded `nvidia-smi` query; the collection does not search the caller's `PATH`. UPower's `OnBattery` reports AC/battery source with a 30-second cache; battery percentage and charging state are not currently collected.
- **Windows:** CPU, memory, uptime, OS metadata, and process data use bounded gopsutil/Win32 calls. AC/battery source and battery percentage/charging use `GetSystemPowerStatus` with a 30-second cache. NVIDIA GPU metrics, including `power.draw` when supported, are queried only from fixed NVIDIA system locations with bounded command execution. CPU temperature is unavailable in this implementation. Missing NVIDIA tooling or unsupported readings remain unavailable.
- Other operating systems report unsupported system metrics rather than Linux-specific guesses. Go heap allocation is a separate process-runtime measurement and is not process RSS or system memory.

The resource guard consumes only its configured thresholds. Unknown sensor data is not a pass and is not itself a fabricated violation. Resource-guard pauses require fresh observations and recovery margins before resuming. Existing `run.paused`, `run.resource_probe`, `run.resumed`, `run.blocked`, and `run.failed` events remain the durable record of guard transitions.

## Inference, streaming, and persistence

Inference telemetry describes a model invocation: queued/loading/prefilling/generating/completed/failed/cancelled status; requested, prompt, output, cached, and total tokens; prompt/output rates; time to first token; prompt/generation time; and context usage/window. Provider usage and llama.cpp timing fields are exact when present. If streamed text arrives without usage counts, output tokens and throughput are marked estimated from observed runes; no claim of exact model tokenization is made.

While a run is active, the latest typed snapshot is held in a bounded process-local cache (128 runs) and sent through runtime/SSE frames. Frequent snapshots are not written to SQLite. When an LLM invocation finishes, a small `inference.summary` internal event stores only the final bounded numeric/status summary and invocation ID. The summary is written in the same transaction as the invocation finish when valid; summary-only insertion failures do not fail the model invocation. It contains no prompt, response text, raw errors, or provider payload. The existing invocation result remains free of telemetry.

The event uses the existing journal and requires no database migration. It is internal and is not exposed by public event replay. `RunRuntime` projects only the latest validated summary as `inference_summary`, so token/timing summaries remain visible after process restart. System/GPU samples are not persisted; after restart they stay unavailable until recollected. The same run can have multiple LLM invocation summaries; only the latest is shown in the compact telemetry panel.

## Local smoke and verification

Check that no task/run is active before a manual model request. Use read-only readiness endpoints; `/api/doctor` performs an inference probe and is not a read-only check.

```sh
curl --noproxy '*' --fail http://127.0.0.1:8000/health
curl --noproxy '*' --fail http://127.0.0.1:8000/v1/models
curl --noproxy '*' --fail http://127.0.0.1:8080/api/health
```

In the local UI, create one Chat conversation, ask for a one-word response, and wait for the run to finish. Open Chat Details and confirm that live host values carry provenance/freshness labels, the inference section reports a final invocation, and Resource Guard displays either known values or explicit unavailable states. `GET /api/runs/{runID}/runtime` should include live `telemetry` while the worker cache is present and a persisted `inference_summary` after an invocation finishes. When `/metrics`, slots, GPU tooling, or optional host sensors are unsupported, the relevant fields should remain unavailable.

Repository checks use the bundled FTS5 build tag:

```sh
go test -tags sqlite_fts5 ./...
go vet -tags sqlite_fts5 ./...
npm --prefix frontend run build
npm --prefix frontend test
npm --prefix frontend run test:browser
```

Phase 10 smoke verification used a disposable database/workspace and a second loopback Qwenchik process, leaving the existing application database untouched. One minimal Chat run completed against the configured local llama.cpp server; the runtime response contained live telemetry and an exact four-token output count. After restarting the disposable app process, the final `inference_summary` was still present with status `completed` and source `llama.cpp`. No tasks were active in the existing worker and the model server reported its only slot idle before the request.

Windows CI/cross-build commands can compile the Windows tests on Linux (`CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go test -tags sqlite_fts5 -exec=/bin/true ./...`), but `-exec=/bin/true` does not run those test binaries. Native Windows test execution is still required to verify OS-specific sensors and API behavior.
