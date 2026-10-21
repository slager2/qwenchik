import assert from 'node:assert/strict'
import { chromium } from 'playwright-core'

async function runLive() {
  const base = new URL(process.env.BASE_URL || 'http://localhost:8080')
  assert.ok(['http:', 'https:'].includes(base.protocol) && !base.username && !base.password, 'BASE_URL must be an HTTP(S) application URL without credentials')
  const timeout = Number(process.env.LIVE_TIMEOUT_MS || 90000)
  assert.ok(Number.isFinite(timeout) && timeout >= 1000 && timeout <= 300000, 'LIVE_TIMEOUT_MS must be between 1000 and 300000')
  const extended = process.env.LIVE_EXTENDED === '1'
  const browser = await chromium.launch({ executablePath: process.env.CHROME_BIN || '/usr/bin/google-chrome', headless: true, args: ['--no-sandbox'] })
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, reducedMotion: 'reduce' })
  context.setDefaultTimeout(timeout)
  context.setDefaultNavigationTimeout(timeout)
  const page = await context.newPage()
  const ownedRuns = new Set()
  const pageErrors = []
  let conversationID = ''
  let passed = false
  const deadline = Date.now() + timeout * (extended ? 5 : 2)
  const remaining = () => Math.max(1, Math.min(timeout, deadline - Date.now()))
  const path = (value) => new URL(value, base.origin).href
  async function json(response) {
    assert.ok(response.ok(), `HTTP ${response.status()} from ${new URL(response.url()).pathname}`)
    return response.json()
  }
  async function get(value) {
    return json(await context.request.get(path(value), { timeout: remaining() }))
  }
  async function until(check, label) {
    const end = Math.min(deadline, Date.now() + timeout)
    while (Date.now() < end) {
      const result = await check()
      if (result) return result
      await new Promise((resolve) => setTimeout(resolve, 250))
    }
    throw new Error(`Timed out: ${label}`)
  }
  async function inspectEvents(runID) {
    const history = []
    let cursor = 0
    let complete = false
    for (let index = 0; index < 100 && Date.now() < deadline; index++) {
      const batch = await get(`/api/runs/${encodeURIComponent(runID)}/events?after=${cursor}`)
      const fresh = Array.isArray(batch) ? batch.filter((event) => event.seq > cursor) : []
      if (!fresh.length) { complete = true; break }
      history.push(...fresh)
      cursor = Math.max(...fresh.map((event) => event.seq))
    }
    assert.ok(complete, 'Could not finish event inspection within the live test budget')
    assert.ok(!history.some((event) => ['search.hit', 'search.provider', 'query.updated'].includes(event.type) || (event.type === 'tool.started' && /search|fetch|browse|open_url/.test(event.payload?.tool || ''))), 'Ordinary chat unexpectedly executed external research')
  }
  async function stopAfterChunk(run) {
    assert.equal(run.conversation_id, conversationID)
    assert.equal(run.mode, 'chat')
    assert.ok(typeof run.id === 'string' && run.id)
    ownedRuns.add(run.id)
    const chunk = await until(async () => {
      const delta = await page.evaluate((id) => window.__liveSSE.find((event) => event.task_id === id && typeof event.payload?.delta === 'string' && event.payload.delta.length), run.id)
      if (delta) return delta
      const current = await get(`/api/runs/${encodeURIComponent(run.id)}`)
      if (['failed', 'cancelled', 'completed'].includes(current.status)) throw new Error(`Run ${run.id} became ${current.status} before a browser SSE chunk; stop was not verified. ${current.error_code || ''}`)
      return null
    }, 'first real browser SSE chunk')
    const stop = page.getByRole('button', { name: 'Остановить', exact: true })
    assert.ok(await stop.isEnabled(), 'Generation completed before Stop could be verified')
    const response = page.waitForResponse((value) => new URL(value.url()).pathname === `/api/runs/${run.id}/cancel` && value.request().method() === 'POST', { timeout: remaining() })
    await stop.click({ timeout: remaining() })
    await json(await response)
    const stopped = await until(async () => {
      const current = await get(`/api/runs/${encodeURIComponent(run.id)}`)
      return ['cancelled', 'completed', 'failed'].includes(current.status) ? current : null
    }, 'cancel persistence')
    assert.equal(stopped.status, 'cancelled', 'Stop raced with completion or failure; cancellation was not verified')
    const saved = await until(async () => {
      const items = await get(`/api/conversations/${encodeURIComponent(conversationID)}/messages`)
      const assistant = items.find((item) => item.id === run.assistant_message_id)
      return assistant?.content && ['cancelled', 'partial', 'stopped'].includes(assistant.status) ? assistant : null
    }, 'persisted partial assistant message')
    assert.ok(saved.content.includes(chunk.payload.delta), 'Canonical partial output lost the received SSE chunk')
    const article = page.getByRole('article', { name: 'Ответ Qwen', exact: true }).last()
    await article.locator('.chat-markdown').waitFor({ timeout: remaining() })
    await page.getByText('Генерация остановлена. Полученный текст сохранён.', { exact: true }).waitFor({ timeout: remaining() })
    await page.reload({ timeout: remaining() })
    await page.getByText('Генерация остановлена. Полученный текст сохранён.', { exact: true }).waitFor({ timeout: remaining() })
    await article.locator('.chat-markdown').waitFor({ timeout: remaining() })
    const before = await article.locator('.chat-markdown').innerText()
    await page.reload({ timeout: remaining() })
    await page.getByText('Генерация остановлена. Полученный текст сохранён.', { exact: true }).waitFor({ timeout: remaining() })
    await page.getByRole('article', { name: 'Ответ Qwen', exact: true }).last().locator('.chat-markdown').waitFor({ timeout: remaining() })
    assert.equal(await page.getByRole('article', { name: 'Ответ Qwen', exact: true }).last().locator('.chat-markdown').innerText(), before, 'Reload changed retained partial text')
    await inspectEvents(run.id)
    console.log(`PASS: real SSE, Stop and reload persistence for owned ${run.operation || 'message'} run ${run.id}`)
  }
  try {
    await get('/api/health')
    const title = `Live browser smoke ${new Date().toISOString()}`
    const created = await json(await context.request.post(path('/api/conversations'), { timeout: remaining(), data: { title, mode: 'chat', research_settings: { web_enabled: false } } }))
    assert.ok(typeof created.id === 'string' && created.id)
    assert.equal(created.title, title)
    assert.equal(created.mode, 'chat')
    conversationID = created.id
    page.on('pageerror', (error) => pageErrors.push(error.message))
    await page.route('**/api/doctor', (route) => route.abort('blockedbyclient'))
    await page.addInitScript(() => {
      window.__liveSSE = []
      const NativeEventSource = window.EventSource
      window.EventSource = class extends NativeEventSource {
        constructor(url, options) {
          super(url, options)
          this.addEventListener('message.delta', (event) => {
            try { window.__liveSSE.push(JSON.parse(event.data)) } catch { window.__liveSSE.push({ invalid: true }) }
          })
        }
      }
    })
    await page.goto(`${base.origin}/#/chat/${encodeURIComponent(conversationID)}`, { timeout: remaining() })
    await page.getByRole('heading', { name: created.title, exact: true }).waitFor({ timeout: remaining() })
    const submitted = page.waitForResponse((response) => new URL(response.url()).pathname === `/api/conversations/${conversationID}/messages` && response.request().method() === 'POST', { timeout: remaining() })
    await page.getByRole('textbox', { name: 'Сообщение', exact: true }).fill('Без поиска и инструментов перечисли числа от 1 до 120, каждое на новой строке. Без пояснений.')
    await page.getByRole('button', { name: 'Отправить', exact: true }).click({ timeout: remaining() })
    const turn = await json(await submitted)
    await stopAfterChunk(turn.run)
    if (extended) {
      for (const [operation, label] of [['retry', 'Повторить ответ'], ['continue', 'Продолжить ответ']]) {
        const current = await get(`/api/conversations/${encodeURIComponent(conversationID)}`)
        const previousID = current.active_run_id || current.latest_run_id
        assert.ok(ownedRuns.has(previousID), 'Refusing to operate on a run not created by this runner')
        const response = page.waitForResponse((value) => new URL(value.url()).pathname === `/api/runs/${previousID}/${operation}` && value.request().method() === 'POST', { timeout: remaining() })
        await page.getByRole('button', { name: label, exact: true }).click({ timeout: remaining() })
        const next = await json(await response)
        await stopAfterChunk(next.run)
      }
    } else console.log('SKIP: retry/continue generation; set LIVE_EXTENDED=1 to test bounded additional runs')
    for (const width of [320, 375, 414, 768, 1440]) {
      await page.setViewportSize({ width, height: 900 })
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `Horizontal overflow at ${width}px`)
    }
    assert.deepEqual(pageErrors, [])
    passed = true
  } finally {
    if (conversationID) {
      for (const id of ownedRuns) {
        try {
          const response = await context.request.get(path(`/api/runs/${encodeURIComponent(id)}`), { timeout: 10000 })
          if (response.ok() && !['completed', 'failed', 'cancelled'].includes((await response.json()).status)) await context.request.post(path(`/api/runs/${encodeURIComponent(id)}/cancel`), { timeout: 10000 })
        } catch (error) { console.error(`Cleanup: could not cancel owned run ${id}: ${error.message}`); passed = false }
      }
      try {
        const response = await context.request.delete(path(`/api/conversations/${encodeURIComponent(conversationID)}`), { timeout: 10000 })
        assert.ok(response.ok(), `Cleanup HTTP ${response.status()}`)
      } catch (error) { console.error(`Cleanup: owned conversation ${conversationID} needs removal: ${error.message}`); passed = false }
    }
    await browser.close()
    if (!passed) process.exitCode = 1
  }
  if (passed) console.log('PASS: deployed frontend + real chat API; no external research requested; only runner-owned conversation/runs modified and archived')
}

if (process.env.RUN_LIVE_BROWSER !== '1') console.log('SKIP: live browser runner is opt-in. After deployment: RUN_LIVE_BROWSER=1 BASE_URL=http://localhost:8080 npm run test:browser:live')
else await runLive()
