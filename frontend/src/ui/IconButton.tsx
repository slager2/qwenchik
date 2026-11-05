import type { ButtonHTMLAttributes, ReactNode, Ref } from 'react'
import { cx } from './cx'

export type IconButtonProps = Omit<ButtonHTMLAttributes<HTMLButtonElement>, 'children'> & {
  /** Accessible name; also used as the native tooltip unless `title` is given. */
  label: string
  icon: ReactNode
  variant?: 'ghost' | 'secondary' | 'primary'
  ref?: Ref<HTMLButtonElement>
}

export default function IconButton({ label, icon, variant = 'ghost', className, type = 'button', title, ...props }: IconButtonProps) {
  return <button type={type} className={cx('ui-icon-button', `ui-icon-button--${variant}`, className)} aria-label={label} title={title ?? label} {...props}>{icon}</button>
}
