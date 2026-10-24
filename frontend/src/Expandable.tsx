import { useId, useState, type ReactNode } from 'react'

/** Quiet, keyboard-accessible disclosure shared by research details. */
export default function Expandable({ title, children, className = '' }: { title: string; children: ReactNode; className?: string }) {
  const id = useId()
  const [open, setOpen] = useState(false)
  return <div className={`research-expandable ${className}`}>
    <button type="button" className="research-expandable__trigger" aria-expanded={open} aria-controls={id} onClick={() => setOpen(!open)}>
      <span>{title}</span><svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true"><path d="m5 6 3 3 3-3" /></svg>
    </button>
    <div id={id} className="research-expandable__grid" data-open={open} aria-hidden={!open} inert={!open}>
      <div className="research-expandable__content">{children}</div>
    </div>
  </div>
}
