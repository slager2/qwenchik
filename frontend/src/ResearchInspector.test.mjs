import test from 'node:test'
import assert from 'node:assert/strict'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import ResearchInspector from './ResearchInspector.tsx'

const remote = (data) => ({ data, loading: false, error: '', reload: async () => {} })
const run = { id: 'r1', mode: 'research', status: 'paused', pause_reason: 'manual', resource_reason: 'high_model_rss' }
const event = (seq, type, payload = {}, task_id = 'r1') => ({ seq, type, payload, task_id, visibility: 'user', created_at: '2026-10-29T10:00:00Z' })
const resources = { plan: remote(null), sources: remote([]), evidence: remote([]), providers: remote(null), runtime: remote({ task_id: 'r1', research_report: { artifact_id: '', markdown_artifact_id: '', outcome: 'completed', sections: 3 }, research_rounds: [{ number: 1, critical_missing: null }] }) }
const props = { resources, run, events: [], onPause: () => {}, onResume: () => {} }
const render = (extra = {}) => renderToStaticMarkup(createElement(ResearchInspector, { ...props, ...extra }))

test('outcome/actions and actual report availability precede plan, sources, and diagnostics', () => {
  const html = render({ onDiagnostics: () => {} })
  const ordered = ['Детали исследования', 'Возобновить', 'Аналитический отчёт', 'data-research-section="sources"', 'Диагностика запуска', 'Проверить модель и поиск']
  for (let i = 1; i < ordered.length; i++) assert.ok(html.indexOf(ordered[i - 1]) < html.indexOf(ordered[i]), ordered[i])
  assert.match(html, /Файл отчёта пока не сохранён/)
  assert.doesNotMatch(html, /Скачать отчёт/)
  assert.match(html, /backend повторно проверит/)
  assert.match(html, /Проверка может вызвать локальную модель и поисковые сервисы/)
})

test('active plan, event boundary and skip counts are distinct from report readiness', () => {
  const runtime = { task_id: 'r1', current_round: 2, adaptive_enabled: true, active_round_plan: { subtasks: [{ id: 'reused', description: 'Current round' }, { id: 'skip', description: 'Skipped work' }, { id: 'done', description: 'Completed work' }] } }
  const html = render({ resources: { ...resources, plan: remote({ subtasks: [{ id: 'old', description: 'Initial plan only' }] }), runtime: remote(runtime) }, events: [event(1, 'step.completed', { step_id: 'reused' }), event(2, 'plan.created'), event(3, 'step.skipped', { step_id: 'skip' }), event(4, 'step.completed', { step_id: 'done' })] })
  assert.match(html, /План текущего раунда/)
  assert.match(html, /Раунд 2/)
  assert.match(html, /value="2" max="3"/)
  assert.match(html, /Выполнено: 1 · Пропущено: 1/)
  assert.match(html, /не готовность отчёта/)
  assert.doesNotMatch(html, /Initial plan only/)
})

test('source failures and byte diagnostics remain accessible under source disclosures', () => {
  const source = { id: 'pdf', canonical_url: 'https://example.org/report.pdf', title: 'PDF source', snippet: 'Excerpt first', status: 'rejected', error_code: 'pdf_source_too_large', declared_bytes: 40000000, effective_limit_bytes: 32000000, fetched_bytes: 0, try_next_source: true, fallback_attempted: false, remaining_candidates: 1, mime_type: 'application/pdf' }
  const html = render({ resources: { ...resources, sources: remote([source]), pdfCapability: { state: 'unavailable' } } })
  assert.match(html, /aria-expanded="false"[^>]*><span>Сведения о загрузке<\/span>/)
  assert.ok(html.indexOf('Excerpt first') < html.indexOf('Сведения о загрузке'))
  for (const text of ['pdf_source_too_large', '40 000 000', '32 000 000', 'Тело не скачивалось', 'Переход пока не подтверждён', 'application/pdf', 'Наличие capability не подтверждает извлечение конкретного PDF']) assert.ok(html.includes(text), text)
  assert.match(html, /<h3 tabindex="-1">Источники/)
  assert.match(html, /<h3 tabindex="-1">Evidence/)
})

test('mismatched runtime/events/evidence cannot disclose another run output', () => {
  const html = render({ resources: { ...resources, evidence: remote([{ id: 'foreign', task_id: 'other', title: 'Foreign evidence' }]), runtime: remote({ task_id: 'other', research_report: { artifact_id: 'foreign-artifact' }, active_round_plan: { subtasks: [{ id: 'foreign', description: 'Foreign plan' }] } }) }, events: [event(1, 'query.updated', { query: 'Foreign query' }, 'other')] })
  assert.match(html, /Наличие файла отчёта неизвестно/)
  assert.doesNotMatch(html, /Foreign|foreign-artifact/)
})

test('persisted artifact links stay run-owned and encode identifiers; nullable gaps are supported', () => {
  const html = render({ run: { ...run, id: 'run/id', status: 'completed' }, resources: { ...resources, runtime: remote({ task_id: 'run/id', research_report: { artifact_id: 'json/id', markdown_artifact_id: 'md/id', outcome: 'partial', sections: 1, unrepresented_criteria: ['Unrepresented criterion'] } }) } })
  assert.match(html, /\/api\/tasks\/run%2Fid\/artifacts\/json%2Fid/)
  assert.match(html, /\/api\/tasks\/run%2Fid\/artifacts\/md%2Fid/)
  assert.match(html, /Unrepresented criterion/)
})

test('diagnostics are callback-only with loading and failure feedback', () => {
  let calls = 0
  const html = render({ onDiagnostics: () => { calls++ }, diagnosticsLoading: true, diagnosticsError: 'Diagnostic check failed' })
  assert.equal(calls, 0)
  assert.match(html, /disabled="">Проверить модель и поиск/)
  assert.match(html, /Проверяем модель и поиск/)
  assert.match(html, /Diagnostic check failed/)
})

test('section shortcuts transfer focus before scrolling without fetching', () => {
  const tree = ResearchInspector(props)
  const find = (node, predicate) => {
    if (!node || typeof node !== 'object') return undefined
    if (predicate(node)) return node
    return [node.props?.children].flat(Infinity).map((child) => find(child, predicate)).find(Boolean)
  }
  const nav = find(tree, (node) => node.type === 'nav')
  const button = find(nav, (node) => node.type === 'button')
  const calls = []
  const heading = { focus: (options) => calls.push(['focus', options]), scrollIntoView: (options) => calls.push(['scroll', options]) }
  button.props.onClick({ currentTarget: { closest: () => ({ querySelector: (selector) => { assert.equal(selector, '[data-research-section="sources"] h3'); return heading } }) } })
  assert.deepEqual(calls.map(([name]) => name), ['focus', 'scroll'])
})
