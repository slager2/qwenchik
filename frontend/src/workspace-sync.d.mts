export function workspaceEventResources(event: { type?: string; payload?: Record<string, unknown> } | null | undefined): string[]
export function fullWorkspaceResources(): string[]
export function applyWorkspaceDelta(current: import('./types').DraftMessage | null, payload: Partial<import('./types').MessageDeltaPayload>, seq: number): {
  draft: import('./types').DraftMessage | null
  accepted: boolean
  needsSync: boolean
  error: '' | 'invalid' | 'gap'
}
