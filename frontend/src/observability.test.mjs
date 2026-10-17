import test from 'node:test'
import assert from 'node:assert/strict'
import { diagnosticSummary, evidenceSummary, failureSummary, providerSummary, streamLabel } from './observability.mjs'

test('configured SearXNG is unknown until observed, then unavailable', () => {
  const provider = { configured: true, enabled: true, health: 'not_checked', last_outcome: null }
  assert.match(providerSummary(provider), /Настроен · Не проверен/)
  assert.doesNotMatch(providerSummary(provider), /Есть результаты/)
  assert.match(providerSummary({ ...provider, health: 'unavailable', last_outcome: { latency_ms: 80, code: 'connection_failed', retryable: true } }), /Недоступен · 80 мс · Код: connection_failed · Можно повторить/)
})

test('all failed attempts retain each provider and retryability', () => {
  const summary = diagnosticSummary({ outcomes: [
    { provider: 'searxng', state: 'unavailable', retryable: true },
    { provider: 'tavily', state: 'unavailable', retryable: false },
    { provider: 'duckduckgo', state: 'blocked', retryable: false },
  ] })
  assert.match(summary, /searxng · Недоступен · Можно повторить/)
  assert.match(summary, /tavily · Недоступен · Автоповтор не рекомендован/)
  assert.match(summary, /duckduckgo · Запрос заблокирован/)
})

test('DDG blocked, genuine zero and filtered are distinct', () => {
  const blocked = diagnosticSummary({ provider: 'duckduckgo', state: 'blocked', code: 'captcha', result_count: 0 })
  const zero = diagnosticSummary({ provider: 'duckduckgo', state: 'no_results', result_count: 0 })
  const filtered = diagnosticSummary({ provider: 'duckduckgo', state: 'filtered', result_count: 0 })
  assert.match(blocked, /Запрос заблокирован · captcha/)
  assert.match(zero, /Нет результатов · Результатов: 0/)
  assert.match(filtered, /Результаты отфильтрованы/)
  assert.notEqual(blocked, zero)
  assert.match(diagnosticSummary({ state: 'degraded', code: 'search_no_results', result_count: 0 }), /Нет результатов/)
  assert.match(diagnosticSummary({ state: 'degraded', code: 'search_filtered_by_policy' }), /Результаты отфильтрованы/)
})

test('missing criteria remain prominent even with a summary', () => {
  const summary = failureSummary({ summary: 'verification', message: 'Task failed', verification_summary: 'Only one domain', missing: ['Three sources', 'Independent evidence', 42], error_code: 'verification_failed' })
  assert.match(summary, /Only one domain/)
  assert.match(summary, /Не хватает: Three sources; Independent evidence/)
  assert.doesNotMatch(summary, /42/)
})

test('evidence count is preserved for failed tasks', () => {
  assert.equal(evidenceSummary(2, 'failed'), 'Сохранено источников: 2')
  assert.match(evidenceSummary(0, 'failed'), /без сохранённых источников/)
  assert.match(diagnosticSummary({ provider: 'searxng', state: 'ok', result_count: 3, distinct_domains: 2 }), /Результатов: 3 · Доменов: 2/)
})

test('stream labels distinguish connecting, reconnecting and terminal closure', () => {
  assert.equal(streamLabel('running', 'connecting'), 'Подключение')
  assert.equal(streamLabel('running', 'live'), 'SSE live')
  assert.equal(streamLabel('running', 'reconnecting'), 'Переподключение')
  for (const status of ['failed', 'completed', 'cancelled']) assert.equal(streamLabel(status, 'reconnecting'), 'Поток завершён')
})
