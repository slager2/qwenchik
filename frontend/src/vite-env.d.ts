/// <reference types="vite/client" />

interface Window {
  __qwenchikDebug?: {
    snapshot: () => QwenchikDebugSnapshot
    reset: () => void
  }
}

interface QwenchikDebugSnapshot {
  marker: string
  generated_at: string
  requests: {
    count: number
    aborted: number
    failures: number
    response_bytes_reported: number
    response_bytes_total: number
    duration_ms_total: number
    by_endpoint: Record<string, { count: number; aborted: number; failures: number; bytes_reported: number; response_bytes_total: number; duration_ms_total: number }>
  }
  sse: { opens: number; reconnects: number; closes: number; active: number; by_endpoint: Record<string, { opens: number; reconnects: number; closes: number; active: number }> }
  reloads: { total: number; by_reason: Record<string, number>; by_resource: Record<string, Record<string, number>> }
  recent: Array<Record<string, string | number | boolean | null>>
}
