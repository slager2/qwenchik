import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { api } from '../api'
import type { Memory, MemoryMatch, MemoryStatus } from '../types'
import { shortID, timeLabel } from './labels'

const memoryStatusLabels: Record<MemoryStatus, string> = {
  candidate: 'Кандидат', accepted: 'Подтверждена', stale: 'Устарела', superseded: 'Заменена', rejected: 'Отклонена',
}

export default function MemoryManager({ onClose }: { onClose: () => void }) {
  const [memories, setMemories] = useState<Memory[]>([])
  const [query, setQuery] = useState('')
  const [matches, setMatches] = useState<MemoryMatch[]>([])
  const [kind, setKind] = useState('preference')
  const [text, setText] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const refresh = useCallback(async () => {
    try {
      setMemories(await api.memories())
      setError('')
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : 'Не удалось загрузить память')
    }
  }, [])

  useEffect(() => { void refresh() }, [refresh])

  async function createCandidate(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!text.trim()) return
    setBusy(true)
    try {
      await api.createMemory(kind, text.trim())
      setText('')
      await refresh()
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : 'Не удалось сохранить память')
    } finally {
      setBusy(false)
    }
  }

  async function search(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setBusy(true)
    try {
      setMatches(await api.searchMemories(query.trim()))
      setError('')
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : 'Не удалось найти память')
    } finally {
      setBusy(false)
    }
  }

  async function decide(id: string, status: Extract<MemoryStatus, 'accepted' | 'rejected' | 'stale' | 'superseded'>) {
    setBusy(true)
    try {
      await api.decideMemory(id, status)
      await refresh()
      if (query.trim()) setMatches(await api.searchMemories(query.trim()))
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : 'Не удалось обновить память')
    } finally {
      setBusy(false)
    }
  }

  async function save(memory: Memory, nextKind: string, nextText: string) {
    setBusy(true)
    try {
      await api.updateMemory(memory.id, nextKind, nextText, memory.metadata)
      await refresh()
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : 'Не удалось изменить память')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="memory-backdrop" role="presentation" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose() }}>
      <section className="memory-manager" role="dialog" aria-modal="true" aria-labelledby="memory-title">
        <header className="memory-manager-header"><div><span className="eyebrow">ЛОКАЛЬНЫЙ КОНТЕКСТ</span><h2 id="memory-title">Память</h2><p>В задачи попадают только подтверждённые записи. Новые заметки сначала требуют вашего решения.</p></div><button className="icon-button" type="button" onClick={onClose} aria-label="Закрыть память">×</button></header>
        {error && <div className="notice notice-error" role="alert">{error}</div>}
        <form className="memory-create" onSubmit={createCandidate}>
          <label className="field-label" htmlFor="memory-kind">Тип</label>
          <select id="memory-kind" className="text-field" value={kind} onChange={(event) => setKind(event.target.value)}><option value="preference">Предпочтение</option><option value="fact">Факт</option><option value="workflow">Процесс</option><option value="constraint">Ограничение</option></select>
          <label className="field-label" htmlFor="memory-text">Новая запись</label>
          <textarea id="memory-text" className="text-field text-area" value={text} onChange={(event) => setText(event.target.value)} maxLength={4000} rows={2} placeholder="Например: ответы — кратко, на русском языке" />
          <button className="button button-primary" type="submit" disabled={busy || !text.trim()}>{busy ? 'Сохраняем…' : 'Добавить как кандидата'}</button>
        </form>
        <form className="memory-search" onSubmit={search}>
          <label className="field-label" htmlFor="memory-search">Проверить поиск по подтверждённым записям</label>
          <div className="memory-search-row"><input id="memory-search" className="text-field" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Введите тему или ключевые слова" /><button className="button button-secondary" type="submit" disabled={busy || !query.trim()}>Найти</button></div>
          {matches.length > 0 && <ul className="memory-match-list">{matches.map((match) => <li key={match.memory.id}><span className="memory-kind">{match.memory.kind} · релевантность {match.relevance}</span><p>{match.memory.text}</p></li>)}</ul>}
        </form>
        <div className="memory-list-heading"><h3>Записи</h3><span>{memories.length}</span></div>
        <div className="memory-list">{memories.map((memory) => <MemoryCard key={memory.id} memory={memory} busy={busy} onDecide={(status) => void decide(memory.id, status)} onSave={(nextKind, nextText) => void save(memory, nextKind, nextText)} />)}{memories.length === 0 && <div className="inline-empty">Память пока пуста.</div>}</div>
      </section>
    </div>
  )
}

function MemoryCard({ memory, busy, onDecide, onSave }: {
  memory: Memory
  busy: boolean
  onDecide: (status: Extract<MemoryStatus, 'accepted' | 'rejected' | 'stale' | 'superseded'>) => void
  onSave: (kind: string, text: string) => void
}) {
  const [kind, setKind] = useState(memory.kind)
  const [text, setText] = useState(memory.text)
  const changed = kind !== memory.kind || text !== memory.text
  return (
    <article className="memory-card">
      <div className="memory-card-top"><span className={`status-tag memory-status-${memory.status}`}>{memoryStatusLabels[memory.status]}</span><time>{timeLabel(memory.updated_at)}</time></div>
      <label className="field-label" htmlFor={`memory-kind-${memory.id}`}>Тип</label>
      <input id={`memory-kind-${memory.id}`} className="text-field" value={kind} onChange={(event) => setKind(event.target.value)} maxLength={64} />
      <label className="field-label" htmlFor={`memory-text-${memory.id}`}>Содержание</label>
      <textarea id={`memory-text-${memory.id}`} className="text-field text-area" value={text} onChange={(event) => setText(event.target.value)} maxLength={4000} rows={2} />
      {memory.source_task_id && <p className="memory-source">Источник: задача {shortID(memory.source_task_id)}{memory.source_evidence_id ? ` · свидетельство ${shortID(memory.source_evidence_id)}` : ''}</p>}
      <div className="memory-actions">
        {changed && <button className="button button-secondary" type="button" onClick={() => onSave(kind, text)} disabled={busy}>Сохранить изменения</button>}
        {memory.status === 'candidate' && <><button className="button button-primary" type="button" onClick={() => onDecide('accepted')} disabled={busy}>Подтвердить</button><button className="button button-danger-quiet" type="button" onClick={() => onDecide('rejected')} disabled={busy}>Отклонить</button></>}
        {memory.status === 'accepted' && <><button className="button button-secondary" type="button" onClick={() => onDecide('stale')} disabled={busy}>Устарела</button><button className="button button-danger-quiet" type="button" onClick={() => onDecide('rejected')} disabled={busy}>Удалить из поиска</button></>}
        {memory.status === 'stale' && <button className="button button-primary" type="button" onClick={() => onDecide('accepted')} disabled={busy}>Подтвердить снова</button>}
      </div>
    </article>
  )
}
