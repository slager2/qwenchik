import type { Approval } from '../types'
import { Badge, Button } from '../ui'
import { timeLabel } from './labels'

export default function ApprovalCard({ approval, busy, onApprove, onReject }: { approval: Approval; busy: boolean; onApprove: () => void; onReject: () => void }) {
  return (
    <section className="approval-card" aria-labelledby={`approval-${approval.id}`}>
      <p className="approval-kicker"><Badge tone="warning">Ожидает вашего решения</Badge></p>
      <h3 id={`approval-${approval.id}`}>{approval.tool_name}</h3>
      <dl className="approval-details">
        <div><dt>Адресат</dt><dd>{approval.target || 'не указан'}</dd></div>
        <div><dt>Почему</dt><dd>{approval.risk_explanation}</dd></div>
        <div><dt>Аргументы действия</dt><dd><pre>{JSON.stringify(approval.arguments, null, 2)}</pre></dd></div>
        {approval.tool_name === 'harness.promote_bundle' && typeof approval.arguments.bundle_hash === 'string' && /^[a-f0-9]{64}$/.test(approval.arguments.bundle_hash) && <div><dt>Перед подтверждением</dt><dd><a href={`/api/bundles/${approval.arguments.bundle_hash}`} target="_blank" rel="noopener noreferrer">Открыть CandidatePatch, provenance и результаты held-out проверки</a><p>Будут изменены только operational hints для новых запусков. Политики, инструменты и hard budgets остаются под контролем backend.</p></dd></div>}
        <div><dt>Истекает</dt><dd>{timeLabel(approval.expires_at)}</dd></div>
      </dl>
      <div className="approval-actions"><Button variant="ghost" onClick={onReject} disabled={busy}>Отклонить</Button><Button variant="primary" onClick={onApprove} disabled={busy} loading={busy}>{busy ? 'Сохраняем…' : 'Подтвердить действие'}</Button></div>
    </section>
  )
}
