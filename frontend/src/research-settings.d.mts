import type { Conversation, ConversationSetup, ConversationSettingsPatch, ResearchProfile } from './types'
export const profileMinutes: Readonly<Record<ResearchProfile, number>>
export const profileLabels: Readonly<Record<ResearchProfile, string>>
export const maxOutputTokens: number
export const defaultSetup: ConversationSetup
export function conversationSetup(conversation?: Conversation | null): ConversationSetup
export function setupProblem(setup: ConversationSetup): string
export function settingsPatch(setup: ConversationSetup): ConversationSettingsPatch
export function withProfile(setup: ConversationSetup, profile: ResearchProfile): ConversationSetup
export function setupSummary(setup: ConversationSetup): string
