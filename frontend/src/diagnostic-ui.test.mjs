import test from 'node:test'
import assert from 'node:assert/strict'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { ProviderCard, TaskFailureCard, SourceList } from './diagnostic-ui.mjs'

const render = (component, props) => renderToStaticMarkup(createElement(component, props))

test('provider cards render unavailable, blocked and genuine empty separately', () => {
  for (const [health, code, expected] of [['unavailable', 'search_unavailable', 'Недоступен'], ['blocked', 'provider_blocked', 'Запрос заблокирован'], ['degraded', 'search_no_results', 'Нет результатов']]) {
    const markup = render(ProviderCard, { name: 'searxng', checkedLabel: '', provider: { enabled: true, configured: true, health, last_outcome: { code, latency_ms: 12, retryable: false } } })
    assert.ok(markup.includes(expected))
    assert.ok(markup.includes('12 мс'))
    assert.ok(markup.includes(code))
  }
})

test('failure card renders all provider failures and missing evidence as an alert', () => {
  const markup = render(TaskFailureCard, { evidenceCount: 0, payload: { message: 'Поиск недоступен', missing: ['Нет подтверждённых названий'], outcomes: [{ provider: 'duckduckgo', state: 'blocked', code: 'provider_blocked' }, { provider: 'searxng', state: 'unavailable', code: 'search_unavailable' }] } })
  for (const text of ['role="alert"', 'Нет подтверждённых названий', 'duckduckgo', 'searxng', 'без сохранённых источников']) assert.ok(markup.includes(text))
})

test('source cards render real links and escape untrusted excerpts', () => {
  const markup = render(SourceList, { formatTime: () => 'checked', sources: [{ id: 'ev1', canonical_url: 'https://example.org/source', title: 'Verified source', excerpt: '<script>bad()</script>', provider: 'direct_http', retrieved_at: '', content_sha256: '1234567890abcdef' }] })
  assert.ok(markup.includes('href="https://example.org/source"'))
  assert.ok(markup.includes('Verified source'))
  assert.ok(markup.includes('&lt;script&gt;'))
  assert.ok(!markup.includes('<script>'))
})
