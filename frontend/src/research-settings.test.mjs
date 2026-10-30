import test from 'node:test'
import assert from 'node:assert/strict'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import ResearchSettings from './ResearchSettings.tsx'
import ChatComposer from './ChatComposer.tsx'
import { defaultSetup, conversationSetup, maxOutputTokens, setupProblem, settingsPatch, profileMinutes } from './research-settings.mjs'

test('stored settings preserve explicit temperature zero and otherwise use client defaults', () => {
  assert.deepEqual(defaultSetup, { profile: 'standard', timeBudgetMinutes: 30, maxOutputTokens: 5120, temperature: 0.2, topP: 0.8 })
  assert.deepEqual(conversationSetup(), defaultSetup)
  assert.deepEqual(conversationSetup(null), defaultSetup)
  assert.equal(maxOutputTokens, 5120)
  assert.deepEqual(conversationSetup({ research_settings: { profile: 'quick', time_budget_seconds: 120 }, model_profile: { temperature: 0, top_p: 0.5, max_output_tokens: 5120 } }), {
    profile: 'quick', timeBudgetMinutes: 2, temperature: 0, topP: 0.5, maxOutputTokens: 5120,
  })
  const invalid = conversationSetup({ research_settings: { profile: 'quick', time_budget_seconds: 99999 }, model_profile: { temperature: NaN, top_p: 0, max_output_tokens: Infinity } })
  assert.deepEqual(invalid, { ...defaultSetup, profile: 'quick', timeBudgetMinutes: 5 })
})

test('all profile ceilings and finite model bounds are validated', () => {
  for (const [profile, cap] of Object.entries(profileMinutes)) {
    assert.equal(setupProblem({ ...defaultSetup, profile, timeBudgetMinutes: cap }), '')
    assert.ok(setupProblem({ ...defaultSetup, profile, timeBudgetMinutes: cap + 1 }))
  }
  for (const [key, values] of Object.entries({ timeBudgetMinutes: [0, 361, NaN, Infinity], maxOutputTokens: [0, 5121, 1.5, NaN, Infinity], temperature: [-0.1, 2.1, NaN, Infinity], topP: [0, -1, 1.01, NaN, Infinity] })) {
    for (const value of values) assert.ok(setupProblem({ ...defaultSetup, [key]: value }), `${key}=${value}`)
  }
  assert.equal(setupProblem({ ...defaultSetup, temperature: 0, topP: 1, maxOutputTokens: 1, timeBudgetMinutes: 1 }), '')
  assert.equal(setupProblem({ ...defaultSetup, temperature: 2, maxOutputTokens: 5120 }), '')
})

test('patch uses backend names and seconds and refuses invalid settings', () => {
  assert.deepEqual(settingsPatch({ ...defaultSetup, temperature: 0, timeBudgetMinutes: 2 }), {
    research_settings: { profile: 'standard', time_budget_seconds: 120 },
    model_profile: { max_output_tokens: 5120, temperature: 0, top_p: 0.8 },
  })
  assert.throws(() => settingsPatch({ ...defaultSetup, topP: 0 }), RangeError)
})

test('settings expose visible labels, limits, parent save errors and active disabling', () => {
  const html = renderToStaticMarkup(createElement(ResearchSettings, { mode: 'research', value: { ...defaultSetup, temperature: 0 }, onChange() {}, disabled: true, dirty: true, error: 'Не удалось сохранить настройки.', onSave() {} }))
  for (const text of ['Профиль исследования', 'Лимит времени, мин', 'Температура', 'Top-p', 'Только для следующего запуска', 'не обещают длительность', 'настройки клиента', 'Не удалось сохранить настройки.', 'Сохранить настройки', 'aria-describedby=', 'value="0"', 'disabled=""']) assert.ok(html.includes(text), text)
  const chat = renderToStaticMarkup(createElement(ResearchSettings, { mode: 'chat', value: defaultSetup }))
  assert.doesNotMatch(chat, /Профиль исследования|Лимит времени, мин/)
  assert.match(chat, /Параметры модели/)
})

test('composer keeps a single form, mode name, visible task label and context reason', () => {
  const html = renderToStaticMarkup(createElement(ChatComposer, { mode: 'research', text: '', busy: false, active: false, run: { mode: 'research', status: 'completed' }, researchContextRunID: '', researchContextReady: false, settings: defaultSetup }))
  assert.equal((html.match(/<form/g) || []).length, 1)
  assert.match(html, /<label for="conversation-input">Задача исследования<\/label>/)
  assert.match(html, /aria-label="Режим разговора"/)
  assert.match(html, /Нужен сохранённый ответ с проверенными ссылками на источники\./)
})
