import { memo, useEffect, useId, useMemo, useRef, useState } from 'react'
import { focusReportHeading, reportOutline } from './report-outline.mjs'
import MarkdownMessage from './MarkdownMessage'
import { citationReferences, type CitationReference } from './conversation-state.mjs'
import type { ConversationMessage, Run } from './types'
import type { RunEvidenceCache } from './evidence-cache.mjs'

export default function ChatTranscript({ messages, loading, active, runID, run, references, evidenceCache, onExample, submissionState }: {
  messages: ConversationMessage[]
  loading: boolean
  active: boolean
  runID: string
  run: Run | null
  references: CitationReference[]
  evidenceCache: RunEvidenceCache
  onExample: (mode: 'chat' | 'research', prompt: string) => void
  submissionState?: 'sending' | 'failed'
}) {
  return <>
    {!messages.length && !loading && <div className="chat-empty"><p className="chat-empty-kicker">Локальный рабочий чат</p><h2>С чего начнём?</h2><p>Напишите сообщение или задачу исследования. Можно начать с примера:</p><div><button type="button" onClick={() => onExample('chat', 'Помоги разобраться в сложной теме')}>Объяснить тему</button><button type="button" onClick={() => onExample('research', 'Сравни варианты и приведи проверяемые источники')}>Исследовать вопрос</button></div></div>}
    {messages.map((message) => {
      const current = message.role === 'assistant' && !!message.run_id && runID === message.run_id && run?.id === message.run_id
      const hasContent = !!message.content.trim()
      const meta = current && run?.status === 'paused' ? hasContent ? 'Ответ на паузе. Полученный текст сохранён.' : 'Ответ ещё не сформирован. Запуск на паузе.'
        : message.status === 'interrupted' ? hasContent ? 'Ответ прерван перезапуском. Сохранённый текст доступен.' : 'Ответ прерван перезапуском. Текст не сформирован.'
          : current && run?.terminal_outcome === 'partial' ? hasContent ? 'Частичный результат сохранён' : 'Запуск завершён без текста ответа'
            : current && run?.terminal_outcome === 'blocked' ? 'Завершение ответа заблокировано проверкой'
              : current && run?.status === 'failed' ? 'Ответ не завершён'
                : current && run?.status === 'cancelled' ? hasContent ? 'Частичный ответ сохранён' : 'Ответ остановлен до получения текста'
              : current && message.status === 'streaming' && active && !['failed', 'cancelled', 'completed'].includes(run?.status || '') ? 'Ответ поступает'
                : ['cancelled', 'stopped', 'partial'].includes(message.status) ? hasContent ? 'Частичный ответ сохранён' : 'Ответ остановлен до получения текста'
                  : message.status === 'failed' ? 'Ответ не завершён' : ''
      const preparing = current && active && !['paused', 'failed', 'cancelled', 'completed'].includes(run?.status || '') && !['failed', 'interrupted', 'cancelled', 'stopped', 'partial'].includes(message.status) && run?.terminal_outcome !== 'blocked'
      const submission = message.role === 'user' && message.status === 'pending' ? submissionState === 'sending' ? 'Отправляем сообщение…' : submissionState === 'failed' ? 'Сообщение не отправлено' : '' : ''
      return <article className={`chat-message chat-role-${message.role}`} key={message.id} data-message-id={message.id} tabIndex={message.role === 'assistant' ? -1 : undefined} aria-label={message.role === 'user' ? 'Ваше сообщение' : 'Ответ Qwen'}><header><strong>{message.role === 'user' ? 'Вы' : 'Qwen'}</strong><time dateTime={message.created_at}>{new Date(message.created_at).toLocaleString('ru-RU', { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' })}</time></header>{hasContent ? <MessageContent message={message} runID={runID} references={references} evidenceCache={evidenceCache} /> : <p className="chat-muted chat-message-empty">{current && run?.status === 'paused' ? 'Ответ ещё не сформирован. Запуск на паузе.' : preparing ? 'Готовлю ответ…' : 'Текст ответа отсутствует.'}</p>}{submission && <p className="chat-muted chat-submission-state" role="status">{submission}</p>}{message.role === 'assistant' && meta && !(current && run?.status === 'paused' && !hasContent) && <footer className="chat-message-meta">{meta}</footer>}</article>
    })}
  </>
}

const MessageContent = memo(function MessageContent({ message, runID, references, evidenceCache }: { message: ConversationMessage; runID: string; references: CitationReference[]; evidenceCache: RunEvidenceCache }) {
  const prefix = useId()
  const container = useRef<HTMLDivElement>(null)
  const [copied, setCopied] = useState(false)
  const headings = useMemo(() => message.role === 'assistant' ? reportOutline(message.content, prefix) : [], [message.content, message.role, prefix])
  const historicalRunID = message.role === 'assistant' && message.run_id && message.run_id !== runID ? message.run_id : ''
  const historical = useRunEvidence(evidenceCache, historicalRunID)
  const refs = message.run_id === runID ? references : citationReferences(historical.data ?? [])
  const copyMessage = async () => {
    try {
      if (!navigator.clipboard) return
      await navigator.clipboard.writeText(message.content)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1800)
    } catch {
      setCopied(false)
    }
  }
  return <div ref={container}>{headings.length >= 2 && <nav className="report-outline" aria-label="Разделы ответа"><details><summary>Разделы ответа · {headings.length}</summary><div className="report-outline-links">{headings.map((heading) => <button key={heading.id} type="button" title={heading.text} onClick={() => focusReportHeading(container.current, heading.id)}>{heading.text}</button>)}</div></details></nav>}<MarkdownMessage content={message.content} idPrefix={prefix} references={message.role === 'assistant' ? refs : []} />{message.role === 'assistant' && <div className="chat-message-actions"><button type="button" onClick={() => void copyMessage()} disabled={!message.content.trim()} aria-label="Скопировать ответ">{copied ? 'Скопировано' : 'Копировать'}</button>{copied && <span className="chat-muted" role="status">Ответ скопирован</span>}</div>}{historicalRunID && historical.error && <p className="chat-muted">{historical.error} <button type="button" onClick={() => void historical.retry()}>Загрузить источники</button></p>}</div>
})

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
