const terminalEvents = new Set(['task.completed', 'task.failed', 'task.cancelled'])
const stepEvents = new Set(['plan.created', 'step.started', 'step.completed', 'step.skipped'])
const sourceEvents = new Set(['source.updated', 'source.fetch.started', 'source.fetch.failed', 'source.failed', 'source.extracted', 'evidence.created'])
const approvalEvents = new Set(['approval.requested', 'approval.approved', 'approval.rejected', 'approval.expired'])
const runtimeEvents = new Set(['run.paused', 'run.resumed', 'run.resource_probe', 'run.blocked', 'run.failed'])

const full = ['task', 'messages', 'events', 'checkpoints', 'steps', 'artifacts', 'evidence', 'approvals', 'runtime', 'tasks']

export function workspaceEventResources(event) {
  if (!event || typeof event.type !== 'string' || event.type === 'message.delta') return []
  if (terminalEvents.has(event.type)) return [...full]
  if (event.type === 'message.created') return ['messages']
  if (event.type === 'task.status_changed' || event.type === 'task.recovered') return ['task', 'tasks']
  if (runtimeEvents.has(event.type)) return ['task', 'runtime', 'tasks']
  if (stepEvents.has(event.type)) return ['steps']
  if (sourceEvents.has(event.type) || ['tool.started', 'tool.failed', 'tool.completed'].includes(event.type) && event.payload?.tool === 'fetch_url') return ['evidence']
  if (event.type === 'artifact.created') return ['artifacts']
  if (approvalEvents.has(event.type)) return ['approvals', 'tasks']
  return []
}

export const fullWorkspaceResources = () => [...full]

export function applyWorkspaceDelta(current, payload, seq) {
  if (!payload || typeof payload.stream_id !== 'string' || typeof payload.message_id !== 'string' || typeof payload.delta !== 'string' || !Number.isSafeInteger(payload.chunk_index) || payload.chunk_index < 0 || !Number.isSafeInteger(seq) || seq <= 0) {
    return { draft: current, accepted: false, needsSync: true, error: 'invalid' }
  }
  const sameStream = !!current && current.stream_id === payload.stream_id && current.message_id === payload.message_id
  if ((!sameStream && payload.chunk_index !== 0) || (sameStream && payload.chunk_index > current.chunk_index + 1)) {
    // Keep the last verified text visible while the canonical REST snapshot
    // is being fetched. A gap must never make the UI look empty or erase a
    // partial answer that has already been validated locally.
    return { draft: current, accepted: false, needsSync: true, error: 'gap' }
  }
  if (!sameStream) return { draft: { ...payload, content: payload.delta, seq }, accepted: true, needsSync: false, error: '' }
  if (payload.chunk_index <= current.chunk_index) return { draft: { ...current, seq }, accepted: true, needsSync: false, error: '' }
  return {
    draft: { ...current, delta: payload.delta, content: current.content + payload.delta, chunk_index: payload.chunk_index, seq },
    accepted: true,
    needsSync: false,
    error: '',
  }
}
