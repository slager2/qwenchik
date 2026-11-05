import { useLayoutEffect, useRef, type KeyboardEvent, type ReactNode } from 'react'
import { Icon } from '../icons'
import { cx } from './cx'
import IconButton from './IconButton'

const focusableSelector = 'a[href], button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), summary, [tabindex="0"]'

function focusable(container: HTMLElement) {
  return [...container.querySelectorAll<HTMLElement>(focusableSelector)].filter((element) => {
    if (element.tabIndex < 0 || element.closest('[inert], [hidden], [aria-hidden="true"]') || !element.getClientRects().length) return false
    if (typeof element.checkVisibility === 'function' && !element.checkVisibility({ checkVisibilityCSS: true, checkOpacity: true })) return false
    // Closed disclosures can retain layout boxes. Only their own first summary
    // participates in keyboard order; hidden descendants must not trap focus.
    for (let ancestor = element.parentElement; ancestor && ancestor !== container; ancestor = ancestor.parentElement) {
      if (ancestor instanceof HTMLDetailsElement && !ancestor.open && !ancestor.querySelector(':scope > summary')?.contains(element)) return false
    }
    return true
  })
}

/**
 * Modal side sheet on a native <dialog>. Opens on mount, closes on unmount,
 * traps Tab focus, closes on Escape/backdrop and restores the previous focus.
 */
export default function Drawer({ title, onClose, children, side = 'right', className, bodyClassName, hideTitle }: {
  title: string
  onClose: () => void
  children: ReactNode
  side?: 'left' | 'right'
  className?: string
  bodyClassName?: string
  hideTitle?: boolean
}) {
  const ref = useRef<HTMLDialogElement>(null)
  useLayoutEffect(() => {
    const previous = document.activeElement as HTMLElement | null
    const dialog = ref.current
    dialog?.showModal()
    return () => { dialog?.close(); previous?.focus({ preventScroll: true }) }
  }, [])
  const trap = (event: KeyboardEvent<HTMLDialogElement>) => {
    if (event.key !== 'Tab') return
    const items = focusable(event.currentTarget)
    const first = items[0]
    const last = items.at(-1)
    if (!first || !last) { event.preventDefault(); return }
    if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus() }
    if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus() }
  }
  return <dialog ref={ref} className={cx('ui-drawer', `ui-drawer--${side}`, className)} aria-label={title} onCancel={onClose} onKeyDown={trap} onClick={(event) => { if (event.target === event.currentTarget) onClose() }}>
    <div className={cx('ui-drawer__body', bodyClassName)}>
      <header className="ui-drawer__header">
        <h2 className={hideTitle ? 'sr-only' : undefined}>{title}</h2>
        <IconButton label={`Закрыть: ${title}`} icon={<Icon name="close" />} onClick={onClose} />
      </header>
      {children}
    </div>
  </dialog>
}
