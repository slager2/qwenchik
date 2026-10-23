import test from 'node:test'
import assert from 'node:assert/strict'
import { fullWorkspaceResources, workspaceEventResources } from './workspace-sync.mjs'

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
