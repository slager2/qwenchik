export const UNFILED_SPACE_ID = 'unfiled'
export const SPACE_STORAGE_KEY = 'qwenchik.spaces.v1'

const unfiled = Object.freeze({ id: UNFILED_SPACE_ID, name: 'Без пространства', description: 'Разговоры, которые ещё не разобраны.', created_at: '', updated_at: '' })

export function emptySpaceState() {
  return { version: 1, spaces: [], assignments: {}, active_space_id: UNFILED_SPACE_ID }
}

export function normalizeSpaceState(value) {
  const source = value && typeof value === 'object' ? value : {}
  const seen = new Set()
  const spaces = Array.isArray(source.spaces) ? source.spaces.flatMap((item) => {
    if (!item || typeof item !== 'object' || typeof item.id !== 'string' || typeof item.name !== 'string') return []
    const id = item.id.trim()
    const name = item.name.trim().slice(0, 80)
    if (!id || id === UNFILED_SPACE_ID || !name || seen.has(id)) return []
    seen.add(id)
    return [{ id, name, description: typeof item.description === 'string' ? item.description.trim().slice(0, 280) : '', created_at: typeof item.created_at === 'string' ? item.created_at : '', updated_at: typeof item.updated_at === 'string' ? item.updated_at : '' }]
  }) : []
  const valid = new Set([UNFILED_SPACE_ID, ...spaces.map((item) => item.id)])
  const assignments = {}
  if (source.assignments && typeof source.assignments === 'object') for (const [conversationID, spaceID] of Object.entries(source.assignments)) {
    if (conversationID && typeof spaceID === 'string' && valid.has(spaceID) && spaceID !== UNFILED_SPACE_ID) assignments[conversationID] = spaceID
  }
  return { version: 1, spaces, assignments, active_space_id: typeof source.active_space_id === 'string' && valid.has(source.active_space_id) ? source.active_space_id : UNFILED_SPACE_ID }
}

export function parseSpaceState(raw) {
  if (!raw) return emptySpaceState()
  try { return normalizeSpaceState(JSON.parse(raw)) } catch { return emptySpaceState() }
}

export function allSpaces(state) {
  return [...state.spaces, unfiled]
}

export function conversationSpaceID(state, conversationID) {
  return state.assignments[conversationID] || UNFILED_SPACE_ID
}

export function conversationsInSpace(state, conversations, spaceID) {
  return conversations.filter((conversation) => conversationSpaceID(state, conversation.id) === spaceID)
}

export function createSpace(state, input, id, now) {
  const name = String(input?.name || '').trim().slice(0, 80)
  if (!name) return state
  const description = String(input?.description || '').trim().slice(0, 280)
  const space = { id, name, description, created_at: now, updated_at: now }
  return normalizeSpaceState({ ...state, spaces: [space, ...state.spaces], active_space_id: id })
}

export function assignConversation(state, conversationID, spaceID) {
  const valid = new Set(allSpaces(state).map((space) => space.id))
  if (!conversationID || !valid.has(spaceID)) return state
  const assignments = { ...state.assignments }
  if (spaceID === UNFILED_SPACE_ID) delete assignments[conversationID]
  else assignments[conversationID] = spaceID
  return { ...state, assignments, active_space_id: spaceID }
}
