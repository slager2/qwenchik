import { useEffect, type Dispatch, type MutableRefObject, type SetStateAction } from 'react'
import { runEventInvalidations, runResyncResources } from '../chat-sync.mjs'
import { terminal, type StreamState } from '../conversation-state.mjs'
import { recordReload, recordSSE, type ReloadReason } from '../network-debug'
import { mergeRuntimeSnapshot } from '../telemetry-state.mjs'
import type { AgentEvent, ConversationMode, InferenceTelemetry, Run, RunRuntime, RuntimeTelemetry } from '../types'
import type { TranscriptAction } from './transcript'

export const runEventTypes = ['context.compacted', 'continuation.requested', 'verification.completed', 'run.paused', 'run.resumed', 'run.resource_probe', 'run.blocked', 'run.failed', 'task.created', 'task.status_changed', 'task.recovered', 'message.created', 'message.delta', 'plan.created', 'step.started', 'step.completed', 'step.skipped', 'tool.started', 'tool.completed', 'tool.failed', 'source.updated', 'source.fetch.started', 'source.fetch.failed', 'source.failed', 'source.extracted', 'research.warning', 'search.provider', 'search.hit', 'query.updated', 'verification.updated', 'evidence.created', 'artifact.created', 'approval.requested', 'approval.approved', 'approval.rejected', 'approval.expired', 'task.completed', 'task.failed', 'task.cancelled']

export interface RunEventStreamBindings {
  dispatch: Dispatch<TranscriptAction>
  setConnection: (value: string) => void
  setError: (value: string) => void
  streamRef: MutableRefObject<StreamState>
  modeRef: MutableRefObject<ConversationMode>
  currentRunRef: MutableRefObject<Run | null>
  connectionRef: MutableRefObject<string>
  runSetterRef: MutableRefObject<Dispatch<SetStateAction<Run | null | undefined>>>
  runtimeSetterRef: MutableRefObject<Dispatch<SetStateAction<RunRuntime | null | undefined>>>
  syncSchedulerRef: MutableRefObject<(resources: string[], reason: ReloadReason) => void>
  fallbackTimerRef: MutableRefObject<number | undefined>
  fallbackDelayRef: MutableRefObject<number>
  fallbackSchedulerRef: MutableRefObject<() => void>
  closeEventStreamRef: MutableRefObject<() => void>
}

/**
 * One EventSource per run: replays from the current cursor, applies events to
 * the transcript, merges runtime/telemetry frames and schedules canonical
 * resyncs. Reconnects when `reconnect` changes.
 */
export function useRunEventStream(runID: string, reconnect: number, bindings: RunEventStreamBindings) {
  const { dispatch, setConnection, setError, streamRef, modeRef, currentRunRef, connectionRef, runSetterRef, runtimeSetterRef, syncSchedulerRef, fallbackTimerRef, fallbackDelayRef, fallbackSchedulerRef, closeEventStreamRef } = bindings
  useEffect(() => {
    if (!runID) { setConnection('idle'); return }
    let disposed = false
    let opened = false
    let reconnecting = false
    let closed = false
    let terminalSeen = false
    let stableTimer: number | undefined
    const endpoint = `/api/runs/${encodeURIComponent(runID)}/events/stream`
    const closeStream = () => { if (opened && !closed) { closed = true; recordSSE('close', endpoint) } }
    const clearFallback = () => { window.clearTimeout(fallbackTimerRef.current); fallbackTimerRef.current = undefined }
    const cursor = streamRef.current.runID === runID ? streamRef.current.cursor : 0
    setConnection('connecting')
    const source = new EventSource(`${endpoint}?after=${cursor}`)
    const closeSource = () => { terminalSeen = true; source.close(); closeStream() }
    closeEventStreamRef.current = closeSource
    const receive = (raw: Event) => {
      if (disposed) return
      try {
        const event = JSON.parse((raw as MessageEvent<string>).data) as AgentEvent
        if (event.task_id !== runID || event.visibility !== 'user') return
        if (event.type !== 'message.delta') recordReload('chat.transcript', ['task.completed', 'task.failed', 'task.cancelled'].includes(event.type) ? 'terminal' : 'sse_event')
        dispatch({ type: 'event', event })
        const invalidation = runEventInvalidations(event, modeRef.current)
        const isTerminal = invalidation.reason === 'terminal'
        if (isTerminal) {
          terminalSeen = true
          clearFallback()
          closeSource(); setConnection('terminal')
        }
        if (invalidation.resources.length) syncSchedulerRef.current(invalidation.resources, invalidation.reason)
      } catch { setConnection('invalid'); setError('Не удалось прочитать событие. Сохранённые сообщения продолжают обновляться.') }
    }
    for (const type of [...runEventTypes, 'research.round.assessed', 'research.round.stopped', 'report.section.updated']) source.addEventListener(type, receive)
    source.addEventListener('snapshot', (raw) => {
      if (disposed) return
      try {
        const next = JSON.parse((raw as MessageEvent<string>).data) as RunRuntime
        if (next.task_id !== runID || !Number.isSafeInteger(next.cursor)) return
        recordReload('chat.runtime', 'sse_open')
        runtimeSetterRef.current((previous) => mergeRuntimeSnapshot(previous, next, { replaceWarnings: true }))
        if (currentRunRef.current?.id === runID) {
          runSetterRef.current((previous) => previous?.id === runID ? { ...previous, status: next.status, stage: next.stage, phase: next.stage, terminal_outcome: next.terminal_outcome, pause_reason: next.pause_reason, resource_reason: next.resource_reason } : previous)
        }
      } catch { setConnection('invalid') }
    })
    source.addEventListener('runtime_snapshot', (raw) => {
      if (disposed) return
      try {
        const update = JSON.parse((raw as MessageEvent<string>).data) as { telemetry?: RuntimeTelemetry; telemetry_revision?: number }
        if (!update.telemetry || !Number.isSafeInteger(update.telemetry_revision) || update.telemetry_revision! < 0) return
        runtimeSetterRef.current((previous) => {
          if (previous?.task_id !== runID || (previous.telemetry_revision ?? -1) > update.telemetry_revision!) return previous
          return { ...previous, telemetry: update.telemetry, telemetry_revision: update.telemetry_revision }
        })
      } catch { setConnection('invalid') }
    })
    source.addEventListener('inference_progress', (raw) => {
      if (disposed) return
      try {
        const update = JSON.parse((raw as MessageEvent<string>).data) as { inference?: InferenceTelemetry; telemetry_revision?: number }
        if (!update.inference || !Number.isSafeInteger(update.telemetry_revision) || update.telemetry_revision! < 0) return
        runtimeSetterRef.current((previous) => {
          if (previous?.task_id !== runID || !previous.telemetry || (previous.telemetry_revision ?? -1) > update.telemetry_revision!) return previous
          return { ...previous, telemetry_revision: update.telemetry_revision, telemetry: { ...previous.telemetry, inference: update.inference! } }
        })
      } catch { setConnection('invalid') }
    })
    source.addEventListener('resource_warning', (raw) => {
      if (disposed) return
      try {
        const update = JSON.parse((raw as MessageEvent<string>).data) as { warnings?: unknown }
        const warnings = Array.isArray(update.warnings) ? update.warnings.filter((item): item is string => typeof item === 'string').slice(0, 16) : []
        runtimeSetterRef.current((previous) => previous?.task_id === runID ? { ...previous, runtime_warnings: warnings } : previous)
      } catch { setConnection('invalid') }
    })
    source.addEventListener('resync', (raw) => {
      if (disposed) return
      try {
        const next = JSON.parse((raw as MessageEvent<string>).data) as { cursor: number }
        if (!Number.isSafeInteger(next.cursor) || next.cursor < 0) throw new Error('invalid cursor')
        dispatch({ type: 'resync', id: runID, cursor: next.cursor })
        syncSchedulerRef.current(runResyncResources(modeRef.current), 'sse_resync')
      } catch { setConnection('invalid') }
    })
    source.onopen = () => {
      if (disposed) return
      if (!opened) { opened = true; recordSSE('open', endpoint) }
      reconnecting = false
      if (!terminalSeen) setConnection('live')
      window.clearTimeout(stableTimer)
      stableTimer = window.setTimeout(() => {
        if (connectionRef.current === 'live') { clearFallback(); fallbackDelayRef.current = 15000 }
      }, 5000)
    }
    source.onerror = () => {
      if (disposed || terminalSeen) return
      window.clearTimeout(stableTimer)
      const latestRun = currentRunRef.current
      if (latestRun && terminal(latestRun.status)) { clearFallback(); closeSource(); setConnection('terminal'); return }
      setConnection('reconnecting')
      if (!reconnecting) { reconnecting = true; recordSSE('reconnect', endpoint) }
      fallbackSchedulerRef.current()
    }
    return () => { disposed = true; source.close(); clearFallback(); window.clearTimeout(stableTimer); closeStream(); if (closeEventStreamRef.current === closeSource) closeEventStreamRef.current = () => {} }
  }, [runID, reconnect])
}
