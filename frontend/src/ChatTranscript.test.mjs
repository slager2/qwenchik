import test from 'node:test'
import assert from 'node:assert/strict'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import ChatTranscript from './ChatTranscript.tsx'

const evidenceCache = {
  get: () => ({ loading: false, error: '', data: [] }),
  subscribe: () => () => {},
  load: async () => [],
}
const message = (status, content = 'Сохранённый текст ответа') => ({
  id: 'assistant-1', conversation_id: 'conversation-1', run_id: 'run-1', role: 'assistant', content, status,
  created_at: '2026-10-24T10:00:00Z', updated_at: '2026-10-24T10:00:00Z', last_event_seq: 1,
})
const render = (item, run, active = false) => renderToStaticMarkup(createElement(ChatTranscript, {
  messages: [item], loading: false, active, runID: 'run-1', run, references: [], evidenceCache,
  onExample: () => {},
}))

test('current completed-but-partial answer is labeled in the transcript', () => {
  const html = render(message('completed'), { id: 'run-1', status: 'completed', terminal_outcome: 'partial' })
  assert.match(html, /Частичный результат сохранён/)
  assert.match(html, /Сохранённый текст ответа/)
})

test('paused streaming text is not presented as still arriving', () => {
  const html = render(message('streaming', 'Полученный фрагмент'), { id: 'run-1', status: 'paused', pause_reason: 'resource_guard' }, true)
  assert.match(html, /Ответ на паузе\. Полученный текст сохранён\./)
  assert.doesNotMatch(html, /Ответ поступает/)
})

test('interrupted and blocked transcript messages have explicit status', () => {
  const interrupted = render(message('interrupted'), { id: 'run-1', status: 'failed', terminal_outcome: 'blocked' })
  assert.match(interrupted, /Ответ прерван перезапуском\. Сохранённый текст доступен\./)
  const blocked = render(message('failed', ''), { id: 'run-1', status: 'failed', terminal_outcome: 'blocked' })
  assert.match(blocked, /Завершение ответа заблокировано проверкой/)
})
