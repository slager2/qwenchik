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
  assert.doesNotMatch(ticker(html), /123456|999|Итог инференса/)
  assert.match(html, /Последний сохранённый вызов/)
  assert.match(html, /Исторические данные, не текущая скорость или состояние ресурсов/)
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

test('touch-visible freshness distinguishes stale, estimated and unknown readings', () => {
  const html = render({ telemetry: {
    timestamp: '2000-01-01T00:00:00Z', stale_after_ms: 1000,
    system: { ram_free: metric(4 * 1024 ** 3, 'bytes', 'exact', '2000-01-01T00:00:00Z'), gpus: [{ temperature: metric(61, 'celsius', 'estimated'), vram_free: { value: 2 * 1024 ** 3, unit: 'bytes', source: 'exact' } }] },
    inference: { status: 'generating', output_tokens_per_second: metric(9, 'tokens/s', 'estimated', '2000-01-01T00:00:00Z') },
  } })
  const strip = ticker(html)
  assert.match(strip, /class="telemetry-panel__ticker-quality">Устарело/)
  assert.match(strip, /class="telemetry-panel__ticker-quality">Оценка/)
  assert.match(strip, /class="telemetry-panel__ticker-quality">Время неизвестно/)
  assert.match(strip, /Снимок устарел/)
  assert.match(html, /Генерация · Время состояния инференса неизвестно/)
  assert.match(html, /class="telemetry-panel__freshness">Устарело/)
})

test('saved invocation is a separate closed disclosure with exact provenance, never live speed', () => {
  const html = render({ telemetry: {
    system: { ram_free: metric(3 * 1024 ** 3, 'bytes') },
    inference: { output_tokens_per_second: metric(12, 'tokens/s') },
  }, inference_summary: {
    invocation_id: 'saved-invocation', invocation_status: 'failed', snapshot_at: '2026-10-28T13:42:17.123Z',
    inference: { source: 'llama.cpp timings', output_tokens_per_second: metric(987, 'tokens/s'), output_tokens: metric(4321, 'tokens') },
  } })
  assert.match(ticker(html), /Speed <strong>12<\/strong>/)
  assert.doesNotMatch(ticker(html), /987|4321|saved-invocation/)
  const saved = html.slice(html.indexOf('<details class="telemetry-panel__saved"'), html.indexOf('</details>'))
  assert.match(saved, /^<details class="telemetry-panel__saved"><summary>/)
  assert.match(saved, /saved-invocation/)
  assert.match(saved, /llama.cpp timings/)
  assert.match(saved, /dateTime="2026-10-28T13:42:17.123Z">2026-10-28T13:42:17.123Z/)
  assert.match(saved, /987 токенов\/с/)
  assert.match(saved, /Ошибка/)
})

test('resource reasons are localized and a manual resource hold is not automatic recovery', () => {
  for (const [reason, label] of [
    ['high_model_rss', 'Превышен лимит памяти сервера модели'],
    ['context_mismatch', 'Контекст модели не совпадает с настройками'],
    ['context_unverified', 'Размер контекста модели не подтверждён'],
    ['context_capacity', 'Запрос не помещается в контекст модели'],
    ['model_resource', 'Модель остановлена из-за ресурсов'],
  ]) {
    const html = render({ pause_reason: 'manual', resource_reason: reason })
    assert.match(html, new RegExp(label))
    assert.match(html, /Ручная пауза с ограничением ресурсов — автоматического продолжения нет/)
    assert.doesNotMatch(html, new RegExp(`>${reason}<`))
  }
  assert.match(render({ pause_reason: 'recovery_required' }), /Исход вызова неизвестен/)
  assert.match(render({ pause_reason: 'resource_guard' }), /Приостановлено защитой ресурсов/)
})
