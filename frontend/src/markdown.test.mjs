import test from 'node:test'
import assert from 'node:assert/strict'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import MarkdownMessage from './MarkdownMessage.tsx'

const render = (content, references = []) => renderToStaticMarkup(createElement(MarkdownMessage, { content, references }))

test('markdown renders GFM tables, lists and code without executing HTML or unsafe URLs', () => {
  const html = render('# Heading\n\n- item\n\n| A | B |\n| - | - |\n| 1 | 2 |\n\n```js\n<script>bad()</script>\n```\n\n<script>window.bad=true</script>\n\n[unsafe](javascript:alert%281%29)')
  for (const expected of ['<h1>Heading</h1>', '<ul>', '<table>', '<pre>', '&lt;script&gt;']) assert.ok(html.includes(expected), expected)
  assert.ok(!html.includes('<script>'))
  assert.ok(!html.includes('href="javascript:'))
})

test('known evidence IDs become numeric real links, unknown markers and code stay literal', () => {
  const html = render('Fact [ev-known]. Unknown [ev-unknown].\n\n`[ev-known]`', [{ id: 'ev-known', label: '1', title: 'Real source', url: 'https://example.org/source' }])
  assert.equal((html.match(/href="https:\/\/example\.org\/source"/g) || []).length, 2)
  assert.ok(html.includes('[1]</a>'))
  assert.ok(html.includes('<summary>Источники · 1</summary>'))
  assert.ok(html.includes('<span class="chat-citation-number">[1]</span><a href="https://example.org/source"'))
  assert.ok(html.includes('<small>example.org</small>'))
  assert.ok(html.includes('[ev-unknown]'))
  assert.ok(html.includes('<code>[ev-known]</code>'))
  assert.equal((html.match(/href=/g) || []).length, 2)
})

test('citation-looking markdown links cannot override evidence URLs or invent numeric sources', () => {
  const html = render('[1](https://evil.example) [ev-known](https://other.example) [99](https://unknown.example) [ev-missing](https://invented.example) [0123456789abcdef0123456789abcdef](https://uncollected.example)', [{ id: 'ev-known', label: '1', title: 'Real source', url: 'https://example.org/source' }])
  assert.equal((html.match(/href="https:\/\/example.org\/source"/g) || []).length, 3)
  for (const domain of ['evil.example', 'other.example', 'unknown.example', 'invented.example', 'uncollected.example']) assert.ok(!html.includes(domain))
  assert.ok(html.includes('[99]'))
  assert.ok(html.includes('[ev-missing]'))
})

test('citation source list omits unsafe reference URLs and keeps its count truthful', () => {
  const html = render('A plain response.', [
    { id: 'unsafe', label: '1', title: 'Unsafe source', url: 'javascript:alert(1)' },
    { id: 'safe', label: '2', title: 'Verified source', url: 'https://example.org/source' },
  ])
  assert.ok(html.includes('<summary>Источники · 1</summary>'))
  assert.ok(html.includes('Verified source'))
  assert.ok(!html.includes('Unsafe source'))
  assert.ok(!html.includes('javascript:'))
})
