import test from 'node:test'
import assert from 'node:assert/strict'
import { resourceReasonLabel, resumeEligibility, researchPresentation, researchPlanPresentation } from './run-presentation.mjs'

const run = { id: 'current', mode: 'research', status: 'paused', pause_reason: 'manual', resource_reason: 'high_model_rss' }
const evidence = { data: [{ id: 'ev1', task_id: 'current' }, { id: 'ev1', task_id: 'current' }, { id: 'foreign', task_id: 'other' }], loading: false, error: '' }
const answer = { run_id: 'current', role: 'assistant', content: '', status: 'pending' }
const event = (seq, type, payload = {}, task_id = 'current') => ({ seq, type, payload, task_id, visibility: 'user' })
const view = (extra = {}) => researchPresentation({ run, runtime: { task_id: 'current' }, evidence, answer, ...extra })

test('Phase 3 manual RSS hold with completed internal report/checkpoint delivers no report', () => {
  const result = view({ runtime: { task_id: 'current', pause_reason: 'manual', resource_reason: 'high_model_rss', verdict: 'PASS', research_report: { artifact_id: '', markdown_artifact_id: '', outcome: 'completed', sections: 4 }, completed_steps: 8 } })
  assert.equal(result.heading, 'Исследование на паузе')
  assert.match(result.state, /лимит памяти модели/)
  assert.match(result.answerLabel, /отсутствует/)
  assert.match(result.reportLabel, /пока не сохранён/)
  assert.equal(result.reportAvailable, false)
  assert.equal(result.evidenceCount, 1)
  assert.equal(resumeEligibility(run).allowed, true)
  assert.match(resumeEligibility(run).explanation, /повторно проверит.*не гарантировано/)
})

test('resume respects manual, guarded, recovery, unknown and nonpaused states', () => {
  for (const reason of ['resource_guard', 'recovery_required', undefined, 'new_backend_reason']) {
    assert.equal(resumeEligibility({ ...run, pause_reason: reason }).allowed, false, String(reason))
  }
  assert.match(resumeEligibility({ ...run, pause_reason: 'resource_guard' }).explanation, /backend.*проверки ресурсов/)
  assert.match(resumeEligibility({ ...run, pause_reason: 'recovery_required' }).explanation, /неизвестный исход/)
  assert.equal(resumeEligibility({ ...run, resource_reason: undefined }).allowed, true)
  assert.equal(resumeEligibility({ ...run, status: 'running' }).allowed, false)
  assert.equal(resumeEligibility(null).allowed, false)
  assert.equal(resumeEligibility({ ...run, id: undefined }, { task_id: undefined, pause_reason: 'resource_guard' }).allowed, true)
})

test('runtime ownership prevents foreign restrictions, artifacts, rounds and gaps', () => {
  const foreign = { task_id: 'other', pause_reason: 'resource_guard', active_round_plan: { subtasks: [] }, research_report: { artifact_id: 'foreign', unrepresented_criteria: ['foreign gap'] } }
  assert.equal(resumeEligibility(run, foreign).allowed, true)
  assert.equal(resumeEligibility(run, { task_id: 'current', pause_reason: 'resource_guard' }).allowed, false)
  const result = view({ runtime: foreign, answer: { ...answer, run_id: 'other', content: 'foreign answer', status: 'completed' } })
  assert.equal(result.reportAvailable, false)
  assert.match(result.reportLabel, /неизвестно/)
  assert.match(result.answerLabel, /неизвестно/)
  assert.deepEqual(result.reportGaps, [])
})

test('unknown, loading, failed and stale evidence never become invented zero counts', () => {
  for (const evidence of [{ loading: false, error: '' }, { loading: true, error: '' }, { loading: false, error: 'offline' }]) {
    const result = view({ runtime: null, evidence })
    assert.equal(result.evidenceCount, null)
    assert.doesNotMatch(result.evidenceLabel, /: 0/)
    assert.match(result.reportLabel, /неизвестно/)
  }
  assert.match(view({ evidence: { ...evidence, error: 'stale' } }).evidenceLabel, /последние загруженные/)
  assert.equal(view({ evidence: { data: [], loading: false, error: '' } }).evidenceCount, 0)
})

test('report availability requires a current artifact and saved canonical text', () => {
  const runtime = { task_id: 'current', research_report: { artifact_id: 'json', markdown_artifact_id: 'md', outcome: 'completed', sections: 2, unrepresented_criteria: ['Missing section'] } }
  const completed = { ...run, status: 'completed' }
  const saved = { ...answer, content: 'Canonical text', status: 'completed' }
  assert.equal(view({ run: completed, runtime, answer: saved }).reportAvailable, true)
  assert.equal(view({ run: completed, runtime }).reportAvailable, false)
  assert.equal(view({ run: completed, runtime, answer: { ...saved, status: 'streaming' } }).reportAvailable, false)
  assert.equal(view({ run: completed, runtime: { task_id: 'current', verdict: 'PASS' }, answer: saved }).reportAvailable, false)
  assert.match(view({ run: completed, runtime }).reportLabel, /текст ответа не подтверждён/)
  assert.deepEqual(view({ run: completed, runtime, answer: saved }).reportGaps, ['Missing section'])
})

test('partial delivered fallback stays partial and current gaps are bounded', () => {
  const runtime = { task_id: 'current', research_report: { artifact_id: 'json', outcome: 'partial', unrepresented_criteria: null } }
  const events = [event(1, 'verification.updated', { status: 'fallback_excerpts' }), event(2, 'verification.completed', { phase: 'evidence_readiness', verdict: 'PARTIAL', missing: Array.from({ length: 10 }, (_, i) => `gap${i}`) }), event(3, 'verification.completed', { phase: 'evidence_readiness', missing: ['foreign'] }, 'other')]
  const result = view({ run: { ...run, status: 'completed', terminal_outcome: 'partial' }, runtime, answer: { ...answer, content: 'Saved excerpts', status: 'completed' }, events })
  assert.equal(result.reportAvailable, true)
  assert.equal(result.fallback, true)
  assert.match(result.answerLabel, /частичный/)
  assert.match(result.reportLabel, /частичный/)
  assert.equal(result.criticalMissing.length, 5)
  assert.ok(!result.criticalMissing.includes('foreign'))
})

test('active plan wins, skips are separate and prior-plan/foreign events cannot finish reused steps', () => {
  const plan = { subtasks: [{ id: 'old', description: 'Initial task', status: 'completed' }] }
  const runtime = { task_id: 'current', active_round_plan: { subtasks: [{ id: 'reuse', description: 'Current task' }, { id: 'skip', description: 'Optional task' }, { id: 'done', description: 'Finished task' }] } }
  const events = [event(1, 'plan.created'), event(2, 'step.completed', { step_id: 'reuse' }), event(3, 'plan.created'), event(4, 'step.skipped', { step_id: 'skip' }), event(5, 'step.completed', { step_id: 'done' }), event(6, 'step.completed', { step_id: 'reuse' }, 'other')]
  const result = researchPlanPresentation(run, runtime, plan, events)
  assert.equal(result.activePlan, true)
  assert.deepEqual(result.steps.map((step) => step.status), ['pending', 'skipped', 'completed'])
  assert.equal(result.completed, 1)
  assert.equal(result.skipped, 1)
  assert.equal(result.processed, 2)
  assert.equal(researchPlanPresentation(run, { ...runtime, task_id: 'other' }, plan, events).steps[0].id, 'old')
})

test('resource and context reasons are readable without leaking arbitrary backend strings', () => {
  assert.match(resourceReasonLabel('high_model_rss'), /памяти модели/)
  assert.match(resourceReasonLabel('context_mismatch'), /контекста/)
  assert.match(resourceReasonLabel('context_unavailable'), /не подтверждён/)
  assert.match(resourceReasonLabel('context_new_reason'), /контекста/)
  assert.doesNotMatch(resourceReasonLabel('raw-private-string'), /raw-private/)
  assert.equal(typeof resourceReasonLabel('constructor'), 'string')
})

test('blocked empty answer, running output and paused streaming fragments remain distinct', () => {
  const blocked = view({ run: { ...run, status: 'failed', terminal_outcome: 'blocked' }, answer: { ...answer, status: 'failed' } })
  assert.match(blocked.heading, /заблокировано/)
  assert.match(blocked.answerLabel, /отсутствует/)
  assert.match(blocked.reportLabel, /не сохранён/)
  const running = view({ run: { ...run, status: 'running' }, answer: { ...answer, content: 'Live fragment', status: 'streaming' } })
  assert.match(running.answerLabel, /поступает/)
  assert.match(running.reportLabel, /продолжается/)
  const paused = view({ answer: { ...answer, content: 'Last fragment', status: 'streaming' } })
  assert.doesNotMatch(paused.answerLabel, /поступает/)
  assert.match(paused.answerLabel, /сохранение текста не подтверждено/)
})
