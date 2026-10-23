import type { Evidence } from './types'
export interface EvidenceCacheEntry { loading: boolean; error: string; data?: Evidence[]; pending?: Promise<EvidenceCacheEntry> }
export interface RunEvidenceCache {
  get(runID: string): EvidenceCacheEntry
  subscribe(runID: string, listener: () => void): () => void
  load(runID: string, retry?: boolean): Promise<EvidenceCacheEntry>
}
export function createRunEvidenceCache(loadEvidence: (runID: string) => Promise<Evidence[]>): RunEvidenceCache
