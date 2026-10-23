import type { RunRuntime, RuntimeTelemetry } from './types'

export function mergeRuntimeSnapshot(current: RunRuntime | null | undefined, incoming: RunRuntime, options?: { replaceWarnings?: boolean }): RunRuntime
export function nextTelemetryExpiry(telemetry: RuntimeTelemetry | null | undefined, runtime: RunRuntime | null | undefined, now: number): number | undefined
