import test from 'node:test'
import assert from 'node:assert/strict'
import { UNFILED_SPACE_ID, allSpaces, assignConversation, conversationSpaceID, createSpace, emptySpaceState, normalizeSpaceState, parseSpaceState } from './space-state.mjs'

test('Space state recovers safely from corrupt local storage', () => {
  assert.deepEqual(parseSpaceState('{nope'), emptySpaceState())
  assert.equal(allSpaces(emptySpaceState()).at(-1).id, UNFILED_SPACE_ID)
})

test('Spaces are normalized and assignments cannot point outside known spaces', () => {
  const state = normalizeSpaceState({ spaces: [{ id: 'one', name: '  Research  ', description: ' Notes ' }, { id: 'one', name: 'duplicate' }], assignments: { a: 'one', b: 'missing', c: 'unfiled' }, active_space_id: 'missing' })
  assert.equal(state.spaces.length, 1)
  assert.equal(state.spaces[0].name, 'Research')
  assert.deepEqual(state.assignments, { a: 'one' })
  assert.equal(state.active_space_id, UNFILED_SPACE_ID)
})

test('Conversation can be assigned and returned to legacy unfiled', () => {
  const created = createSpace(emptySpaceState(), { name: 'Qwen research' }, 'space-1', '2026-10-31T00:00:00Z')
  const assigned = assignConversation(created, 'conversation-1', 'space-1')
  assert.equal(conversationSpaceID(assigned, 'conversation-1'), 'space-1')
  const unfiled = assignConversation(assigned, 'conversation-1', UNFILED_SPACE_ID)
  assert.equal(conversationSpaceID(unfiled, 'conversation-1'), UNFILED_SPACE_ID)
  assert.deepEqual(unfiled.assignments, {})
})
