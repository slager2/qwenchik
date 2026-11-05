import type { InputHTMLAttributes, Ref } from 'react'
import { cx } from './cx'

export type InputProps = InputHTMLAttributes<HTMLInputElement> & { ref?: Ref<HTMLInputElement> }

export default function Input({ className, ...props }: InputProps) {
  return <input className={cx('ui-input', className)} {...props} />
}
