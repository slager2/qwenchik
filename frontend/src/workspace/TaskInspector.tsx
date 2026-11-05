import type { FormEvent } from 'react'
import { SourceList, TaskFailureCard } from '../diagnostic-ui.mjs'
import { streamLabel } from '../observability.mjs'
import { resumeEligibility } from '../run-presentation.mjs'
import TelemetryPanel from '../TelemetryPanel'
import type { AgentEvent, Approval, Artifact, Checkpoint, DraftMessage, Evidence, Message, RunRuntime, Task, TaskStep } from '../types'
import ApprovalCard from './ApprovalCard'
import { Icon } from '../icons'
import { Badge, Button, IconButton, Textarea } from '../ui'
import { eventLabels, eventSummary, shortID, stageLabels, statusLabels, timeLabel } from './labels'

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
  runtime: RunRuntime | null
  runtimeError: string
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
  onRefresh: () => void
  onSend: (event: FormEvent<HTMLFormElement>) => void
}

export default function TaskInspector(props: InspectorProps) {
  const { task, messages, draft, events, checkpoints, steps, artifacts, evidence, approvals, runtime, runtimeError, connection, busy } = props
  const canSend = ['inbox', 'queued', 'paused', 'failed'].includes(task.status)
  const canPause = ['queued', 'planning', 'running', 'verifying'].includes(task.status)
  const canCancel = !['completed', 'failed', 'cancelled'].includes(task.status)
  const timeline = [...events].sort((a, b) => b.seq - a.seq).slice(0, 30)
  const sortedMessages = [...messages].sort((a, b) => a.created_at.localeCompare(b.created_at))
  const failure = [...events].reverse().find((event) => event.type === 'task.failed')
  const resume = resumeEligibility(task, runtime)

  const tone = task.status === 'waiting_approval' ? 'warning' : ['planning', 'running', 'verifying'].includes(task.status) ? 'accent' : task.status === 'completed' ? 'success' : task.status === 'failed' ? 'danger' : 'neutral'

  return (
    <section className="inspector" aria-label="Инспектор задачи">
      <header className="inspector-header">
        <div className="inspector-header__title"><h2>{task.title}</h2><p><Badge tone={tone} className="status-tag">{statusLabels[task.status]}</Badge><span className="inspector-id">#{shortID(task.id)}</span><span>{task.stage ? stageLabels[task.stage] ?? task.stage : 'Ожидает запуска'}</span></p></div>
      </header>
      <div className="inspector-actions">
        {['inbox', 'failed'].includes(task.status) && <Button variant="primary" size="sm" icon={<Icon name="play" size={16} />} onClick={props.onRun} disabled={busy}>{task.status === 'failed' ? 'Повторить' : 'Запустить'}</Button>}
        {canPause && <Button variant="secondary" size="sm" icon={<Icon name="pause" size={16} />} onClick={props.onPause} disabled={busy}>Пауза</Button>}
        {task.status === 'paused' && <Button variant="primary" size="sm" onClick={props.onResume} disabled={busy || !resume.allowed} aria-describedby="workspace-resume-explanation">Продолжить</Button>}
        {canCancel && <Button variant="danger" size="sm" onClick={props.onCancel} disabled={busy}>Отменить</Button>}
        <Button variant="ghost" size="sm" icon={<Icon name="refresh" size={16} />} onClick={props.onRefresh} disabled={busy}>Обновить данные</Button>
        {busy && <span className="action-feedback" role="status">Сохраняем…</span>}
      </div>
      {task.status === 'paused' && <p id="workspace-resume-explanation" className="ui-muted">{resume.explanation}</p>}

      {task.status === 'failed' && <TaskFailureCard payload={failure?.payload} evidenceCount={evidence.length} />}

      {task.status === 'waiting_approval' && approvals.filter((approval) => approval.status === 'pending').map((approval) => <ApprovalCard key={approval.id} approval={approval} busy={busy} onApprove={() => props.onApprove(approval.id)} onReject={() => props.onReject(approval.id)} />)}

      <div className="inspector-section goal-section">
        <h3>Цель</h3>
        <p className="goal-copy">{task.description}</p>
      </div>

      <div className="inspector-section">
        {runtimeError && <p className="ui-muted" role="status">{runtimeError}</p>}
        <TelemetryPanel runtime={runtime?.task_id === task.id ? runtime : null} />
      </div>

      <div className="inspector-section">
        <h3>План <span className="section-count">{steps.length}</span></h3>
        {steps.length ? (
          <ol className="plan-list">{steps.map((step) => <li key={step.step_id} data-status={step.status}><span className={`step-state step-${step.status}`} aria-label={step.status}>{step.status === 'completed' ? '✓' : step.status === 'running' ? '•' : step.status === 'failed' ? '!' : step.status === 'skipped' ? '–' : step.position.toString()}</span><span>{step.description}</span><span className="plan-kind">{step.status === 'running' ? 'выполняется' : step.status === 'completed' ? 'готово' : step.status === 'failed' ? 'ошибка' : step.status === 'skipped' ? 'пропущено' : step.kind}</span></li>)}</ol>
        ) : <p className="inline-empty">План появится после запуска.</p>}
      </div>

      <div className="inspector-section chat-section">
        <h3>Разговор <span className="section-count">{messages.length + 1 + (draft ? 1 : 0)}</span></h3>
        <div className="message-list" aria-live="polite">
          <article className="message message-user"><p>{task.description}</p><time>{timeLabel(task.created_at)}</time></article>
          {sortedMessages.map((message) => <article className={`message message-${message.role}`} key={message.id}><span className="message-role">{message.role === 'assistant' ? 'Qwen' : message.role === 'user' ? 'Вы' : 'Система'}</span><p>{message.content}</p><time>{timeLabel(message.created_at)}</time></article>)}
          {draft && !sortedMessages.some((message) => message.id === draft.message_id) && <article className="message message-draft" key={`draft-${draft.message_id}`}><span className="message-role">Qwen · черновик</span><p>{draft.content}</p><time>Ответ проверяется</time></article>}
        </div>
        <form className="chat-form" onSubmit={props.onSend}>
          <label className="sr-only" htmlFor="chat-message">Добавить сообщение к задаче</label>
          <Textarea id="chat-message" autoGrow maxRows={6} value={props.chatText} onChange={(event) => props.setChatText(event.target.value)} placeholder={canSend ? (task.status === 'failed' ? 'Уточнить задачу и запустить повторно…' : 'Добавить уточнение…') : 'Сообщение доступно до запуска или на паузе'} rows={1} maxLength={20000} disabled={!canSend || busy} />
          <IconButton variant="primary" className="send-button" type="submit" label="Отправить сообщение" icon={<Icon name="send" />} disabled={!canSend || busy || !props.chatText.trim()} />
        </form>
        {!canSend && <p className="field-hint">Новые сообщения принимаются до запуска, в очереди и на паузе.</p>}
      </div>

      <div className="inspector-section" id="timeline">
        <div className="section-title"><h3>События</h3><span className={`stream-status ${connection !== 'live' || ['completed', 'failed', 'cancelled'].includes(task.status) ? 'is-reconnecting' : ''}`} role="status">{streamLabel(task.status, connection)}</span></div>
        {timeline.length ? <ol className="event-list">{timeline.map((event) => <li className="event-row" key={event.id}><span className="event-seq">{event.seq}</span><div className="event-copy"><strong>{eventLabels[event.type] ?? event.type}</strong>{eventSummary(event) && <p>{eventSummary(event)}</p>}<time>{timeLabel(event.created_at)}</time></div></li>)}</ol> : <p className="inline-empty">Событий пока нет.</p>}
      </div>

      <div className="inspector-section resource-section">
        <h3>Checkpoint-ы <span className="section-count">{checkpoints.length}</span></h3>
        {checkpoints.length ? <ul className="checkpoint-list">{checkpoints.slice(-4).reverse().map((checkpoint) => <li key={checkpoint.id}><span>Последнее событие <b className="mono">#{checkpoint.last_event_seq}</b></span><time>{timeLabel(checkpoint.created_at)}</time></li>)}</ul> : <p className="inline-empty">Checkpoint появится после планирования.</p>}
      </div>
      <div className="inspector-section">
        <h3>Источники <span className="section-count">{evidence.length}</span></h3>
        <SourceList sources={evidence} formatTime={timeLabel} />
      </div>
      <div className="inspector-section">
        <h3>Артефакты <span className="section-count">{artifacts.length}</span></h3>
        {artifacts.length ? <ul className="artifact-list">{artifacts.map((artifact) => <li key={artifact.id}><a href={`/api/tasks/${encodeURIComponent(task.id)}/artifacts/${encodeURIComponent(artifact.id)}`}>{artifact.name}</a><small>{artifact.mime_type} · {artifact.size_bytes.toLocaleString('ru-RU')} байт</small></li>)}</ul> : <p className="inline-empty">Сохранённых файлов пока нет.</p>}
      </div>
    </section>
  )
}
