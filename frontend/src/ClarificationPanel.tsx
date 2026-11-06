import { useEffect, useId, useRef, useState } from 'react'
import { CLARIFY_EXTRA_LIMIT, clarificationChoice } from './clarification-state.mjs'
import type { ClarificationChoice, ResearchClarification } from './types'
import { Icon } from './icons'
import { Button, Spinner, Textarea } from './ui'
import './styles/clarify.css'

/**
 * Shown between a research request and the run: the model's readings of the
 * question, quick answers, and free text. Everything is optional; "Skip"
 * starts research with the original question.
 */
export default function ClarificationPanel({ clarification, busy, onStart, onSkip, onEdit }: {
  /** Undefined while options are being prepared. */
  clarification?: ResearchClarification
  busy: boolean
  onStart: (choice: ClarificationChoice | undefined) => void
  onSkip: () => void
  onEdit?: () => void
}) {
  const id = useId()
  const [selected, setSelected] = useState('')
  const [answers, setAnswers] = useState<Record<string, string>>({})
  const [extra, setExtra] = useState('')
  const root = useRef<HTMLElement>(null)
  const loading = !clarification
  useEffect(() => { root.current?.scrollIntoView({ block: 'nearest', behavior: 'instant' }) }, [loading])
  const choice = clarificationChoice(clarification, selected, answers, extra)

  return <section ref={root} className="clarify" aria-labelledby={`${id}-title`} aria-busy={loading || undefined}>
    <header className="clarify__header">
      <span className="clarify__icon" aria-hidden="true"><Icon name="sliders" size={18} /></span>
      <div>
        <h2 id={`${id}-title`}>Уточним запрос</h2>
        <p>{loading ? 'Модель предложит варианты трактовки и уточняющие вопросы — обычно до 30 секунд. Можно не ждать и начать сразу.' : 'Выберите, что именно нужно, и ответьте на пару вопросов — план и сроки будут точнее. Всё необязательно.'}</p>
      </div>
    </header>

    {loading ? <div className="clarify__loading" role="status"><Spinner size="sm" decorative /><span>Готовлю варианты…</span></div> : <>
      {clarification.interpretations.length > 0 && <div className="clarify__group">
        <h3 id={`${id}-readings`}>Что именно вы хотите получить?</h3>
        <div className="clarify__readings" role="radiogroup" aria-labelledby={`${id}-readings`}>
          {clarification.interpretations.map((item) => {
            const checked = selected === item.id
            return <button key={item.id} type="button" role="radio" aria-checked={checked} className="clarify-reading" disabled={busy} onClick={() => setSelected(checked ? '' : item.id)}>
              <span className="clarify-reading__mark" aria-hidden="true">{checked && <Icon name="check" size={14} />}</span>
              <span className="clarify-reading__text"><strong>{item.title}</strong><span>{item.goal}</span></span>
            </button>
          })}
        </div>
      </div>}

      {clarification.questions.map((question) => <div className="clarify__group" key={question.id}>
        <h3 id={`${id}-${question.id}`}>{question.question}</h3>
        <div className="clarify__chips" role="radiogroup" aria-labelledby={`${id}-${question.id}`}>
          {question.options.map((option) => {
            const checked = answers[question.id] === option
            return <button key={option} type="button" role="radio" aria-checked={checked} className="clarify-chip" disabled={busy} onClick={() => setAnswers((current) => ({ ...current, [question.id]: checked ? '' : option }))}>{checked && <Icon name="check" size={14} />}{option}</button>
          })}
        </div>
      </div>)}

      <div className="clarify__group">
        <label className="clarify__label" htmlFor={`${id}-extra`}>Дополнить <span>необязательно</span></label>
        <Textarea id={`${id}-extra`} className="clarify__extra" autoGrow maxRows={6} rows={2} maxLength={CLARIFY_EXTRA_LIMIT} value={extra} disabled={busy} placeholder="Например: уже знаю Python, интересует геномика, бюджет на курсы — 0 ₽" onChange={(event) => setExtra(event.target.value)} />
      </div>
    </>}

    <footer className="clarify__footer">
      <div className="clarify__actions">
        {!loading && <Button variant="primary" loading={busy} disabled={busy} onClick={() => onStart(choice)} icon={<Icon name="play" size={16} />}>Начать исследование</Button>}
        <Button variant={loading ? 'secondary' : 'ghost'} disabled={busy && !loading} onClick={onSkip}>Пропустить (как есть)</Button>
        {onEdit && !loading && <Button variant="ghost" className="clarify__edit" disabled={busy} onClick={onEdit} icon={<Icon name="edit" size={16} />}>Изменить вопрос</Button>}
      </div>
      {!loading && <p className="clarify__hint" role="status">{choice ? 'Исследование учтёт выбранные уточнения; исходный вопрос сохранится.' : 'Ничего не выбрано — исследование начнётся с исходного вопроса.'}</p>}
    </footer>
  </section>
}
