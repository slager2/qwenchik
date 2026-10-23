import { useEffect, useState } from 'react'
import MarkdownMessage from './MarkdownMessage'
import { citationReferences, type CitationReference } from './conversation-state.mjs'
import type { ConversationMessage } from './types'
import type { RunEvidenceCache } from './evidence-cache.mjs'

export default function ChatTranscript({ messages, loading, active, runID, references, evidenceCache, onExample }: {
  messages: ConversationMessage[]
  loading: boolean
  active: boolean
  runID: string
  references: CitationReference[]
  evidenceCache: RunEvidenceCache
  onExample: (mode: 'chat' | 'research', prompt: string) => void
}) {
  return <>
    {!messages.length && !loading && <div className="chat-empty"><span className="chat-empty-mark">Q</span><h2>С чего начнём?</h2><p>Задайте вопрос локальной модели или выберите Deep Research для работы с источниками.</p><div><button type="button" onClick={() => onExample('chat', 'Помоги разобраться в сложной теме')}>Объяснить тему</button><button type="button" onClick={() => onExample('research', 'Сравни варианты и приведи проверяемые источники')}>Исследовать вопрос</button></div></div>}
    {messages.map((message) => <article className={`chat-message chat-role-${message.role}`} key={message.id} aria-label={message.role === 'user' ? 'Ваше сообщение' : 'Ответ Qwen'}><header><strong>{message.role === 'user' ? 'Вы' : 'Qwen'}</strong><time dateTime={message.created_at}>{new Date(message.created_at).toLocaleString('ru-RU', { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' })}</time></header>{message.content ? <MessageContent message={message} runID={runID} references={references} evidenceCache={evidenceCache} /> : <p className="chat-muted">{active ? 'Готовлю ответ…' : 'Текст ответа отсутствует.'}</p>}{message.status === 'pending' && <p className="chat-muted">Не отправлено. Повторите действие.</p>}{message.role === 'assistant' && <footer className="chat-message-meta">{message.status === 'streaming' && active ? 'Ответ поступает' : ['cancelled', 'stopped', 'partial'].includes(message.status) ? 'Частичный ответ сохранён' : message.status === 'failed' ? 'Ответ не завершён' : ''}</footer>}</article>)}
  </>
}

function MessageContent({ message, runID, references, evidenceCache }: { message: ConversationMessage; runID: string; references: CitationReference[]; evidenceCache: RunEvidenceCache }) {
  const historicalRunID = message.role === 'assistant' && message.run_id && message.run_id !== runID ? message.run_id : ''
  const historical = useRunEvidence(evidenceCache, historicalRunID)
  const refs = message.run_id === runID ? references : citationReferences(historical.data ?? [])
  return <><MarkdownMessage content={message.content} references={message.role === 'assistant' ? refs : []} />{historicalRunID && historical.error && <p className="chat-muted">{historical.error} <button type="button" onClick={() => void historical.retry()}>Загрузить источники</button></p>}</>
}

function useRunEvidence(cache: RunEvidenceCache, runID: string) {
  const [observed, setObserved] = useState(() => ({ runID, entry: cache.get(runID) }))
  useEffect(() => {
    if (!runID) { setObserved({ runID: '', entry: { loading: false, error: '', data: undefined } }); return }
    setObserved({ runID, entry: cache.get(runID) })
    const update = () => setObserved({ runID, entry: cache.get(runID) })
    const unsubscribe = cache.subscribe(runID, update)
    void cache.load(runID)
    return unsubscribe
  }, [cache, runID])
  const entry = observed.runID === runID ? observed.entry : cache.get(runID)
  return { ...entry, retry: () => cache.load(runID, true) }
}
