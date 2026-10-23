const MARKER = 'QWENCHIK_DEV_NETWORK_DIAGNOSTICS_V1'
const reasons = new Set(['initial', 'sse_open', 'sse_event', 'sse_resync', 'terminal', 'fallback_poll', 'manual_retry', 'user_action'])
const staticEndpoints = new Set(['health', 'doctor', 'providers', 'conversations', 'tasks', 'approvals', 'memories', 'refinement', 'runs', 'bundles'])
const dynamicRoots = new Set(['conversations', 'runs', 'tasks', 'approvals', 'memories'])

function endpointPath(input: string): string[] {
  try {
    const parsed = new URL(input, 'http://qwenchik.local')
    return parsed.pathname.split('/').filter(Boolean)
  } catch {
    return []
  }
}

export function normalizeEndpoint(input: string): string {
  const parts = endpointPath(input)
  if (parts[0] !== 'api' || parts.length < 2 || parts.length > 6 || !staticEndpoints.has(parts[1])) return '/api/:endpoint'
  const root = parts[1]
  if (root === 'memories' && parts.length === 3 && parts[2] === 'search') return '/api/memories/search'
  if (root === 'bundles' && parts.length === 3 && parts[2] === 'rollback') return '/api/bundles/rollback'
  if (dynamicRoots.has(root)) {
    if (parts.length < 3) return `/api/${root}`
    const suffix = parts.slice(3)
    if (root === 'tasks' && suffix[0] === 'artifacts' && suffix.length === 2) return '/api/tasks/:id/artifacts/:id'
    const validSuffix = new Set(['messages', 'events', 'events/stream', 'sources', 'evidence', 'plan', 'runtime', 'cancel', 'retry', 'continue', 'checkpoints', 'steps', 'artifacts', 'approvals', 'pause', 'resume', 'run', 'approve', 'reject', 'accept', 'stale', 'supersede'])
    if (suffix.length && !validSuffix.has(suffix.join('/'))) return '/api/:endpoint'
    return suffix.length ? `/api/${root}/:id/${suffix.join('/')}` : `/api/${root}/:id`
  }
  if (root === 'bundles' && parts.length === 3) return '/api/bundles/:id'
  if (parts.length === 2 && ['health', 'doctor', 'providers', 'conversations', 'tasks', 'approvals', 'memories', 'refinement'].includes(root)) return `/api/${root}`
  return '/api/:endpoint'
}

function blank(): QwenchikDebugSnapshot {
  return {
    marker: MARKER,
    generated_at: new Date().toISOString(),
    requests: { count: 0, aborted: 0, failures: 0, response_bytes_reported: 0, response_bytes_total: 0, duration_ms_total: 0, by_endpoint: {} },
    sse: { opens: 0, reconnects: 0, closes: 0, active: 0, by_endpoint: {} },
    reloads: { total: 0, by_reason: {}, by_resource: {} },
    recent: [],
  }
}

let state = blank()
const recentLimit = 100
function touch() { state.generated_at = new Date().toISOString() }
function push(entry: Record<string, string | number | boolean | null>) { state.recent.push(entry); if (state.recent.length > recentLimit) state.recent.splice(0, state.recent.length - recentLimit); touch() }
function endpointStats(method: string, endpoint: string) {
  const key = `${method} ${endpoint}`
  return state.requests.by_endpoint[key] ??= { count: 0, aborted: 0, failures: 0, bytes_reported: 0, response_bytes_total: 0, duration_ms_total: 0 }
}

export function recordRequest(value: { method: string; path: string; status?: number; duration_ms: number; response_bytes?: number; aborted?: boolean; failed?: boolean }) {
  const endpoint = normalizeEndpoint(value.path)
  const method = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS'].includes(value.method.toUpperCase()) ? value.method.toUpperCase() : 'OTHER'
  const stats = endpointStats(method, endpoint)
  const aborted = value.aborted === true
  const failed = value.failed === true || typeof value.status !== 'number' || value.status >= 400
  const bytesKnown = Number.isSafeInteger(value.response_bytes) && value.response_bytes! >= 0
  state.requests.count++; stats.count++
  if (aborted) { state.requests.aborted++; stats.aborted++ }
  if (failed && !aborted) { state.requests.failures++; stats.failures++ }
  if (bytesKnown) { state.requests.response_bytes_reported++; state.requests.response_bytes_total += value.response_bytes!; stats.bytes_reported++; stats.response_bytes_total += value.response_bytes! }
  const duration = Number.isFinite(value.duration_ms) ? Math.max(0, Math.round(value.duration_ms)) : 0
  state.requests.duration_ms_total += duration; stats.duration_ms_total += duration
  push({ type: 'http', method, endpoint, status: value.status ?? null, duration_ms: duration, response_bytes: bytesKnown ? value.response_bytes! : null, aborted })
}

export function recordSSE(kind: 'open' | 'reconnect' | 'close', path: string) {
  const endpoint = normalizeEndpoint(path)
  const current = state.sse.by_endpoint[endpoint] ??= { opens: 0, reconnects: 0, closes: 0, active: 0 }
  if (kind === 'open') { state.sse.opens++; state.sse.active++; current.opens++; current.active++ }
  if (kind === 'reconnect') { state.sse.reconnects++; current.reconnects++ }
  if (kind === 'close') { state.sse.closes++; current.closes++; state.sse.active = Math.max(0, state.sse.active - 1); current.active = Math.max(0, current.active - 1) }
  push({ type: 'sse', kind, endpoint, active: state.sse.active })
}

export function recordReload(resource: string, reason: string) {
  const safeReason = reasons.has(reason) ? reason : 'user_action'
  const safeResource = /^[a-z][a-z0-9_.-]{0,47}$/.test(resource) ? resource : 'resource'
  state.reloads.total++
  state.reloads.by_reason[safeReason] = (state.reloads.by_reason[safeReason] ?? 0) + 1
  const resources = state.reloads.by_resource[safeResource] ??= {}
  resources[safeReason] = (resources[safeReason] ?? 0) + 1
  push({ type: 'reload', resource: safeResource, reason: safeReason })
}

export function snapshot(): QwenchikDebugSnapshot { return JSON.parse(JSON.stringify(state)) as QwenchikDebugSnapshot }
export function reset() { state = blank(); install() }
export function install() { if (typeof window !== 'undefined') window.__qwenchikDebug = { snapshot, reset } }
