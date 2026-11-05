import { useState } from 'react'
import { Icon } from './icons'
import { Button } from './ui'
import type { Conversation } from './types'

/** Conversation history for the shell sidebar: search plus date-grouped titles. */
export default function ChatSidebar({ conversations, error, loading, selected, onSelect, onRetry }: {
  conversations: Conversation[]
  error: string
  loading: boolean
  selected: string
  onSelect: (id: string) => void
  onNew?: () => void
  onRetry: () => void
}) {
  const [query, setQuery] = useState('')
  const visible = conversations.filter((item) => !item.archived_at && item.title.toLocaleLowerCase().includes(query.toLocaleLowerCase()))
  const now = new Date()
  const today = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime()
  const yesterday = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 1).getTime()
  const groups = new Map<string, Conversation[]>()
  for (const item of visible) {
    const timestamp = item.updated_at || item.created_at
    const date = timestamp ? new Date(timestamp) : null
    const time = date?.getTime()
    const label = time === undefined || !Number.isFinite(time) ? 'Без даты' : time >= today ? 'Сегодня' : time >= yesterday ? 'Вчера' : date!.toLocaleDateString('ru-RU', { month: 'long', year: 'numeric' })
    groups.set(label, [...(groups.get(label) || []), item])
  }
  return <div className="chat-sidebar">
    {conversations.length > 6 && <label className="chat-search"><span className="sr-only">Найти разговор</span><Icon name="search" size={16} /><input type="search" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Поиск по чатам" /></label>}
    {error ? <div className="chat-sidebar__error" role="status"><p>{error}</p><Button size="sm" variant="ghost" onClick={onRetry}>Повторить загрузку</Button></div> : loading && conversations.length === 0 ? <p className="chat-sidebar__note" role="status">Загрузка…</p> : null}
    <nav aria-label="История разговоров" className="chat-conversations">
      {Array.from(groups, ([label, items]) => <section key={label} aria-label={label}>
        <h3>{label}</h3>
        {items.map((item) => <a href={`#/chat/${encodeURIComponent(item.id)}`} key={item.id} aria-current={selected === item.id ? 'page' : undefined} title={item.title || 'Новый разговор'} onClick={(event) => { event.preventDefault(); onSelect(item.id) }}>
          <span>{item.title || 'Новый разговор'}</span>
          {item.mode === 'research' && <Icon name="sources" size={14} title="Исследование" />}
          {Number.isFinite(Date.parse(item.updated_at || item.created_at)) && <time dateTime={item.updated_at || item.created_at} className="sr-only">{new Date(item.updated_at || item.created_at).toLocaleDateString('ru-RU', { day: 'numeric', month: 'short' })}</time>}
        </a>)}
      </section>)}
      {!loading && !error && visible.length === 0 && <p className="chat-sidebar__note">{query ? 'Ничего не найдено.' : 'Разговоров пока нет.'}</p>}
    </nav>
  </div>
}
