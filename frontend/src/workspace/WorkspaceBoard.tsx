import type { Task, TaskStatus } from '../types'
import { Badge, type BadgeTone } from '../ui'

const tone = (status: TaskStatus): BadgeTone => status === 'waiting_approval' ? 'warning'
  : ['planning', 'running', 'verifying'].includes(status) ? 'accent'
    : status === 'completed' ? 'success' : status === 'failed' ? 'danger' : 'neutral'

/** Tasks grouped by lifecycle column. Empty groups are omitted. */
export default function WorkspaceBoard({ columns, tasks, selectedID, statusLabels, stageLabels, onSelect, formatTime }: {
  columns: { id: string; title: string; statuses: TaskStatus[] }[]
  tasks: Task[]
  selectedID: string
  statusLabels: Record<TaskStatus, string>
  stageLabels: Record<string, string>
  onSelect: (id: string) => void
  formatTime: (value: string) => string
}) {
  return <section className="board-grid" aria-label="Список задач">
    {columns.map((column) => {
      const items = tasks.filter((task) => column.statuses.includes(task.status))
      if (!items.length) return null
      return <section className={`board-column column-${column.id}`} id={`column-${column.id}`} key={column.id} aria-labelledby={`heading-${column.id}`}>
        <header className="column-heading"><h2 id={`heading-${column.id}`}>{column.title}</h2><span className="column-count">{items.length}</span></header>
        <div className="column-items">
          {items.map((task) => <button className={`task-card ${selectedID === task.id ? 'is-selected' : ''} ${task.status === 'waiting_approval' ? 'is-approval-required' : ''}`} key={task.id} type="button" onClick={() => onSelect(task.id)} aria-pressed={selectedID === task.id}>
            <span className="task-card__main"><strong>{task.title}</strong><span className="task-card__description">{task.description}</span></span>
            <span className="task-card__meta">
              <Badge tone={tone(task.status)}>{task.status === 'waiting_approval' ? 'Нужно ваше решение' : statusLabels[task.status]}</Badge>
              <span>{task.stage ? stageLabels[task.stage] ?? task.stage : 'Без этапа'} · <time dateTime={task.updated_at}>{formatTime(task.updated_at)}</time></span>
            </span>
          </button>)}
        </div>
      </section>
    })}
  </section>
}
