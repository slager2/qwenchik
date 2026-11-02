import { useCallback, useEffect, useLayoutEffect, useMemo, useReducer, useRef, useState, type FormEvent, type ReactNode } from 'react'
import { api } from './api'
import { createCoalescedScheduler, nextFallbackDelay, runEventInvalidations, runResyncResources } from './chat-sync.mjs'
import { createRunEvidenceCache } from './evidence-cache.mjs'
import { mergeRuntimeSnapshot } from './telemetry-state.mjs'
import ChatComposer from './ChatComposer'
import ChatTranscript from './ChatTranscript'
import { applyRunEvent, canUseResearchContext, citationReferences, explicitResearchContextReferences, createStreamState, mergeMessages, mergeRunEventHistory, normalizeSources, terminal, type StreamState } from './conversation-state.mjs'
import ResearchInspector from './ResearchInspector'
import ResearchOverview from './ResearchOverview'
import AppShell from './AppShell'
import { conversationSetup, defaultSetup, settingsPatch, setupProblem } from './research-settings.mjs'
import FailureDetails from './FailureDetails'
import { publicResearchEvents } from './research-state.mjs'
import ChatSidebar from './ChatSidebar'
import { recordReload, recordSSE, type ReloadReason } from './network-debug'
import type { AgentEvent, Conversation, ConversationMessage, ConversationMode, ConversationSetup, DoctorResponse, Evidence, InferenceTelemetry, ResearchPlan, Run, RunRuntime, RuntimeTelemetry, Submission } from './types'
import './chat.css'

const failure = (error: unknown) => error instanceof Error ? error.message : 'Не удалось выполнить запрос. Попробуйте ещё раз.'
const terminalLabels: Record<string, string> = { partial: 'Частичный результат', blocked: 'Заблокировано проверкой', budget_exhausted: 'Бюджет исчерпан' }
const labels: Record<string, string> = { queued: 'В очереди', planning: 'Планирование', running: 'В работе', verifying: 'Проверка', completed: 'Готово', failed: 'Не удалось завершить', cancelled: 'Остановлено', paused: 'На паузе', waiting_approval: 'Нужно решение', search: 'Поиск источников', searching: 'Поиск источников', fetch: 'Чтение источников', fetching: 'Чтение источников', extract: 'Извлечение текста', synthesis: 'Подготовка ответа', synthesizing: 'Подготовка ответа', verification: 'Сверка источников', finalization: 'Завершение', discovered: 'Найден', validated: 'Проверен', rejected: 'Отклонён', extracted: 'Текст извлечён', cited: 'Использован в ответе' }
const eventTypes = ['context.compacted', 'continuation.requested', 'verification.completed', 'run.paused', 'run.resumed', 'run.resource_probe', 'run.blocked', 'run.failed', 'task.created', 'task.status_changed', 'task.recovered', 'message.created', 'message.delta', 'plan.created', 'step.started', 'step.completed', 'step.skipped', 'tool.started', 'tool.completed', 'tool.failed', 'source.updated', 'source.fetch.started', 'source.fetch.failed', 'source.failed', 'source.extracted', 'research.warning', 'search.provider', 'search.hit', 'query.updated', 'verification.updated', 'evidence.created', 'artifact.created', 'approval.requested', 'approval.approved', 'approval.rejected', 'approval.expired', 'task.completed', 'task.failed', 'task.cancelled']

function useRemote<T>(load: (signal: AbortSignal) => Promise<T>, interval = 0, resource = 'chat.resource', merge?: (previous: T | undefined, next: T) => T) {
  const [data, setData] = useState<T>()
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const controller = useRef<AbortController | null>(null)
  const sequence = useRef(0)
  const reload = useCallback(async (reason: 'initial' | 'sse_open' | 'sse_event' | 'sse_resync' | 'terminal' | 'fallback_poll' | 'manual_retry' | 'user_action' = 'manual_retry') => {
    recordReload(resource, reason)
    const request = ++sequence.current
    controller.current?.abort()
    const next = new AbortController()
    controller.current = next
    setLoading(true)
    try {
      const result = await load(next.signal)
      if (next.signal.aborted || request !== sequence.current) return
      setData(merge ? (previous) => merge(previous, result) : result)
      setError('')
    } catch (caught) {
      if (!next.signal.aborted && request === sequence.current) setError(failure(caught))
    } finally {
      if (!next.signal.aborted && request === sequence.current) setLoading(false)
    }
  }, [load, merge, resource])
  const updateData = useCallback<typeof setData>((value) => {
    controller.current?.abort()
    sequence.current++
    setData(value)
    setLoading(false)
    setError('')
  }, [])
  useEffect(() => {
    setData(undefined)
    setError('')
    void reload('initial')
    const timer = interval ? window.setInterval(() => void reload('fallback_poll'), interval) : undefined
    return () => { window.clearInterval(timer); controller.current?.abort(); sequence.current++ }
  }, [reload, interval])
  return { data, setData: updateData, error, loading, reload }
}

function ResourceStatus({ resource, empty }: { resource: { data: unknown; error: string; loading: boolean; reload: () => Promise<void> }; empty?: boolean }) {
  if (resource.error) return <div className="chat-resource-error" role="status"><p>{resource.error}{resource.data !== undefined ? ' Показаны последние загруженные данные.' : ''}</p><button type="button" onClick={() => void resource.reload()}>Повторить загрузку</button></div>
  if (resource.loading && resource.data === undefined) return <p className="chat-muted" role="status">Загрузка…</p>
  if (empty) return <p className="chat-muted">Пока нет данных.</p>
  return null
}

function Drawer({ title, onClose, children }: { title: string; onClose: () => void; children: ReactNode }) {
  const ref = useRef<HTMLDialogElement>(null)
  useLayoutEffect(() => {
    const previous = document.activeElement as HTMLElement | null
    ref.current?.showModal()
    return () => { ref.current?.close(); previous?.focus({ preventScroll: true }) }
  }, [])
  return <dialog ref={ref} className="chat-drawer" aria-label={title} onCancel={onClose} onKeyDown={(event) => {
    if (event.key !== 'Tab') return
    const focusable = [...event.currentTarget.querySelectorAll<HTMLElement>('a[href], button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), summary, [tabindex="0"]')].filter((element) => {
      if (element.tabIndex < 0 || element.closest('[inert], [hidden], [aria-hidden="true"]') || !element.getClientRects().length) return false
      if (typeof element.checkVisibility === 'function' && !element.checkVisibility({ checkVisibilityCSS: true, checkOpacity: true })) return false
      // Closed disclosures can retain layout boxes. Only their own first summary
      // participates in keyboard order; hidden descendants must not trap focus.
      for (let ancestor = element.parentElement; ancestor && ancestor !== event.currentTarget; ancestor = ancestor.parentElement) {
        if (ancestor instanceof HTMLDetailsElement && !ancestor.open && !ancestor.querySelector(':scope > summary')?.contains(element)) return false
      }
      return true
    })
    const first = focusable[0]
    const last = focusable.at(-1)
    if (!first || !last) { event.preventDefault(); return }
    if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus() }
    if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus() }
  }} onClick={(event) => { if (event.target === event.currentTarget) onClose() }}><div className="chat-drawer-body"><header><h2>{title}</h2><button type="button" onClick={onClose} aria-label={`Закрыть: ${title}`}>×</button></header>{children}</div></dialog>
}

function chatID() {
  const match = window.location.hash.match(/^#\/chat\/([^/?]+)/)
  try { return match ? decodeURIComponent(match[1]) : '' } catch { return '' }
}

export default function ChatRoute() {
  const [selected, setSelected] = useState(chatID)
  const [sidebar, setSidebar] = useState(false)
  const [newVersion, setNewVersion] = useState(0)
  const conversations = useRemote(useCallback((signal) => api.conversations(signal), []), 30000, 'chat.conversations')
  useEffect(() => {
    const navigate = () => { setSelected(chatID()); setSidebar(false) }
    window.addEventListener('hashchange', navigate)
    return () => window.removeEventListener('hashchange', navigate)
  }, [])
  const select = (id: string) => {
    if (!id) setNewVersion((value) => value + 1)
    window.location.hash = id ? `/chat/${encodeURIComponent(id)}` : '/chat'
    setSelected(id)
    setSidebar(false)
  }
  const navigation = <ChatSidebar conversations={conversations.data ?? []} error={conversations.error} loading={conversations.loading} selected={selected} onSelect={select} onNew={() => select('')} onRetry={() => void conversations.reload('manual_retry')} />
  const focusComposer = useCallback(() => document.getElementById('conversation-input')?.focus(), [])
  return <AppShell activeSection="chat" sidebar={navigation} onNewChat={() => select('')} composerFocus={focusComposer}><div className="conversation-shell"><ConversationView key={selected || `new-${newVersion}`} id={selected} onSelect={select} onChanged={conversations.reload} onSidebar={() => setSidebar(true)} />{sidebar && <Drawer title="Разговоры" onClose={() => setSidebar(false)}>{navigation}</Drawer>}</div></AppShell>
}

interface Transcript { messages: ConversationMessage[]; stream: StreamState }
type TranscriptAction = { type: 'snapshot'; messages: ConversationMessage[] } | { type: 'run'; id: string } | { type: 'event'; event: AgentEvent } | { type: 'history'; id: string; events: AgentEvent[] } | { type: 'resync'; id: string; cursor: number }
function transcriptReducer(state: Transcript, action: TranscriptAction): Transcript {
  if (action.type === 'snapshot') return { ...state, stream: { ...state.stream, needsSync: false }, messages: mergeMessages(state.messages, action.messages) }
  if (action.type === 'run') return state.stream.runID === action.id ? state : { ...state, stream: createStreamState(action.id) }
  if (action.type === 'history') return state.stream.runID === action.id ? { ...state, stream: mergeRunEventHistory(state.stream, action.events) } : state
  if (action.type === 'resync') return { ...state, stream: { ...createStreamState(action.id), events: state.stream.runID === action.id ? state.stream.events : [], cursor: action.cursor } }
  const result = applyRunEvent(state.stream, state.messages, action.event)
  return { stream: result.state, messages: result.messages }
}

function ConversationView({ id, onSelect, onChanged, onSidebar }: { id: string; onSelect: (id: string) => void; onChanged: () => Promise<void>; onSidebar: () => void }) {
  const conversation = useRemote(useCallback((signal) => id ? api.conversation(id, signal) : Promise.resolve<Conversation | null>(null), [id]), id ? 30000 : 0, 'chat.conversation')
  const spaces = useRemote(useCallback((signal) => api.spaces(signal), []), 60000, 'chat.spaces')
  const snapshots = useRemote(useCallback((signal) => id ? api.conversationMessages(id, signal) : Promise.resolve<ConversationMessage[]>([]), [id]), 0, 'chat.messages')
  const [transcript, dispatch] = useReducer(transcriptReducer, { messages: [], stream: createStreamState('') })
  const [evidenceCache] = useState(() => createRunEvidenceCache((sourceRunID: string) => api.runEvidence(sourceRunID)))
  const [runID, setRunID] = useState('')
  const run = useRemote(useCallback((signal) => runID ? api.conversationRun(runID, signal) : Promise.resolve<Run | null>(null), [runID]), 0, 'chat.run')
  const mergeRunRuntime = useCallback((previous: RunRuntime | null | undefined, next: RunRuntime | null) => previous && next ? mergeRuntimeSnapshot(previous, next) : next, [])
  const runtime = useRemote(useCallback((signal) => runID ? api.runRuntime(runID, signal) : Promise.resolve<RunRuntime | null>(null), [runID]), 0, 'chat.runtime', mergeRunRuntime)
  const history = useRemote(useCallback(async (signal) => {
    const events: AgentEvent[] = []
    let after = 0
    if (runID) for (let page = 0; page < 20; page++) {
      const batch = await api.runEvents(runID, after, signal)
      if (!Array.isArray(batch)) throw new Error('Не удалось прочитать историю запуска.')
      const accepted = batch.filter((event) => event.task_id === runID && event.visibility === 'user' && Number.isSafeInteger(event.seq) && event.seq > after)
      events.push(...accepted)
      const next = accepted.reduce((cursor, event) => Math.max(cursor, event.seq), after)
      if (batch.length < 500 || next === after) break
      after = next
      if (page === 19) throw new Error('История запуска превышает лимит синхронизации. Показаны доступные сведения.')
    }
    return { id: runID, events }
  }, [runID]), 0, 'chat.events')
  const runSetterRef = useRef(run.setData)
  const runtimeSetterRef = useRef(runtime.setData)
  runSetterRef.current = run.setData
  runtimeSetterRef.current = runtime.setData
  const [diagnosticsRequested, setDiagnosticsRequested] = useState(false)
  const doctor = useRemote(useCallback((signal) => diagnosticsRequested ? api.doctor(signal) : Promise.resolve<DoctorResponse | null>(null), [diagnosticsRequested]), 0, 'chat.doctor')
  const configuration = useRemote(useCallback((signal) => api.labPolicy(signal), []), 60000, 'chat.configuration')
  const health = useRemote(useCallback((signal) => api.health(signal), []), 60000, 'chat.health')
  const [setup, setSetup] = useState<ConversationSetup>(() => ({ ...defaultSetup }))
  const [settingsDirty, setSettingsDirty] = useState(false)
  const [mode, setMode] = useState<ConversationMode>('chat')
  const [researchContextRunID, setResearchContextRunID] = useState('')
  const modeRef = useRef(mode)
  modeRef.current = mode
  const [text, setText] = useState('')
  const [title, setTitle] = useState('')
  const [editing, setEditing] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [actionRetry, setActionRetry] = useState<(() => void) | null>(null)
  const [details, setDetails] = useState(false)
  const [connection, setConnection] = useState('idle')
  const connectionRef = useRef(connection)
  connectionRef.current = connection
  const [reconnect, setReconnect] = useState(0)
  const [follow, setFollow] = useState(true)
  const [optimistic, setOptimistic] = useState<ConversationMessage[]>([])
  const [mobile, setMobile] = useState(() => window.matchMedia('(max-width: 70rem)').matches)
  const listRef = useRef<HTMLDivElement>(null)
  const centerRef = useRef<HTMLElement>(null)
  const readingRun = useRef({ id: '', openSaved: false, positioned: false })
  const streamRef = useRef(transcript.stream)
  const syncSchedulerRef = useRef<(resources: string[], reason: ReloadReason) => void>(() => {})
  const syncFlushRef = useRef<(resources: string[], reason: ReloadReason) => void>(() => {})
  const coalescedSyncRef = useRef<ReturnType<typeof createCoalescedScheduler> | null>(null)
  const fallbackTimerRef = useRef<number | undefined>(undefined)
  const fallbackDelayRef = useRef(15000)
  const fallbackSchedulerRef = useRef<() => void>(() => {})
  const pageHiddenRef = useRef(document.hidden)
  const closeEventStreamRef = useRef<() => void>(() => {})
  const alive = useRef(true)
  const submissionKey = useRef<{ content: string; key: string } | null>(null)
  const runActionKey = useRef<{ operation: string; key: string } | null>(null)
  const conversationKey = useRef(crypto.randomUUID())
  const createdID = useRef(id)
  const requestBusy = useRef(false)
  streamRef.current = transcript.stream
  const active = !!runID && (!run.data || run.data.id !== runID || !terminal(run.data.status))
  const currentRun = run.data?.id === runID ? run.data : null
  const currentRunRef = useRef<Run | null>(currentRun)
  currentRunRef.current = currentRun
  useEffect(() => { alive.current = true; return () => { alive.current = false } }, [])
  useEffect(() => {
    const media = window.matchMedia('(max-width: 70rem)')
    const update = () => setMobile(media.matches)
    media.addEventListener('change', update)
    return () => media.removeEventListener('change', update)
  }, [])
  useEffect(() => {
    const item = conversation.data
    if (!item) return
    setMode(item.mode)
    const latest = item.active_run_id || item.latest_run_id
    if (latest) setRunID(latest)
  }, [conversation.data])
  useEffect(() => { if (conversation.data && !settingsDirty) setSetup(conversationSetup(conversation.data)) }, [conversation.data, settingsDirty])
  useEffect(() => { if (!editing && conversation.data) setTitle(conversation.data.title) }, [conversation.data, editing])
  useEffect(() => { if (snapshots.data) dispatch({ type: 'snapshot', messages: snapshots.data }) }, [snapshots.data])
  useEffect(() => { dispatch({ type: 'run', id: runID }) }, [runID])
  useEffect(() => { if (history.data?.id === runID) dispatch({ type: 'history', id: runID, events: history.data.events }) }, [history.data, runID])
  useEffect(() => {
    const surface = scrollSurface()
    if (!follow || !surface) return
    surface.scrollTop = surface.scrollHeight
  }, [transcript.messages, follow])

  function scrollSurface() {
    return window.matchMedia('(max-width: 48rem)').matches ? centerRef.current : listRef.current
  }

  function observeScroll(surface: HTMLElement) {
    setFollow(surface.scrollHeight - surface.scrollTop - surface.clientHeight < 100)
  }

  async function action(work: () => Promise<void>) {
    if (requestBusy.current) return
    requestBusy.current = true
    setBusy(true)
    setError('')
    setActionRetry(null)
    try { await work() } catch (caught) {
      if (alive.current) { setError(failure(caught)); setActionRetry(() => () => void action(work)) }
    } finally {
      requestBusy.current = false
      if (alive.current) setBusy(false)
    }
  }

  function accept(result: Submission) {
    if (!alive.current) return
    dispatch({ type: 'snapshot', messages: [result.user_message, result.assistant_message] })
    dispatch({ type: 'run', id: result.run.id })
    setRunID(result.run.id)
    run.setData(result.run)
    conversation.setData((current) => current ? { ...current, active_run_id: result.run.id, latest_run_id: result.run.id, mode: result.run.mode } : current)
    setFollow(true)
    void onChanged()
  }

  function send(event?: FormEvent) {
    event?.preventDefault()
    if (!text.trim() || active || busy) return
    const settingsError = setupProblem(setup)
    if (settingsError) { setError(settingsError); return }
    const content = text.trim()
    const selectedMode = mode
    if (!submissionKey.current || submissionKey.current.content !== content) submissionKey.current = { content, key: crypto.randomUUID() }
    const key = submissionKey.current.key
    let shouldSaveSettings = settingsDirty
    const selectedSetup = { ...setup }
    const now = new Date().toISOString()
    setOptimistic([{ id: `pending-${key}`, conversation_id: id, run_id: '', role: 'user', content, status: 'pending', created_at: now, updated_at: now, last_event_seq: 0 }])
    void action(async () => {
      let target = createdID.current
      if (!target) {
        const item = await api.createConversation(content.slice(0, 70), selectedMode, conversationKey.current, settingsPatch(selectedSetup))
        target = item.id
        createdID.current = target
        shouldSaveSettings = false
        if (alive.current) setSettingsDirty(false)
      } else if (shouldSaveSettings) {
        const item = await api.updateConversation(target, settingsPatch(selectedSetup))
        shouldSaveSettings = false
        if (alive.current) { conversation.setData(item); setSettingsDirty(false) }
      }
      const result = await api.submitMessage(target, content, selectedMode, key, selectedMode === 'chat' ? researchContextRunID : '')
      submissionKey.current = null
      if (!alive.current) return
      if (!id) { void onChanged(); onSelect(target); return }
      accept(result)
      setOptimistic([])
      setText('')
    })
  }

  function operate(operation: 'retry' | 'continue') {
    if (!currentRun || active || busy) return
    const target = currentRun.id
    const identifier = `${target}:${operation}`
    if (runActionKey.current?.operation !== identifier) runActionKey.current = { operation: identifier, key: crypto.randomUUID() }
    const key = runActionKey.current.key
    void action(async () => {
      const result = await (operation === 'retry' ? api.retryRun(target, key) : api.continueRun(target, key))
      runActionKey.current = null
      accept(result)
    })
  }

  function changeMode(next: ConversationMode) {
    submissionKey.current = null
    if (!id) { setMode(next); if (next === 'research') setResearchContextRunID(''); return }
    void action(async () => {
      const result = await api.updateConversation(id, { mode: next })
      if (!alive.current) return
      conversation.setData(result); setMode(next)
      if (next === 'research') { setResearchContextRunID(''); submissionKey.current = null }
      void onChanged()
    })
  }

  function moveToSpace(nextSpaceID: string) {
    if (!id || active || busy || nextSpaceID === (conversation.data?.space_id || '')) return
    void action(async () => {
      const updated = await api.updateConversation(id, { space_id: nextSpaceID })
      if (!alive.current) return
      conversation.setData(updated)
      void onChanged()
    })
  }

  function enableResearchContext() {
    if (!currentRun || !id || !researchContextReady) return
    const sourceRunID = currentRun.id
    void action(async () => {
      const updated = await api.updateConversation(id, { mode: 'chat' })
      if (!alive.current) return
      conversation.setData(updated); setMode('chat'); setDetails(false); setResearchContextRunID(sourceRunID); submissionKey.current = null; void onChanged()
    })
  }

  function stopRun() {
    void action(async () => {
      const next = await api.cancelRun(runID)
      if (alive.current) { run.setData(next); scheduleSync(['messages', 'conversation', 'run', 'runtime'], 'user_action'); void onChanged() }
    })
  }

  function saveSettings() {
    const problem = setupProblem(setup)
    if (problem) { setError(problem); return }
    if (!id || active) return
    void action(async () => {
      const item = await api.updateConversation(id, settingsPatch(setup))
      if (alive.current) { conversation.setData(item); setSettingsDirty(false) }
    })
  }

  function changeSettings(value: ConversationSetup) {
    setSetup(value)
    setSettingsDirty(true)
    // A failed operation retains its original draft for idempotent retry. Once
    // the settings draft changes, that old closure must not overwrite it.
    setActionRetry(null)
  }

  function requestDiagnostics() {
    if (diagnosticsRequested) void doctor.reload('user_action')
    else setDiagnosticsRequested(true)
  }

  const status = currentRun ? currentRun.status === 'paused' ? labels.paused : terminal(currentRun.status) ? terminalLabels[currentRun.terminal_outcome || ''] || labels[currentRun.status] : labels[currentRun.phase || currentRun.stage || ''] || labels[currentRun.status] || currentRun.status : 'Готов к сообщению'
  const content = [...transcript.messages.filter((message) => ['user', 'assistant'].includes(message.role)), ...optimistic]
  const latestAssistant = [...content].reverse().find((message) => message.role === 'assistant' && message.run_id === currentRun?.id)
  const hasPartial = !!latestAssistant?.content.trim()
  useEffect(() => {
    if (!currentRun) return
    if (readingRun.current.id !== currentRun.id) readingRun.current = { id: currentRun.id, openSaved: currentRun.mode === 'research' && (terminal(currentRun.status) || currentRun.status === 'paused'), positioned: false }
    if (!readingRun.current.openSaved || readingRun.current.positioned || !latestAssistant) return
    readingRun.current.positioned = true
    setFollow(false)
    if (centerRef.current) centerRef.current.scrollTop = 0
    if (listRef.current) {
      const article = [...listRef.current.querySelectorAll<HTMLElement>('article[data-message-id]')].find((element) => element.dataset.messageId === latestAssistant.id)
      listRef.current.scrollTop = !window.matchMedia('(max-width: 48rem)').matches && latestAssistant.content.trim() && article
        ? listRef.current.scrollTop + article.getBoundingClientRect().top - listRef.current.getBoundingClientRect().top
        : 0
    }
  }, [currentRun?.id, currentRun?.mode, currentRun?.status, latestAssistant?.id, latestAssistant?.content])

  function readAnswer(focus = true) {
    const article = [...(listRef.current?.querySelectorAll<HTMLElement>('article[data-message-id]') ?? [])].find((element) => element.dataset.messageId === latestAssistant?.id)
    if (!article) return
    setFollow(false)
    article.scrollIntoView({ block: 'start', inline: 'nearest', behavior: 'instant' })
    if (focus) article.focus({ preventScroll: true })
  }
  const terminalFailure = publicResearchEvents(transcript.stream.events).filter((event) => event.task_id === currentRun?.id && event.type === 'task.failed').at(-1)
  const failurePayload = terminalFailure?.payload
  const failureMessage = typeof failurePayload?.message === 'string' ? failurePayload.message.slice(0, 1000) : ''
  const verificationSummary = typeof failurePayload?.verification_summary === 'string' ? failurePayload.verification_summary.slice(0, 1000) : ''
  const missingCriteria = Array.isArray(failurePayload?.missing) ? failurePayload.missing.filter((item): item is string => typeof item === 'string').slice(0, 20) : []
  const research = useResearch(runID, details && mode === 'research', currentRun?.mode === 'research')
  const researchContextReady = canUseResearchContext(currentRun, latestAssistant, research.evidence.data ?? [])
  const scheduleFallback = useCallback(() => {
    if (fallbackTimerRef.current !== undefined) return
    fallbackTimerRef.current = window.setTimeout(() => {
      fallbackTimerRef.current = undefined
      if (!alive.current) return
      // Background tabs do not need a polling fallback. Keep the SSE cursor
      // intact and reconcile once the tab becomes visible again.
      if (pageHiddenRef.current) return
      const latestRun = currentRunRef.current
      if (latestRun && terminal(latestRun.status)) { closeEventStreamRef.current(); setConnection('terminal'); return }
      setConnection('degraded')
      const resources = ['messages', 'run', 'runtime', 'conversation', 'events']
      if (details && mode === 'research') resources.push('plan', 'sources', 'evidence')
      syncSchedulerRef.current(resources, 'fallback_poll')
      fallbackDelayRef.current = nextFallbackDelay(fallbackDelayRef.current)
      fallbackSchedulerRef.current()
    }, fallbackDelayRef.current)
  }, [details, mode])
  fallbackSchedulerRef.current = scheduleFallback
  useEffect(() => {
    const handleVisibility = () => {
      pageHiddenRef.current = document.hidden
      if (document.hidden || !runID) return
      fallbackDelayRef.current = 15000
      const latestRun = currentRunRef.current
      if (latestRun && terminal(latestRun.status)) return
      // A hidden tab may have missed resource refreshes while the browser
      // throttled timers. Reconcile canonical state on return without
      // forcing a second EventSource connection.
      const resources = ['messages', 'run', 'runtime', 'conversation', 'events']
      if (details && mode === 'research') resources.push('plan', 'sources', 'evidence')
      syncSchedulerRef.current(resources, 'fallback_poll')
    }
    document.addEventListener('visibilitychange', handleVisibility)
    return () => document.removeEventListener('visibilitychange', handleVisibility)
  }, [details, mode, runID])
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
    for (const type of [...eventTypes, 'research.round.assessed', 'research.round.stopped', 'report.section.updated']) source.addEventListener(type, receive)
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
  const bridgeMessage = transcript.messages.find((message) => message.id === currentRun?.user_message_id) ?? null
  const bridgeReferences = explicitResearchContextReferences(bridgeMessage, id)
  const references = useMemo(() => currentRun?.mode === 'chat' && bridgeReferences.length ? bridgeReferences : citationReferences(research.evidence.data ?? []), [currentRun?.mode, bridgeReferences.length, bridgeMessage, research.evidence.data])
  const flushSync = useCallback((resources: string[], syncReason: string) => {
    for (const resource of resources) {
      if (resource === 'messages') void snapshots.reload(syncReason as ReloadReason)
      else if (resource === 'conversation') void conversation.reload(syncReason as ReloadReason)
      else if (resource === 'run') void run.reload(syncReason as ReloadReason)
      else if (resource === 'runtime') void runtime.reload(syncReason as ReloadReason)
      else if (resource === 'events') void history.reload(syncReason as ReloadReason)
      else if (resource === 'plan' && details && mode === 'research') void research.plan.reload(syncReason as ReloadReason)
      else if (resource === 'sources' && details && mode === 'research') void research.sources.reload(syncReason as ReloadReason)
      else if (resource === 'evidence' && currentRunRef.current?.mode === 'research') void research.evidence.reload(syncReason as ReloadReason)
      else if (resource === 'providers' && details && mode === 'research') void research.providers.reload(syncReason as ReloadReason)
    }
  }, [conversation.reload, details, mode, research.evidence.reload, research.plan.reload, research.providers.reload, research.sources.reload, run.reload, runtime.reload, snapshots.reload, history.reload])
  syncFlushRef.current = flushSync
  if (!coalescedSyncRef.current) coalescedSyncRef.current = createCoalescedScheduler((resources, reason) => syncFlushRef.current(resources, reason), 300)
  const scheduleSync = useCallback((resources: string[], reason: ReloadReason) => coalescedSyncRef.current?.schedule(resources, reason), [])
  syncSchedulerRef.current = scheduleSync
  const terminalSyncRef = useRef('')
  useEffect(() => {
    if (!currentRun || !terminal(currentRun.status)) { terminalSyncRef.current = ''; return }
    const key = `${currentRun.id}:${currentRun.status}`
    if (terminalSyncRef.current === key) return
    terminalSyncRef.current = key
    // A terminal run may be discovered by polling/snapshot after history was
    // fetched. Always reconcile canonical text and diagnostics once more.
    syncSchedulerRef.current(runResyncResources(currentRun.mode), 'terminal')
  }, [currentRun?.id, currentRun?.status, currentRun?.mode])
  useEffect(() => () => coalescedSyncRef.current?.cancel(), [])
  useEffect(() => {
    if (!transcript.stream.needsSync) return
    const resources = ['messages', 'run', 'runtime']
    if (mode === 'research') resources.push('plan', 'sources', 'evidence')
    scheduleSync(resources, 'sse_resync')
  }, [transcript.stream.needsSync, mode, scheduleSync])
  const backendReady = health.data?.status === 'ok' && health.data.database === 'ok' && !health.error
  const modelReady = doctor.data?.checks?.llm?.status === 'ok'
  const currentRuntime = runtime.data?.task_id === currentRun?.id ? runtime.data ?? null : null
  const pauseRun = () => void action(async () => { await api.pause(runID); await run.reload('user_action'); await runtime.reload('user_action') })
  const resumeRun = () => void action(async () => { await api.resume(runID); await run.reload('user_action'); await runtime.reload('user_action') })
  const detailsContent = <ResearchInspector resources={{ ...research, runtime, pdfCapability: doctor.data?.pdf_extractor }} events={transcript.stream.events} run={currentRun} showHeading={!mobile} busy={busy} onRetry={() => operate('retry')} onPause={pauseRun} onResume={resumeRun} onDiagnostics={requestDiagnostics} diagnosticsLoading={diagnosticsRequested && doctor.loading} diagnosticsError={doctor.error} />

  return <main className={`chat-main ${details ? 'chat-has-details' : ''}`}>
    <header className="chat-header"><button className="chat-mobile-menu" type="button" aria-label="Открыть разговоры" onClick={onSidebar}>☰</button><div className="chat-heading">{editing ? <form onSubmit={(event) => { event.preventDefault(); if (title.trim()) void action(async () => { const next = await api.updateConversation(id, { title: title.trim() }); if (alive.current) { conversation.setData(next); setEditing(false); void onChanged() } }) }}><input aria-label="Название разговора" value={title} maxLength={200} onChange={(event) => setTitle(event.target.value)} autoFocus /><button type="submit" disabled={busy || !title.trim()}>Сохранить</button><button type="button" onClick={() => setEditing(false)}>Отмена</button></form> : <><h1>{conversation.data?.title || 'Новый разговор'}</h1><p>{mode === 'research' ? 'Deep Research' : 'Локальный чат'}<span aria-hidden="true"> · </span>{status}</p></>}</div><div className="chat-header-actions">{id && <><label className="chat-space-picker"><span>Пространство</span><select aria-label="Пространство разговора" value={conversation.data?.space_id || ''} disabled={busy || active || spaces.loading} onChange={(event) => moveToSpace(event.target.value)}><option value="">Без пространства</option>{(spaces.data || []).map((item) => <option key={item.id} value={item.id}>{item.name || item.title}</option>)}</select></label><button type="button" onClick={() => setEditing(true)} disabled={busy} aria-label="Переименовать разговор">Название</button><button type="button" onClick={() => void action(async () => { await api.deleteConversation(id); if (alive.current) { void onChanged(); onSelect('') } })} disabled={busy || active} title={active ? 'Сначала остановите текущую генерацию' : 'Удалить разговор'}>Удалить</button></>}<button type="button" aria-expanded={details} onClick={() => setDetails((value) => !value)}>Детали</button></div></header>
    <div className="chat-body"><section className="chat-center" ref={centerRef} onScroll={(event) => { if (window.matchMedia('(max-width: 48rem)').matches) observeScroll(event.currentTarget) }} aria-label="Текущий разговор"><div className="chat-health"><span className={backendReady ? 'chat-online' : ''}>Приложение: {health.error ? 'нет связи' : backendReady ? 'доступно' : 'проверяем соединение'}</span><span className={modelReady && !doctor.error ? 'chat-online' : ''}>Qwen: {doctor.error ? 'проверка недоступна' : modelReady ? 'проверена по запросу' : health.data?.model?.status === 'configured' ? 'локальная · проверка по запросу' : 'доступность не проверена'}</span></div>
      <ResourceStatus resource={conversation} /><ResourceStatus resource={snapshots} />{runID && <ResourceStatus resource={run} />}{history.error && <ResourceStatus resource={history} />}
      <ResearchOverview run={currentRun} runtime={currentRuntime} events={transcript.stream.events} answer={latestAssistant ?? null} evidence={research.evidence} busy={busy} onDetails={() => setDetails(true)} onReadAnswer={() => readAnswer()} onPause={pauseRun} onResume={resumeRun} onStop={stopRun} />
      <div className="chat-transcript" ref={listRef} onScroll={(event) => { if (!window.matchMedia('(max-width: 48rem)').matches) observeScroll(event.currentTarget) }}>
        <ChatTranscript messages={content} loading={snapshots.loading} active={active} runID={runID} run={currentRun} references={references} evidenceCache={evidenceCache} submissionState={optimistic.length ? busy ? 'sending' : error ? 'failed' : 'sending' : undefined} onExample={(nextMode, prompt) => { setMode(nextMode); setText(prompt) }} />
      </div>
      {!follow && <button type="button" className="chat-to-bottom" onClick={() => setFollow(true)}>К последнему сообщению ↓</button>}
      {currentRun?.status === 'failed' && <div className="chat-action-error" role="alert"><strong>{currentRun.terminal_outcome === 'blocked' ? 'Ответ заблокирован проверкой' : 'Не удалось завершить ответ'}</strong><p>{failureMessage || currentRun.error_message || 'Можно повторить запрос.'}{currentRun.error_code ? ` Код: ${currentRun.error_code}` : typeof failurePayload?.error_code === 'string' ? ` Код: ${failurePayload.error_code}` : ''}</p><FailureDetails payload={failurePayload} />{verificationSummary && <p>{verificationSummary}</p>}{missingCriteria.length > 0 && <div className="chat-failure-missing"><strong>Не хватает критических критериев</strong><ul>{missingCriteria.map((item, index) => <li key={`${index}-${item}`}>{item}</li>)}</ul></div>}</div>}
      {currentRun?.status === 'cancelled' && <p className="chat-muted" role="status">Генерация остановлена. {hasPartial ? 'Полученный текст сохранён.' : 'Текст ответа не сформирован.'}</p>}
      {currentRun?.status === 'waiting_approval' && <p role="status">Нужно ваше решение. <a href="#/workspace">Открыть подтверждения в рабочем пространстве</a></p>}
      {error && <div className="chat-action-error" role="alert"><p>{error}</p>{actionRetry && <button type="button" onClick={actionRetry} disabled={busy}>Повторить действие</button>}</div>}
      {currentRun && !active && <div className="chat-answer-actions"><button type="button" disabled={busy} onClick={() => operate('retry')}>Повторить ответ</button><button type="button" disabled={busy || !hasPartial} title={!hasPartial ? 'Нет текста для продолжения' : 'Продолжить ответ новым запуском'} onClick={() => operate('continue')}>Продолжить ответ</button></div>}
        <ChatComposer mode={mode} privacyMode={currentRuntime?.privacy_mode || configuration.data?.research_limits?.privacy_mode} contextBudget={currentRuntime?.context_budget || doctor.data?.context_budget} onModeChange={changeMode} text={text} onTextChange={setText} onSend={send} busy={busy} active={active} run={currentRun} onStop={stopRun} researchContextRunID={researchContextRunID} researchContextReady={researchContextReady} onEnableResearchContext={enableResearchContext} onClearResearchContext={() => { setResearchContextRunID(''); submissionKey.current = null }} settings={setup} onSettingsChange={optimistic.length ? undefined : changeSettings} settingsError={setupProblem(setup)} settingsDirty={settingsDirty} settingsSaving={busy && settingsDirty} onSaveSettings={id && !active ? saveSettings : undefined} />
      <div className="chat-composer-note">{active && <span>Новый запрос доступен после завершения или остановки.</span>}{runID && <span role="status">{connection === 'live' ? 'Поток подключён' : connection === 'connecting' ? 'Подключение…' : connection === 'reconnecting' ? 'Переподключение…' : connection === 'degraded' ? 'Резервная синхронизация' : connection === 'invalid' ? 'Ошибка потока' : connection === 'terminal' ? 'Поток завершён' : 'Нет активного потока'}</span>}{active && <button type="button" onClick={() => { recordReload('chat.stream', 'manual_retry'); setReconnect((value) => value + 1) }}>Переподключить</button>}</div>
    </section>{details && !mobile && <aside className="chat-research-desktop" aria-label="Детали исследования">{detailsContent}</aside>}</div>{details && mobile && <Drawer title="Детали исследования" onClose={() => setDetails(false)}>{detailsContent}</Drawer>}
  </main>
}

function useResearch(runID: string, enabled: boolean, evidenceEnabled = enabled) {
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
