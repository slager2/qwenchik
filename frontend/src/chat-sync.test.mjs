import test from 'node:test'
import assert from 'node:assert/strict'
import { createCoalescedScheduler, nextFallbackDelay, runEventInvalidations, runResyncResources } from './chat-sync.mjs'

test('Chat SSE invalidates only resources affected by committed events', () => {
  assert.deepEqual(runEventInvalidations({ type: 'message.delta' }, 'research'), { resources: [], reason: 'sse_event' })
  assert.deepEqual(runEventInvalidations({ type: 'step.completed' }, 'research').resources, ['plan'])
  assert.deepEqual(runEventInvalidations({ type: 'evidence.created' }, 'research').resources, ['evidence'])
  assert.deepEqual(runEventInvalidations({ type: 'tool.started', payload: { tool: 'fetch_url' } }, 'research').resources, ['sources'])
  assert.deepEqual(runEventInvalidations({ type: 'tool.started', payload: { tool: 'search' } }, 'research').resources, [])
  assert.deepEqual(runEventInvalidations({ type: 'task.completed' }, 'research').resources, ['messages', 'conversation', 'run', 'runtime', 'plan', 'sources', 'evidence'])
  assert.deepEqual(runEventInvalidations({ type: 'task.completed' }, 'chat').resources, ['messages', 'conversation', 'run', 'runtime'])
  assert.deepEqual(runResyncResources('chat'), ['messages', 'conversation', 'run', 'runtime'])
})

test('fallback uses a finite bounded 15/30/60 second backoff', () => {
  assert.equal(nextFallbackDelay(15000), 30000)
  assert.equal(nextFallbackDelay(30000), 60000)
  assert.equal(nextFallbackDelay(60000), 60000)
  assert.equal(nextFallbackDelay(Number.NaN), 30000)
})

test('coalesced resource refresh groups burst events and terminal cause wins', async () => {
  const flushed = []
  const scheduler = createCoalescedScheduler((resources, reason) => flushed.push({ resources, reason }), 10)
  scheduler.schedule(['plan'], 'sse_event')
  scheduler.schedule(['sources', 'plan'], 'sse_event')
  scheduler.schedule(['messages', 'evidence'], 'terminal')
  await new Promise((resolve) => setTimeout(resolve, 25))
  assert.equal(flushed.length, 1)
  assert.deepEqual(flushed[0], { resources: ['plan', 'sources', 'messages', 'evidence'], reason: 'terminal' })
  scheduler.cancel()
})
