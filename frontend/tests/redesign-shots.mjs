import { spawn } from 'node:child_process'
import { mkdir } from 'node:fs/promises'
import { join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { chromium } from 'playwright-core'

// Deterministic visual captures for the redesign. Every /api request is
// answered from in-memory fixtures; nothing reaches a real backend.
const root = fileURLToPath(new URL('..', import.meta.url))
const output = resolve(process.env.SHOTS_DIR || join(root, '..', '..', 'frontend-shots'))
const port = 4176
const base = `http://127.0.0.1:${port}`
const only = process.env.SHOTS_ONLY ? process.env.SHOTS_ONLY.split(',') : null
await mkdir(output, { recursive: true })

const now = new Date('2026-11-05T09:30:00Z').toISOString()
const ago = (hours) => new Date(Date.parse(now) - hours * 3600e3).toISOString()
const conversation = (id, title, mode, hours, extra = {}) => ({ id, title, mode, created_at: ago(hours), updated_at: ago(hours), latest_run_id: `run-${id}`, ...extra })
const conversations = [
  conversation('a', 'Сравнение векторных баз данных', 'research', 1),
  conversation('r', 'Рынок локальных LLM в 2026 году', 'research', 2, { active_run_id: 'run-r' }),
  conversation('c', 'Рецепт хлеба на закваске', 'chat', 20),
  conversation('d', 'Настройка llama.cpp на ноутбуке', 'chat', 30),
  conversation('e', 'Обзор литературы по RAG', 'research', 200),
  conversation('f', 'План поездки в Казань', 'chat', 400),
  conversation('g', 'Почему небо голубое', 'chat', 420),
]
const message = (id, conversation_id, run_id, role, content, status = 'completed') => ({ id, conversation_id, run_id, role, content, status, created_at: now, updated_at: now, last_event_seq: 0 })
const answer = `Короткий ответ: для локального прототипа подойдёт **SQLite с расширением sqlite-vec**, а для продакшена с миллионами векторов — **Qdrant** или **pgvector** [ev-1].

## Что сравнивать

1. Тип индекса (HNSW, IVF) и качество поиска на ваших данных.
2. Фильтрацию по метаданным вместе с векторным поиском.
3. Эксплуатацию: бэкапы, репликацию, мониторинг [ev-2].

| Вариант | Когда выбрать | Ограничения |
| --- | --- | --- |
| sqlite-vec | Один пользователь, локально | Нет распределённости |
| pgvector | Уже есть PostgreSQL | Настройка индексов |
| Qdrant | Большие коллекции | Отдельный сервис |

\`\`\`python
import sqlite3, sqlite_vec

db = sqlite3.connect("notes.db")
db.enable_load_extension(True)
sqlite_vec.load(db)
\`\`\`

Если важна простота, начните с \`sqlite-vec\` и вынесите поиск в отдельный модуль — так переход на другой движок будет дешёвым.`
const messages = {
  a: [message('u-a', 'a', 'run-a', 'user', 'Какую векторную базу выбрать для личной базы знаний?'), message('m-a', 'a', 'run-a', 'assistant', answer)],
  r: [message('u-r', 'r', 'run-r', 'user', 'Исследуй рынок локальных LLM в 2026 году: ключевые модели, требования к железу и тренды.'), message('m-r', 'r', 'run-r', 'assistant', '', 'streaming')],
}
for (const item of conversations) messages[item.id] ??= []
const run = (id, conversation_id, mode, status, extra = {}) => ({ id, conversation_id, user_message_id: `u-${conversation_id}`, assistant_message_id: `m-${conversation_id}`, mode, operation: 'message', status, retry_count: 0, ...extra })
const runs = { 'run-a': run('run-a', 'a', 'research', 'completed'), 'run-r': run('run-r', 'r', 'research', 'running', { stage: 'search', phase: 'search' }) }
for (const item of conversations) runs[item.latest_run_id] ??= run(item.latest_run_id, item.id, item.mode, 'completed')
const evidence = {
  'run-a': [
    { id: 'ev-1', task_id: 'run-a', provider: 'searxng', original_query: '', original_url: 'https://qdrant.tech/documentation/', canonical_url: 'https://qdrant.tech/documentation/', title: 'Qdrant documentation', language: 'en', retrieved_at: now, http_status: 200, content_sha256: 'a'.repeat(64), extraction_method: 'html', excerpt: 'Qdrant is a vector similarity search engine.' },
    { id: 'ev-2', task_id: 'run-a', provider: 'searxng', original_query: '', original_url: 'https://github.com/pgvector/pgvector', canonical_url: 'https://github.com/pgvector/pgvector', title: 'pgvector: open-source vector similarity search for Postgres', language: 'en', retrieved_at: now, http_status: 200, content_sha256: 'b'.repeat(64), extraction_method: 'html', excerpt: 'Store your vectors with the rest of your data.' },
  ],
}
const plan = { goal: 'Рынок локальных LLM', plan: { subtasks: [
  { id: 'step1', kind: 'search', description: 'Найти обзоры локальных LLM, опубликованные в 2026 году', success_criteria: ['Не менее трёх независимых обзоров'] },
  { id: 'step2', kind: 'fetch', description: 'Собрать требования к памяти и GPU для популярных моделей', success_criteria: ['Таблица требований'] },
  { id: 'step3', kind: 'synthesis', description: 'Сравнить тренды: квантизация, MoE, длинный контекст', success_criteria: ['Вывод с источниками'] },
] } }
const sources = [
  { id: 'src-1', title: 'State of Local AI 2026', canonical_url: 'https://example.org/local-ai-2026', provider: 'searxng', status: 'extracted', snippet: 'Обзор рынка локального инференса и популярных открытых моделей.' },
  { id: 'src-2', title: 'llama.cpp performance notes', canonical_url: 'https://github.com/ggml-org/llama.cpp', provider: 'searxng', status: 'fetching' },
  { id: 'src-3', title: 'Quantization benchmarks', canonical_url: 'https://example.com/quant', provider: 'searxng', status: 'discovered' },
]
const researchEvents = [
  { id: 'e1', task_id: 'run-r', seq: 1, type: 'plan.created', visibility: 'user', schema_version: 1, created_at: now, payload: { steps: 3 } },
  { id: 'e2', task_id: 'run-r', seq: 2, type: 'step.started', visibility: 'user', schema_version: 1, created_at: now, payload: { step_id: 'step1' } },
  { id: 'e3', task_id: 'run-r', seq: 3, type: 'step.completed', visibility: 'user', schema_version: 1, created_at: now, payload: { step_id: 'step1' } },
  { id: 'e4', task_id: 'run-r', seq: 4, type: 'step.started', visibility: 'user', schema_version: 1, created_at: now, payload: { step_id: 'step2' } },
]
const spaces = [
  { id: 's1', title: 'Диплом', name: 'Диплом', description: 'Исследование методов RAG для русскоязычных документов', created_at: ago(500), updated_at: ago(3) },
  { id: 's2', title: 'Домашний сервер', name: 'Домашний сервер', description: 'Железо, сеть и бэкапы', created_at: ago(900), updated_at: ago(50) },
]
const task = (id, title, description, status, stage, hours) => ({ id, title, description, status, stage, priority: 0, version: 1, created_at: ago(hours), updated_at: ago(hours) })
const tasks = [
  task('t1', 'Собрать обзор по квантизации', 'Найди свежие сравнения GGUF-квантизаций и сведи в таблицу.', 'running', 'fetch', 1),
  task('t2', 'Отправить отчёт коллеге', 'Подготовить письмо с выводами по исследованию.', 'waiting_approval', 'finalization', 2),
  task('t3', 'Проверить ссылки в README', 'Пройти по всем ссылкам и отметить битые.', 'queued', undefined, 5),
  task('t4', 'Конспект статьи про MoE', 'Сделать краткий конспект с ключевыми цифрами.', 'completed', 'finalization', 30),
  task('t5', 'Импорт заметок', 'Импортировать заметки из Obsidian.', 'failed', 'extract', 60),
]
const approvals = { t2: [{ id: 'ap1', task_id: 't2', tool_name: 'email.send', arguments: { to: 'colleague@example.org', subject: 'Выводы исследования' }, target: 'colleague@example.org', risk_explanation: 'Письмо уйдёт внешнему адресату.', expires_at: ago(-24), status: 'pending', created_at: now }] }

async function api(route) {
  const request = route.request()
  const url = new URL(request.url())
  const parts = url.pathname.split('/').filter(Boolean)
  const json = (body, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) })
  if (request.method() !== 'GET') return json({ error: { message: 'Только просмотр' } }, 405)
  const [, resource, id, sub, extra] = parts
  if (resource === 'health') return json({ status: 'ok', database: 'ok', model: { id: 'qwen3', status: 'configured' }, worker: 'attached' })
  if (resource === 'lab') return json({ research_limits: { configured: true, privacy_mode: 'local_inference_external_search' } })
  if (resource === 'providers') return json({ search: { providers: { searxng: { enabled: true, health: 'healthy', endpoint: 'http://127.0.0.1:8888' } } } })
  if (resource === 'memories') return json([])
  if (resource === 'conversations') {
    if (!id) return json(conversations)
    const item = conversations.find((entry) => entry.id === id)
    if (!item) return json({ error: { message: 'Не найдено' } }, 404)
    if (sub === 'messages') return json(messages[id])
    return json(item)
  }
  if (resource === 'runs') {
    const item = runs[id]
    if (!item) return json({ error: { message: 'Не найдено' } }, 404)
    if (!sub) return json(item)
    if (sub === 'runtime') return json({ task_id: id, cursor: 4, status: item.status, stage: item.stage, profile: 'standard', counters: { llm_calls: 6, search_queries: 4, fetches: 2 }, budget: { max_llm_calls: 60, max_search_queries: 30, max_fetches: 40 }, research_report: { artifact_id: '', markdown_artifact_id: '', outcome: '', sections: 0 } })
    if (sub === 'events' && extra === 'stream') return route.fulfill({ status: 200, contentType: 'text/event-stream', body: 'retry: 600000\n\n' })
    if (sub === 'events') return json(id === 'run-r' && Number(url.searchParams.get('after') || 0) === 0 ? researchEvents : [])
    if (sub === 'plan') return json(id === 'run-r' ? plan : null)
    if (sub === 'sources') return json(id === 'run-r' ? sources : [])
    if (sub === 'evidence') return json(evidence[id] ?? [])
    return json([])
  }
  if (resource === 'spaces') {
    if (!id) return json(spaces)
    if (sub === 'conversations') return json(conversations.slice(0, 3).map((item) => ({ ...item, space_id: id })))
    if (sub === 'context') return json([{ run_id: 'run-e', conversation_id: 'e', conversation_title: 'Обзор литературы по RAG', verification_status: 'supported', summary: 'Гибридный поиск (BM25 + векторы) стабильно лучше чисто векторного на русскоязычных корпусах.', sources: [{ evidence_id: 'x1', title: 'BEIR benchmark', canonical_url: 'https://example.org/beir' }, { evidence_id: 'x2', title: 'Hybrid search study', canonical_url: 'https://example.org/hybrid' }], updated_at: ago(48) }])
    if (sub === 'research') return json([{ run: runs['run-e'], conversation_title: 'Обзор литературы по RAG', goal: 'Обзор литературы по RAG', verification_status: 'supported', completed_at: ago(48) }])
    return json(spaces.find((item) => item.id === id) ?? null)
  }
  if (resource === 'tasks') {
    if (!id) return json(tasks)
    const item = tasks.find((entry) => entry.id === id)
    if (!sub) return json(item)
    if (sub === 'events' && extra === 'stream') return route.fulfill({ status: 200, contentType: 'text/event-stream', body: 'retry: 600000\n\n' })
    if (sub === 'steps') return json(id === 't1' ? [
      { id: 'ts1', task_id: id, step_id: 's1', position: 1, kind: 'search', description: 'Найти сравнения квантизаций', dependencies: [], expected_output: '', success_criteria: [], status: 'completed', attempts: 1, created_at: now, updated_at: now },
      { id: 'ts2', task_id: id, step_id: 's2', position: 2, kind: 'fetch', description: 'Прочитать три лучших источника', dependencies: [], expected_output: '', success_criteria: [], status: 'running', attempts: 1, created_at: now, updated_at: now },
      { id: 'ts3', task_id: id, step_id: 's3', position: 3, kind: 'synthesis', description: 'Свести результаты в таблицу', dependencies: [], expected_output: '', success_criteria: [], status: 'pending', attempts: 0, created_at: now, updated_at: now },
    ] : [])
    if (sub === 'approvals') return json(approvals[id] ?? [])
    if (sub === 'events') return json(id === 't1' ? [{ id: 'te1', task_id: id, seq: 1, type: 'task.created', visibility: 'user', schema_version: 1, created_at: ago(1), payload: {} }, { id: 'te2', task_id: id, seq: 2, type: 'tool.started', visibility: 'user', schema_version: 1, created_at: now, payload: { tool: 'search', summary: 'Ищу сравнения квантизаций' } }] : [])
    return json([])
  }
  if (resource === 'runtime' || resource === 'doctor') return json({ status: 'ok', checks: {} })
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
  const views = [{ name: 'desktop', width: 1440, height: 900 }, { name: 'mobile', width: 390, height: 844 }]
  const scenes = [
    { name: 'chat-empty', hash: '#/chat' },
    { name: 'chat-messages', hash: '#/chat/a' },
    { name: 'chat-research', hash: '#/chat/r' },
    { name: 'chat-research-details', hash: '#/chat/r', details: true },
    { name: 'workspace', hash: '#/workspace' },
    { name: 'spaces', hash: '#/spaces/s1' },
    { name: 'nav', hash: '#/chat/a', nav: true, mobileOnly: true },
  ]
  for (const theme of ['light', 'dark']) for (const view of views) for (const scene of scenes) {
    if (only && !only.includes(scene.name)) continue
    if (scene.mobileOnly && view.name !== 'mobile') continue
    const context = await browser.newContext({ viewport: view, deviceScaleFactor: 1, locale: 'ru-RU', timezoneId: 'Europe/Moscow', colorScheme: theme, reducedMotion: 'reduce' })
    await context.addInitScript((value) => { localStorage.setItem('qwenchik.theme', value) }, theme)
    await context.route('**/api/**', api)
    const page = await context.newPage()
    page.on('pageerror', (error) => console.error(`${scene.name}: ${error.message}`))
    await page.goto(`${base}/${scene.hash}`)
    await page.waitForTimeout(900)
    if (scene.details) { await page.getByRole('button', { name: 'Детали', exact: true }).click(); await page.waitForTimeout(500) }
    if (scene.nav) { await page.getByRole('button', { name: 'Открыть разговоры', exact: true }).click(); await page.waitForTimeout(300) }
    await page.evaluate(() => document.fonts.ready)
    const file = join(output, `${scene.name}-${view.name}-${theme}.png`)
    await page.screenshot({ path: file })
    console.log(file)
    await context.close()
  }
} finally {
  await browser?.close()
  server.kill()
}
