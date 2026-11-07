import { spawn } from 'node:child_process'
import { mkdir, readFile } from 'node:fs/promises'
import { join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { chromium } from 'playwright-core'

// Captures a rendered research report (tables, citations) in light/dark and
// desktop/mobile. Every /api request is answered from fixtures; the report
// Markdown comes from REPORT_MD (default: the Obsidian export test fixture,
// rendered by the orchestrator report renderer).
const root = fileURLToPath(new URL('..', import.meta.url))
const output = resolve(process.env.SHOTS_DIR || join(root, '..', '..', 'report-shots'))
const markdownPath = resolve(process.env.REPORT_MD || join(root, '..', 'internal', 'report', 'obsidian', 'testdata', 'report.md'))
const port = 4177
const base = `http://127.0.0.1:${port}`
await mkdir(output, { recursive: true })

const now = new Date('2026-11-07T10:00:00Z').toISOString()
const report = await readFile(markdownPath, 'utf8')
const conversation = { id: 'k', title: 'Гранты на IT-бакалавриат', mode: 'research', created_at: now, updated_at: now, latest_run_id: 'run-k' }
const message = (id, role, content) => ({ id, conversation_id: 'k', run_id: 'run-k', run_mode: 'research', role, content, status: 'completed', created_at: now, updated_at: now, last_event_seq: 0 })
const messages = [message('u-k', 'user', 'Куда гражданин Казахстана может поступить на IT-бакалавриат на полный грант?'), message('m-k', 'assistant', report)]
const run = { id: 'run-k', conversation_id: 'k', user_message_id: 'u-k', assistant_message_id: 'm-k', mode: 'research', operation: 'message', status: 'completed', retry_count: 0 }
const sources = [
  ['3c757d00-1032-43dc-8b10-547adc82b579', 'https://stipendiumhungaricum.hu/apply/', 'Stipendium Hungaricum — Application 2026/2027'],
  ['8e37407c-9eec-4764-a66e-0680229bba13', 'https://www.turkiyeburslari.gov.tr/en/page/prospective-students/scholarship-coverage', 'Türkiye Scholarships — coverage'],
  ['1be322ed-9841-423c-ba3a-04965fc464fb', 'https://www.studyinkorea.go.kr/gks/2027-undergraduate.pdf', 'GKS 2027 Undergraduate Guidelines'],
  ['5c766154-6377-4f72-bd5d-e45213895e2b', 'https://www.gov.kz/memleket/entities/edu/press/news/details/sh-2025', 'Stipendium Hungaricum для Казахстана 2025-2026'],
]
const evidence = sources.map(([id, url, title], index) => ({ id, task_id: 'run-k', provider: 'searxng', original_query: '', original_url: url, canonical_url: url, title, language: 'ru', retrieved_at: now, http_status: 200, content_sha256: String(index).repeat(64), extraction_method: 'html', excerpt: title }))

async function api(route) {
  const request = route.request()
  const url = new URL(request.url())
  const [, resource, id, sub, extra] = url.pathname.split('/').filter(Boolean)
  const json = (body, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) })
  if (request.method() !== 'GET') return json({ error: { message: 'Только просмотр' } }, 405)
  if (resource === 'health') return json({ status: 'ok', database: 'ok', model: { id: 'qwen3.5-9b', status: 'configured' }, worker: 'attached', report_pdf: { available: true } })
  if (resource === 'lab') return json({ research_limits: { configured: true, privacy_mode: 'local_inference_external_search' } })
  if (resource === 'conversations') {
    if (!id) return json([conversation])
    if (sub === 'messages') return json(messages)
    return json(conversation)
  }
  if (resource === 'runs') {
    if (!sub) return json(run)
    if (sub === 'events' && extra === 'stream') return route.fulfill({ status: 200, contentType: 'text/event-stream', body: 'retry: 600000\n\n' })
    if (sub === 'evidence') return json(evidence)
    if (sub === 'runtime') return json({ task_id: id, cursor: 1, status: 'completed', profile: 'standard', counters: {}, budget: {}, research_report: { outcome: 'partial', sections: 3 } })
    return json([])
  }
  return json([])
}

const server = spawn(process.execPath, ['node_modules/vite/bin/vite.js', '--host', '127.0.0.1', '--port', String(port), '--strictPort'], { cwd: root, stdio: 'pipe' })
let browser
try {
  for (let attempt = 0; ; attempt++) {
    try { if ((await fetch(base)).ok) break } catch { /* starting */ }
    if (attempt > 100) throw new Error('Vite did not start')
    await new Promise((done) => setTimeout(done, 200))
  }
  browser = await chromium.launch({ executablePath: process.env.CHROME_BIN || '/usr/bin/google-chrome', headless: true, args: ['--no-sandbox'] })
  for (const theme of ['light', 'dark']) for (const view of [{ name: 'desktop', width: 1440, height: 900 }, { name: 'mobile', width: 390, height: 844 }]) {
    const context = await browser.newContext({ viewport: view, deviceScaleFactor: 1, locale: 'ru-RU', timezoneId: 'Europe/Moscow', colorScheme: theme, reducedMotion: 'reduce' })
    await context.addInitScript((value) => { localStorage.setItem('qwenchik.theme', value) }, theme)
    await context.route('**/api/**', api)
    const page = await context.newPage()
    page.on('pageerror', (error) => console.error(`${view.name}: ${error.message}`))
    await page.goto(`${base}/#/chat/k`)
    await page.waitForTimeout(1200)
    await page.evaluate(() => document.fonts.ready)
    const answer = page.locator('.chat-markdown').last()
    for (const [index, table] of (await answer.locator('.chat-table-scroll').all()).slice(0, 3).entries()) {
      await table.scrollIntoViewIfNeeded()
      const file = join(output, `report-table${index + 1}-${view.name}-${theme}.png`)
      await table.screenshot({ path: file })
      console.log(file)
    }
    await answer.locator('h2').first().scrollIntoViewIfNeeded()
    const top = join(output, `report-top-${view.name}-${theme}.png`)
    await page.screenshot({ path: top })
    console.log(top)
    const actions = page.locator('.chat-message-actions').last()
    await actions.scrollIntoViewIfNeeded()
    const bottom = join(output, `report-actions-${view.name}-${theme}.png`)
    await page.screenshot({ path: bottom })
    console.log(bottom)
    await context.close()
  }
} finally {
  await browser?.close()
  server.kill()
}
