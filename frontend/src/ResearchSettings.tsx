import { useId, useState } from 'react'
import type { ConversationMode, ConversationSetup } from './types'
import { defaultSetup, maxOutputTokens, profileMinutes, setupProblem } from './research-settings.mjs'

export default function ResearchSettings({ mode, value, onChange, disabled, error = '', dirty, saving, onSave }: {
  mode: ConversationMode; value: ConversationSetup; onChange?: (value: ConversationSetup) => void
  disabled?: boolean; error?: string; dirty?: boolean; saving?: boolean; onSave?: () => void
}) {
  const id = useId()
  const [touched, setTouched] = useState<Partial<Record<keyof ConversationSetup, boolean>>>({})
  const problem = setupProblem(value)
  const locked = disabled || saving || !onChange
  const numeric = (key: 'timeBudgetMinutes' | 'maxOutputTokens' | 'temperature' | 'topP', label: string, min: number, max: number, step: number | 'any') => {
    const fieldProblem = setupProblem({ ...defaultSetup, ...(key === 'timeBudgetMinutes' ? { profile: value.profile } : {}), [key]: value[key] })
    const invalid = !!touched[key] && !!fieldProblem
    return <label key={key} htmlFor={`${id}-${key}`}>{label}<input id={`${id}-${key}`} type="number" min={min} max={max} step={step} value={Number.isFinite(value[key]) ? value[key] : ''} disabled={locked} aria-invalid={invalid || undefined} aria-describedby={`${id}-caption ${id}-${key}-hint ${id}-status`} onBlur={() => setTouched((old) => ({ ...old, [key]: true }))} onChange={(event) => onChange?.({ ...value, [key]: event.target.value === '' ? NaN : Number(event.target.value) })} /><small id={`${id}-${key}-hint`}>{invalid ? fieldProblem : min === Number.MIN_VALUE ? 'Больше 0, не больше 1' : `${min}–${max}`}</small></label>
  }
  return <details className="chat-settings"><summary>Настройки следующего запуска</summary>
    <p id={`${id}-caption`}>Только для следующего запуска. Лимиты не обещают длительность или завершение исследования. Если сохранённых значений нет, показаны настройки клиента, а не измерения сервера.</p>
    {mode === 'research' && <div className="chat-settings-grid"><label htmlFor={`${id}-profile`}>Профиль исследования<select id={`${id}-profile`} value={value.profile} disabled={locked} aria-describedby={`${id}-caption`} onChange={(event) => { const profile = event.target.value as ConversationSetup['profile']; onChange?.({ ...value, profile, timeBudgetMinutes: Math.min(value.timeBudgetMinutes, profileMinutes[profile]) }) }}>
      <option value="quick">Быстрый · до 5 мин</option><option value="standard">Стандартный · до 30 мин</option><option value="deep_laptop">Глубокий · до 120 мин</option><option value="long_laptop">Долгий · до 240 мин</option><option value="overnight_laptop">Ночной · до 360 мин</option>
    </select></label>{numeric('timeBudgetMinutes', 'Лимит времени, мин', 1, profileMinutes[value.profile], 'any')}</div>}
    <details><summary>Параметры модели</summary><div className="chat-settings-grid">{numeric('maxOutputTokens', 'Лимит выходных токенов', 1, maxOutputTokens, 1)}{numeric('temperature', 'Температура', 0, 2, 'any')}{numeric('topP', 'Top-p', Number.MIN_VALUE, 1, 'any')}</div></details>
    <p id={`${id}-status`} className="chat-settings-status" role="status">{error || (saving ? 'Сохраняем настройки…' : dirty ? 'Есть несохранённые изменения.' : 'Настройки для следующего запуска.')}</p>
    {onSave && <button type="button" disabled={locked || !!problem || !dirty} aria-describedby={`${id}-status`} onClick={onSave}>{saving ? 'Сохраняем…' : 'Сохранить настройки'}</button>}
  </details>
}
