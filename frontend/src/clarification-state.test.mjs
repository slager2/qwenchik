import test from 'node:test'
import assert from 'node:assert/strict'
import { clarificationChoice, clarificationUsable, messageClarification, readClarifyPreference, writeClarifyPreference } from './clarification-state.mjs'

const clarification = {
  status: 'ready',
  interpretations: [{ id: 'i1', title: 'С нуля', goal: 'План с нуля.' }, { id: 'i2', title: 'Из IT', goal: 'Переход из IT.' }],
  questions: [{ id: 'q1', question: 'Сколько часов в неделю?', options: ['5', '10'] }, { id: 'q2', question: 'Опыт?', options: ['Нет', 'Да'] }],
}

test('choice merges the selected reading, answers and extra text', () => {
  assert.deepEqual(clarificationChoice(clarification, 'i2', { q1: '10', q2: '  ' }, '  геномика '), {
    interpretation_title: 'Из IT', interpretation_goal: 'Переход из IT.',
    answers: [{ question: 'Сколько часов в неделю?', answer: '10' }], extra: 'геномика',
  })
  assert.equal(clarificationChoice(clarification, '', {}, '  '), undefined)
  assert.equal(clarificationChoice(clarification, 'missing', {}), undefined)
  assert.equal(clarificationChoice(null, 'i1', { q1: '5' }, 'x').extra, 'x')
  assert.equal(clarificationChoice(clarification, '', {}, 'я'.repeat(1200)).extra.length, 1000)
})

test('only ready responses with options are shown', () => {
  assert.equal(clarificationUsable(clarification), true)
  assert.equal(clarificationUsable({ ...clarification, interpretations: [], questions: [] }), false)
  assert.equal(clarificationUsable({ status: 'unavailable', reason: 'timeout', interpretations: [], questions: [] }), false)
  assert.equal(clarificationUsable(undefined), false)
})

test('preference defaults on and tolerates storage failures', () => {
  const values = new Map()
  const storage = { getItem: (key) => values.get(key) ?? null, setItem: (key, value) => values.set(key, value) }
  assert.equal(readClarifyPreference(storage), true)
  writeClarifyPreference(storage, false)
  assert.equal(readClarifyPreference(storage), false)
  const broken = { getItem: () => { throw new Error('denied') }, setItem: () => { throw new Error('denied') } }
  assert.equal(readClarifyPreference(broken), true)
  assert.doesNotThrow(() => writeClarifyPreference(broken, true))
})

test('transcript summary reads stored user metadata defensively', () => {
  assert.deepEqual(messageClarification({ role: 'user', metadata: { clarification: { interpretation_title: 'С нуля', answers: [{ question: 'Часы?', answer: '10' }, { answer: 7 }] } } }), { title: 'С нуля', goal: '', answers: [{ question: 'Часы?', answer: '10' }], extra: '' })
  assert.equal(messageClarification({ role: 'assistant', metadata: { clarification: { extra: 'x' } } }), null)
  assert.equal(messageClarification({ role: 'user', metadata: { clarification: 'bad' } }), null)
  assert.equal(messageClarification({ role: 'user' }), null)
})
