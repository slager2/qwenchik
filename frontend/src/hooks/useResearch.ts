import { useCallback } from 'react'
import { api } from '../api'
import { normalizeSources } from '../conversation-state.mjs'
import type { Evidence, ResearchPlan } from '../types'
import { useRemote } from './useRemote'

/** Research plan, sources, evidence and providers for a run; loaded only when enabled. */
export function useResearch(runID: string, enabled: boolean, evidenceEnabled = enabled) {
  const plan = useRemote(useCallback((signal) => enabled && runID ? api.runPlan(runID, signal) : Promise.resolve<ResearchPlan | null>(null), [enabled, runID]), 0, 'research.plan')
  const sources = useRemote(useCallback(async (signal) => enabled && runID ? normalizeSources(await api.runSources(runID, signal)) : [], [enabled, runID]), 0, 'research.sources')
  const evidence = useRemote(useCallback(async (signal) => {
    if (!evidenceEnabled || !runID) return []
    const result = await api.runEvidence(runID, signal)
    return (Array.isArray(result) ? result : []) as Evidence[]
  }, [evidenceEnabled, runID]), 0, 'research.evidence')
  const providers = useRemote(useCallback((signal) => enabled ? api.providers(signal) : Promise.resolve(null), [enabled]), 0, 'research.providers')
  return { plan, sources, evidence, providers }
}
