import { useEffect, useId, useState } from 'react'
import { nextTelemetryExpiry } from './telemetry-state.mjs'
import type { RunRuntime, TelemetryMetric, TelemetryMetricValue } from './types'

type Metric = TelemetryMetric<TelemetryMetricValue> | null | undefined
type Quality = 'exact' | 'estimated' | 'unavailable' | 'stale' | 'reported' | 'attention' | 'disabled'

export interface TelemetryPanelProps {
  runtime?: RunRuntime | null
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

const verdictLabels: Record<string, string> = { PASS: 'Evidence достаточно', RESEARCH: 'Нужно исследование', REPAIR: 'Нужно исправление', UNCERTAIN: 'Есть противоречия', BLOCK: 'Завершение заблокировано' }
const notificationLabels: Record<string, string> = { pending: 'В очереди', attempted: 'Доставка начата', failed: 'Повтор доставки запланирован', delivered: 'Принято провайдером', abandoned: 'Доставка недоступна' }

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
      const detail = `${presentation.detail}${metric?.updated_at ? `; обновлено: ${metric.updated_at}` : ''}${metric?.error ? `; ${metric.error}` : ''}`
      return <div className="telemetry-panel__metric" key={label}>
        <dt>{label}</dt>
        <dd>
          <span className="telemetry-panel__value" title={detail}>{presentation.value}</span>
          <span className="telemetry-panel__sr-only">Качество данных: {detail}</span>
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
      <span className="telemetry-panel__value" title={detail || qualityLabel}>{text}</span>
      <span className="telemetry-panel__sr-only">{detail || qualityLabel}</span>
    </dd>
  </div>
}

function guardState(runtime: RunRuntime | null | undefined): { label: string; quality: Quality } {
  if (!runtime) return { label: 'Нет данных', quality: 'unavailable' }
  if (runtime.pause_reason === 'resource_guard') return { label: 'Приостановлено защитой ресурсов', quality: 'attention' }
  if (runtime.runtime?.enabled === false) return { label: 'Отключён', quality: 'disabled' }
  if (runtime.resource_reason) return { label: 'Требует внимания', quality: 'attention' }
  if (runtime.runtime?.enabled === true) return { label: 'Мониторинг включён', quality: 'reported' }
  return { label: 'Состояние неизвестно', quality: 'unavailable' }
}

function GuardSection({ runtime, headingId }: { runtime: RunRuntime | null | undefined; headingId: string }) {
  const sleep = runtime?.runtime?.sleep
  const state = guardState(runtime)

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

  return <section className="telemetry-panel__card" aria-labelledby={headingId}>
    <header className="telemetry-panel__card-heading">
      <h3 id={headingId}>Контроль ресурсов</h3>
      <span className="telemetry-panel__note">{state.label}</span>
    </header>
    <dl className="telemetry-panel__metrics">
      <InfoRow label="Защита включена" value={runtime?.runtime?.enabled == null ? '' : runtime.runtime.enabled ? 'Да' : 'Нет'} quality={runtime?.runtime?.enabled == null ? 'unavailable' : 'reported'} />
      {pauseReason && <InfoRow label="Причина паузы" value={reasonLabels[pauseReason] || pauseReason} quality="reported" />}
      {resourceReason && <InfoRow label="Проблема с ресурсами" value={reasonLabels[resourceReason] || resourceReason} quality="reported" />}
      <InfoRow label="Защита от сна" value={keepAwake} quality={keepAwakeKnown ? 'reported' : 'unavailable'} detail={sleep?.failure_reason || (sleep?.active === false ? 'Запрошено, но не активно' : undefined)} />
      <InfoRow label="Провайдер защиты доступен" value={sleep?.available == null ? '' : sleep.available ? 'Да' : 'Нет'} quality={sleep?.available == null ? 'unavailable' : 'reported'} detail={sleep?.failure_reason || undefined} />
      <InfoRow label="Провайдер защиты" value={sleep?.provider || ''} quality={sleep?.provider ? 'reported' : 'unavailable'} />
      <InfoRow label="Проверок ресурсов" value={runtime?.resource_probe_attempts == null ? '' : number(runtime.resource_probe_attempts)} quality={runtime?.resource_probe_attempts == null ? 'unavailable' : 'reported'} />
      <InfoRow label="Следующая проверка" value={nextProbeLabel} quality={nextProbeLabel ? 'reported' : 'unavailable'} />
    </dl>
    {runtime?.runtime?.sleep_policy && <p className="telemetry-panel__note">Политика защиты от сна: {runtime.runtime.sleep_policy}</p>}
    {sleep?.failure_reason && <p className="telemetry-panel__warning">Проблема защиты от сна: {sleep.failure_reason}</p>}
  </section>
}

function HashRow({ label, value }: { label: string; value?: string }) {
  const [state, setState] = useState<'idle' | 'loading' | 'success' | 'error'>('idle')
  useEffect(() => { setState('idle') }, [value])
  const copy = async () => {
    if (!value) return
    setState('loading')
    try {
      await navigator.clipboard.writeText(value)
      setState('success')
    } catch {
      setState('error')
    }
  }
  return <div className="telemetry-panel__metric">
    <dt>{label}</dt>
    <dd>{value ? <button type="button" className="telemetry-panel__copy" data-state={state} disabled={state === 'loading'} onClick={copy} title={value} aria-label={`Копировать ${label}: ${value}`}>
      {value.length > 10 ? `${value.slice(0, 6)}...${value.slice(-4)}` : value}
    </button> : <span title="Нет данных">—</span>}
      <span className="telemetry-panel__copy-status" role="status">{state === 'success' ? 'Скопировано' : state === 'error' ? 'Не удалось скопировать' : state === 'loading' ? 'Копирование…' : ''}</span>
    </dd>
  </div>
}

function statusPresentation(status: string | undefined, updatedAt: string | undefined, staleAfterMs: number | undefined, now: number) {
  if (!status) return { value: 'Нет данных', quality: 'unavailable' as Quality, detail: 'Состояние инференса недоступно' }
  if (isStale(updatedAt, staleAfterMs, now)) return { value: inferenceStatusLabels[status] || status, quality: 'stale' as Quality, detail: 'Состояние инференса устарело' }
  return { value: inferenceStatusLabels[status] || status, quality: 'reported' as Quality, detail: 'Состояние инференса получено' }
}

export default function TelemetryPanel({ runtime, className = '', showWarnings = true }: TelemetryPanelProps) {
  const generatedId = useId()
  const headingId = `telemetry-panel-${generatedId}`
  const telemetry = runtime?.telemetry
  const system = telemetry?.system
  const inference = telemetry?.inference
  const [expanded, setExpanded] = useState(false)
  const [now, setNow] = useState(() => Date.now())
  const staleAfterMs = telemetry?.stale_after_ms ?? 30_000
  useEffect(() => {
    const expiry = nextTelemetryExpiry(telemetry, undefined, now)
    if (expiry === undefined) return
    const timer = window.setTimeout(() => setNow(Date.now()), Math.max(0, expiry - now))
    return () => window.clearTimeout(timer)
  }, [now, runtime, telemetry])
  const snapshotTimestamp = telemetry?.timestamp
  const sampleStale = snapshotTimestamp ? isStale(snapshotTimestamp, staleAfterMs, now) : false
  const sampleTimestampValid = !!snapshotTimestamp && Number.isFinite(Date.parse(snapshotTimestamp))
  const gpuList = Array.isArray(system?.gpus) ? system.gpus : []
  const inferenceStatus = statusPresentation(inference?.status, inference?.updated_at, staleAfterMs, now)
  const panelClass = ['telemetry-panel', className].filter(Boolean).join(' ')
  const warnings = runtime?.runtime_warnings ?? runtime?.runtime?.active_warnings ?? runtime?.runtime?.warnings ?? []
  const speed = metricPresentation(inference?.output_tokens_per_second, staleAfterMs, now)
  const tickerValue = (metric: Metric, memory = false) => {
    const item = metricPresentation(metric, staleAfterMs, now)
    const value = item.quality === 'unavailable' ? '—' : memory && typeof metric?.value === 'number'
      ? `${number(metric.value / 1024 ** 3)}G`
      : typeof metric?.value === 'number' ? `${number(metric.value)}°` : item.value
    return <span title={item.detail}>{value}<span className="telemetry-panel__sr-only"> · {item.detail}{memory ? ', GiB свободно' : ', °C'}</span></span>
  }

  return <section className={panelClass} aria-label="Телеметрия">
    <div className="telemetry-panel__ticker" aria-label="Ресурсы: первый GPU, свободная память и скорость генерации">
      <span className="telemetry-panel__ticker-item">GPU {tickerValue(gpuList[0]?.temperature)} <span className="telemetry-panel__ticker-memory" title="Свободная VRAM">{tickerValue(gpuList[0]?.vram_free, true)}</span></span>
      {speed.quality !== 'unavailable' && <span className="telemetry-panel__ticker-item" title={speed.detail}>Speed <strong>{typeof inference?.output_tokens_per_second?.value === 'number' ? number(inference.output_tokens_per_second.value) : '—'}</strong><span>tok/s</span></span>}
      <span className="telemetry-panel__ticker-item">RAM {tickerValue(system?.ram_free, true)}</span>
    </div>
    {showWarnings && <>
      {runtime?.pause_reason && <p className="telemetry-panel__warning">{runtime.pause_reason === 'resource_guard' ? 'Приостановлено защитой ресурсов' : reasonLabels[runtime.pause_reason] || runtime.pause_reason}</p>}
      {runtime?.resource_reason && <p className="telemetry-panel__warning">{reasonLabels[runtime.resource_reason] || runtime.resource_reason}</p>}
      {warnings.length > 0 && <ul className="telemetry-panel__warnings" aria-label="Предупреждения runtime">{warnings.map((warning, index) => <li key={index}>{warning}</li>)}</ul>}
    </>}
    <button type="button" id={headingId} className="telemetry-panel__disclosure" aria-expanded={expanded} aria-controls={`${headingId}-diagnostics`} onClick={() => setExpanded(!expanded)}>
      Подробная диагностика <svg className="telemetry-panel__chevron" viewBox="0 0 16 16" width="16" height="16" aria-hidden="true"><path d="m5 6 3 3 3-3" /></svg>
    </button>
    <div id={`${headingId}-diagnostics`} className="telemetry-panel__disclosure-grid" data-expanded={expanded} inert={!expanded} aria-hidden={!expanded}>
    <div className="telemetry-panel__disclosure-content">
      <p className="telemetry-panel__note">{!telemetry ? 'Телеметрия недоступна.' : !sampleTimestampValid ? 'Время снимка неизвестно' : sampleStale ? 'Снимок устарел' : 'Снимок получен'}{sampleTimestampValid && <> · <time dateTime={snapshotTimestamp}>{new Date(snapshotTimestamp!).toLocaleTimeString()}</time></>}</p>
      <section className="telemetry-panel__card" aria-labelledby={`${headingId}-system`}>
        <header className="telemetry-panel__card-heading"><h3 id={`${headingId}-system`}>Система</h3></header>
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
      </section>

      <section className="telemetry-panel__card" aria-labelledby={`${headingId}-inference`}>
        <header className="telemetry-panel__card-heading">
          <h3 id={`${headingId}-inference`}>Инференс</h3>
          <span className="telemetry-panel__note" title={inferenceStatus.detail}>{inferenceStatus.value}</span>
        </header>
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
      </section>

      <section className="telemetry-panel__card" aria-label="Выполнение">
        <h3>Выполнение</h3>
        <dl className="telemetry-panel__metrics">
          <HashRow label="Bundle" value={runtime?.bundle_hash} />
          <HashRow label="Контекст" value={runtime?.context_snapshot_hash} />
          <InfoRow label="Сжатия контекста" value={runtime?.compactions == null ? '' : number(runtime.compactions)} />
          <InfoRow label="Профиль" value={runtime?.profile || ''} />
          <InfoRow label="Прошло / осталось" value={runtime?.elapsed_seconds == null && runtime?.remaining_seconds == null ? '' : `${runtime?.elapsed_seconds == null ? '—' : formatDuration(runtime.elapsed_seconds)} / ${runtime?.remaining_seconds == null ? '—' : formatDuration(runtime.remaining_seconds)}`} />
          <InfoRow label="Вызовы модели / лимит" value={`${runtime?.counters?.llm_calls ?? '—'} / ${runtime?.budget?.max_llm_calls ?? '—'}`} />
          <InfoRow label="Поиски / лимит" value={`${runtime?.counters?.search_queries ?? '—'} / ${runtime?.budget?.max_search_queries ?? '—'}`} />
          <InfoRow label="Загрузки / лимит" value={`${runtime?.counters?.fetches ?? '—'} / ${runtime?.budget?.max_fetches ?? '—'}`} />
          <InfoRow label="Остаток загрузок" value={runtime?.budget_remaining?.fetched_bytes == null ? '' : formatBytes(runtime.budget_remaining.fetched_bytes)} />
          <InfoRow label="Продолжения / лимит" value={`${runtime?.completion?.continuations ?? '—'} / ${runtime?.max_continuations ?? '—'}`} />
          {runtime?.verdict && <InfoRow label="Готовность evidence" value={verdictLabels[runtime.verdict] || 'Статус неизвестен'} />}
          {runtime?.notification && <InfoRow label="Уведомление" value={notificationLabels[runtime.notification.status] || 'Статус неизвестен'} />}
        </dl>
      </section>
      <GuardSection runtime={runtime} headingId={`${headingId}-guard`} />
    </div>
    </div>
  </section>
}
