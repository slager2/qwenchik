import { useState } from 'react'
import { Icon } from './icons'
import { Button, Tooltip } from './ui'
import { obsidianExportMessage, reportObsidianURL } from './report-pdf.mjs'

/**
 * Writes the finished research report as an Obsidian vault folder
 * (00 MOC + one note per section + sources) into the documents folder and
 * shows where it went.
 */
export default function ReportObsidianButton({ runID, size = 'sm' }: { runID: string; size?: 'sm' | 'md' }) {
  const [state, setState] = useState<{ loading: boolean; message: string; error: boolean }>({ loading: false, message: '', error: false })
  const run = async () => {
    if (state.loading) return
    setState({ loading: true, message: '', error: false })
    try {
      const response = await fetch(reportObsidianURL(runID), { method: 'POST', headers: { 'Content-Type': 'application/json', Accept: 'application/json' } })
      let body: { path?: string; notes?: number; error?: { message?: string } } | null = null
      try { body = await response.json() } catch { body = null }
      if (!response.ok) {
        setState({ loading: false, message: body?.error?.message || `Экспорт не выполнен (код ${response.status})`, error: true })
        return
      }
      setState({ loading: false, message: obsidianExportMessage(body), error: false })
    } catch {
      setState({ loading: false, message: 'Экспорт не выполнен: нет связи с приложением.', error: true })
    }
  }
  return <span className="report-pdf-action">
    <Tooltip content={state.loading ? 'Сохраняем заметки…' : 'Сохранить отчёт как папку заметок Obsidian (MOC, разделы, источники) в «Документы/Qwenchik»'}>
      <Button variant="ghost" size={size} icon={<Icon name="external" size={16} />} loading={state.loading} onClick={() => void run()}>Экспорт в Obsidian</Button>
    </Tooltip>
    {state.message && <span className={state.error ? 'chat-muted report-pdf-error' : 'chat-muted report-export-done'} role="status" title={state.message}>{state.message}</span>}
  </span>
}
