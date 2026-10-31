import { useCallback, useEffect, useMemo, useState } from 'react'
import { api } from './api'
import type { Conversation, Space, SpaceContextItem, SpaceResearchRun } from './types'
import './spaces.css'

function spaceID() {
  const match = window.location.hash.match(/^#\/spaces\/([^/?]+)/)
  try { return match ? decodeURIComponent(match[1]) : '' } catch { return '' }
}

function formatDate(value: string) {
  const time = Date.parse(value)
  return Number.isFinite(time) ? new Date(time).toLocaleDateString('ru-RU', { day: 'numeric', month: 'short' }) : '—'
}

export default function SpacesRoute() {
  const [selected, setSelected] = useState(spaceID)
  const [spaces, setSpaces] = useState<Space[]>([])
  const [conversations, setConversations] = useState<Conversation[]>([])
  const [context, setContext] = useState<SpaceContextItem[]>([])
  const [research, setResearch] = useState<SpaceResearchRun[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [creating, setCreating] = useState(false)
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    setLoading(true); setError('')
    try {
      const next = await api.spaces()
      setSpaces(next)
      const current = selected || next[0]?.id || ''
      if (current && current !== selected) { setSelected(current); window.location.hash = `/spaces/${encodeURIComponent(current)}` }
      if (!current) { setConversations([]); setContext([]); setResearch([]); return }
      const [items, saved, runs] = await Promise.all([api.spaceConversations(current), api.spaceContext(current), api.spaceResearch(current)])
      setConversations(items); setContext(saved); setResearch(runs)
    } catch (caught) { setError(caught instanceof Error ? caught.message : 'Не удалось загрузить пространства.') }
    finally { setLoading(false) }
  }, [selected])

  useEffect(() => { void load() }, [load])
  useEffect(() => {
    const navigate = () => setSelected(spaceID())
    window.addEventListener('hashchange', navigate)
    return () => window.removeEventListener('hashchange', navigate)
  }, [])

  const active = useMemo(() => spaces.find((item) => item.id === selected) || null, [spaces, selected])
  const select = (id: string) => { setSelected(id); window.location.hash = `/spaces/${encodeURIComponent(id)}` }
  const create = async () => {
    if (!name.trim() || busy) return
    setBusy(true); setError('')
    try {
      const item = await api.createSpace(name.trim(), description.trim())
      setName(''); setDescription(''); setCreating(false); setSpaces((items) => [item, ...items]); select(item.id)
    } catch (caught) { setError(caught instanceof Error ? caught.message : 'Не удалось создать пространство.') }
    finally { setBusy(false) }
  }
  const newChat = async () => {
    if (!active || busy) return
    setBusy(true); setError('')
    try {
      const item = await api.createConversation('Новый чат', 'chat', crypto.randomUUID(), { space_id: active.id })
      window.location.hash = `/chat/${encodeURIComponent(item.id)}`
    } catch (caught) { setError(caught instanceof Error ? caught.message : 'Не удалось создать чат.') }
    finally { setBusy(false) }
  }

  return <main className="spaces-shell">
    <header className="spaces-topbar"><a className="spaces-brand" href="#/chat"><span aria-hidden="true">Q</span><strong>qwenchik</strong></a><div className="spaces-top-actions"><a href="#/chat" className="spaces-quiet-link">Чат</a><a href="#/workspace" className="spaces-quiet-link">Операции</a><button className="spaces-primary" type="button" onClick={() => setCreating(true)}>+ Пространство</button></div></header>
    <div className="spaces-layout">
      <aside className="spaces-nav" aria-label="Пространства"><div className="spaces-nav-head"><div><span className="spaces-kicker">КОНТЕКСТ</span><h1>Пространства</h1></div><button className="spaces-icon-button" type="button" aria-label="Создать пространство" onClick={() => setCreating(true)}>+</button></div>
        <p className="spaces-nav-copy">Исследования и решения остаются рядом с чатами — агент видит только подтверждённый контекст выбранного пространства.</p>
        <nav>{spaces.map((item) => <a key={item.id} href={`#/spaces/${encodeURIComponent(item.id)}`} className={item.id === selected ? 'is-active' : ''} onClick={(event) => { event.preventDefault(); select(item.id) }}><span className="spaces-nav-dot" aria-hidden="true" /><span><strong>{item.name || item.title}</strong><small>{item.description || 'Без описания'}</small></span></a>)}</nav>
        {!loading && !spaces.length && <p className="spaces-empty">Создайте первое пространство для долгого проекта.</p>}
      </aside>
      <section className="spaces-main" aria-live="polite">
        {error && <div className="spaces-error" role="alert">{error}<button type="button" onClick={() => void load()}>Повторить</button></div>}
        {loading ? <div className="spaces-loading">Загружаю контекст…</div> : active ? <>
          <header className="spaces-heading"><div><span className="spaces-kicker">ПРОСТРАНСТВО</span><h2>{active.name || active.title}</h2><p>{active.description || 'Добавьте описание, чтобы держать цель проекта в поле зрения.'}</p></div><button className="spaces-primary" type="button" onClick={() => void newChat()} disabled={busy}>Новый чат в пространстве</button></header>
          <div className="spaces-stats"><div><strong>{conversations.length}</strong><span>чатов</span></div><div><strong>{context.length}</strong><span>проверенных материалов</span></div><div><strong>{research.length}</strong><span>исследований</span></div><div className="spaces-stats-note">Контекст ограничен 16 KB и собирается из завершённых ответов с подтверждёнными источниками.</div></div>
          <div className="spaces-grid">
            <section className="spaces-panel spaces-conversations"><header><div><span className="spaces-kicker">РАЗГОВОРЫ</span><h3>Последние чаты</h3></div><span className="spaces-count">{conversations.length}</span></header>{conversations.length ? <ul>{conversations.slice(0, 8).map((item) => <li key={item.id}><a href={`#/chat/${encodeURIComponent(item.id)}`}><strong>{item.title || 'Новый чат'}</strong><small>{item.mode === 'research' ? 'Deep Research' : 'Чат'} · {formatDate(item.updated_at || item.created_at)}</small></a></li>)}</ul> : <p className="spaces-muted">В этом пространстве пока нет чатов.</p>}</section>
            <section className="spaces-panel spaces-context"><header><div><span className="spaces-kicker">ПАМЯТЬ АГЕНТА</span><h3>Проверенный контекст</h3></div><span className="spaces-count">{context.length}</span></header>{context.length ? <div className="spaces-context-list">{context.map((item) => <article key={item.run_id}><div className="spaces-context-meta"><span>✓ supported</span><time>{formatDate(item.updated_at)}</time></div><h4>{item.conversation_title}</h4><p>{item.summary}</p><details><summary>{item.sources.length} источников</summary><ul>{item.sources.map((source) => <li key={source.evidence_id}><a href={source.canonical_url} target="_blank" rel="noreferrer">{source.title || source.canonical_url}</a></li>)}</ul></details></article>)}</div> : <p className="spaces-muted">После завершённого исследования с проверенными ссылками материалы появятся здесь.</p>}</section>
            <section className="spaces-panel spaces-research"><header><div><span className="spaces-kicker">ИСТОРИЯ</span><h3>Исследования</h3></div><span className="spaces-count">{research.length}</span></header>{research.length ? <ul>{research.slice(0, 6).map((item) => <li key={item.run.id}><span className="spaces-status" aria-hidden="true" /><div><strong>{item.goal || item.conversation_title}</strong><small>{item.verification_status || 'Статус не указан'} · {formatDate(item.completed_at || item.run.updated_at)}</small></div></li>)}</ul> : <p className="spaces-muted">Здесь будет история исследовательских запусков.</p>}</section>
          </div>
        </> : <div className="spaces-empty-state"><span className="spaces-kicker">НАЧНИТЕ С ПРОСТРАНСТВА</span><h2>Один проект — один долговечный контекст.</h2><p>Создайте пространство, чтобы исследования, источники и решения не терялись между чатами.</p><button className="spaces-primary" type="button" onClick={() => setCreating(true)}>Создать пространство</button></div>}
      </section>
    </div>
    {creating && <div className="spaces-modal-backdrop" role="presentation" onMouseDown={(event) => { if (event.target === event.currentTarget) setCreating(false) }}><form className="spaces-modal" role="dialog" aria-modal="true" aria-labelledby="space-dialog-title" onSubmit={(event) => { event.preventDefault(); void create() }}><span className="spaces-kicker">НОВОЕ ПРОСТРАНСТВО</span><h2 id="space-dialog-title">Дайте контексту имя</h2><label>Название<input autoFocus value={name} maxLength={80} onChange={(event) => setName(event.target.value)} placeholder="Например, запуск продукта" /></label><label>Описание <span>необязательно</span><textarea value={description} maxLength={280} onChange={(event) => setDescription(event.target.value)} placeholder="Цель, ограничения и важные решения" rows={3} /></label><div className="spaces-modal-actions"><button className="spaces-quiet-button" type="button" onClick={() => setCreating(false)}>Отмена</button><button className="spaces-primary" type="submit" disabled={!name.trim() || busy}>{busy ? 'Создаём…' : 'Создать'}</button></div></form></div>}
  </main>
}
