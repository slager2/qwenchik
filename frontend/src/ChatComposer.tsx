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
  return <>
    {contextBudget?.context_status !== 'matched' && <ContextNotice budget={contextBudget} />}
    {mode === 'research' && <PrivacyNotice mode={privacyMode} />}
    {run?.mode === 'research' && run.status === 'completed' && !researchContextRunID && <div><button className="chat-context-enable" type="button" disabled={busy || active || !researchContextReady} aria-describedby="research-context-reason" onClick={onEnableResearchContext}>Продолжить с контекстом исследования</button><p id="research-context-reason" className="chat-muted">{!researchContextReady ? 'Нужен сохранённый ответ с проверенными ссылками на источники.' : busy || active ? 'Контекст можно подключить после завершения текущего действия.' : 'Сохранённый ответ и его источники будут добавлены в контекст следующего сообщения.'}</p></div>}
    {researchContextRunID && <div className="chat-context-chip" role="status"><span>Контекст исследования · запуск <abbr title={researchContextRunID}>{researchContextRunID.slice(0, 8)}</abbr></span><button type="button" disabled={busy || active} aria-label="Убрать контекст исследования" onClick={onClearResearchContext}>Убрать контекст</button></div>}
    <form className="chat-composer" onSubmit={onSend}>
      <label htmlFor="conversation-input">{mode === 'research' ? 'Задача исследования' : 'Сообщение'}</label>
      <textarea id="conversation-input" rows={3} value={text} maxLength={20000} placeholder={mode === 'research' ? 'Что нужно исследовать?' : 'Напишите сообщение…'} onChange={(event) => onTextChange(event.target.value)} onKeyDown={(event) => { if (event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing && event.keyCode !== 229) { event.preventDefault(); event.currentTarget.form?.requestSubmit() } }} />
      <div className="chat-composer-controls">
        <label>Режим<select aria-label="Режим разговора" value={mode} disabled={busy || active} onChange={(event) => onModeChange(event.target.value as ConversationMode)}><option value="chat">Chat</option><option value="research">Deep Research</option></select></label>
        {active ? <button className="chat-primary" type="button" disabled={busy || !run} onClick={onStop}>{busy ? 'Останавливаем…' : 'Остановить'}</button> : <button className="chat-primary" type="submit" disabled={busy || !text.trim()}>{busy ? 'Отправляем…' : 'Отправить'}</button>}
      </div>
      {settings && <ResearchSettings mode={mode} value={settings} onChange={onSettingsChange} disabled={busy || active} error={settingsError} dirty={settingsDirty} saving={settingsSaving} onSave={onSaveSettings} />}
    </form>
  </>
}
