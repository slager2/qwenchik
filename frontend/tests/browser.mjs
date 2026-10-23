import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { chromium } from 'playwright-core'

const root = fileURLToPath(new URL('..', import.meta.url))
const base = 'http://127.0.0.1:4175'
const server = spawn(process.execPath, ['node_modules/vite/bin/vite.js', '--host', '127.0.0.1', '--port', '4175', '--strictPort'], { cwd: root, stdio: 'pipe' })
let serverOutput = ''
server.stdout.on('data', (data) => { serverOutput += data })
server.stderr.on('data', (data) => { serverOutput += data })
let browser
const failures = []
const now = '2026-10-19T10:00:00Z'
const conversation = (id, mode) => ({ id, title: id === 'a' ? 'Первый разговор' : 'Исследование', mode, created_at: now, updated_at: now, latest_run_id: `r-${id}` })
const conversations = [conversation('a', 'chat'), conversation('b', 'research')]
const message = (id, conversation_id, run_id, role, content, status = 'completed', seq = 0) => ({ id, conversation_id, run_id, role, content, status, created_at: now, updated_at: now, last_event_seq: seq })
const messages = {
  a: [message('u-a', 'a', 'r-a', 'user', 'Первый вопрос'), message('m-a', 'a', 'r-a', 'assistant', 'Ответ первого разговора')],
  b: [message('u-b', 'b', 'r-b', 'user', 'Второй вопрос'), message('m-b', 'b', 'r-b', 'assistant', '## Результат\n\nПроверяемый факт [ev-1].\n\n| A | B |\n| - | - |\n| 1 | 2 |\n\n```js\n' + 'veryLongIdentifier'.repeat(60) + '\n```')],
}
const runs = Object.fromEntries(conversations.map((item) => [item.latest_run_id, { id: item.latest_run_id, conversation_id: item.id, user_message_id: `u-${item.id}`, assistant_message_id: `m-${item.id}`, mode: item.mode, operation: 'message', status: 'completed', retry_count: 0 }]))
const events = {}
events['r-b'] = [
  { type: 'plan.created', payload: {} },
  { type: 'step.started', payload: { step_id: 'step1' } },
  { type: 'tool.started', payload: { step_id: 'step1', tool: 'search', summary: 'Проверяю официальные источники', arguments: { query: 'PRIVATE-ARGUMENT' }, reasoning: 'PRIVATE-REASONING' } },
  { type: 'search.hit', payload: { step_id: 'step1', hit: { id: 'blocked-source', url: 'https://blocked.example/source', title: 'Недоступный источник', snippet: 'Snippet', provider: 'searxng', status: 'discovered' } } },
  { type: 'search.hit', payload: { step_id: 'step1', hit: { id: 'blocked-source', url: 'https://blocked.example/source', title: 'Недоступный источник', provider: 'searxng', status: 'rejected', error_code: 'fetch_timeout' } } },
  { type: 'query.updated', payload: { step_id: 'step1', query: 'actual public search query' } },
  { type: 'step.completed', payload: { step_id: 'step1' } },
  { type: 'search.hit', payload: { step_id: 'step2', hit: { id: 'pdf-rejected', url: 'https://example.org/report.pdf', title: 'PDF с превышением лимита', snippet: 'Описание PDF из поиска', provider: 'searxng', status: 'validated' } } },
  { type: 'tool.started', payload: { tool: 'fetch_url', step_id: 'step2', plan_attempt: 1, attempt_id: 'pdf-attempt', target_url: 'https://example.org/report.pdf' } },
  { type: 'tool.failed', payload: { tool: 'fetch_url', step_id: 'step2', plan_attempt: 1, attempt_id: 'pdf-attempt', source_fingerprint: 'pdf-rejected', target_url: 'https://example.org/report.pdf', status: 'rejected', error_code: 'pdf_source_too_large', declared_bytes: 40534667, effective_limit_bytes: 32000000, fetched_bytes: 0, remaining_candidates: 1, mime_type: 'application/pdf', try_next_source: true, fallback_attempted: false, timestamp: now } },
].map((event, index) => ({ ...event, id: `research-event-${index}`, task_id: 'r-b', seq: index + 1, visibility: 'user', schema_version: 1, created_at: now }))
const streamCursors = []
const submissionKeys = []
let counter = 0
let rejectNext = false
let sourcesFail = true
let planFail = false
let providersFail = false
let messagesFail = false
let doctorBlocked = true
let sourceRequests = 0
let evidenceRequests = 0
let legacyEvidenceDone = false
const legacyTask = { id: 'legacy-task', title: 'Существующая задача', description: 'Проверить сохранённую задачу', status: 'failed', priority: 0, version: 1, created_at: now, updated_at: now }
const activeWorkspaceTask = { id: 'active-workspace-task', title: 'Активный поток Workspace', description: 'Проверить выборочную синхронизацию', status: 'running', stage: 'fetch', priority: 0, version: 1, created_at: now, updated_at: now }
const workspaceEventRequests = []
const workspaceSSEEvents = [
  { type: 'task.created', payload: { status: 'running' } },
  { type: 'message.delta', payload: { stream_id: 'workspace-stream', message_id: 'workspace-assistant', chunk_index: 0, delta: 'Workspace live draft' } },
  { type: 'task.status_changed', payload: { from: 'running', to: 'verifying' } },
  { type: 'step.started', payload: { step_id: 'step1' } },
  { type: 'step.completed', payload: { step_id: 'step1' } },
  { type: 'step.completed', payload: { step_id: 'step2' } },
  { type: 'evidence.created', payload: { evidence_id: 'workspace-evidence' } },
  { type: 'artifact.created', payload: { artifact_id: 'workspace-artifact' } },
  { type: 'approval.requested', payload: { approval_id: 'workspace-approval' } },
].map((event, index) => ({ ...event, id: `workspace-event-${index + 1}`, task_id: activeWorkspaceTask.id, seq: index + 1, schema_version: 1, visibility: 'user', created_at: now }))

async function until(check, description, timeout = 10000) {
  const deadline = Date.now() + timeout
  while (Date.now() < deadline) {
    if (await check()) return
    await new Promise((resolve) => setTimeout(resolve, 50))
  }
  throw new Error(`Timed out: ${description}`)
}

function submit(id, content, mode, operation = 'message', previous) {
  const runID = `run-${++counter}`
  const user = previous ? messages[id].find((item) => item.id === previous.user_message_id) : message(`user-${counter}`, id, runID, 'user', content)
  const assistant = message(`assistant-${counter}`, id, runID, 'assistant', 'Начало', 'streaming', 1)
  if (!previous) messages[id].push(user)
  messages[id].push(assistant)
  const run = { id: runID, conversation_id: id, user_message_id: user.id, assistant_message_id: assistant.id, mode, operation, status: 'running', retry_count: operation === 'retry' ? 1 : 0 }
  runs[runID] = run
  const conv = conversations.find((item) => item.id === id)
  conv.active_run_id = runID
  conv.latest_run_id = runID
  events[runID] = [1, 2].map((seq) => ({ id: `${runID}-${seq}`, task_id: runID, seq, type: 'message.delta', schema_version: 1, visibility: 'user', created_at: now, payload: { message_id: assistant.id, stream_id: `stream-${counter}`, chunk_index: seq - 1, delta: seq === 1 ? 'Начало' : ' потока' } }))
  return { conversation_id: id, user_message: user, assistant_message: assistant, run }
}

try {
  await until(async () => { if (server.exitCode !== null) throw new Error(serverOutput); try { return (await fetch(base)).ok } catch { return false } }, 'Vite startup')
  browser = await chromium.launch({ executablePath: process.env.CHROME_BIN || '/usr/bin/google-chrome', headless: true, args: ['--no-sandbox'] })
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 }, reducedMotion: 'reduce' })
  page.on('pageerror', (error) => failures.push(error.message))
  await page.route('**/api/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const parts = url.pathname.split('/').filter(Boolean)
    const method = request.method()
    const json = (value, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(value) })
    if (parts[1] === 'tasks' && parts[2] === activeWorkspaceTask.id && method === 'GET') workspaceEventRequests.push(`${parts.slice(3).join('/') || 'task'}`)
    if (parts[1] === 'doctor') return doctorBlocked ? route.abort('blockedbyclient') : json({ status: 'ok', checks: { app: { status: 'ok' }, database: { status: 'ok' }, worker: { status: 'ok' }, llm: { status: 'ok' } } })
    if (parts[1] === 'health') return json({ status: 'ok', database: 'ok' })
    if (parts[1] === 'providers') return providersFail ? json({ error: { message: 'Провайдеры недоступны' } }, 503) : json({ search: { providers: {} } })
    if (parts[1] === 'memories') return json([])
    if (parts[1] === 'tasks') {
      if (!parts[2]) return json([legacyTask, activeWorkspaceTask])
      if (parts[2] === activeWorkspaceTask.id) {
        if (!parts[3]) return json(activeWorkspaceTask)
        if (parts[3] === 'messages') return json([])
        if (parts[3] === 'events' && parts[4] === 'stream') {
          activeWorkspaceTask.status = 'verifying'
          const after = Number(url.searchParams.get('after') || request.headers()['last-event-id'] || 0)
          const selected = workspaceSSEEvents.filter((event) => event.seq > after)
          const body = 'retry: 10000\n\n' + selected.map((event) => `id: ${event.seq}\nevent: ${event.type}\ndata: ${JSON.stringify(event)}\n\n`).join('')
          return route.fulfill({ contentType: 'text/event-stream', body })
        }
        if (parts[3] === 'events') return json(workspaceSSEEvents.slice(0, 1).filter((event) => event.seq > Number(url.searchParams.get('after') || 0)))
        if (parts[3] === 'steps') return json([{ id: 'ws-step-1', task_id: activeWorkspaceTask.id, step_id: 'step1', position: 0, kind: 'fetch', description: 'Этап 1', dependencies: [], expected_output: '', success_criteria: ['done'], status: 'running', attempts: 1, created_at: now, updated_at: now }])
        if (parts[3] === 'evidence') return json([])
        if (parts[3] === 'artifacts') return json([])
        if (parts[3] === 'approvals') return json([])
        if (parts[3] === 'checkpoints') return json([])
        return json([])
      }
      if (!parts[3]) return json(legacyTask)
      if (parts[3] === 'messages') return json([{ id: 'legacy-message', task_id: legacyTask.id, role: 'user', content: 'Сохранённое уточнение задачи', created_at: now }])
      if (parts[3] === 'events') return json([{ id: 'legacy-event', task_id: legacyTask.id, seq: 1, type: 'message.delta', schema_version: 1, visibility: 'user', created_at: now, payload: { message_id: 'legacy-partial', stream_id: 'legacy-stream', chunk_index: 0, delta: 'Частичный ответ старой задачи' } }])
      if (parts[3] === 'evidence') {
        await new Promise((resolve) => setTimeout(resolve, 2000))
        legacyEvidenceDone = true
        return json({ error: { message: 'Legacy evidence unavailable' } }, 503)
      }
      return json([])
    }
    if (parts[1] === 'conversations') {
      const id = parts[2]
      if (!id && method === 'GET') return json(conversations)
      if (!id && method === 'POST') {
        const body = request.postDataJSON()
        const next = { id: `c-${++counter}`, title: body.title, mode: body.mode, created_at: now, updated_at: now }
        conversations.push(next)
        messages[next.id] = []
        return json(next, 201)
      }
      const conv = conversations.find((item) => item.id === id)
      if (!conv) return json({ error: { message: 'Разговор не найден' } }, 404)
      if (!parts[3] && method === 'GET') return json(conv)
      if (!parts[3] && method === 'PATCH') { Object.assign(conv, request.postDataJSON()); return json(conv) }
      if (!parts[3] && method === 'DELETE') { conv.archived_at = now; return route.fulfill({ status: 204 }) }
      if (parts[3] === 'messages' && method === 'GET') return messagesFail ? json({ error: { message: 'Сеть недоступна' } }, 503) : json(messages[id])
      if (parts[3] === 'messages' && method === 'POST') {
        submissionKeys.push(request.headers()['idempotency-key'])
        await new Promise((resolve) => setTimeout(resolve, 250))
        if (rejectNext) { rejectNext = false; return json({ error: { code: 'unavailable', message: 'Временный сетевой сбой', retryable: true } }, 503) }
        const body = request.postDataJSON()
        return json(submit(id, body.content, body.mode), 202)
      }
    }
    if (parts[1] === 'runs') {
      const run = runs[parts[2]]
      if (!run) return json({ error: { message: 'Run not found' } }, 404)
      if (!parts[3]) return json(run)
      if (parts[3] === 'runtime') return json({ task_id: run.id, cursor: events[run.id]?.at(-1)?.seq || 0, status: run.status, terminal_outcome: run.terminal_outcome, pause_reason: run.status === 'paused' ? 'manual' : '', completed_steps: 1, compactions: 1, max_continuations: 1, completion: { continuations: 0 }, runtime: { resources: { free_ram_bytes: null, gpu_free_vram_mb: null, gpu_temp_c: null, power_source: 'unknown' }, sleep: { active: false, available: false } } })
      if (parts[3] === 'cancel') {
        run.status = 'cancelled'
        const conv = conversations.find((item) => item.id === run.conversation_id)
        delete conv.active_run_id
        const assistant = messages[run.conversation_id].find((item) => item.id === run.assistant_message_id)
        assistant.status = 'cancelled'
        return json(run, 202)
      }
      if (['retry', 'continue'].includes(parts[3])) return json(submit(run.conversation_id, '', run.mode, parts[3], run), 202)
      if (parts[3] === 'events' && parts[4] === 'stream') {
        const after = Number(url.searchParams.get('after') || 0)
        streamCursors.push(after)
        const selected = (events[run.id] || []).filter((item) => item.seq > Math.max(after, Number(request.headers()['last-event-id'] || 0)))
        const body = 'retry: 10000\n\n' + selected.map((event) => `id: ${event.seq}\nevent: ${event.type}\ndata: ${JSON.stringify(event)}\n\n`).join('')
        const assistant = messages[run.conversation_id].find((item) => item.id === run.assistant_message_id)
        if (assistant && selected.length && run.status === 'running' && assistant.last_event_seq <= 2) { assistant.content = 'Начало потока'; assistant.last_event_seq = 2 }
        return route.fulfill({ contentType: 'text/event-stream', body })
      }
      if (parts[3] === 'events') return json((events[run.id] || []).filter((event) => event.seq > Number(url.searchParams.get('after') || 0)))
      if (parts[3] === 'sources') { sourceRequests++; return sourcesFail ? json({ error: { message: 'Источники временно недоступны' } }, 503) : json([{ id: 's-1', title: 'Реальный источник', url: 'https://example.org/source', status: 'extracted', provider: 'searxng' }]) }
      if (parts[3] === 'evidence') { evidenceRequests++; return json(run.mode === 'research' ? [{ id: 'ev-1', title: 'Реальный источник', canonical_url: 'https://example.org/source', excerpt: 'Проверенный фрагмент', provider: 'searxng', extraction_method: 'html', retrieved_at: now, content_sha256: 'abc', quality_flags: [] }] : []) }
      if (parts[3] === 'plan') return planFail ? json({ error: { message: 'План недоступен' } }, 503) : json({ goal: 'Сравнить', subtasks: [{ id: 'step1', description: 'Проверить источники' }] })
    }
    return json({ error: { message: `Unmocked: ${method} ${url.pathname}` } }, 404)
  })

  await page.goto(`${base}/#/chat/a`)
  await page.getByText('Ответ первого разговора', { exact: true }).waitFor()
  await page.waitForFunction(() => typeof window.__qwenchikDebug?.snapshot === 'function')
  const initialDebug = await page.evaluate(() => window.__qwenchikDebug.snapshot())
  assert.ok(initialDebug.requests.count > 0, 'development diagnostics should observe initial API requests')
  assert.ok(Object.keys(initialDebug.requests.by_endpoint).every((endpoint) => endpoint.startsWith('GET /api/')), 'HTTP endpoint aggregation should use normalized paths')
  assert.ok(!JSON.stringify(initialDebug).includes('PRIVATE-'), 'debug snapshot must not include request payloads')
  assert.equal(initialDebug.marker, 'QWENCHIK_DEV_NETWORK_DIAGNOSTICS_V1')
  await page.getByText('Backend: доступен', { exact: true }).waitFor()
  await page.getByText('Qwen: не проверена', { exact: true }).waitFor()
  doctorBlocked = false
  await page.getByRole('link', { name: /Исследование/ }).click()
  await page.getByRole('heading', { name: 'Результат', exact: true }).waitFor()
  await page.getByText('Источники временно недоступны', { exact: true }).waitFor()
  await page.getByText('actual public search query', { exact: true }).waitFor()
  await page.getByText('Проверяю официальные источники', { exact: true }).waitFor()
  await page.getByText('Источник не ответил вовремя. Проверьте доступные материалы ниже.', { exact: true }).waitFor()
  assert.ok(!(await page.locator('body').innerText()).includes('PRIVATE-'))
  assert.ok((await page.locator('.chat-research section').filter({ has: page.getByRole('heading', { name: 'План', exact: true }) }).innerText()).includes('Завершено'))
  assert.equal(await page.locator('.chat-markdown a[href="https://example.org/source"]').count(), 1)
  assert.equal(await page.getByText('Ответ первого разговора', { exact: true }).count(), 0)
  assert.equal(await page.locator('.chat-markdown table').count(), 1)
  const pdfCard = page.locator('.chat-source-cards > li').filter({ has: page.getByRole('link', { name: 'PDF с превышением лимита', exact: true }) })
  await pdfCard.getByText('Код: pdf_source_too_large', { exact: true }).waitFor()
  await pdfCard.getByText('Тело не скачивалось', { exact: true }).waitFor()
  await pdfCard.getByText('Разрешён следующий источник. Переход пока не подтверждён.', { exact: true }).waitFor()
  assert.match(await pdfCard.innerText(), /40,535 MB; 38,657 MiB/)
  assert.match(await pdfCard.innerText(), /32 MB; 30,518 MiB/)
  assert.equal(await page.locator('.chat-markdown a[href="https://example.org/report.pdf"]').count(), 0)
  await page.getByText('Проверенный фрагмент', { exact: true }).waitFor()
  for (const [type, payload] of [
    ['source.updated', { ...events['r-b'].at(-1).payload, fallback_attempted: true }],
    ['tool.started', { tool: 'fetch_url', step_id: 'step2', target_url: 'https://example.org/source' }],
    ['source.extracted', { tool: 'fetch_url', step_id: 'step2', plan_attempt: 1, attempt_id: 'html-attempt', target_url: 'https://example.org/source' }],
  ]) events['r-b'].push({ id: `research-event-${events['r-b'].length}`, task_id: 'r-b', seq: events['r-b'].length + 1, visibility: 'user', schema_version: 1, created_at: now, type, payload })
  await page.reload()
  await pdfCard.getByText('Попытка перехода к другому источнику подтверждена.', { exact: true }).waitFor()
  assert.equal(await pdfCard.count(), 1)
  assert.equal(await pdfCard.getByText('Код: pdf_source_too_large', { exact: true }).count(), 1)
  sourcesFail = false
  const previousEvidenceRequests = evidenceRequests
  await page.getByRole('button', { name: 'Повторить загрузку' }).click()
  await page.getByRole('link', { name: 'Реальный источник', exact: true }).waitFor()
  assert.equal(evidenceRequests, previousEvidenceRequests)
  assert.ok(sourceRequests >= 2)
  planFail = true
  providersFail = true
  await page.reload()
  await page.getByRole('heading', { name: 'Результат', exact: true }).waitFor()
  await page.getByText('План недоступен', { exact: true }).waitFor()
  await page.getByText('Провайдеры недоступны', { exact: true }).waitFor()
  assert.equal(await page.locator('.chat-markdown a[href="https://example.org/source"]').count(), 1)
  planFail = false
  providersFail = false
  messagesFail = true
  await page.reload()
  await page.getByText('Сеть недоступна', { exact: true }).waitFor()
  messagesFail = false

  for (const width of [320, 375, 414, 768, 1440]) {
    await page.setViewportSize({ width, height: 900 })
    await until(async () => width > 1120 || await page.locator('dialog[open]').count() > 0, 'responsive details drawer')
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `page overflow at ${width}`)
    if (width <= 1120) {
      const dialog = page.getByRole('dialog', { name: 'Детали исследования', exact: true })
      await dialog.waitFor()
      for (let index = 0; index < 12; index++) {
        await page.keyboard.press('Tab')
        assert.ok(await page.evaluate(() => document.querySelector('dialog[open]')?.contains(document.activeElement)), `focus escaped at ${width}`)
      }
      await page.keyboard.press('Escape')
      await until(async () => await page.locator('dialog[open]').count() === 0, 'drawer close')
      await page.getByRole('button', { name: 'Детали', exact: true }).click()
    }
  }
  await page.getByRole('button', { name: 'Детали', exact: true }).click()
  await page.getByRole('link', { name: /Первый разговор/ }).click()
  await page.getByText('Ответ первого разговора', { exact: true }).waitFor()
  const input = page.getByRole('textbox', { name: 'Сообщение', exact: true })
  await input.fill('Новый вопрос')
  await input.evaluate((element) => element.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, isComposing: true })))
  assert.equal(submissionKeys.length, 0)
  await input.press('Shift+Enter')
  assert.equal(await input.inputValue(), 'Новый вопрос\n')
  rejectNext = true
  await input.press('Enter')
  await page.locator('.chat-role-user').getByText('Новый вопрос', { exact: true }).waitFor()
  await page.getByText('Временный сетевой сбой', { exact: true }).waitFor()
  await page.getByRole('button', { name: 'Повторить действие' }).click()
  await page.getByRole('button', { name: 'Остановить', exact: true }).waitFor()
  await page.getByText('Начало потока', { exact: true }).waitFor()
  await page.waitForTimeout(350)
  const activeRunID = Object.values(runs).at(-1).id
  const syncCountsBefore = await page.evaluate(() => {
    const endpoints = window.__qwenchikDebug.snapshot().requests.by_endpoint
    return Object.fromEntries(['/api/conversations/:id/messages', '/api/runs/:id', '/api/runs/:id/runtime'].map((key) => [key, endpoints[`GET ${key}`]?.count || 0]))
  })
  await page.waitForTimeout(3000)
  const syncCountsAfter = await page.evaluate(() => {
    const endpoints = window.__qwenchikDebug.snapshot().requests.by_endpoint
    return Object.fromEntries(['/api/conversations/:id/messages', '/api/runs/:id', '/api/runs/:id/runtime'].map((key) => [key, endpoints[`GET ${key}`]?.count || 0]))
  })
  assert.deepEqual(syncCountsAfter, syncCountsBefore, `SSE-active chat issued repeating snapshots for ${activeRunID}`)
  assert.equal(submissionKeys[0], submissionKeys[1])
  await page.getByRole('button', { name: 'Переподключить', exact: true }).click()
  await until(() => streamCursors.some((cursor) => cursor >= 2), 'explicit reconnect cursor')
  assert.equal(await page.getByText('Начало потока', { exact: true }).count(), 1)
  await page.getByRole('button', { name: 'Остановить', exact: true }).click()
  await page.getByText('Генерация остановлена. Полученный текст сохранён.', { exact: true }).waitFor()
  assert.equal(await page.getByText('Начало потока', { exact: true }).count(), 1)
  await page.reload()
  await page.getByText('Начало потока', { exact: true }).waitFor()
  await page.getByRole('button', { name: 'Продолжить ответ', exact: true }).waitFor()
  await page.getByRole('button', { name: 'Продолжить ответ', exact: true }).click()
  await page.getByRole('button', { name: 'Остановить', exact: true }).waitFor()
  let activeRun = Object.values(runs).at(-1)
  activeRun.status = 'completed'
  const canonical = messages.a.find((item) => item.id === activeRun.assistant_message_id)
  canonical.content = 'Финальный ответ без terminal SSE'
  canonical.status = 'completed'
  canonical.last_event_seq = 5
  delete conversations[0].active_run_id
  await page.getByText('Финальный ответ без terminal SSE', { exact: true }).waitFor()
  await page.getByRole('button', { name: 'Повторить ответ', exact: true }).waitFor()
  await page.getByRole('button', { name: 'Повторить ответ', exact: true }).click()
  await page.getByRole('button', { name: 'Остановить', exact: true }).waitFor()
  await page.getByRole('button', { name: 'Остановить', exact: true }).click()
  await page.getByRole('button', { name: 'Переименовать разговор' }).click()
  await page.getByRole('textbox', { name: 'Название разговора' }).fill('Переименованный чат')
  await page.getByRole('button', { name: 'Сохранить', exact: true }).click()
  await page.getByRole('heading', { name: 'Переименованный чат', exact: true }).waitFor()
  await page.getByRole('link', { name: 'Рабочее пространство' }).click()
  await page.getByRole('heading', { name: 'Задачи', exact: true }).waitFor()
  await page.getByText('Сохранённое уточнение задачи', { exact: true }).waitFor()
  assert.equal(legacyEvidenceDone, false)
  await page.getByText('Частичный ответ старой задачи', { exact: true }).waitFor()
  await until(() => legacyEvidenceDone, 'legacy evidence failure')
  await page.getByText('Часть сведений задачи недоступна. Загруженные данные сохранены; обновите задачу для повтора.', { exact: true }).waitFor()
  assert.equal(await page.getByText('Сохранённое уточнение задачи', { exact: true }).count(), 1)
  assert.equal(await page.getByText('Частичный ответ старой задачи', { exact: true }).count(), 1)
  await page.locator('.task-card').filter({ hasText: 'Активный поток Workspace' }).click()
  await page.getByText('Workspace live draft', { exact: true }).waitFor()
  await page.locator('.inspector-header .status-tag').getByText('Проверка', { exact: true }).waitFor()
  await page.waitForTimeout(650)
  for (const endpoint of ['messages', 'events', 'checkpoints']) assert.equal(workspaceEventRequests.filter((item) => item === endpoint).length, 1, `${endpoint} should not refresh for unrelated SSE events`)
  for (const endpoint of ['steps', 'evidence', 'artifacts', 'approvals']) assert.equal(workspaceEventRequests.filter((item) => item === endpoint).length, 2, `${endpoint} should be coalesced to initial + one selective refresh`)
  assert.equal(workspaceEventRequests.filter((item) => item === 'task').length, 2, 'status changes refresh the selected task once')
  assert.equal(workspaceEventRequests.filter((item) => item === 'events/stream').length, 1, 'status transitions do not recreate the workspace SSE connection')
  await page.getByRole('button', { name: /Память/ }).click()
  await page.getByRole('dialog', { name: 'Память', exact: true }).waitFor()
  await page.keyboard.press('Escape')
  await page.getByRole('link', { name: 'Открыть чаты' }).click()
  await page.getByRole('heading', { name: 'Новый разговор', exact: true }).waitFor()
  await page.getByRole('combobox', { name: 'Режим разговора' }).selectOption('research')
  await page.getByRole('textbox', { name: 'Сообщение', exact: true }).fill('Исследуй вопрос')
  await page.getByRole('button', { name: 'Отправить', exact: true }).click()
  await page.getByRole('button', { name: 'Остановить', exact: true }).waitFor()
  assert.equal(Object.values(runs).at(-1).mode, 'research')
  assert.match(page.url(), /#\/chat\/c-/)
  await page.getByRole('button', { name: 'Остановить', exact: true }).click()
  await page.getByRole('button', { name: 'Удалить', exact: true }).click()
  await page.getByRole('heading', { name: 'Новый разговор', exact: true }).waitFor()
  await page.setViewportSize({ width: 375, height: 900 })
  const trigger = page.getByRole('button', { name: 'Открыть разговоры', exact: true })
  await trigger.click()
  await page.getByRole('dialog', { name: 'Разговоры', exact: true }).waitFor()
  await page.keyboard.press('Escape')
  assert.ok(await trigger.evaluate((element) => element === document.activeElement))
  await trigger.click()
  await page.mouse.click(372, 450)
  await until(async () => await page.locator('dialog[open]').count() === 0, 'backdrop dismissal')
  assert.ok(await trigger.evaluate((element) => element === document.activeElement))
  assert.deepEqual(failures, [])
  console.log('PASS: Chrome browser smoke; conversation isolation/refresh, markdown/citations, independent source failure/retry, widths 320/375/414/768/1440, drawer focus/Escape, optimistic failure/idempotent retry, streaming/reconnect/stop, continuation/retry, lost terminal polling, rename/create/research/delete, workspace/memory navigation.')
} finally {
  await browser?.close()
  server.kill('SIGTERM')
}
