import test from 'node:test'
import assert from 'node:assert/strict'
import { applyRunEvent, createStreamState, mergeMessages, normalizeSources, safeURL, citationReferences, terminal, legacyDraft } from './conversation-state.mjs'

const message = { id: 'm', run_id: 'r', conversation_id: 'c', role: 'assistant', content: '', status: 'streaming', last_event_seq: 0, created_at: '2026-10-19T10:00:00Z' }
const delta = (seq, index, text, stream = 's') => ({ task_id: 'r', seq, type: 'message.delta', payload: { message_id: 'm', stream_id: stream, chunk_index: index, delta: text } })
const apply = (current, event) => applyRunEvent(current.state, current.messages, event)

test('replayed, duplicate and out-of-order events never duplicate text', () => {
  let current = { state: createStreamState('r'), messages: [message] }
  current = apply(current, delta(1, 0, 'Hello'))
  current = apply(current, delta(1, 0, 'Hello'))
  current = apply(current, delta(4, 1, ' world'))
  current = apply(current, delta(2, 0, 'old'))
  assert.equal(current.messages[0].content, 'Hello world')
  assert.equal(current.state.cursor, 4)
})

test('persisted snapshot plus full replay applies only new chunks', () => {
  let current = { state: createStreamState('r'), messages: [{ ...message, content: 'AB', last_event_seq: 2 }] }
  for (const event of [delta(1, 0, 'A'), delta(2, 1, 'B'), delta(3, 2, 'C')]) current = apply(current, event)
  assert.equal(current.messages[0].content, 'ABC')
  assert.equal(current.state.needsSync, false)
})

test('index zero in a new synthesis stream replaces provisional output', () => {
  let current = { state: createStreamState('r'), messages: [message] }
  current = apply(current, delta(1, 0, 'Unverified answer'))
  current = apply(current, delta(2, 0, 'Corrected', 'repair'))
  current = apply(current, delta(3, 1, ' answer', 'repair'))
  assert.equal(current.messages[0].content, 'Corrected answer')
})

test('missing chunks require canonical sync and never concatenate corrupted output', () => {
  let current = { state: createStreamState('r'), messages: [message] }
  current = apply(current, delta(1, 0, 'A'))
  current = apply(current, delta(3, 2, 'C'))
  assert.equal(current.messages[0].content, 'A')
  assert.equal(current.state.needsSync, true)
})

test('stop/failure retain partial text, foreign runs and malformed events are ignored', () => {
  let current = { state: createStreamState('r'), messages: [message] }
  current = apply(current, delta(1, 0, 'Partial'))
  current = apply(current, { task_id: 'r', seq: 2, type: 'task.cancelled', payload: {} })
  current = apply(current, { ...delta(3, 1, 'Wrong'), task_id: 'another-run' })
  current = apply(current, { task_id: 'r', seq: 3, type: 'message.delta', payload: {} })
  assert.equal(current.messages[0].content, 'Partial')
  assert.equal(terminal('cancelled'), true)
  assert.equal(terminal('waiting_approval'), false)
})

test('canonical terminal message wins over stale snapshots without clearing history', () => {
  const partial = { ...message, content: 'Partial', last_event_seq: 5 }
  const stale = { ...message, content: '', last_event_seq: 2 }
  const completed = { ...message, content: 'Final answer', status: 'completed', last_event_seq: 6 }
  assert.equal(mergeMessages([partial], [stale])[0].content, 'Partial')
  assert.equal(mergeMessages([partial], [completed])[0].status, 'completed')
  assert.equal(mergeMessages([completed], [stale])[0].content, 'Final answer')
})

test('source normalization and citation references reject unsafe or rejected targets', () => {
  const sources = normalizeSources({ sources: [{ source_id: 's', url: 'https://example.org', status: 'validated' }, { id: 'bad', url: 'javascript:alert(1)' }] })
  assert.equal(sources[0].canonical_url, 'https://example.org/')
  assert.equal(sources[1].canonical_url, '')
  assert.equal(safeURL('https://user:password@example.org'), '')
  assert.deepEqual(citationReferences([{ id: 'good', canonical_url: 'https://example.org' }, { id: 'bad', status: 'rejected', canonical_url: 'https://bad.org' }]).map((item) => item.id), ['good'])
})

test('legacy terminal tasks reconstruct persisted partial deltas without duplicating saved messages', () => {
  const events = [delta(1, 0, 'Partial'), delta(2, 1, ' answer'), { task_id: 'r', seq: 3, type: 'task.failed', payload: {} }]
  assert.equal(legacyDraft(events).content, 'Partial answer')
  assert.equal(legacyDraft(events, ['m']), null)
  assert.equal(legacyDraft([...events, delta(4, 0, 'Repaired partial', 'repair')]).content, 'Repaired partial')
})
