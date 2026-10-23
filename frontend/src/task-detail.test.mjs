import test from 'node:test'
import assert from 'node:assert/strict'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { TaskFailureCard, SourceList } from './diagnostic-ui.mjs'
import { failureSummary, streamLabel } from './observability.mjs'
import { createTaskDetailTracker } from './task-detail.mjs'

function deferred() {
  let resolve
  const promise = new Promise((done) => { resolve = done })
  return { promise, resolve }
}

test('polling observes failure before SSE and refreshes final diagnostics and evidence once', async () => {
  const tracker = createTaskDetailTracker()
  const stale = deferred()
  const final = deferred()
  const responses = [stale, final]
  const pending = []
  let requests = 0
  let detail = { events: [], evidence: [] }
  const observe = (status) => {
    if (!tracker.observe('task-a', status)) return
    const isCurrent = tracker.beginRequest('task-a')
    pending.push(responses[requests++].promise.then((result) => {
      if (isCurrent()) detail = result
    }))
  }

  observe('running')
  observe('verifying')
  observe('failed')
  assert.equal(streamLabel('failed', 'live'), 'Поток завершён')
  for (let poll = 0; poll < 5; poll += 1) observe('failed')
  assert.equal(requests, 2)

  const payload = { message: 'Поиск недоступен', missing: ['Второй независимый источник'], code: 'search_unavailable' }
  final.resolve({
    events: [{ type: 'task.failed', payload }],
    evidence: [{ id: 'ev-final', canonical_url: 'https://example.org/final', title: 'Final source', excerpt: 'Saved before failure', provider: 'direct_http', retrieved_at: '', content_sha256: '1234567890abcdef' }],
  })
  await pending[1]
  stale.resolve({ events: [], evidence: [] })
  await pending[0]

  assert.match(failureSummary(detail.events[0].payload), /Второй независимый источник/)
  const markup = renderToStaticMarkup(createElement(TaskFailureCard, { payload: detail.events[0].payload, evidenceCount: detail.evidence.length }))
    + renderToStaticMarkup(createElement(SourceList, { sources: detail.evidence, formatTime: () => 'checked' }))
  for (const text of ['search_unavailable', 'Второй независимый источник', 'Сохранено источников: 1', 'https://example.org/final', 'Final source']) assert.ok(markup.includes(text))
})

test('terminal observations deduplicate by selection and status and allow a new run', () => {
  const tracker = createTaskDetailTracker()
  assert.equal(tracker.observe('', undefined), false)
  assert.equal(tracker.observe('a', 'running'), true)
  assert.equal(tracker.observe('a', 'running'), false)
  for (const status of ['completed', 'failed', 'cancelled']) {
    assert.equal(tracker.observe('a', status), true)
    assert.equal(tracker.observe('a', status), false)
  }
  assert.equal(tracker.observe('b', 'cancelled'), true)
  assert.equal(tracker.observe('b', 'cancelled'), false)
  assert.equal(tracker.observe('a', 'failed'), true)
  assert.equal(tracker.observe('a', 'queued'), false)
  assert.equal(tracker.observe('a', 'running'), false)
  assert.equal(tracker.observe('a', 'failed'), true)
  assert.equal(tracker.observe('a', 'failed'), false)
})

test('selection changes invalidate responses even when switching back to the same task', async () => {
  const tracker = createTaskDetailTracker()
  tracker.select('a')
  const oldResponse = deferred()
  const isOldCurrent = tracker.beginRequest('a')
  let displayed = 'b detail'
  const pending = oldResponse.promise.then((value) => {
    if (isOldCurrent()) displayed = value
  })
  tracker.select('b')
  const isBCurrent = tracker.beginRequest('b')
  assert.equal(tracker.beginRequest('a')(), false)
  assert.equal(isBCurrent(), true)
  oldResponse.resolve('stale a detail')
  await pending
  assert.equal(displayed, 'b detail')
  tracker.select('a')
  assert.equal(isOldCurrent(), false)
  assert.equal(isBCurrent(), false)
  const isNewCurrent = tracker.beginRequest('a')
  assert.equal(isNewCurrent(), true)
  tracker.select('')
  assert.equal(isNewCurrent(), false)
})

test('selective refresh invalidates stale responses only for the same resource', () => {
  const tracker = createTaskDetailTracker()
  tracker.select('task')
  const oldMessages = tracker.beginRequest('task', 'messages')
  const steps = tracker.beginRequest('task', 'steps')
  const newMessages = tracker.beginRequest('task', 'messages')
  assert.equal(oldMessages(), false)
  assert.equal(steps(), true)
  assert.equal(newMessages(), true)
})
