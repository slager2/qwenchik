import type { Conversation, Space, SpaceState } from './types'
export const UNFILED_SPACE_ID: 'unfiled'
export const SPACE_STORAGE_KEY: string
export function emptySpaceState(): SpaceState
export function normalizeSpaceState(value: unknown): SpaceState
export function parseSpaceState(raw: string | null | undefined): SpaceState
export function allSpaces(state: SpaceState): Space[]
export function conversationSpaceID(state: SpaceState, conversationID: string): string
export function conversationsInSpace(state: SpaceState, conversations: Conversation[], spaceID: string): Conversation[]
export function createSpace(state: SpaceState, input: { name: string; description?: string }, id: string, now: string): SpaceState
export function assignConversation(state: SpaceState, conversationID: string, spaceID: string): SpaceState
