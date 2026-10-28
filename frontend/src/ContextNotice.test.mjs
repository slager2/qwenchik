import assert from 'node:assert/strict'
import test from 'node:test'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import ContextNotice from './ContextNotice.tsx'

test('context mismatch shows configured, observed and saved values without inventing capacity', () => {
  const html = renderToStaticMarkup(React.createElement(ContextNotice, { budget: {
    configured_context: 131072, server_reported_context: 32768, session_context: 65536,
    available_input_tokens: 26624, reserved_output_tokens: 4096, reserved_system_tokens: 2048,
    context_status: 'context_mismatch',
  } }))
  assert.match(html, /role="alert"/)
  assert.match(html, /inference заблокирован/)
  assert.match(html, /131/)
  assert.match(html, /32/)
  const unavailable = renderToStaticMarkup(React.createElement(ContextNotice, { budget: {
    configured_context: 32768, server_reported_context: null, session_context: 32768,
    available_input_tokens: null, reserved_output_tokens: 4096, reserved_system_tokens: 2048,
    context_status: 'unavailable',
  } }))
  assert.match(unavailable, /сервер неизвестно/)
  assert.match(unavailable, /Input budget: неизвестно/)
})
