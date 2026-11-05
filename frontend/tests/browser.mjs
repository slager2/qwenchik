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
let chatWarningIssued = false
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
  runtime: { enabled: true, keep_awake: true, resources: { free_ram_bytes: 8192, rss_bytes: 1024, gpu_free_vram_mb: null, gpu_temp_c: null, power_source: 'unknown', snapshot_at: new Date().toISOString(), warnings: task_id === 'r-b' && chatWarningIssued ? ['chat warning fixture'] : [] }, sleep: { active: false, available: false, provider: 'unavailable' } },
})
const conversation = (id, mode) => ({ id, title: id === 'a' ? 'Первый разговор' : 'Исследование', mode, created_at: now, updated_at: now, latest_run_id: `r-${id}` })
const conversations = [conversation('a', 'chat'), conversation('b', 'research'), { id: 'c', title: 'Заблокированная проверка', mode: 'research', created_at: now, updated_at: now, latest_run_id: 'r-c' }, { id: 'd', title: 'Ресурсная пауза', mode: 'research', created_at: now, updated_at: now, latest_run_id: 'r-d' }]
conversations.push({ ...conversation('e', 'research'), title: 'Сбой планирования' })
conversations.push({ ...conversation('f', 'research'), title: 'Поздний сбой планирования' })
conversations.push({ ...conversation('g', 'research'), title: 'Ручное удержание RSS' })
conversations.push({ ...conversation('h', 'research'), title: 'Проверка неизвестного исхода' })
const message = (id, conversation_id, run_id, role, content, status = 'completed', seq = 0) => ({ id, conversation_id, run_id, role, content, status, created_at: now, updated_at: now, last_event_seq: seq })
const messages = {
  a: [message('u-a', 'a', 'r-a', 'user', 'Первый вопрос'), message('m-a', 'a', 'r-a', 'assistant', 'Ответ первого разговора')],
  b: [message('u-b', 'b', 'r-b', 'user', 'Второй вопрос'), message('m-b', 'b', 'r-b', 'assistant', '## Результат\n\nПроверяемый факт [ev-1].\n\n| A | B |\n| - | - |\n| 1 | 2 |\n\n```js\n' + 'veryLongIdentifier'.repeat(60) + '\n```')],
  c: [message('u-c', 'c', 'r-c', 'user', 'Проверить критические критерии'), message('m-c', 'c', 'r-c', 'assistant', '', 'failed')],
  d: [message('u-d', 'd', 'r-d', 'user', 'Исследование на паузе'), message('m-d', 'd', 'r-d', 'assistant', 'Сохранённый фрагмент', 'streaming')],
  e: [message('old-e', 'e', 'old-e-run', 'assistant', 'Предыдущий непустой ответ'), message('u-e', 'e', 'r-e', 'user', 'Составить план'), message('m-e', 'e', 'r-e', 'assistant', '', 'failed')],
  f: [message('u-f', 'f', 'r-f', 'user', 'Составить роадмап'), message('m-f', 'f', 'r-f', 'assistant', '', 'streaming')],
}
const runs = Object.fromEntries(conversations.map((item) => [item.latest_run_id, { id: item.latest_run_id, conversation_id: item.id, user_message_id: `u-${item.id}`, assistant_message_id: `m-${item.id}`, mode: item.mode, operation: 'message', status: 'completed', retry_count: 0 }]))
messages.b[1].content += '\n\n## Ограничения\n\n' + 'Частичный отчёт с сохранёнными ограничениями. '.repeat(50) + '\n\n### Детали ограничения\n\nМатериал не означает независимую проверку.'
for (const id of ['g', 'h']) {
  messages[id] = [message(`u-${id}`, id, `r-${id}`, 'user', 'Сохранённое исследование'), message(`m-${id}`, id, `r-${id}`, 'assistant', '', 'paused')]
  runs[`r-${id}`] = { ...runs[`r-${id}`], status: 'paused', pause_reason: id === 'g' ? 'manual' : 'recovery_required', resource_reason: id === 'g' ? 'high_model_rss' : 'fetch_outcome_unknown', stage: 'verification' }
}
const heldRuntime = run => ({ task_id: run.id, cursor: 0, status: 'paused', pause_reason: run.pause_reason, resource_reason: run.resource_reason, profile: 'standard', counters: { llm_calls: 10, search_queries: 2, fetches: 2, evidence: 2 }, research_report: { artifact_id: '', markdown_artifact_id: '', outcome: 'completed', sections: 4, unrepresented_criteria: null }, runtime: { enabled: true } })
messages.b[1].metadata = { verification: { status: 'supported' } }
messages.b[1].citations = [{ evidence_id: 'ev-1', url: 'https://example.org/source' }]
runs['r-b'].terminal_outcome = 'partial'
runs['r-c'] = { ...runs['r-c'], status: 'failed', terminal_outcome: 'blocked', error_code: 'completion_blocked', error_message: 'Проверка не смогла подтвердить обязательные критерии.' }
runs['r-d'] = { ...runs['r-d'], status: 'paused', pause_reason: 'resource_guard', resource_reason: 'low_ram', stage: 'verification' }
runs['r-e'] = { ...runs['r-e'], status: 'failed', stage: 'planning', error_code: 'research_plan_incomplete' }
runs['r-f'] = { ...runs['r-f'], status: 'planning', stage: 'planning' }
const events = {}
events['r-f'] = []
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
let planningStreamMode = 'resync'
let planningHistoryRequests = 0
let latePlanningHistoryRequests = 0
let rejectNext = false
let sourcesFail = true
let planFail = false
let providersFail = false
let messagesFail = false
let runtimeGetUnavailable = false
let workspaceRuntimeGetUnavailable = false
let doctorBlocked = true
let doctorRequests = 0
let policyRequests = 0
let settingsReject = false
const settingsWrites = []
let healthMalformed = false
let sourceRequests = 0
let evidenceRequests = 0
let legacyEvidenceDone = false
const legacyTask = { id: 'legacy-task', title: 'Существующая задача', description: 'Проверить сохранённую задачу', status: 'failed', priority: 0, version: 1, created_at: now, updated_at: now }
const activeWorkspaceTask = { id: 'active-workspace-task', title: 'Активный поток Workspace', description: 'Проверить выборочную синхронизацию', status: 'running', stage: 'fetch', priority: 0, version: 1, created_at: now, updated_at: now }
const pausedWorkspaceTasks = ['manual', 'resource_guard', 'recovery_required'].map((pause_reason, index) => ({ ...legacyTask, id: `workspace-hold-${index}`, title: `Workspace ${pause_reason}`, status: 'paused', pause_reason, resource_reason: 'high_model_rss' }))
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
  await openDetails(page)
  const research = page.locator('.chat-research')
  await research.waitFor({ state: 'visible' })
  const diagnostics = research.getByRole('button', { name: 'Диагностика запуска', exact: true })
  if (await diagnostics.getAttribute('aria-expanded') === 'false') await diagnostics.click()
  for (const name of ['Поисковые запросы', 'Поисковые провайдеры', 'Действия']) {
    const toggle = research.getByRole('button', { name, exact: true })
    await toggle.waitFor({ state: 'visible' })
    if (await toggle.getAttribute('aria-expanded') === 'false') await toggle.click()
    assert.equal(await toggle.getAttribute('aria-expanded'), 'true', `${name} should be expanded`)
  }
  for (const toggle of await research.getByRole('button', { name: 'Сведения о загрузке', exact: true }).all()) {
    if (await toggle.getAttribute('aria-expanded') === 'false') await toggle.click()
  }
}

async function openDetails(page) {
  const toggle = page.getByRole('button', { name: 'Детали', exact: true })
  if (await toggle.getAttribute('aria-expanded') === 'false') await toggle.click()
}

async function assertSurfaceLayout(page, description) {
  const issues = await page.evaluate(() => {
    const modal = document.querySelector('dialog[open]')
    const root = modal || document.querySelector('.conversation-shell') || document.body
    const issues = []
    for (const element of root.querySelectorAll('button, input, select, textarea, summary, h1, h2, .research-overview, .chat-composer')) {
      const r = element.getBoundingClientRect()
      if (!r.width || !r.height || element.closest('[inert], [aria-hidden="true"]') || getComputedStyle(element).visibility === 'hidden' || r.bottom <= 0 || r.top >= innerHeight) continue
      const control = element.matches('button, input, select, textarea, summary')
      if (r.left < -1 || r.right > innerWidth + 1 || control && (r.width < 43.5 || r.height < 43.5)) issues.push({ tag: element.tagName, class: element.className, x: r.x, y: r.y, width: r.width, height: r.height })
    }
    return issues
  })
  assert.deepEqual(issues, [], `${description}: child bounds or 44px action targets`)
}

async function assertWorkspaceRail(page, width) {
  // The workspace has no private rail any more: on narrow screens the shared
  // shell top bar carries navigation and the page actions.
  const bar = page.locator('.workspace-page > .shell-topbar')
  await bar.scrollIntoViewIfNeeded()
  const measured = await bar.evaluate(element => {
    const rect = node => {
      const r = node.getBoundingClientRect()
      return { left: r.left, top: r.top, right: r.right, bottom: r.bottom, width: r.width, height: r.height }
    }
    const inside = (r, outer) => r.left >= outer.left - 1 && r.right <= outer.right + 1 && r.top >= outer.top - 1 && r.bottom <= outer.bottom + 1
    const overlaps = (a, b) => Math.min(a.right, b.right) - Math.max(a.left, b.left) > 1 && Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top) > 1
    const viewport = { left: 0, top: 0, right: innerWidth, bottom: innerHeight }
    const frame = rect(element)
    const issues = []
    const controls = [...element.querySelectorAll('a[href], button')].filter(control => control.getClientRects().length && getComputedStyle(control).visibility !== 'hidden')
    if (!controls.some(control => control.getAttribute('aria-label') === 'Открыть навигацию')) issues.push('navigation trigger missing')
    const actions = controls.map(control => {
      const bounds = rect(control)
      const label = control.getAttribute('aria-label') || control.textContent.trim()
      if (!inside(bounds, viewport) || !inside(bounds, frame)) issues.push(`${label}: action clipped`)
      if (bounds.width < 43.5 || bounds.height < 43.5) issues.push(`${label}: action smaller than 44px`)
      return { label, ...bounds }
    })
    for (let i = 0; i < actions.length; i++) for (let j = i + 1; j < actions.length; j++) {
      if (overlaps(actions[i], actions[j])) issues.push(`${actions[i].label}/${actions[j].label}: action overlap`)
    }
    const heading = element.querySelector('h1')
    if (!heading) issues.push('heading missing')
    else if (!inside(rect(heading), frame)) issues.push('heading clipped')
    return { issues, frame, actions }
  })
  assert.deepEqual(measured.issues, [], `Workspace top bar ${width}px: ${JSON.stringify(measured)}`)
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
  if (content === 'Проверка автопрокрутки') events[runID][1].payload.delta += '\n\n' + 'Новый фрагмент длинного потокового ответа.\n\n'.repeat(35) + 'Хвост потока'
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
    if (parts[1] === 'doctor') { doctorRequests++; return doctorBlocked ? route.abort('blockedbyclient') : json({ status: 'ok', checks: { app: { status: 'ok' }, database: { status: 'ok' }, worker: { status: 'ok' }, llm: { status: 'ok' } } }) }
    if (parts[1] === 'lab' && parts[2] === 'policy') { policyRequests++; return json({ hold_resource_pauses: false, research_limits: { configured: true, privacy_mode: 'local_inference_external_search', adaptive_enabled: true, sectioned_report_enabled: true, pdf_allow_unsandboxed_fallback: false, task_fetch_budget_bytes: 134217728, source_fetch_bytes: 16777216, pdf_bytes: 67108864 } }) }
    if (parts[1] === 'health') return healthMalformed ? route.fulfill({ status: 200, contentType: 'application/json', body: '{invalid json' }) : json({ status: 'ok', database: 'ok', model: { id: 'local-qwen', status: 'configured' }, worker: 'attached', worker_details: { status: 'fresh', busy: false }, runtime: { enabled: true } })
    if (parts[1] === 'providers') return providersFail ? json({ error: { message: 'Провайдеры недоступны' } }, 503) : json({ search: { providers: {} } })
    if (parts[1] === 'memories') return json([])
    if (parts[1] === 'tasks') {
      if (!parts[2]) return json([legacyTask, activeWorkspaceTask, ...pausedWorkspaceTasks])
      const heldTask = pausedWorkspaceTasks.find(task => task.id === parts[2])
      if (heldTask) {
        if (!parts[3]) return json(heldTask)
        if (parts[3] === 'events' && parts[4] === 'stream') return route.fulfill({ contentType: 'text/event-stream', body: ': idle\n\n' })
        return json([])
      }
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
        settingsWrites.push({ method, body })
        const next = { id: `c-${++counter}`, ...body, created_at: now, updated_at: now }
        conversations.push(next)
        messages[next.id] = []
        return json(next, 201)
      }
      const conv = conversations.find((item) => item.id === id)
      if (!conv) return json({ error: { message: 'Разговор не найден' } }, 404)
      if (!parts[3] && method === 'GET') return json(conv)
      if (!parts[3] && method === 'PATCH') { const body = request.postDataJSON(); settingsWrites.push({ method, body }); if (settingsReject) { settingsReject = false; return json({ error: { message: 'Сервер отклонил настройки', code: 'invalid_conversation' } }, 400) } Object.assign(conv, body); return json(conv) }
      if (!parts[3] && method === 'DELETE') { conv.archived_at = now; return route.fulfill({ status: 204 }) }
      if (parts[3] === 'messages' && method === 'GET') return messagesFail ? json({ error: { message: 'Сеть недоступна' } }, 503) : json(messages[id])
      if (parts[3] === 'messages' && method === 'POST') {
        const body = request.postDataJSON()
        submissionKeys.push(request.headers()['idempotency-key'])
        if (body.research_run_id) researchContextSubmissions.push(body.research_run_id)
        await new Promise((resolve) => setTimeout(resolve, 750))
        if (rejectNext) { rejectNext = false; return json({ error: { code: 'unavailable', message: 'Временный сетевой сбой', retryable: true } }, 503) }
        const result = submit(id, body.content, body.mode)
        if (body.research_run_id) result.user_message.metadata = { research_run_id: body.research_run_id, research_context: { run_id: body.research_run_id, verification_status: 'supported', sources: [{ evidence_id: 'ev-1', title: 'Реальный источник', canonical_url: 'https://example.org/source' }] } }
        return json(result, 202)
      }
    }
    if (parts[1] === 'runs') {
      const heldTask = pausedWorkspaceTasks.find(task => task.id === parts[2])
      if (heldTask && parts[3] === 'runtime') return json({ ...heldRuntime({ ...heldTask, id: heldTask.id }), task_id: heldTask.id })
      if (parts[3] === 'runtime' && [legacyTask.id, activeWorkspaceTask.id].includes(parts[2])) {
        if (parts[2] === activeWorkspaceTask.id && workspaceRuntimeGetUnavailable) return json({ error: { message: 'Workspace runtime unavailable' } }, 503)
        return json(runtimeFixture(parts[2]))
      }
      const run = runs[parts[2]]
      if (!run) return json({ error: { message: 'Run not found' } }, 404)
      if (!parts[3]) return json(run)
      if (['r-g', 'r-h'].includes(run.id)) {
        if (parts[3] === 'runtime') return json(heldRuntime(run))
        if (parts[3] === 'evidence') return json([1, 2].map(n => ({ id: `${run.id}-ev-${n}`, task_id: run.id, title: `Сохранённый материал ${n}`, canonical_url: `https://example.org/held-${n}`, excerpt: 'Сохранённый фрагмент', provider: 'offline', extraction_method: 'text', retrieved_at: now })))
        if (parts[3] === 'events' && parts[4] === 'stream') return route.fulfill({ contentType: 'text/event-stream', body: ': held\n\n' })
        if (parts[3] === 'plan') return json(null)
        return json([])
      }
      if (run.id === 'r-f') {
        if (parts[3] === 'events' && parts[4] === 'stream') {
          await new Promise((resolve) => setTimeout(resolve, 1000))
          run.status = 'failed'
          run.error_code = 'research_plan_incomplete'
          run.terminal_outcome = 'blocked'
          events[run.id] = [{ id: 'late-failure', task_id: run.id, seq: 1, type: 'task.failed', visibility: 'user', schema_version: 1, created_at: now, payload: { failure_stage: 'planning', failure_role: 'planner_correction', planner_attempt: 2, validation_error: 'Поздняя диагностическая причина', error_code: 'research_plan_incomplete' } }]
          const snapshot = { task_id: run.id, cursor: 1, status: 'failed', stage: 'planning', terminal_outcome: 'blocked' }
          return route.fulfill({ contentType: 'text/event-stream', body: `event: snapshot\ndata: ${JSON.stringify(snapshot)}\n\n` })
        }
        if (parts[3] === 'events') latePlanningHistoryRequests++
        if (parts[3] === 'plan') return json({ error: { message: 'План не создан' } }, 404)
      }
      if (run.id === 'r-e') {
        if (parts[3] === 'runtime') return json({ task_id: run.id, cursor: 1, status: 'failed', stage: 'planning', counters: { llm_calls: 2, search_queries: 0, fetches: 0 } })
        if (parts[3] === 'plan') return json({ error: { message: 'Детали исследования недоступны' } }, 404)
        if (['sources', 'evidence'].includes(parts[3])) return json([])
        if (parts[3] === 'events' && parts[4] === 'stream') {
          if (planningStreamMode === 'offline') return route.abort('connectionfailed')
          if (planningStreamMode === 'resync') return route.fulfill({ contentType: 'text/event-stream', body: 'event: resync\ndata: {"cursor":1}\n\n' })
        }
        if (parts[3] === 'events') planningHistoryRequests++
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
          if (runtimeGetUnavailable) await new Promise((resolve) => setTimeout(resolve, 1500))
          const runtime = runtimeFixture(run.id, events[run.id]?.at(-1)?.seq || 0)
          runtime.status = run.status
          runtime.terminal_outcome = run.terminal_outcome
          runtime.verdict = 'PARTIAL'
          runtime.telemetry.system.cpu_usage.value = 37
          runtime.telemetry.system.gpus[0].temperature.value = 63
          runtime.telemetry.system.ram_free.value = 6 * 1024 ** 3
          runtime.telemetry.inference.output_tokens.value = 5
          const inference = { ...runtime.telemetry.inference, output_tokens: { ...runtime.telemetry.inference.output_tokens, value: 9 } }
          // REST snapshots and SSE describe the same current warning state.
          chatWarningIssued = true
          telemetryFrames = `event: snapshot\ndata: ${JSON.stringify(runtime)}\n\n` +
            `event: runtime_snapshot\ndata: ${JSON.stringify({ telemetry: runtime.telemetry, telemetry_revision: 2 })}\n\n` +
            `event: inference_progress\ndata: ${JSON.stringify({ inference, telemetry_revision: 3 })}\n\n` +
            `event: resource_warning\ndata: ${JSON.stringify({ warnings: ['chat warning fixture'] })}\n\n`
        }
        const body = 'retry: 10000\n\n' + telemetryFrames + selected.map((event) => `id: ${event.seq}\nevent: ${event.type}\ndata: ${JSON.stringify(event)}\n\n`).join('')
        const assistant = messages[run.conversation_id].find((item) => item.id === run.assistant_message_id)
        if (assistant && selected.length && run.status === 'running' && assistant.last_event_seq <= 2) { assistant.content = (events[run.id] || []).filter(event => event.type === 'message.delta').map(event => event.payload.delta).join(''); assistant.last_event_seq = 2 }
        return route.fulfill({ contentType: 'text/event-stream', body })
      }
      if (parts[3] === 'events') return json((events[run.id] || []).filter((event) => event.seq > Number(url.searchParams.get('after') || 0)))
      if (parts[3] === 'sources') { sourceRequests++; return sourcesFail ? json({ error: { message: 'Источники временно недоступны' } }, 503) : json([{ id: 's-1', title: 'Реальный источник', url: 'https://example.org/source', status: 'extracted', provider: 'searxng' }]) }
      if (parts[3] === 'evidence') { evidenceRequests++; return json(run.mode === 'research' ? [{ id: 'ev-1', task_id: run.id, http_status: 200, title: 'Реальный источник', canonical_url: 'https://example.org/source', excerpt: 'Проверенный фрагмент', provider: 'searxng', extraction_method: 'html', retrieved_at: now, content_sha256: 'abc', quality_flags: [] }] : []) }
      if (parts[3] === 'plan') return planFail ? json({ error: { message: 'План недоступен' } }, 503) : json({ goal: 'Сравнить', subtasks: [{ id: 'step1', description: 'Проверить источники' }] })
    }
    return json({ error: { message: `Unmocked: ${method} ${url.pathname}` } }, 404)
  })

  // A real Phase 3 hold shape: checkpoints can contain completed sections while
  // no answer/artifact was delivered. Never promote those internal fields.
  for (const id of ['g', 'h', 'd']) {
    await page.goto(`${base}/#/chat/${id}`)
    const overview = page.locator('.research-overview')
    await overview.getByRole('heading', { name: 'Исследование на паузе', exact: true }).waitFor()
    const resume = overview.getByRole('button', { name: 'Возобновить', exact: true })
    assert.equal(await resume.isEnabled(), id === 'g', `${id}: backend-compatible resume eligibility`)
    if (id === 'g') {
      await overview.getByText('Сохранённых материалов: 2', { exact: true }).waitFor()
      assert.match(await overview.innerText(), /Превышен лимит памяти модели/)
      assert.match(await overview.innerText(), /backend повторно проверит ограничения; продолжение не гарантировано/)
      assert.match(await overview.innerText(), /Текст ответа пока отсутствует · Файл отчёта пока не сохранён/)
      assert.doesNotMatch(await page.locator('.chat-center').innerText(), /Готовлю ответ|Отчёт сохранён|Результат сохранён/)
      assert.equal(await page.getByRole('link', { name: /Скачать отчёт/ }).count(), 0)
    }
    await openDetails(page)
    assert.equal(await page.locator('.chat-research').getByRole('button', { name: 'Возобновить', exact: true }).isEnabled(), id === 'g', `${id}: Inspector uses same resume eligibility as Overview`)
  }
  for (const width of [320, 375, 414, 768, 1024, 1280, 1440]) {
    await page.setViewportSize({ width, height: 900 })
    await page.goto(`${base}/#/chat/g`)
    await page.locator('.research-overview').getByText('Сохранённых материалов: 2', { exact: true }).waitFor()
    assert.equal(await page.locator('dialog[open]').count(), 0, `research navigation must not open a modal at ${width}`)
    await page.reload()
    await page.locator('.research-overview').getByText('Сохранённых материалов: 2', { exact: true }).waitFor()
    assert.equal(await page.locator('dialog[open]').count(), 0, `research reload must not open a modal at ${width}`)
    await assertSurfaceLayout(page, `paused overview ${width}`)
  }
  await page.setViewportSize({ width: 375, height: 560 })
  await page.goto(`${base}/#/chat`)
  await page.getByRole('combobox', { name: 'Режим разговора' }).selectOption('research')
  assert.equal(await page.locator('dialog[open]').count(), 0, 'mobile mode selection leaves composer available')
  await page.addStyleTag({ content: 'html { font-size: 20px !important; }' })
  await page.getByRole('textbox', { name: 'Задача исследования', exact: true }).scrollIntoViewIfNeeded()
  assert.equal(await page.getByRole('textbox', { name: 'Задача исследования', exact: true }).isVisible(), true)
  assert.ok(await page.getByRole('textbox', { name: 'Задача исследования', exact: true }).evaluate(el => { const r = el.getBoundingClientRect(); return r.top >= 0 && r.bottom <= innerHeight }), 'short viewport composer is reachable inside viewport')
  await assertSurfaceLayout(page, 'short height enlarged text composer')
  await page.setViewportSize({ width: 900, height: 375 })
  await page.getByRole('textbox', { name: 'Задача исследования', exact: true }).scrollIntoViewIfNeeded()
  assert.ok(await page.getByRole('textbox', { name: 'Задача исследования', exact: true }).evaluate(el => { const r = el.getBoundingClientRect(); return r.top >= 0 && r.bottom <= innerHeight }), 'landscape composer is reachable inside viewport')
  await assertSurfaceLayout(page, 'landscape enlarged text composer')
  for (const width of [1440, 375]) {
    await page.setViewportSize({ width, height: 900 })
    await page.goto(`${base}/#/chat/b`)
    await page.reload() // Fresh saved-run mount, not resize of an already focused answer.
    await page.locator('.research-overview').getByRole('button', { name: 'Читать ответ', exact: true }).waitFor()
    await page.getByRole('heading', { name: 'Результат', exact: true }).waitFor()
    await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))))
    await page.waitForTimeout(250)
    const initialReading = await page.evaluate(() => {
      const center = document.querySelector('.chat-center')
      const transcript = document.querySelector('.chat-transcript')
      const article = transcript.querySelector('article[data-message-id="m-b"]')
      const frame = transcript.getBoundingClientRect()
      return { centerTop: center.scrollTop, transcriptTop: transcript.scrollTop, remaining: transcript.scrollHeight - transcript.scrollTop - transcript.clientHeight, frameHeight: frame.height, answerOffset: article.getBoundingClientRect().top - frame.top }
    })
    if (width === 1440) {
      assert.ok(initialReading.frameHeight >= 192, 'saved desktop report retains a readable transcript, not an 80px sliver')
      assert.ok(Math.abs(initialReading.answerOffset) <= 32, `desktop opens at assistant start: ${JSON.stringify(initialReading)}`)
      assert.ok(initialReading.remaining > 100, 'long saved partial report must not open at its end')
    } else {
      assert.equal(initialReading.centerTop, 0, 'saved mobile research starts at overview, not the composer/end')
      assert.equal(initialReading.transcriptTop, 0, 'mobile transcript is not an independent scroll surface')
      assert.equal(await page.locator('dialog[open]').count(), 0)
    }
    const hash = new URL(page.url()).hash
    await page.locator('.research-overview').getByRole('button', { name: 'Читать ответ', exact: true }).click()
    assert.equal(new URL(page.url()).hash, hash, 'Read answer preserves conversation hash')
    assert.ok(await page.locator('article[data-message-id="m-b"]').evaluate(element => document.activeElement === element && element.tabIndex === -1), 'Read answer focuses the selected saved assistant article')
    if (width === 375) {
      assert.ok(await page.locator('.chat-center').evaluate(element => element.scrollTop > 0), 'Read answer scrolls the mobile conversation surface')
      const composer = page.getByRole('textbox', { name: 'Задача исследования', exact: true })
      await composer.scrollIntoViewIfNeeded()
      assert.ok(await composer.evaluate(element => { const r = element.getBoundingClientRect(); return r.top >= 0 && r.bottom <= innerHeight }), 'composer remains reachable below a long saved mobile answer')
      assert.equal(await page.locator('.chat-transcript').evaluate(element => element.scrollTop), 0)
    }
    await assertSurfaceLayout(page, `saved report reading ${width}`)
  }
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.clock.install()
  await page.goto(`${base}/#/chat/a`)
  await page.getByText('Ответ первого разговора', { exact: true }).waitFor()
  await page.waitForFunction(() => typeof window.__qwenchikDebug?.snapshot === 'function')
  const initialDebug = await page.evaluate(() => window.__qwenchikDebug.snapshot())
  assert.ok(initialDebug.requests.count > 0, 'development diagnostics should observe initial API requests')
  assert.ok(Object.keys(initialDebug.requests.by_endpoint).every((endpoint) => endpoint.startsWith('GET /api/')), 'HTTP endpoint aggregation should use normalized paths')
  assert.ok(!JSON.stringify(initialDebug).includes('PRIVATE-'), 'debug snapshot must not include request payloads')
  assert.equal(initialDebug.marker, 'QWENCHIK_DEV_NETWORK_DIAGNOSTICS_V1')
  assert.equal(doctorRequests, 0, 'all initial navigation is passive')
  const policyBefore = policyRequests
  await page.clock.fastForward(120000)
  await until(() => policyRequests > policyBefore, 'passive policy refresh')
  assert.equal(doctorRequests, 0, 'advancing two minutes must not run doctor')
  await page.clock.resume()
  await page.getByText('Приложение: доступно', { exact: true }).waitFor()
  await page.getByText('Qwen: локальная · проверка по запросу', { exact: true }).waitFor()
  const priorFailures = initialDebug.requests.by_endpoint['GET /api/health']?.failures || 0
  healthMalformed = true
  await page.reload()
  await page.getByText('Приложение: нет связи', { exact: true }).waitFor()
  await page.waitForFunction((before) => (window.__qwenchikDebug?.snapshot().requests.by_endpoint['GET /api/health']?.failures || 0) > before, priorFailures)
  healthMalformed = false
  await page.reload()
  await page.getByText('Приложение: доступно', { exact: true }).waitFor()
  doctorBlocked = false
  await page.locator('a[href="#/chat/b"]').click()
  await page.getByRole('heading', { name: 'Результат', exact: true }).waitFor()
  assert.equal(doctorRequests, 0, 'opening Chat/research must not probe doctor')
  await openDetails(page)
  const chatRunStatus = page.locator('.chat-heading p')
  await until(async () => (await chatRunStatus.innerText()).includes('Частичный результат'), 'partial terminal status')
  assert.match(await chatRunStatus.innerText(), /Частичный результат/)
  await page.locator('.chat-role-assistant .chat-message-meta').getByText('Частичный результат сохранён', { exact: true }).waitFor()
  const readiness = page.getByRole('region', { name: 'Проверка критериев', exact: true })
  await readiness.getByText('Дополнительные параметры совместимости не подтверждены.', { exact: true }).waitFor()
  await readiness.getByText(/Оценки критериев/).click()
  await readiness.getByText('ev-1', { exact: true }).waitFor()
  await page.getByText('Источники временно недоступны', { exact: true }).waitFor()
  await page.getByRole('button', { name: 'Диагностика запуска', exact: true }).click()
  for (const name of ['Поисковые запросы', 'Поисковые провайдеры', 'Действия']) {
    assert.equal(await page.locator('.chat-research').getByRole('button', { name, exact: true }).getAttribute('aria-expanded'), 'false', `${name} starts collapsed`)
  }
  await expandResearchSections(page)
  await page.getByRole('button', { name: 'Проверить модель и поиск', exact: true }).click()
  await until(() => doctorRequests === 1, 'one explicit Chat doctor request')
  await page.getByText('Qwen: проверена по запросу', { exact: true }).waitFor()
  await page.waitForTimeout(200)
  assert.equal(doctorRequests, 1, 'explicit diagnostics do not start a polling loop')
  await page.getByText('actual public search query', { exact: true }).waitFor()
  await page.getByText('Проверяю официальные источники', { exact: true }).waitFor()
  await page.getByText('Источник не ответил вовремя. Проверьте доступные материалы ниже.', { exact: true }).waitFor()
  assert.ok(!(await page.locator('body').innerText()).includes('PRIVATE-'))
  const researchPlan = page.locator('.chat-research section').filter({ has: page.getByRole('heading', { name: 'План', exact: true }) })
  assert.equal(await researchPlan.locator('ol').count(), 1, 'plan has one ordered list')
  assert.equal(await researchPlan.locator('ol > li[data-status="completed"]').count(), 1)
  assert.ok((await researchPlan.innerText()).includes('Завершено'))
  assert.equal(await page.locator('.chat-step-board').count(), 0, 'plan is not duplicated as a step board')
  const progress = page.getByRole('progressbar', { name: 'Обработано этапов плана', exact: true })
  assert.equal(await progress.getAttribute('value'), '1')
  assert.equal(await progress.getAttribute('max'), '1')
  assert.equal(await page.locator('.chat-markdown a[href="https://example.org/source"]').count(), 2)
  const citationSources = page.locator('.chat-citation-sources').first()
  await citationSources.locator('summary').click()
  await citationSources.getByRole('link', { name: 'Реальный источник', exact: true }).waitFor()
  assert.equal(await citationSources.getByText('example.org', { exact: true }).count(), 1)
  assert.equal(await page.getByText('Ответ первого разговора', { exact: true }).count(), 0)
  assert.equal(await page.locator('.chat-markdown table').count(), 1)
  const answer = page.getByRole('article', { name: 'Ответ Qwen', exact: true }).last()
  const outline = answer.getByRole('navigation', { name: 'Разделы ответа', exact: true })
  await outline.locator('summary').click()
  const hashBeforeOutline = new URL(page.url()).hash
  await outline.getByRole('button', { name: 'Детали ограничения', exact: true }).click()
  assert.equal(new URL(page.url()).hash, hashBeforeOutline, 'outline navigation does not change conversation hash')
  assert.ok(await answer.evaluate(element => element.contains(document.activeElement) && document.activeElement.textContent === 'Детали ограничения'), 'outline focuses heading in its own answer')
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
  const retrySources = page.getByRole('button', { name: 'Повторить загрузку', exact: true })
  await retrySources.waitFor()
  await page.waitForTimeout(500)
  await retrySources.waitFor()
  const previousEvidenceRequests = evidenceRequests
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
  await openDetails(page)
  await page.getByText('Runtime snapshot unavailable', { exact: true }).waitFor()
  await page.waitForFunction(() => !document.body.innerText.includes('Runtime snapshot unavailable'))
  await expandResearchSections(page)
  runtimeGetUnavailable = false
  messagesFail = true
  await page.reload()
  await page.getByText('Сеть недоступна', { exact: true }).waitFor()
  await expandResearchSections(page)
  assert.equal(await page.getByRole('button', { name: 'Продолжить с контекстом исследования', exact: true }).isDisabled(), true, 'missing current answer must not enable research context')
  messagesFail = false
  await page.reload()
  await page.getByRole('heading', { name: 'Результат', exact: true }).waitFor()
  await expandResearchSections(page)

  const detailsToggle = page.getByRole('button', { name: 'Детали', exact: true })
  for (const width of [320, 375, 414, 768, 1024, 1280, 1440]) {
    if (await detailsToggle.getAttribute('aria-expanded') === 'true') {
      if (await page.locator('dialog[open]').count()) await page.keyboard.press('Escape')
      else await detailsToggle.click()
    }
    await page.setViewportSize({ width, height: 900 })
    assert.equal(await page.locator('dialog[open]').count(), 0, `resize does not automatically open Details at ${width}`)
    await assertSurfaceLayout(page, `partial report ${width}`)
    await detailsToggle.click()
    await until(async () => width > 1120 || await page.locator('dialog[open]').count() > 0, `responsive details drawer at ${width}`)
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `page overflow at ${width}`)
    if (width === 375) {
      await expandResearchSections(page)
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
      await assertSurfaceLayout(page, `Details actions ${width}`)
      const close = dialog.getByRole('button', { name: 'Закрыть: Детали исследования', exact: true })
      await close.focus()
      await page.keyboard.press('Shift+Tab')
      if (!await dialog.evaluate(element => element.contains(document.activeElement) && document.activeElement !== element.querySelector('button'))) failures.push(`Details ${width}px: reverse Tab failed to wrap from first control past inert disclosures`)
      await dialog.evaluate(element => {
        const usable = [...element.querySelectorAll('a[href], button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [tabindex="0"]')].filter(el => el.getClientRects().length && !el.closest('[inert], [aria-hidden="true"]') && getComputedStyle(el).visibility !== 'hidden')
        usable.at(-1)?.focus()
      })
      await page.keyboard.press('Tab')
      if (!await close.evaluate(element => element === document.activeElement)) failures.push(`Details ${width}px: forward Tab failed to wrap from last usable control`)
      for (let index = 0; index < 12; index++) {
        await page.keyboard.press('Tab')
        assert.ok(await page.evaluate(() => document.querySelector('dialog[open]')?.contains(document.activeElement)), `focus escaped at ${width}`)
      }
      for (let index = 0; index < 12; index++) {
        await page.keyboard.press('Shift+Tab')
        assert.ok(await page.evaluate(() => document.querySelector('dialog[open]')?.contains(document.activeElement)), `reverse focus escaped at ${width}`)
      }
      await page.keyboard.press('Escape')
      await until(async () => await page.locator('dialog[open]').count() === 0, 'drawer close')
      assert.ok(await detailsToggle.evaluate(element => element === document.activeElement), 'Escape restores Details trigger focus')
    }
  }
  if (await detailsToggle.getAttribute('aria-expanded') === 'true') await detailsToggle.click()
  const contextContinue = page.getByRole('button', { name: 'Продолжить с контекстом исследования', exact: true })
  await until(() => contextContinue.isEnabled(), 'research context remains eligible with Details collapsed')
  await contextContinue.click()
  await page.getByText(/Контекст исследования · запуск/, { exact: false }).waitFor()
  const bridgeInput = page.getByRole('textbox', { name: 'Сообщение', exact: true })
  await bridgeInput.fill('Explain the checked findings, briefly.')
  await page.getByRole('button', { name: 'Отправить', exact: true }).click()
  await page.locator('.chat-composer').getByRole('button', { name: 'Остановить', exact: true }).waitFor()
  assert.equal(researchContextSubmissions.at(-1), 'r-b', 'explicit Chat continuation attaches only the selected completed research run')
  const bridgeCitationSources = page.locator('.chat-citation-sources').last()
  const bridgeCitationSummary = bridgeCitationSources.locator('summary')
  await bridgeCitationSummary.evaluate((element) => element.scrollIntoView({ block: 'center' }))
  await bridgeCitationSummary.click()
  await bridgeCitationSources.getByRole('link', { name: 'Реальный источник', exact: true }).waitFor()
  assert.equal(await page.getByRole('button', { name: 'Убрать контекст исследования', exact: true }).isDisabled(), true, 'active run pins research context')
  await page.locator('.chat-composer').getByRole('button', { name: 'Остановить', exact: true }).click()
  await page.getByRole('button', { name: 'Убрать контекст исследования', exact: true }).click()
  assert.equal(await page.getByText(/Контекст исследования · запуск/, { exact: false }).count(), 0)
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
  await openDetails(page)
  const planningDiagnostic = page.getByRole('status').filter({ has: page.getByRole('heading', { name: 'Планирование не завершено', exact: true }) })
  await planningDiagnostic.waitFor()
  await planningDiagnostic.getByText('План не был сохранён: ошибка произошла до начала поиска', { exact: true }).waitFor()
  await planningDiagnostic.getByText('Причина: Не задан шаг поиска', { exact: true }).waitFor()
  assert.ok(planningHistoryRequests > 0, 'resync must recover planning diagnostics from REST history')
  assert.match(await page.locator('.chat-action-error[role="alert"]').innerText(), /Не задан шаг поиска[\s\S]*planning[\s\S]*planner[\s\S]*2[\s\S]*steps: 2/)
  assert.match(await planningDiagnostic.innerText(), /Вызовы LLM\s+2\s+Поиски\s+0\s+Загрузки\s+0/)
  assert.equal(await page.getByRole('button', { name: 'Продолжить ответ', exact: true }).isDisabled(), true, 'previous run text must not enable continue for an empty failed assistant')
  assert.doesNotMatch(await page.locator('body').innerText(), /PRIVATE-PLAN|PRIVATE-COUNT|Детали исследования недоступны/)
  // Legacy error-code fallback still diagnoses planning when failure_stage is absent.
  delete events['r-e'][0].payload.failure_stage
  events['r-e'][0].stage = 'planning'
  events['r-e'][0].payload.error_code = 'invalid_model_output'
  runs['r-e'].error_code = 'invalid_model_output'
  planningStreamMode = 'offline'
  await page.reload()
  await openDetails(page)
  await planningDiagnostic.waitFor()
  await planningDiagnostic.getByText('Код: invalid_model_output', { exact: true }).waitFor()
  assert.equal(await page.getByRole('button', { name: 'Продолжить ответ', exact: true }).isDisabled(), true)
  const retryRequest = page.waitForRequest((request) => request.url().endsWith('/api/runs/r-e/retry') && request.method() === 'POST')
  await planningDiagnostic.getByRole('button', { name: 'Повторить ответ', exact: true }).click()
  assert.ok((await retryRequest).headers()['idempotency-key'])
  await page.locator('.chat-composer').getByRole('button', { name: 'Остановить', exact: true }).waitFor()
  await page.getByRole('link', { name: /Поздний сбой планирования/ }).click()
  await until(async () => (await chatRunStatus.innerText()).includes('Планирование'), 'late planning run begins')
  const lateDetailsToggle = page.getByRole('button', { name: 'Детали', exact: true })
  if (await lateDetailsToggle.getAttribute('aria-expanded') === 'true') await lateDetailsToggle.click()
  await page.locator('.chat-action-error[role="alert"]').getByText('Причина: Поздняя диагностическая причина', { exact: true }).waitFor()
  assert.ok(latePlanningHistoryRequests >= 2, 'terminal snapshot must trigger final history reconciliation after initial empty history')
  assert.equal(await page.getByRole('button', { name: 'Продолжить ответ', exact: true }).isDisabled(), true)
  await lateDetailsToggle.click()
  await page.getByRole('heading', { name: 'Планирование не завершено', exact: true }).waitFor()
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
  await page.getByText('Отправляем сообщение…', { exact: true }).waitFor()
  assert.equal(await page.getByText('Сообщение не отправлено', { exact: true }).count(), 0, 'pending send is not a failure')
  await page.locator('.chat-role-user').getByText('Новый вопрос', { exact: true }).waitFor()
  await page.getByText('Временный сетевой сбой', { exact: true }).waitFor()
  await page.getByText('Сообщение не отправлено', { exact: true }).waitFor()
  await page.getByRole('button', { name: 'Повторить действие' }).click()
  await page.locator('.chat-composer').getByRole('button', { name: 'Остановить', exact: true }).waitFor()
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
  await page.locator('.chat-composer').getByRole('button', { name: 'Остановить', exact: true }).click()
  await page.getByText('Генерация остановлена. Полученный текст сохранён.', { exact: true }).waitFor()
  assert.equal(await page.getByText('Начало потока', { exact: true }).count(), 1)
  await page.reload()
  await page.getByText('Начало потока', { exact: true }).waitFor()
  await page.getByRole('button', { name: 'Продолжить ответ', exact: true }).waitFor()
  await page.getByRole('button', { name: 'Продолжить ответ', exact: true }).click()
  await page.locator('.chat-composer').getByRole('button', { name: 'Остановить', exact: true }).waitFor()
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
  await page.locator('.chat-composer').getByRole('button', { name: 'Остановить', exact: true }).waitFor()
  await page.locator('.chat-composer').getByRole('button', { name: 'Остановить', exact: true }).click()
  await page.getByRole('button', { name: 'Переименовать разговор' }).click()
  await page.getByRole('textbox', { name: 'Название разговора' }).fill('Переименованный чат')
  await page.getByRole('button', { name: 'Сохранить', exact: true }).click()
  await page.getByRole('heading', { name: 'Переименованный чат', exact: true }).waitFor()
  await page.getByRole('link', { name: 'Рабочее пространство' }).click()
  await page.getByRole('heading', { name: 'Задачи', exact: true }).waitFor()
  assert.equal(doctorRequests, 1, 'Workspace initial load must be passive')
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
  for (const width of [320, 375, 414, 680, 768, 1024, 1280, 1440]) {
    await page.setViewportSize({ width, height: 900 })
    await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))))
    await assertPageOverflow(page, `workspace page overflow at ${width}`)
    if ([320, 375, 414].includes(width)) await assertWorkspaceRail(page, width)
    if (width === 375) await assertTelemetryOverflow(workspaceTelemetry, 'Workspace expanded mobile 375px')
    await page.getByRole('heading', { name: 'Задачи', exact: true }).waitFor()
    await page.locator('.task-card').filter({ hasText: 'Активный поток Workspace' }).waitFor()
  }
  await page.setViewportSize({ width: 1440, height: 1000 })
  for (const task of pausedWorkspaceTasks) {
    await page.locator('.task-card').filter({ hasText: task.title }).click()
    const resume = page.locator('.inspector-actions').getByRole('button', { name: 'Продолжить', exact: true })
    await resume.waitFor()
    assert.equal(await resume.isEnabled(), task.pause_reason === 'manual', `Workspace ${task.pause_reason} resume eligibility`)
    if (task.pause_reason === 'manual') await page.getByText(/При возобновлении backend повторно проверит ограничения/).waitFor()
  }
  await page.getByText('Сервисы и диагностика', { exact: true }).click()
  assert.equal(doctorRequests, 1, 'opening Workspace diagnostics disclosure remains passive')
  await page.getByRole('button', { name: 'Проверить модель и поиск', exact: true }).click()
  await until(() => doctorRequests === 2, 'one explicit Workspace doctor request')
  await page.waitForTimeout(200)
  assert.equal(doctorRequests, 2)
  await page.getByRole('button', { name: /Память/ }).click()
  await page.getByRole('dialog', { name: 'Память', exact: true }).waitFor()
  await page.keyboard.press('Escape')
  await page.getByRole('link', { name: 'Открыть чаты' }).click()
  await page.getByRole('heading', { name: 'Новый разговор', exact: true }).waitFor()
  await page.getByRole('combobox', { name: 'Режим разговора' }).selectOption('research')
  await page.getByRole('textbox', { name: 'Задача исследования', exact: true }).fill('Исследуй вопрос')
  await page.getByText('Настройки следующего запуска', { exact: true }).click()
  await page.getByRole('combobox', { name: 'Профиль исследования', exact: true }).selectOption('quick')
  await page.getByRole('spinbutton', { name: /^Лимит времени, мин/ }).fill('2')
  await page.getByText('Параметры модели', { exact: true }).click()
  assert.equal(await page.getByRole('spinbutton', { name: /^Температура/ }).inputValue(), '0.2', 'unsaved temperature uses documented client default')
  assert.equal(await page.getByRole('spinbutton', { name: /^Top-p/ }).inputValue(), '0.8')
  const outputDefault = await page.getByRole('spinbutton', { name: /^Лимит выходных токенов/ }).inputValue()
  if (outputDefault !== '5120') failures.push(`Unsaved output-token default: expected configured 5120, received ${outputDefault}`)
  await page.getByRole('spinbutton', { name: /^Температура/ }).fill('3')
  const beforeInvalid = submissionKeys.length
  await page.getByRole('button', { name: 'Отправить', exact: true }).click()
  await page.waitForTimeout(100)
  assert.equal(submissionKeys.length, beforeInvalid, 'invalid settings cannot dispatch')
  await page.getByRole('spinbutton', { name: /^Температура/ }).fill('0')
  await page.getByRole('spinbutton', { name: /^Top-p/ }).fill('0.8')
  await page.getByRole('spinbutton', { name: /^Лимит выходных токенов/ }).fill('4096')
  await page.getByRole('button', { name: 'Отправить', exact: true }).click()
  await page.locator('.chat-composer').getByRole('button', { name: 'Остановить', exact: true }).waitFor()
  assert.equal(Object.values(runs).at(-1).mode, 'research')
  assert.match(page.url(), /#\/chat\/c-/)
  const createdSettings = settingsWrites.filter(write => write.method === 'POST').at(-1).body
  assert.deepEqual(createdSettings.research_settings, { profile: 'quick', time_budget_seconds: 120 })
  assert.deepEqual(createdSettings.model_profile, { temperature: 0, top_p: 0.8, max_output_tokens: 4096 })
  await page.getByText('Настройки следующего запуска', { exact: true }).click()
  assert.equal(await page.getByRole('combobox', { name: 'Профиль исследования', exact: true }).isDisabled(), true, 'active run pins settings')
  await page.getByText('Параметры модели', { exact: true }).click()
  assert.equal(await page.getByRole('spinbutton', { name: /^Температура/ }).isDisabled(), true)
  await page.locator('.chat-composer').getByRole('button', { name: 'Остановить', exact: true }).click()
  const settingsPatchCount = () => settingsWrites.filter(write => write.method === 'PATCH' && write.body.model_profile).length
  const beforeFailedSave = settingsPatchCount()
  await page.getByRole('spinbutton', { name: /^Температура/ }).fill('0.1')
  settingsReject = true
  await page.getByRole('button', { name: 'Сохранить настройки', exact: true }).click()
  await page.getByText('Сервер отклонил настройки', { exact: true }).waitFor()
  const staleSettingsRetry = page.getByRole('button', { name: 'Повторить действие', exact: true })
  await staleSettingsRetry.waitFor()
  assert.equal(settingsPatchCount(), beforeFailedSave + 1)
  assert.equal(settingsWrites.filter(write => write.method === 'PATCH' && write.body.model_profile).at(-1).body.model_profile.temperature, 0.1, 'failed Save A captured settings A')
  await page.getByRole('spinbutton', { name: /^Температура/ }).fill('0')
  await staleSettingsRetry.waitFor({ state: 'hidden' })
  assert.equal(settingsPatchCount(), beforeFailedSave + 1, 'editing settings B clears stale retry without dispatching settings A')
  await page.getByRole('button', { name: 'Сохранить настройки', exact: true }).click()
  await until(() => settingsPatchCount() === beforeFailedSave + 2, 'normal Save persists edited settings B')
  const patched = settingsWrites.filter(write => write.method === 'PATCH' && write.body.model_profile).at(-1).body
  assert.deepEqual(patched.research_settings, { profile: 'quick', time_budget_seconds: 120 })
  assert.deepEqual(patched.model_profile, { temperature: 0, top_p: 0.8, max_output_tokens: 4096 })
  await page.reload()
  await page.getByText('Настройки следующего запуска', { exact: true }).click()
  assert.equal(await page.getByRole('spinbutton', { name: /^Лимит времени, мин/ }).inputValue(), '2')
  await page.getByText('Параметры модели', { exact: true }).click()
  assert.equal(await page.getByRole('spinbutton', { name: /^Температура/ }).inputValue(), '0')
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
  for (const width of [375, 1440]) {
    await page.setViewportSize({ width, height: 900 })
    await page.goto(`${base}/#/chat`)
    await page.getByRole('textbox', { name: 'Сообщение', exact: true }).fill('Проверка автопрокрутки')
    await page.getByRole('button', { name: 'Отправить', exact: true }).click()
    await page.locator('.chat-role-assistant .chat-markdown').last().getByText('Хвост потока', { exact: true }).waitFor()
    const surface = width <= 768 ? '.chat-center' : '.chat-transcript'
    await until(() => page.locator(surface).evaluate(element => element.scrollHeight - element.scrollTop - element.clientHeight < 100), `new live SSE follows ${surface} at ${width}`)
    assert.ok(await page.locator(surface).evaluate(element => element.scrollHeight > element.clientHeight + 100), 'autofollow fixture genuinely overflows its scroll surface')
    await page.locator('.chat-composer').getByRole('button', { name: 'Остановить', exact: true }).click()
  }
  console.log('Completed mocked browser scenarios:', JSON.stringify({ widths: [320, 375, 414, 768, 1024, 1280, 1440], chat_pause_cases: 3, workspace_pause_cases: 3, doctor_requests: doctorRequests, passive_policy_requests: policyRequests, collected_failures: failures }))
  assert.deepEqual(failures, [])
  console.log('PASS: Chrome browser smoke; manual RSS hold/no false delivery, guarded/recovery resume, passive discovery/idle refresh, explicit-only diagnostics, next-run settings create/PATCH/validation/error/reload/pinning, scoped report outline, child bounds/44px actions, short-height/enlarged-text/reduced-motion, bidirectional drawer wrapping/restoration, plus preserved conversation/citation/source/telemetry/SSE/idempotency/Stop/Retry/Continue/Workspace regressions. All API/SSE responses mocked; no real inference or DB mutation.')
} finally {
  await browser?.close()
  server.kill('SIGTERM')
}
