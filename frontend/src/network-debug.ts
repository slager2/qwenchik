export type ReloadReason = 'initial' | 'sse_open' | 'sse_event' | 'sse_resync' | 'terminal' | 'fallback_poll' | 'manual_retry' | 'user_action'

if (import.meta.env.DEV) void import('./network-debug-store').then(({ install }) => install())

export function recordHTTP(value: { method: string; path: string; status?: number; duration_ms: number; response_bytes?: number; aborted?: boolean }) {
  if (import.meta.env.DEV) void import('./network-debug-store').then(({ install, recordRequest }) => { install(); recordRequest(value) })
}

export function recordSSE(kind: 'open' | 'reconnect' | 'close', path: string) {
  if (import.meta.env.DEV) void import('./network-debug-store').then(({ install, recordSSE: record }) => { install(); record(kind, path) })
}

export function recordReload(resource: string, reason: ReloadReason) {
  if (import.meta.env.DEV) void import('./network-debug-store').then(({ install, recordReload: record }) => { install(); record(resource, reason) })
}
