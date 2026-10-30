import type { AgentEvent, ConversationMessage, Evidence, ResearchPlan, Run, RunRuntime } from './types'

export function resourceReasonLabel(reason?: string): string
export function resumeEligibility(run: { id?: string; status: string; pause_reason?: string; resource_reason?: string } | null, runtime?: RunRuntime | null): { allowed: boolean; explanation: string }
export function researchPresentation(input: {
  run: Run | null
  runtime?: RunRuntime | null
  events?: AgentEvent[]
  answer?: ConversationMessage | null
  evidence?: { data?: Evidence[]; loading: boolean; error: string }
}): {
  heading: string; state: string; answerLabel: string; reportLabel: string; evidenceLabel: string
  evidenceCount: number | null; reportAvailable: boolean; hasArtifact: boolean
  criticalMissing: string[]; reportGaps: string[]; fallback: boolean; partial: boolean
}
export function researchPlanPresentation(run: Run | null, runtime: RunRuntime | null | undefined, plan: ResearchPlan | null | undefined, events: AgentEvent[]): {
  steps: { id: string; description: string; status: string }[]
  completed: number; skipped: number; processed: number; activePlan: boolean
}
