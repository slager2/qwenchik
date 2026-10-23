import test from 'node:test'
import assert from 'node:assert/strict'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import RunStatus from './RunStatus.tsx'

const run = { id: 'run-1', status: 'running' }
const render = (data) => renderToStaticMarkup(createElement(RunStatus, { run, data }))

test('nested keep-awake requests remain explicit when the inhibitor is unavailable', () => {
  const html = render({
    task_id: 'run-1', cursor: 0, status: 'running',
    runtime: { keep_awake: true, sleep: { active: false, available: false, provider: 'unavailable' }, resources: {} },
  })
  assert.match(html, /Запрошено, не активно/)
  assert.match(html, /Блокировка сна не подтверждена: провайдер недоступен/)
})

test('missing sleep state is unknown rather than reported as not requested', () => {
  const html = render({ task_id: 'run-1', cursor: 0, status: 'running', runtime: { resources: {} } })
  assert.match(html, /Удержание от сна<\/dt><dd>Неизвестно/)
  assert.doesNotMatch(html, /Блокировка сна не подтверждена/)
})

test('latest runtime warning frame replaces warnings from the previous resource sample', () => {
  const html = render({
    task_id: 'run-1', cursor: 0, status: 'running', runtime_warnings: [],
    runtime: { resources: { warnings: ['warning already cleared'] } },
  })
  assert.doesNotMatch(html, /warning already cleared/)
})
