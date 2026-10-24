const text = (value: unknown) => typeof value === 'string' ? value.slice(0, 1000) : ''
export const safeCount = (value: unknown): number | undefined => Number.isSafeInteger(value) && (value as number) >= 0 ? value as number : undefined

export default function FailureDetails({ payload }: { payload?: Record<string, unknown> }) {
  if (!payload) return null
  const reason = text(payload.validation_error)
  const stage = text(payload.failure_stage)
  const role = text(payload.failure_role)
  const attempt = safeCount(payload.planner_attempt)
  const summary = payload.plan_summary
  const counts = summary && typeof summary === 'object' && !Array.isArray(summary)
    ? Object.entries(summary).filter(([key, value]) => /^[a-z][a-z0-9_]{0,63}$/.test(key) && safeCount(value) !== undefined).slice(0, 20)
    : []
  return <>
    {reason && <p>Причина: {reason}</p>}
    <dl>
      {stage && <><dt>Этап сбоя</dt><dd>{stage}</dd></>}
      {role && <><dt>Роль модели</dt><dd>{role}</dd></>}
      {attempt !== undefined && <><dt>Попытка планирования</dt><dd>{attempt}</dd></>}
    </dl>
    {counts.length > 0 && <div aria-label="Сводка плана"><strong>Сводка плана</strong><ul>{counts.map(([key, value]) => <li key={key}>{key}: {value as number}</li>)}</ul></div>}
  </>
}
