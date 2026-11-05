import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react'
import type { Task } from '../types'
import { Icon } from '../icons'
import { columns } from './labels'

interface PaletteProps {
  tasks: Task[]
  onClose: () => void
  onExecute: (command: string) => void
}

export default function TaskCommandPalette({ tasks, onClose, onExecute }: PaletteProps) {
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

  function handleKey(event: KeyboardEvent<HTMLInputElement>) {
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
        <div className="palette-search"><Icon name="search" size={18} className="palette-icon" /><input ref={inputRef} value={query} onChange={(event) => { setQuery(event.target.value); setActive(0) }} onKeyDown={handleKey} placeholder="Найти команду…" aria-label="Найти команду" /><kbd className="ui-kbd">Esc</kbd></div>
        <div className="palette-results" role="listbox" aria-label="Команды">
          {commands.map((command, index) => <button className={`palette-option ${active === index ? 'is-active' : ''}`} type="button" role="option" aria-selected={active === index} key={command.id} onMouseEnter={() => setActive(index)} onClick={() => onExecute(command.id)}><span>{command.label}</span><small>{command.hint}</small></button>)}
          {commands.length === 0 && <div className="palette-empty">Команд не найдено</div>}
        </div>
        <footer className="palette-footer"><span>{tasks.length} задач</span><span><kbd className="ui-kbd">↑</kbd><kbd className="ui-kbd">↓</kbd> навигация <kbd className="ui-kbd">↵</kbd> выбрать</span></footer>
      </section>
    </div>
  )
}
