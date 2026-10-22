import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import { api } from './api'
import ChatShell from './ChatShell'
import { legacyDraft } from './conversation-state.mjs'
import { createTaskDetailTracker } from './task-detail.mjs'
import { diagnosticSummary, failureSummary, streamLabel } from './observability.mjs'
import { ProviderCard, TaskFailureCard, SourceList } from './diagnostic-ui.mjs'
import type { ProvidersResponse } from './types'
import type { AgentEvent, Approval, Artifact, Checkpoint, DraftMessage, Evidence, Memory, MemoryMatch, MemoryStatus, Message, MessageDeltaPayload, Task, TaskStatus, TaskStep } from './types'

const columns: { id: string; title: string; statuses: TaskStatus[] }[] = [
  { id: 'inbox', title: 'Входящие', statuses: ['inbox'] },
  { id: 'queued', title: 'В очереди', statuses: ['queued'] },
  { id: 'running', title: 'В работе', statuses: ['planning', 'running', 'verifying'] },
  { id: 'approval', title: 'Нужно решение', statuses: ['waiting_approval'] },
  { id: 'paused', title: 'На паузе', statuses: ['paused'] },
  { id: 'done', title: 'Завершено', statuses: ['completed'] },
  { id: 'failed', title: 'Сбой / отмена', statuses: ['failed', 'cancelled'] },
]

const statusLabels: Record<TaskStatus, string> = {
  inbox: 'Входящая',
  queued: 'В очереди',
  planning: 'Планирование',
  running: 'В работе',
  waiting_approval: 'Нужно решение',
  paused: 'На паузе',
  verifying: 'Проверка',
  completed: 'Завершена',
  failed: 'Сбой',
  cancelled: 'Отменена',
}

const stageLabels: Record<string, string> = {
  intake: 'Приём задачи',
  planning: 'Планирование',
  search: 'Поиск',
  fetch: 'Загрузка',
  extract: 'Извлечение',
  browser: 'Браузер',
  code_execution: 'Изолированное выполнение',
  synthesis: 'Анализ',
  verification: 'Проверка',
  finalization: 'Финализация',
}

const eventLabels: Record<string, string> = {
  'task.created': 'Задача создана',
  'task.status_changed': 'Изменён статус',
  'task.recovered': 'Работа восстановлена',
  'message.created': 'Новое сообщение',
  'plan.created': 'План сохранён',
  'step.started': 'Шаг начат',
  'step.completed': 'Шаг завершён',
  'step.skipped': 'Шаг пропущен по условию',
  'tool.started': 'Запущен инструмент',
  'tool.completed': 'Инструмент завершён',
  'tool.failed': 'Ошибка инструмента',
  'search.provider': 'Попытка поискового провайдера',
  'approval.requested': 'Требуется подтверждение',
  'approval.approved': 'Действие подтверждено',
  'approval.rejected': 'Действие отклонено',
  'approval.expired': 'Подтверждение истекло',
  'artifact.created': 'Артефакт сохранён',
  'evidence.created': 'Источник сохранён',
  'task.completed': 'Задача завершена',
  'task.failed': 'Задача завершилась с ошибкой',
  'task.cancelled': 'Задача отменена',
}

interface DoctorResponse {
  status: string
  checks: Record<string, { status: string; message: string }>
}

function timeLabel(value: string) {
  return new Intl.DateTimeFormat('ru-RU', { hour: '2-digit', minute: '2-digit', day: '2-digit', month: 'short' }).format(new Date(value))
}

function shortID(value: string) {
  return value.slice(0, 8)
}

function eventSummary(event: AgentEvent) {
  const payload = event.payload
  if (event.type === 'search.provider') return diagnosticSummary(payload)
  if (event.type === 'task.failed' || event.type === 'tool.failed') return failureSummary(payload)
  if (typeof payload.summary === 'string') return payload.summary
  if (typeof payload.description === 'string') return payload.description
  if (typeof payload.message === 'string') {
    const missing = Array.isArray(payload.missing) ? payload.missing.filter((item): item is string => typeof item === 'string') : []
    const verification = typeof payload.verification_summary === 'string' ? payload.verification_summary : ''
    return [payload.message, verification, missing.length ? `Не хватает: ${missing.join('; ')}` : ''].filter(Boolean).join(' ')
  }
  if (typeof payload.verification_summary === 'string') return payload.verification_summary
  if (typeof payload.error_code === 'string') return `Код: ${payload.error_code}`
  if (typeof payload.status === 'string') return `Статус: ${payload.status}`
  if (typeof payload.tool === 'string') return `Инструмент: ${payload.tool}`
  if (typeof payload.evidence_id === 'string') return `Источник: ${payload.evidence_id}`
  if (typeof payload.artifact_id === 'string') return `Файл: ${typeof payload.name === 'string' ? payload.name : payload.artifact_id}`
  if (typeof payload.steps === 'number') return `Шагов в плане: ${payload.steps}`
  return ''
}

function Workspace() {
  const [tasks, setTasks] = useState<Task[]>([])
  const [selectedID, setSelectedID] = useState('')
  const [detailTracker] = useState(createTaskDetailTracker)
  const [messages, setMessages] = useState<Message[]>([])
  const [draft, setDraft] = useState<DraftMessage | null>(null)
  const [events, setEvents] = useState<AgentEvent[]>([])
  const [checkpoints, setCheckpoints] = useState<Checkpoint[]>([])
  const [steps, setSteps] = useState<TaskStep[]>([])
  const [artifacts, setArtifacts] = useState<Artifact[]>([])
  const [evidence, setEvidence] = useState<Evidence[]>([])
  const [approvals, setApprovals] = useState<Approval[]>([])
  const [doctor, setDoctor] = useState<DoctorResponse | null>(null)
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
  const [paletteOpen, setPaletteOpen] = useState(false)
  const [memoryOpen, setMemoryOpen] = useState(false)
  const selectedTask = tasks.find((task) => task.id === selectedID)

  const refreshTasks = useCallback(async (quiet = false) => {
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

  const refreshDetail = useCallback(async (id: string) => {
    const isCurrent = detailTracker.beginRequest(id)
    let savedIDs: string[] = []
    async function load<T>(request: Promise<T>, apply: (value: T) => void) {
      const value = await request
      if (isCurrent()) apply(value)
    }
    const results = await Promise.allSettled([
      load(api.task(id), (task) => setTasks((current) => current.map((item) => item.id === task.id ? task : item))),
      load(api.messages(id), (items) => {
        savedIDs = items.map((message) => message.id)
        setMessages(items)
        setDraft((current) => current && savedIDs.includes(current.message_id) ? null : current)
      }),
      load(api.events(id), (items) => {
        setEvents(items.filter((event) => event.type !== 'message.delta'))
        const restored = legacyDraft(items, savedIDs)
        if (restored) setDraft((current) => current && current.seq > restored.seq ? current : restored)
      }),
      load(api.checkpoints(id), setCheckpoints),
      load(api.steps(id), setSteps),
      load(api.artifacts(id), setArtifacts),
      load(api.evidence(id), setEvidence),
      load(api.approvals(id), setApprovals),
    ])
    if (!isCurrent()) return
    if (results.some((result) => result.status === 'rejected')) setError('Часть сведений задачи недоступна. Загруженные данные сохранены; обновите задачу для повтора.')
  }, [detailTracker])

  useEffect(() => {
    void refreshTasks()
    const timer = window.setInterval(() => void refreshTasks(true), 4000)
    return () => window.clearInterval(timer)
  }, [refreshTasks])

  useLayoutEffect(() => {
    detailTracker.select(selectedID)
    setMessages([])
    setDraft(null)
    setEvents([])
    setCheckpoints([])
    setSteps([])
    setArtifacts([])
    setEvidence([])
    setApprovals([])
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
      return
    }
    if (detailTracker.observe(selectedID, selectedTask?.status)) void refreshDetail(selectedID)
  }, [selectedID, selectedTask?.status, refreshDetail, detailTracker])

  useEffect(() => {
    setDraft(null)
  }, [selectedID])

  useEffect(() => {
    let active = true
    const refresh = async () => {
      try {
        const result = await api.providers()
        if (active) { setProviders(result); setProvidersError('') }
      } catch {
        if (active) setProvidersError('Состояние провайдеров недоступно; показанные данные могут быть устаревшими.')
      }
    }
    void refresh()
    const timer = window.setInterval(() => void refresh(), 4000)
    return () => { active = false; window.clearInterval(timer) }
  }, [])

  useEffect(() => {
    if (!selectedID) return
    if (selectedTask && ['completed', 'failed', 'cancelled'].includes(selectedTask.status)) {
      setConnection('idle')
      return
    }
    setConnection('connecting')
    const source = new EventSource(`/api/tasks/${encodeURIComponent(selectedID)}/events/stream`)
    const eventTypes = [...Object.keys(eventLabels), 'message.delta']
    const listeners: [string, EventListener][] = eventTypes.map((type) => {
      const listener: EventListener = (rawEvent) => {
        const event = rawEvent as MessageEvent<string>
        let parsed: AgentEvent
        try {
          parsed = JSON.parse(event.data) as AgentEvent
        } catch {
          setError('Получено событие в неизвестном формате')
          return
        }
        if (parsed.type === 'message.delta') {
          const payload = parsed.payload as Partial<MessageDeltaPayload>
          if (typeof payload.stream_id !== 'string' || typeof payload.message_id !== 'string' || typeof payload.delta !== 'string' || !Number.isInteger(payload.chunk_index)) {
            setError('Получен некорректный фрагмент ответа')
            return
          }
          setDraft((current) => {
            if (current && parsed.seq <= current.seq) return current
            if (!current || current.stream_id !== payload.stream_id || current.message_id !== payload.message_id) {
              return { ...payload, content: payload.delta!, seq: parsed.seq } as DraftMessage
            }
            if (payload.chunk_index! <= current.chunk_index) return { ...current, seq: parsed.seq }
            return {
              ...current,
              delta: payload.delta!,
              content: current.content + payload.delta!,
              chunk_index: payload.chunk_index!,
              seq: parsed.seq,
            }
          })
          return
        }
        setEvents((current) => current.some((item) => item.seq === parsed.seq) ? current : [...current, parsed].slice(-200))
        if (['task.completed', 'task.failed', 'task.cancelled'].includes(parsed.type)) {
          source.close()
          setConnection('idle')
        }
        void refreshTasks(true)
        void refreshDetail(selectedID)
      }
      source.addEventListener(type, listener)
      return [type, listener]
    })
    source.onopen = () => setConnection('live')
    source.onerror = () => setConnection('reconnecting')
    return () => {
      for (const [type, listener] of listeners) source.removeEventListener(type, listener)
      source.close()
    }
  }, [selectedID, selectedTask?.status, refreshDetail, refreshTasks])

  useEffect(() => {
    let active = true
    const check = async () => {
      try {
        const response = await fetch('/api/doctor')
        const result = await response.json() as DoctorResponse
        if (active) setDoctor(result)
      } catch {
        if (active) setDoctor(null)
      }
    }
    void check()
    const timer = window.setInterval(() => void check(), 30000)
    return () => { active = false; window.clearInterval(timer) }
  }, [])

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
      await refreshTasks(true)
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
      await refreshTasks(true)
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
      await refreshTasks(true)
      await refreshDetail(taskID)
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
      if (selectedID) await refreshDetail(selectedID)
      await refreshTasks(true)
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
      await refreshDetail(selectedID)
      await refreshTasks(true)
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
  const apiReady = doctor && ['app', 'worker', 'database'].every((name) => doctor.checks[name]?.status === 'ok')

  return (
    <div className="app-shell">
      <aside className="rail" aria-label="Основная навигация">
        <a className="brand" href="#/workspace" aria-label="Qwenchik, доска задач">
          <span className="brand-mark">Q</span>
          <span className="brand-name">qwenchik</span>
        </a>
        <div className="rail-divider" />
        <a className="rail-link" href="#/chat" aria-label="Открыть чаты">Чаты</a>
        <button className="rail-link is-current" type="button" aria-current="page" onClick={() => document.getElementById('board')?.scrollIntoView()}>
          <span className="rail-glyph">▦</span><span>Доска</span>
        </button>
        <button className="rail-link" type="button" onClick={() => selectedTask && document.getElementById('timeline')?.scrollIntoView({ behavior: 'smooth' })} disabled={!selectedTask}>
          <span className="rail-glyph">≋</span><span>События</span>
        </button>
        <button className="rail-link" type="button" onClick={() => setMemoryOpen(true)} aria-haspopup="dialog">
          <span className="rail-glyph">⌘</span><span>Память</span>
        </button>
        <div className="rail-spacer" />
        <div className="rail-runtime" title={modelCheck?.message ?? 'Проверка локальной модели не выполнена'}>
          <span className={`health-dot ${modelCheck?.status === 'ok' ? 'is-ok' : 'is-warning'}`} />
          <span>Qwen</span>
        </div>
      </aside>

      <main className="workspace" id="board">
        <header className="topbar">
          <div className="breadcrumb"><span>Рабочее пространство</span><span className="crumb-sep">/</span><strong>Задачи</strong></div>
          <div className="top-actions">
            <span className={`service-pill ${apiReady ? 'is-online' : ''}`}>
              <span className="health-dot" />
              {apiReady ? 'Приложение и worker готовы' : 'Сервис не готов / не проверен'}
            </span>
            <button className="button button-quiet palette-trigger" type="button" onClick={() => setPaletteOpen(true)} aria-label="Открыть командную палитру">
              <span>Команды</span><kbd>⌘ K</kbd>
            </button>
            <button className="button button-primary" type="button" onClick={() => setCreateOpen((open) => !open)}>
              <span className="button-plus">+</span> Новая задача
            </button>
          </div>
        </header>

        <section className="page-heading">
          <div>
            <div className="eyebrow"><span className="eyebrow-line" /> ЛОКАЛЬНЫЙ АГЕНТ · ОДИН WORKER</div>
            <h1>Задачи</h1>
            <p className="page-subtitle">План, состояние и реальные события выполнения — в одном месте.</p>
          </div>
          <div className="summary-strip" aria-label="Сводка задач">
            <div className="summary-item"><span className="summary-value">{counts.total}</span><span className="summary-label">всего</span></div>
            <div className="summary-rule" />
            <div className="summary-item"><span className="summary-value">{counts.active}</span><span className="summary-label">в работе</span></div>
            <div className="summary-rule" />
            <div className="summary-item"><span className="summary-value">{counts.approvals}</span><span className="summary-label">ожидают решения</span></div>
          </div>
        </section>

        {error && <div className="notice notice-error" role="alert"><span className="notice-marker">!</span><span>{error}</span><button type="button" onClick={() => setError('')} aria-label="Закрыть сообщение">×</button></div>}

        <section className="provider-panel" aria-label="Поисковые провайдеры">
          <h2>Поисковые провайдеры</h2>
          <p>Конфигурация и последнее реальное обращение. До первого запроса здоровье неизвестно.</p>
          <p>Поисковые запросы передаются включённым внешним сервисам. DuckDuckGo можно отключить через SEARCH_ENABLE_DDG=false; локальный SearXNG также обращается к публичным поисковикам.</p>
          {providersError && <p role="status">{providersError}</p>}
          {!providers && !providersError && <p>Загрузка состояния…</p>}
          <div className="provider-grid">{Object.entries(providers?.search.providers ?? {}).map(([name, provider]) => <ProviderCard key={name} name={name} provider={provider} checkedLabel={provider.last_checked ? timeLabel(provider.last_checked) : ''} />)}</div>
        </section>

        {createOpen && (
          <form className="create-panel" onSubmit={createTask}>
            <div className="create-heading">
              <div><span className="eyebrow">НОВАЯ РАБОТА</span><h2>Что нужно сделать?</h2></div>
              <button className="icon-button" type="button" onClick={() => setCreateOpen(false)} aria-label="Закрыть форму">×</button>
            </div>
            <label className="field-label" htmlFor="new-task-title">Название <span>необязательно</span></label>
            <input id="new-task-title" className="text-field" value={createTitle} onChange={(event) => setCreateTitle(event.target.value)} placeholder="Коротко обозначьте результат" maxLength={200} />
            <label className="field-label" htmlFor="new-task-description">Описание задачи</label>
            <textarea id="new-task-description" className="text-field text-area" value={createDescription} onChange={(event) => setCreateDescription(event.target.value)} placeholder="Опишите цель и ограничения. Задача сохранится локально." maxLength={20000} required rows={3} />
            <div className="create-footer"><span className="privacy-note"><span className="lock-mark">⌑</span> Сохранится в локальной SQLite-базе</span><button className="button button-primary" type="submit" disabled={busy || !createDescription.trim()}>{busy ? 'Создаём…' : 'Создать задачу'}</button></div>
          </form>
        )}

        <div className="board-toolbar">
          <div className="board-label"><span className="board-indicator" /> KANBAN <span className="board-count">{tasks.length}</span></div>
          <div className="board-meta"><span>{refreshing ? 'Обновление…' : 'Обновляется автоматически'}</span><button className="text-button" type="button" onClick={() => void refreshTasks()} disabled={refreshing}>{refreshing ? 'Обновляем' : 'Обновить'}</button></div>
        </div>

        <section className="board-grid" aria-label="Kanban доска">
          {columns.map((column) => {
            const items = tasks.filter((task) => column.statuses.includes(task.status))
            return (
              <section className={`board-column column-${column.id}`} id={`column-${column.id}`} key={column.id} aria-labelledby={`heading-${column.id}`}>
                <header className="column-heading">
                  <div className="column-title-wrap"><span className={`column-marker marker-${column.id}`} /><h2 id={`heading-${column.id}`}>{column.title}</h2></div>
                  <span className="column-count">{items.length.toString().padStart(2, '0')}</span>
                </header>
                <div className="column-items">
                  {items.map((task) => (
                    <button className={`task-card ${selectedID === task.id ? 'is-selected' : ''}`} key={task.id} type="button" onClick={() => setSelectedID(task.id)} aria-pressed={selectedID === task.id}>
                      <div className="task-card-top"><span className={`status-tag status-${task.status}`}>{statusLabels[task.status]}</span><span className="task-id">{shortID(task.id)}</span></div>
                      <h3>{task.title}</h3>
                      <p>{task.description}</p>
                      <div className="task-card-bottom"><span>{task.stage ? stageLabels[task.stage] ?? task.stage : 'Без этапа'}</span><time dateTime={task.updated_at}>{timeLabel(task.updated_at)}</time></div>
                    </button>
                  ))}
                  {items.length === 0 && <div className="column-empty"><span className="empty-dash">—</span><span>Пока пусто</span></div>}
                </div>
              </section>
            )
          })}
        </section>

        <div className={`detail-layout ${selectedTask ? 'has-selection' : ''}`}>
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
              onSend={sendMessage}
            />
          ) : (
            <div className="selection-empty"><span className="selection-glyph">↖</span><div><strong>Выберите задачу</strong><p>План, чат и поток событий появятся здесь.</p></div></div>
          )}
        </div>

        <footer className="app-footer"><span>QWENCHIK · LOCAL-FIRST</span><span>События и прогресс принадлежат backend</span></footer>
      </main>

      {paletteOpen && <CommandPalette tasks={tasks} onClose={() => setPaletteOpen(false)} onExecute={(command) => void executeCommand(command)} />}
      {memoryOpen && <MemoryManager onClose={() => setMemoryOpen(false)} />}
    </div>
  )
}

interface InspectorProps {
  task: Task
  messages: Message[]
  draft: DraftMessage | null
  events: AgentEvent[]
  checkpoints: Checkpoint[]
  steps: TaskStep[]
  artifacts: Artifact[]
  evidence: Evidence[]
  approvals: Approval[]
  connection: string
  busy: boolean
  chatText: string
  setChatText: (value: string) => void
  onRun: () => void
  onPause: () => void
  onResume: () => void
  onCancel: () => void
  onApprove: (approvalID: string) => void
  onReject: (approvalID: string) => void
  onSend: (event: FormEvent<HTMLFormElement>) => void
}

function TaskInspector(props: InspectorProps) {
  const { task, messages, draft, events, checkpoints, steps, artifacts, evidence, approvals, connection, busy } = props
  const canSend = ['inbox', 'queued', 'paused', 'failed'].includes(task.status)
  const canPause = ['queued', 'planning', 'running', 'verifying'].includes(task.status)
  const canCancel = !['completed', 'failed', 'cancelled'].includes(task.status)
  const timeline = [...events].sort((a, b) => b.seq - a.seq).slice(0, 30)
  const sortedMessages = [...messages].sort((a, b) => a.created_at.localeCompare(b.created_at))
  const failure = [...events].reverse().find((event) => event.type === 'task.failed')

  return (
    <section className="inspector" aria-label="Инспектор задачи">
      <header className="inspector-header">
        <div><div className="eyebrow">ЗАДАЧА <span className="mono">{shortID(task.id)}</span></div><h2>{task.title}</h2></div>
        <span className={`status-tag status-${task.status}`}>{statusLabels[task.status]}</span>
      </header>
      <div className="inspector-actions">
        {['inbox', 'failed'].includes(task.status) && <button className="button button-primary" type="button" onClick={props.onRun} disabled={busy}>{task.status === 'failed' ? 'Повторить' : 'Запустить'}</button>}
        {canPause && <button className="button button-secondary" type="button" onClick={props.onPause} disabled={busy}>Пауза</button>}
        {task.status === 'paused' && <button className="button button-primary" type="button" onClick={props.onResume} disabled={busy}>Продолжить</button>}
        {canCancel && <button className="button button-danger-quiet" type="button" onClick={props.onCancel} disabled={busy}>Отменить</button>}
        {busy && <span className="action-feedback" role="status">Сохраняем…</span>}
      </div>

      {task.status === 'failed' && <TaskFailureCard payload={failure?.payload} evidenceCount={evidence.length} />}

      {task.status === 'waiting_approval' && approvals.filter((approval) => approval.status === 'pending').map((approval) => <ApprovalCard key={approval.id} approval={approval} busy={busy} onApprove={() => props.onApprove(approval.id)} onReject={() => props.onReject(approval.id)} />)}

      <div className="inspector-section goal-section">
        <div className="section-kicker">ЦЕЛЬ</div>
        <p className="goal-copy">{task.description}</p>
        <div className="stage-row"><span>Текущий этап</span><strong>{task.stage ? stageLabels[task.stage] ?? task.stage : 'Ожидает запуска'}</strong></div>
      </div>

      <div className="inspector-section">
        <div className="section-title"><div><span className="section-kicker">ПЛАН</span><h3>Шаги выполнения</h3></div><span className="section-count">{steps.length}</span></div>
        {steps.length ? (
          <ol className="plan-list">{steps.map((step) => <li key={step.step_id}><span className={`step-state step-${step.status}`} aria-label={step.status}>{step.status === 'completed' ? '✓' : step.status === 'running' ? '·' : step.status === 'failed' ? '!' : step.status === 'skipped' ? '–' : step.position.toString().padStart(2, '0')}</span><span>{step.description}</span><span className="plan-kind">{step.status === 'running' ? 'выполняется' : step.status === 'completed' ? 'готово' : step.status === 'failed' ? 'ошибка' : step.status === 'skipped' ? 'пропущено' : step.kind}</span></li>)}</ol>
        ) : <div className="inline-empty">План появится после запуска.</div>}
      </div>

      <div className="inspector-section chat-section">
        <div className="section-title"><div><span className="section-kicker">РАЗГОВОР</span><h3>Контекст задачи</h3></div><span className="section-count">{messages.length + 1 + (draft ? 1 : 0)}</span></div>
        <div className="message-list" aria-live="polite">
          <article className="message message-user"><span className="message-role">ЗАПРОС</span><p>{task.description}</p><time>{timeLabel(task.created_at)}</time></article>
          {sortedMessages.map((message) => <article className={`message message-${message.role}`} key={message.id}><span className="message-role">{message.role === 'assistant' ? 'QWEN' : message.role === 'user' ? 'ВЫ' : 'СИСТЕМА'}</span><p>{message.content}</p><time>{timeLabel(message.created_at)}</time></article>)}
          {draft && !sortedMessages.some((message) => message.id === draft.message_id) && <article className="message message-draft" key={`draft-${draft.message_id}`}><span className="message-role">QWEN · ЧЕРНОВИК</span><p>{draft.content}</p><time>Ответ проверяется</time></article>}
        </div>
        <form className="chat-form" onSubmit={props.onSend}>
          <label className="sr-only" htmlFor="chat-message">Добавить сообщение к задаче</label>
          <textarea id="chat-message" className="text-field chat-input" value={props.chatText} onChange={(event) => props.setChatText(event.target.value)} placeholder={canSend ? (task.status === 'failed' ? 'Уточнить задачу и запустить повторно…' : 'Добавить уточнение…') : 'Сообщение доступно до запуска или на паузе'} rows={2} maxLength={20000} disabled={!canSend || busy} />
          <button className="button button-secondary send-button" type="submit" aria-label="Отправить сообщение" disabled={!canSend || busy || !props.chatText.trim()}>{busy ? '…' : 'Отправить'}</button>
        </form>
        {!canSend && <p className="field-hint">Новые сообщения принимаются до запуска, в очереди и на паузе.</p>}
      </div>

      <div className="inspector-section" id="timeline">
        <div className="section-title"><div><span className="section-kicker">ИСТОРИЯ</span><h3>События выполнения</h3></div><span className={`stream-status ${connection !== 'live' || ['completed', 'failed', 'cancelled'].includes(task.status) ? 'is-reconnecting' : ''}`} role="status"><span className="health-dot" />{streamLabel(task.status, connection)}</span></div>
        {timeline.length ? <ol className="event-list">{timeline.map((event) => <li className="event-row" key={event.id}><span className="event-seq">{event.seq.toString().padStart(3, '0')}</span><div className="event-copy"><strong>{eventLabels[event.type] ?? event.type}</strong>{eventSummary(event) && <p>{eventSummary(event)}</p>}<time>{timeLabel(event.created_at)}</time></div></li>)}</ol> : <div className="inline-empty">Событий пока нет.</div>}
      </div>

      <div className="inspector-section resource-section">
        <div className="section-title"><div><span className="section-kicker">ВОССТАНОВЛЕНИЕ</span><h3>Checkpoint-ы</h3></div><span className="section-count">{checkpoints.length}</span></div>
        {checkpoints.length ? <ul className="checkpoint-list">{checkpoints.slice(-4).reverse().map((checkpoint) => <li key={checkpoint.id}><span className="checkpoint-dot" /><span>Последнее событие <b className="mono">#{checkpoint.last_event_seq}</b></span><time>{timeLabel(checkpoint.created_at)}</time></li>)}</ul> : <div className="inline-empty">Checkpoint появится после планирования.</div>}
        <div className="resource-pair">
          <div><div className="resource-heading"><span className="section-kicker">ИСТОЧНИКИ</span><span className="section-count">{evidence.length}</span></div><SourceList sources={evidence} formatTime={timeLabel} /></div>
          <div><div className="resource-heading"><span className="section-kicker">АРТЕФАКТЫ</span><span className="section-count">{artifacts.length}</span></div>{artifacts.length ? <ul className="artifact-list">{artifacts.map((artifact) => <li key={artifact.id}><span className="artifact-glyph">↳</span><span><a href={`/api/tasks/${encodeURIComponent(task.id)}/artifacts/${encodeURIComponent(artifact.id)}`}>{artifact.name}</a><small>{artifact.mime_type} · {artifact.size_bytes.toLocaleString('ru-RU')} байт</small></span></li>)}</ul> : <p>Сохранённых файлов пока нет.</p>}</div>
        </div>
      </div>
    </section>
  )
}

function ApprovalCard({ approval, busy, onApprove, onReject }: { approval: Approval; busy: boolean; onApprove: () => void; onReject: () => void }) {
  return (
    <section className="approval-card" aria-labelledby={`approval-${approval.id}`}>
      <div className="approval-kicker"><span className="approval-pulse" /> ОЖИДАЕТ ВАШЕГО РЕШЕНИЯ</div>
      <h3 id={`approval-${approval.id}`}>{approval.tool_name}</h3>
      <dl className="approval-details">
        <div><dt>Адресат</dt><dd>{approval.target || 'не указан'}</dd></div>
        <div><dt>Почему</dt><dd>{approval.risk_explanation}</dd></div>
        <div><dt>Аргументы действия</dt><dd><pre>{JSON.stringify(approval.arguments, null, 2)}</pre></dd></div>
        {approval.tool_name === 'harness.promote_bundle' && typeof approval.arguments.bundle_hash === 'string' && /^[a-f0-9]{64}$/.test(approval.arguments.bundle_hash) && <div><dt>Перед подтверждением</dt><dd><a href={`/api/bundles/${approval.arguments.bundle_hash}`} target="_blank" rel="noopener noreferrer">Открыть CandidatePatch, provenance и результаты held-out проверки</a><p>Будут изменены только operational hints для новых запусков. Политики, инструменты и hard budgets остаются под контролем backend.</p></dd></div>}
        <div><dt>Истекает</dt><dd>{timeLabel(approval.expires_at)}</dd></div>
      </dl>
      <div className="approval-actions"><button className="button button-danger-quiet" type="button" onClick={onReject} disabled={busy}>Отклонить</button><button className="button button-primary" type="button" onClick={onApprove} disabled={busy}>{busy ? 'Сохраняем…' : 'Подтвердить действие'}</button></div>
    </section>
  )
}

const memoryStatusLabels: Record<MemoryStatus, string> = {
  candidate: 'Кандидат', accepted: 'Подтверждена', stale: 'Устарела', superseded: 'Заменена', rejected: 'Отклонена',
}

function MemoryManager({ onClose }: { onClose: () => void }) {
  const [memories, setMemories] = useState<Memory[]>([])
  const [query, setQuery] = useState('')
  const [matches, setMatches] = useState<MemoryMatch[]>([])
  const [kind, setKind] = useState('preference')
  const [text, setText] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const refresh = useCallback(async () => {
    try {
      setMemories(await api.memories())
      setError('')
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : 'Не удалось загрузить память')
    }
  }, [])

  useEffect(() => { void refresh() }, [refresh])

  async function createCandidate(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!text.trim()) return
    setBusy(true)
    try {
      await api.createMemory(kind, text.trim())
      setText('')
      await refresh()
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : 'Не удалось сохранить память')
    } finally {
      setBusy(false)
    }
  }

  async function search(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setBusy(true)
    try {
      setMatches(await api.searchMemories(query.trim()))
      setError('')
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : 'Не удалось найти память')
    } finally {
      setBusy(false)
    }
  }

  async function decide(id: string, status: Extract<MemoryStatus, 'accepted' | 'rejected' | 'stale' | 'superseded'>) {
    setBusy(true)
    try {
      await api.decideMemory(id, status)
      await refresh()
      if (query.trim()) setMatches(await api.searchMemories(query.trim()))
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : 'Не удалось обновить память')
    } finally {
      setBusy(false)
    }
  }

  async function save(memory: Memory, nextKind: string, nextText: string) {
    setBusy(true)
    try {
      await api.updateMemory(memory.id, nextKind, nextText, memory.metadata)
      await refresh()
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : 'Не удалось изменить память')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="memory-backdrop" role="presentation" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose() }}>
      <section className="memory-manager" role="dialog" aria-modal="true" aria-labelledby="memory-title">
        <header className="memory-manager-header"><div><span className="eyebrow">ЛОКАЛЬНЫЙ КОНТЕКСТ</span><h2 id="memory-title">Память</h2><p>В задачи попадают только подтверждённые записи. Новые заметки сначала требуют вашего решения.</p></div><button className="icon-button" type="button" onClick={onClose} aria-label="Закрыть память">×</button></header>
        {error && <div className="notice notice-error" role="alert">{error}</div>}
        <form className="memory-create" onSubmit={createCandidate}>
          <label className="field-label" htmlFor="memory-kind">Тип</label>
          <select id="memory-kind" className="text-field" value={kind} onChange={(event) => setKind(event.target.value)}><option value="preference">Предпочтение</option><option value="fact">Факт</option><option value="workflow">Процесс</option><option value="constraint">Ограничение</option></select>
          <label className="field-label" htmlFor="memory-text">Новая запись</label>
          <textarea id="memory-text" className="text-field text-area" value={text} onChange={(event) => setText(event.target.value)} maxLength={4000} rows={2} placeholder="Например: ответы — кратко, на русском языке" />
          <button className="button button-primary" type="submit" disabled={busy || !text.trim()}>{busy ? 'Сохраняем…' : 'Добавить как кандидата'}</button>
        </form>
        <form className="memory-search" onSubmit={search}>
          <label className="field-label" htmlFor="memory-search">Проверить поиск по подтверждённым записям</label>
          <div className="memory-search-row"><input id="memory-search" className="text-field" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Введите тему или ключевые слова" /><button className="button button-secondary" type="submit" disabled={busy || !query.trim()}>Найти</button></div>
          {matches.length > 0 && <ul className="memory-match-list">{matches.map((match) => <li key={match.memory.id}><span className="memory-kind">{match.memory.kind} · релевантность {match.relevance}</span><p>{match.memory.text}</p></li>)}</ul>}
        </form>
        <div className="memory-list-heading"><h3>Записи</h3><span>{memories.length}</span></div>
        <div className="memory-list">{memories.map((memory) => <MemoryCard key={memory.id} memory={memory} busy={busy} onDecide={(status) => void decide(memory.id, status)} onSave={(nextKind, nextText) => void save(memory, nextKind, nextText)} />)}{memories.length === 0 && <div className="inline-empty">Память пока пуста.</div>}</div>
      </section>
    </div>
  )
}

function MemoryCard({ memory, busy, onDecide, onSave }: {
  memory: Memory
  busy: boolean
  onDecide: (status: Extract<MemoryStatus, 'accepted' | 'rejected' | 'stale' | 'superseded'>) => void
  onSave: (kind: string, text: string) => void
}) {
  const [kind, setKind] = useState(memory.kind)
  const [text, setText] = useState(memory.text)
  const changed = kind !== memory.kind || text !== memory.text
  return (
    <article className="memory-card">
      <div className="memory-card-top"><span className={`status-tag memory-status-${memory.status}`}>{memoryStatusLabels[memory.status]}</span><time>{timeLabel(memory.updated_at)}</time></div>
      <label className="field-label" htmlFor={`memory-kind-${memory.id}`}>Тип</label>
      <input id={`memory-kind-${memory.id}`} className="text-field" value={kind} onChange={(event) => setKind(event.target.value)} maxLength={64} />
      <label className="field-label" htmlFor={`memory-text-${memory.id}`}>Содержание</label>
      <textarea id={`memory-text-${memory.id}`} className="text-field text-area" value={text} onChange={(event) => setText(event.target.value)} maxLength={4000} rows={2} />
      {memory.source_task_id && <p className="memory-source">Источник: задача {shortID(memory.source_task_id)}{memory.source_evidence_id ? ` · свидетельство ${shortID(memory.source_evidence_id)}` : ''}</p>}
      <div className="memory-actions">
        {changed && <button className="button button-secondary" type="button" onClick={() => onSave(kind, text)} disabled={busy}>Сохранить изменения</button>}
        {memory.status === 'candidate' && <><button className="button button-primary" type="button" onClick={() => onDecide('accepted')} disabled={busy}>Подтвердить</button><button className="button button-danger-quiet" type="button" onClick={() => onDecide('rejected')} disabled={busy}>Отклонить</button></>}
        {memory.status === 'accepted' && <><button className="button button-secondary" type="button" onClick={() => onDecide('stale')} disabled={busy}>Устарела</button><button className="button button-danger-quiet" type="button" onClick={() => onDecide('rejected')} disabled={busy}>Удалить из поиска</button></>}
        {memory.status === 'stale' && <button className="button button-primary" type="button" onClick={() => onDecide('accepted')} disabled={busy}>Подтвердить снова</button>}
      </div>
    </article>
  )
}

interface PaletteProps {
  tasks: Task[]
  onClose: () => void
  onExecute: (command: string) => void
}

function CommandPalette({ tasks, onClose, onExecute }: PaletteProps) {
  const [query, setQuery] = useState('')
  const [active, setActive] = useState(0)
  const inputRef = useRef<HTMLInputElement>(null)
  const commands = useMemo(() => {
    const base = [
      { id: 'new', label: 'Создать задачу', hint: 'Доска', keywords: 'новая создать задача' },
      { id: 'memories', label: 'Открыть память', hint: 'Контекст', keywords: 'память memory memories' },
      { id: 'refresh', label: 'Обновить доску', hint: 'Действие', keywords: 'обновить перезагрузить' },
      { id: 'active', label: 'Открыть активную задачу', hint: 'Навигация', keywords: 'активная running работа' },
      ...columns.map((column) => ({ id: `column:${column.id}`, label: `Перейти: ${column.title}`, hint: 'Колонка', keywords: column.title.toLowerCase() })),
    ]
    return base.filter((command) => `${command.label} ${command.keywords}`.toLowerCase().includes(query.toLowerCase()))
  }, [query])

  useEffect(() => { inputRef.current?.focus() }, [])

  function handleKey(event: React.KeyboardEvent<HTMLInputElement>) {
    if (event.key === 'ArrowDown') {
      event.preventDefault()
      setActive((index) => Math.min(index + 1, commands.length - 1))
    } else if (event.key === 'ArrowUp') {
      event.preventDefault()
      setActive((index) => Math.max(index - 1, 0))
    } else if (event.key === 'Enter' && commands[active]) {
      onExecute(commands[active].id)
    }
  }

  return (
    <div className="palette-backdrop" role="presentation" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose() }}>
      <section className="command-palette" role="dialog" aria-modal="true" aria-label="Командная палитра">
        <div className="palette-search"><span className="palette-icon">⌕</span><input ref={inputRef} value={query} onChange={(event) => { setQuery(event.target.value); setActive(0) }} onKeyDown={handleKey} placeholder="Найти команду…" aria-label="Найти команду" /><kbd>ESC</kbd></div>
        <div className="palette-results" role="listbox" aria-label="Команды">
          {commands.map((command, index) => <button className={`palette-option ${active === index ? 'is-active' : ''}`} type="button" role="option" aria-selected={active === index} key={command.id} onMouseEnter={() => setActive(index)} onClick={() => onExecute(command.id)}><span>{command.label}</span><small>{command.hint}</small><span className="palette-arrow">↵</span></button>)}
          {commands.length === 0 && <div className="palette-empty">Команд не найдено</div>}
        </div>
        <footer className="palette-footer"><span>{tasks.length} задач</span><span><kbd>↑</kbd><kbd>↓</kbd> навигация <kbd>↵</kbd> выбрать</span></footer>
      </section>
    </div>
  )
}

function App() {
  const [workspace, setWorkspace] = useState(() => window.location.hash.startsWith('#/workspace'))
  useEffect(() => {
    const navigate = () => setWorkspace(window.location.hash.startsWith('#/workspace'))
    window.addEventListener('hashchange', navigate)
    return () => window.removeEventListener('hashchange', navigate)
  }, [])
  return workspace ? <Workspace /> : <ChatShell />
}

export default App
