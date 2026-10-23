export type RunSyncReason = 'sse_event' | 'terminal'
export type SyncReason = 'initial' | 'fallback_poll' | 'sse_event' | 'sse_open' | 'sse_resync' | 'manual_retry' | 'user_action' | 'terminal'
export function runEventInvalidations(event: { type?: string; payload?: Record<string, unknown> } | null | undefined, mode: string): { resources: string[]; reason: RunSyncReason }
export function runResyncResources(mode: string): string[]
export function nextFallbackDelay(current: number): number
export function createCoalescedScheduler(flush: (resources: string[], reason: SyncReason) => void, delay?: number): { schedule(resources: string[], reason?: SyncReason): void; cancel(): void }
