import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import { api } from './api'
import { createCoalescedScheduler } from './chat-sync.mjs'
import { recordReload, recordSSE, type ReloadReason } from './network-debug'
import { legacyDraft } from './conversation-state.mjs'
import { createTaskDetailTracker } from './task-detail.mjs'
import { applyWorkspaceDelta, fullWorkspaceResources, workspaceEventResources } from './workspace-sync.mjs'
import WorkspaceBoard from './workspace/WorkspaceBoard'
import { ProviderCard } from './diagnostic-ui.mjs'
import type { AgentEvent, Approval, Artifact, Checkpoint, DraftMessage, Evidence, HealthResponse, InferenceTelemetry, Message, MessageDeltaPayload, ProvidersResponse, RunRuntime, RuntimeTelemetry, Task, TaskStatus, TaskStep } from './types'
import { mergeRuntimeSnapshot } from './telemetry-state.mjs'
import AppShell, { TopBar } from './AppShell'
import { Icon } from './icons'
import { Badge, Button, EmptyState, IconButton, Input, plural, Textarea } from './ui'
import './styles/workspace.css'
import { columns, eventLabels, stageLabels, statusLabels, timeLabel } from './workspace/labels'
import TaskInspector from './workspace/TaskInspector'
import MemoryManager from './workspace/MemoryManager'
import TaskCommandPalette from './workspace/TaskCommandPalette'

type DetailResource = 'task' | 'messages' | 'events' | 'checkpoints' | 'steps' | 'artifacts' | 'evidence' | 'approvals' | 'runtime'
type DetailSelection = Partial<Record<DetailResource, boolean>>

interface DoctorResponse {
  status: string
  checks: Record<string, { status: string; message: string }>
}

export default function WorkspaceRoute() {
  const [tasks, setTasks] = useState<Task[]>([])
  const [selectedID, setSelectedID] = useState('')
  const [detailTracker] = useState(createTaskDetailTracker)
  const [messages, setMessages] = useState<Message[]>([])
  const savedMessageIDs = useRef<string[]>([])
  const [draft, setDraft] = useState<DraftMessage | null>(null)
  const [events, setEvents] = useState<AgentEvent[]>([])
  const seenEventSeq = useRef(new Set<number>())
  const lastEventSeq = useRef(0)
  const [detailLoadedID, setDetailLoadedID] = useState('')
  const [checkpoints, setCheckpoints] = useState<Checkpoint[]>([])
  const [steps, setSteps] = useState<TaskStep[]>([])
  const [artifacts, setArtifacts] = useState<Artifact[]>([])
  const [evidence, setEvidence] = useState<Evidence[]>([])
  const [approvals, setApprovals] = useState<Approval[]>([])
  const [runtime, setRuntime] = useState<RunRuntime | null>(null)
  const [doctor, setDoctor] = useState<DoctorResponse | null>(null)
  const [health, setHealth] = useState<HealthResponse | null>(null)
  const [privacyMode, setPrivacyMode] = useState('')
  const [diagnosticsBusy, setDiagnosticsBusy] = useState(false)
  const [diagnosticsError, setDiagnosticsError] = useState('')
  const [createOpen, setCreateOpen] = useState(false)
  const [createTitle, setCreateTitle] = useState('')
  const [createDescription, setCreateDescription] = useState('')
  const [chatText, setChatText] = useState('')
  const [busy, setBusy] = useState(false)
  const [refreshing, setRefreshing] = useState(false)
  const [connection, setConnection] = useState('idle')
  const [providers, setProviders] = useState<ProvidersResponse | null>(null)
  const [providersError, setProvidersError] = useState('')
  const [error, setError] = useState('')
  const [runtimeError, setRuntimeError] = useState('')
  const [detailError, setDetailError] = useState('')
  const detailFailures = useRef(new Set<DetailResource>())
  const [paletteOpen, setPaletteOpen] = useState(false)
  const [memoryOpen, setMemoryOpen] = useState(false)
  const [pageHidden, setPageHidden] = useState(document.hidden)
  const draftRef = useRef(draft)
  draftRef.current = draft
  const selectedTask = tasks.find((task) => task.id === selectedID)
  const selectedTaskRef = useRef(selectedTask)
  selectedTaskRef.current = selectedTask
  const connectionRef = useRef(connection)
  connectionRef.current = connection
  const workspaceQueueRef = useRef<(resources: string[], reason: ReloadReason) => void>(() => {})
  const workspaceFlushRef = useRef<(resources: string[], reason: ReloadReason) => void>(() => {})
  const workspaceCoalescerRef = useRef<ReturnType<typeof createCoalescedScheduler> | null>(null)

  const refreshTasks = useCallback(async (quiet = false, reason: ReloadReason = quiet ? 'fallback_poll' : 'user_action') => {
    recordReload('workspace.tasks', reason)
    if (!quiet) setRefreshing(true)
    try {
      const items = await api.tasks()
      setTasks(items)
      setSelectedID((current) => current || items[0]?.id || '')
      setError('')
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : 'Не удалось загрузить задачи')
    } finally {
      setRefreshing(false)
    }
  }, [])

  const refreshDetail = useCallback(async (
    id: string,
    selection: DetailSelection = Object.fromEntries(fullWorkspaceResources().filter((resource) => resource !== 'tasks').map((resource) => [resource, true])) as DetailSelection,
    reason: ReloadReason = 'user_action',
  ) => {
    const requested = (Object.entries(selection) as [DetailResource, boolean][]).filter(([, enabled]) => enabled).map(([resource]) => resource)
    if (!requested.length) return
    recordReload('workspace.detail', reason)
    const jobs: Promise<void>[] = []
    const completeFull = requested.length === 9 ? detailTracker.beginRequest(id, 'detail-ready') : () => false
    const load = <T,>(resource: DetailResource, request: Promise<T>, apply: (value: T) => void, onError?: () => void) => {
      const isCurrent = detailTracker.beginRequest(id, resource)
      jobs.push(request.then((value) => {
        if (!isCurrent()) return
        detailFailures.current.delete(resource)
        apply(value)
      }).catch(() => {
        if (!isCurrent()) return
        if (resource === 'runtime') onError?.()
        else detailFailures.current.add(resource)
      }))
    }
    if (selection.task) load('task', api.task(id), (task) => setTasks((current) => current.map((item) => item.id === task.id ? task : item)))
    if (selection.messages) load('messages', api.messages(id), (items) => {
      savedMessageIDs.current = items.map((message) => message.id)
      setMessages(items)
      setDraft((current) => current && savedMessageIDs.current.includes(current.message_id) ? null : current)
    })
    if (selection.events) load('events', api.events(id), (items) => {
      const visible = items.filter((event) => event.type !== 'message.delta')
      setEvents(visible)
      seenEventSeq.current = new Set(visible.map((event) => event.seq))
      lastEventSeq.current = visible.reduce((maximum, event) => Math.max(maximum, event.seq), 0)
      const restored = legacyDraft(items, savedMessageIDs.current)
      if (restored) setDraft((current) => current && current.seq > restored.seq ? current : restored)
    })
    if (selection.checkpoints) load('checkpoints', api.checkpoints(id), setCheckpoints)
    if (selection.steps) load('steps', api.steps(id), setSteps)
    if (selection.artifacts) load('artifacts', api.artifacts(id), setArtifacts)
    if (selection.evidence) load('evidence', api.evidence(id), setEvidence)
    if (selection.approvals) load('approvals', api.approvals(id), setApprovals)
    if (selection.runtime) load('runtime', api.runRuntime(id), (item) => {
      setRuntime((current) => mergeRuntimeSnapshot(current, item))
      setRuntimeError('')
    }, () => setRuntimeError('Снимок runtime временно недоступен.'))
    await Promise.all(jobs)
    setDetailError([...detailFailures.current].some((resource) => resource !== 'runtime') ? 'Часть сведений задачи недоступна. Загруженные данные сохранены; обновите задачу для повтора.' : '')
    if (completeFull()) setDetailLoadedID(id)
  }, [detailTracker])

  const flushWorkspace = useCallback((resources: string[], reason: ReloadReason) => {
    if (resources.includes('tasks')) void refreshTasks(true, reason)
    if (!selectedID) return
    const selection: DetailSelection = {}
    for (const resource of resources) if (resource !== 'tasks' && ['task', 'messages', 'events', 'checkpoints', 'steps', 'artifacts', 'evidence', 'approvals', 'runtime'].includes(resource)) selection[resource as DetailResource] = true
    if (Object.keys(selection).length) void refreshDetail(selectedID, selection, reason)
  }, [refreshDetail, refreshTasks, selectedID])
  workspaceFlushRef.current = flushWorkspace
  if (!workspaceCoalescerRef.current) workspaceCoalescerRef.current = createCoalescedScheduler((resources, reason) => workspaceFlushRef.current(resources, reason), 300)
  const queueWorkspaceRefresh = useCallback((resources: string[], reason: ReloadReason) => workspaceCoalescerRef.current?.schedule(resources, reason), [])
  workspaceQueueRef.current = queueWorkspaceRefresh
  useEffect(() => () => workspaceCoalescerRef.current?.cancel(), [])

  useEffect(() => {
    void refreshTasks(false, 'initial')
  }, [refreshTasks])

  useEffect(() => {
    const update = () => setPageHidden(document.hidden)
    document.addEventListener('visibilitychange', update)
    return () => document.removeEventListener('visibilitychange', update)
  }, [])

  useEffect(() => {
    if (pageHidden) return
    const hasActiveWork = tasks.some((task) => ['queued', 'planning', 'running', 'verifying'].includes(task.status))
    const selectedHasLiveStream = !!selectedTask && ['queued', 'planning', 'running', 'verifying'].includes(selectedTask.status) && connection === 'live'
    if (!hasActiveWork || selectedHasLiveStream) return
    const timer = window.setInterval(() => void refreshTasks(true, 'fallback_poll'), 30000)
    return () => window.clearInterval(timer)
  }, [connection, pageHidden, refreshTasks, selectedTask?.id, selectedTask?.status, tasks])

  useLayoutEffect(() => {
    detailTracker.select(selectedID)
    detailFailures.current.clear()
    setMessages([])
    savedMessageIDs.current = []
    setDraft(null)
    setEvents([])
    seenEventSeq.current = new Set()
    lastEventSeq.current = 0
    setDetailLoadedID('')
    setCheckpoints([])
    setSteps([])
    setArtifacts([])
    setEvidence([])
    setApprovals([])
    setRuntime(null)
    setRuntimeError('')
    setDetailError('')
    return () => detailTracker.select('')
  }, [selectedID, detailTracker])

  useEffect(() => {
    if (!selectedID) {
      detailTracker.observe('', undefined)
      setMessages([])
      setDraft(null)
      setEvents([])
      setCheckpoints([])
      setSteps([])
      setArtifacts([])
      setEvidence([])
      setApprovals([])
      setRuntime(null)
      setRuntimeError('')
      setDetailError('')
      return
    }
    if (detailTracker.observe(selectedID, selectedTask?.status)) void refreshDetail(selectedID, undefined, connectionRef.current === 'live' ? 'sse_event' : 'initial')
  }, [selectedID, selectedTask?.status, refreshDetail, detailTracker])

  useEffect(() => {
    setDraft(null)
  }, [selectedID])

  const refreshProviders = useCallback(async (reason: ReloadReason = 'manual_retry') => {
    recordReload('workspace.providers', reason)
    try { setProviders(await api.providers()); setProvidersError('') }
    catch { setProvidersError('Состояние провайдеров недоступно; показанные данные могут быть устаревшими.') }
  }, [])
  useEffect(() => { void refreshProviders('initial') }, [refreshProviders])

  useEffect(() => {
    if (!selectedID || detailLoadedID !== selectedID) return
    const task = selectedTaskRef.current
    if (task && ['completed', 'failed', 'cancelled'].includes(task.status)) {
      setConnection('idle')
      return
    }
    setConnection('connecting')
    const endpoint = `/api/tasks/${encodeURIComponent(selectedID)}/events/stream`
    const cursor = lastEventSeq.current
    const source = new EventSource(`${endpoint}?after=${cursor}`)
    let opened = false
    let reconnecting = false
    let closed = false
    const closeStream = () => { if (opened && !closed) { closed = true; recordSSE('close', endpoint) } }
    let disposed = false
    const eventTypes = [...new Set([...Object.keys(eventLabels), 'message.delta', 'task.recovered', 'source.updated', 'source.fetch.started', 'source.fetch.failed', 'source.failed', 'source.extracted', 'run.paused', 'run.resumed', 'run.resource_probe', 'run.blocked', 'run.failed'])]
    const listeners: [string, EventListener][] = eventTypes.map((type) => {
      const listener: EventListener = (rawEvent) => {
        if (disposed) return
        const event = rawEvent as MessageEvent<string>
        let parsed: AgentEvent
        try {
          parsed = JSON.parse(event.data) as AgentEvent
        } catch {
          setError('Получено событие в неизвестном формате')
          return
        }
        if (parsed.task_id !== selectedID) return
        if (!Number.isSafeInteger(parsed.seq) || parsed.seq <= 0 || seenEventSeq.current.has(parsed.seq)) return
        if (parsed.type === 'message.delta') {
          recordReload('workspace.draft', 'sse_event')
          const payload = parsed.payload as Partial<MessageDeltaPayload>
          const applied = applyWorkspaceDelta(draftRef.current, payload, parsed.seq)
          if (applied.needsSync) {
            draftRef.current = applied.draft
            setDraft(applied.draft)
            setError(applied.error === 'gap' ? 'Поток ответа потерял фрагмент; восстанавливаем сохранённый текст.' : 'Получен некорректный фрагмент ответа')
            workspaceQueueRef.current(['messages', 'events', 'runtime'], 'sse_resync')
            return
          }
          seenEventSeq.current.add(parsed.seq)
          lastEventSeq.current = Math.max(lastEventSeq.current, parsed.seq)
          draftRef.current = applied.draft
          setDraft(applied.draft)
          return
        }
        seenEventSeq.current.add(parsed.seq)
        lastEventSeq.current = Math.max(lastEventSeq.current, parsed.seq)
        setEvents((current) => current.some((item) => item.seq === parsed.seq) ? current : [...current, parsed].slice(-200))
        const terminal = ['task.completed', 'task.failed', 'task.cancelled'].includes(parsed.type)
        const payloadStatus = typeof parsed.payload.to === 'string' && ['inbox', 'queued', 'planning', 'running', 'waiting_approval', 'paused', 'verifying', 'completed', 'failed', 'cancelled'].includes(parsed.payload.to) ? parsed.payload.to as TaskStatus : undefined
        const terminalStatus: TaskStatus | undefined = parsed.type === 'task.completed' ? 'completed' : parsed.type === 'task.failed' ? 'failed' : parsed.type === 'task.cancelled' ? 'cancelled' : undefined
        const status = terminalStatus || payloadStatus
        if (status) detailTracker.observe(selectedID, status)
        const resources = workspaceEventResources(parsed)
        if (resources.length) workspaceQueueRef.current(resources, terminal ? 'terminal' : 'sse_event')
        if (terminal) {
          source.close()
          closeStream()
          setConnection('idle')
        }
      }
      source.addEventListener(type, listener)
      return [type, listener]
    })
    source.addEventListener('snapshot', (rawEvent) => {
      if (disposed) return
      try {
        const next = JSON.parse((rawEvent as MessageEvent<string>).data) as RunRuntime
        if (next.task_id !== selectedID || !Number.isSafeInteger(next.cursor)) return
        detailTracker.beginRequest(selectedID, 'runtime')
        setRuntime((previous) => mergeRuntimeSnapshot(previous, next, { replaceWarnings: true }))
        setRuntimeError('')
      } catch { setError('Не удалось прочитать снимок состояния задачи') }
    })
    source.addEventListener('runtime_snapshot', (rawEvent) => {
      if (disposed) return
      try {
        const update = JSON.parse((rawEvent as MessageEvent<string>).data) as { telemetry?: RuntimeTelemetry; telemetry_revision?: number }
        if (!update.telemetry || !Number.isSafeInteger(update.telemetry_revision) || update.telemetry_revision! < 0) return
        detailTracker.beginRequest(selectedID, 'runtime')
        setRuntime((previous) => {
          if (previous?.task_id !== selectedID || (previous.telemetry_revision ?? -1) > update.telemetry_revision!) return previous
          return { ...previous, telemetry: update.telemetry, telemetry_revision: update.telemetry_revision }
        })
        setRuntimeError('')
      } catch { setError('Не удалось прочитать снимок телеметрии') }
    })
    source.addEventListener('inference_progress', (rawEvent) => {
      if (disposed) return
      try {
        const update = JSON.parse((rawEvent as MessageEvent<string>).data) as { inference?: InferenceTelemetry; telemetry_revision?: number }
        if (!update.inference || !Number.isSafeInteger(update.telemetry_revision) || update.telemetry_revision! < 0) return
        detailTracker.beginRequest(selectedID, 'runtime')
        setRuntime((previous) => {
          if (previous?.task_id !== selectedID || !previous.telemetry || (previous.telemetry_revision ?? -1) > update.telemetry_revision!) return previous
          return { ...previous, telemetry_revision: update.telemetry_revision, telemetry: { ...previous.telemetry, inference: update.inference! } }
        })
        setRuntimeError('')
      } catch { setError('Не удалось прочитать прогресс инференса') }
    })
    source.addEventListener('resource_warning', (rawEvent) => {
      if (disposed) return
      try {
        const update = JSON.parse((rawEvent as MessageEvent<string>).data) as { warnings?: unknown }
        const warnings = Array.isArray(update.warnings) ? update.warnings.filter((item): item is string => typeof item === 'string').slice(0, 16) : []
        detailTracker.beginRequest(selectedID, 'runtime')
        setRuntime((previous) => previous?.task_id === selectedID ? { ...previous, runtime_warnings: warnings } : previous)
        setRuntimeError('')
      } catch { setError('Не удалось прочитать предупреждения runtime') }
    })
    source.addEventListener('resync', (event) => {
      try {
        const payload = JSON.parse((event as MessageEvent<string>).data) as { cursor?: number }
        if (!Number.isSafeInteger(payload.cursor) || !payload.cursor) throw new Error('invalid cursor')
        lastEventSeq.current = Math.max(lastEventSeq.current, payload.cursor)
        workspaceQueueRef.current(fullWorkspaceResources(), 'sse_resync')
      } catch { setError('Потерян фрагмент истории. Перезагрузите выбранную задачу.') }
    })
    source.onopen = () => { if (!opened) { opened = true; recordSSE('open', endpoint) }; reconnecting = false; setConnection('live') }
    source.onerror = () => { setConnection('reconnecting'); if (!reconnecting) { reconnecting = true; recordSSE('reconnect', endpoint) } }
    return () => {
      disposed = true
      for (const [type, listener] of listeners) source.removeEventListener(type, listener)
      source.close()
      closeStream()
    }
  }, [selectedID, detailLoadedID, detailTracker])

  useEffect(() => {
    let active = true
    const check = async (reason: ReloadReason) => {
      recordReload('workspace.configuration', reason)
      const [status, policy] = await Promise.allSettled([api.health(), api.labPolicy()])
      if (!active) return
      setHealth(status.status === 'fulfilled' ? status.value : null)
      setPrivacyMode(policy.status === 'fulfilled' ? policy.value?.research_limits?.privacy_mode ?? '' : '')
    }
    void check('initial')
    const timer = window.setInterval(() => void check('fallback_poll'), 60000)
    return () => { active = false; window.clearInterval(timer) }
  }, [])

  async function requestDiagnostics() {
    if (diagnosticsBusy) return
    setDiagnosticsBusy(true)
    setDiagnosticsError('')
    recordReload('workspace.doctor', 'user_action')
    try { setDoctor(await api.doctor()) }
    catch (caught) { setDiagnosticsError(caught instanceof Error ? caught.message : 'Не удалось выполнить проверку модели и поиска.') }
    finally { setDiagnosticsBusy(false) }
  }

  useEffect(() => {
    const handleKey = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault()
        setPaletteOpen((open) => !open)
      }
      if (event.key === 'Escape') setPaletteOpen(false)
      if (event.key === 'Escape') setMemoryOpen(false)
    }
    window.addEventListener('keydown', handleKey)
    return () => window.removeEventListener('keydown', handleKey)
  }, [])

  const counts = useMemo(() => ({
    total: tasks.length,
    active: tasks.filter((task) => ['planning', 'running', 'verifying'].includes(task.status)).length,
    approvals: tasks.filter((task) => task.status === 'waiting_approval').length,
  }), [tasks])

  async function createTask(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!createDescription.trim()) return
    setBusy(true)
    setError('')
    try {
      const task = await api.createTask(createTitle.trim(), createDescription.trim())
      setCreateTitle('')
      setCreateDescription('')
      setCreateOpen(false)
      setSelectedID(task.id)
      await refreshTasks(true, 'user_action')
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : 'Не удалось создать задачу')
    } finally {
      setBusy(false)
    }
  }

  async function runTask(taskID: string) {
    setBusy(true)
    setError('')
    try {
      await api.run(taskID)
      await refreshTasks(true, 'user_action')
      setSelectedID(taskID)
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : 'Не удалось запустить задачу')
    } finally {
      setBusy(false)
    }
  }

  async function controlTask(action: 'pause' | 'resume' | 'cancel', taskID: string) {
    setBusy(true)
    setError('')
    try {
      await api[action](taskID)
      await refreshTasks(true, 'user_action')
      await refreshDetail(taskID, undefined, 'user_action')
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : 'Не удалось изменить задачу')
    } finally {
      setBusy(false)
    }
  }

  async function decideApproval(action: 'approve' | 'reject', approvalID: string) {
    setBusy(true)
    setError('')
    try {
      await api[action](approvalID)
      if (selectedID) await refreshDetail(selectedID, undefined, 'user_action')
      await refreshTasks(true, 'user_action')
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : 'Не удалось записать решение')
    } finally {
      setBusy(false)
    }
  }

  async function sendMessage(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!selectedID || !chatText.trim()) return
    setBusy(true)
    setError('')
    try {
      await api.sendMessage(selectedID, chatText.trim())
      setChatText('')
      await refreshDetail(selectedID, undefined, 'user_action')
      await refreshTasks(true, 'user_action')
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : 'Не удалось отправить сообщение')
    } finally {
      setBusy(false)
    }
  }

  async function executeCommand(command: string) {
    setPaletteOpen(false)
    if (command === 'new') {
      setCreateOpen(true)
      window.setTimeout(() => document.querySelector<HTMLInputElement>('#new-task-title')?.focus(), 0)
    } else if (command === 'refresh') {
      await refreshTasks()
    } else if (command === 'active') {
      const active = tasks.find((task) => ['planning', 'running', 'verifying'].includes(task.status))
      if (active) setSelectedID(active.id)
    } else if (command === 'memories') {
      setMemoryOpen(true)
    } else if (command.startsWith('column:')) {
      document.getElementById(`column-${command.slice(7)}`)?.scrollIntoView({ behavior: 'smooth', block: 'start' })
    }
  }

  const modelCheck = doctor?.checks.llm
  const apiReady = health?.status === 'ok' && health.database === 'ok' && health.worker === 'attached'

  const summary = [`${counts.total} ${plural(counts.total, 'задача', 'задачи', 'задач')}`, counts.active ? `${counts.active} в работе` : '', counts.approvals ? `${counts.approvals} ${counts.approvals === 1 ? 'ждёт' : 'ждут'} решения` : ''].filter(Boolean).join(' · ')

  return (
    <AppShell activeSection="tasks" commandPalette={false} onMemory={() => setMemoryOpen(true)}>
      <main className="workspace-page" id="board">
        <TopBar title="Задачи" subtitle={summary} actions={<>
          <span className="workspace-health" title={modelCheck?.message ?? 'Проверка локальной модели не выполнена'}><Badge tone={apiReady ? 'success' : 'warning'}>{apiReady ? 'Сервис доступен' : 'Соединение не подтверждено'}</Badge></span>
          <IconButton label="Открыть командную палитру" title="Команды (⌘K)" icon={<Icon name="search" />} onClick={() => setPaletteOpen(true)} />
          <IconButton label={refreshing ? 'Обновляем' : 'Обновить'} icon={<Icon name="refresh" />} onClick={() => void refreshTasks()} disabled={refreshing} />
          <Button variant="primary" size="sm" icon={<Icon name="plus" />} onClick={() => setCreateOpen((open) => !open)}>Новая задача</Button>
        </>} />

        <div className={`workspace-layout ${selectedTask ? 'has-selection' : ''}`}>
          <div className="workspace-list">
            {(error || detailError) && <div className="workspace-notice" role="alert"><Icon name="alert" size={18} /><span>{error || detailError}</span><IconButton label="Закрыть сообщение" icon={<Icon name="close" />} onClick={() => { setError(''); setDetailError('') }} /></div>}

            {createOpen && (
              <form className="workspace-create" onSubmit={createTask}>
                <div className="workspace-create__heading"><h2>Новая задача</h2><IconButton label="Закрыть форму" icon={<Icon name="close" />} onClick={() => setCreateOpen(false)} /></div>
                <label className="ui-field" htmlFor="new-task-title">Название <small>необязательно</small></label>
                <Input id="new-task-title" value={createTitle} onChange={(event) => setCreateTitle(event.target.value)} placeholder="Коротко обозначьте результат" maxLength={200} />
                <label className="ui-field" htmlFor="new-task-description">Описание задачи</label>
                <Textarea id="new-task-description" value={createDescription} onChange={(event) => setCreateDescription(event.target.value)} placeholder="Опишите цель и ограничения" maxLength={20000} required rows={3} />
                <div className="workspace-create__footer"><span className="ui-muted">Задача сохранится в локальной базе.</span><Button variant="primary" type="submit" loading={busy} disabled={busy || !createDescription.trim()}>{busy ? 'Создаём…' : 'Создать задачу'}</Button></div>
              </form>
            )}

            <WorkspaceBoard columns={columns} tasks={tasks} selectedID={selectedID} statusLabels={statusLabels} stageLabels={stageLabels} onSelect={setSelectedID} formatTime={timeLabel} />
            {tasks.length === 0 && !error && <EmptyState icon={<Icon name="tasks" />} title="Задач пока нет" description="Создайте задачу, и агент выполнит её в фоне: план, события и результат появятся здесь." action={<Button variant="secondary" icon={<Icon name="plus" />} onClick={() => setCreateOpen(true)}>Новая задача</Button>} />}

            <details className="workspace-services"><summary>Сервисы и диагностика</summary><section aria-label="Поисковые провайдеры">
              <h2>Поисковые провайдеры</h2>
              <p className="ui-muted">Конфигурация и последнее реальное обращение. До первого запроса здоровье неизвестно.</p>
              <p className="ui-muted">{privacyMode === 'offline' ? 'Offline: поиск и чтение ограничены импортированным корпусом; сетевые источники отключены.' : privacyMode ? 'Модель работает локально; поисковые запросы передаются включённым внешним сервисам. Локальный SearXNG также обращается к публичным поисковикам.' : 'Режим приватности пока не загружен. Конфигурация провайдера не подтверждает его доступность.'}</p>
              {providersError && <p role="status">{providersError}</p>}
              {!providers && !providersError && <p className="ui-muted">Загрузка состояния…</p>}
              <div className="provider-grid">{Object.entries(providers?.search.providers ?? {}).map(([name, provider]) => <ProviderCard key={name} name={name} provider={provider} checkedLabel={provider.last_checked ? timeLabel(provider.last_checked) : ''} />)}</div>
              <div className="workspace-services__actions">
                <Button variant="secondary" size="sm" onClick={() => void refreshProviders('manual_retry')}>Обновить состояние провайдеров</Button>
                <Button variant="secondary" size="sm" loading={diagnosticsBusy} disabled={diagnosticsBusy} onClick={() => void requestDiagnostics()}>{diagnosticsBusy ? 'Проверяем…' : 'Проверить модель и поиск'}</Button>
              </div>
              <p className="ui-muted">Проверка по запросу может выполнить короткую генерацию и обращение к поиску.</p>
              {doctor && <p role="status">Последняя явная проверка: {doctor.status === 'ok' ? 'проверки пройдены' : doctor.status === 'warning' ? 'есть ограничения' : 'есть ошибки'}. Модель: {modelCheck?.status === 'ok' ? 'доступна на момент проверки' : 'доступность не подтверждена'}.</p>}
              {diagnosticsError && <p role="alert">{diagnosticsError}</p>}
            </section></details>
          </div>

          <div className="workspace-detail">
            {selectedTask ? (
              <TaskInspector
                task={selectedTask}
                messages={messages}
                draft={draft}
                events={events}
                checkpoints={checkpoints}
                steps={steps}
                artifacts={artifacts}
                evidence={evidence}
                approvals={approvals}
                runtime={runtime}
                runtimeError={runtimeError}
                connection={connection}
                busy={busy}
                chatText={chatText}
                setChatText={setChatText}
                onRun={() => void runTask(selectedTask.id)}
                onPause={() => void controlTask('pause', selectedTask.id)}
                onResume={() => void controlTask('resume', selectedTask.id)}
                onCancel={() => void controlTask('cancel', selectedTask.id)}
                onApprove={(id) => void decideApproval('approve', id)}
                onReject={(id) => void decideApproval('reject', id)}
                onRefresh={() => void refreshDetail(selectedTask.id, undefined, 'manual_retry')}
                onSend={sendMessage}
              />
            ) : tasks.length > 0 && <EmptyState icon={<Icon name="panel-right" />} title="Выберите задачу" description="План, сообщения и поток событий появятся здесь." />}
          </div>
        </div>

        {paletteOpen && <TaskCommandPalette tasks={tasks} onClose={() => setPaletteOpen(false)} onExecute={(command) => void executeCommand(command)} />}
        {memoryOpen && <MemoryManager onClose={() => setMemoryOpen(false)} />}
      </main>
    </AppShell>
  )
}
