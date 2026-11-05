import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { api } from './api'
import type { Conversation, Space, SpaceContextItem, SpaceResearchRun } from './types'
import AppShell, { TopBar } from './AppShell'
import { Icon } from './icons'
import { Badge, Button, EmptyState, IconButton, Input, plural, Spinner, Textarea } from './ui'
import './styles/spaces.css'

function spaceID() {
  const match = window.location.hash.match(/^#\/spaces\/([^/?]+)/)
  try { return match ? decodeURIComponent(match[1]) : '' } catch { return '' }
}

function formatDate(value?: string) {
  const time = Date.parse(value ?? '')
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

  const projectSidebar = <div className="spaces-nav" aria-label="Проекты">
    <div className="spaces-nav-head"><h2>Проекты</h2><IconButton label="Создать проект" icon={<Icon name="plus" size={18} />} onClick={() => setCreating(true)} /></div>
    <nav>{spaces.map((item) => <a key={item.id} href={`#/spaces/${encodeURIComponent(item.id)}`} aria-current={item.id === selected ? 'page' : undefined} onClick={(event) => { event.preventDefault(); select(item.id) }}><Icon name="projects" size={16} /><span>{item.name || item.title}</span></a>)}</nav>
    {!loading && !spaces.length && <p className="spaces-empty">Проектов пока нет.</p>}
  </div>

  return <AppShell activeSection="projects" sidebar={projectSidebar} mobileSidebar={projectSidebar}>
    <main className="spaces-page">
      <TopBar title={active ? active.name || active.title : 'Проекты'} subtitle={active ? `${conversations.length} ${plural(conversations.length, 'чат', 'чата', 'чатов')} · ${context.length} ${plural(context.length, 'материал', 'материала', 'материалов')} · ${research.length} ${plural(research.length, 'исследование', 'исследования', 'исследований')}` : undefined} actions={active && <Button variant="primary" size="sm" icon={<Icon name="plus" />} onClick={() => void newChat()} disabled={busy}>Новый чат в проекте</Button>} />
      <section className="spaces-main" aria-live="polite">
        {error && <div className="workspace-notice" role="alert"><Icon name="alert" size={18} /><span>{error}</span><Button size="sm" variant="ghost" onClick={() => void load()}>Повторить</Button></div>}
        {loading ? <div className="spaces-loading"><Spinner /></div> : active ? <>
          <header className="spaces-heading"><p>{active.description || 'Добавьте описание, чтобы держать цель проекта в поле зрения.'}</p><p className="ui-muted">Контекст проекта ограничен 16 KB и собирается из завершённых ответов с подтверждёнными источниками.</p></header>
          <section className="spaces-section" aria-labelledby="spaces-conversations"><h3 id="spaces-conversations">Чаты <span>{conversations.length}</span></h3>{conversations.length ? <ul className="spaces-list">{conversations.slice(0, 8).map((item) => <li key={item.id}><a href={`#/chat/${encodeURIComponent(item.id)}`}><Icon name={item.mode === 'research' ? 'sources' : 'chat'} size={16} /><span>{item.title || 'Новый чат'}</span><small>{item.mode === 'research' ? 'Deep Research' : 'Чат'} · {formatDate(item.updated_at || item.created_at)}</small></a></li>)}</ul> : <p className="spaces-muted">В этом проекте пока нет чатов.</p>}</section>
          <section className="spaces-section" aria-labelledby="spaces-context"><h3 id="spaces-context">Проверенные материалы <span>{context.length}</span></h3>{context.length ? <div className="spaces-context-list">{context.map((item) => <article key={item.run_id}><div className="spaces-context-meta"><Badge tone="success">Проверено</Badge><time>{formatDate(item.updated_at)}</time></div><h4>{item.conversation_title}</h4><p>{item.summary}</p><details><summary>{item.sources.length} источников</summary><ul>{item.sources.map((source) => <li key={source.evidence_id}><a href={source.canonical_url} target="_blank" rel="noreferrer">{source.title || source.canonical_url}</a></li>)}</ul></details></article>)}</div> : <p className="spaces-muted">После завершённого исследования с проверенными ссылками материалы появятся здесь.</p>}</section>
          <section className="spaces-section" aria-labelledby="spaces-research"><h3 id="spaces-research">Исследования <span>{research.length}</span></h3>{research.length ? <ul className="spaces-list">{research.slice(0, 6).map((item) => <li key={item.run.id}><div><Icon name="sources" size={16} /><span>{item.goal || item.conversation_title}</span><small>{item.verification_status === 'supported' ? 'Проверено' : item.verification_status || 'Статус не указан'} · {formatDate(item.completed_at)}</small></div></li>)}</ul> : <p className="spaces-muted">Здесь будет история исследовательских запусков.</p>}</section>
        </> : <EmptyState icon={<Icon name="projects" />} title="Один проект — один долговечный контекст" description="Создайте проект, чтобы исследования, источники и решения не терялись между чатами." action={<Button variant="primary" icon={<Icon name="plus" />} onClick={() => setCreating(true)}>Создать проект</Button>} />}
      </section>
      {creating && <ProjectDialog name={name} description={description} busy={busy} onName={setName} onDescription={setDescription} onCancel={() => setCreating(false)} onSubmit={() => void create()} />}
    </main>
  </AppShell>
}

function ProjectDialog({ name, description, busy, onName, onDescription, onCancel, onSubmit }: { name: string; description: string; busy: boolean; onName: (value: string) => void; onDescription: (value: string) => void; onCancel: () => void; onSubmit: () => void }) {
  const ref = useRef<HTMLDialogElement>(null)
  useLayoutEffect(() => {
    const previous = document.activeElement as HTMLElement | null
    const dialog = ref.current
    dialog?.showModal()
    return () => { dialog?.close(); previous?.focus({ preventScroll: true }) }
  }, [])
  return <dialog ref={ref} className="spaces-modal" aria-labelledby="space-dialog-title" onCancel={onCancel} onClick={(event) => { if (event.target === event.currentTarget) onCancel() }}>
    <form onSubmit={(event) => { event.preventDefault(); onSubmit() }}>
      <h2 id="space-dialog-title">Новый проект</h2>
      <label className="ui-field">Название<Input autoFocus value={name} maxLength={80} onChange={(event) => onName(event.target.value)} placeholder="Например, запуск продукта" /></label>
      <label className="ui-field">Описание <small>необязательно</small><Textarea value={description} maxLength={280} onChange={(event) => onDescription(event.target.value)} placeholder="Цель, ограничения и важные решения" rows={3} /></label>
      <div className="spaces-modal-actions"><Button variant="ghost" onClick={onCancel}>Отмена</Button><Button variant="primary" type="submit" disabled={!name.trim() || busy} loading={busy}>{busy ? 'Создаём…' : 'Создать'}</Button></div>
    </form>
  </dialog>
}
