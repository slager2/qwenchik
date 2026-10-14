import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import { api } from './api'
import type { AgentEvent, Artifact, Checkpoint, Evidence, Message, Task, TaskStatus, TaskStep } from './types'

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
  'tool.started': 'Запущен инструмент',
  'tool.completed': 'Инструмент завершён',
  'approval.requested': 'Требуется подтверждение',
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
  if (typeof payload.summary === 'string') return payload.summary
  if (typeof payload.description === 'string') return payload.description
  if (typeof payload.error_code === 'string') return `Код: ${payload.error_code}`
  if (typeof payload.status === 'string') return `Статус: ${payload.status}`
  if (typeof payload.tool === 'string') return `Инструмент: ${payload.tool}`
  if (typeof payload.evidence_id === 'string') return `Источник: ${payload.evidence_id}`
  if (typeof payload.artifact_id === 'string') return `Файл: ${typeof payload.name === 'string' ? payload.name : payload.artifact_id}`
  if (typeof payload.steps === 'number') return `Шагов в плане: ${payload.steps}`
  return ''
}

function App() {
  const [tasks, setTasks] = useState<Task[]>([])
  const [selectedID, setSelectedID] = useState('')
  const [messages, setMessages] = useState<Message[]>([])
  const [events, setEvents] = useState<AgentEvent[]>([])
  const [checkpoints, setCheckpoints] = useState<Checkpoint[]>([])
  const [steps, setSteps] = useState<TaskStep[]>([])
  const [artifacts, setArtifacts] = useState<Artifact[]>([])
  const [evidence, setEvidence] = useState<Evidence[]>([])
  const [doctor, setDoctor] = useState<DoctorResponse | null>(null)
  const [createOpen, setCreateOpen] = useState(false)
  const [createTitle, setCreateTitle] = useState('')
  const [createDescription, setCreateDescription] = useState('')
  const [chatText, setChatText] = useState('')
  const [busy, setBusy] = useState(false)
  const [refreshing, setRefreshing] = useState(false)
  const [reconnecting, setReconnecting] = useState(false)
  const [error, setError] = useState('')
  const [paletteOpen, setPaletteOpen] = useState(false)
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
    try {
      const [task, nextMessages, nextEvents, nextCheckpoints, nextSteps, nextArtifacts, nextEvidence] = await Promise.all([
        api.task(id), api.messages(id), api.events(id), api.checkpoints(id), api.steps(id), api.artifacts(id), api.evidence(id),
      ])
      setTasks((current) => current.map((item) => item.id === task.id ? task : item))
      setMessages(nextMessages)
      setEvents(nextEvents)
      setCheckpoints(nextCheckpoints)
      setSteps(nextSteps)
      setArtifacts(nextArtifacts)
      setEvidence(nextEvidence)
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : 'Не удалось загрузить задачу')
    }
  }, [])

  useEffect(() => {
    void refreshTasks()
    const timer = window.setInterval(() => void refreshTasks(true), 4000)
    return () => window.clearInterval(timer)
  }, [refreshTasks])

  useEffect(() => {
    if (!selectedID) {
      setMessages([])
      setEvents([])
      setCheckpoints([])
      setSteps([])
      setArtifacts([])
      setEvidence([])
      return
    }
    void refreshDetail(selectedID)
  }, [selectedID, refreshDetail])

  useEffect(() => {
    if (!selectedID) return
    const source = new EventSource(`/api/tasks/${encodeURIComponent(selectedID)}/events/stream`)
    const eventTypes = Object.keys(eventLabels)
    const listeners: [string, EventListener][] = eventTypes.map((type) => {
      const listener: EventListener = (rawEvent) => {
        const event = rawEvent as MessageEvent<string>
        try {
          const parsed = JSON.parse(event.data) as AgentEvent
          setEvents((current) => current.some((item) => item.seq === parsed.seq) ? current : [...current, parsed].slice(-200))
        } catch {
          setError('Получено событие в неизвестном формате')
        }
        void refreshTasks(true)
        void refreshDetail(selectedID)
      }
      source.addEventListener(type, listener)
      return [type, listener]
    })
    source.onopen = () => setReconnecting(false)
    source.onerror = () => setReconnecting(true)
    return () => {
      for (const [type, listener] of listeners) source.removeEventListener(type, listener)
      source.close()
    }
  }, [selectedID, refreshDetail, refreshTasks])

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
    } else if (command.startsWith('column:')) {
      document.getElementById(`column-${command.slice(7)}`)?.scrollIntoView({ behavior: 'smooth', block: 'start' })
    }
  }

  const modelCheck = doctor?.checks.llm
  const apiCheck = doctor?.checks.database

  return (
    <div className="app-shell">
      <aside className="rail" aria-label="Основная навигация">
        <a className="brand" href="#board" aria-label="Qwenchik, доска задач">
          <span className="brand-mark">Q</span>
          <span className="brand-name">qwenchik</span>
        </a>
        <div className="rail-divider" />
        <button className="rail-link is-current" type="button" aria-current="page" onClick={() => document.getElementById('board')?.scrollIntoView()}>
          <span className="rail-glyph">▦</span><span>Доска</span>
        </button>
        <button className="rail-link" type="button" onClick={() => selectedTask && document.getElementById('timeline')?.scrollIntoView({ behavior: 'smooth' })} disabled={!selectedTask}>
          <span className="rail-glyph">≋</span><span>События</span>
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
            <span className={`service-pill ${apiCheck?.status === 'ok' ? 'is-online' : ''}`}>
              <span className="health-dot" />
              {apiCheck?.status === 'ok' ? 'Локально подключено' : 'Проверка сервиса'}
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
              events={events}
              checkpoints={checkpoints}
              steps={steps}
              artifacts={artifacts}
              evidence={evidence}
              reconnecting={reconnecting}
              busy={busy}
              chatText={chatText}
              setChatText={setChatText}
              onRun={() => void runTask(selectedTask.id)}
              onPause={() => void controlTask('pause', selectedTask.id)}
              onResume={() => void controlTask('resume', selectedTask.id)}
              onCancel={() => void controlTask('cancel', selectedTask.id)}
              onSend={sendMessage}
            />
          ) : (
            <div className="selection-empty"><span className="selection-glyph">↖</span><div><strong>Выберите задачу</strong><p>План, чат и поток событий появятся здесь.</p></div></div>
          )}
        </div>

        <footer className="app-footer"><span>QWENCHIK · LOCAL-FIRST</span><span>События и прогресс принадлежат backend</span></footer>
      </main>

      {paletteOpen && <CommandPalette tasks={tasks} onClose={() => setPaletteOpen(false)} onExecute={(command) => void executeCommand(command)} />}
    </div>
  )
}

interface InspectorProps {
  task: Task
  messages: Message[]
  events: AgentEvent[]
  checkpoints: Checkpoint[]
  steps: TaskStep[]
  artifacts: Artifact[]
  evidence: Evidence[]
  reconnecting: boolean
  busy: boolean
  chatText: string
  setChatText: (value: string) => void
  onRun: () => void
  onPause: () => void
  onResume: () => void
  onCancel: () => void
  onSend: (event: FormEvent<HTMLFormElement>) => void
}

function TaskInspector(props: InspectorProps) {
  const { task, messages, events, checkpoints, steps, artifacts, evidence, reconnecting, busy } = props
  const canSend = ['inbox', 'queued', 'paused'].includes(task.status)
  const canPause = ['queued', 'planning', 'running', 'verifying'].includes(task.status)
  const canCancel = !['completed', 'failed', 'cancelled'].includes(task.status)
  const timeline = [...events].sort((a, b) => b.seq - a.seq).slice(0, 30)
  const sortedMessages = [...messages].sort((a, b) => a.created_at.localeCompare(b.created_at))

  return (
    <section className="inspector" aria-label="Инспектор задачи">
      <header className="inspector-header">
        <div><div className="eyebrow">ЗАДАЧА <span className="mono">{shortID(task.id)}</span></div><h2>{task.title}</h2></div>
        <span className={`status-tag status-${task.status}`}>{statusLabels[task.status]}</span>
      </header>
      <div className="inspector-actions">
        {task.status === 'inbox' && <button className="button button-primary" type="button" onClick={props.onRun} disabled={busy}>Запустить</button>}
        {canPause && <button className="button button-secondary" type="button" onClick={props.onPause} disabled={busy}>Пауза</button>}
        {task.status === 'paused' && <button className="button button-primary" type="button" onClick={props.onResume} disabled={busy}>Продолжить</button>}
        {canCancel && <button className="button button-danger-quiet" type="button" onClick={props.onCancel} disabled={busy}>Отменить</button>}
        {busy && <span className="action-feedback" role="status">Сохраняем…</span>}
      </div>

      <div className="inspector-section goal-section">
        <div className="section-kicker">ЦЕЛЬ</div>
        <p className="goal-copy">{task.description}</p>
        <div className="stage-row"><span>Текущий этап</span><strong>{task.stage ? stageLabels[task.stage] ?? task.stage : 'Ожидает запуска'}</strong></div>
      </div>

      <div className="inspector-section">
        <div className="section-title"><div><span className="section-kicker">ПЛАН</span><h3>Шаги выполнения</h3></div><span className="section-count">{steps.length}</span></div>
        {steps.length ? (
          <ol className="plan-list">{steps.map((step) => <li key={step.step_id}><span className={`step-state step-${step.status}`} aria-label={step.status}>{step.status === 'completed' ? '✓' : step.status === 'running' ? '·' : step.status === 'failed' ? '!' : step.position.toString().padStart(2, '0')}</span><span>{step.description}</span><span className="plan-kind">{step.status === 'running' ? 'выполняется' : step.status === 'completed' ? 'готово' : step.status === 'failed' ? 'ошибка' : step.kind}</span></li>)}</ol>
        ) : <div className="inline-empty">План появится после запуска.</div>}
      </div>

      <div className="inspector-section chat-section">
        <div className="section-title"><div><span className="section-kicker">РАЗГОВОР</span><h3>Контекст задачи</h3></div><span className="section-count">{messages.length + 1}</span></div>
        <div className="message-list" aria-live="polite">
          <article className="message message-user"><span className="message-role">ЗАПРОС</span><p>{task.description}</p><time>{timeLabel(task.created_at)}</time></article>
          {sortedMessages.map((message) => <article className={`message message-${message.role}`} key={message.id}><span className="message-role">{message.role === 'assistant' ? 'QWEN' : message.role === 'user' ? 'ВЫ' : 'СИСТЕМА'}</span><p>{message.content}</p><time>{timeLabel(message.created_at)}</time></article>)}
        </div>
        <form className="chat-form" onSubmit={props.onSend}>
          <label className="sr-only" htmlFor="chat-message">Добавить сообщение к задаче</label>
          <textarea id="chat-message" className="text-field chat-input" value={props.chatText} onChange={(event) => props.setChatText(event.target.value)} placeholder={canSend ? 'Добавить уточнение…' : 'Сообщение доступно до запуска или на паузе'} rows={2} maxLength={20000} disabled={!canSend || busy} />
          <button className="button button-secondary send-button" type="submit" aria-label="Отправить сообщение" disabled={!canSend || busy || !props.chatText.trim()}>{busy ? '…' : 'Отправить'}</button>
        </form>
        {!canSend && <p className="field-hint">Новые сообщения принимаются до запуска, в очереди и на паузе.</p>}
      </div>

      <div className="inspector-section" id="timeline">
        <div className="section-title"><div><span className="section-kicker">ИСТОРИЯ</span><h3>События выполнения</h3></div><span className={`stream-status ${reconnecting ? 'is-reconnecting' : ''}`}><span className="health-dot" />{reconnecting ? 'Переподключение' : 'SSE live'}</span></div>
        {timeline.length ? <ol className="event-list">{timeline.map((event) => <li className="event-row" key={event.id}><span className="event-seq">{event.seq.toString().padStart(3, '0')}</span><div className="event-copy"><strong>{eventLabels[event.type] ?? event.type}</strong>{eventSummary(event) && <p>{eventSummary(event)}</p>}<time>{timeLabel(event.created_at)}</time></div></li>)}</ol> : <div className="inline-empty">Событий пока нет.</div>}
      </div>

      <div className="inspector-section resource-section">
        <div className="section-title"><div><span className="section-kicker">ВОССТАНОВЛЕНИЕ</span><h3>Checkpoint-ы</h3></div><span className="section-count">{checkpoints.length}</span></div>
        {checkpoints.length ? <ul className="checkpoint-list">{checkpoints.slice(-4).reverse().map((checkpoint) => <li key={checkpoint.id}><span className="checkpoint-dot" /><span>Последнее событие <b className="mono">#{checkpoint.last_event_seq}</b></span><time>{timeLabel(checkpoint.created_at)}</time></li>)}</ul> : <div className="inline-empty">Checkpoint появится после планирования.</div>}
        <div className="resource-pair">
          <div><div className="resource-heading"><span className="section-kicker">ИСТОЧНИКИ</span><span className="section-count">{evidence.length}</span></div>{evidence.length ? <ul className="source-list">{evidence.map((source) => <li key={source.id}><a href={source.canonical_url} target="_blank" rel="noreferrer">{source.title || source.canonical_url}</a><p>{source.excerpt}</p><span className="source-meta">{source.provider} · {timeLabel(source.retrieved_at)} · <span className="mono">{source.content_sha256.slice(0, 10)}</span></span></li>)}</ul> : <p>Источники появятся после research-поиска.</p>}</div>
          <div><div className="resource-heading"><span className="section-kicker">АРТЕФАКТЫ</span><span className="section-count">{artifacts.length}</span></div>{artifacts.length ? <ul className="artifact-list">{artifacts.map((artifact) => <li key={artifact.id}><span className="artifact-glyph">↳</span><span><a href={`/api/tasks/${encodeURIComponent(task.id)}/artifacts/${encodeURIComponent(artifact.id)}`}>{artifact.name}</a><small>{artifact.mime_type} · {artifact.size_bytes.toLocaleString('ru-RU')} байт</small></span></li>)}</ul> : <p>Сохранённых файлов пока нет.</p>}</div>
        </div>
      </div>
    </section>
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

export default App
