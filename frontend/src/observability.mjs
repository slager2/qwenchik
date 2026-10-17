export const healthLabels = {
  not_checked: 'Не проверен', ok: 'Есть результаты', healthy: 'Есть результаты',
  blocked: 'Запрос заблокирован', no_results: 'Нет результатов', filtered: 'Результаты отфильтрованы',
  unavailable: 'Недоступен', disabled: 'Отключён', error: 'Ошибка',
  degraded: 'Ограниченная работа', malformed: 'Некорректный ответ', misconfigured: 'Ошибка настройки',
}

function outcomeLabel(state, code) {
  if (code === 'search_no_results') return healthLabels.no_results
  if (code === 'search_filtered_by_policy') return healthLabels.filtered
  if (code === 'provider_not_checked') return healthLabels.not_checked
  if (code === 'provider_rate_limited') return 'Лимит запросов'
  return healthLabels[state] ?? state ?? healthLabels.not_checked
}

export function providerSummary(provider) {
  const outcome = provider.last_outcome
  return [provider.enabled ? 'Включён' : 'Отключён', provider.configured ? 'Настроен' : 'Не настроен',
    outcomeLabel(provider.health, outcome?.code),
    outcome ? `${outcome.latency_ms ?? 0} мс` : '',
    outcome?.code ? `Код: ${outcome.code}` : '',
    outcome?.http_status ? `HTTP ${outcome.http_status}` : '',
    outcome ? (outcome.retryable ? 'Можно повторить' : 'Автоповтор не рекомендован') : '',
  ].filter(Boolean).join(' · ')
}

export function diagnosticSummary(payload) {
  const attempts = payload.outcomes ?? payload.providers
  if (Array.isArray(attempts)) return attempts.map(diagnosticSummary).join('; ')
  return [payload.provider, payload.state ? outcomeLabel(payload.state, payload.code) : '',
    payload.code ?? payload.error_code,
    typeof payload.result_count === 'number' ? `Результатов: ${payload.result_count}` : '',
    typeof payload.distinct_domains === 'number' ? `Доменов: ${payload.distinct_domains}` : '',
    typeof payload.latency_ms === 'number' ? `${payload.latency_ms} мс` : '',
    payload.retryable === true ? 'Можно повторить' : payload.retryable === false ? 'Автоповтор не рекомендован' : '',
  ].filter(Boolean).join(' · ')
}

export function failureSummary(payload) {
  const missing = Array.isArray(payload.missing) ? payload.missing.filter((item) => typeof item === 'string') : []
  return [payload.message, payload.verification_summary,
    missing.length ? `Не хватает: ${missing.join('; ')}` : '', diagnosticSummary(payload),
  ].filter(Boolean).join(' ')
}

export function streamLabel(status, connection) {
  if (['completed', 'failed', 'cancelled'].includes(status)) return 'Поток завершён'
  return { idle: 'Поток не подключён', connecting: 'Подключение', live: 'SSE live', reconnecting: 'Переподключение' }[connection] ?? 'Поток не подключён'
}

export function evidenceSummary(count, status) {
  return count > 0 ? `Сохранено источников: ${count}` : status === 'failed' ? 'Задача завершилась без сохранённых источников.' : 'Сохранённых источников пока нет.'
}
