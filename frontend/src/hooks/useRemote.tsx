import { useCallback, useEffect, useRef, useState } from 'react'
import { recordReload, type ReloadReason } from '../network-debug'

export const failure = (error: unknown) => error instanceof Error ? error.message : 'Не удалось выполнить запрос. Попробуйте ещё раз.'

export interface Remote<T> { data?: T; error: string; loading: boolean; reload: (reason?: ReloadReason) => Promise<void> }

/** Abortable, sequence-guarded resource loader with optional polling and merge. */
export function useRemote<T>(load: (signal: AbortSignal) => Promise<T>, interval = 0, resource = 'chat.resource', merge?: (previous: T | undefined, next: T) => T) {
  const [data, setData] = useState<T>()
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const controller = useRef<AbortController | null>(null)
  const sequence = useRef(0)
  const reload = useCallback(async (reason: ReloadReason = 'manual_retry') => {
    recordReload(resource, reason)
    const request = ++sequence.current
    controller.current?.abort()
    const next = new AbortController()
    controller.current = next
    setLoading(true)
    try {
      const result = await load(next.signal)
      if (next.signal.aborted || request !== sequence.current) return
      setData(merge ? (previous) => merge(previous, result) : result)
      setError('')
    } catch (caught) {
      if (!next.signal.aborted && request === sequence.current) setError(failure(caught))
    } finally {
      if (!next.signal.aborted && request === sequence.current) setLoading(false)
    }
  }, [load, merge, resource])
  const updateData = useCallback<typeof setData>((value) => {
    controller.current?.abort()
    sequence.current++
    setData(value)
    setLoading(false)
    setError('')
  }, [])
  useEffect(() => {
    setData(undefined)
    setError('')
    void reload('initial')
    const timer = interval ? window.setInterval(() => void reload('fallback_poll'), interval) : undefined
    return () => { window.clearInterval(timer); controller.current?.abort(); sequence.current++ }
  }, [reload, interval])
  return { data, setData: updateData, error, loading, reload }
}

export function ResourceStatus({ resource, empty }: { resource: { data: unknown; error: string; loading: boolean; reload: () => Promise<void> }; empty?: boolean }) {
  if (resource.error) return <div className="chat-resource-error" role="status"><p>{resource.error}{resource.data !== undefined ? ' Показаны последние загруженные данные.' : ''}</p><button type="button" onClick={() => void resource.reload()}>Повторить загрузку</button></div>
  if (resource.loading && resource.data === undefined) return <p className="chat-muted" role="status">Загрузка…</p>
  if (empty) return <p className="chat-muted">Пока нет данных.</p>
  return null
}
