import test from 'node:test'
import assert from 'node:assert/strict'
import { normalizeEndpoint, recordReload, recordRequest, recordSSE, reset, snapshot } from './network-debug-store.ts'

test('development endpoint labels discard query, origin and dynamic identifiers', () => {
  assert.equal(normalizeEndpoint('https://local.invalid/api/runs/01234567-89ab-cdef-0123-456789abcdef/events/stream?after=22&token=TOP_SECRET'), '/api/runs/:id/events/stream')
  assert.equal(normalizeEndpoint('/api/tasks/task-private-identifier/artifacts/artifact-private'), '/api/tasks/:id/artifacts/:id')
  assert.equal(normalizeEndpoint('/api/unknown/PRIVATE_TOKEN?api_key=SECRET'), '/api/:endpoint')
})

test('aggregates methods, available bytes, latency and aborted requests without payloads', () => {
  reset()
  recordRequest({ method: 'GET', path: '/api/runs/run-one?token=SECRET', status: 200, duration_ms: 9.6, response_bytes: 124 })
  recordRequest({ method: 'GET', path: '/api/runs/run-two?after=987', status: 0, duration_ms: 3, aborted: true })
  recordRequest({ method: 'POST', path: '/api/conversations/c-id/messages?key=SECRET', status: 503, duration_ms: 17 })
  const debug = snapshot()
  assert.equal(debug.requests.count, 3)
  assert.equal(debug.requests.aborted, 1)
  assert.equal(debug.requests.failures, 1)
  assert.equal(debug.requests.response_bytes_reported, 1)
  assert.equal(debug.requests.by_endpoint['GET /api/runs/:id'].count, 2)
  assert.equal(debug.requests.by_endpoint['GET /api/runs/:id'].aborted, 1)
  assert.equal(debug.requests.by_endpoint['POST /api/conversations/:id/messages'].failures, 1)
  assert.ok(!JSON.stringify(debug).includes('SECRET'))
  assert.ok(!JSON.stringify(debug).includes('run-one'))
})

test('tracks SSE opens/reconnects/closes and bounded reload-reason counters', () => {
  reset()
  const endpoint = '/api/runs/private-id/events/stream?after=999'
  recordSSE('open', endpoint)
  recordSSE('reconnect', endpoint)
  recordSSE('close', endpoint)
  recordSSE('close', endpoint)
  recordReload('chat.messages', 'initial')
  recordReload('chat.messages', 'sse_resync')
  recordReload('chat.messages', 'not-a-reason')
  const debug = snapshot()
  assert.equal(debug.sse.opens, 1)
  assert.equal(debug.sse.reconnects, 1)
  assert.equal(debug.sse.closes, 2)
  assert.equal(debug.sse.active, 0)
  assert.equal(debug.reloads.by_reason.initial, 1)
  assert.equal(debug.reloads.by_reason.sse_resync, 1)
  assert.equal(debug.reloads.by_reason.user_action, 1)
  assert.ok(!JSON.stringify(debug).includes('private-id'))
})
