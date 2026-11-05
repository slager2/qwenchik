import { useCallback, useEffect, useMemo, useReducer, useRef, useState, type FormEvent } from 'react'
import { api } from './api'
import { createCoalescedScheduler, nextFallbackDelay, runResyncResources } from './chat-sync.mjs'
import { createRunEvidenceCache } from './evidence-cache.mjs'
import { mergeRuntimeSnapshot } from './telemetry-state.mjs'
import ChatComposer from './ChatComposer'
import ChatTranscript from './ChatTranscript'
import { canUseResearchContext, citationReferences, explicitResearchContextReferences, createStreamState, terminal } from './conversation-state.mjs'
import ResearchInspector from './ResearchInspector'
import ResearchOverview from './ResearchOverview'
import AppShell, { TopBar } from './AppShell'
import { conversationSetup, defaultSetup, settingsPatch, setupProblem } from './research-settings.mjs'
import FailureDetails from './FailureDetails'
import { publicResearchEvents } from './research-state.mjs'
import ChatSidebar from './ChatSidebar'
import { recordReload, type ReloadReason } from './network-debug'
import type { AgentEvent, Conversation, ConversationMessage, ConversationMode, ConversationSetup, DoctorResponse, Run, RunRuntime, Submission } from './types'
import { failure, ResourceStatus, useRemote } from './hooks/useRemote'
import { useResearch } from './hooks/useResearch'
import { useRunEventStream } from './hooks/useRunEventStream'
import { transcriptReducer } from './hooks/transcript'
import { Button, Drawer, IconButton, Input } from './ui'
import { Icon } from './icons'
import './styles/chat.css'
import './styles/research.css'

const terminalLabels: Record<string, string> = { partial: 'Частичный результат', blocked: 'Заблокировано проверкой', budget_exhausted: 'Бюджет исчерпан' }
const labels: Record<string, string> = { queued: 'В очереди', planning: 'Планирование', running: 'В работе', verifying: 'Проверка', completed: 'Готово', failed: 'Не удалось завершить', cancelled: 'Остановлено', paused: 'На паузе', waiting_approval: 'Нужно решение', search: 'Поиск источников', searching: 'Поиск источников', fetch: 'Чтение источников', fetching: 'Чтение источников', extract: 'Извлечение текста', synthesis: 'Подготовка ответа', synthesizing: 'Подготовка ответа', verification: 'Сверка источников', finalization: 'Завершение', discovered: 'Найден', validated: 'Проверен', rejected: 'Отклонён', extracted: 'Текст извлечён', cited: 'Использован в ответе' }

function chatID() {
  const match = window.location.hash.match(/^#\/chat\/([^/?]+)/)
  try { return match ? decodeURIComponent(match[1]) : '' } catch { return '' }
}

export default function ChatRoute() {
  const [selected, setSelected] = useState(chatID)
  const [newVersion, setNewVersion] = useState(0)
  const conversations = useRemote(useCallback((signal) => api.conversations(signal), []), 30000, 'chat.conversations')
  useEffect(() => {
    const navigate = () => setSelected(chatID())
    window.addEventListener('hashchange', navigate)
    return () => window.removeEventListener('hashchange', navigate)
  }, [])
  const select = (id: string) => {
    if (!id) setNewVersion((value) => value + 1)
    window.location.hash = id ? `/chat/${encodeURIComponent(id)}` : '/chat'
    setSelected(id)
  }
  const navigation = <ChatSidebar conversations={conversations.data ?? []} error={conversations.error} loading={conversations.loading} selected={selected} onSelect={select} onRetry={() => void conversations.reload('manual_retry')} />
  const focusComposer = useCallback(() => document.getElementById('conversation-input')?.focus(), [])
  return <AppShell activeSection="chat" sidebar={navigation} onNewChat={() => select('')} composerFocus={focusComposer} navigationTitle="Разговоры" navigationLabel="Открыть разговоры"><div className="conversation-shell"><ConversationView key={selected || `new-${newVersion}`} id={selected} onSelect={select} onChanged={conversations.reload} /></div></AppShell>
}

function ConversationView({ id, onSelect, onChanged }: { id: string; onSelect: (id: string) => void; onChanged: () => Promise<void> }) {
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
  useRunEventStream(runID, reconnect, { dispatch, setConnection, setError, streamRef, modeRef, currentRunRef, connectionRef, runSetterRef, runtimeSetterRef, syncSchedulerRef, fallbackTimerRef, fallbackDelayRef, fallbackSchedulerRef, closeEventStreamRef })
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

  const connectionLabel = connection === 'live' ? 'Поток подключён' : connection === 'connecting' ? 'Подключение…' : connection === 'reconnecting' ? 'Переподключение…' : connection === 'degraded' ? 'Резервная синхронизация' : connection === 'invalid' ? 'Ошибка потока' : connection === 'terminal' ? 'Поток завершён' : 'Нет активного потока'
  const renameConversation = (event: FormEvent) => { event.preventDefault(); if (title.trim()) void action(async () => { const next = await api.updateConversation(id, { title: title.trim() }); if (alive.current) { conversation.setData(next); setEditing(false); void onChanged() } }) }
  const deleteConversation = () => void action(async () => { await api.deleteConversation(id); if (alive.current) { void onChanged(); onSelect('') } })
  const spacePicker = <label className="chat-space-picker" title="Проект разговора"><Icon name="projects" size={16} /><span className="sr-only">Пространство</span><select aria-label="Пространство разговора" value={conversation.data?.space_id || ''} disabled={busy || active || spaces.loading} onChange={(event) => moveToSpace(event.target.value)}><option value="">Без проекта</option>{(spaces.data || []).map((item) => <option key={item.id} value={item.id}>{item.name || item.title}</option>)}</select></label>
  const headerActions = <>
    {id && <>
      <span className="chat-header__inline">
        {spacePicker}
        <IconButton label="Переименовать разговор" icon={<Icon name="edit" />} onClick={() => setEditing(true)} disabled={busy} />
        <IconButton label="Удалить" title={active ? 'Сначала остановите текущую генерацию' : 'Удалить разговор'} icon={<Icon name="trash" />} onClick={deleteConversation} disabled={busy || active} />
      </span>
      <details className="chat-more" onToggle={(event) => { const menu = event.currentTarget; if (menu.open) { const close = (pointer: PointerEvent) => { if (!menu.contains(pointer.target as Node)) { menu.open = false; document.removeEventListener('pointerdown', close) } }; document.addEventListener('pointerdown', close) } }}>
        <summary aria-label="Действия с разговором" title="Действия с разговором"><Icon name="more" /></summary>
        <div className="chat-more__menu">
          {spacePicker}
          <Button variant="ghost" icon={<Icon name="edit" />} onClick={(event) => { (event.currentTarget.closest('details') as HTMLDetailsElement).open = false; setEditing(true) }} disabled={busy}>Переименовать</Button>
          <Button variant="ghost" icon={<Icon name="trash" />} onClick={deleteConversation} disabled={busy || active}>Удалить разговор</Button>
        </div>
      </details>
    </>}
    <IconButton label="Детали" title={details ? 'Скрыть детали' : 'Детали запуска и источники'} aria-expanded={details} className="chat-details-toggle" icon={<Icon name="panel-right" />} onClick={() => setDetails((value) => !value)} />
  </>

  return <main className={`chat-main ${details ? 'chat-has-details' : ''}`}>
    <TopBar className="chat-header" actions={headerActions}><div className="chat-heading">{editing
      ? <form onSubmit={renameConversation}><Input aria-label="Название разговора" value={title} maxLength={200} onChange={(event) => setTitle(event.target.value)} autoFocus /><Button type="submit" variant="primary" size="sm" disabled={busy || !title.trim()}>Сохранить</Button><Button size="sm" variant="ghost" onClick={() => setEditing(false)}>Отмена</Button></form>
      : <><h1>{conversation.data?.title || 'Новый разговор'}</h1><p>{mode === 'research' ? 'Deep Research' : 'Чат'}<span aria-hidden="true"> · </span>{status}</p></>}</div></TopBar>
    <div className="chat-body"><section className="chat-center" ref={centerRef} onScroll={(event) => { if (window.matchMedia('(max-width: 48rem)').matches) observeScroll(event.currentTarget) }} aria-label="Текущий разговор">
      <ResourceStatus resource={conversation} /><ResourceStatus resource={snapshots} />{runID && <ResourceStatus resource={run} />}{history.error && <ResourceStatus resource={history} />}
      <ResearchOverview run={currentRun} runtime={currentRuntime} events={transcript.stream.events} answer={latestAssistant ?? null} evidence={research.evidence} busy={busy} onDetails={() => setDetails(true)} onReadAnswer={() => readAnswer()} onPause={pauseRun} onResume={resumeRun} onStop={stopRun} />
      <div className="chat-transcript" ref={listRef} onScroll={(event) => { if (!window.matchMedia('(max-width: 48rem)').matches) observeScroll(event.currentTarget) }}>
        <ChatTranscript messages={content} loading={snapshots.loading} active={active} runID={runID} run={currentRun} references={references} evidenceCache={evidenceCache} submissionState={optimistic.length ? busy ? 'sending' : error ? 'failed' : 'sending' : undefined} onExample={(nextMode, prompt) => { setMode(nextMode); setText(prompt) }} />
        {currentRun?.status === 'failed' && <div className="chat-action-error" role="alert"><Icon name="alert" size={18} /><div><strong>{currentRun.terminal_outcome === 'blocked' ? 'Ответ заблокирован проверкой' : 'Не удалось завершить ответ'}</strong><p>{failureMessage || currentRun.error_message || 'Можно повторить запрос.'}{currentRun.error_code ? ` Код: ${currentRun.error_code}` : typeof failurePayload?.error_code === 'string' ? ` Код: ${failurePayload.error_code}` : ''}</p><FailureDetails payload={failurePayload} />{verificationSummary && <p>{verificationSummary}</p>}{missingCriteria.length > 0 && <div className="chat-failure-missing"><strong>Не хватает критических критериев</strong><ul>{missingCriteria.map((item, index) => <li key={`${index}-${item}`}>{item}</li>)}</ul></div>}</div></div>}
        {currentRun?.status === 'cancelled' && <p className="chat-notice" role="status">Генерация остановлена. {hasPartial ? 'Полученный текст сохранён.' : 'Текст ответа не сформирован.'}</p>}
        {currentRun?.status === 'waiting_approval' && <p className="chat-notice" role="status">Нужно ваше решение. <a href="#/workspace">Открыть подтверждения в рабочем пространстве</a></p>}
        {currentRun && !active && <div className="chat-answer-actions"><Button size="sm" variant="ghost" icon={<Icon name="refresh" size={16} />} disabled={busy} onClick={() => operate('retry')}>Повторить ответ</Button><Button size="sm" variant="ghost" icon={<Icon name="play" size={16} />} disabled={busy || !hasPartial} title={!hasPartial ? 'Нет текста для продолжения' : 'Продолжить ответ новым запуском'} onClick={() => operate('continue')}>Продолжить ответ</Button></div>}
      </div>
      {!follow && <IconButton className="chat-to-bottom" variant="secondary" label="К последнему сообщению" icon={<Icon name="arrow-down" />} onClick={() => setFollow(true)} />}
      {error && <div className="chat-action-error" role="alert"><Icon name="alert" size={18} /><div><p>{error}</p>{actionRetry && <Button size="sm" variant="secondary" onClick={actionRetry} disabled={busy}>Повторить действие</Button>}</div></div>}
      <ChatComposer mode={mode} privacyMode={currentRuntime?.privacy_mode || configuration.data?.research_limits?.privacy_mode} contextBudget={currentRuntime?.context_budget || doctor.data?.context_budget} onModeChange={changeMode} text={text} onTextChange={setText} onSend={send} busy={busy} active={active} run={currentRun} onStop={stopRun} researchContextRunID={researchContextRunID} researchContextReady={researchContextReady} onEnableResearchContext={enableResearchContext} onClearResearchContext={() => { setResearchContextRunID(''); submissionKey.current = null }} settings={setup} onSettingsChange={optimistic.length ? undefined : changeSettings} settingsError={setupProblem(setup)} settingsDirty={settingsDirty} settingsSaving={busy && settingsDirty} onSaveSettings={id && !active ? saveSettings : undefined} />
      <div className="chat-composer-note">
        <span className={backendReady ? 'chat-online' : 'chat-offline'}>Приложение: {health.error ? 'нет связи' : backendReady ? 'доступно' : 'проверяем соединение'}</span>
        <span className={modelReady && !doctor.error ? 'chat-online' : ''}>Qwen: {doctor.error ? 'проверка недоступна' : modelReady ? 'проверена по запросу' : health.data?.model?.status === 'configured' ? 'локальная · проверка по запросу' : 'доступность не проверена'}</span>
        {runID && <span role="status">{connectionLabel}</span>}
        {active && <span>Новый запрос — после завершения или остановки.</span>}
        {active && <Button size="sm" variant="ghost" onClick={() => { recordReload('chat.stream', 'manual_retry'); setReconnect((value) => value + 1) }}>Переподключить</Button>}
      </div>
    </section>{details && !mobile && <aside className="chat-research-desktop" aria-label="Детали исследования">{detailsContent}</aside>}</div>{details && mobile && <Drawer title="Детали исследования" className="chat-drawer" bodyClassName="chat-drawer-body" onClose={() => setDetails(false)}>{detailsContent}</Drawer>}
  </main>
}
