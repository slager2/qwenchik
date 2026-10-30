import { publicResearchEvents, researchSteps } from './research-state.mjs'

const reasons = {
  resource_guard: 'Защита ресурсов',
  high_model_rss: 'Превышен лимит памяти модели',
  high_rss: 'Превышен лимит памяти приложения',
  low_ram: 'Недостаточно свободной RAM',
  low_vram: 'Недостаточно свободной VRAM',
  gpu_temperature: 'Высокая температура GPU',
  llama_unhealthy: 'Локальная модель недоступна',
  model_oom: 'Модели не хватило памяти',
  model_timeout: 'Превышено время ожидания модели',
  llama_timeout: 'Превышено время ожидания модели',
  telemetry_unavailable: 'Данные о ресурсах недоступны',
  recovery_required: 'Нужно проверить неизвестный исход вызова',
  context_mismatch: 'Настройки контекста не совпадают с моделью',
  context_unavailable: 'Контекст модели не подтверждён',
  context_unverified: 'Контекст модели не подтверждён',
  context_capacity: 'Недостаточно бюджета контекста для запроса',
  model_resource: 'Модель остановлена ограничением ресурсов',
  context_budget_exceeded: 'Превышен доступный бюджет контекста',
  context_limit_exceeded: 'Превышен лимит контекста модели',
  context_length_exceeded: 'Превышена длина контекста модели',
  manual: 'Пауза пользователя',
}

export function resourceReasonLabel(reason) {
  if (!reason) return 'Причина не передана'
  if (Object.hasOwn(reasons, reason)) return reasons[reason]
  if (reason.startsWith('context')) return 'Ограничение контекста модели'
  return 'Причина требует уточнения'
}

export function resumeEligibility(run, runtime) {
  if (!run || run.status !== 'paused') return { allowed: false, explanation: 'Запуск не находится на паузе.' }
  const current = run.id && runtime?.task_id === run.id ? runtime : null
  const reason = current?.pause_reason || run.pause_reason
  const resource = current?.resource_reason || run.resource_reason
  if (reason === 'resource_guard') return { allowed: false, explanation: 'Возобновление разрешит backend после успешной проверки ресурсов. Можно остановить запуск.' }
  if (reason === 'recovery_required') return { allowed: false, explanation: 'Нужно проверить неизвестный исход вызова. Возобновление пока недоступно.' }
  if (reason === 'manual') return { allowed: true, explanation: resource
    ? 'Ресурсная пауза удерживается вручную. При возобновлении backend повторно проверит ограничения; продолжение не гарантировано.'
    : 'Пауза пользователя. Можно запросить возобновление; backend проверит условия запуска.' }
  return { allowed: false, explanation: 'Причина паузы не подтверждена. Обновите сведения запуска перед возобновлением.' }
}

const list = (value, limit = 5) => Array.isArray(value)
  ? value.filter((item) => typeof item === 'string' && item.trim()).slice(0, limit).map((item) => item.slice(0, 500)) : []
const terminal = (status) => ['completed', 'failed', 'cancelled'].includes(status)
const stages = { queued: 'В очереди', planning: 'Планирование', running: 'В работе', verifying: 'Проверка', search: 'Поиск источников', searching: 'Поиск источников', fetch: 'Чтение источников', fetching: 'Чтение источников', extract: 'Извлечение текста', synthesis: 'Подготовка ответа', synthesizing: 'Подготовка ответа', verification: 'Сверка источников', finalization: 'Завершение', waiting_approval: 'Нужно решение' }

// Presentation only: no timers, network requests, or inferred lifecycle transitions.
export function researchPresentation({ run, runtime, events = [], answer, evidence }) {
  const current = run?.id && runtime?.task_id === run.id ? runtime : null
  const currentEvents = publicResearchEvents(events).filter((event) => event.task_id === run?.id)
  const readiness = currentEvents.filter((event) => event.type === 'verification.completed' && event.payload.phase === 'evidence_readiness').at(-1)
  const report = current?.research_report
  const hasArtifact = !!(report?.artifact_id?.trim() || report?.markdown_artifact_id?.trim())
  const currentAnswer = answer?.run_id === run?.id && answer?.role === 'assistant' ? answer : null
  const hasText = !!currentAnswer?.content?.trim()
  const savedText = hasText && !['pending', 'streaming'].includes(currentAnswer.status)
  const outcome = current?.terminal_outcome || run?.terminal_outcome
  const fallback = currentEvents.filter((event) => event.type === 'verification.updated').at(-1)?.payload.status === 'fallback_excerpts'
  const partial = hasText && (fallback || outcome === 'partial' || outcome === 'budget_exhausted' || run?.status !== 'completed' || ['partial', 'interrupted', 'cancelled', 'stopped', 'failed'].includes(currentAnswer.status))
  const reportAvailable = hasArtifact && savedText
  const isTerminal = terminal(run?.status)
  const answerLabel = hasText
    ? !savedText ? run?.status === 'paused' || isTerminal ? 'Получен фрагмент ответа; сохранение текста не подтверждено' : 'Текст ответа поступает; сохранение не подтверждено' : partial ? 'Сохранён частичный ответ' : 'Текст ответа сохранён'
    : currentAnswer ? 'Текст ответа пока отсутствует' : 'Состояние текста ответа неизвестно'
  const reportLabel = reportAvailable ? partial || report?.outcome === 'partial' ? 'Сохранён частичный отчёт' : 'Отчёт сохранён'
    : hasArtifact ? 'Файл отчёта сохранён; текст ответа не подтверждён'
      : !current ? 'Наличие файла отчёта неизвестно'
        : isTerminal ? 'Файл отчёта не сохранён' : run?.status === 'paused' ? 'Файл отчёта пока не сохранён' : 'Файл отчёта ещё не сохранён; исследование продолжается'
  const observed = evidence?.data
  // Evidence endpoints may return a mixed snapshot while a run selection is
  // changing. Ignore foreign rows instead of exposing them or discarding the
  // current run's own evidence altogether.
  const owned = Array.isArray(observed) ? observed.filter((item) => item.task_id === run?.id) : undefined
  const evidenceCount = owned === undefined ? null : new Set(owned.map((item) => item.id)).size
  const evidenceLabel = evidenceCount === null ? evidence?.error ? 'Сохранённые материалы недоступны' : evidence?.loading ? 'Материалы загружаются' : 'Сохранённые материалы не проверены'
    : `Сохранённых материалов: ${evidenceCount}${evidence?.error || evidence?.loading ? ' · последние загруженные данные' : ''}`
  const round = current?.research_rounds?.at(-1)
  const criticalMissing = list(readiness?.payload.missing ?? round?.critical_missing)
  const reportGaps = hasArtifact ? list(report?.unrepresented_criteria) : []
  const reason = current?.resource_reason || run?.resource_reason || current?.pause_reason || run?.pause_reason
  const heading = run?.status === 'paused' ? 'Исследование на паузе'
    : outcome === 'blocked' ? 'Завершение заблокировано проверкой'
      : run?.status === 'failed' ? 'Исследование не завершено'
        : run?.status === 'cancelled' ? 'Исследование остановлено'
          : run?.status === 'completed' ? reportAvailable ? partial || report?.outcome === 'partial' ? 'Частичный результат сохранён' : 'Результат сохранён' : 'Запуск завершён' : 'Исследование выполняется'
  const state = run?.status === 'paused' ? resourceReasonLabel(reason) : isTerminal ? '' : stages[run?.phase || run?.stage || run?.status] || 'Этап уточняется'
  return { heading, state, answerLabel, reportLabel, evidenceLabel, evidenceCount, reportAvailable, hasArtifact, criticalMissing, reportGaps, fallback: !!fallback, partial: !!partial }
}

export function researchPlanPresentation(run, runtime, plan, events) {
  const current = run?.id && runtime?.task_id === run.id ? runtime : null
  const activePlan = current?.active_round_plan
  const selected = activePlan ? { subtasks: activePlan.subtasks } : plan
  // researchSteps applies only events at/after the latest plan.created boundary.
  const steps = researchSteps(selected, publicResearchEvents(events).filter((event) => event.task_id === run?.id))
  const completed = steps.filter((step) => step.status === 'completed').length
  const skipped = steps.filter((step) => step.status === 'skipped').length
  return { steps, completed, skipped, processed: completed + skipped, activePlan: !!activePlan }
}
