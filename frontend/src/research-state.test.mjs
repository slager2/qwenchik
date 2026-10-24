import test from 'node:test'
import assert from 'node:assert/strict'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { formatSourceBytes, researchError, researchSources, researchQueries, researchSteps } from './research-state.mjs'
import { citationReferences, normalizeSources } from './conversation-state.mjs'
import ResearchPanel from './ResearchInspector.tsx'

const event = (seq, type, payload, visibility = 'user') => ({ seq, type, payload, visibility, created_at: '2026-10-19T10:00:00Z' })
const hit = (seq, status, extra = {}) => event(seq, 'search.hit', { step_id: 's1', hit: { id: 'h1', url: 'https://example.org/source', title: 'Источник', provider: 'searxng', status, ...extra } })

test('nested real search observations retain latest rejection and partial sources when endpoint fails', () => {
  const events = [hit(1, 'discovered'), hit(2, 'validated'), hit(3, 'rejected', { error_code: 'url_rejected_ssrf' }), event(4, 'search.hit', { hit: { id: 'h2', url: 'javascript:bad()', title: 'Unsafe', status: 'rejected' } })]
  const sources = researchSources([{ id: 'older', title: 'Old title', canonical_url: 'https://example.org/source', status: 'found' }], events)
  assert.equal(sources.length, 2)
  assert.equal(sources[0].status, 'rejected')
  assert.equal(sources[0].error_code, 'url_rejected_ssrf')
  assert.equal(sources[1].canonical_url, '')
})

test('queries come only from explicit public fields, never tool arguments or model reasoning', () => {
  const events = [event(1, 'tool.started', { tool: 'search', summary: 'Проверяю официальные источники', arguments: { query: 'hidden argument' }, reasoning: 'private reasoning' }), event(2, 'query.updated', { query: 'actual public query' }), event(3, 'query.updated', { query: 'internal secret' }, 'internal')]
  assert.deepEqual(researchQueries(events), { queries: ['actual public query'], summaries: ['Проверяю официальные источники'] })
})

test('latest plan boundary and ordered events update real step IDs without carrying old completion', () => {
  const plan = { subtasks: [{ id: 's1', description: 'Проверить источники' }] }
  const events = [event(1, 'step.completed', { step_id: 's1' }), event(2, 'plan.created', {}), event(3, 'step.started', { step_id: 's1' })]
  assert.equal(researchSteps(plan, events)[0].status, 'running')
  assert.equal(researchSteps(plan, [...events, event(4, 'step.completed', { step_id: 's1' })])[0].status, 'completed')
})

test('research panel renders partial/error states with human explanations and no raw tool JSON', () => {
  const resource = (data, error = '') => ({ data, error, loading: false, reload: async () => {} })
  const events = [hit(1, 'rejected', { error_code: 'fetch_timeout' }), event(2, 'tool.started', { tool: 'search', summary: 'Краткое действие', arguments: { api_key: 'never-show' } }), event(3, 'query.updated', { query: 'real query' })]
  const html = renderToStaticMarkup(createElement(ResearchPanel, { run: { status: 'running', stage: 'fetch' }, events, resources: { plan: resource({ subtasks: [{ id: 's1', description: 'Проверка' }] }), sources: resource(undefined, 'Источники недоступны'), evidence: resource([]), providers: resource(null) } }))
  for (const expected of ['Часть источников', 'не ответил вовремя', 'Краткое действие', 'real query', 'Чтение страниц', 'Повторить загрузку']) assert.ok(html.includes(expected), expected)
  assert.ok(!html.includes('never-show'))
  assert.ok(!html.includes('api_key'))
})

test('terminal research explains unfinished fetching and fallback, with a single mobile drawer heading', () => {
  const resource = (data) => ({ data, error: '', loading: false, reload: async () => {} })
  const html = renderToStaticMarkup(createElement(ResearchPanel, { showHeading: false, run: { status: 'completed', stage: 'finalization' }, events: [event(1, 'verification.updated', { status: 'fallback_excerpts' })], resources: { plan: resource({ subtasks: [{ id: 's1', description: 'Find sources. Long request and acceptance constraints.' }] }), sources: resource([{ id: 'pending', title: 'Source', canonical_url: 'https://example.org', status: 'fetching' }]), evidence: resource([]), providers: resource(null) } }))
  for (const expected of ['Показаны дословные выдержки', 'Загрузка не подтверждена', 'aria-expanded="false"', 'Long request and acceptance constraints.']) assert.ok(html.includes(expected), expected)
  assert.doesNotMatch(html, /<details|<summary|chat-step-board|chat-run-status/)
  assert.ok(!html.includes('<h2>Детали исследования</h2>'))
  assert.ok(!html.includes('<p>Загрузка</p>'))
})

test('unified inspector keeps guard controls and outcomes without legacy resource duplicates', () => {
  const resource = (data) => ({ data, error: '', loading: false, reload: async () => {} })
  const html = renderToStaticMarkup(createElement(ResearchPanel, {
    run: { id: 'run-1', mode: 'research', status: 'paused' }, events: [], onResume: () => {},
    resources: { plan: resource(null), sources: resource([]), evidence: resource([]), providers: resource(null),
      runtime: resource({ task_id: 'run-1', pause_reason: 'resource_guard', terminal_outcome: 'partial', runtime: { resources: { free_ram_bytes: 999999999 } } }) },
  }))
  assert.match(html, /Возобновить/)
  assert.match(html, /disabled=""/)
  assert.match(html, /Возобновление разрешит backend/)
  assert.match(html, /Частичный результат/)
  assert.equal((html.match(/class="telemetry-panel"/g) || []).length, 1)
  assert.doesNotMatch(html, /chat-run-status|999999999/)
})

test('source failures replay by attempt and sequence, retain discovery, and require a next attempt to confirm fallback', () => {
  const failure = event(3, 'tool.failed', { tool: 'fetch_url', step_id: 'fetch_1', plan_attempt: 1, attempt_id: 'pdf-1', source_fingerprint: 'pdf', target_url: 'https://example.org/source', status: 'rejected', error_code: 'pdf_source_too_large', declared_bytes: 40534667, effective_limit_bytes: 32000000, fetched_bytes: 0, remaining_candidates: 2, mime_type: 'application/pdf', try_next_source: true, fallback_attempted: false, timestamp: '2026-10-20T10:00:00Z' })
  const initial = [hit(1, 'validated', { snippet: 'Discovery snippet' }), event(2, 'tool.started', { tool: 'fetch_url', step_id: 'fetch_1', plan_attempt: 1, attempt_id: 'pdf-1', target_url: 'https://example.org/source' }), failure]
  const pending = researchSources([], initial)[0]
  assert.equal(pending.status, 'rejected')
  assert.equal(pending.title, 'Источник')
  assert.equal(pending.snippet, 'Discovery snippet')
  assert.equal(pending.fetched_bytes, 0)
  assert.equal(pending.fallback_attempted, false)
  const next = event(4, 'tool.started', { tool: 'fetch_url', step_id: 'fetch_1', plan_attempt: 1, attempt_id: 'html-2', target_url: 'https://good.example/article' })
  const extracted = event(5, 'source.extracted', { tool: 'fetch_url', attempt_id: 'html-2', target_url: 'https://good.example/article' })
  const sources = researchSources([{ id: 'pdf', url: 'https://example.org/source', status: 'fetching', event_seq: 2 }], [...initial, failure, next, extracted, event(6, 'tool.started', { tool: 'fetch_url', attempt_id: 'pdf-1', target_url: 'https://example.org/source' })])
  assert.equal(sources.length, 2)
  assert.equal(sources[0].status, 'rejected')
  assert.equal(sources[0].fallback_attempted, true)
  assert.equal(sources[0].declared_bytes, 40534667)
  assert.equal(sources[1].status, 'extracted')
  const unrelated = researchSources([], [...initial, { ...next, payload: { ...next.payload, plan_attempt: 2 } }])
  assert.equal(unrelated[0].fallback_attempted, false)
  const updated = event(4, 'source.updated', { ...failure.payload, fallback_attempted: true })
  const legacyStart = event(5, 'tool.started', { tool: 'fetch_url', step_id: 'fetch_1', target_url: 'https://good.example/article' })
  const workerSources = researchSources([], [...initial, updated, legacyStart])
  assert.equal(workerSources[0].fallback_attempted, true)
  assert.equal(workerSources[0].title, 'Источник')
  assert.equal(workerSources[0].fetched_bytes, 0)
  assert.deepEqual(citationReferences(normalizeSources([{ id: 'bad', url: 'https://bad.example', status: 'extracted', quality_flags: ['challenge'] }])), [])
})

test('explicit failed URLs and fingerprint-only rejections need no discovery and cannot become citations', () => {
  const sources = researchSources([], [event(1, 'tool.failed', { tool: 'fetch_url', target_url: 'https://example.org/direct.pdf', attempt_id: 'direct', code: 'pdf_malformed', fetched_bytes: 321, try_next_source: false }), event(2, 'tool.failed', { tool: 'fetch_url', source_fingerprint: 'private', target_url: 'https://example.org/?token=private-fixture', error_code: 'raw private-fixture', fetched_bytes: -1 })])
  assert.equal(sources.length, 2)
  assert.equal(sources[0].status, 'failed')
  assert.equal(sources[0].error_code, 'pdf_malformed')
  assert.equal(sources[1].canonical_url, '')
  assert.equal(sources[1].error_code, 'source_failed')
  assert.equal(sources[1].fetched_bytes, undefined)
  assert.deepEqual(citationReferences(sources), [])
  const normalized = normalizeSources([{ id: 'x', fetched_bytes: '0', declared_bytes: 2 ** 41, effective_limit_bytes: 1.5, remaining_candidates: -1, fallback_attempted: 'true', mime_type: 'private/raw', timestamp: 'bad' }])[0]
  for (const key of ['fetched_bytes', 'declared_bytes', 'effective_limit_bytes', 'remaining_candidates', 'fallback_attempted', 'mime_type', 'timestamp']) assert.equal(normalized[key], undefined)
})

test('byte formatter distinguishes decimal MB and binary MiB without claiming an in-policy PDF is oversized', () => {
  assert.match(formatSourceBytes(40000000), /40 MB; 38,147 MiB/)
  assert.match(formatSourceBytes(67108864), /67,109 MB; 64 MiB/)
  assert.match(formatSourceBytes(0), /0 байт \(0 MB; 0 MiB\)/)
  assert.equal(formatSourceBytes(-1), 'Неизвестно')
  assert.notEqual(researchError('pdf_source_too_large'), researchError('task_fetch_budget_exceeded'))
})

test('DNS failure stays a source network error through replay and rendering', () => {
  const failure = event(2, 'tool.failed', { tool: 'fetch_url', target_url: 'https://example.org/source', source_fingerprint: 'h1', attempt_id: 'dns-1', step_id: 'fetch_1', status: 'failed', error_code: 'fetch_dns_failed', fetched_bytes: 0, try_next_source: true, remaining_candidates: 4 })
  const sources = researchSources([], [hit(1, 'validated'), failure, failure])
  assert.equal(sources.length, 1)
  assert.equal(sources[0].error_code, 'fetch_dns_failed')
  assert.equal(sources[0].status, 'failed')
  assert.equal(sources[0].fetched_bytes, 0)
  assert.match(researchError(sources[0].error_code), /DNS/)
  assert.ok(!researchError(sources[0].error_code).includes('Параметры'))
})

test('PDF panel shows exact header-only and streaming failures while accepted evidence remains visible', () => {
  const resource = (data) => ({ data, error: '', loading: false, reload: async () => {} })
  const failure = event(1, 'tool.failed', { tool: 'fetch_url', target_url: 'https://example.org/direct.pdf', attempt_id: 'pdf', status: 'rejected', error_code: 'pdf_source_too_large', declared_bytes: 40534667, effective_limit_bytes: 32000000, fetched_bytes: 0, mime_type: 'application/pdf', remaining_candidates: 0, try_next_source: true, fallback_attempted: false })
  const render = (events) => renderToStaticMarkup(createElement(ResearchPanel, { run: { status: 'completed' }, events, resources: { plan: resource(null), sources: resource([]), evidence: resource([{ id: 'ev-good', title: 'Accepted HTML', excerpt: 'Saved independent evidence', canonical_url: 'https://good.example' }]), providers: resource(null) } }))
  const html = render([failure])
  for (const expected of ['pdf_source_too_large', formatSourceBytes(40534667), formatSourceBytes(32000000), '0 байт (0 MB; 0 MiB)', 'Тело не скачивалось', 'Переход пока не подтверждён', 'Осталось кандидатов: 0', 'application/pdf', 'Saved independent evidence', '2026-10-19T10:00:00Z']) assert.ok(html.includes(expected), expected)
  assert.ok(!html.includes('Попытка перехода к другому источнику подтверждена'))
  const streaming = render([{ ...failure, payload: { ...failure.payload, declared_bytes: undefined, fetched_bytes: 32000001, fallback_attempted: true } }])
  assert.ok(streaming.includes(formatSourceBytes(32000001)))
  assert.ok(streaming.includes('полнота загрузки этим не подтверждается'))
  assert.ok(!streaming.includes('Тело не скачивалось'))
})

test('persisted partial warning survives completed research even when visible source snapshots are all accepted', () => {
  const resource = (data) => ({ data, error: '', loading: false, reload: async () => {} })
  const html = renderToStaticMarkup(createElement(ResearchPanel, { run: { status: 'completed' }, events: [event(1, 'research.warning', { code: 'sources_skipped', partial: true, source_failures: [], message: 'private-fixture' })], resources: { plan: resource(null), sources: resource([{ id: 'accepted', title: 'Accepted', status: 'cited', url: 'https://good.example' }]), evidence: resource([{ id: 'ev-good', excerpt: 'Accepted independent evidence' }]), providers: resource(null) } }))
  assert.ok(html.includes('Часть источников недоступна или отклонена'))
  assert.ok(html.includes('Accepted independent evidence'))
  assert.ok(!html.includes('private-fixture'))
})
