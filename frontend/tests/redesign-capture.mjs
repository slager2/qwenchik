import assert from 'node:assert/strict'
import { mkdir, writeFile } from 'node:fs/promises'
import { dirname, join, resolve } from 'node:path'
import { chromium } from 'playwright-core'

// Read-only visual evidence for the redesign. It uses the locally served app,
// permits GET/HEAD only, and records a compact computed-style baseline.
const output = resolve(process.env.REDESIGN_OUTPUT || 'docs/redesign/phase-0/before')
const base = new URL(process.env.BASE_URL || 'http://127.0.0.1:8080')
assert.ok(base.protocol === 'http:' && ['127.0.0.1', 'localhost'].includes(base.hostname), 'Use a loopback HTTP origin')
assert.ok(['8080', '4175', '5173'].includes(base.port), 'Use an approved local application port')
await mkdir(output, { recursive: true })

const views = [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'tablet', width: 1024, height: 768 },
  { name: 'mobile', width: 375, height: 812 },
]
const routes = [
  { name: 'chat', path: '/#/chat' },
  { name: 'projects', path: '/#/spaces' },
  { name: 'tasks', path: '/#/workspace' },
]
const manifest = { version: 1, origin: base.origin, policy: 'GET/HEAD only; external origins blocked', captures: [], computed: {}, errors: [] }

const computed = () => [...document.querySelectorAll('button, a, textarea, input, select, h1, h2, p, small')]
  .filter(element => {
    const rect = element.getBoundingClientRect()
    const style = getComputedStyle(element)
    return rect.width > 0 && rect.height > 0 && style.visibility !== 'hidden'
  })
  .slice(0, 15)
  .map(element => {
    const rect = element.getBoundingClientRect()
    const style = getComputedStyle(element)
    return {
      element: element.tagName.toLowerCase(), label: (element.getAttribute('aria-label') || element.textContent || '').trim().replace(/\s+/g, ' ').slice(0, 48),
      font_size: style.fontSize, line_height: style.lineHeight, font_family: style.fontFamily,
      padding: style.padding, gap: style.gap, width: Math.round(rect.width), height: Math.round(rect.height),
    }
  })

let browser
try {
  browser = await chromium.launch({ executablePath: process.env.CHROME_BIN || '/usr/bin/google-chrome', headless: true, args: ['--no-sandbox'] })
  const context = await browser.newContext({ locale: 'ru-RU', timezoneId: 'UTC', reducedMotion: 'reduce', serviceWorkers: 'block' })
  await context.route('**/*', route => {
    const request = route.request()
    const url = new URL(request.url())
    if (!['GET', 'HEAD'].includes(request.method()) || url.origin !== base.origin || url.pathname.startsWith('/api/doctor')) return route.abort('blockedbyclient')
    return route.continue()
  })
  for (const view of views) {
    for (const route of routes) {
      const page = await context.newPage()
      await page.setViewportSize(view)
      const url = new URL(route.path, base).href
      await page.goto(url, { waitUntil: 'domcontentloaded' })
      await page.waitForTimeout(800)
      await page.evaluate(() => document.fonts.ready)
      const file = `${route.name}-${view.width}x${view.height}.png`
      await page.screenshot({ path: join(output, file), fullPage: false })
      const record = { route: route.name, viewport: view, file }
      manifest.captures.push(record)
      if (route.name === 'chat' && view.name === 'desktop') manifest.computed = await page.evaluate(computed)
      await page.close()
    }
  }
  await context.close()
} catch (error) {
  manifest.errors.push(String(error?.message || error))
  throw error
} finally {
  await browser?.close()
  await writeFile(join(output, 'manifest.json'), `${JSON.stringify(manifest, null, 2)}\n`)
}
