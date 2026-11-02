import { type ReactNode, useEffect, useId, useRef, useState } from 'react'
import CommandPalette from './CommandPalette'
import { Icon, type IconName } from './icons'
import './styles/app-shell.css'

export type AppSection = 'chat' | 'projects' | 'tasks' | 'memory'

type AppShellProps = {
  children: ReactNode
  /** Optional route-specific content rendered below the shared navigation. */
  sidebar?: ReactNode
  /** Optional compact route navigation for the mobile drawer. */
  mobileSidebar?: ReactNode
  activeSection?: AppSection
  onNewChat?: () => void
  onMemory?: () => void
  composerFocus?: () => void
}

type NavItem = { id: AppSection; label: string; href: string; icon: IconName }

const navigation: NavItem[] = [
  { id: 'chat', label: 'Чат', href: '#/chat', icon: 'chat' },
  { id: 'projects', label: 'Проекты', href: '#/spaces', icon: 'projects' },
  { id: 'tasks', label: 'Задачи', href: '#/workspace', icon: 'tasks' },
  { id: 'memory', label: 'Память', href: '#/memory', icon: 'memory' },
]

const SIDEBAR_KEY = 'qwenchik.sidebar.collapsed'

function readCollapsed() {
  try { return window.localStorage.getItem(SIDEBAR_KEY) === 'true' } catch { return false }
}

function Navigation({ activeSection, onNavigate }: { activeSection?: AppSection; onNavigate?: () => void }) {
  return <nav className="q-app-shell__nav" aria-label="Основная навигация">
    {navigation.map((item) => <a key={item.id} className="q-app-shell__nav-link" data-active={item.id === activeSection || undefined} href={item.href} aria-label={item.id === 'chat' ? 'Открыть чаты' : undefined} aria-current={item.id === activeSection ? 'page' : undefined} onClick={onNavigate}>
      <Icon name={item.icon} /><span>{item.label}</span>
    </a>)}
  </nav>
}

/**
 * Shared, presentation-only application frame. It deliberately owns no route, API,
 * SSE, or task state, so existing pages can migrate into it independently.
 */
export default function AppShell({ children, sidebar, mobileSidebar, activeSection, onNewChat, onMemory, composerFocus }: AppShellProps) {
  const [collapsed, setCollapsed] = useState(readCollapsed)
  const mobileDialogRef = useRef<HTMLDialogElement>(null)
  const statusDialogRef = useRef<HTMLDialogElement>(null)
  const mobileDialogTitle = useId()

  useEffect(() => {
    try { window.localStorage.setItem(SIDEBAR_KEY, String(collapsed)) } catch { /* Private mode can deny storage. */ }
  }, [collapsed])

  const openDialog = (dialog: HTMLDialogElement | null) => { if (dialog && !dialog.open) dialog.showModal() }
  const closeMobileNavigation = () => mobileDialogRef.current?.close()

  return <div className="q-app-shell" data-collapsed={collapsed || undefined}>
    <aside className="q-app-shell__sidebar" aria-label="Qwenchik">
      <div className="q-app-shell__sidebar-top">
        <a className="q-app-shell__brand" href="#/chat" aria-label="Qwenchik, открыть чат"><Icon name="brand" /><span>Qwenchik</span></a>
        <button className="q-icon-button q-app-shell__collapse" type="button" onClick={() => setCollapsed((value) => !value)} aria-label={collapsed ? 'Развернуть боковую панель' : 'Свернуть боковую панель'} aria-pressed={collapsed}><Icon name="panel-left" /></button>
      </div>
      {onNewChat && <button className="q-app-shell__new-chat" type="button" onClick={onNewChat}><Icon name="plus" /><span>Новый чат</span></button>}
      <Navigation activeSection={activeSection} />
      {sidebar && <div className="q-app-shell__route-sidebar">{sidebar}</div>}
      <div className="q-app-shell__sidebar-bottom">
        <button className="q-app-shell__status" type="button" onClick={() => openDialog(statusDialogRef.current)}><span className="q-status-dot" aria-hidden="true" /><span>Статус системы</span></button>
        {onMemory && <button className="q-app-shell__status" type="button" onClick={onMemory}><Icon name="memory" /><span>Память</span></button>}
        <a className="q-app-shell__nav-link" href="#/settings"><Icon name="settings" /><span>Настройки</span></a>
      </div>
    </aside>
    <header className="q-app-shell__mobile-bar">
      <button className="q-icon-button" type="button" onClick={() => openDialog(mobileDialogRef.current)} aria-label="Открыть навигацию"><Icon name="menu" /></button>
      <a className="q-app-shell__mobile-brand" href="#/chat"><Icon name="brand" /><span>Qwenchik</span></a>
      <CommandPalette onFocusComposer={composerFocus} />
    </header>
    <div className="q-app-shell__content"><div className="q-app-shell__desktop-tools"><CommandPalette onFocusComposer={composerFocus} /></div>{children}</div>
    <dialog ref={mobileDialogRef} className="q-app-shell__mobile-dialog" aria-labelledby={mobileDialogTitle} onClick={(event) => { const target = event.target as HTMLElement; if (event.target === event.currentTarget || (target.closest('a,button') && !target.closest('.q-icon-button'))) closeMobileNavigation() }}>
      <section><header><div><p className="q-dialog-eyebrow">Навигация</p><h2 id={mobileDialogTitle}>Qwenchik</h2></div><button className="q-icon-button" type="button" onClick={closeMobileNavigation} aria-label="Закрыть навигацию"><Icon name="close" /></button></header><Navigation activeSection={activeSection} onNavigate={closeMobileNavigation} />{onNewChat && <button className="q-app-shell__new-chat" type="button" onClick={() => { onNewChat(); closeMobileNavigation() }}><Icon name="plus" />Новый чат</button>}{onMemory && <button className="q-app-shell__status" type="button" onClick={() => { onMemory(); closeMobileNavigation() }}><Icon name="memory" /><span>Память</span></button>}{mobileSidebar && <div className="q-app-shell__mobile-route-sidebar">{mobileSidebar}</div>}<a className="q-app-shell__nav-link" href="#/settings" onClick={closeMobileNavigation}><Icon name="settings" /><span>Настройки</span></a></section>
    </dialog>
    <dialog ref={statusDialogRef} className="q-app-shell__status-dialog" aria-labelledby="q-status-dialog-title" onClick={(event) => { if (event.target === event.currentTarget) statusDialogRef.current?.close() }}>
      <section><header><div><p className="q-dialog-eyebrow">Локальная среда</p><h2 id="q-status-dialog-title">Статус системы</h2></div><button className="q-icon-button" type="button" onClick={() => statusDialogRef.current?.close()} aria-label="Закрыть статус"><Icon name="close" /></button></header><p>Подключите данные сервиса на уровне маршрута. Оболочка не делает сетевых запросов и не меняет состояние задач.</p></section>
    </dialog>
  </div>
}
