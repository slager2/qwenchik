import assert from 'node:assert/strict'
import { mkdir, lstat, writeFile } from 'node:fs/promises'
import { join, dirname, resolve } from 'node:path'
import { execFileSync } from 'node:child_process'
import { chromium } from 'playwright-core'

// Real served UI/API only, including production preview with the real API proxy.
// No API fulfillment, generation, or cleanup mutations.
const label = process.env.PHASE5_LABEL
assert.ok(['before', 'after'].includes(label), 'Set literal PHASE5_LABEL=before or PHASE5_LABEL=after')
const base = new URL(process.env.BASE_URL || 'http://127.0.0.1:8080')
const referenceRevision = process.env.PHASE5_REFERENCE_REVISION || ''
assert.ok(!referenceRevision || label === 'before' && referenceRevision === '7e0901c', 'Only the fixed pre-Phase-5 revision can be a reconstructed reference')
assert.ok(base.protocol === 'http:' && ['127.0.0.1', 'localhost', '[::1]'].includes(base.hostname), 'BASE_URL must be loopback HTTP')
assert.ok(['8080', '4175', '4176', '4177', '4178', '5173'].includes(base.port), 'BASE_URL must use an approved loopback preview/application port')
assert.ok(!base.username && !base.password && base.pathname === '/' && !base.search && !base.hash, 'BASE_URL must be an application origin')
if (label === 'before') assert.equal(base.port, referenceRevision ? '4178' : '8080', 'Before uses the original deployment or the explicitly identified historical-asset reference')
const output = resolve(process.env.PHASE5_OUTPUT || `/tmp/opencode/phase5-${label}`)
assert.equal(dirname(output), '/tmp/opencode', 'Output must be a direct child of /tmp/opencode')
assert.ok((await lstat(dirname(output))).isDirectory(), 'Output parent must already exist')
await mkdir(output) // Exclusive directory: never overwrite a previous capture.

const cases = [
  { name: 'empty', path: '/#/chat' },
  { name: 'paused', id: '52a74da3-3a33-4d6f-bf46-f09026b6b445', run: '91efaabb-abeb-40da-92e4-54224021d156' },
  { name: 'failed', id: '7992f085-c82c-4fbb-a9c2-7d89fa45059c', run: '602e81b7-ff10-49e9-b636-f111f24c7f5e' },
  { name: 'partial', id: '6056daf2-3b3c-4fe6-881c-7bbbced4896b', run: 'c66807d3-9935-4b75-b2ab-74888b740459' },
  { name: 'workspace', path: '/#/workspace' },
]
let revision = 'unknown'
try { revision = execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim() } catch {}
const manifest = {
  version: 1, label, base_url: base.origin, output, source_revision: revision,
  reference_revision: referenceRevision || null,
  reference_scope: referenceRevision ? 'Reconstructed old UI from committed static assets, current real API; not a recovered original before capture' : null,
  started_at: new Date().toISOString(), browser: {},
  policy: { real_get_responses: true, doctor_blocked: true, mutations_blocked: true, external_origins_blocked: true, before_drawers_untouched: true },
  counts: { requests: 0, doctor_blocked: 0, external_blocked: 0, mutations: 0 },
  captures: [], errors: [],
}
const safeError = error => String(error?.message || error).slice(0, 1500)
const increment = (object, key) => { object[key] = (object[key] || 0) + 1 }
let browser
let context
let current
const deadline = Date.now() + 8 * 60_000

function responseSummary(path, body) {
  if (Array.isArray(body)) return { path, count: body.length }
  if (!body || typeof body !== 'object') return { path, shape: typeof body }
  const summary = { path }
  for (const key of ['id', 'task_id', 'conversation_id', 'run_id', 'active_run_id', 'latest_run_id', 'status', 'stage', 'phase', 'mode', 'terminal_outcome', 'pause_reason', 'resource_reason', 'error_code', 'profile', 'current_round', 'cursor']) {
    if (typeof body[key] === 'string' || typeof body[key] === 'number') summary[key] = body[key]
  }
  for (const key of ['telemetry', 'inference_summary', 'research_report', 'context_budget']) summary[`${key}_present`] = !!body[key]
  if (body.research_report) summary.report = {
    artifact_id: body.research_report.artifact_id, markdown_artifact_id: body.research_report.markdown_artifact_id,
    outcome: body.research_report.outcome, sections: body.research_report.sections,
  }
  return summary
}

async function layout(page) {
  return page.evaluate(() => {
    const visible = element => {
      const r = element.getBoundingClientRect()
      const s = getComputedStyle(element)
      return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && !element.closest('[inert], [aria-hidden="true"]')
    }
    const describe = element => {
      const r = element.getBoundingClientRect()
      return { tag: element.tagName, class: String(element.className).slice(0, 160), x: Math.round(r.x), y: Math.round(r.y), width: Math.round(r.width), height: Math.round(r.height), scroll_width: element.scrollWidth, client_width: element.clientWidth }
    }
    const nodes = [...document.querySelectorAll('body *')].filter(visible)
    const clipped = nodes.filter(el => {
      const r = el.getBoundingClientRect()
      return r.left < -1 || r.right > innerWidth + 1
    })
    const targets = [...document.querySelectorAll('button, a[href], input, select, textarea, summary')].filter(visible)
    const small = targets.filter(el => {
      const r = el.getBoundingClientRect()
      return r.width < 44 || r.height < 44
    })
    return {
      viewport: { width: innerWidth, height: innerHeight }, document_width: document.documentElement.scrollWidth,
      open_dialogs: [...document.querySelectorAll('dialog[open]')].map(el => ({ label: el.getAttribute('aria-label'), ...describe(el) })),
      clipping_count: clipped.length, clipping_samples: clipped.slice(0, 30).map(describe),
      small_target_count: small.length, small_target_samples: small.slice(0, 30).map(describe),
      note: 'Observations only; offscreen scroll content and inline links require human review. No baseline accessibility assertions.',
    }
  })
}

async function shot(page, record, suffix = '') {
  const path = join(output, `${label}-${record.case}-${record.width}${suffix}.png`)
  const measurements = await layout(page)
  const bytes = await page.screenshot({ fullPage: false, timeout: 15000 })
  await writeFile(path, bytes, { flag: 'wx' })
  record.screenshots.push({ path, label, view: suffix || 'original', layout: measurements })
}

try {
  browser = await chromium.launch({ executablePath: process.env.CHROME_BIN || '/usr/bin/google-chrome', headless: true, timeout: 20000, args: ['--no-sandbox'] })
  manifest.browser = { version: browser.version(), executable: process.env.CHROME_BIN || '/usr/bin/google-chrome', locale: 'ru-RU', timezone: 'UTC', reduced_motion: 'reduce', height: 900 }
  context = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'ru-RU', timezoneId: 'UTC', reducedMotion: 'reduce', serviceWorkers: 'block' })
  context.setDefaultTimeout(12000)
  context.setDefaultNavigationTimeout(20000)

  // Install before any page/goto; even accidental UI actions cannot reach writes.
  await context.route('**/*', async route => {
    const request = route.request()
    const url = new URL(request.url())
    manifest.counts.requests++
    if (current) increment(current.requests, `${request.method()} ${url.pathname}`)
    if (!['GET', 'HEAD'].includes(request.method())) {
      manifest.counts.mutations++
      current?.mutations.push({ method: request.method(), path: url.pathname })
      return route.abort('blockedbyclient')
    }
    if (url.origin !== base.origin) {
      manifest.counts.external_blocked++
      current?.external_blocked.push({ origin: url.origin, path: url.pathname })
      return route.abort('blockedbyclient')
    }
    if (/^\/api\/doctor(?:\/|$)/.test(url.pathname)) {
      manifest.counts.doctor_blocked++
      if (current) current.doctor_blocked++
      return route.abort('blockedbyclient')
    }
    return route.continue()
  })

  for (const width of [320, 375, 414, 768, 1024, 1280, 1440]) {
    for (const item of cases) {
      assert.ok(Date.now() < deadline, 'Capture exceeded eight-minute total budget')
      const record = { case: item.name, width, height: 900, label, conversation_id: item.id, expected_run_id: item.run, url: new URL(item.path || `/#/chat/${item.id}`, base).href, screenshots: [], assets: [], requests: {}, responses: [], doctor_blocked: 0, mutations: [], external_blocked: [], page_errors: [], console_errors: [], request_failures: [] }
      current = record
      manifest.captures.push(record)
      const page = await context.newPage()
      await page.setViewportSize({ width, height: 900 })
      const reads = []
      const assets = new Set()
      page.on('pageerror', error => record.page_errors.push(safeError(error)))
      page.on('console', message => { if (message.type() === 'error') record.console_errors.push(message.text().slice(0, 600)) })
      page.on('requestfailed', request => record.request_failures.push({ path: new URL(request.url()).pathname, error: request.failure()?.errorText }))
      page.on('response', response => {
        const url = new URL(response.url())
        if (url.pathname.startsWith('/assets/') || /\.(?:js|css|woff2?)(?:$)/.test(url.pathname)) assets.add(response.url())
        if (!url.pathname.startsWith('/api/') || url.pathname.endsWith('/stream')) return
        reads.push((async () => {
          const entry = { path: url.pathname, http_status: response.status() }
          record.responses.push(entry)
          if ((response.headers()['content-type'] || '').includes('application/json')) {
            try { Object.assign(entry, responseSummary(url.pathname, await response.json())) } catch { entry.body_unavailable = true }
          }
        })())
      })
      try {
        await page.goto(record.url, { waitUntil: 'domcontentloaded' })
        await page.getByRole('heading', { level: 1, includeHidden: true }).first().waitFor({ state: 'attached' })
        if (item.id) await page.getByRole('article', { name: 'Ответ Qwen', exact: true, includeHidden: true }).first().waitFor({ state: 'attached' })
        await page.evaluate(() => Promise.race([document.fonts.ready, new Promise((_, reject) => setTimeout(() => reject(new Error('Font readiness timeout')), 10000))]))
        await page.waitForTimeout(1200)
        assert.equal(record.mutations.length, 0, 'UI attempted mutation; blocked before reaching backend')
        await shot(page, record) // Never close automatic baseline drawers.

        if (label === 'after' && item.name === 'partial' && [375, 1440].includes(width)) {
          const details = page.getByRole('button', { name: 'Детали', exact: true })
          if (await details.getAttribute('aria-expanded') === 'false') await details.click()
          await page.locator('.chat-research').waitFor()
          await page.waitForTimeout(250)
          await shot(page, record, '-details')
          const sources = page.locator('[data-research-section="sources"]').first()
          await sources.scrollIntoViewIfNeeded()
          await page.waitForTimeout(250)
          await shot(page, record, '-sources')
        }
        await Promise.race([Promise.allSettled(reads), new Promise(resolve => setTimeout(resolve, 3000))])
        if (item.id) {
          assert.ok(record.responses.some(r => r.path === `/api/conversations/${item.id}/messages` && r.http_status === 200), 'Real saved messages did not load')
          assert.ok(record.responses.some(r => r.path === `/api/runs/${item.run}` && r.http_status === 200), 'Expected saved run did not load')
        }
      } finally {
        record.assets = [...assets].sort()
        await page.close()
      }
      assert.equal(record.page_errors.length, 0, 'Unexpected browser JavaScript error')
      assert.equal(record.mutations.length, 0, 'Unexpected mutating request')
    }
  }
  assert.equal(manifest.counts.mutations, 0)
  if (label === 'after') assert.equal(manifest.counts.doctor_blocked, 0, 'After UI automatically requested doctor; blocked, but acceptance failed')
  manifest.result = 'passed'
} catch (error) {
  manifest.result = 'failed'
  manifest.errors.push(safeError(error))
  process.exitCode = 1
} finally {
  await browser?.close()
  manifest.finished_at = new Date().toISOString()
  manifest.screenshot_count = manifest.captures.reduce((n, record) => n + record.screenshots.length, 0)
  const path = join(output, 'manifest.json')
  await writeFile(path, `${JSON.stringify(manifest, null, 2)}\n`, { flag: 'wx' })
  console.log(JSON.stringify({ result: manifest.result, manifest: path, screenshots: manifest.screenshot_count, counts: manifest.counts, errors: manifest.errors }, null, 2))
}
