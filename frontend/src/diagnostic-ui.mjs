import { createElement as h } from 'react'
import { providerSummary, failureSummary, evidenceSummary } from './observability.mjs'

export function ProviderCard({ name, provider, checkedLabel }) {
  return h('article', { className: `provider-card health-${provider.health}` },
    h('h3', null, name), h('p', null, providerSummary(provider)),
    h('small', null, provider.endpoint || 'Endpoint не настроен'),
    h('p', null, checkedLabel ? `Последняя проверка: ${checkedLabel}` : 'Реальных обращений ещё не было'))
}

export function TaskFailureCard({ payload, evidenceCount }) {
  return h('div', { className: 'notice notice-error task-failure', role: 'alert' },
    h('strong', null, 'Задача не выполнена'),
    h('p', null, payload ? failureSummary(payload) : 'Подробности сбоя пока не загружены.'),
    h('p', null, evidenceSummary(evidenceCount, 'failed')))
}

export function SourceList({ sources, formatTime }) {
  if (!sources.length) return h('p', null, 'Источники появятся после research-поиска.')
  return h('ul', { className: 'source-list' }, sources.map((source) => h('li', { key: source.id },
    h('a', { href: source.canonical_url, target: '_blank', rel: 'noreferrer' }, source.title || source.canonical_url),
    h('p', null, source.excerpt), h('span', { className: 'source-meta' }, `${source.provider} · ${formatTime(source.retrieved_at)} · `,
      h('span', { className: 'mono' }, source.content_sha256.slice(0, 10))))))
}
