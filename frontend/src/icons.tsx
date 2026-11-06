import type { ReactElement, SVGProps } from 'react'

export type IconName =
  | 'brand' | 'chat' | 'projects' | 'tasks' | 'memory' | 'settings' | 'status' | 'menu' | 'panel-left' | 'panel-right'
  | 'search' | 'plus' | 'close' | 'send' | 'stop' | 'copy' | 'check' | 'chevron-down' | 'chevron-right' | 'sun' | 'moon'
  | 'edit' | 'trash' | 'sources' | 'external' | 'refresh' | 'arrow-down' | 'more' | 'pause' | 'play' | 'sliders' | 'alert' | 'download'

type IconProps = Omit<SVGProps<SVGSVGElement>, 'children'> & { name: IconName; title?: string; size?: number }

const paths: Record<IconName, ReactElement> = {
  brand: <><circle cx="12" cy="12" r="8.25" /><path d="M15.5 15.5 19 19" /><path d="M9 12a3 3 0 0 1 3-3" /></>,
  chat: <path d="M5 6.5A2.5 2.5 0 0 1 7.5 4h9A2.5 2.5 0 0 1 19 6.5v7a2.5 2.5 0 0 1-2.5 2.5H11l-4 3.5V16h0A2 2 0 0 1 5 14z" />,
  projects: <path d="M4 7.5A1.5 1.5 0 0 1 5.5 6h4l2 2h7A1.5 1.5 0 0 1 20 9.5v8a1.5 1.5 0 0 1-1.5 1.5h-13A1.5 1.5 0 0 1 4 17.5z" />,
  tasks: <><path d="m5 7 1.5 1.5L9 6" /><path d="m5 13 1.5 1.5L9 12" /><path d="M12 7.5h7M12 13.5h7M5 18.5h14" /></>,
  memory: <><path d="M6 4.5h10.5A1.5 1.5 0 0 1 18 6v13.5H7.5A1.5 1.5 0 0 1 6 18z" /><path d="M6 18a1.5 1.5 0 0 1 1.5-1.5H18" /><path d="M9.5 8.5h5" /></>,
  settings: <><circle cx="12" cy="12" r="2.75" /><path d="M19.4 13.4a7.8 7.8 0 0 0 .05-2.8l1.8-1.4-1.8-3.1-2.15.86a7.8 7.8 0 0 0-2.42-1.4L14.55 3h-3.6l-.35 2.55a7.8 7.8 0 0 0-2.42 1.4l-2.15-.86-1.8 3.1 1.8 1.4a7.8 7.8 0 0 0 0 2.8l-1.8 1.4 1.8 3.1 2.15-.86a7.8 7.8 0 0 0 2.42 1.4l.35 2.55h3.6l.35-2.55a7.8 7.8 0 0 0 2.42-1.4l2.15.86 1.8-3.1z" /></>,
  status: <><circle cx="12" cy="12" r="8" /><path d="M12 8v4l2.5 1.6" /></>,
  menu: <path d="M4.5 7.5h15M4.5 12h15M4.5 16.5h15" />,
  'panel-left': <><rect x="4" y="5" width="16" height="14" rx="2" /><path d="M9.5 5v14" /></>,
  'panel-right': <><rect x="4" y="5" width="16" height="14" rx="2" /><path d="M14.5 5v14" /></>,
  search: <><circle cx="10.5" cy="10.5" r="5.5" /><path d="m15 15 4 4" /></>,
  plus: <path d="M12 5v14M5 12h14" />,
  close: <path d="m6.5 6.5 11 11M17.5 6.5l-11 11" />,
  send: <path d="M12 19V5M6 11l6-6 6 6" />,
  stop: <rect x="7" y="7" width="10" height="10" rx="1.5" fill="currentColor" stroke="none" />,
  copy: <><rect x="8.5" y="8.5" width="10.5" height="10.5" rx="2" /><path d="M15.5 8.5V6.5a2 2 0 0 0-2-2h-7a2 2 0 0 0-2 2v7a2 2 0 0 0 2 2h2" /></>,
  check: <path d="m5.5 12.5 4 4 9-9" />,
  'chevron-down': <path d="m7 10 5 5 5-5" />,
  'chevron-right': <path d="m10 7 5 5-5 5" />,
  sun: <><circle cx="12" cy="12" r="3.5" /><path d="M12 3.5v2M12 18.5v2M3.5 12h2M18.5 12h2M6 6l1.4 1.4M16.6 16.6 18 18M6 18l1.4-1.4M16.6 7.4 18 6" /></>,
  moon: <path d="M19 14.5A7.5 7.5 0 0 1 9.5 5a7.5 7.5 0 1 0 9.5 9.5z" />,
  edit: <><path d="M5 19h4L19 9l-4-4L5 15z" /><path d="m13.5 6.5 4 4" /></>,
  trash: <><path d="M5 7h14M10 7V5h4v2M7 7l1 12h8l1-12" /></>,
  sources: <><path d="M5 5.5h5.5a2 2 0 0 1 2 2V19a1.5 1.5 0 0 0-1.5-1.5H5z" /><path d="M19 5.5h-5.5a2 2 0 0 0-2 2V19a1.5 1.5 0 0 1 1.5-1.5H19z" /></>,
  external: <><path d="M14 5h5v5M19 5l-8 8" /><path d="M17 14v4a1 1 0 0 1-1 1H6a1 1 0 0 1-1-1V8a1 1 0 0 1 1-1h4" /></>,
  refresh: <><path d="M19 8a7.5 7.5 0 1 0 .5 6" /><path d="M19.5 4v4.5H15" /></>,
  'arrow-down': <path d="M12 5v14M6 13l6 6 6-6" />,
  download: <><path d="M12 4.5v10M7.5 10.5 12 15l4.5-4.5" /><path d="M5 16.5v1.5A1.5 1.5 0 0 0 6.5 19.5h11a1.5 1.5 0 0 0 1.5-1.5v-1.5" /></>,
  more: <><circle cx="6" cy="12" r="1" fill="currentColor" /><circle cx="12" cy="12" r="1" fill="currentColor" /><circle cx="18" cy="12" r="1" fill="currentColor" /></>,
  pause: <path d="M9 6v12M15 6v12" />,
  play: <path d="M8 5.5v13l10-6.5z" />,
  sliders: <><path d="M5 7h9M18 7h1M5 17h3M12 17h7" /><circle cx="16" cy="7" r="2" /><circle cx="10" cy="17" r="2" /></>,
  alert: <><path d="M12 4 3 19.5h18z" /><path d="M12 10v4.5M12 17.2v.1" /></>,
}

/** A small, dependency-free icon primitive. All shapes inherit currentColor. */
export function Icon({ name, title, size = 20, ...props }: IconProps) {
  return <svg viewBox="0 0 24 24" width={size} height={size} fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden={title ? undefined : true} role={title ? 'img' : undefined} focusable="false" {...props}>
    {title && <title>{title}</title>}
    {paths[name]}
  </svg>
}
