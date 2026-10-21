import type { AgentEvent, ResearchPlan, SourceRecord } from './types'
export function publicResearchEvents(events: AgentEvent[]): AgentEvent[]
export function researchSources(sources: SourceRecord[], events: AgentEvent[]): SourceRecord[]
export function researchQueries(events: AgentEvent[]): { queries: string[]; summaries: string[] }
export function researchSteps(plan: ResearchPlan | null | undefined, events: AgentEvent[]): { id: string; description: string; status: string }[]
export function researchError(code?: string): string
export function formatSourceBytes(value?: number): string
