import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { mkdir } from 'node:fs/promises'
import { join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { chromium } from 'playwright-core'

// Visual captures of the pre-research clarification flow. Every /api request
// is answered from in-memory fixtures; nothing reaches a real backend.
const root = fileURLToPath(new URL('..', import.meta.url))
const output = resolve(process.env.SHOTS_DIR || join(root, '..', '..', 'clarify-shots'))
const port = 4177
const base = `http://127.0.0.1:${port}`
await mkdir(output, { recursive: true })

const now = new Date('2026-11-05T09:30:00Z').toISOString()
const question = 'сделай мне роадмап по биоинформатике и по химии с информатикой, что я должен знать чтобы стать биоинформатиком или биоинженером, дай мне этапы и время за сколько я чему научусь'
const clarification = {
  status: 'ready',
  interpretations: [
    { id: 'i1', title: 'С нуля, самостоятельно', goal: 'Составить пошаговый план самообучения биоинформатике с нуля: биология, химия, программирование и статистика, с этапами и сроками.' },
    { id: 'i2', title: 'Переход из IT', goal: 'План перехода в биоинформатику для человека, который уже программирует: какие биологические и химические основы добрать и за сколько.' },
    { id: 'i3', title: 'Через университет', goal: 'Сравнить путь через бакалавриат или магистратуру по биоинформатике и биоинженерии со сроками обучения и требованиями.' },
    { id: 'i4', title: 'Фокус на биоинженерию', goal: 'Роадмап именно для биоинженера: лабораторные навыки, молекулярная биология, химия и вычислительные инструменты.' },
  ],
  questions: [
    { id: 'q1', question: 'Сколько часов в неделю готовы учиться?', options: ['5', '10', '20+'] },
    { id: 'q2', question: 'Есть ли опыт программирования?', options: ['Нет', 'Немного Python', 'Работаю разработчиком'] },
    { id: 'q3', question: 'Какое направление важнее?', options: ['Биоинформатика', 'Биоинженерия', 'Оба'] },
  ],
}
const research = { id: 'r', title: 'Роадмап в биоинформатику', mode: 'research', created_at: now, updated_at: now, latest_run_id: 'run-r', active_run_id: 'run-r' }
const fresh = { id: 'n', title: question.slice(0, 70), mode: 'research', created_at: now, updated_at: now }
const userMessage = { id: 'u-r', conversation_id: 'r', run_id: 'run-r', role: 'user', content: question, status: 'completed', created_at: now, updated_at: now, last_event_seq: 0, metadata: { clarification: { interpretation_title: 'С нуля, самостоятельно', interpretation_goal: clarification.interpretations[0].goal, answers: [{ question: 'Сколько часов в неделю готовы учиться?', answer: '10' }, { question: 'Есть ли опыт программирования?', answer: 'Немного Python' }], extra: 'Интересует геномика' } } }
const assistant = { id: 'm-r', conversation_id: 'r', run_id: 'run-r', role: 'assistant', content: '', status: 'streaming', created_at: now, updated_at: now, last_event_seq: 0 }
const run = { id: 'run-r', conversation_id: 'r', user_message_id: 'u-r', assistant_message_id: 'm-r', mode: 'research', operation: 'send', status: 'running', stage: 'planning', retry_count: 0 }

const submissions = []
function handler(clarifyDelay) {
  return async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const parts = url.pathname.split('/').filter(Boolean)
    const json = (body, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) })
    const [, resource, id, sub, extra] = parts
    if (resource === 'health') return json({ status: 'ok', database: 'ok', model: { id: 'qwen3', status: 'configured' }, worker: 'attached' })
    if (resource === 'lab') return json({ research_limits: { configured: true, privacy_mode: 'local_inference_external_search' } })
    if (resource === 'conversations') {
      if (!id && request.method() === 'POST') return json(fresh, 201)
      if (!id) return json([research])
      if (sub === 'clarify') { await new Promise((done) => setTimeout(done, clarifyDelay)); return json(clarification) }
      if (sub === 'messages' && request.method() === 'POST') {
        const body = request.postDataJSON()
        submissions.push(body)
        return json({ conversation_id: id, user_message: { ...userMessage, conversation_id: id, content: body.content }, assistant_message: { ...assistant, conversation_id: id }, run: { ...run, conversation_id: id } }, 202)
      }
      if (sub === 'messages') return json(id === 'r' ? [userMessage, assistant] : [])
      return json(id === 'r' ? research : fresh)
    }
    if (resource === 'runs') {
      if (!sub) return json(run)
      if (sub === 'runtime') return json({ task_id: 'run-r', cursor: 0, status: 'running', stage: 'planning', profile: 'standard', counters: {}, budget: {} })
      if (sub === 'events' && extra === 'stream') return route.fulfill({ status: 200, contentType: 'text/event-stream', body: 'retry: 600000\n\n' })
      return json([])
    }
    if (resource === 'spaces') return json([])
    return json([])
  }
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
  const scenes = ['clarify-loading', 'clarify-ready', 'clarified-transcript']
  for (const theme of ['light', 'dark']) for (const view of views) for (const scene of scenes) {
    const context = await browser.newContext({ viewport: view, deviceScaleFactor: 1, locale: 'ru-RU', timezoneId: 'Europe/Moscow', colorScheme: theme, reducedMotion: 'reduce' })
    await context.addInitScript((value) => { localStorage.setItem('qwenchik.theme', value) }, theme)
    await context.route('**/api/**', handler(scene === 'clarify-loading' ? 60000 : 300))
    const page = await context.newPage()
    page.on('pageerror', (error) => console.error(`${scene}: ${error.message}`))
    if (scene === 'clarified-transcript') {
      await page.goto(`${base}/#/chat/r`)
      await page.waitForTimeout(900)
    } else {
      await page.goto(`${base}/#/chat`)
      await page.waitForTimeout(600)
      await page.getByLabel('Режим разговора').selectOption('research')
      await page.locator('#conversation-input').fill(question)
      await page.locator('#conversation-input').press('Enter')
      if (scene === 'clarify-ready') {
        await page.getByRole('radio', { name: /С нуля, самостоятельно/ }).click()
        await page.getByRole('radio', { name: '10', exact: true }).click()
        await page.getByRole('radio', { name: 'Немного Python' }).click()
        await page.getByLabel(/Дополнить/).fill('Интересует геномика')
        await page.getByRole('radio', { name: /С нуля, самостоятельно/ }).scrollIntoViewIfNeeded()
      } else {
        await page.waitForTimeout(500)
      }
    }
    await page.evaluate(() => document.fonts.ready)
    const file = join(output, `${scene}-${view.name}-${theme}.png`)
    await page.screenshot({ path: file })
    if (scene === 'clarify-ready') await page.screenshot({ path: join(output, `${scene}-${view.name}-${theme}-full.png`), fullPage: true })
    console.log(file)
    if (scene === 'clarify-ready' && view.name === 'desktop' && theme === 'light') {
      // Behaviour check: the chosen options reach POST /messages.
      await page.getByRole('button', { name: 'Начать исследование' }).click()
      await page.waitForTimeout(500)
      assert.deepEqual(submissions.at(-1), { content: question, mode: 'research', clarification: { interpretation_title: clarification.interpretations[0].title, interpretation_goal: clarification.interpretations[0].goal, answers: [{ question: clarification.questions[0].question, answer: '10' }, { question: clarification.questions[1].question, answer: 'Немного Python' }], extra: 'Интересует геномика' } })
    }
    if (scene === 'clarify-loading' && view.name === 'desktop' && theme === 'light') {
      // Skipping while options load submits the original question at once.
      await page.getByRole('button', { name: 'Пропустить (как есть)' }).click()
      await page.waitForTimeout(500)
      assert.deepEqual(submissions.at(-1), { content: question, mode: 'research' })
    }
    await context.close()
  }
  assert.equal(submissions.length, 2)
  console.log('clarification flow checks passed')
} finally {
  await browser?.close()
  server.kill()
}
