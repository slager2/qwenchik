const terminalEvents = new Set(['task.completed', 'task.failed', 'task.cancelled'])
const stepEvents = new Set(['step.started', 'step.completed', 'step.skipped'])
const sourceEvents = new Set(['source.updated', 'source.fetch.started', 'source.fetch.failed', 'source.failed', 'source.extracted'])
const runEvents = new Set(['task.status_changed', 'task.recovered', 'run.paused', 'run.resumed', 'run.resource_probe', 'run.blocked', 'run.failed'])
const reasonPriority = { initial: 0, fallback_poll: 1, sse_event: 2, sse_open: 2, sse_resync: 3, manual_retry: 3, user_action: 3, terminal: 4 }

export function runEventInvalidations(event, mode) {
  if (!event || typeof event.type !== 'string' || event.type === 'message.delta') return { resources: [], reason: 'sse_event' }
  const resources = new Set()
  const terminal = terminalEvents.has(event.type)
  if (event.type === 'message.created') resources.add('messages')
  if (runEvents.has(event.type)) { resources.add('run'); resources.add('runtime') }
  if (event.type === 'plan.created' || stepEvents.has(event.type)) resources.add('plan')
  if (sourceEvents.has(event.type) || ['tool.started', 'tool.failed', 'tool.completed'].includes(event.type) && event.payload?.tool === 'fetch_url') resources.add('sources')
  if (event.type === 'evidence.created') resources.add('evidence')
  if (event.type === 'search.provider') resources.add('providers')
  if (terminal) {
    for (const key of ['messages', 'conversation', 'run', 'runtime']) resources.add(key)
    if (mode === 'research') for (const key of ['plan', 'sources', 'evidence']) resources.add(key)
  }
  return { resources: [...resources], reason: terminal ? 'terminal' : 'sse_event' }
}

export function runResyncResources(mode) {
  const resources = ['messages', 'conversation', 'run', 'runtime']
  if (mode === 'research') resources.push('plan', 'sources', 'evidence')
  return resources
}

export function nextFallbackDelay(current) {
  const delay = Number.isFinite(current) ? Math.max(15000, Math.floor(current)) : 15000
  return Math.min(delay * 2, 60000)
}

export function createCoalescedScheduler(flush, delay = 300) {
  let timer
  let reason = 'initial'
  const pending = new Set()
  return {
    schedule(resources, nextReason = 'sse_event') {
      for (const resource of resources) pending.add(resource)
      if ((reasonPriority[nextReason] ?? 3) > (reasonPriority[reason] ?? 0)) reason = nextReason
      if (timer !== undefined || pending.size === 0) return
      timer = setTimeout(() => {
        timer = undefined
        const selected = [...pending]
        pending.clear()
        const selectedReason = reason
        reason = 'initial'
        flush(selected, selectedReason)
      }, Math.max(0, delay))
    },
    cancel() { clearTimeout(timer); timer = undefined; pending.clear(); reason = 'initial' },
  }
}
