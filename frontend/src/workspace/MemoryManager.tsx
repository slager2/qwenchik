import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { api } from '../api'
import type { Memory, MemoryMatch, MemoryStatus } from '../types'
import { Icon } from '../icons'
import { Badge, Button, Drawer, EmptyState, Input, Textarea } from '../ui'
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
    <Drawer title="Память" className="memory-drawer" onClose={onClose}>
      <p className="ui-muted">В задачи попадают только подтверждённые записи. Новые заметки сначала требуют вашего решения.</p>
      {error && <div className="workspace-notice" role="alert"><Icon name="alert" size={18} /><span>{error}</span></div>}
      <form className="memory-create" onSubmit={createCandidate}>
        <div className="memory-create__row">
          <label className="ui-field" htmlFor="memory-kind">Тип<select id="memory-kind" className="ui-select" value={kind} onChange={(event) => setKind(event.target.value)}><option value="preference">Предпочтение</option><option value="fact">Факт</option><option value="workflow">Процесс</option><option value="constraint">Ограничение</option></select></label>
        </div>
        <label className="ui-field" htmlFor="memory-text">Новая запись</label>
        <Textarea id="memory-text" value={text} onChange={(event) => setText(event.target.value)} maxLength={4000} rows={2} placeholder="Например: ответы — кратко, на русском языке" />
        <Button variant="primary" type="submit" disabled={busy || !text.trim()}>{busy ? 'Сохраняем…' : 'Добавить как кандидата'}</Button>
      </form>
      <form className="memory-search" onSubmit={search}>
        <label className="ui-field" htmlFor="memory-search">Проверить поиск по подтверждённым записям</label>
        <div className="memory-search-row"><Input id="memory-search" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Тема или ключевые слова" /><Button variant="secondary" type="submit" disabled={busy || !query.trim()}>Найти</Button></div>
        {matches.length > 0 && <ul className="memory-match-list">{matches.map((match) => <li key={match.memory.id}><span className="memory-kind">{match.memory.kind} · релевантность {match.relevance}</span><p>{match.memory.text}</p></li>)}</ul>}
      </form>
      <div className="memory-list-heading"><h3>Записи</h3><span>{memories.length}</span></div>
      <div className="memory-list">{memories.map((memory) => <MemoryCard key={memory.id} memory={memory} busy={busy} onDecide={(status) => void decide(memory.id, status)} onSave={(nextKind, nextText) => void save(memory, nextKind, nextText)} />)}{memories.length === 0 && <EmptyState icon={<Icon name="memory" />} title="Память пока пуста" description="Добавьте предпочтение или факт — после подтверждения он будет учитываться в задачах." />}</div>
    </Drawer>
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
      <div className="memory-card-top"><Badge tone={memory.status === 'accepted' ? 'success' : memory.status === 'candidate' ? 'warning' : 'neutral'} className={`status-tag memory-status-${memory.status}`}>{memoryStatusLabels[memory.status]}</Badge><time>{timeLabel(memory.updated_at)}</time></div>
      <label className="ui-field" htmlFor={`memory-kind-${memory.id}`}>Тип</label>
      <Input id={`memory-kind-${memory.id}`} value={kind} onChange={(event) => setKind(event.target.value)} maxLength={64} />
      <label className="ui-field" htmlFor={`memory-text-${memory.id}`}>Содержание</label>
      <Textarea id={`memory-text-${memory.id}`} value={text} onChange={(event) => setText(event.target.value)} maxLength={4000} rows={2} />
      {memory.source_task_id && <p className="memory-source">Источник: задача {shortID(memory.source_task_id)}{memory.source_evidence_id ? ` · свидетельство ${shortID(memory.source_evidence_id)}` : ''}</p>}
      <div className="memory-actions">
        {changed && <Button variant="secondary" size="sm" onClick={() => onSave(kind, text)} disabled={busy}>Сохранить изменения</Button>}
        {memory.status === 'candidate' && <><Button variant="primary" size="sm" onClick={() => onDecide('accepted')} disabled={busy}>Подтвердить</Button><Button variant="danger" size="sm" onClick={() => onDecide('rejected')} disabled={busy}>Отклонить</Button></>}
        {memory.status === 'accepted' && <><Button variant="secondary" size="sm" onClick={() => onDecide('stale')} disabled={busy}>Устарела</Button><Button variant="danger" size="sm" onClick={() => onDecide('rejected')} disabled={busy}>Удалить из поиска</Button></>}
        {memory.status === 'stale' && <Button variant="primary" size="sm" onClick={() => onDecide('accepted')} disabled={busy}>Подтвердить снова</Button>}
      </div>
    </article>
  )
}
