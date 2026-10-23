const terminalEvents = new Set(['task.completed', 'task.failed', 'task.cancelled'])
const stepEvents = new Set(['plan.created', 'step.started', 'step.completed', 'step.skipped'])
const sourceEvents = new Set(['source.updated', 'source.fetch.started', 'source.fetch.failed', 'source.failed', 'source.extracted', 'evidence.created'])
const approvalEvents = new Set(['approval.requested', 'approval.approved', 'approval.rejected', 'approval.expired'])
const runtimeEvents = new Set(['run.paused', 'run.resumed', 'run.resource_probe', 'run.blocked', 'run.failed'])

const full = ['task', 'messages', 'events', 'checkpoints', 'steps', 'artifacts', 'evidence', 'approvals', 'tasks']

export function workspaceEventResources(event) {
  if (!event || typeof event.type !== 'string' || event.type === 'message.delta') return []
  if (terminalEvents.has(event.type)) return [...full]
  if (event.type === 'message.created') return ['messages']
  if (event.type === 'task.status_changed' || event.type === 'task.recovered') return ['task', 'tasks']
  if (runtimeEvents.has(event.type)) return ['task', 'tasks']
  if (stepEvents.has(event.type)) return ['steps']
  if (sourceEvents.has(event.type) || ['tool.started', 'tool.failed', 'tool.completed'].includes(event.type) && event.payload?.tool === 'fetch_url') return ['evidence']
  if (event.type === 'artifact.created') return ['artifacts']
  if (approvalEvents.has(event.type)) return ['approvals', 'tasks']
  return []
}

export const fullWorkspaceResources = () => [...full]
