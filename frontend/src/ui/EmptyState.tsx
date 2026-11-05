import type { ReactNode } from 'react'
import { cx } from './cx'

export default function EmptyState({ icon, title, description, action, className }: { icon?: ReactNode; title: ReactNode; description?: ReactNode; action?: ReactNode; className?: string }) {
  return <div className={cx('ui-empty', className)}>
    {icon && <div className="ui-empty__icon" aria-hidden="true">{icon}</div>}
    <p className="ui-empty__title">{title}</p>
    {description && <p className="ui-empty__description">{description}</p>}
    {action && <div className="ui-empty__action">{action}</div>}
  </div>
}
