import { useEffect, useRef, useState } from 'react'
import { Icon } from './icons'

export type CommandPaletteAction = {
  id: string
  label: string
  hint?: string
  shortcut?: string
  href?: string
  onSelect?: () => void
}

type CommandPaletteProps = {
  actions?: CommandPaletteAction[]
  onFocusComposer?: () => void
}

const isEditable = (target: EventTarget | null) => target instanceof HTMLElement && (target.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(target.tagName))

/** Native-dialog command palette; consumers can replace its small presentation-only action list. */
export default function CommandPalette({ actions, onFocusComposer }: CommandPaletteProps) {
  const dialogRef = useRef<HTMLDialogElement>(null)
  const [open, setOpen] = useState(false)
  const defaults: CommandPaletteAction[] = [
    { id: 'new-chat', label: 'Новый чат', hint: 'Начать разговор', href: '#/chat' },
    { id: 'projects', label: 'Открыть проекты', hint: 'Контекст и материалы', href: '#/spaces' },
    { id: 'tasks', label: 'Открыть задачи', hint: 'Очередь и подтверждения', href: '#/workspace' },
    { id: 'focus-composer', label: 'Перейти к сообщению', hint: 'Фокус в поле ввода', shortcut: '/', onSelect: onFocusComposer },
  ]
  const commands = actions ?? defaults

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault()
        setOpen(true)
      }
      if (event.key === '/' && !isEditable(event.target)) {
        event.preventDefault()
        onFocusComposer?.()
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [onFocusComposer])

  useEffect(() => {
    const dialog = dialogRef.current
    if (!dialog) return
    if (open && !dialog.open) dialog.showModal()
    if (!open && dialog.open) dialog.close()
  }, [open])

  const close = () => setOpen(false)
  const select = (action: CommandPaletteAction) => {
    action.onSelect?.()
    close()
  }

  return <>
    <button className="q-command-trigger" type="button" onClick={() => setOpen(true)} aria-label="Открыть командную палитру">
      <Icon name="search" /><span>Команды</span><kbd>⌘K</kbd>
    </button>
    <dialog ref={dialogRef} className="q-command-palette" aria-labelledby="q-command-palette-title" onClose={close} onClick={(event) => { if (event.target === event.currentTarget) close() }}>
      <header className="q-command-palette__header"><div><p className="q-command-palette__eyebrow">Быстрые действия</p><h2 id="q-command-palette-title">Командная палитра</h2></div><button type="button" className="q-icon-button" onClick={close} aria-label="Закрыть командную палитру"><Icon name="close" /></button></header>
      <div className="q-command-palette__list" role="list">
        {commands.map((action) => action.href
          ? <a key={action.id} className="q-command-palette__action" href={action.href} role="listitem" onClick={() => select(action)}><span><strong>{action.label}</strong>{action.hint && <small>{action.hint}</small>}</span>{action.shortcut && <kbd>{action.shortcut}</kbd>}</a>
          : <button key={action.id} className="q-command-palette__action" type="button" role="listitem" onClick={() => select(action)}><span><strong>{action.label}</strong>{action.hint && <small>{action.hint}</small>}</span>{action.shortcut && <kbd>{action.shortcut}</kbd>}</button>,
        )}
      </div>
    </dialog>
  </>
}
