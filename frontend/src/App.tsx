import { Component, lazy, Suspense, useEffect, useState, type ReactNode } from 'react'
import ChatRoute from './ChatRoute'
import { Button, Spinner } from './ui'

const WorkspaceRoute = lazy(() => import('./WorkspaceRoute'))
const SpacesRoute = lazy(() => import('./SpacesRoute'))

class WorkspaceLoadBoundary extends Component<{ children: ReactNode }, { failed: boolean }> {
  state = { failed: false }
  static getDerivedStateFromError() { return { failed: true } }
  render() {
    if (this.state.failed) return <main className="route-loading" role="alert"><p>Не удалось загрузить раздел.</p><Button variant="secondary" onClick={() => window.location.reload()}>Перезагрузить интерфейс</Button></main>
    return this.props.children
  }
}

function RouteLoading() {
  return <main className="route-loading" role="status" aria-live="polite"><Spinner label="Загрузка" /></main>
}

export default function App() {
  const [route, setRoute] = useState(() => window.location.hash)

  useEffect(() => {
    const navigate = () => setRoute(window.location.hash)
    window.addEventListener('hashchange', navigate)
    return () => window.removeEventListener('hashchange', navigate)
  }, [])

  if (route.startsWith('#/spaces')) return <WorkspaceLoadBoundary><Suspense fallback={<RouteLoading />}><SpacesRoute /></Suspense></WorkspaceLoadBoundary>
  if (!route.startsWith('#/workspace')) return <ChatRoute />

  return <WorkspaceLoadBoundary><Suspense fallback={<RouteLoading />}><WorkspaceRoute /></Suspense></WorkspaceLoadBoundary>
}
