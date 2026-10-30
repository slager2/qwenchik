import { useState } from 'react'
import type { Conversation } from './types'

export default function ChatSidebar({ conversations, error, loading, selected, onSelect, onNew, onRetry }: {
  conversations: Conversation[]
  error: string
  loading: boolean
  selected: string
  onSelect: (id: string) => void
  onNew: () => void
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
  return <>
    <a className="chat-wordmark" href="#/chat" onClick={(event) => { event.preventDefault(); onNew() }}>Q<span>qwenchik</span></a>
    <button className="chat-new" type="button" onClick={onNew}>+ Новый чат</button>
    <label className="chat-search">Найти разговор<input type="search" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="По названию" /></label>
    {error ? <div className="chat-resource-error" role="status"><p>{error}</p><button type="button" onClick={onRetry}>Повторить загрузку</button></div> : loading && conversations.length === 0 ? <p className="chat-muted" role="status">Загрузка…</p> : null}
    <nav aria-label="Разговоры" className="chat-conversations">
      {Array.from(groups, ([label, items]) => <section key={label} aria-label={label}><h3 className="chat-muted">{label}</h3>{items.map((item) => <a href={`#/chat/${encodeURIComponent(item.id)}`} key={item.id} aria-current={selected === item.id ? 'page' : undefined} onClick={(event) => { event.preventDefault(); onSelect(item.id) }}><span>{item.title || 'Новый разговор'}</span><small>{item.mode === 'research' ? 'Исследование' : 'Чат'}{Number.isFinite(Date.parse(item.updated_at || item.created_at)) && <> · <time dateTime={item.updated_at || item.created_at}>{new Date(item.updated_at || item.created_at).toLocaleDateString('ru-RU', { day: 'numeric', month: 'short' })}</time></>}</small></a>)}</section>)}
      {!loading && !error && visible.length === 0 && <p className="chat-muted">{query ? 'Разговоры не найдены. Измените запрос.' : 'Разговоров пока нет. Нажмите «Новый чат», чтобы начать.'}</p>}
    </nav>
    <a className="chat-workspace-link" href="#/workspace">Рабочее пространство <span aria-hidden="true">↗</span></a>
    <p className="chat-sidebar-note">Задачи, подтверждения и память</p>
  </>
}
