import test from 'node:test'
import assert from 'node:assert/strict'
import { applyWorkspaceDelta, fullWorkspaceResources, workspaceEventResources } from './workspace-sync.mjs'

test('Workspace applies the narrowest resource invalidation for each event', () => {
  assert.deepEqual(workspaceEventResources({ type: 'message.delta' }), [])
  assert.deepEqual(workspaceEventResources({ type: 'message.created' }), ['messages'])
  assert.deepEqual(workspaceEventResources({ type: 'task.status_changed' }), ['task', 'tasks'])
  assert.deepEqual(workspaceEventResources({ type: 'run.paused' }), ['task', 'runtime', 'tasks'])
  assert.deepEqual(workspaceEventResources({ type: 'step.completed' }), ['steps'])
  assert.deepEqual(workspaceEventResources({ type: 'evidence.created' }), ['evidence'])
  assert.deepEqual(workspaceEventResources({ type: 'artifact.created' }), ['artifacts'])
  assert.deepEqual(workspaceEventResources({ type: 'approval.approved' }), ['approvals', 'tasks'])
  assert.deepEqual(workspaceEventResources({ type: 'tool.started', payload: { tool: 'search' } }), [])
  assert.deepEqual(workspaceEventResources({ type: 'tool.started', payload: { tool: 'fetch_url' } }), ['evidence'])
  assert.deepEqual(workspaceEventResources({ type: 'task.failed' }), fullWorkspaceResources())
})

test('Workspace rejects missing or malformed stream chunks before advancing its draft', () => {
  const first = applyWorkspaceDelta(null, { stream_id: 's', message_id: 'm', chunk_index: 0, delta: 'A' }, 10)
  assert.equal(first.draft.content, 'A')
  assert.equal(first.accepted, true)

  const next = applyWorkspaceDelta(first.draft, { stream_id: 's', message_id: 'm', chunk_index: 1, delta: 'B' }, 11)
  assert.equal(next.draft.content, 'AB')

  const gap = applyWorkspaceDelta(next.draft, { stream_id: 's', message_id: 'm', chunk_index: 3, delta: 'D' }, 12)
  assert.equal(gap.draft, next.draft)
  assert.deepEqual({ accepted: gap.accepted, needsSync: gap.needsSync, error: gap.error }, { accepted: false, needsSync: true, error: 'gap' })

  const bad = applyWorkspaceDelta(next.draft, { stream_id: 's', message_id: 'm', chunk_index: -1, delta: 'bad' }, 12)
  assert.equal(bad.error, 'invalid')
  assert.equal(bad.accepted, false)

  const duplicate = applyWorkspaceDelta(next.draft, { stream_id: 's', message_id: 'm', chunk_index: 1, delta: 'B' }, 13)
  assert.equal(duplicate.draft.content, 'AB')
  assert.equal(duplicate.draft.seq, 13)
})
