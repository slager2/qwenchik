import { applyRunEvent, createStreamState, mergeMessages, mergeRunEventHistory, type StreamState } from '../conversation-state.mjs'
import type { AgentEvent, ConversationMessage } from '../types'

export interface Transcript { messages: ConversationMessage[]; stream: StreamState }
export type TranscriptAction = { type: 'snapshot'; messages: ConversationMessage[] } | { type: 'run'; id: string } | { type: 'event'; event: AgentEvent } | { type: 'history'; id: string; events: AgentEvent[] } | { type: 'resync'; id: string; cursor: number }
export function transcriptReducer(state: Transcript, action: TranscriptAction): Transcript {
  if (action.type === 'snapshot') return { ...state, stream: { ...state.stream, needsSync: false }, messages: mergeMessages(state.messages, action.messages) }
  if (action.type === 'run') return state.stream.runID === action.id ? state : { ...state, stream: createStreamState(action.id) }
  if (action.type === 'history') return state.stream.runID === action.id ? { ...state, stream: mergeRunEventHistory(state.stream, action.events) } : state
  if (action.type === 'resync') return { ...state, stream: { ...createStreamState(action.id), events: state.stream.runID === action.id ? state.stream.events : [], cursor: action.cursor } }
  const result = applyRunEvent(state.stream, state.messages, action.event)
  return { stream: result.state, messages: result.messages }
}
