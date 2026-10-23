import test from 'node:test'
import assert from 'node:assert/strict'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'

const { default: TelemetryPanel } = await import('./TelemetryPanel.tsx')

const metric = (value, source = 'exact', updated_at = new Date().toISOString(), unit = '') => ({ value, source, unit, updated_at })
const render = (props = {}) => renderToStaticMarkup(createElement(TelemetryPanel, props))

test('missing telemetry renders accessible unavailable states and tolerates null inputs', () => {
  const html = render({ runtime: null, telemetry: null })
  assert.match(html, />Система</)
  assert.match(html, />Инференс</)
  assert.match(html, />Контроль ресурсов</)
  assert.match(html, /Нет данных/)
  assert.match(html, /aria-label="Качество данных: Нет данных"/)
  assert.match(html, /<dt>Защита от сна<\/dt><dd><span class="telemetry-panel__value">—<\/span><span class="telemetry-panel__quality is-unavailable"/)
})

test('telemetry provenance, stale values, and RunRuntime resource guard state stay distinct', () => {
  const now = new Date().toISOString()
  const old = '2000-01-01T00:00:00.000Z'
  const html = render({
    runtime: {
      task_id: 'run-1', status: 'running', cursor: 0,
      pause_reason: 'resource_guard', resource_reason: 'low_ram', resource_probe_attempts: 2,
      runtime: { enabled: true, keep_awake: true, resources: { free_ram_bytes: 0, rss_bytes: null, snapshot_at: old, warnings: ['Memory below threshold'] }, sleep: { active: false } },
    },
    telemetry: {
      timestamp: now, stale_after_ms: 1000, capabilities: {},
      system: {
        metadata: { uptime_seconds: metric(61, 'exact', now, 'seconds') },
        cpu_usage: metric(42.5, 'exact', now, 'percent'),
        cpu_temperature: metric(71, 'estimated', now, 'celsius'),
        ram_used: metric(null, 'unavailable', now, 'bytes'),
        gpus: [],
      },
      inference: {
        status: 'generating', updated_at: now,
        requested_tokens: metric(100, 'exact', now, 'tokens'),
        output_tokens: metric(5, 'estimated', old, 'tokens'),
      },
    },
  })

  assert.match(html, /Качество данных: Точно/)
  assert.match(html, /Качество данных: Оценка/)
  assert.match(html, /Устарело · оценка/)
  assert.match(html, /Качество данных: Нет данных/)
  assert.match(html, /Приостановлено защитой ресурсов/)
  assert.match(html, /Запрошено, не активно/)
  assert.match(html, /Недостаточно свободной RAM/)
  assert.match(html, /0 B/)
  assert.match(html, /Снимок ресурсов устарел/)
  assert.match(html, /Memory below threshold/)
})

test('fresh warning frames suppress stale warning copies from the last resource snapshot', () => {
  const html = render({
    runtime: {
      task_id: 'run-warning', status: 'running', cursor: 0, runtime_warnings: [],
      runtime: { enabled: true, resources: { warnings: ['warning already cleared'] } },
    },
  })
  assert.doesNotMatch(html, /warning already cleared/)
})

test('persisted inference summaries remain visible when the live telemetry cache is gone', () => {
  const at = new Date().toISOString()
  const html = render({
    runtime: {
      task_id: 'run-summary', status: 'completed', cursor: 4,
      inference_summary: {
        invocation_id: 'invocation-abc', invocation_status: 'completed', snapshot_at: at,
        inference: {
          status: 'completed', source: 'llama.cpp', updated_at: at,
          output_tokens: metric(42, 'exact', at, 'tokens'),
        },
      },
    },
  })
  assert.match(html, /Итог инференса сохранён/)
  assert.match(html, /Сохранённый вызов: Завершён/)
  assert.match(html, /42 токенов/)
})
