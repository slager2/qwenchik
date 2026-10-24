import test from 'node:test'
import assert from 'node:assert/strict'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import RunStatus from './RunStatus.tsx'

test('resource pause, unknown telemetry and contradictions remain explicit without unsafe resume or HTML', () => {
  const html = renderToStaticMarkup(createElement(RunStatus, {
    run: { id: 'r', status: 'paused' }, onResume() {}, data: {
      task_id: 'r', pause_reason: 'resource_guard', resource_reason: 'low_ram', compactions: 2,
      runtime: { resources: { free_ram_bytes: null, gpu_free_vram_mb: null, gpu_temp_c: null }, sleep: { active: false, available: false } },
      keep_awake_requested: true, contradictions: ['<script>untrusted()</script>'], verdict: 'UNCERTAIN',
    },
  }))
  assert.match(html, /Недостаточно свободной RAM/)
  assert.match(html, /backend после успешной проверки ресурсов/)
  assert.match(html, /disabled=""[^>]*>Возобновить/)
  assert.match(html, /неизвестно/)
  assert.doesNotMatch(html, /0 MiB|<script>/)
  assert.match(html, /&lt;script&gt;/)
  assert.match(html, /Блокировка сна не подтверждена/)
  assert.match(html, /2 сжатий/)
})

test('terminal outcome and delivery failure do not pretend the report was completed or discard its outcome', () => {
  const html = renderToStaticMarkup(createElement(RunStatus, { run: { id: 'r', status: 'failed', terminal_outcome: 'budget_exhausted' }, data: { task_id: 'r', terminal_outcome: 'budget_exhausted', notification: { status: 'abandoned' } } }))
  assert.match(html, /Бюджет исчерпан/)
  assert.match(html, /доставка недоступна/)
  assert.doesNotMatch(html, />Завершено</)
})

test('partial evidence readiness is localized separately from terminal outcome', () => {
  const html = renderToStaticMarkup(createElement(RunStatus, {
    run: { id: 'r', status: 'completed', terminal_outcome: 'partial' },
    data: { task_id: 'r', verdict: 'PARTIAL', terminal_outcome: 'partial' },
  }))
  assert.match(html, /Частичный результат/)
  assert.match(html, /Критические критерии выполнены; дополнительные не подтверждены/)
  assert.doesNotMatch(html, /Статус неизвестен/)
})
