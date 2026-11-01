import type { ReactNode } from 'react'

export type ResearchProgressStage = 'queued' | 'search' | 'read' | 'verify' | 'answer'

interface ResearchProgressProps {
  status?: string
  phase?: string
  processed?: number
  total?: number
  children?: ReactNode
}

const stageLabels: Record<ResearchProgressStage, string> = {
  queued: 'Подготовка',
  search: 'Поиск источников',
  read: 'Чтение материалов',
  verify: 'Проверка фактов',
  answer: 'Подготовка ответа',
}

const stageFor = (status: string | undefined, phase: string | undefined): ResearchProgressStage => {
  const value = `${phase || ''} ${status || ''}`.toLowerCase()
  if (value.includes('search')) return 'search'
  if (value.includes('fetch') || value.includes('extract') || value.includes('read')) return 'read'
  if (value.includes('verif')) return 'verify'
  if (value.includes('synth') || value.includes('final') || value.includes('answer') || value.includes('complet')) return 'answer'
  return 'queued'
}

const statusText = (status: string | undefined) => {
  if (status === 'paused') return 'На паузе'
  if (status === 'failed') return 'Не удалось завершить'
  if (status === 'cancelled') return 'Остановлено'
  if (status === 'completed') return 'Готово'
  if (status === 'waiting_approval') return 'Нужно решение'
  return 'Выполняется'
}

/** Presentation-only progress model. It never infers or mutates backend state. */
export default function ResearchProgress({ status, phase, processed, total, children }: ResearchProgressProps) {
  const active = stageFor(status, phase)
  const stages = Object.keys(stageLabels) as ResearchProgressStage[]
  const activeIndex = stages.indexOf(active)
  const hasPlan = typeof processed === 'number' && typeof total === 'number' && total > 0
  const boundedProcessed = hasPlan ? Math.max(0, Math.min(processed!, total!)) : undefined

  return <section className="q-research-progress" aria-label="Этапы исследования" data-active-stage={active}>
    <div className="q-research-progress__header">
      <div>
        <p className="q-research-progress__eyebrow">Ход исследования</p>
        <p className="q-research-progress__status" role="status">{statusText(status)}</p>
      </div>
      {hasPlan && <span className="q-research-progress__count">{boundedProcessed} из {total} этапов</span>}
    </div>
    <ol className="q-research-progress__steps">
      {stages.map((stage, index) => {
        const complete = index < activeIndex || status === 'completed'
        const current = index === activeIndex && status !== 'completed'
        return <li key={stage} className="q-research-progress__step" data-current={current || undefined} data-complete={complete || undefined}>
          <span className="q-research-progress__marker" aria-hidden="true">{complete ? '✓' : index + 1}</span>
          <span>{stageLabels[stage]}</span>
        </li>
      })}
    </ol>
    {hasPlan && <progress className="q-research-progress__bar" value={boundedProcessed} max={total} aria-label="Обработано этапов плана" />}
    {children}
  </section>
}
