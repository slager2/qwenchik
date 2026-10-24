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
const bundleHash = '0123456789abcdef'.repeat(4)
const contextHash = 'fedcba9876543210'.repeat(4)
const telemetryFixture = () => {
  const at = new Date().toISOString()
  const metric = (value, unit, source = 'exact') => ({ value, unit, source, updated_at: at })
  return {
    timestamp: at, stale_after_ms: 30000,
    capabilities: { cpu: true, ram: true, gpu: true, gpu_temperature: true, gpu_utilization: false, power: false, battery: false, process_rss: true, llama_health: false, llama_timings: true, llama_metrics: false, llama_slots: false },
    system: {
      metadata: { os: 'linux', platform: 'linux', uptime_seconds: metric(3600, 'seconds') },
      cpu_usage: metric(24, 'percent'), cpu_temperature: metric(null, 'celsius', 'unavailable'),
      ram_used: metric(4 * 1024 ** 3, 'bytes'), ram_free: metric(8 * 1024 ** 3, 'bytes'), ram_total: metric(12 * 1024 ** 3, 'bytes'),
      gpus: [{ name: metric('Browser fixture GPU', 'name'), temperature: metric(52, 'celsius'), vram_free: metric(4 * 1024 ** 3, 'bytes') }], agent_rss: metric(1024, 'bytes'), llama_server_rss: metric(null, 'bytes', 'unavailable'),
      power_source: metric('unknown', 'status'), power_watts: metric(null, 'watts', 'unavailable'),
      battery_percent: metric(null, 'percent', 'unavailable'), battery_charging: metric(null, 'boolean', 'unavailable'),
      llama_health: metric(null, 'status', 'unavailable'), llama_slots_active: metric(null, 'slots', 'unavailable'), llama_slots_total: metric(null, 'slots', 'unavailable'),
    },
    inference: {
      status: 'generating', source: 'llama.cpp', updated_at: at,
      requested_tokens: metric(128, 'tokens'), prompt_tokens: metric(32, 'tokens'), output_tokens: metric(8, 'tokens'), total_tokens: metric(40, 'tokens'),
      cached_prompt_tokens: metric(null, 'tokens', 'unavailable'), prompt_tokens_per_second: metric(220, 'tokens/s'), output_tokens_per_second: metric(18, 'tokens/s'),
      ttft_ms: metric(120, 'ms'), prompt_ms: metric(145, 'ms'), generation_ms: metric(444, 'ms'), context_tokens: metric(40, 'tokens'), context_window: metric(32768, 'tokens'),
    },
  }
}
const runtimeFixture = (task_id, cursor = 0) => ({
  task_id, cursor, status: 'running', telemetry: telemetryFixture(), telemetry_revision: 1, bundle_hash: bundleHash, context_snapshot_hash: contextHash,
  runtime: { enabled: true, keep_awake: true, resources: { free_ram_bytes: 8192, rss_bytes: 1024, gpu_free_vram_mb: null, gpu_temp_c: null, power_source: 'unknown', snapshot_at: new Date().toISOString(), warnings: [] }, sleep: { active: false, available: false, provider: 'unavailable' } },
})
const conversation = (id, mode) => ({ id, title: id === 'a' ? 'Первый разговор' : 'Исследование', mode, created_at: now, updated_at: now, latest_run_id: `r-${id}` })
const conversations = [conversation('a', 'chat'), conversation('b', 'research'), { id: 'c', title: 'Заблокированная проверка', mode: 'research', created_at: now, updated_at: now, latest_run_id: 'r-c' }, { id: 'd', title: 'Ресурсная пауза', mode: 'research', created_at: now, updated_at: now, latest_run_id: 'r-d' }]
conversations.push({ ...conversation('e', 'research'), title: 'Сбой планирования' })
const message = (id, conversation_id, run_id, role, content, status = 'completed', seq = 0) => ({ id, conversation_id, run_id, role, content, status, created_at: now, updated_at: now, last_event_seq: seq })
const messages = {
  a: [message('u-a', 'a', 'r-a', 'user', 'Первый вопрос'), message('m-a', 'a', 'r-a', 'assistant', 'Ответ первого разговора')],
  b: [message('u-b', 'b', 'r-b', 'user', 'Второй вопрос'), message('m-b', 'b', 'r-b', 'assistant', '## Результат\n\nПроверяемый факт [ev-1].\n\n| A | B |\n| - | - |\n| 1 | 2 |\n\n```js\n' + 'veryLongIdentifier'.repeat(60) + '\n```')],
  c: [message('u-c', 'c', 'r-c', 'user', 'Проверить критические критерии'), message('m-c', 'c', 'r-c', 'assistant', '', 'failed')],
  d: [message('u-d', 'd', 'r-d', 'user', 'Исследование на паузе'), message('m-d', 'd', 'r-d', 'assistant', 'Сохранённый фрагмент', 'streaming')],
  e: [message('old-e', 'e', 'old-e-run', 'assistant', 'Предыдущий непустой ответ'), message('u-e', 'e', 'r-e', 'user', 'Составить план'), message('m-e', 'e', 'r-e', 'assistant', '', 'failed')],
}
const runs = Object.fromEntries(conversations.map((item) => [item.latest_run_id, { id: item.latest_run_id, conversation_id: item.id, user_message_id: `u-${item.id}`, assistant_message_id: `m-${item.id}`, mode: item.mode, operation: 'message', status: 'completed', retry_count: 0 }]))
runs['r-b'].terminal_outcome = 'partial'
runs['r-c'] = { ...runs['r-c'], status: 'failed', terminal_outcome: 'blocked', error_code: 'completion_blocked', error_message: 'Проверка не смогла подтвердить обязательные критерии.' }
runs['r-d'] = { ...runs['r-d'], status: 'paused', pause_reason: 'resource_guard', resource_reason: 'low_ram', stage: 'verification' }
runs['r-e'] = { ...runs['r-e'], status: 'failed', stage: 'planning', error_code: 'research_plan_incomplete' }
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
events['r-b'].push({
  id: 'research-verification-partial', task_id: 'r-b', seq: events['r-b'].length + 1, type: 'verification.completed', schema_version: 1, visibility: 'user', created_at: now,
  payload: { verdict: 'PARTIAL', phase: 'evidence_readiness', missing: [], supplementary_missing: ['Дополнительные параметры совместимости не подтверждены.'], criteria: [{ step_id: 'step1', criterion_index: 0, classification: 'critical', met: true, evidence_ids: ['ev-1'] }, { step_id: 'step1', criterion_index: 1, classification: 'supplementary', met: false, evidence_ids: [] }] },
})
events['r-c'] = [{ id: 'blocked-failure', task_id: 'r-c', seq: 1, type: 'task.failed', schema_version: 1, visibility: 'user', created_at: now, payload: { error_code: 'completion_blocked', message: 'Завершение заблокировано проверкой.', verification_summary: 'Обязательные требования пользователя не подтверждены источниками.', missing: ['Критическое требование: подтвердить запрошенный параметр.'] } }]
events['r-d'] = [{ id: 'paused-event', task_id: 'r-d', seq: 1, type: 'run.paused', schema_version: 1, visibility: 'user', created_at: now, payload: { pause_reason: 'resource_guard', resource_reason: 'low_ram' } }]
events['r-e'] = [{ id: 'planning-failure', task_id: 'r-e', seq: 1, type: 'task.failed', schema_version: 1, visibility: 'user', created_at: now, payload: { failure_stage: 'planning', failure_role: 'planner', planner_attempt: 2, validation_error: 'Не задан шаг поиска', error_code: 'research_plan_incomplete', plan_summary: { steps: 2, search_steps: 0, raw_plan: 'PRIVATE-PLAN', query_count: 'PRIVATE-COUNT' } } }]
const streamCursors = []
const submissionKeys = []
const researchContextSubmissions = []
let counter = 0
let rejectNext = false
let sourcesFail = true
let planFail = false
let providersFail = false
let messagesFail = false
let runtimeGetUnavailable = false
let workspaceRuntimeGetUnavailable = false
let doctorBlocked = true
let healthMalformed = false
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

async function assertTelemetryOverflow(panel, description) {
  const overflow = await panel.evaluate((element) => {
    const bounds = element.getBoundingClientRect()
    return [...element.querySelectorAll('.telemetry-panel__ticker, .telemetry-panel__ticker-item, .telemetry-panel__disclosure-grid[aria-hidden="false"], .telemetry-panel__metric, .telemetry-panel__copy')]
      .filter((child) => {
        const rect = child.getBoundingClientRect()
        return rect.width > 0 && rect.height > 0 && (child.scrollWidth > child.clientWidth + 1 || rect.left < bounds.left - 1 || rect.right > bounds.right + 1)
      })
      .map((child) => ({ className: child.className, text: child.textContent, width: child.clientWidth, scrollWidth: child.scrollWidth }))
  })
  assert.deepEqual(overflow, [], `${description}: telemetry horizontal overflow`)
}

async function assertPageOverflow(page, description) {
  const layout = await page.evaluate(() => ({
    viewport: innerWidth,
    width: document.documentElement.scrollWidth,
    overflowing: [...document.querySelectorAll('body *')].filter((element) => {
      const rect = element.getBoundingClientRect()
      return rect.width > 0 && rect.height > 0 && rect.right > innerWidth + 1 && getComputedStyle(element).visibility !== 'hidden'
    }).slice(0, 12).map((element) => ({ tag: element.tagName, className: element.className, right: element.getBoundingClientRect().right })),
  }))
  if (layout.width > layout.viewport) failures.push(`${description}: ${JSON.stringify(layout)}`)
}

async function expandResearchSections(page) {
  const research = page.locator('.chat-research')
  await research.waitFor({ state: 'visible' })
  for (const name of ['Поисковые запросы', 'Поисковые провайдеры', 'Действия']) {
    const toggle = research.getByRole('button', { name, exact: true })
    await toggle.waitFor({ state: 'visible' })
    if (await toggle.getAttribute('aria-expanded') === 'false') await toggle.click()
    assert.equal(await toggle.getAttribute('aria-expanded'), 'true', `${name} should be expanded`)
  }
}

async function checkTelemetry(panel, { cpu, tokens, temperature, ram, description }) {
  await panel.waitFor({ state: 'visible' })
  const ticker = panel.locator('.telemetry-panel__ticker')
  await until(async () => (await ticker.innerText()).includes(`${temperature}°`) && (await ticker.innerText()).includes(`${ram}G`), `${description} SSE resource ticker`)
  assert.match(await ticker.innerText(), /GPU/)
  assert.match(await ticker.innerText(), /RAM/)
  assert.doesNotMatch(await ticker.innerText(), /CPU/)
  assert.equal(await panel.locator('details, summary').count(), 0, `${description}: React disclosure replaces native details`)
  const toggle = panel.getByRole('button', { name: 'Подробная диагностика', exact: true })
  assert.equal(await toggle.count(), 1)
  assert.equal(await toggle.getAttribute('aria-expanded'), 'false')
  const diagnosticsID = await toggle.getAttribute('aria-controls')
  assert.ok(diagnosticsID)
  const diagnostics = panel.locator(`[id=${JSON.stringify(diagnosticsID)}]`)
  assert.equal(await panel.getByRole('heading', { name: 'Система', exact: true }).count(), 0, 'collapsed diagnostics are hidden from accessibility tree')
  await toggle.click()
  assert.equal(await toggle.getAttribute('aria-expanded'), 'true')
  assert.equal(await panel.evaluate((element, id) => element.querySelector(`[id="${CSS.escape(id)}"]`)?.getAttribute('aria-hidden'), diagnosticsID), 'false')
  await diagnostics.waitFor({ state: 'visible' })
  const metric = (label) => panel.locator('.telemetry-panel__metric').filter({ has: panel.page().getByText(label, { exact: true }) })
  await until(async () => (await metric('Загрузка CPU').innerText()).includes(`${cpu}%`), `${description} SSE CPU in diagnostics`)
  await until(async () => (await metric('Токенов ответа').innerText()).includes(`${tokens} токенов`), `${description} inference_progress output tokens`)
  for (const [label, hash] of [['Bundle', bundleHash], ['Контекст', contextHash]]) {
    const copy = panel.getByRole('button', { name: `Копировать ${label}: ${hash}`, exact: true })
    assert.equal(await copy.innerText(), `${hash.slice(0, 6)}...${hash.slice(-4)}`)
    assert.equal(await copy.getAttribute('title'), hash)
    await copy.click()
    await until(async () => await copy.getAttribute('data-state') === 'success', `${description} ${label} clipboard success`)
    assert.equal(await panel.page().evaluate(() => navigator.clipboard.readText()), hash, 'clipboard retains full hash')
  }
  const originalStyle = await panel.getAttribute('style')
  try {
    await panel.evaluate((element) => { element.style.width = '330px'; element.style.maxWidth = 'none'; element.style.boxSizing = 'border-box' })
    assert.equal(await panel.evaluate((element) => Math.round(element.getBoundingClientRect().width)), 330)
    await assertTelemetryOverflow(panel, `${description} expanded 330px panel`)
    await toggle.click()
    assert.equal(await toggle.getAttribute('aria-expanded'), 'false')
    await assertTelemetryOverflow(panel, `${description} collapsed 330px panel`)
    await toggle.click()
  } finally {
    await panel.evaluate((element, style) => { if (style === null) element.removeAttribute('style'); else element.setAttribute('style', style) }, originalStyle)
  }
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
  await page.context().grantPermissions(['clipboard-read', 'clipboard-write'], { origin: base })
  page.on('pageerror', (error) => failures.push(error.message))
  await page.route('**/api/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const parts = url.pathname.split('/').filter(Boolean)
    const method = request.method()
    const json = (value, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(value) })
    if (parts[1] === 'tasks' && parts[2] === activeWorkspaceTask.id && method === 'GET') workspaceEventRequests.push(`${parts.slice(3).join('/') || 'task'}`)
    if (parts[1] === 'doctor') return doctorBlocked ? route.abort('blockedbyclient') : json({ status: 'ok', checks: { app: { status: 'ok' }, database: { status: 'ok' }, worker: { status: 'ok' }, llm: { status: 'ok' } } })
    if (parts[1] === 'health') return healthMalformed ? route.fulfill({ status: 200, contentType: 'application/json', body: '{invalid json' }) : json({ status: 'ok', database: 'ok' })
    if (parts[1] === 'providers') return providersFail ? json({ error: { message: 'Провайдеры недоступны' } }, 503) : json({ search: { providers: {} } })
    if (parts[1] === 'memories') return json([])
    if (parts[1] === 'tasks') {
      if (!parts[2]) return json([legacyTask, activeWorkspaceTask])
      if (parts[2] === activeWorkspaceTask.id) {
        if (!parts[3]) return json(activeWorkspaceTask)
        if (parts[3] === 'runtime') return json(runtimeFixture(activeWorkspaceTask.id))
        if (parts[3] === 'messages') return json([])
        if (parts[3] === 'events' && parts[4] === 'stream') {
          if (workspaceRuntimeGetUnavailable) await new Promise((resolve) => setTimeout(resolve, 250))
          activeWorkspaceTask.status = 'verifying'
          const after = Number(url.searchParams.get('after') || request.headers()['last-event-id'] || 0)
          const selected = workspaceSSEEvents.filter((event) => event.seq > after)
          const runtime = runtimeFixture(activeWorkspaceTask.id, 1)
          runtime.telemetry.system.cpu_usage.value = 31
          runtime.telemetry.system.gpus[0].temperature.value = 61
          runtime.telemetry.system.ram_free.value = 7 * 1024 ** 3
          runtime.telemetry.inference.output_tokens.value = 6
          const inference = { ...runtime.telemetry.inference, output_tokens: { ...runtime.telemetry.inference.output_tokens, value: 12 } }
          const telemetryFrames = `event: snapshot\ndata: ${JSON.stringify(runtime)}\n\n` +
            `event: runtime_snapshot\ndata: ${JSON.stringify({ telemetry: runtime.telemetry, telemetry_revision: 2 })}\n\n` +
            `event: inference_progress\ndata: ${JSON.stringify({ inference, telemetry_revision: 3 })}\n\n` +
            `event: resource_warning\ndata: ${JSON.stringify({ warnings: ['runtime warning fixture'] })}\n\n`
          const body = 'retry: 10000\n\n' + telemetryFrames + selected.map((event) => `id: ${event.seq}\nevent: ${event.type}\ndata: ${JSON.stringify(event)}\n\n`).join('')
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
        const body = request.postDataJSON()
        submissionKeys.push(request.headers()['idempotency-key'])
        if (body.research_run_id) researchContextSubmissions.push(body.research_run_id)
        await new Promise((resolve) => setTimeout(resolve, 250))
        if (rejectNext) { rejectNext = false; return json({ error: { code: 'unavailable', message: 'Временный сетевой сбой', retryable: true } }, 503) }
        const result = submit(id, body.content, body.mode)
        if (body.research_run_id) result.user_message.metadata = { research_run_id: body.research_run_id, research_context: { run_id: body.research_run_id, verification_status: 'supported', sources: [{ evidence_id: 'ev-1', title: 'Реальный источник', canonical_url: 'https://example.org/source' }] } }
        return json(result, 202)
      }
    }
    if (parts[1] === 'runs') {
      if (parts[3] === 'runtime' && [legacyTask.id, activeWorkspaceTask.id].includes(parts[2])) {
        if (parts[2] === activeWorkspaceTask.id && workspaceRuntimeGetUnavailable) return json({ error: { message: 'Workspace runtime unavailable' } }, 503)
        return json(runtimeFixture(parts[2]))
      }
      const run = runs[parts[2]]
      if (!run) return json({ error: { message: 'Run not found' } }, 404)
      if (!parts[3]) return json(run)
      if (run.id === 'r-e') {
        if (parts[3] === 'runtime') return json({ task_id: run.id, cursor: 1, status: 'failed', stage: 'planning', counters: { llm_calls: 2, search_queries: 0, fetches: 0 } })
        if (parts[3] === 'plan') return json({ error: { message: 'Детали исследования недоступны' } }, 404)
        if (['sources', 'evidence'].includes(parts[3])) return json([])
      }
      if (parts[3] === 'runtime') return runtimeGetUnavailable && run.id === 'r-b' ? json({ error: { message: 'Runtime snapshot unavailable' } }, 503) : json({ ...runtimeFixture(run.id, events[run.id]?.at(-1)?.seq || 0), status: run.status, terminal_outcome: run.terminal_outcome, pause_reason: run.pause_reason || '', resource_reason: run.resource_reason || '', verdict: run.id === 'r-b' ? 'PARTIAL' : run.id === 'r-c' ? 'RESEARCH' : undefined, completed_steps: 1, compactions: 1, max_continuations: 1, completion: { continuations: 0 } })
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
        let telemetryFrames = ''
        if (run.id === 'r-b') {
          if (runtimeGetUnavailable) await new Promise((resolve) => setTimeout(resolve, 250))
          const runtime = runtimeFixture(run.id, events[run.id]?.at(-1)?.seq || 0)
          runtime.status = run.status
          runtime.terminal_outcome = run.terminal_outcome
          runtime.verdict = 'PARTIAL'
          runtime.telemetry.system.cpu_usage.value = 37
          runtime.telemetry.system.gpus[0].temperature.value = 63
          runtime.telemetry.system.ram_free.value = 6 * 1024 ** 3
          runtime.telemetry.inference.output_tokens.value = 5
          const inference = { ...runtime.telemetry.inference, output_tokens: { ...runtime.telemetry.inference.output_tokens, value: 9 } }
          telemetryFrames = `event: snapshot\ndata: ${JSON.stringify(runtime)}\n\n` +
            `event: runtime_snapshot\ndata: ${JSON.stringify({ telemetry: runtime.telemetry, telemetry_revision: 2 })}\n\n` +
            `event: inference_progress\ndata: ${JSON.stringify({ inference, telemetry_revision: 3 })}\n\n` +
            `event: resource_warning\ndata: ${JSON.stringify({ warnings: ['chat warning fixture'] })}\n\n`
        }
        const body = 'retry: 10000\n\n' + telemetryFrames + selected.map((event) => `id: ${event.seq}\nevent: ${event.type}\ndata: ${JSON.stringify(event)}\n\n`).join('')
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
  const priorFailures = initialDebug.requests.by_endpoint['GET /api/health']?.failures || 0
  healthMalformed = true
  await page.reload()
  await page.getByText('Backend: нет связи', { exact: true }).waitFor()
  await page.waitForFunction((before) => (window.__qwenchikDebug?.snapshot().requests.by_endpoint['GET /api/health']?.failures || 0) > before, priorFailures)
  healthMalformed = false
  await page.reload()
  await page.getByText('Backend: доступен', { exact: true }).waitFor()
  doctorBlocked = false
  await page.locator('a[href="#/chat/b"]').click()
  await page.getByRole('heading', { name: 'Результат', exact: true }).waitFor()
  const chatRunStatus = page.locator('.chat-heading p')
  await until(async () => (await chatRunStatus.innerText()).includes('Частичный результат'), 'partial terminal status')
  assert.match(await chatRunStatus.innerText(), /Частичный результат/)
  await page.locator('.chat-role-assistant .chat-message-meta').getByText('Частичный результат сохранён', { exact: true }).waitFor()
  const readiness = page.getByRole('region', { name: 'Проверка критериев', exact: true })
  await readiness.getByText('Дополнительные параметры совместимости не подтверждены.', { exact: true }).waitFor()
  await readiness.getByText(/Оценки критериев/).click()
  await readiness.getByText('ev-1', { exact: true }).waitFor()
  await page.getByText('Источники временно недоступны', { exact: true }).waitFor()
  for (const name of ['Поисковые запросы', 'Поисковые провайдеры', 'Действия']) {
    assert.equal(await page.locator('.chat-research').getByRole('button', { name, exact: true }).getAttribute('aria-expanded'), 'false', `${name} starts collapsed`)
  }
  await expandResearchSections(page)
  await page.getByText('actual public search query', { exact: true }).waitFor()
  await page.getByText('Проверяю официальные источники', { exact: true }).waitFor()
  await page.getByText('Источник не ответил вовремя. Проверьте доступные материалы ниже.', { exact: true }).waitFor()
  assert.ok(!(await page.locator('body').innerText()).includes('PRIVATE-'))
  const researchPlan = page.locator('.chat-research section').filter({ has: page.getByRole('heading', { name: 'План', exact: true }) })
  assert.equal(await researchPlan.locator('ol').count(), 1, 'plan has one ordered list')
  assert.equal(await researchPlan.locator('ol > li[data-status="completed"]').count(), 1)
  assert.ok((await researchPlan.innerText()).includes('Завершено'))
  assert.equal(await page.locator('.chat-step-board').count(), 0, 'plan is not duplicated as a step board')
  const progress = page.getByRole('progressbar', { name: 'Выполнено этапов', exact: true })
  assert.equal(await progress.getAttribute('value'), '1')
  assert.equal(await progress.getAttribute('max'), '1')
  assert.equal(await page.locator('.chat-markdown a[href="https://example.org/source"]').count(), 2)
  const citationSources = page.locator('.chat-citation-sources').first()
  await citationSources.locator('summary').click()
  await citationSources.getByRole('link', { name: 'Реальный источник', exact: true }).waitFor()
  assert.equal(await citationSources.getByText('example.org', { exact: true }).count(), 1)
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
  const chatTelemetry = page.locator('.telemetry-panel').first()
  await checkTelemetry(chatTelemetry, { cpu: 37, tokens: 9, temperature: 63, ram: 6, description: 'Chat' })
  if (process.env.TELEMETRY_SCREENSHOTS) {
    await chatTelemetry.getByRole('button', { name: 'Подробная диагностика', exact: true }).click()
    await page.locator('.chat-research-desktop').evaluate((element) => { element.scrollTop = 0 })
    await page.waitForTimeout(200)
    await page.screenshot({ path: '/tmp/opencode/telemetry-desktop.png' })
  }
  await chatTelemetry.getByRole('list', { name: 'Предупреждения runtime', exact: true }).getByText('chat warning fixture', { exact: true }).waitFor()
  const pdfFailure = events['r-b'].find((item) => item.type === 'tool.failed' && item.payload.error_code === 'pdf_source_too_large')
  for (const [type, payload] of [
    ['source.updated', { ...pdfFailure.payload, fallback_attempted: true }],
    ['tool.started', { tool: 'fetch_url', step_id: 'step2', target_url: 'https://example.org/source' }],
    ['source.extracted', { tool: 'fetch_url', step_id: 'step2', plan_attempt: 1, attempt_id: 'html-attempt', target_url: 'https://example.org/source' }],
  ]) events['r-b'].push({ id: `research-event-${events['r-b'].length}`, task_id: 'r-b', seq: events['r-b'].length + 1, visibility: 'user', schema_version: 1, created_at: now, type, payload })
  await page.reload()
  await expandResearchSections(page)
  await pdfCard.getByText('Попытка перехода к другому источнику подтверждена.', { exact: true }).waitFor()
  assert.equal(await pdfCard.count(), 1)
  assert.equal(await pdfCard.getByText('Код: pdf_source_too_large', { exact: true }).count(), 1)
  const previousEvidenceRequests = evidenceRequests
  const retrySources = page.getByRole('button', { name: 'Повторить загрузку', exact: true })
  await retrySources.waitFor()
  await page.waitForTimeout(500)
  await retrySources.waitFor()
  sourcesFail = false
  await retrySources.click()
  await page.getByRole('link', { name: 'Реальный источник', exact: true }).waitFor()
  assert.equal(evidenceRequests, previousEvidenceRequests)
  assert.ok(sourceRequests >= 2)
  planFail = true
  providersFail = true
  await page.reload()
  await page.getByRole('heading', { name: 'Результат', exact: true }).waitFor()
  await expandResearchSections(page)
  await page.getByText('План недоступен', { exact: true }).waitFor()
  await page.getByText('Провайдеры недоступны', { exact: true }).waitFor()
  assert.equal(await page.locator('.chat-markdown a[href="https://example.org/source"]').count(), 2)
  planFail = false
  providersFail = false
  runtimeGetUnavailable = true
  await page.reload()
  await page.getByText('Runtime snapshot unavailable', { exact: true }).waitFor()
  await page.waitForFunction(() => !document.body.innerText.includes('Runtime snapshot unavailable'))
  await expandResearchSections(page)
  runtimeGetUnavailable = false
  messagesFail = true
  await page.reload()
  await page.getByText('Сеть недоступна', { exact: true }).waitFor()
  await expandResearchSections(page)
  messagesFail = false

  const detailsToggle = page.getByRole('button', { name: 'Детали', exact: true })
  if (await detailsToggle.getAttribute('aria-expanded') === 'false') await detailsToggle.click()
  for (const width of [320, 375, 414, 768, 1440]) {
    await page.setViewportSize({ width, height: 900 })
    await until(async () => width > 1120 || await page.locator('dialog[open]').count() > 0, `responsive details drawer at ${width}`)
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `page overflow at ${width}`)
    if (width === 375) {
      const mobileTelemetry = page.locator('.telemetry-panel').first()
      const toggle = mobileTelemetry.getByRole('button', { name: 'Подробная диагностика', exact: true })
      if (await toggle.getAttribute('aria-expanded') === 'false') await toggle.click()
      await assertTelemetryOverflow(mobileTelemetry, 'Chat expanded mobile 375px')
      await toggle.click()
      await assertTelemetryOverflow(mobileTelemetry, 'Chat collapsed mobile 375px')
      if (process.env.TELEMETRY_SCREENSHOTS) {
        await page.locator('.chat-drawer-body').evaluate((element) => { element.scrollTop = 0 })
        await page.screenshot({ path: '/tmp/opencode/telemetry-mobile.png' })
      }
    }
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
  await page.getByRole('button', { name: 'Продолжить с контекстом исследования', exact: true }).click()
  await page.getByText(/Research context: ON/, { exact: false }).waitFor()
  const bridgeInput = page.getByRole('textbox', { name: 'Сообщение', exact: true })
  await bridgeInput.fill('Explain the checked findings, briefly.')
  await page.getByRole('button', { name: 'Отправить', exact: true }).click()
  await page.getByRole('button', { name: 'Остановить', exact: true }).waitFor()
  assert.equal(researchContextSubmissions.at(-1), 'r-b', 'explicit Chat continuation attaches only the selected completed research run')
  const bridgeCitationSources = page.locator('.chat-citation-sources').last()
  const bridgeCitationSummary = bridgeCitationSources.locator('summary')
  await bridgeCitationSummary.evaluate((element) => element.scrollIntoView({ block: 'center' }))
  await bridgeCitationSummary.click()
  await bridgeCitationSources.getByRole('link', { name: 'Реальный источник', exact: true }).waitFor()
  await page.getByRole('button', { name: 'Очистить research context', exact: true }).click()
  assert.equal(await page.getByText(/Research context: ON/, { exact: false }).count(), 0)
  await page.getByRole('button', { name: 'Детали', exact: true }).click()
  await page.getByRole('link', { name: /Первый разговор/ }).click()
  await page.getByText('Ответ первого разговора', { exact: true }).waitFor()
  await page.getByRole('link', { name: /Заблокированная проверка/ }).click()
  await until(async () => (await chatRunStatus.innerText()).includes('Заблокировано проверкой'), 'blocked terminal status')
  assert.match(await chatRunStatus.innerText(), /Заблокировано проверкой/)
  await page.getByText('Обязательные требования пользователя не подтверждены источниками.', { exact: true }).waitFor()
  await page.getByText('Критическое требование: подтвердить запрошенный параметр.', { exact: true }).waitFor()
  await page.getByRole('link', { name: /Ресурсная пауза/ }).click()
  await until(async () => (await chatRunStatus.innerText()).includes('На паузе'), 'resource pause status')
  assert.match(await chatRunStatus.innerText(), /На паузе/)
  await page.locator('.chat-role-assistant .chat-message-meta').getByText('Ответ на паузе. Полученный текст сохранён.', { exact: true }).waitFor()
  assert.equal(await page.locator('.chat-role-assistant .chat-message-meta').getByText('Ответ поступает', { exact: true }).count(), 0)
  const unsafeResume = page.getByRole('button', { name: 'Возобновить', exact: true })
  await unsafeResume.waitFor()
  assert.equal(await unsafeResume.isDisabled(), true)
  await page.getByRole('link', { name: /Сбой планирования/ }).click()
  const planningDiagnostic = page.getByRole('status').filter({ has: page.getByRole('heading', { name: 'Планирование не завершено', exact: true }) })
  await planningDiagnostic.waitFor()
  await planningDiagnostic.getByText('План не был сохранён: ошибка произошла до начала поиска', { exact: true }).waitFor()
  await planningDiagnostic.getByText('Причина: Не задан шаг поиска', { exact: true }).waitFor()
  assert.match(await page.locator('.chat-action-error[role="alert"]').innerText(), /Не задан шаг поиска[\s\S]*planning[\s\S]*planner[\s\S]*2[\s\S]*steps: 2/)
  assert.match(await planningDiagnostic.innerText(), /Вызовы LLM\s+2\s+Поиски\s+0\s+Загрузки\s+0/)
  assert.equal(await page.getByRole('button', { name: 'Продолжить ответ', exact: true }).isDisabled(), true, 'previous run text must not enable continue for an empty failed assistant')
  assert.doesNotMatch(await page.locator('body').innerText(), /PRIVATE-PLAN|PRIVATE-COUNT|Детали исследования недоступны/)
  // Legacy error-code fallback still diagnoses planning when failure_stage is absent.
  delete events['r-e'][0].payload.failure_stage
  events['r-e'][0].stage = 'planning'
  events['r-e'][0].payload.error_code = 'invalid_model_output'
  runs['r-e'].error_code = 'invalid_model_output'
  await page.reload()
  await planningDiagnostic.waitFor()
  await planningDiagnostic.getByText('Код: invalid_model_output', { exact: true }).waitFor()
  assert.equal(await page.getByRole('button', { name: 'Продолжить ответ', exact: true }).isDisabled(), true)
  const retryRequest = page.waitForRequest((request) => request.url().endsWith('/api/runs/r-e/retry') && request.method() === 'POST')
  await planningDiagnostic.getByRole('button', { name: 'Повторить ответ', exact: true }).click()
  assert.ok((await retryRequest).headers()['idempotency-key'])
  await page.getByRole('button', { name: 'Остановить', exact: true }).waitFor()
  await page.getByRole('link', { name: /Первый разговор/ }).click()
  await page.getByText('Ответ первого разговора', { exact: true }).waitFor()
  const chatDetails = page.getByRole('button', { name: 'Детали', exact: true })
  if (await chatDetails.getAttribute('aria-expanded') === 'true') await chatDetails.click()
  const input = page.getByRole('textbox', { name: 'Сообщение', exact: true })
  await input.fill('Новый вопрос')
  await input.evaluate((element) => element.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, isComposing: true })))
  const firstChatSubmission = submissionKeys.length
  assert.equal(firstChatSubmission, researchContextSubmissions.length + 0)
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
  assert.equal(submissionKeys[firstChatSubmission], submissionKeys[firstChatSubmission + 1])
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
  workspaceRuntimeGetUnavailable = true
  await page.locator('.task-card').filter({ hasText: 'Активный поток Workspace' }).click()
  await page.getByText('Снимок runtime временно недоступен.', { exact: true }).waitFor()
  await page.waitForFunction(() => !document.body.innerText.includes('Снимок runtime временно недоступен.'))
  workspaceRuntimeGetUnavailable = false
  await page.getByText('Workspace live draft', { exact: true }).waitFor()
  await page.locator('.inspector-header .status-tag').getByText('Проверка', { exact: true }).waitFor()
  const workspaceTelemetry = page.locator('.telemetry-panel').last()
  await checkTelemetry(workspaceTelemetry, { cpu: 31, tokens: 12, temperature: 61, ram: 7, description: 'Workspace' })
  await workspaceTelemetry.getByRole('list', { name: 'Предупреждения runtime', exact: true }).getByText('runtime warning fixture', { exact: true }).waitFor()
  await page.waitForTimeout(650)
  for (const endpoint of ['messages', 'events', 'checkpoints']) assert.equal(workspaceEventRequests.filter((item) => item === endpoint).length, 1, `${endpoint} should not refresh for unrelated SSE events`)
  for (const endpoint of ['steps', 'evidence', 'artifacts', 'approvals']) assert.equal(workspaceEventRequests.filter((item) => item === endpoint).length, 2, `${endpoint} should be coalesced to initial + one selective refresh`)
  assert.equal(workspaceEventRequests.filter((item) => item === 'task').length, 2, 'status changes refresh the selected task once')
  assert.equal(workspaceEventRequests.filter((item) => item === 'events/stream').length, 1, 'status transitions do not recreate the workspace SSE connection')
  for (const width of [320, 375, 414, 680, 768, 1440]) {
    await page.setViewportSize({ width, height: 900 })
    await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))))
    await assertPageOverflow(page, `workspace page overflow at ${width}`)
    if (width === 375) await assertTelemetryOverflow(workspaceTelemetry, 'Workspace expanded mobile 375px')
    await page.getByRole('heading', { name: 'Задачи', exact: true }).waitFor()
    await page.locator('.task-card').filter({ hasText: 'Активный поток Workspace' }).waitFor()
  }
  await page.setViewportSize({ width: 1440, height: 1000 })
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
  console.log('PASS: Chrome browser smoke; conversation isolation/refresh, markdown/citations, independent source failure/retry, SSE GPU/RAM ticker and CPU/output-token diagnostics, React disclosure, 330px telemetry panels/375px mobile overflow, abbreviated hashes/full clipboard values, widths 320/375/414/768/1440, drawer focus/Escape, optimistic failure/idempotent retry, streaming/reconnect/stop, continuation/retry, lost terminal polling, rename/create/research/delete, workspace/memory navigation.')
} finally {
  await browser?.close()
  server.kill('SIGTERM')
}
