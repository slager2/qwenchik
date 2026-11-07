import test from 'node:test'
import assert from 'node:assert/strict'
import { filenameFromDisposition, obsidianExportMessage, reportObsidianURL, reportPDFAvailability, reportPDFEligible, reportPDFURL } from './report-pdf.mjs'

const answer = { role: 'assistant', run_id: 'run-1', content: '# Отчёт', status: 'completed', run_mode: 'research' }

test('PDF export is offered for finished research answers only', () => {
  assert.equal(reportPDFEligible(answer, null), true)
  assert.equal(reportPDFEligible({ ...answer, status: 'partial' }, null), true)
  assert.equal(reportPDFEligible({ ...answer, run_mode: 'chat' }, null), false)
  assert.equal(reportPDFEligible({ ...answer, role: 'user' }, null), false)
  assert.equal(reportPDFEligible({ ...answer, content: '  ' }, null), false)
  assert.equal(reportPDFEligible({ ...answer, status: 'streaming' }, null), false)
  // Live run state wins for the current run.
  assert.equal(reportPDFEligible({ ...answer, run_mode: undefined, status: 'streaming' }, { id: 'run-1', mode: 'research', status: 'completed' }), true)
  assert.equal(reportPDFEligible(answer, { id: 'run-1', mode: 'research', status: 'running' }), false)
  assert.equal(reportPDFEligible(answer, { id: 'run-1', mode: 'research', status: 'failed' }), false)
  assert.equal(reportPDFEligible(answer, { id: 'other', mode: 'chat', status: 'running' }), true)
})

test('availability follows health and treats an unknown state as allowed', () => {
  assert.equal(reportPDFAvailability({ report_pdf: { available: false } }).disabled, true)
  assert.match(reportPDFAvailability({ report_pdf: { available: false } }).reason, /Chrome/)
  assert.equal(reportPDFAvailability({ report_pdf: { available: true } }).disabled, false)
  assert.equal(reportPDFAvailability(undefined).disabled, false)
})

test('download URL and file name parsing', () => {
  assert.equal(reportPDFURL('a/b'), '/api/runs/a%2Fb/report.pdf')
  assert.equal(filenameFromDisposition("attachment; filename*=utf-8''%D0%9E%D1%82%D1%87%D1%91%D1%82.pdf"), 'Отчёт.pdf')
  assert.equal(filenameFromDisposition('attachment; filename="report.pdf"'), 'report.pdf')
  assert.equal(filenameFromDisposition("attachment; filename*=utf-8''..%2F..%2Fevil.pdf"), '.. .. evil.pdf')
  assert.equal(filenameFromDisposition(null), 'Отчёт исследования.pdf')
})

test('Obsidian export status names the folder and the note count', () => {
  assert.equal(reportObsidianURL('a/b'), '/api/runs/a%2Fb/report/obsidian')
  assert.equal(obsidianExportMessage({ path: '/home/u/Документы/Qwenchik/2026-11-07 Гранты', notes: 6 }), 'Сохранено 6 заметок: 2026-11-07 Гранты')
  assert.equal(obsidianExportMessage({ path: 'C:\\Docs\\Qwenchik\\R', notes: 2 }), 'Сохранено 2 заметки: R')
  assert.equal(obsidianExportMessage({ path: '/x/One', notes: 21 }), 'Сохранено 21 заметка: One')
})
