import type { ClarificationChoice, ConversationMessage, ResearchClarification } from './types'

export const CLARIFY_PREFERENCE_KEY: string
export const CLARIFY_EXTRA_LIMIT: number
export function readClarifyPreference(storage: Pick<Storage, 'getItem'> | null | undefined): boolean
export function writeClarifyPreference(storage: Pick<Storage, 'setItem'> | null | undefined, enabled: boolean): void
export function clarificationUsable(clarification: ResearchClarification | null | undefined): boolean
export function clarificationChoice(clarification: ResearchClarification | null | undefined, selectedID: string, answers?: Record<string, string>, extra?: string): ClarificationChoice | undefined
export function messageClarification(message: Pick<ConversationMessage, 'role' | 'metadata'> | null | undefined): { title: string; goal: string; answers: Array<{ question: string; answer: string }>; extra: string } | null
