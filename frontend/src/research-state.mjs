import { normalizeSources, sourceErrorCode } from './conversation-state.mjs'

const text = (value, limit = 600) => typeof value === 'string' ? value.replace(/[\u0000-\u0008\u000b\u000c\u000e-\u001f]/g, '').slice(0, limit) : ''
export const publicResearchEvents = (events) => events.filter((event) => event?.visibility === 'user' && Number.isSafeInteger(event.seq) && event.seq >= 0 && event.payload && typeof event.payload === 'object').sort((a, b) => a.seq - b.seq)

export function planningFailure(run, events) {
  if (run?.status !== 'failed') return null
  const current = publicResearchEvents(events).filter((event) => event.task_id === run.id)
  if (current.some((event) => event.type === 'plan.created')) return null
  const failure = current.filter((event) => event.type === 'task.failed').at(-1)
  const stage = failure?.payload.failure_stage || failure?.stage || run.phase || run.stage
  const code = failure?.payload.error_code || run.error_code
  if (failure?.payload.failure_stage !== 'planning' && !(stage === 'planning' && ['research_plan_incomplete', 'invalid_model_output'].includes(code))) return null
  return { ...failure?.payload, failure_stage: stage, error_code: code }
}

export function researchSources(sources, events) {
  const records = new Map()
  const fingerprints = new Map()
  const seen = new Set()
  const failures = new Map()
  function add(source, snapshot) {
    const normalized = normalizeSources([source])[0]
    if (!normalized) return
    const fingerprint = normalized.source_fingerprint || normalized.fingerprint || normalized.id
    const key = fingerprints.get(fingerprint) || normalized.canonical_url || normalized.id
    const previous = records.get(key)
    fingerprints.set(fingerprint, key)
    if (snapshot && previous && (previous.event_seq ?? 0) > (normalized.event_seq ?? 0) && !(normalized.event_seq === undefined && ['found', 'discovered', 'validated', 'fetching'].includes(previous.status) && ['extracted', 'cited'].includes(normalized.status))) {
      for (const field of ['title', 'snippet', 'provider', 'query']) if (!previous[field] && normalized[field]) previous[field] = normalized[field]
      return
    }
    const discovery = ['found', 'discovered', 'validated'].includes(normalized.status)
    if (previous && discovery && !['found', 'discovered', 'validated'].includes(previous.status)) {
      for (const field of ['title', 'snippet', 'provider', 'query']) if (!previous[field] && normalized[field]) previous[field] = normalized[field]
      return
    }
    if (previous && normalized.attempt_id && previous.attempt_id === normalized.attempt_id && normalized.status === 'fetching' && ['failed', 'rejected', 'extracted', 'cited'].includes(previous.status)) return
    const merged = { ...previous, ...normalized, title: normalized.title || previous?.title || '', canonical_url: normalized.canonical_url || previous?.canonical_url || '', query: text(normalized.query) || previous?.query }
    if (previous?.fallback_attempted && previous.attempt_id === normalized.attempt_id) merged.fallback_attempted = true
    if (normalized.status === 'fetching' && normalized.attempt_id !== previous?.attempt_id) {
      for (const key of ['error_code', 'declared_bytes', 'effective_limit_bytes', 'fetched_bytes', 'mime_type', 'try_next_source', 'remaining_candidates', 'fallback_attempted', 'user_retry_task']) delete merged[key]
    }
    records.set(key, merged)
    return merged
  }
  for (const event of publicResearchEvents(events)) {
    if (seen.has(event.seq)) continue
    seen.add(event.seq)
    if (event.type === 'search.hit') {
      const hit = event.payload.hit
      if (!hit || typeof hit !== 'object' || typeof hit.id !== 'string') continue
      add({ ...hit, title: text(hit.title), provider: text(hit.provider), query: text(hit.query), error_code: typeof hit.error_code === 'string' ? sourceErrorCode(hit.error_code) : undefined, excerpt: text(hit.snippet), event_seq: event.seq }, false)
      continue
    }
    const p = event.payload
    const status = { 'tool.started': 'fetching', 'source.fetch.started': 'fetching', 'tool.failed': p.status === 'rejected' ? 'rejected' : 'failed', 'source.fetch.failed': p.status === 'rejected' ? 'rejected' : 'failed', 'source.failed': p.status === 'rejected' ? 'rejected' : 'failed', 'source.extracted': 'extracted', 'source.updated': p.status }[event.type]
    if (!status || (event.type.startsWith('tool.') && p.tool !== 'fetch_url') || !(p.target_url || p.url || p.source_fingerprint || p.fingerprint)) continue
    const scope = `${p.plan_attempt ?? ''}:${p.step_id ?? ''}`
    const failed = failures.get(scope)
    if (status === 'fetching' && failed && Number.isSafeInteger(p.plan_attempt) && p.step_id && p.attempt_id && failed.attempt_id && failed.attempt_id !== p.attempt_id && p.target_url !== failed.canonical_url) failed.fallback_attempted = true
    const record = add({ ...p, id: p.source_fingerprint || p.fingerprint || p.target_url || p.url, status, error_code: p.error_code || p.code || p.failure, timestamp: p.timestamp || event.created_at, event_seq: event.seq }, false)
    if (record && ['failed', 'rejected'].includes(status) && record.try_next_source) failures.set(scope, record)
  }
  for (const source of sources) add(source, true)
  return [...records.values()]
}

export function researchQueries(events) {
  const queries = new Set()
  const summaries = new Set()
  for (const event of publicResearchEvents(events)) {
    if (event.type === 'query.updated' || (event.type === 'tool.started' && event.payload.tool === 'search')) {
      const query = text(event.payload.query, 2000).trim()
      if (query) queries.add(query)
      else if (event.type === 'tool.started') {
        const summary = text(event.payload.summary).trim()
        if (summary) summaries.add(summary)
      }
    }
  }
  return { queries: [...queries], summaries: [...summaries] }
}

export function researchSteps(plan, events) {
  const steps = plan?.steps || plan?.subtasks || plan?.plan?.subtasks || []
  const publicEvents = publicResearchEvents(events)
  const boundary = publicEvents.filter((event) => event.type === 'plan.created').at(-1)?.seq ?? 0
  const states = new Map()
  for (const event of publicEvents) {
    if (event.seq < boundary || typeof event.payload.step_id !== 'string') continue
    const status = { 'step.started': 'running', 'step.completed': 'completed', 'step.skipped': 'skipped', 'tool.failed': 'failed' }[event.type]
    if (status) states.set(event.payload.step_id, status)
  }
  return (Array.isArray(steps) ? steps : []).filter((step) => step && typeof step.description === 'string').map((step) => ({ id: step.step_id || step.id, description: step.description, status: states.get(step.step_id || step.id) || step.status || 'pending' }))
}

export function researchError(code) {
	const messages = {
    task_fetch_budget_exceeded: 'Исчерпан суммарный бюджет загрузок исследования.',
    fetch_budget_exceeded: 'Достигнут прежний лимит загрузки. Тип лимита не уточнён.',
    source_response_too_large: 'Размер источника превышает допустимый лимит загрузки.',
    pdf_source_too_large: 'Размер PDF превышает допустимый лимит загрузки.',
    pdf_extraction_input_too_large: 'PDF превышает лимит входных данных извлечения текста.',
    pdf_page_limit_exceeded: 'PDF содержит больше страниц, чем разрешено политикой.',
    pdf_text_limit_exceeded: 'Извлечённый текст PDF превышает допустимый объём.',
    pdf_resource_exceeded: 'Извлечение PDF остановлено по лимиту ресурсов.',
    pdf_extractor_unavailable: 'Среда извлечения PDF недоступна при текущей политике изоляции.',
    pdf_malformed: 'Структура PDF повреждена или не поддерживается.',
    pdf_encrypted: 'Зашифрованный PDF не обработан.',
    pdf_no_text: 'PDF не содержит пригодного для извлечения текста.',
    pdf_timeout: 'Извлечение текста PDF не завершилось за отведённое время.',
    fetch_dns_failed: 'Не удалось определить сетевой адрес сайта (DNS). Этот источник не загружен.',
    invalid_tool_arguments: 'Параметры загрузки не прошли проверку.',
    tool_denied: 'Загрузка запрещена политикой доступа.',
    unknown_research_target: 'Адрес не принадлежит разрешённым источникам исследования.',
    approval_required: 'Для действия требуется подтверждение.',
    approval_expired: 'Срок подтверждения действия истёк.',
    fetch_outcome_unknown: 'Результат загрузки неизвестен. Автоматический повтор не подтверждён.',
    unsupported_content: 'Формат источника не поддерживается.',
    unsupported_mime: 'Формат источника не поддерживается.',
  }
  if (messages[code]) return messages[code]
  if (/timeout|deadline/.test(code || '')) return 'Источник не ответил вовремя. Проверьте доступные материалы ниже.'
  if (/ssrf|policy|unsafe|url_rejected/.test(code || '')) return 'Адрес отклонён проверкой безопасности и не использован как источник.'
  if (/challenge|captcha|blocked/.test(code || '')) return 'Сайт заблокировал загрузку. Его содержимое не подтверждено.'
  if (/empty|extraction/.test(code || '')) return 'Не удалось получить пригодный текст страницы.'
  if (/auth|credential/.test(code || '')) return 'Провайдер не принял настройки доступа.'
  if (/rate.limit/.test(code || '')) return 'Провайдер ограничил частоту запросов. Повторите исследование позже.'
  return 'Источник недоступен или отклонён. Ответ может опираться только на доступные материалы.'
}

export function formatSourceBytes(value) {
  if (!Number.isSafeInteger(value) || value < 0 || value > 2 ** 40) return 'Неизвестно'
  const decimal = (number) => number.toLocaleString('ru-RU', { maximumFractionDigits: 3 })
  return `${value.toLocaleString('ru-RU')} байт (${decimal(value / 1000000)} MB; ${decimal(value / 1048576)} MiB)`
}
