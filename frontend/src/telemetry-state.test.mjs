import test from 'node:test'
import assert from 'node:assert/strict'
import { mergeRuntimeSnapshot, nextTelemetryExpiry } from './telemetry-state.mjs'

test('an older runtime GET cannot replace newer SSE telemetry or warnings', () => {
  const current = {
    task_id: 'task-1', cursor: 5, telemetry_revision: 9,
    telemetry: { timestamp: 'newer' }, runtime_warnings: ['new warning'],
  }
  const delayedGet = {
    task_id: 'task-1', cursor: 4, telemetry_revision: 8,
    telemetry: { timestamp: 'older' },
  }

  assert.equal(mergeRuntimeSnapshot(current, delayedGet), current)
})

test('runtime snapshots remain scoped to their task and retain telemetry when a response omits it', () => {
  const current = { task_id: 'task-1', cursor: 2, telemetry_revision: 3, telemetry: { timestamp: 'current' }, runtime_warnings: ['warning'] }
  const next = { task_id: 'task-2', cursor: 1 }
  assert.equal(mergeRuntimeSnapshot(current, next), next)

  const refresh = { task_id: 'task-1', cursor: 3, telemetry_revision: 4 }
  assert.deepEqual(mergeRuntimeSnapshot(current, refresh), {
    ...refresh, telemetry: current.telemetry, telemetry_revision: 4, runtime_warnings: current.runtime_warnings,
  })
})

test('a reconnect snapshot clears warnings that disappeared while the stream was disconnected', () => {
  const current = { task_id: 'task-1', cursor: 5, telemetry_revision: 4, runtime_warnings: ['old warning'] }
  const reconnected = { task_id: 'task-1', cursor: 5, telemetry_revision: 4, runtime: { resources: { warnings: [] } } }
  assert.deepEqual(mergeRuntimeSnapshot(current, reconnected, { replaceWarnings: true }).runtime_warnings, [])
  assert.deepEqual(mergeRuntimeSnapshot(current, reconnected).runtime_warnings, [])
})

test('a successful runtime GET refreshes the warning baseline after the SSE stream closes', () => {
  const current = { task_id: 'task-1', cursor: 5, telemetry_revision: 4, runtime_warnings: ['old warning'] }
  const refreshed = { task_id: 'task-1', cursor: 5, telemetry_revision: 4, runtime: { resources: { warnings: ['new warning'] } } }
  assert.deepEqual(mergeRuntimeSnapshot(current, refreshed).runtime_warnings, ['new warning'])
})

test('freshness timer advances through metric expiry boundaries with a fake clock', () => {
  const now = Date.parse('2026-01-01T00:00:00.000Z')
  const telemetry = {
    timestamp: new Date(now - 5_000).toISOString(), stale_after_ms: 10_000,
    system: { cpu_usage: { updated_at: new Date(now - 3_000).toISOString() } },
  }
  assert.equal(nextTelemetryExpiry(telemetry, null, now), now + 5_001)
  assert.equal(nextTelemetryExpiry(telemetry, null, now + 5_001), now + 7_001)
  assert.equal(nextTelemetryExpiry(telemetry, null, now + 7_001), undefined)
})
