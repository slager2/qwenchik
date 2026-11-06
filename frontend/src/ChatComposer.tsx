import type { FormEvent } from 'react'
import type { ConversationMode, ConversationSetup, Run } from './types'
import ResearchSettings from './ResearchSettings'
import ContextNotice from './ContextNotice'
import type { ContextBudget } from './types'
import { Icon } from './icons'
import { Button, IconButton, Textarea } from './ui'

const privacyText = (mode?: string) => mode === 'offline' ? 'Офлайн: только локальный корпус, сеть не используется.'
  : mode === 'local_searxng' ? 'Локальная модель · локальный SearXNG; открываемые сайты получают запросы.'
    : mode === 'local_inference_external_search' ? 'Локальная модель · поисковые запросы и сайты выходят в сеть.' : 'Локальная модель.'

export default function ChatComposer({
  mode, privacyMode, contextBudget, onModeChange, text, onTextChange, onSend, busy, active, run, onStop,
  researchContextRunID, researchContextReady, onEnableResearchContext, onClearResearchContext,
  settings, onSettingsChange, settingsError, settingsDirty, settingsSaving, onSaveSettings, clarify, onClarifyChange,
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
  /** Clarify research questions before the run starts. */
  clarify?: boolean
  onClarifyChange?: (value: boolean) => void
}) {
  const estimatedTokens = Math.ceil(text.length / 2.5)
  const availableTokens = contextBudget?.available_input_tokens ?? null
  const tokenWarning = availableTokens !== null && estimatedTokens > availableTokens
  const research = mode === 'research'
  return <div className="chat-composer-area">
    {contextBudget?.context_status !== 'matched' && <ContextNotice budget={contextBudget} />}
    {run?.mode === 'research' && run.status === 'completed' && !researchContextRunID && <div className="chat-context-offer">
      <Button size="sm" variant="ghost" className="chat-context-enable" title={!researchContextReady ? 'Нужен сохранённый ответ с проверенными ссылками на источники' : undefined} disabled={busy || active || !researchContextReady} aria-describedby="research-context-reason" onClick={onEnableResearchContext} icon={<Icon name="chat" />}>Продолжить с контекстом исследования</Button>
      <p id="research-context-reason" className="sr-only">{!researchContextReady ? 'Нужен сохранённый ответ с проверенными ссылками на источники.' : busy || active ? 'Контекст можно подключить после завершения текущего действия.' : 'Сохранённый ответ и его источники будут добавлены в контекст следующего сообщения.'}</p>
    </div>}
    <form className="chat-composer" data-mode={mode} onSubmit={onSend}>
      {researchContextRunID && <div className="chat-context-chip" role="status"><Icon name="sources" size={16} /><span>Контекст исследования · запуск <abbr title={researchContextRunID}>{researchContextRunID.slice(0, 8)}</abbr></span><IconButton label="Убрать контекст исследования" icon={<Icon name="close" />} disabled={busy || active} onClick={onClearResearchContext} /></div>}
      <label htmlFor="conversation-input" className={research ? undefined : 'sr-only'}>{research ? 'Задача исследования' : 'Сообщение'}</label>
      <Textarea id="conversation-input" autoGrow maxRows={10} rows={1} value={text} maxLength={20000} aria-describedby="conversation-input-help" placeholder={research ? 'Что нужно исследовать?' : 'Спросите что-нибудь'} onChange={(event) => onTextChange(event.target.value)} onKeyDown={(event) => { if (event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing && event.keyCode !== 229) { event.preventDefault(); event.currentTarget.form?.requestSubmit() } }} />
      <p id="conversation-input-help" className="sr-only">Enter — отправить · Shift+Enter — новая строка</p>
      <div className="chat-composer-controls">
        <label className="chat-mode-control" data-mode={mode}>
          <Icon name={research ? 'sources' : 'chat'} size={16} />
          <span className="sr-only">Режим разговора</span>
          <select aria-label="Режим разговора" value={mode} disabled={busy || active} onChange={(event) => onModeChange(event.target.value as ConversationMode)}><option value="chat">Чат</option><option value="research">Исследование</option></select>
          <Icon name="chevron-down" size={14} className="chat-mode-control__chevron" />
        </label>
        {research && onClarifyChange && <button type="button" className="chat-clarify-toggle" aria-pressed={!!clarify} aria-label="Уточнять вопрос перед исследованием" title={clarify ? 'Перед запуском модель предложит варианты трактовки и уточняющие вопросы' : 'Исследование начнётся сразу, без уточнения'} disabled={busy || active} onClick={() => onClarifyChange(!clarify)}><Icon name={clarify ? 'check' : 'sliders'} size={16} /><span className="chat-clarify-toggle__label">Уточнять</span></button>}
        <span className={`chat-token-meter${tokenWarning ? ' is-warning' : ''}`} role="status">
          {text.trim() ? <>{estimatedTokens.toLocaleString('ru-RU')} ток.{availableTokens !== null && <> из ~{availableTokens.toLocaleString('ru-RU')}</>}</> : ''}
          {contextBudget?.context_status === 'context_mismatch' && <span> · модель заблокирована: контекст не совпал</span>}
        </span>
        {active
          ? <IconButton variant="primary" className="chat-send" label="Остановить" title={busy ? 'Останавливаем…' : 'Остановить генерацию'} icon={<Icon name="stop" />} disabled={busy || !run} onClick={onStop} />
          : <IconButton variant="primary" className="chat-send" type="submit" label="Отправить" title={busy ? 'Отправляем…' : 'Отправить (Enter)'} icon={<Icon name="send" />} disabled={busy || !text.trim()} />}
      </div>
    </form>
    <div className="chat-composer-footer">
      {settings && <ResearchSettings mode={mode} value={settings} onChange={onSettingsChange} disabled={busy || active} error={settingsError} dirty={settingsDirty} saving={settingsSaving} onSave={onSaveSettings} />}
      <p className="chat-privacy" role="note">{privacyText(privacyMode)}</p>
    </div>
  </div>
}
