import { diagnosticSummary, failureSummary } from '../observability.mjs'
import type { AgentEvent, TaskStatus } from '../types'

export const columns: { id: string; title: string; statuses: TaskStatus[] }[] = [
  { id: 'inbox', title: 'Входящие', statuses: ['inbox'] },
  { id: 'queued', title: 'В очереди', statuses: ['queued'] },
  { id: 'running', title: 'В работе', statuses: ['planning', 'running', 'verifying'] },
  { id: 'approval', title: 'Нужно решение', statuses: ['waiting_approval'] },
  { id: 'paused', title: 'На паузе', statuses: ['paused'] },
  { id: 'done', title: 'Завершено', statuses: ['completed'] },
  { id: 'failed', title: 'Сбой / отмена', statuses: ['failed', 'cancelled'] },
]

export const statusLabels: Record<TaskStatus, string> = {
  inbox: 'Входящая',
  queued: 'В очереди',
  planning: 'Планирование',
  running: 'В работе',
  waiting_approval: 'Нужно решение',
  paused: 'На паузе',
  verifying: 'Проверка',
  completed: 'Завершена',
  failed: 'Сбой',
  cancelled: 'Отменена',
}

export const stageLabels: Record<string, string> = {
  intake: 'Приём задачи',
  planning: 'Планирование',
  search: 'Поиск',
  fetch: 'Загрузка',
  extract: 'Извлечение',
  browser: 'Браузер',
  code_execution: 'Изолированное выполнение',
  synthesis: 'Анализ',
  verification: 'Проверка',
  finalization: 'Финализация',
}

export const eventLabels: Record<string, string> = {
  'task.created': 'Задача создана',
  'task.status_changed': 'Изменён статус',
  'task.recovered': 'Работа восстановлена',
  'message.created': 'Новое сообщение',
  'plan.created': 'План сохранён',
  'step.started': 'Шаг начат',
  'step.completed': 'Шаг завершён',
  'step.skipped': 'Шаг пропущен по условию',
  'tool.started': 'Запущен инструмент',
  'tool.completed': 'Инструмент завершён',
  'tool.failed': 'Ошибка инструмента',
  'search.provider': 'Попытка поискового провайдера',
  'approval.requested': 'Требуется подтверждение',
  'approval.approved': 'Действие подтверждено',
  'approval.rejected': 'Действие отклонено',
  'approval.expired': 'Подтверждение истекло',
  'artifact.created': 'Артефакт сохранён',
  'evidence.created': 'Источник сохранён',
  'task.completed': 'Задача завершена',
  'task.failed': 'Задача завершилась с ошибкой',
  'task.cancelled': 'Задача отменена',
}

export function timeLabel(value: string) {
  return new Intl.DateTimeFormat('ru-RU', { hour: '2-digit', minute: '2-digit', day: '2-digit', month: 'short' }).format(new Date(value))
}

export function shortID(value: string) {
  return value.slice(0, 8)
}

export function eventSummary(event: AgentEvent) {
  const payload = event.payload
  if (event.type === 'search.provider') return diagnosticSummary(payload)
  if (event.type === 'task.failed' || event.type === 'tool.failed') return failureSummary(payload)
  if (typeof payload.summary === 'string') return payload.summary
  if (typeof payload.description === 'string') return payload.description
  if (typeof payload.message === 'string') {
    const missing = Array.isArray(payload.missing) ? payload.missing.filter((item): item is string => typeof item === 'string') : []
    const verification = typeof payload.verification_summary === 'string' ? payload.verification_summary : ''
    return [payload.message, verification, missing.length ? `Не хватает: ${missing.join('; ')}` : ''].filter(Boolean).join(' ')
  }
  if (typeof payload.verification_summary === 'string') return payload.verification_summary
  if (typeof payload.error_code === 'string') return `Код: ${payload.error_code}`
  if (typeof payload.status === 'string') return `Статус: ${payload.status}`
  if (typeof payload.tool === 'string') return `Инструмент: ${payload.tool}`
  if (typeof payload.evidence_id === 'string') return `Источник: ${payload.evidence_id}`
  if (typeof payload.artifact_id === 'string') return `Файл: ${typeof payload.name === 'string' ? payload.name : payload.artifact_id}`
  if (typeof payload.steps === 'number') return `Шагов в плане: ${payload.steps}`
  return ''
}
