import type { ReactElement, SVGProps } from 'react'

export type IconName = 'brand' | 'chat' | 'projects' | 'tasks' | 'memory' | 'settings' | 'status' | 'menu' | 'panel-left' | 'search' | 'plus' | 'close'

type IconProps = Omit<SVGProps<SVGSVGElement>, 'children'> & { name: IconName; title?: string }

const paths: Record<IconName, ReactElement> = {
  brand: <><path d="M12 3.25a8.75 8.75 0 1 0 8.75 8.75" /><path d="M12 7.25v4.9l3.5 2.1" /><path d="M16.8 4.2h3.95v3.95" /></>,
  chat: <><path d="M4.5 5.5h15v10h-8l-4 3v-3h-3z" /><path d="M8 10.5h8M8 13h5" /></>,
  projects: <><path d="M4.5 6.5h6l1.45 1.8H19.5v9.2H4.5z" /><path d="M4.5 8.3h15" /></>,
  tasks: <><rect x="5" y="4.5" width="14" height="15" rx="1.5" /><path d="M8 9h8M8 13h8M8 17h4" /></>,
  memory: <><path d="M7.2 4.5h8.1A2.2 2.2 0 0 1 17.5 6.7v10.8H8.7A2.2 2.2 0 0 0 6.5 19.7V6.7a2.2 2.2 0 0 1 .7-1.6Z" /><path d="M8.7 17.5h8.8M10 8.5h4.5M10 12h4.5" /></>,
  settings: <><circle cx="12" cy="12" r="3" /><path d="M19.4 13.4a7.8 7.8 0 0 0 .05-2.8l1.8-1.4-1.8-3.1-2.15.86a7.8 7.8 0 0 0-2.42-1.4L14.55 3h-3.6l-.35 2.55a7.8 7.8 0 0 0-2.42 1.4l-2.15-.86-1.8 3.1 1.8 1.4a7.8 7.8 0 0 0 0 2.8l-1.8 1.4 1.8 3.1 2.15-.86a7.8 7.8 0 0 0 2.42 1.4l.35 2.55h3.6l.35-2.55a7.8 7.8 0 0 0 2.42-1.4l2.15.86 1.8-3.1z" /></>,
  status: <><circle cx="12" cy="12" r="8" /><path d="M12 8v4l2.5 1.6" /></>,
  menu: <><path d="M4.5 7.5h15M4.5 12h15M4.5 16.5h15" /></>,
  'panel-left': <><rect x="4.5" y="5" width="15" height="14" rx="1.5" /><path d="M10 5v14M7 10h.01M7 14h.01" /></>,
  search: <><circle cx="10.5" cy="10.5" r="5.5" /><path d="m15 15 4 4" /></>,
  plus: <path d="M12 5v14M5 12h14" />,
  close: <path d="m6 6 12 12M18 6 6 18" />,
}

/** A small, dependency-free icon primitive. All shapes deliberately inherit currentColor. */
export function Icon({ name, title, ...props }: IconProps) {
  return <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round" aria-hidden={title ? undefined : true} role={title ? 'img' : undefined} focusable="false" {...props}>
    {title && <title>{title}</title>}
    {paths[name]}
  </svg>
}
