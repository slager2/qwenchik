import test from 'node:test'
import assert from 'node:assert/strict'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import ResearchOverview from './ResearchOverview.tsx'

const run = { id: 'r1', mode: 'research', status: 'paused', pause_reason: 'manual', resource_reason: 'high_model_rss' }
const base = { run, runtime: { task_id: 'r1', research_report: { artifact_id: '', markdown_artifact_id: '', outcome: 'completed', sections: 3 } }, events: [], answer: { run_id: 'r1', role: 'assistant', status: 'pending', content: '' }, evidence: { loading: false, error: '' }, onDetails: () => {}, onResume: () => {}, onStop: () => {} }
const render = (props = {}) => renderToStaticMarkup(createElement(ResearchOverview, { ...base, ...props }))

test('only research runs render the compact six-class overview contract', () => {
  assert.equal(render({ run: null }), '')
  assert.equal(render({ run: { ...run, mode: 'chat' } }), '')
  const html = render()
  for (const name of ['', '__heading', '__state', '__facts', '__actions', '__note']) assert.ok(html.includes(`class="research-overview${name}"`))
  assert.match(html, /Исследование на паузе/)
  assert.match(html, /лимит памяти модели/)
  assert.match(html, /Текст ответа пока отсутствует/)
  assert.match(html, /Файл отчёта пока не сохранён/)
  assert.doesNotMatch(html, /Материалов: 0|Отчёт сохранён|PASS/)
  assert.equal((html.match(/<li>/g) || []).length, 2)
})

test('manual hold offers explicit resume while guarded/recovery/unknown pauses explain disabled resume', () => {
  assert.match(render(), /<button type="button" aria-describedby="[^"]+">Возобновить/)
  for (const reason of ['resource_guard', 'recovery_required', 'unknown']) {
    const html = render({ run: { ...run, pause_reason: reason } })
    assert.match(html, /disabled=""[^>]*>Возобновить/)
    assert.match(html, /Остановить/)
  }
  assert.match(render({ busy: true }), /aria-busy="true"/)
})

test('foreign runtime and evidence do not appear as current output', () => {
  const html = render({ runtime: { task_id: 'foreign', research_report: { artifact_id: 'foreign' } }, evidence: { data: [{ id: 'foreign', task_id: 'foreign', title: 'Foreign' }], loading: false, error: '' } })
  assert.match(html, /Наличие файла отчёта неизвестно/)
  assert.doesNotMatch(html, /Отчёт сохранён|Foreign|Сохранённых материалов: 1/)
})

test('partial canonical output, fallback and report gaps remain disclosed and explicit', () => {
  const html = render({ run: { ...run, status: 'completed', terminal_outcome: 'partial' }, runtime: { task_id: 'r1', research_report: { artifact_id: 'report', outcome: 'partial', unrepresented_criteria: ['Missing comparison'] } }, answer: { ...base.answer, status: 'completed', content: 'Excerpt' }, events: [{ seq: 1, task_id: 'r1', visibility: 'user', type: 'verification.updated', payload: { status: 'fallback_excerpts' } }] })
  assert.match(html, /Сохранён частичный отчёт/)
  assert.match(html, /<details class="research-overview__note">/)
  assert.match(html, /Missing comparison/)
  assert.match(html, /Синтезированный вывод надёжно подтвердить не удалось/)
  assert.doesNotMatch(html, />Остановить|>Возобновить/)
})
