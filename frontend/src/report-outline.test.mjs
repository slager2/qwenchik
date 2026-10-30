import test from 'node:test'
import assert from 'node:assert/strict'
import { reportOutline, focusReportHeading } from './report-outline.mjs'

test('outline uses parsed Markdown headings, including setext, but ignores fenced code and HTML', () => {
  const headings = reportOutline('# **Итог**\n\n```md\n# fake\n```\n\nПодробности\n---\n\n<h2>HTML</h2>\n\n> ## В цитате', 'answer')
  assert.deepEqual(headings.map((item) => item.text), ['Итог', 'Подробности', 'В цитате'])
  assert.deepEqual(headings.map((item) => item.depth), [1, 2, 2])
  assert.equal(new Set(headings.map((item) => item.id)).size, 3)
})

test('outline navigation focuses and scrolls only a heading in its container without touching the router', () => {
  const calls = []
  const heading = { id: 'own-heading', focus: (options) => calls.push(['focus', options]), scrollIntoView: (options) => calls.push(['scroll', options]) }
  const container = { querySelectorAll: (selector) => { assert.equal(selector, '.chat-markdown [id]'); return [heading] } }
  Object.defineProperty(globalThis, 'location', { configurable: true, get() { throw new Error('Hash routing must not be touched') } })
  try {
    focusReportHeading(container, 'other-heading')
    assert.equal(calls.length, 0)
    focusReportHeading(container, 'own-heading')
    assert.deepEqual(calls, [['focus', { preventScroll: true }], ['scroll', { block: 'start', behavior: 'instant' }]])
  } finally { delete globalThis.location }
})
