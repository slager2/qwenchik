import { useId } from 'react'
import { researchPresentation, resumeEligibility } from './run-presentation.mjs'
import { Icon } from './icons'
import { Spinner } from './ui'
import type { AgentEvent, ConversationMessage, Evidence, Run, RunRuntime } from './types'

interface ResearchOverviewProps {
  run: Run | null
  runtime: RunRuntime | null
  events: AgentEvent[]
  answer?: ConversationMessage | null
  evidence: { data?: Evidence[]; loading: boolean; error: string }
  busy?: boolean
  onDetails: () => void
  onReadAnswer?: () => void
  onPause?: () => void
  onResume?: () => void
  onStop?: () => void
}

/* Hallmark · component: research overview · modern-minimal / locked Cobalt
 * pre-emit critique: P5 H5 E4 S5 R5 V4 · saved output is distinct from execution */
export default function ResearchOverview({ run, runtime, events, answer, evidence, busy, onDetails, onReadAnswer, onPause, onResume, onStop }: ResearchOverviewProps) {
  const headingID = useId()
  const noteID = useId()
  if (run?.mode !== 'research') return null
  const view = researchPresentation({ run, runtime, events, answer, evidence })
  const resume = resumeEligibility(run, runtime)
  const paused = run.status === 'paused'
  const active = !['completed', 'failed', 'cancelled'].includes(run.status)
  return <section className="research-overview" aria-labelledby={headingID} aria-busy={busy || undefined} data-active={active || undefined}>
    <div className="q-research-overview__heading-row">
      <span className="q-research-overview__indicator" aria-hidden="true">{active && !paused ? <Spinner size="sm" decorative /> : <Icon name={paused ? 'pause' : 'sources'} size={16} />}</span>
      <div className="q-research-overview__text">
        <h2 className="research-overview__heading" id={headingID}>{view.heading}</h2>
        {view.state && <p className="research-overview__state">{view.state}</p>}
        <ul className="research-overview__facts"><li><span className="q-research-overview__fact-label sr-only">Ответ и отчёт: </span>{view.answerLabel} · {view.reportLabel}</li><li><span className="q-research-overview__fact-label">{view.evidenceLabel}</span></li></ul>
      </div>
      {busy && <span className="q-research-overview__live" role="status">Обновляется</span>}
    </div>
    {paused && <p className="research-overview__note" id={noteID}>{resume.explanation}</p>}
    {(view.criticalMissing.length > 0 || view.reportGaps.length > 0 || view.fallback) && <details className="research-overview__note">
      <summary>{view.criticalMissing.length > 0 ? 'Есть критические пробелы' : view.reportGaps.length > 0 ? 'Ограничения отчёта' : 'Сохранены дословные выдержки'}</summary>
      {view.criticalMissing.length > 0 && <><p>Критические подтверждения отсутствуют:</p><ul>{view.criticalMissing.map((item, index) => <li key={index}>{item}</li>)}</ul></>}
      {view.reportGaps.length > 0 && <><p>Не отражено в сохранённом отчёте:</p><ul>{view.reportGaps.map((item, index) => <li key={index}>{item}</li>)}</ul></>}
      {view.fallback && <p>Показаны дословные выдержки. Синтезированный вывод надёжно подтвердить не удалось.</p>}
      <p>Полные сведения доступны в деталях исследования.</p>
    </details>}
    <div className="research-overview__actions">
      {answer?.run_id === run.id && !!answer.content.trim() && onReadAnswer && <button type="button" className="ui-button ui-button--secondary ui-button--sm" onClick={onReadAnswer}>Читать ответ</button>}
      {paused && onResume && <button type="button" aria-describedby={noteID} disabled={busy || !resume.allowed} onClick={onResume}>Возобновить</button>}
      {['planning', 'running', 'verifying'].includes(run.status) && onPause && <button type="button" className="ui-button ui-button--ghost ui-button--sm" disabled={busy} onClick={onPause}>Пауза</button>}
      {active && onStop && <button type="button" className="ui-button ui-button--danger ui-button--sm" disabled={busy} onClick={onStop}>Остановить</button>}
      <button type="button" className="ui-button ui-button--ghost ui-button--sm" onClick={onDetails}>План и источники</button>
    </div>
  </section>
}
