const terminalMessage = new Set(['completed', 'partial'])
const terminalRun = new Set(['completed', 'failed', 'cancelled'])

/** URL of the server-rendered PDF for a research run. */
export function reportPDFURL(runID) {
  return `/api/runs/${encodeURIComponent(runID)}/report.pdf`
}

/**
 * Whether an assistant message is a finished research report that can be
 * exported. The current run's live state wins over the stored message mode.
 */
export function reportPDFEligible(message, run) {
  if (!message || message.role !== 'assistant' || !message.run_id || !message.content?.trim()) return false
  if (run && run.id === message.run_id) return run.mode === 'research' && terminalRun.has(run.status) && run.status !== 'failed'
  return message.run_mode === 'research' && terminalMessage.has(message.status)
}

/** Button state from /api/health report_pdf availability (unknown = allowed). */
export function reportPDFAvailability(health) {
  const available = health?.report_pdf?.available
  if (available === false) return { disabled: true, reason: 'PDF недоступен: на компьютере не найден Chrome или Chromium. Укажите PDF_RENDERER_BIN.' }
  return { disabled: false, reason: 'Скачать отчёт в PDF' }
}

/** File name from a Content-Disposition header (RFC 5987 filename* first). */
export function filenameFromDisposition(header, fallback = 'Отчёт исследования.pdf') {
  if (!header) return fallback
  const extended = /filename\*\s*=\s*([^']*)'[^']*'([^;]+)/i.exec(header)
  if (extended) {
    try { return sanitize(decodeURIComponent(extended[2].trim())) || fallback } catch { /* fall through */ }
  }
  const plain = /filename\s*=\s*("([^"]*)"|[^;]+)/i.exec(header)
  if (plain) return sanitize((plain[2] ?? plain[1]).trim()) || fallback
  return fallback
}

function sanitize(name) {
  return name.replace(/[\\/\u0000-\u001f]/g, ' ').trim()
}
