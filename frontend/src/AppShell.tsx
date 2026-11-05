import { createContext, useCallback, useContext, useEffect, useId, useRef, useState, type MouseEvent, type ReactNode } from 'react'
import CommandPalette from './CommandPalette'
import { Icon, type IconName } from './icons'
import { cx, Drawer, IconButton } from './ui'
import './styles/shell.css'

export type AppSection = 'chat' | 'projects' | 'tasks' | 'memory'

type AppShellProps = {
  children: ReactNode
  /** Route-specific content rendered below the shared navigation (e.g. conversations). */
  sidebar?: ReactNode
  /** Route content for the mobile navigation sheet; defaults to `sidebar`. */
  mobileSidebar?: ReactNode
  activeSection?: AppSection
  onNewChat?: () => void
  onMemory?: () => void
  commandPalette?: boolean
  composerFocus?: () => void
  /** Title of the mobile navigation sheet and the trigger's accessible name. */
  navigationTitle?: string
  navigationLabel?: string
}

type NavItem = { id: AppSection; label: string; href: string; icon: IconName; aria?: string }

const navigation: NavItem[] = [
  { id: 'chat', label: 'Чат', href: '#/chat', icon: 'chat', aria: 'Открыть чаты' },
  { id: 'projects', label: 'Проекты', href: '#/spaces', icon: 'projects' },
  { id: 'tasks', label: 'Задачи', href: '#/workspace', icon: 'tasks', aria: 'Рабочее пространство' },
]

const SIDEBAR_KEY = 'qwenchik.sidebar.collapsed'
const THEME_KEY = 'qwenchik.theme'
const TEXT_SCALE_KEY = 'qwenchik.text-scale'

type Theme = 'system' | 'light' | 'dark'
type TextScale = 'default' | 'large'

function readCollapsed() {
  try { return window.localStorage.getItem(SIDEBAR_KEY) === 'true' } catch { return false }
}

function readTheme(): Theme {
  try {
    const value = window.localStorage.getItem(THEME_KEY)
    return value === 'light' || value === 'dark' ? value : 'system'
  } catch { return 'system' }
}

function readTextScale(): TextScale {
  try { return window.localStorage.getItem(TEXT_SCALE_KEY) === 'large' ? 'large' : 'default' } catch { return 'default' }
}

function systemDark() {
  return typeof window.matchMedia === 'function' && window.matchMedia('(prefers-color-scheme: dark)').matches
}

const ShellContext = createContext<{ openNavigation: () => void; navigationLabel: string }>({ openNavigation: () => {}, navigationLabel: 'Открыть навигацию' })

/** Route top bar. Renders the mobile navigation trigger, a title area and quiet actions. */
export function TopBar({ title, subtitle, actions, className, children }: { title?: ReactNode; subtitle?: ReactNode; actions?: ReactNode; className?: string; children?: ReactNode }) {
  const shell = useContext(ShellContext)
  return <header className={cx('shell-topbar', className)}>
    <IconButton className="shell-topbar__menu" label={shell.navigationLabel} icon={<Icon name="menu" />} onClick={shell.openNavigation} />
    <div className="shell-topbar__title">{children ?? <>{title && <h1>{title}</h1>}{subtitle && <p>{subtitle}</p>}</>}</div>
    {actions && <div className="shell-topbar__actions">{actions}</div>}
  </header>
}

function Navigation({ activeSection }: { activeSection?: AppSection }) {
  return <nav className="shell-nav" aria-label="Основная навигация">
    {navigation.map((item) => <a key={item.id} className="shell-nav__link" href={item.href} aria-label={item.aria} title={item.label} aria-current={item.id === activeSection ? 'page' : undefined}>
      <Icon name={item.icon} /><span>{item.label}</span>
    </a>)}
  </nav>
}

/**
 * Application frame: collapsible sidebar, mobile navigation sheet and
 * appearance preferences. It owns no route, API, SSE or task state.
 */
export default function AppShell({ children, sidebar, mobileSidebar, activeSection, onNewChat, onMemory, commandPalette = true, composerFocus, navigationTitle = 'Навигация', navigationLabel = 'Открыть навигацию' }: AppShellProps) {
  const [collapsed, setCollapsed] = useState(readCollapsed)
  const [theme, setTheme] = useState<Theme>(readTheme)
  const [textScale, setTextScale] = useState<TextScale>(readTextScale)
  const [navOpen, setNavOpen] = useState(false)
  const preferencesRef = useRef<HTMLDialogElement>(null)
  const preferencesTitle = useId()

  useEffect(() => {
    try { window.localStorage.setItem(SIDEBAR_KEY, String(collapsed)) } catch { /* Private mode can deny storage. */ }
  }, [collapsed])

  useEffect(() => {
    document.documentElement.dataset.theme = theme === 'system' ? '' : theme
    try { window.localStorage.setItem(THEME_KEY, theme) } catch { /* Private mode can deny storage. */ }
  }, [theme])

  useEffect(() => {
    document.documentElement.dataset.textScale = textScale === 'default' ? '' : textScale
    try { window.localStorage.setItem(TEXT_SCALE_KEY, textScale) } catch { /* Private mode can deny storage. */ }
  }, [textScale])

  useEffect(() => {
    const close = () => setNavOpen(false)
    window.addEventListener('hashchange', close)
    return () => window.removeEventListener('hashchange', close)
  }, [])

  const openNavigation = useCallback(() => setNavOpen(true), [])
  const dark = theme === 'dark' || (theme === 'system' && systemDark())
  const toggleTheme = () => setTheme(dark ? 'light' : 'dark')
  const themeLabel = dark ? 'Светлая тема' : 'Тёмная тема'
  const openPreferences = () => { const dialog = preferencesRef.current; if (dialog && !dialog.open) dialog.showModal() }
  const closeOnNavigate = (event: MouseEvent) => { if ((event.target as HTMLElement).closest('a[href], [data-close-navigation]')) setNavOpen(false) }

  const newChat = onNewChat
    ? <button className="shell-nav__link shell-new-chat" type="button" onClick={onNewChat} title="Новый чат" data-close-navigation><Icon name="edit" /><span>Новый чат</span></button>
    : <a className="shell-nav__link shell-new-chat" href="#/chat" title="Новый чат"><Icon name="edit" /><span>Новый чат</span></a>

  const footer = <div className="shell-sidebar__footer">
    {commandPalette && <button className="shell-nav__link" type="button" title="Команды" onClick={() => window.dispatchEvent(new Event('qwenchik:open-command-palette'))} data-close-navigation><Icon name="search" /><span>Команды</span><kbd className="ui-kbd">⌘K</kbd></button>}
    {onMemory && <button className="shell-nav__link" type="button" title="Память" onClick={onMemory} data-close-navigation><Icon name="memory" /><span>Память</span></button>}
    <button className="shell-nav__link" type="button" onClick={toggleTheme} aria-label="Переключить светлую и тёмную тему" title={themeLabel}><Icon name={dark ? 'sun' : 'moon'} /><span>{themeLabel}</span></button>
    <button className="shell-nav__link" type="button" onClick={openPreferences} title="Настройки" data-close-navigation><Icon name="settings" /><span>Настройки</span></button>
  </div>

  return <ShellContext.Provider value={{ openNavigation, navigationLabel }}>
    <div className="shell" data-collapsed={collapsed || undefined}>
      <aside className="shell-sidebar" aria-label="Qwenchik">
        <div className="shell-sidebar__top">
          <a className="shell-brand" href="#/chat" aria-label="Qwenchik, открыть чат"><span className="shell-brand__mark" aria-hidden="true">Q</span><span>Qwenchik</span></a>
          <IconButton className="shell-sidebar__collapse" label={collapsed ? 'Развернуть боковую панель' : 'Свернуть боковую панель'} aria-pressed={collapsed} icon={<Icon name="panel-left" />} onClick={() => setCollapsed((value) => !value)} />
        </div>
        <div className="shell-sidebar__primary">{newChat}<Navigation activeSection={activeSection} /></div>
        {sidebar && <div className="shell-sidebar__route">{sidebar}</div>}
        {footer}
      </aside>
      <div className="shell-main">
        {commandPalette && <CommandPalette onFocusComposer={composerFocus} />}
        {children}
      </div>
      {navOpen && <Drawer title={navigationTitle} side="left" className="shell-drawer" onClose={() => setNavOpen(false)}>
        <div className="shell-drawer__content" onClick={closeOnNavigate}>
          <div className="shell-sidebar__primary">{newChat}<Navigation activeSection={activeSection} /></div>
          {(mobileSidebar ?? sidebar) && <div className="shell-sidebar__route">{mobileSidebar ?? sidebar}</div>}
          {footer}
        </div>
      </Drawer>}
      <dialog ref={preferencesRef} className="shell-preferences" aria-labelledby={preferencesTitle} onClick={(event) => { if (event.target === event.currentTarget) preferencesRef.current?.close() }}>
        <section>
          <header><h2 id={preferencesTitle}>Настройки</h2><IconButton label="Закрыть настройки" icon={<Icon name="close" />} onClick={() => preferencesRef.current?.close()} /></header>
          <fieldset><legend>Тема</legend><div className="shell-segmented" role="group" aria-label="Тема оформления">
            {(['system', 'light', 'dark'] as Theme[]).map((value) => <button key={value} type="button" aria-pressed={theme === value} onClick={() => setTheme(value)}>{value === 'system' ? 'Системная' : value === 'light' ? 'Светлая' : 'Тёмная'}</button>)}
          </div></fieldset>
          <fieldset><legend>Размер текста</legend><div className="shell-segmented" role="group" aria-label="Размер текста">
            <button type="button" aria-pressed={textScale === 'default'} onClick={() => setTextScale('default')}>Обычный</button>
            <button type="button" aria-pressed={textScale === 'large'} onClick={() => setTextScale('large')}>Увеличенный</button>
          </div></fieldset>
          <p className="ui-muted">Настройки оформления хранятся только в этом браузере.</p>
        </section>
      </dialog>
    </div>
  </ShellContext.Provider>
}
