import test from 'node:test'
import assert from 'node:assert/strict'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'

const { default: TelemetryPanel } = await import('./TelemetryPanel.tsx')
const metric = (value, unit = '', source = 'exact', updated_at = new Date().toISOString()) => ({ value, source, unit, updated_at })
const render = (runtime, props = {}) => renderToStaticMarkup(createElement(TelemetryPanel, { runtime, ...props }))
const ticker = html => html.slice(html.indexOf('class="telemetry-panel__ticker"'), html.indexOf('<button'))

test('ticker uses live GPU temperature/free VRAM, speed and free RAM', () => {
  const html = render({ telemetry: {
    system: { gpus: [{ temperature: metric(65, 'celsius'), vram_free: metric(8 * 1024 ** 3, 'bytes') }], ram_free: metric(16 * 1024 ** 3, 'bytes') },
    inference: { output_tokens_per_second: metric(24, 'tokens/s') },
  } }, { className: 'custom-panel' })
  const strip = ticker(html)
  assert.match(html, /telemetry-panel custom-panel/)
  assert.match(strip, /GPU/)
  assert.match(strip, /65°/)
  assert.match(strip, /8G/)
  assert.match(strip, /Speed/)
  assert.match(strip, /24/)
  assert.match(strip, /tok\/s/)
  assert.match(strip, /RAM/)
  assert.match(strip, /16G/)
  assert.doesNotMatch(html, /<(?:details|summary)\b|telemetry-panel__quality/)
  assert.match(html, /aria-expanded="false"/)
  assert.match(html, /data-expanded="false" inert="" aria-hidden="true"/)
  assert.match(html, /Подробная диагностика/)
})

test('missing telemetry stays unavailable and never reads legacy or persisted metrics', () => {
  const html = render({
    runtime: { resources: { free_ram_bytes: 123456, gpu_temp_c: 99 } },
    inference_summary: { inference: { output_tokens: metric(123456), output_tokens_per_second: metric(999) } },
  })
  assert.match(html, /Телеметрия недоступна/)
  assert.match(html, /Нет данных/)
  assert.match(ticker(html), /—/)
  assert.doesNotMatch(ticker(html), /Speed|0G|99°/)
  assert.doesNotMatch(html, /123456|999|Итог инференса/)
  assert.doesNotThrow(() => render(null))
})

test('diagnostics retain provenance, stale and unavailable distinctions without badges', () => {
  const html = render({ telemetry: {
    system: { cpu_usage: metric(42, 'percent'), cpu_temperature: metric(71, 'celsius', 'estimated'), ram_free: metric(null, 'bytes', 'unavailable'), gpus: [] },
    inference: { output_tokens: metric(5, 'tokens', 'estimated', '2000-01-01T00:00:00Z') },
  } })
  assert.match(html, /Качество данных: Точно/)
  assert.match(html, /Качество данных: Оценка/)
  assert.match(html, /Устарело; ранее: оценка/)
  assert.match(html, /Качество данных: Нет данных/)
  assert.match(html, /5 токенов/)
})

test('known zero is displayed, unavailable speed is omitted', () => {
  const html = render({ telemetry: { system: { ram_free: metric(0, 'bytes'), gpus: [] }, inference: { output_tokens_per_second: metric(0, 'tokens/s') } } })
  assert.match(ticker(html), /0G/)
  assert.match(ticker(html), /Speed <strong>0<\/strong>/)
  const unavailable = render({ telemetry: { inference: { output_tokens_per_second: metric(0, 'tokens/s', 'unavailable') } } })
  assert.doesNotMatch(ticker(unavailable), /Speed/)
})

test('hashes show first six and last four characters with accessible full clipboard targets', () => {
  const hash = 'abcdef0123456789fedcba9876543210'
  const html = render({ bundle_hash: hash, context_snapshot_hash: hash, compactions: 2 })
  assert.equal((html.match(/>abcdef\.\.\.3210<\/button>/g) || []).length, 2)
  assert.match(html, new RegExp(`aria-label="Копировать Bundle: ${hash}"`))
  assert.match(html, /role="status"/)
  assert.match(html, /Сжатия контекста/)
})

test('partial readiness verdict has a localized label', () => {
  const html = render({ verdict: 'PARTIAL' })
  assert.match(html, /Готовность evidence/)
  assert.match(html, /Частичный результат/)
  assert.doesNotMatch(html, /Статус неизвестен/)
})

test('guard warnings stay visible above disclosure and respect showWarnings', () => {
  const runtime = { pause_reason: 'resource_guard', resource_reason: 'low_ram', runtime_warnings: ['Memory below threshold'], runtime: { enabled: true, keep_awake: true, sleep: { active: false }, resources: { warnings: ['obsolete warning'] } } }
  const html = render(runtime)
  assert.ok(html.indexOf('Memory below threshold') < html.indexOf('Подробная диагностика'))
  assert.match(html, /Приостановлено защитой ресурсов/)
  assert.match(html, /Недостаточно свободной RAM/)
  assert.match(html, /Запрошено, не активно/)
  assert.doesNotMatch(html, /obsolete warning/)
  assert.doesNotMatch(render(runtime, { showWarnings: false }), /Memory below threshold/)
  assert.doesNotMatch(render({ ...runtime, runtime_warnings: [] }), /Memory below threshold|obsolete warning/)
})
