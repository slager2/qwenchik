import test from 'node:test'
import assert from 'node:assert/strict'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import ChatTranscript from './ChatTranscript.tsx'
import ChatSidebar from './ChatSidebar.tsx'

const evidenceCache = {
  get: () => ({ loading: false, error: '', data: [] }),
  subscribe: () => () => {},
  load: async () => [],
}
const message = (status, content = 'Сохранённый текст ответа') => ({
  id: 'assistant-1', conversation_id: 'conversation-1', run_id: 'run-1', role: 'assistant', content, status,
  created_at: '2026-10-24T10:00:00Z', updated_at: '2026-10-24T10:00:00Z', last_event_seq: 1,
})
const render = (item, run, active = false, extra = {}) => renderToStaticMarkup(createElement(ChatTranscript, {
  messages: [item], loading: false, active, runID: 'run-1', run, references: [], evidenceCache,
  onExample: () => {}, ...extra,
}))

test('current completed-but-partial answer is labeled in the transcript', () => {
  const html = render(message('completed'), { id: 'run-1', status: 'completed', terminal_outcome: 'partial' })
  assert.match(html, /Частичный результат сохранён/)
  assert.match(html, /Сохранённый текст ответа/)
})

test('optimistic user sending and failure are distinct; assistant pending is never unsent', () => {
  const user = { ...message('pending', 'Вопрос'), role: 'user' }
  assert.match(render(user, null, false, { submissionState: 'sending' }), /Отправляем сообщение…/)
  assert.doesNotMatch(render(user, null, false, { submissionState: 'sending' }), /не отправлено|Не отправлено/)
  assert.match(render(user, null, false, { submissionState: 'failed' }), /Сообщение не отправлено/)
  assert.doesNotMatch(render(user, null), /не отправлено|Не отправлено/)
  assert.doesNotMatch(render(message('pending', ''), null, false, { submissionState: 'failed' }), /не отправлено|Не отправлено/)
})

test('empty paused, failed and blocked answers never claim text was saved or preparation is active', () => {
  const paused = render(message('streaming', ''), { id: 'run-1', status: 'paused' }, true)
  assert.match(paused, /Ответ ещё не сформирован\. Запуск на паузе\./)
  assert.match(paused, /chat-message-empty/)
  assert.match(render(message('streaming', ''), { id: 'run-1', status: 'failed' }, true), /Ответ не завершён/)
  for (const html of [paused, render(message('failed', ''), { id: 'run-1', status: 'failed' }, true), render(message('streaming', ''), { id: 'run-1', status: 'failed', terminal_outcome: 'blocked' }, true), render(message('partial', '  '), { id: 'run-1', status: 'completed', terminal_outcome: 'partial' })]) {
    assert.doesNotMatch(html, /Готовлю ответ|Ответ поступает|текст сохранён|результат сохранён|ответ сохранён/)
  }
})

test('a historical empty answer does not inherit current streaming or pause state', () => {
  const old = { ...message('streaming', ''), run_id: 'old-run' }
  for (const status of ['running', 'paused']) assert.doesNotMatch(render(old, { id: 'run-1', status }, true), /Готовлю ответ|Ответ поступает|Запуск на паузе/)
})

test('historical citations use their own evidence and report headings stay within each message', () => {
  const cache = { ...evidenceCache, get: (id) => ({ loading: false, error: '', data: id === 'old-run' ? [{ id: 'ev-old', canonical_url: 'https://old.example/source', title: 'Старый источник' }] : [] }) }
  const old = { ...message('completed', '# Итог\n\nФакт [ev-old].\n\n## Детали\n\nТекст'), run_id: 'old-run' }
  const html = render(old, { id: 'run-1', status: 'paused' }, false, { evidenceCache: cache, references: [{ id: 'ev-old', label: '1', url: 'https://current.example/source', title: 'Текущий источник' }] })
  assert.match(html, /https:\/\/old.example\/source/)
  assert.doesNotMatch(html, /current.example|Ответ на паузе/)
  assert.match(html, /class="report-outline"/)
  assert.match(html, /class="report-outline-links"/)
  assert.match(html, /tabindex="-1"/)
  assert.doesNotMatch(html, /href="#/)
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

test('sidebar has a true empty state and labels legacy dates without inventing status', () => {
  const sidebar = (conversations) => renderToStaticMarkup(createElement(ChatSidebar, { conversations, error: '', loading: false, selected: '', onNew() {}, onSelect() {}, onRetry() {} }))
  assert.match(sidebar([]), /Разговоров пока нет/)
  assert.match(sidebar([{ id: 'archived', title: 'Скрытый', archived_at: '2026-01-01' }]), /Разговоров пока нет/)
  const legacy = sidebar([{ id: 'legacy', title: 'Старый разговор', mode: 'research' }])
  assert.match(legacy, /Без даты/)
  assert.doesNotMatch(legacy, /Invalid Date|<time|На паузе|Выполняется|Завершено/)
  const dated = sidebar([{ id: 'dated', title: 'С датой', mode: 'chat', created_at: '2026-01-01T10:00:00Z' }])
  assert.match(dated, /<time dateTime="2026-01-01T10:00:00Z"/)
})
