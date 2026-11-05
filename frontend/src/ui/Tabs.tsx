import { useRef, type KeyboardEvent, type ReactNode } from 'react'
import { cx } from './cx'

export type TabItem<T extends string> = { id: T; label: ReactNode; count?: number | string }

/** Underlined tab list with roving arrow-key focus. Panels are rendered by the caller. */
export default function Tabs<T extends string>({ items, value, onChange, label, idPrefix, className }: { items: TabItem<T>[]; value: T; onChange: (id: T) => void; label: string; idPrefix: string; className?: string }) {
  const list = useRef<HTMLDivElement>(null)
  const move = (event: KeyboardEvent<HTMLDivElement>) => {
    if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return
    event.preventDefault()
    const index = items.findIndex((item) => item.id === value)
    const next = event.key === 'Home' ? 0 : event.key === 'End' ? items.length - 1 : (index + (event.key === 'ArrowRight' ? 1 : -1) + items.length) % items.length
    onChange(items[next].id)
    list.current?.querySelectorAll<HTMLButtonElement>('[role="tab"]')[next]?.focus()
  }
  return <div ref={list} className={cx('ui-tabs', className)} role="tablist" aria-label={label} onKeyDown={move}>
    {items.map((item) => <button key={item.id} type="button" role="tab" id={`${idPrefix}-tab-${item.id}`} aria-controls={`${idPrefix}-panel-${item.id}`} aria-selected={item.id === value} tabIndex={item.id === value ? 0 : -1} className="ui-tabs__tab" onClick={() => onChange(item.id)}>
      {item.label}{item.count !== undefined && <span className="ui-tabs__count">{item.count}</span>}
    </button>)}
  </div>
}
