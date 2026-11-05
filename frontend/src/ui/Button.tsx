import type { ButtonHTMLAttributes, ReactNode, Ref } from 'react'
import { cx } from './cx'
import Spinner from './Spinner'

export type ButtonVariant = 'primary' | 'secondary' | 'ghost' | 'danger'

export type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: ButtonVariant
  size?: 'md' | 'sm'
  icon?: ReactNode
  loading?: boolean
  ref?: Ref<HTMLButtonElement>
}

export default function Button({ variant = 'secondary', size = 'md', icon, loading, className, children, type = 'button', disabled, ...props }: ButtonProps) {
  return <button type={type} className={cx('ui-button', `ui-button--${variant}`, size === 'sm' && 'ui-button--sm', className)} disabled={disabled} aria-busy={loading || undefined} {...props}>
    {loading ? <Spinner size="sm" decorative /> : icon}
    {children}
  </button>
}
