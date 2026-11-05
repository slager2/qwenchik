import type { HTMLAttributes } from 'react'
import { cx } from './cx'

export type BadgeTone = 'neutral' | 'accent' | 'success' | 'warning' | 'danger'

/** Quiet status label: text plus an optional dot, never a filled pill. */
export default function Badge({ tone = 'neutral', dot = true, className, children, ...props }: HTMLAttributes<HTMLSpanElement> & { tone?: BadgeTone; dot?: boolean }) {
  return <span className={cx('ui-badge', `ui-badge--${tone}`, className)} data-tone={tone} {...props}>{dot && <span className="ui-badge__dot" aria-hidden="true" />}{children}</span>
}
