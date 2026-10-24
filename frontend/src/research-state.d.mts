import type { AgentEvent, ResearchPlan, SourceRecord, Run } from './types'
export function planningFailure(run: Run | null, events: AgentEvent[]): Record<string, unknown> | null
export function publicResearchEvents(events: AgentEvent[]): AgentEvent[]
export function researchSources(sources: SourceRecord[], events: AgentEvent[]): SourceRecord[]
export function researchQueries(events: AgentEvent[]): { queries: string[]; summaries: string[] }
export function researchSteps(plan: ResearchPlan | null | undefined, events: AgentEvent[]): { id: string; description: string; status: string }[]
export function researchError(code?: string): string
export function formatSourceBytes(value?: number): string
