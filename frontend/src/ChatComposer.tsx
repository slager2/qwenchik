import type { FormEvent } from 'react'
import type { ConversationMode, ConversationSetup, Run } from './types'
import ResearchSettings from './ResearchSettings'
import PrivacyNotice from './PrivacyNotice'
import ContextNotice from './ContextNotice'
import type { ContextBudget } from './types'

export default function ChatComposer({
  mode, privacyMode, contextBudget, onModeChange, text, onTextChange, onSend, busy, active, run, onStop,
  researchContextRunID, researchContextReady, onEnableResearchContext, onClearResearchContext,
  settings, onSettingsChange, settingsError, settingsDirty, settingsSaving, onSaveSettings,
}: {
  mode: ConversationMode
  privacyMode?: string
  contextBudget?: ContextBudget
  onModeChange: (mode: ConversationMode) => void
  text: string
  onTextChange: (text: string) => void
  onSend: (event: FormEvent<HTMLFormElement>) => void
  busy: boolean
  active: boolean
  run: Run | null
  onStop: () => void
  researchContextRunID: string
  researchContextReady: boolean
  onEnableResearchContext: () => void
  onClearResearchContext: () => void
  settings?: ConversationSetup
  onSettingsChange?: (value: ConversationSetup) => void
  settingsError?: string
  settingsDirty?: boolean
  settingsSaving?: boolean
  onSaveSettings?: () => void
}) {
  const estimatedTokens = Math.ceil(text.length / 2.5)
  const availableTokens = contextBudget?.available_input_tokens ?? null
  const tokenWarning = availableTokens !== null && estimatedTokens > availableTokens
  const privacyLabel = privacyMode === 'offline' ? 'Офлайн' : privacyMode === 'local_searxng' ? 'Локальная модель · поиск' : privacyMode === 'local_inference_external_search' ? 'Локальная модель · внешний поиск' : 'Локальная сессия'
  return <>
    {contextBudget?.context_status !== 'matched' && <ContextNotice budget={contextBudget} />}
    {mode === 'research' && <PrivacyNotice mode={privacyMode} />}
    {run?.mode === 'research' && run.status === 'completed' && !researchContextRunID && <div><button className="chat-context-enable" type="button" disabled={busy || active || !researchContextReady} aria-describedby="research-context-reason" onClick={onEnableResearchContext}>Продолжить с контекстом исследования</button><p id="research-context-reason" className="chat-muted">{!researchContextReady ? 'Нужен сохранённый ответ с проверенными ссылками на источники.' : busy || active ? 'Контекст можно подключить после завершения текущего действия.' : 'Сохранённый ответ и его источники будут добавлены в контекст следующего сообщения.'}</p></div>}
    {researchContextRunID && <div className="chat-context-chip" role="status"><span>Контекст исследования · запуск <abbr title={researchContextRunID}>{researchContextRunID.slice(0, 8)}</abbr></span><button type="button" disabled={busy || active} aria-label="Убрать контекст исследования" onClick={onClearResearchContext}>Убрать контекст</button></div>}
    <form className="chat-composer" onSubmit={onSend}>
      <div className="chat-composer-heading">
        <label htmlFor="conversation-input">{mode === 'research' ? 'Задача исследования' : 'Сообщение'}</label>
        <span className="chat-privacy-chip" aria-label="Режим приватности определяется локальной конфигурацией"><span aria-hidden="true" className="chat-privacy-dot" />{privacyLabel}</span>
      </div>
      <textarea id="conversation-input" rows={3} value={text} maxLength={20000} aria-describedby="conversation-input-help" placeholder={mode === 'research' ? 'Что нужно исследовать?' : 'Напишите сообщение…'} onChange={(event) => onTextChange(event.target.value)} onKeyDown={(event) => { if (event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing && event.keyCode !== 229) { event.preventDefault(); event.currentTarget.form?.requestSubmit() } }} />
      <p id="conversation-input-help" className="chat-composer-hint">Enter — отправить · Shift+Enter — новая строка</p>
      <div className="chat-composer-controls">
        <label className="chat-mode-control">Режим разговора<select aria-label="Режим разговора" value={mode} disabled={busy || active} onChange={(event) => onModeChange(event.target.value as ConversationMode)}><option value="chat">Чат</option><option value="research">Исследование</option></select></label>
        {active ? <button className="chat-primary" type="button" disabled={busy || !run} onClick={onStop}>{busy ? 'Останавливаем…' : 'Остановить'}</button> : <button className="chat-primary" type="submit" disabled={busy || !text.trim()}>{busy ? 'Отправляем…' : 'Отправить'}</button>}
      </div>
      <div className={`chat-token-meter${tokenWarning ? ' is-warning' : ''}`} role="status">
        <span>{estimatedTokens.toLocaleString('ru-RU')} токенов в черновике</span>
        {availableTokens !== null && <span>доступно ~{availableTokens.toLocaleString('ru-RU')} из {(contextBudget?.configured_context || 32768).toLocaleString('ru-RU')}</span>}
        {contextBudget?.context_status === 'context_mismatch' && <span>модель заблокирована: контекст не совпал</span>}
      </div>
      {settings && <ResearchSettings mode={mode} value={settings} onChange={onSettingsChange} disabled={busy || active} error={settingsError} dirty={settingsDirty} saving={settingsSaving} onSave={onSaveSettings} />}
    </form>
  </>
}
