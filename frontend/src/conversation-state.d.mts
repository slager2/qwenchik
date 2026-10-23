import type { AgentEvent, ConversationMessage, DraftMessage, Evidence, SourceRecord } from './types'
export function terminal(status: string): boolean
export function mergeMessages(current: ConversationMessage[], incoming: ConversationMessage[]): ConversationMessage[]
export interface StreamState {
  runID: string
  cursor: number
  streams: Record<string, { id: string; index: number }>
  events: AgentEvent[]
  needsSync: boolean
}
export function createStreamState(runID: string): StreamState
export function applyRunEvent(state: StreamState, messages: ConversationMessage[], event: AgentEvent): { state: StreamState; messages: ConversationMessage[] }
export function safeURL(value: unknown): string
export function normalizeSources(value: unknown): SourceRecord[]
export function sourceErrorCode(code: string): string
export interface CitationReference { id: string; label: string; title: string; url: string }
export function citationReferences(evidence: (Evidence | SourceRecord)[]): CitationReference[]
export function explicitResearchContextReferences(userMessage: ConversationMessage | null | undefined, conversationID: string): CitationReference[]
export function legacyDraft(events: AgentEvent[], savedIDs?: string[]): DraftMessage | null
