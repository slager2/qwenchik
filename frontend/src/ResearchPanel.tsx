import { ProviderCard } from './diagnostic-ui.mjs'
import { safeURL } from './conversation-state.mjs'
import { diagnosticSummary } from './observability.mjs'
import { formatSourceBytes, publicResearchEvents, researchError, researchQueries, researchSources, researchSteps } from './research-state.mjs'
import type { AgentEvent, Evidence, ProvidersResponse, ResearchPlan, Run, SourceRecord } from './types'

interface Remote<T> { data?: T; error: string; loading: boolean; reload: () => Promise<void> }
interface Resources {
  plan: Remote<ResearchPlan | null>
  sources: Remote<SourceRecord[]>
  evidence: Remote<Evidence[]>
  providers: Remote<ProvidersResponse | null>
}
const labels: Record<string, string> = { pending: 'Ожидает выполнения', running: 'Выполняется', planning: 'Планирование', queued: 'В очереди', paused: 'На паузе', verifying: 'Сверка утверждений', waiting_approval: 'Нужно решение', completed: 'Завершено', cancelled: 'Остановлено', failed: 'Недоступен', skipped: 'Пропущен', search: 'Поиск', fetch: 'Чтение страниц', extract: 'Извлечение текста', synthesis: 'Подготовка ответа', verification: 'Проверка', finalization: 'Завершение', found: 'Найден', discovered: 'Найден', validated: 'Адрес проверен', rejected: 'Отклонён', fetching: 'Загрузка', extracted: 'Текст извлечён', cited: 'Использован в ответе' }
const stamp = (value: string) => new Date(value).toLocaleString('ru-RU', { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' })

function Status({ resource, empty }: { resource: Remote<unknown>; empty?: boolean }) {
  if (resource.error) return <div className="chat-resource-error" role="status"><p>{resource.error}{resource.data !== undefined ? ' Показаны последние загруженные данные.' : ''}</p><button type="button" onClick={() => void resource.reload()}>Повторить загрузку</button></div>
  if (resource.loading && resource.data === undefined) return <p className="chat-muted" role="status">Загрузка…</p>
  return empty ? <p className="chat-muted">Пока нет данных.</p> : null
}

export default function ResearchPanel({ resources, events, run, showHeading = true }: { resources: Resources; events: AgentEvent[]; run: Run | null; showHeading?: boolean }) {
  const { plan, sources, evidence, providers } = resources
  const steps = researchSteps(plan.data, events)
  const sourceList = researchSources(sources.data ?? [], events)
  const queries = researchQueries(events)
  const activity = publicResearchEvents(events).filter((event) => ['search.provider', 'tool.started', 'tool.failed', 'plan.created', 'evidence.created', 'task.status_changed'].includes(event.type))
  const terminal = run && ['completed', 'cancelled', 'failed'].includes(run.status)
  const state = run ? terminal ? run.status : run.phase || run.stage || run.status : ''
  const warning = publicResearchEvents(events).filter((event) => event.type === 'research.warning').at(-1)
  const partial = warning?.payload.partial === true || sourceList.some((source) => ['rejected', 'failed'].includes(source.status || '') || (terminal && source.status === 'fetching'))
  const verification = publicResearchEvents(events).filter((event) => event.type === 'verification.updated').at(-1)
  const fallback = verification?.payload.status === 'fallback_excerpts'
  return <div className="chat-research">
    <header>{showHeading && <h2>Детали исследования</h2>}<p>{labels[state] || 'Исследование ещё не запущено'}</p></header>
    <nav className="chat-research-shortcuts" aria-label="Разделы исследования">
      <button type="button" onClick={(event) => event.currentTarget.closest('.chat-research')?.querySelector('[data-research-section="sources"]')?.scrollIntoView({ block: 'start', inline: 'nearest' })}>Источники · {sourceList.length}</button>
      <button type="button" onClick={(event) => event.currentTarget.closest('.chat-research')?.querySelector('[data-research-section="evidence"]')?.scrollIntoView({ block: 'start', inline: 'nearest' })}>Evidence · {evidence.data?.length ?? 0}</button>
    </nav>
    {fallback && <p className="chat-partial-note" role="status">Показаны дословные выдержки. Синтезированный вывод надёжно подтвердить не удалось.</p>}
    {partial && <p className="chat-partial-note" role="status">Часть источников недоступна или отклонена. Это может ограничить полноту ответа.</p>}
    <section><h3>План</h3><Status resource={plan} empty={!steps.length} />
      {steps.length > 0 && <ol className="chat-plan-list">{steps.map((step) => {
        const lead = step.description.split(/[.!?](?:\s|$)|:\s/)[0]
        const concise = lead.length > 120 ? `${lead.slice(0, 117).trimEnd()}…` : lead
        return <li key={step.id}><details><summary><span>{concise}</span></summary><p>{step.description}</p></details><small>{labels[step.status] || step.status}</small></li>
      })}</ol>}
    </section>
    <section><h3>Поисковые запросы</h3>
      {queries.queries.length > 0 ? <ul>{queries.queries.map((query) => <li key={query}>{query}</li>)}</ul> : <p className="chat-muted">Точные запросы пока не переданы backend.</p>}
      {queries.summaries.length > 0 && <><h4>Краткие действия поиска</h4><ul>{queries.summaries.map((summary) => <li key={summary}>{summary}</li>)}</ul></>}
    </section>
    <section data-research-section="sources"><h3>Источники <span>{sourceList.length}</span></h3><Status resource={sources} empty={!sourceList.length} />
      <ul className="chat-source-cards">{sourceList.map((source) => {
        const url = safeURL(source.canonical_url)
        const unavailable = ['rejected', 'failed'].includes(source.status || '') || !!source.error_code
        const unfinished = terminal && source.status === 'fetching'
        return <li key={source.id}>
          {url ? <a href={url} target="_blank" rel="noopener noreferrer">{source.title || new URL(url).hostname}</a> : <strong>{source.title || 'Источник без доступной ссылки'}</strong>}
          <p>{unfinished ? 'Загрузка не подтверждена' : labels[source.status || ''] || source.status || 'Найден'}{source.provider ? ` · ${source.provider}` : ''}</p>
          {url && <small>{new URL(url).hostname}</small>}
          {source.query && <p>Запрос: {source.query}</p>}
          {source.snippet && <p>{source.snippet}</p>}
          {unavailable && <div className="chat-source-error" role="status"><p>{researchError(source.error_code)}</p>{source.error_code && <small>Код: {source.error_code}</small>}</div>}
          {(source.declared_bytes !== undefined || source.effective_limit_bytes !== undefined || source.fetched_bytes !== undefined) && <dl>
            {source.declared_bytes !== undefined && <><dt>Заявленный размер</dt><dd>{formatSourceBytes(source.declared_bytes)}</dd></>}
            {source.effective_limit_bytes !== undefined && <><dt>Применённый лимит</dt><dd>{formatSourceBytes(source.effective_limit_bytes)}</dd></>}
            {source.fetched_bytes !== undefined && <><dt>Прочитано из тела ответа</dt><dd>{formatSourceBytes(source.fetched_bytes)}</dd><dt>Загрузка тела</dt><dd>{source.fetched_bytes === 0 ? 'Тело не скачивалось' : 'Прочитаны байты тела; полнота загрузки этим не подтверждается'}</dd></>}
          </dl>}
          {source.mime_type && <p>Формат: {source.mime_type}</p>}
          {source.fallback_attempted === true ? <p>Попытка перехода к другому источнику подтверждена.</p> : source.try_next_source === true ? <p>Разрешён следующий источник. Переход пока не подтверждён.</p> : source.try_next_source === false ? <p>Автоматический переход к другому источнику не предусмотрен.</p> : null}
          {source.remaining_candidates !== undefined && <p>Осталось кандидатов: {source.remaining_candidates}</p>}
          {unfinished && <p className="chat-muted">Запуск завершён без подтверждения загрузки этого источника.</p>}
          {(source.timestamp || source.retrieved_at) && <time dateTime={source.timestamp || source.retrieved_at}>{stamp((source.timestamp || source.retrieved_at)!)}</time>}
        </li>
      })}</ul>
    </section>
    <section data-research-section="evidence"><h3>Evidence <span>{evidence.data?.length ?? 0}</span></h3><Status resource={evidence} empty={!evidence.data?.length} />
      <ul className="chat-source-cards">{(evidence.data ?? []).map((item) => <li key={item.id}><strong>{item.title || 'Сохранённый источник'}</strong><p>{item.excerpt}</p>{safeURL(item.canonical_url) && <a href={safeURL(item.canonical_url)} target="_blank" rel="noopener noreferrer">Открыть источник</a>}<small>{item.provider} · {item.extraction_method}</small></li>)}</ul>
      <p className="chat-muted">Ссылки подтверждают наличие источника, а не проверку каждого утверждения.</p>
    </section>
    <section><h3>Поисковые провайдеры</h3><Status resource={providers} empty={!providers.data} />{Object.entries(providers.data?.search.providers ?? {}).map(([name, provider]) => <ProviderCard key={name} name={name} provider={provider} checkedLabel={provider.last_checked ? stamp(provider.last_checked) : ''} />)}</section>
    <section><h3>Действия</h3>{!activity.length && <p className="chat-muted">События появятся во время работы.</p>}
      <ol>{activity.slice(-30).reverse().map((event) => <li key={event.seq}>
        <strong>{event.type === 'search.provider' ? 'Результат провайдера' : event.type === 'tool.failed' ? 'Не удалось выполнить действие' : event.type === 'tool.started' ? 'Выполняется действие' : event.type === 'plan.created' ? 'План сохранён' : event.type === 'evidence.created' ? 'Источник сохранён' : labels[String(event.payload.to || event.stage || '')] || 'Состояние обновлено'}</strong>
        {event.type === 'search.provider' && <p>{diagnosticSummary(event.payload)}</p>}
        {event.type === 'tool.failed' && <p>{researchError(typeof event.payload.error_code === 'string' ? event.payload.error_code : typeof event.payload.code === 'string' ? event.payload.code : '')}</p>}
        <time>{stamp(event.created_at)}</time>
      </li>)}</ol>
    </section>
  </div>
}
