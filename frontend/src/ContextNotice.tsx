import type { ContextBudget } from './types'

export default function ContextNotice({ budget }: { budget?: ContextBudget }) {
  if (!budget) return null
  const mismatch = budget.context_status !== 'matched'
  return <div className={mismatch ? 'chat-partial-note' : 'chat-muted'} role={mismatch ? 'alert' : undefined}>
    {mismatch && <strong>Контекст модели не подтверждён — inference заблокирован.</strong>}
    <p>Context: настройки {budget.configured_context.toLocaleString('ru-RU')} · сервер {budget.server_reported_context?.toLocaleString('ru-RU') ?? 'неизвестно'}{budget.session_context != null && ` · session ${budget.session_context.toLocaleString('ru-RU')}`}</p>
    <p>Input budget: {budget.available_input_tokens?.toLocaleString('ru-RU') ?? 'неизвестно'} tokens · output reserve {budget.reserved_output_tokens.toLocaleString('ru-RU')} · system reserve {budget.reserved_system_tokens.toLocaleString('ru-RU')}</p>
    {mismatch && <p>Согласуйте LLM_CONTEXT_LENGTH с фактическим runtime llama-server. Сохранённые session settings автоматически не увеличиваются.</p>}
  </div>
}
