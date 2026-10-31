import { Component, lazy, Suspense, useEffect, useState, type ReactNode } from 'react'
import ChatRoute from './ChatRoute'
import SpacesRoute from './SpacesRoute'

const WorkspaceRoute = lazy(() => import('./WorkspaceRoute'))

class WorkspaceLoadBoundary extends Component<{ children: ReactNode }, { failed: boolean }> {
  state = { failed: false }
  static getDerivedStateFromError() { return { failed: true } }
  render() {
    if (this.state.failed) return <main className="workspace-loading" role="alert"><p>Не удалось загрузить рабочее пространство.</p><button type="button" onClick={() => window.location.reload()}>Перезагрузить интерфейс</button></main>
    return this.props.children
  }
}

export default function App() {
  const [route, setRoute] = useState(() => window.location.hash)

  useEffect(() => {
    const navigate = () => setRoute(window.location.hash)
    window.addEventListener('hashchange', navigate)
    return () => window.removeEventListener('hashchange', navigate)
  }, [])

  if (route.startsWith('#/spaces')) return <SpacesRoute />
  if (!route.startsWith('#/workspace')) return <ChatRoute />

  return <WorkspaceLoadBoundary><Suspense fallback={<main className="workspace-loading" role="status" aria-live="polite">Загрузка рабочего пространства…</main>}><WorkspaceRoute /></Suspense></WorkspaceLoadBoundary>
}
