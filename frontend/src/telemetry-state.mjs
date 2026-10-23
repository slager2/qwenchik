export function mergeRuntimeSnapshot(current, incoming, { replaceWarnings = false } = {}) {
  if (!current || current.task_id !== incoming.task_id) return incoming

  const currentRevision = Number.isSafeInteger(current.telemetry_revision) ? current.telemetry_revision : -1
  const incomingRevision = Number.isSafeInteger(incoming.telemetry_revision) ? incoming.telemetry_revision : -1
  if (current.cursor > incoming.cursor) return current
  const preserveTelemetry = currentRevision > incomingRevision

  const snapshotWarnings = incoming.runtime_warnings ?? runtimeWarningSnapshot(incoming.runtime)
  const runtimeWarnings = snapshotWarnings ?? (replaceWarnings ? [] : current.runtime_warnings)
  const inferenceSummary = incoming.inference_summary ?? current.inference_summary

  return {
    ...incoming,
    telemetry: preserveTelemetry ? current.telemetry : incoming.telemetry ?? current.telemetry,
    ...(inferenceSummary ? { inference_summary: inferenceSummary } : {}),
    telemetry_revision: Math.max(currentRevision, incomingRevision) >= 0 ? Math.max(currentRevision, incomingRevision) : undefined,
    runtime_warnings: runtimeWarnings,
  }
}

function runtimeWarningSnapshot(runtime) {
  if (!runtime || typeof runtime !== 'object') return undefined
  const warnings = new Set()
  const objects = [runtime, runtime.resources, runtime.last_snapshot, runtime.snapshot]
  for (const object of objects) {
    if (!object || typeof object !== 'object') continue
    for (const key of ['warnings', 'active_warnings']) {
      if (!Array.isArray(object[key])) continue
      for (const warning of object[key]) if (typeof warning === 'string' && warnings.size < 16) warnings.add(warning)
    }
  }
  return [...warnings]
}

export function nextTelemetryExpiry(telemetry, runtime, now) {
  const staleAfterMs = telemetry?.stale_after_ms ?? 30_000
  if (!Number.isFinite(staleAfterMs) || staleAfterMs <= 0) return undefined

  let earliest
  const visit = (value, depth = 0) => {
    if (depth > 8 || value == null) return
    if (Array.isArray(value)) {
      for (const item of value.slice(0, 32)) visit(item, depth + 1)
      return
    }
    if (typeof value !== 'object') return

    for (const key of ['timestamp', 'updated_at', 'snapshot_at']) {
      const raw = value[key]
      if (typeof raw !== 'string') continue
      const timestamp = Date.parse(raw)
      if (!Number.isFinite(timestamp)) continue
      const expiry = timestamp + staleAfterMs + 1
      if (expiry > now && (earliest === undefined || expiry < earliest)) earliest = expiry
    }
    for (const item of Object.values(value)) visit(item, depth + 1)
  }

  visit(telemetry)
  visit(runtime?.runtime?.resources)
  visit(runtime?.inference_summary)
  return earliest
}
