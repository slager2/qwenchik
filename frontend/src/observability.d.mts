import type { ProviderStatus } from './types'
export const healthLabels: Record<string, string>
export function providerSummary(provider: ProviderStatus): string
export function diagnosticSummary(payload: Record<string, unknown>): string
export function failureSummary(payload: Record<string, unknown>): string
export function streamLabel(status: string, connection: string): string
export function evidenceSummary(count: number, status: string): string
