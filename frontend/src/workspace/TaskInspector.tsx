import type { FormEvent } from 'react'
import { SourceList, TaskFailureCard } from '../diagnostic-ui.mjs'
import { streamLabel } from '../observability.mjs'
import { resumeEligibility } from '../run-presentation.mjs'
import TelemetryPanel from '../TelemetryPanel'
import type { AgentEvent, Approval, Artifact, Checkpoint, DraftMessage, Evidence, Message, RunRuntime, Task, TaskStep } from '../types'
import ApprovalCard from './ApprovalCard'
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

  return (
    <section className="inspector" aria-label="Инспектор задачи">
      <header className="inspector-header">
        <div><div className="eyebrow">ЗАДАЧА <span className="mono">{shortID(task.id)}</span></div><h2>{task.title}</h2></div>
        <span className={`status-tag status-${task.status}`}>{statusLabels[task.status]}</span>
      </header>
      <div className="inspector-actions">
        {['inbox', 'failed'].includes(task.status) && <button className="button button-primary" type="button" onClick={props.onRun} disabled={busy}>{task.status === 'failed' ? 'Повторить' : 'Запустить'}</button>}
        {canPause && <button className="button button-secondary" type="button" onClick={props.onPause} disabled={busy}>Пауза</button>}
        {task.status === 'paused' && <button className="button button-primary" type="button" onClick={props.onResume} disabled={busy || !resume.allowed} aria-describedby="workspace-resume-explanation">Продолжить</button>}
        {canCancel && <button className="button button-danger-quiet" type="button" onClick={props.onCancel} disabled={busy}>Отменить</button>}
        <button className="button button-quiet" type="button" onClick={props.onRefresh} disabled={busy}>Обновить данные</button>
        {busy && <span className="action-feedback" role="status">Сохраняем…</span>}
      </div>
      {task.status === 'paused' && <p id="workspace-resume-explanation" className="chat-muted">{resume.explanation}</p>}

      {task.status === 'failed' && <TaskFailureCard payload={failure?.payload} evidenceCount={evidence.length} />}

      {task.status === 'waiting_approval' && approvals.filter((approval) => approval.status === 'pending').map((approval) => <ApprovalCard key={approval.id} approval={approval} busy={busy} onApprove={() => props.onApprove(approval.id)} onReject={() => props.onReject(approval.id)} />)}

      <div className="inspector-section goal-section">
        <div className="section-kicker">ЦЕЛЬ</div>
        <p className="goal-copy">{task.description}</p>
        <div className="stage-row"><span>Текущий этап</span><strong>{task.stage ? stageLabels[task.stage] ?? task.stage : 'Ожидает запуска'}</strong></div>
      </div>

      <div className="inspector-section">
        {runtimeError && <p className="chat-muted" role="status">{runtimeError}</p>}
        <TelemetryPanel runtime={runtime?.task_id === task.id ? runtime : null} />
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
