import { ProviderCard } from './diagnostic-ui.mjs'
import TelemetryPanel from './TelemetryPanel'
import PrivacyNotice from './PrivacyNotice'
import ContextNotice from './ContextNotice'
import Expandable from './Expandable'
import FailureDetails, { safeCount } from './FailureDetails'
import type { ReloadReason } from './network-debug'
import { safeURL } from './conversation-state.mjs'
import { diagnosticSummary } from './observability.mjs'
import { researchPlanPresentation, resourceReasonLabel, resumeEligibility } from './run-presentation.mjs'
import { formatSourceBytes, planningFailure, publicResearchEvents, researchError, researchQueries, researchSources } from './research-state.mjs'
import ResearchProgress from './ResearchProgress'
import ReportPDFButton from './ReportPDFButton'
import ReportObsidianButton from './ReportObsidianButton'
import type { AgentEvent, DoctorResponse, Evidence, ProvidersResponse, ResearchPlan, Run, RunRuntime, SourceRecord, VerificationCriterionRecord } from './types'

interface Remote<T> { data?: T; error: string; loading: boolean; reload: (reason?: ReloadReason) => Promise<void> }
interface Resources {
	 pdfCapability?: DoctorResponse['pdf_extractor']
  plan: Remote<ResearchPlan | null>
  sources: Remote<SourceRecord[]>
  evidence: Remote<Evidence[]>
  providers: Remote<ProvidersResponse | null>
  runtime?: Remote<RunRuntime | null>
}
const labels: Record<string, string> = { pending: 'Ожидает выполнения', running: 'Выполняется', planning: 'Планирование', queued: 'В очереди', paused: 'На паузе', verifying: 'Сверка утверждений', waiting_approval: 'Нужно решение', completed: 'Завершено', cancelled: 'Остановлено', failed: 'Недоступен', skipped: 'Пропущен', search: 'Поиск', fetch: 'Чтение страниц', extract: 'Извлечение текста', synthesis: 'Подготовка ответа', verification: 'Проверка', finalization: 'Завершение', found: 'Найден', discovered: 'Найден', validated: 'Адрес проверен', rejected: 'Отклонён', fetching: 'Загрузка', extracted: 'Текст извлечён', cited: 'Использован в ответе' }
const stamp = (value: string) => new Date(value).toLocaleString('ru-RU', { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' })
const readinessLabels: Record<string, string> = {
  PASS: 'Критические критерии выполнены',
  PARTIAL: 'Критические критерии выполнены; дополнительные не подтверждены',
  RESEARCH: 'Не хватает критических подтверждений',
  UNCERTAIN: 'Есть неразрешённые противоречия',
  BLOCK: 'Проверка заблокировала завершение',
}

function stringList(value: unknown, limit = 20): string[] {
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === 'string').slice(0, limit).map((item) => item.slice(0, 500)) : []
}

function verificationCriteria(value: unknown): VerificationCriterionRecord[] {
  if (!Array.isArray(value)) return []
  return value.flatMap((item) => {
    if (!item || typeof item !== 'object') return []
    const candidate = item as Partial<VerificationCriterionRecord>
    if (typeof candidate.step_id !== 'string' || !Number.isInteger(candidate.criterion_index) || (candidate.criterion_index ?? -1) < 0 || (candidate.criterion_index ?? 10) >= 10 || !['critical', 'supplementary'].includes(String(candidate.classification)) || typeof candidate.met !== 'boolean') return []
    return [{
      step_id: candidate.step_id.slice(0, 128), criterion_index: candidate.criterion_index!,
      classification: candidate.classification as VerificationCriterionRecord['classification'], met: candidate.met,
      evidence_ids: Array.isArray(candidate.evidence_ids) ? candidate.evidence_ids.filter((id): id is string => typeof id === 'string').slice(0, 16).map((id) => id.slice(0, 128)) : [],
    }]
  }).slice(0, 120)
}

function Status({ resource, empty }: { resource: Remote<unknown>; empty?: boolean }) {
  if (resource.error) return <div className="chat-resource-error" role="status"><p>{resource.error}{resource.data !== undefined ? ' Показаны последние загруженные данные.' : ''}</p><button type="button" onClick={() => void resource.reload()}>Повторить загрузку</button></div>
  if (resource.loading && resource.data === undefined) return <p className="chat-muted" role="status">Загрузка…</p>
  return empty ? <p className="chat-muted">Пока нет данных.</p> : null
}

export default function ResearchInspector({ resources, events, run, showHeading = true, busy, onPause, onResume, onRetry, onDiagnostics, diagnosticsLoading, diagnosticsError, reportPDF }: { reportPDF?: { disabled: boolean; reason: string }; resources: Resources; events: AgentEvent[]; run: Run | null; showHeading?: boolean; busy?: boolean; onPause?: () => void; onResume?: () => void; onRetry?: () => void; onDiagnostics?: () => void; diagnosticsLoading?: boolean; diagnosticsError?: string }) {
  const { plan, sources, evidence, providers } = resources
  const currentRuntime = run && resources.runtime?.data?.task_id === run.id ? resources.runtime?.data ?? null : null
  // The events prop is run-scoped by its owner. Retain legacy unscoped public
  // entries, but never project entries explicitly owned by another run.
  const currentEvents = publicResearchEvents(events).filter((event) => !event.task_id || event.task_id === run?.id)
  const selectedPlan = currentRuntime?.active_round_plan ? { subtasks: currentRuntime.active_round_plan.subtasks } : plan.data
  const failedPlanning = planningFailure(run, currentEvents, selectedPlan)
  const { steps, completed, skipped, processed, activePlan } = researchPlanPresentation(run, currentRuntime, plan.data, currentEvents)
  const sourceList = researchSources(sources.data ?? [], currentEvents)
  const ownedEvidence = evidence.data?.filter((item) => item.task_id === run?.id)
  const sourcesKnown = sources.data !== undefined || sourceList.length > 0
  const queries = researchQueries(currentEvents)
  const activity = currentEvents.filter((event) => ['search.provider', 'tool.started', 'tool.failed', 'plan.created', 'evidence.created', 'task.status_changed', 'context.compacted', 'continuation.requested', 'verification.completed', 'run.paused', 'run.resumed', 'run.blocked'].includes(event.type))
  const terminal = run && ['completed', 'cancelled', 'failed'].includes(run.status)
  const state = run ? run.status === 'paused' ? 'paused' : terminal ? run.status : run.phase || run.stage || run.status : ''
  const warning = currentEvents.filter((event) => event.type === 'research.warning').at(-1)
  const partial = warning?.payload.partial === true || sourceList.some((source) => ['rejected', 'failed'].includes(source.status || '') || (terminal && source.status === 'fetching'))
  const verification = currentEvents.filter((event) => event.type === 'verification.updated').at(-1)
  const fallback = verification?.payload.status === 'fallback_excerpts'
  const readiness = currentEvents.filter((event) => event.type === 'verification.completed' && event.payload.phase === 'evidence_readiness').at(-1)
  const readinessVerdict = typeof readiness?.payload.verdict === 'string' ? readiness.payload.verdict : ''
  const criticalMissing = stringList(readiness?.payload.missing)
  const supplementaryMissing = stringList(readiness?.payload.supplementary_missing)
  const criteria = verificationCriteria(readiness?.payload.criteria)
  const plannedSteps = currentRuntime?.active_round_plan?.subtasks ?? plan.data?.plan?.subtasks ?? plan.data?.subtasks ?? []
  const resume = resumeEligibility(run, currentRuntime)
  const outcome = currentRuntime?.terminal_outcome || run?.terminal_outcome
  const outcomes: Record<string, string> = { partial: 'Частичный результат', blocked: 'Заблокировано проверкой', budget_exhausted: 'Бюджет исчерпан', cancelled: 'Остановлено', failed: 'Ошибка запуска' }
  const report = currentRuntime?.research_report
  const reportSaved = !!(report?.artifact_id?.trim() || report?.markdown_artifact_id?.trim())
  const focusSection = (button: HTMLButtonElement, section: string) => {
    const heading = button.closest('.chat-research')?.querySelector<HTMLElement>(`[data-research-section="${section}"] h3`)
    heading?.focus({ preventScroll: true })
    heading?.scrollIntoView({ block: 'start', inline: 'nearest' })
  }
  return <div className="chat-research q-research-inspector">
    <header className="research-heading q-research-inspector__heading">{showHeading && <><h2>{run?.mode === 'chat' ? 'Детали запуска' : 'Детали исследования'}</h2></>}<p>{labels[state] || 'Исследование ещё не запущено'}</p></header>
    <ResearchProgress status={run?.status} phase={run?.phase || run?.stage} />
    <div className="research-progress q-research-inspector__summary" aria-label="Состояние исследования">
      {outcome && outcomes[outcome] && <p className="research-outcome" role="status">{outcomes[outcome]}</p>}
      {run?.status === 'paused' && <><p>{resourceReasonLabel(currentRuntime?.resource_reason || run.resource_reason || currentRuntime?.pause_reason || run.pause_reason)}</p><p className="chat-muted" role="status">{resume.explanation}</p></>}
      <div className="research-run-actions">
        {run && ['planning', 'running', 'verifying'].includes(run.status) && onPause && <button type="button" disabled={busy} onClick={onPause}>Пауза</button>}
        {run?.status === 'paused' && onResume && <button type="button" disabled={busy || !resume.allowed} onClick={onResume}>Возобновить</button>}
      </div>
    </div>
    {run?.mode === 'research' && <section aria-label="Аналитический отчёт"><h3>Аналитический отчёт</h3>{reportSaved && report && currentRuntime ? <>
      <p>Файл отчёта сохранён. Текст ответа доступен в разговоре, если он был сохранён.</p>
      <p>Секций: {safeCount(report.sections) ?? 'неизвестно'} · Итог файла: {outcomes[report.outcome] || (report.outcome === 'completed' ? 'Завершено' : 'Не уточнён')}</p>
      {report.artifact_id && <a href={`/api/tasks/${encodeURIComponent(currentRuntime.task_id)}/artifacts/${encodeURIComponent(report.artifact_id)}`}>Скачать отчёт с metadata (JSON)</a>}
      {report.markdown_artifact_id && <p><a href={`/api/tasks/${encodeURIComponent(currentRuntime.task_id)}/artifacts/${encodeURIComponent(report.markdown_artifact_id)}`}>Скачать Markdown</a></p>}
      {reportPDF && run && terminal && run.status !== 'failed' && <p><ReportPDFButton runID={run.id} disabled={reportPDF.disabled} reason={reportPDF.reason} /> <ReportObsidianButton runID={run.id} /></p>}
      {stringList(report.unrepresented_criteria).length > 0 && <><h4>Не отражено в сохранённом отчёте</h4><ul>{stringList(report.unrepresented_criteria).map((item, index) => <li key={index}>{item}</li>)}</ul></>}
    </> : <p>{!currentRuntime ? 'Наличие файла отчёта неизвестно: сведения запуска недоступны.' : terminal ? 'Файл отчёта не сохранён.' : 'Файл отчёта пока не сохранён. Этапы и внутренние проверки не подтверждают доставку ответа.'}</p>}</section>}
    {failedPlanning && <section className="chat-action-error" aria-label="Ошибка планирования" role="status">
      <h3>Планирование не завершено</h3>
      <p>План не был сохранён: ошибка произошла до начала поиска</p>
      <FailureDetails payload={failedPlanning} />
      {typeof failedPlanning.error_code === 'string' && <p>Код: {failedPlanning.error_code}</p>}
      <dl><dt>Вызовы LLM</dt><dd>{safeCount(currentRuntime?.counters?.llm_calls) ?? '—'}</dd><dt>Поиски</dt><dd>{safeCount(currentRuntime?.counters?.search_queries) ?? '—'}</dd><dt>Загрузки</dt><dd>{safeCount(currentRuntime?.counters?.fetches) ?? '—'}</dd></dl>
      {onRetry && <button type="button" disabled={busy} onClick={onRetry}>Повторить ответ</button>}
    </section>}
    {!!currentRuntime?.contradictions?.length && <div className="chat-partial-note" role="status"><h4>Неразрешённые противоречия</h4><ul>{stringList(currentRuntime.contradictions).map((item, index) => <li key={index}>{item}</li>)}</ul></div>}
    {resources.runtime?.error && <Status resource={resources.runtime} />}
    <nav className="chat-research-shortcuts q-research-tabs" aria-label="Разделы исследования" role="tablist">
      <button type="button" role="tab" aria-controls="research-sources-section" onClick={(event) => focusSection(event.currentTarget, 'sources')}>Источники · {sourcesKnown ? sourceList.length : '—'}</button>
      <button type="button" role="tab" aria-controls="research-evidence-section" onClick={(event) => focusSection(event.currentTarget, 'evidence')}>Подтверждения · {ownedEvidence?.length ?? '—'}</button>
    </nav>
    {fallback && <p className="chat-partial-note" role="status">Показаны дословные выдержки. Синтезированный вывод надёжно подтвердить не удалось.</p>}
    {partial && <p className="chat-partial-note" role="status">Часть источников недоступна или отклонена. Это может ограничить полноту ответа.</p>}
    {readiness && <section className="chat-partial-note" aria-label="Проверка критериев"><h3>Готовность ответа</h3><p role="status">{readinessLabels[readinessVerdict] || 'Статус проверки неизвестен'}</p><p>Это оценка готовности evidence; проверка цитат выполняется отдельно.</p>{criticalMissing.length > 0 && <><h4>Критические пробелы</h4><ul>{criticalMissing.map((item, index) => <li key={`critical-${index}`}>{item}</li>)}</ul></>}{supplementaryMissing.length > 0 && <><h4>Дополнительные критерии</h4><ul>{supplementaryMissing.map((item, index) => <li key={`supplementary-${index}`}>{item}</li>)}</ul></>}{criteria.length > 0 && <details><summary>Оценки критериев · {criteria.length}</summary><ol>{criteria.map((item) => {
      const title = plannedSteps.find((step) => step.id === item.step_id)?.success_criteria?.[item.criterion_index]
      return <li key={`${item.step_id}-${item.criterion_index}`}><strong>{item.classification === 'critical' ? 'Критический' : 'Дополнительный'}</strong>: {title || `Критерий ${item.step_id}.${item.criterion_index + 1}`} · {item.met ? 'выполнен' : 'не подтверждён'}{item.evidence_ids.length > 0 && <p>Evidence IDs: {item.evidence_ids.map((id) => <code key={id}>{id} </code>)}</p>}</li>
    })}</ol></details>}</section>}
    <section><h3>{activePlan ? 'План текущего раунда' : 'План'}</h3>
      {currentRuntime?.adaptive_enabled && <p>Раунд {currentRuntime.current_round ?? 'неизвестен'}</p>}
      {steps.length > 0 && <div className="research-progress" aria-label="Обработка плана"><div className="research-progress__caption"><span>Обработано этапов</span><strong>{processed} / {steps.length}</strong></div><progress value={processed} max={steps.length} aria-label="Обработано этапов плана" /><p>Выполнено: {completed} · Пропущено: {skipped}. Это обработка плана, а не готовность отчёта.</p></div>}
      {!failedPlanning && (!activePlan || plan.error) && <Status resource={plan} empty={!steps.length} />}
      {steps.length > 0 && <ol className="chat-plan-list">{steps.map((step) => {
        const lead = step.description.split(/[.!?](?:\s|$)|:\s/)[0]
        const concise = lead.length > 120 ? `${lead.slice(0, 117).trimEnd()}…` : lead
        return <li key={step.id} data-status={step.status}><span className="research-step-mark" aria-hidden="true">{step.status === 'completed' ? '✓' : step.status === 'running' ? '•' : step.status === 'skipped' ? '–' : '·'}</span><div><Expandable title={concise}><p>{step.description}</p></Expandable><small>{labels[step.status] || step.status}</small></div></li>
      })}</ol>}
    </section>
    <section id="research-sources-section" className="q-research-inspector__section" data-research-section="sources"><h3 tabIndex={-1}>Источники <span>{sourcesKnown ? sourceList.length : '—'}</span></h3><Status resource={sources} empty={sourcesKnown && !sourceList.length} />
      <ul className="chat-source-cards">{sourceList.map((source) => {
        const url = safeURL(source.canonical_url)
        const unavailable = ['rejected', 'failed'].includes(source.status || '') || !!source.error_code
        const unfinished = terminal && source.status === 'fetching'
        return <li key={source.id}>
          {url ? <a href={url} target="_blank" rel="noopener noreferrer">{source.title || new URL(url).hostname}</a> : <strong>{source.title || 'Источник без доступной ссылки'}</strong>}
          <p>{unfinished ? 'Загрузка не подтверждена' : labels[source.status || ''] || source.status || 'Найден'}{source.provider ? ` · ${source.provider}` : ''}</p>
          {url && <small>{new URL(url).hostname}</small>}
          {source.snippet && <p>{source.snippet}</p>}
          {unavailable && <div className="chat-source-error" role="status"><p>{researchError(source.error_code)}</p></div>}
          <Expandable title="Сведения о загрузке">
          {source.query && <p>Запрос: {source.query}</p>}
          {source.error_code && <small>Код: {source.error_code}</small>}
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
          </Expandable>
        </li>
      })}</ul>
    </section>
    <section id="research-evidence-section" className="q-research-inspector__section" data-research-section="evidence"><h3 tabIndex={-1}>Evidence <span className="q-research-inspector__human-label">Подтверждения</span> <span>{ownedEvidence?.length ?? '—'}</span></h3><Status resource={evidence} empty={ownedEvidence !== undefined && !ownedEvidence.length} />
      <ul className="chat-source-cards">{(ownedEvidence ?? []).map((item) => <li key={item.id}><strong>{item.title || 'Сохранённый источник'}</strong>{safeURL(item.canonical_url) && <small>{new URL(safeURL(item.canonical_url)).hostname}</small>}<p>Сохранённый материал</p><p>{item.excerpt}</p>{safeURL(item.canonical_url) && <a href={safeURL(item.canonical_url)} target="_blank" rel="noopener noreferrer">Открыть источник</a>}<details><summary>Происхождение материала</summary><small>{item.provider} · {item.extraction_method}</small><code>{item.id}</code></details></li>)}</ul>
      <p className="chat-muted">Ссылки подтверждают наличие источника, а не проверку каждого утверждения.</p>
    </section>
    <section className="research-secondary"><Expandable title="Диагностика запуска">
      {onDiagnostics && <><p>Проверка может вызвать локальную модель и поисковые сервисы. Запускается только по вашему действию.</p><button type="button" disabled={diagnosticsLoading} onClick={onDiagnostics}>Проверить модель и поиск</button>{diagnosticsLoading && <p role="status">Проверяем модель и поиск…</p>}</>}
      {diagnosticsError && <p className="chat-resource-error" role="status">{diagnosticsError}</p>}
      <ContextNotice budget={currentRuntime?.context_budget} />
      {currentRuntime?.privacy_mode && <PrivacyNotice mode={currentRuntime.privacy_mode} />}
      <TelemetryPanel runtime={currentRuntime} />
      {resources.pdfCapability && <p role="status">PDF: {resources.pdfCapability.state==='isolated_available'?'изолированная среда доступна':resources.pdfCapability.state==='pure_go_fallback_available'?'разрешён process-bounded fallback без OS-изоляции':'извлечение недоступно'}. {resources.pdfCapability.smoke_tested?'Fixture smoke подтверждён.':'Наличие capability не подтверждает извлечение конкретного PDF.'}</p>}
      {!!currentRuntime?.research_rounds?.length && <section aria-label="Раунды исследования"><h3>Раунды исследования</h3><ol>{currentRuntime.research_rounds.map((round) => <li key={round.number}><strong>Раунд {round.number}</strong><p>Evidence: {safeCount(round.evidence_count) ?? '—'} · Открытых вопросов: {safeCount(round.frontier_count) ?? '—'}</p>{stringList(round.critical_missing).length > 0 && <ul>{stringList(round.critical_missing).map((item,index)=><li key={index}>{item}</li>)}</ul>}</li>)}</ol>{currentRuntime.research_stop_reason && <p>Причина остановки: {currentRuntime.research_stop_reason}</p>}</section>}
      <Expandable title="Поисковые запросы">
        {queries.queries.length > 0 ? <ul>{queries.queries.map((query) => <li key={query}>{query}</li>)}</ul> : <p className="chat-muted">Точные запросы пока не переданы backend.</p>}
        {queries.summaries.length > 0 && <><h4>Краткие действия поиска</h4><ul>{queries.summaries.map((summary) => <li key={summary}>{summary}</li>)}</ul></>}
      </Expandable>
    <section className="research-secondary"><Expandable title="Поисковые провайдеры"><button type="button" disabled={providers.loading} onClick={() => void providers.reload('manual_retry')}>{providers.loading ? 'Обновляем…' : 'Обновить провайдеров'}</button><Status resource={providers} empty={!providers.data} />{Object.entries(providers.data?.search.providers ?? {}).map(([name, provider]) => <ProviderCard key={name} name={name} provider={provider} checkedLabel={provider.last_checked ? stamp(provider.last_checked) : ''} />)}</Expandable></section>
    <section className="research-secondary"><Expandable title="Действия">{!activity.length && <p className="chat-muted">События появятся во время работы.</p>}
      <ol>{activity.slice(-30).reverse().map((event) => <li key={event.seq}>
        <strong>{({ 'context.compacted': 'Рабочий контекст сжат', 'continuation.requested': 'Ограниченное продолжение исследования', 'verification.completed': 'Проверка готовности evidence', 'run.paused': 'Запуск на паузе', 'run.resumed': 'Запуск возобновлён', 'run.blocked': 'Запуск заблокирован' } as Record<string, string>)[event.type] || (event.type === 'search.provider' ? 'Результат провайдера' : event.type === 'tool.failed' ? 'Не удалось выполнить действие' : event.type === 'tool.started' ? 'Выполняется действие' : event.type === 'plan.created' ? 'План сохранён' : event.type === 'evidence.created' ? 'Источник сохранён' : labels[String(event.payload.to || event.stage || '')] || 'Состояние обновлено')}</strong>
        {event.type === 'context.compacted' && typeof event.payload.reduced_bytes === 'number' && <p>Сокращено {formatSourceBytes(event.payload.reduced_bytes)}. Evidence и checkpoint сохранены.</p>}
        {event.type === 'verification.completed' && typeof event.payload.verdict === 'string' && <p>{readinessLabels[event.payload.verdict] || event.payload.verdict}</p>}
        {event.type === 'search.provider' && <p>{diagnosticSummary(event.payload)}</p>}
        {event.type === 'tool.failed' && <p>{researchError(typeof event.payload.error_code === 'string' ? event.payload.error_code : typeof event.payload.code === 'string' ? event.payload.code : '')}</p>}
        <time>{stamp(event.created_at)}</time>
      </li>)}</ol>
    </Expandable></section>
    </Expandable></section>
  </div>
}
