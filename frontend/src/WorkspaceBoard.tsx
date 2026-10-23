import type { Task, TaskStatus } from './types'

export default function WorkspaceBoard({ columns, tasks, selectedID, statusLabels, stageLabels, onSelect, formatTime }: {
  columns: { id: string; title: string; statuses: TaskStatus[] }[]
  tasks: Task[]
  selectedID: string
  statusLabels: Record<TaskStatus, string>
  stageLabels: Record<string, string>
  onSelect: (id: string) => void
  formatTime: (value: string) => string
}) {
  return <section className="board-grid" aria-label="Kanban доска">
    {columns.map((column) => {
      const items = tasks.filter((task) => column.statuses.includes(task.status))
      return <section className={`board-column column-${column.id}`} id={`column-${column.id}`} key={column.id} aria-labelledby={`heading-${column.id}`}>
        <header className="column-heading"><div className="column-title-wrap"><span className={`column-marker marker-${column.id}`} /><h2 id={`heading-${column.id}`}>{column.title}</h2></div><span className="column-count">{items.length.toString().padStart(2, '0')}</span></header>
        <div className="column-items">
          {items.map((task) => <button className={`task-card ${selectedID === task.id ? 'is-selected' : ''}`} key={task.id} type="button" onClick={() => onSelect(task.id)} aria-pressed={selectedID === task.id}>
            <div className="task-card-top"><span className={`status-tag status-${task.status}`}>{statusLabels[task.status]}</span><span className="task-id">{task.id.slice(0, 8)}</span></div>
            <h3>{task.title}</h3><p>{task.description}</p>
            <div className="task-card-bottom"><span>{task.stage ? stageLabels[task.stage] ?? task.stage : 'Без этапа'}</span><time dateTime={task.updated_at}>{formatTime(task.updated_at)}</time></div>
          </button>)}
          {items.length === 0 && <div className="column-empty"><span className="empty-dash">—</span><span>Пока пусто</span></div>}
        </div>
      </section>
    })}
  </section>
}
