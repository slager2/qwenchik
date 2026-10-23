import { useEffect, useId, useState } from 'react'
import type { ReactNode } from 'react'
import { nextTelemetryExpiry } from './telemetry-state.mjs'
import type { RunRuntime, RuntimeTelemetry, TelemetryMetric, TelemetryMetricValue } from './types'

type Metric = TelemetryMetric<TelemetryMetricValue> | null | undefined
type Quality = 'exact' | 'estimated' | 'unavailable' | 'stale' | 'reported' | 'attention' | 'disabled'

export interface TelemetryPanelProps {
  /** Uses runtime.telemetry unless the telemetry prop is explicitly provided. */
  runtime?: RunRuntime | null
  telemetry?: RuntimeTelemetry | null
  className?: string
  showWarnings?: boolean
}

interface MetricRowData {
  label: string
  metric: Metric
}

const qualityLabels: Record<Quality, string> = {
  exact: 'Точно',
  estimated: 'Оценка',
  unavailable: 'Нет данных',
  stale: 'Устарело',
  reported: 'Сообщено',
  attention: 'Требует внимания',
  disabled: 'Отключено',
}

const inferenceStatusLabels: Record<string, string> = {
  queued: 'В очереди', loading: 'Загрузка модели', prefilling: 'Обработка запроса',
  generating: 'Генерация', completed: 'Завершено', failed: 'Ошибка', cancelled: 'Остановлено',
}

const reasonLabels: Record<string, string> = {
  resource_guard: 'Защита ресурсов',
  low_ram: 'Недостаточно свободной RAM', low_vram: 'Недостаточно свободной VRAM',
  high_rss: 'Превышен лимит памяти процесса', gpu_temperature: 'Высокая температура GPU',
  llama_unhealthy: 'Локальная модель недоступна', llama_timeout: 'Истекло ожидание модели',
  model_timeout: 'Истекло ожидание модели', model_oom: 'Модели не хватило памяти',
  telemetry_unavailable: 'Телеметрия ресурсов недоступна', recovery_required: 'Нужна проверка результата вызова',
  manual: 'Пауза пользователя',
}

const numberFormat = new Intl.NumberFormat(undefined, { maximumFractionDigits: 1 })

function number(value: number): string {
  return Number.isFinite(value) ? numberFormat.format(value) : '—'
}

function formatBytes(value: number): string {
  if (!Number.isFinite(value)) return '—'
  if (value === 0) return '0 B'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  const exponent = Math.max(0, Math.min(Math.floor(Math.log(Math.abs(value)) / Math.log(1024)), units.length - 1))
  return `${number(value / (1024 ** exponent))} ${units[exponent]}`
}

function formatDuration(seconds: number): string {
  if (!Number.isFinite(seconds)) return '—'
  const whole = Math.max(0, Math.floor(seconds))
  const days = Math.floor(whole / 86_400)
  const hours = Math.floor((whole % 86_400) / 3_600)
  const minutes = Math.floor((whole % 3_600) / 60)
  const remainder = whole % 60
  if (days) return `${days} д ${hours} ч`
  if (hours) return `${hours} ч ${minutes} мин`
  if (minutes) return `${minutes} мин ${remainder} с`
  return `${remainder} с`
}

function formatValue(value: TelemetryMetricValue, unit: string): string {
  if (typeof value === 'boolean') return value ? 'Да' : 'Нет'
  if (typeof value === 'string') {
    if (unit === 'status') return ({ healthy: 'Доступна', unhealthy: 'Недоступна', unknown: 'Неизвестно', AC: 'От сети', battery: 'От батареи' } as Record<string, string>)[value] || value || '—'
    return value || '—'
  }
  if (!Number.isFinite(value)) return '—'

  switch (unit) {
    case 'bytes': return formatBytes(value)
    case 'percent': return `${number(value)}%`
    case 'celsius': return `${number(value)} °C`
    case 'watts': return `${number(value)} W`
    case 'seconds': return formatDuration(value)
    case 'milliseconds':
    case 'ms': return `${number(value)} ms`
    case 'tokens/s': return `${number(value)} токенов/с`
    case 'tokens': return `${number(value)} токенов`
    case 'slots': return `${number(value)} слотов`
    case 'name':
    case 'status':
    case 'boolean': return number(value)
    default: return unit ? `${number(value)} ${unit}` : number(value)
  }
}

function isStale(updatedAt: string | undefined, staleAfterMs: number | undefined, now: number): boolean {
  if (!updatedAt || !Number.isFinite(staleAfterMs) || staleAfterMs! <= 0) return false
  const timestamp = Date.parse(updatedAt)
  return Number.isFinite(timestamp) && now - timestamp > staleAfterMs!
}

function metricPresentation(metric: Metric, staleAfterMs: number | undefined, now: number) {
  if (!metric || typeof metric !== 'object' || metric.value == null || metric.value === '' || (typeof metric.value === 'number' && !Number.isFinite(metric.value)) || metric.source === 'unavailable') {
    return { value: '—', quality: 'unavailable' as Quality, detail: qualityLabels.unavailable }
  }
  if (metric.source !== 'exact' && metric.source !== 'estimated') {
    return { value: '—', quality: 'unavailable' as Quality, detail: qualityLabels.unavailable }
  }

  if (isStale(metric.updated_at, staleAfterMs, now)) {
    return {
      value: formatValue(metric.value, metric.unit || ''),
      quality: 'stale' as Quality,
      detail: `Устарело; ранее: ${qualityLabels[metric.source].toLowerCase()}`,
    }
  }

  return {
    value: formatValue(metric.value, metric.unit || ''),
    quality: metric.source,
    detail: qualityLabels[metric.source],
  }
}

function MetricRows({ rows, staleAfterMs, now }: { rows: MetricRowData[]; staleAfterMs?: number; now: number }) {
  return <dl className="telemetry-panel__metrics">
    {rows.map(({ label, metric }) => {
      const presentation = metricPresentation(metric, staleAfterMs, now)
       const labelText = presentation.quality === 'stale'
        ? `Устарело · ${metric?.source === 'estimated' ? 'оценка' : 'точно'}`
        : qualityLabels[presentation.quality]
      return <div className="telemetry-panel__metric" key={label}>
        <dt>{label}</dt>
        <dd>
          <span className="telemetry-panel__value">{presentation.value}</span>
          <span className={`telemetry-panel__quality is-${presentation.quality}`} aria-label={`Качество данных: ${presentation.detail}`} title={presentation.detail}>{labelText}</span>
        </dd>
      </div>
    })}
  </dl>
}

function InfoRow({ label, value, quality = 'reported', detail }: { label: string; value: string; quality?: Quality; detail?: string }) {
  const unavailable = quality === 'unavailable' || !value
  const state = unavailable ? 'unavailable' : quality
  const text = unavailable ? '—' : value
  const qualityLabel = qualityLabels[state]
  return <div className="telemetry-panel__metric">
    <dt>{label}</dt>
    <dd>
      <span className="telemetry-panel__value">{text}</span>
      <span className={`telemetry-panel__quality is-${state}`} aria-label={`Качество данных: ${detail || qualityLabel}`} title={detail || qualityLabel}>{state === 'stale' ? 'Устарело' : qualityLabel}</span>
    </dd>
  </div>
}

function plainValue(value: number | string | null | undefined, snapshotAt: string | undefined, staleAfterMs: number | undefined, now: number, formatter: (value: number | string) => string): { value: string; quality: Quality } {
  if (value == null || value === '' || (typeof value === 'number' && !Number.isFinite(value))) return { value: '', quality: 'unavailable' }
  return {
    value: formatter(value),
    quality: isStale(snapshotAt, staleAfterMs, now) ? 'stale' : 'reported',
  }
}

function guardState(runtime: RunRuntime | null | undefined): { label: string; quality: Quality } {
  if (!runtime) return { label: 'Нет данных', quality: 'unavailable' }
  if (runtime.pause_reason === 'resource_guard') return { label: 'Приостановлено защитой ресурсов', quality: 'attention' }
  if (runtime.runtime?.enabled === false) return { label: 'Отключён', quality: 'disabled' }
  if (runtime.resource_reason) return { label: 'Требует внимания', quality: 'attention' }
  if (runtime.runtime?.enabled === true) return { label: 'Мониторинг включён', quality: 'reported' }
  return { label: 'Состояние неизвестно', quality: 'unavailable' }
}

function GuardSection({ runtime, staleAfterMs, now, headingId, showWarnings }: { runtime: RunRuntime | null | undefined; staleAfterMs?: number; now: number; headingId: string; showWarnings: boolean }) {
  const resources = runtime?.runtime?.resources
  const sleep = runtime?.runtime?.sleep
  const warnings = runtime?.runtime_warnings ?? resources?.warnings ?? []
  const snapshotAt = resources?.snapshot_at
  const state = guardState(runtime)
  const rows: ReactNode[] = []

  const addResource = (label: string, value: number | string | null | undefined, formatter: (value: number | string) => string) => {
    const item = plainValue(value, snapshotAt, staleAfterMs, now, formatter)
    rows.push(<InfoRow key={label} label={label} value={item.value} quality={item.quality} detail={item.quality === 'stale' ? 'Снимок ресурсов устарел' : undefined} />)
  }

  const pauseReason = runtime?.pause_reason
  const resourceReason = runtime?.resource_reason
  const nextProbe = runtime?.resume_after
  const nextProbeLabel = nextProbe && Number.isFinite(Date.parse(nextProbe)) ? new Date(nextProbe).toLocaleString() : nextProbe || ''
  const keepAwakeRequested = runtime?.keep_awake_requested ?? runtime?.runtime?.keep_awake
  const keepAwakeKnown = keepAwakeRequested != null || sleep?.active != null
  const keepAwake = keepAwakeRequested === true
    ? sleep?.active === true ? 'Активно' : sleep?.active === false ? 'Запрошено, не активно' : 'Запрошено'
    : keepAwakeRequested === false
      ? sleep?.active === true ? 'Активно' : 'Не запрошено'
      : sleep?.active === true ? 'Активно' : sleep?.active === false ? 'Не активно' : ''

  addResource('Свободная RAM', resources?.free_ram_bytes, (value) => formatBytes(Number(value)))
  addResource('Память процесса (RSS)', resources?.rss_bytes, (value) => formatBytes(Number(value)))
  addResource('Свободная VRAM', resources?.gpu_free_vram_mb, (value) => `${number(Number(value))} MiB`)
  addResource('Температура GPU', resources?.gpu_temp_c, (value) => `${number(Number(value))} °C`)
  addResource('Источник питания', resources?.power_source, (value) => value === 'AC' ? 'От сети' : value === 'battery' ? 'От батареи' : value === 'unknown' ? 'Неизвестно' : String(value))

  return <details className="telemetry-panel__card" aria-labelledby={headingId}>
    <summary className="telemetry-panel__card-heading">
      <h3 id={headingId}>Контроль ресурсов</h3>
      <span className={`telemetry-panel__quality is-${state.quality}`} aria-label={`Состояние защиты: ${state.label}`}>{state.label}</span>
    </summary>
    <dl className="telemetry-panel__metrics">
      <InfoRow label="Защита включена" value={runtime?.runtime?.enabled == null ? '' : runtime.runtime.enabled ? 'Да' : 'Нет'} quality={runtime?.runtime?.enabled == null ? 'unavailable' : 'reported'} />
      {pauseReason && <InfoRow label="Причина паузы" value={reasonLabels[pauseReason] || pauseReason} quality="reported" />}
      {resourceReason && <InfoRow label="Проблема с ресурсами" value={reasonLabels[resourceReason] || resourceReason} quality="reported" />}
      {rows}
      <InfoRow label="Защита от сна" value={keepAwake} quality={keepAwakeKnown ? 'reported' : 'unavailable'} detail={sleep?.failure_reason || (sleep?.active === false ? 'Запрошено, но не активно' : undefined)} />
      <InfoRow label="Провайдер защиты доступен" value={sleep?.available == null ? '' : sleep.available ? 'Да' : 'Нет'} quality={sleep?.available == null ? 'unavailable' : 'reported'} detail={sleep?.failure_reason || undefined} />
      <InfoRow label="Провайдер защиты" value={sleep?.provider || ''} quality={sleep?.provider ? 'reported' : 'unavailable'} />
      <InfoRow label="Проверок ресурсов" value={runtime?.resource_probe_attempts == null ? '' : number(runtime.resource_probe_attempts)} quality={runtime?.resource_probe_attempts == null ? 'unavailable' : 'reported'} />
      <InfoRow label="Следующая проверка" value={nextProbeLabel} quality={nextProbeLabel ? 'reported' : 'unavailable'} />
    </dl>
    {snapshotAt && Number.isFinite(Date.parse(snapshotAt)) && <p className="telemetry-panel__note">Снимок ресурсов: <time dateTime={snapshotAt}>{new Date(snapshotAt).toLocaleString()}</time>{isStale(snapshotAt, staleAfterMs, now) ? ' · устарел' : ''}</p>}
    {runtime?.runtime?.sleep_policy && <p className="telemetry-panel__note">Политика защиты от сна: {runtime.runtime.sleep_policy}</p>}
    {sleep?.failure_reason && <p className="telemetry-panel__warning">Проблема защиты от сна: {sleep.failure_reason}</p>}
    {showWarnings && warnings.length ? <ul className="telemetry-panel__warnings" aria-label="Предупреждения runtime">{warnings.map((warning, index) => <li key={`${warning}-${index}`}>{warning}</li>)}</ul> : null}
  </details>
}

function statusPresentation(status: string | undefined, updatedAt: string | undefined, staleAfterMs: number | undefined, now: number) {
  if (!status) return { value: 'Нет данных', quality: 'unavailable' as Quality, detail: 'Состояние инференса недоступно' }
  if (isStale(updatedAt, staleAfterMs, now)) return { value: inferenceStatusLabels[status] || status, quality: 'stale' as Quality, detail: 'Состояние инференса устарело' }
  return { value: inferenceStatusLabels[status] || status, quality: 'reported' as Quality, detail: 'Состояние инференса получено' }
}

export default function TelemetryPanel({ runtime, telemetry: telemetryOverride, className = '', showWarnings = true }: TelemetryPanelProps) {
  const generatedId = useId()
  const headingId = `telemetry-panel-${generatedId}`
  const telemetry = telemetryOverride === undefined ? runtime?.telemetry : telemetryOverride
  const system = telemetry?.system
  const inference = telemetry?.inference
  const [now, setNow] = useState(() => Date.now())
  const staleAfterMs = telemetry?.stale_after_ms ?? 30_000
  useEffect(() => {
    const expiry = nextTelemetryExpiry(telemetry, runtime, now)
    if (expiry === undefined) return
    const timer = window.setTimeout(() => setNow(Date.now()), Math.max(0, expiry - now))
    return () => window.clearTimeout(timer)
  }, [now, runtime, telemetry])
  const sampleStale = telemetry ? isStale(telemetry.timestamp, staleAfterMs, now) : false
  const sampleTimestampValid = !!telemetry?.timestamp && Number.isFinite(Date.parse(telemetry.timestamp))
  const gpuList = Array.isArray(system?.gpus) ? system.gpus : []
  const inferenceStatus = statusPresentation(inference?.status, inference?.updated_at, staleAfterMs, now)
  const panelClass = ['telemetry-panel', className].filter(Boolean).join(' ')

  return <section className={panelClass} aria-labelledby={headingId}>
    <header className="telemetry-panel__heading">
      <div>
        <p className="telemetry-panel__eyebrow">ВЫПОЛНЕНИЕ</p>
        <h2 id={headingId}>Телеметрия</h2>
      </div>
      <div className="telemetry-panel__snapshot">
        <span className={`telemetry-panel__quality is-${!telemetry || !sampleTimestampValid ? 'unavailable' : sampleStale ? 'stale' : 'reported'}`}>
          {!telemetry ? 'Нет данных' : !sampleTimestampValid ? 'Время снимка неизвестно' : sampleStale ? 'Снимок устарел' : 'Снимок получен'}
        </span>
        {telemetry?.timestamp && Number.isFinite(Date.parse(telemetry.timestamp)) && <time dateTime={telemetry.timestamp}>{new Date(telemetry.timestamp).toLocaleTimeString()}</time>}
      </div>
    </header>

    <div className="telemetry-panel__grid">
      <details className="telemetry-panel__card" aria-labelledby={`${headingId}-system`}>
        <summary className="telemetry-panel__card-heading"><h3 id={`${headingId}-system`}>Система</h3></summary>
        {(system?.metadata?.platform || system?.metadata?.os) && <p className="telemetry-panel__note">{[system.metadata.platform, system.metadata.os].filter(Boolean).join(' · ')}</p>}
        <MetricRows staleAfterMs={staleAfterMs} now={now} rows={[
          { label: 'Время работы', metric: system?.metadata?.uptime_seconds },
          { label: 'Загрузка CPU', metric: system?.cpu_usage },
          { label: 'Температура CPU', metric: system?.cpu_temperature },
          { label: 'Занято RAM', metric: system?.ram_used },
          { label: 'Свободно RAM', metric: system?.ram_free },
          { label: 'Всего RAM', metric: system?.ram_total },
          { label: 'RSS агента', metric: system?.agent_rss },
          { label: 'RSS сервера модели', metric: system?.llama_server_rss },
          { label: 'Источник питания', metric: system?.power_source },
          { label: 'Потребление', metric: system?.power_watts },
          { label: 'Заряд батареи', metric: system?.battery_percent },
          { label: 'Зарядка', metric: system?.battery_charging },
          { label: 'Состояние модели', metric: system?.llama_health },
          { label: 'Активные слоты модели', metric: system?.llama_slots_active },
          { label: 'Всего слотов модели', metric: system?.llama_slots_total },
        ]} />
        {gpuList.map((gpu, index) => <section className="telemetry-panel__gpu" aria-label={`Графический процессор ${index + 1}`} key={`gpu-${index}`}>
          <h4>GPU {index + 1}</h4>
          <MetricRows staleAfterMs={staleAfterMs} now={now} rows={[
            { label: 'Устройство', metric: gpu?.name },
            { label: 'Загрузка', metric: gpu?.utilization },
            { label: 'Температура', metric: gpu?.temperature },
            { label: 'Занято VRAM', metric: gpu?.vram_used },
            { label: 'Свободно VRAM', metric: gpu?.vram_free },
            { label: 'Всего VRAM', metric: gpu?.vram_total },
            { label: 'Потребление', metric: gpu?.power_watts },
          ]} />
        </section>)}
        {!gpuList.length && <p className="telemetry-panel__note">{system ? 'Данные GPU не переданы.' : 'Данные GPU недоступны.'}</p>}
      </details>

      <details className="telemetry-panel__card" aria-labelledby={`${headingId}-inference`}>
        <summary className="telemetry-panel__card-heading">
          <h3 id={`${headingId}-inference`}>Инференс</h3>
          <span className={`telemetry-panel__quality is-${inferenceStatus.quality}`} aria-label={`Состояние инференса: ${inferenceStatus.detail}`} title={inferenceStatus.detail}>{inferenceStatus.value}</span>
        </summary>
        {inference?.source && <p className="telemetry-panel__note">Источник данных: {inference.source}</p>}
        <MetricRows staleAfterMs={staleAfterMs} now={now} rows={[
          { label: 'Запрошено токенов', metric: inference?.requested_tokens },
          { label: 'Токенов запроса', metric: inference?.prompt_tokens },
          { label: 'Токенов ответа', metric: inference?.output_tokens },
          { label: 'Всего токенов', metric: inference?.total_tokens },
          { label: 'Из кэша запроса', metric: inference?.cached_prompt_tokens },
          { label: 'Скорость обработки запроса', metric: inference?.prompt_tokens_per_second },
          { label: 'Скорость генерации', metric: inference?.output_tokens_per_second },
          { label: 'До первого токена', metric: inference?.ttft_ms },
          { label: 'Время обработки запроса', metric: inference?.prompt_ms },
          { label: 'Время генерации', metric: inference?.generation_ms },
          { label: 'Токенов в контексте', metric: inference?.context_tokens },
          { label: 'Размер окна контекста', metric: inference?.context_window },
        ]} />
      </details>

      <GuardSection runtime={runtime} staleAfterMs={staleAfterMs} now={now} headingId={`${headingId}-guard`} showWarnings={showWarnings} />
    </div>
  </section>
}
