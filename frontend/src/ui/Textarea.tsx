import { useLayoutEffect, useRef, type Ref, type TextareaHTMLAttributes } from 'react'
import { cx } from './cx'

export type TextareaProps = TextareaHTMLAttributes<HTMLTextAreaElement> & {
  /** Grow with content up to `maxRows` lines. */
  autoGrow?: boolean
  maxRows?: number
  ref?: Ref<HTMLTextAreaElement>
}

function assignRef<T>(ref: Ref<T> | undefined, value: T | null) {
  if (typeof ref === 'function') ref(value)
  else if (ref) (ref as { current: T | null }).current = value
}

export default function Textarea({ className, autoGrow, maxRows = 10, value, ref, ...props }: TextareaProps) {
  const local = useRef<HTMLTextAreaElement | null>(null)
  useLayoutEffect(() => {
    const element = local.current
    if (!autoGrow || !element) return
    const style = getComputedStyle(element)
    const line = parseFloat(style.lineHeight) || 24
    const chrome = parseFloat(style.paddingTop) + parseFloat(style.paddingBottom) + parseFloat(style.borderTopWidth) + parseFloat(style.borderBottomWidth)
    element.style.height = 'auto'
    const max = line * maxRows + chrome
    element.style.height = `${Math.min(element.scrollHeight + parseFloat(style.borderTopWidth) + parseFloat(style.borderBottomWidth), max)}px`
    element.style.overflowY = element.scrollHeight > max ? 'auto' : 'hidden'
  }, [autoGrow, maxRows, value])
  return <textarea ref={(element) => { local.current = element; assignRef(ref, element) }} className={cx('ui-textarea', autoGrow && 'ui-textarea--grow', className)} value={value} {...props} />
}
