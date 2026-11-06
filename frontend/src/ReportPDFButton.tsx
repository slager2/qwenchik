import { useState } from 'react'
import { Icon } from './icons'
import { Button, Tooltip } from './ui'
import { filenameFromDisposition, reportPDFURL } from './report-pdf.mjs'

/**
 * Downloads the server-rendered PDF of a finished research report. Rendering
 * can take a few seconds the first time, so the button shows progress and
 * reports server errors inline instead of navigating away.
 */
export default function ReportPDFButton({ runID, disabled, reason, size = 'sm' }: { runID: string; disabled?: boolean; reason: string; size?: 'sm' | 'md' }) {
  const [state, setState] = useState<{ loading: boolean; error: string }>({ loading: false, error: '' })
  const download = async () => {
    if (state.loading || disabled) return
    setState({ loading: true, error: '' })
    try {
      const response = await fetch(reportPDFURL(runID), { headers: { Accept: 'application/pdf' } })
      if (!response.ok) {
        let message = `PDF не подготовлен (код ${response.status})`
        try { message = (await response.json())?.error?.message || message } catch { /* keep status message */ }
        setState({ loading: false, error: message })
        return
      }
      const blob = await response.blob()
      const url = URL.createObjectURL(blob)
      const link = document.createElement('a')
      link.href = url
      link.download = filenameFromDisposition(response.headers.get('Content-Disposition'))
      document.body.append(link)
      link.click()
      link.remove()
      window.setTimeout(() => URL.revokeObjectURL(url), 30_000)
      setState({ loading: false, error: '' })
    } catch {
      setState({ loading: false, error: 'PDF не скачан: нет связи с приложением.' })
    }
  }
  return <span className="report-pdf-action">
    <Tooltip content={state.loading ? 'Готовим PDF…' : reason}>
      <Button variant="ghost" size={size} icon={<Icon name="download" size={16} />} loading={state.loading} disabled={disabled} onClick={() => void download()}>Скачать PDF</Button>
    </Tooltip>
    {state.error && <span className="chat-muted report-pdf-error" role="status">{state.error}</span>}
  </span>
}
