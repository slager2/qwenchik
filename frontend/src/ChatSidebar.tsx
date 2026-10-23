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
  return <>
    <a className="chat-wordmark" href="#/chat" onClick={(event) => { event.preventDefault(); onNew() }}>Q<span>qwenchik</span></a>
    <button className="chat-new" type="button" onClick={onNew}>+ Новый чат</button>
    <label className="chat-search">Найти разговор<input type="search" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="По названию" /></label>
    {error ? <div className="chat-resource-error" role="status"><p>{error}</p><button type="button" onClick={onRetry}>Повторить загрузку</button></div> : loading && conversations.length === 0 ? <p className="chat-muted" role="status">Загрузка…</p> : null}
    <nav aria-label="Разговоры" className="chat-conversations">
      {visible.map((item) => <a href={`#/chat/${encodeURIComponent(item.id)}`} key={item.id} aria-current={selected === item.id ? 'page' : undefined} onClick={(event) => { event.preventDefault(); onSelect(item.id) }}><span>{item.title || 'Новый разговор'}</span><small>{item.mode === 'research' ? 'Исследование' : 'Чат'}</small></a>)}
      {conversations.length > 0 && visible.length === 0 && <p className="chat-muted">{query ? 'Разговоры не найдены.' : 'Ваши разговоры появятся здесь.'}</p>}
    </nav>
    <a className="chat-workspace-link" href="#/workspace">Рабочее пространство <span aria-hidden="true">↗</span></a>
    <p className="chat-sidebar-note">Задачи, подтверждения и память</p>
  </>
}
