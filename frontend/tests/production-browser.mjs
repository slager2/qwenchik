import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { chromium } from 'playwright-core'

const root = fileURLToPath(new URL('..', import.meta.url))
const base = 'http://127.0.0.1:4176'
const server = spawn(process.execPath, ['node_modules/vite/bin/vite.js', 'preview', '--host', '127.0.0.1', '--port', '4176', '--strictPort'], { cwd: root, stdio: 'pipe' })
let serverOutput = ''
server.stdout.on('data', (data) => { serverOutput += data })
server.stderr.on('data', (data) => { serverOutput += data })
let browser

async function waitForServer() {
  const deadline = Date.now() + 10000
  while (Date.now() < deadline) {
    if (server.exitCode !== null) throw new Error(`Vite preview exited: ${serverOutput}`)
    try { if ((await fetch(base)).ok) return } catch {}
    await new Promise((resolve) => setTimeout(resolve, 50))
  }
  throw new Error(`Vite preview did not start: ${serverOutput}`)
}

async function mockAPI(page) {
  await page.route('**/api/**', (route) => {
    const path = new URL(route.request().url()).pathname
    const body = path === '/api/health'
      ? { status: 'ok', database: 'ok' }
      : path === '/api/doctor'
        ? { status: 'ok', checks: { app: { status: 'ok' }, worker: { status: 'ok' }, database: { status: 'ok' }, llm: { status: 'ok' } } }
        : path === '/api/conversations' || path === '/api/tasks'
          ? []
          : path === '/api/providers'
            ? { search: { providers: {} } }
            : { error: { message: 'Not found' } }
    return route.fulfill({ status: path === '/api/health' || path === '/api/doctor' || path === '/api/conversations' || path === '/api/tasks' || path === '/api/providers' ? 200 : 404, contentType: 'application/json', body: JSON.stringify(body) })
  })
}

try {
  await waitForServer()
  browser = await chromium.launch({ executablePath: process.env.CHROME_BIN || '/usr/bin/google-chrome', headless: true, args: ['--no-sandbox'] })
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } })
  const pageErrors = []
  const assets = []
  page.on('pageerror', (error) => pageErrors.push(error.message))
  page.on('request', (request) => {
    const pathname = new URL(request.url()).pathname
    if (pathname.startsWith('/assets/')) assets.push(pathname)
  })
  await mockAPI(page)
  await page.goto(`${base}/#/chat`)
  await page.getByRole('heading', { name: 'С чего начнём?', exact: true }).first().waitFor()
  assert.equal(await page.getByRole('heading', { name: 'С чего начнём?', exact: true }).count(), 1, 'empty Chat should render its onboarding only once')
  assert.equal(await page.evaluate(() => '__qwenchikDebug' in window), false, 'production must not expose development diagnostics')
  assert.equal(assets.some((asset) => /\/WorkspaceRoute-[^/]+\.js$/.test(asset)), false, 'chat must not request the Workspace chunk')

  const workspaceResponse = page.waitForResponse((response) => /\/WorkspaceRoute-[^/]+\.js$/.test(new URL(response.url()).pathname))
  await page.getByRole('link', { name: 'Рабочее пространство' }).click()
  await page.getByRole('heading', { name: 'Задачи', exact: true }).waitFor()
  assert.equal((await workspaceResponse).status(), 200)
  assert.equal(assets.filter((asset) => /\/WorkspaceRoute-[^/]+\.js$/.test(asset)).length, 1, 'workspace navigation should load one dedicated chunk')
  assert.deepEqual(pageErrors, [])

  const failedPage = await browser.newPage({ viewport: { width: 1280, height: 900 } })
  await mockAPI(failedPage)
  await failedPage.route('**/assets/WorkspaceRoute-*.js', (route) => route.abort())
  await failedPage.goto(`${base}/#/chat`)
  await failedPage.getByRole('heading', { name: 'С чего начнём?', exact: true }).first().waitFor()
  await failedPage.getByRole('link', { name: 'Рабочее пространство' }).click()
  const alert = failedPage.getByRole('alert')
  await alert.getByText('Не удалось загрузить рабочее пространство.', { exact: true }).waitFor()
  await alert.getByRole('button', { name: 'Перезагрузить интерфейс', exact: true }).waitFor()
  await failedPage.close()
  console.log('PASS: production preview defers Workspace until navigation, serves its chunk, omits dev diagnostics, and shows a recoverable fallback when the chunk fails.')
} catch (error) {
  console.error(error)
  process.exitCode = 1
} finally {
  await browser?.close()
  server.kill('SIGTERM')
}
