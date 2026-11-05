import { useId, type ReactElement, cloneElement } from 'react'

/** CSS tooltip shown on hover and keyboard focus. The child keeps its own accessible name. */
export default function Tooltip({ content, children, side = 'top' }: { content: string; children: ReactElement<{ 'aria-describedby'?: string }>; side?: 'top' | 'bottom' }) {
  const id = useId()
  return <span className="ui-tooltip" data-side={side}>
    {cloneElement(children, { 'aria-describedby': id })}
    <span role="tooltip" id={id} className="ui-tooltip__bubble">{content}</span>
  </span>
}
