import test from 'node:test'
import assert from 'node:assert/strict'
import { createRunEvidenceCache } from './evidence-cache.mjs'

test('historical evidence requests are shared per run across concurrent messages', async () => {
  let requests = 0
  const resolvers = new Map()
  const cache = createRunEvidenceCache((runID) => { requests++; return new Promise((done) => { resolvers.set(runID, done) }) })
  const first = cache.load('run-a')
  const second = cache.load('run-a')
  cache.load('run-b')
  await Promise.resolve()
  assert.equal(requests, 2)
  resolvers.get('run-a')([{ id: 'good', task_id: 'run-a' }, { id: 'foreign', task_id: 'run-b' }])
  resolvers.get('run-b')([{ id: 'b', task_id: 'run-b' }])
  const [a1, a2] = await Promise.all([first, second])
  assert.equal(a1, a2)
  assert.deepEqual(cache.get('run-a').data, [{ id: 'good', task_id: 'run-a' }])
})

test('failed evidence is cached until an explicit retry and preserves no cross-run data', async () => {
  let requests = 0
  const cache = createRunEvidenceCache(async (runID) => { requests++; if (requests === 1) throw new Error('secret=hidden'); return [{ id: `e-${runID}`, task_id: runID }] })
  const failed = await cache.load('run-a')
  assert.equal(failed.error, 'Ссылки этого ответа временно недоступны.')
  await cache.load('run-a')
  assert.equal(requests, 1)
  await cache.load('run-a', true)
  assert.equal(requests, 2)
  assert.equal(cache.get('run-a').data[0].id, 'e-run-a')
  assert.deepEqual(cache.get('run-b').data, undefined)
})
