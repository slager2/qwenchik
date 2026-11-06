// Pre-research clarification: pure helpers shared by ChatRoute and the panel.

export const CLARIFY_PREFERENCE_KEY = 'qwenchik.clarifyResearch'
export const CLARIFY_EXTRA_LIMIT = 1000

/** Whether research questions are clarified before starting (default on). */
export function readClarifyPreference(storage) {
  try { return storage?.getItem(CLARIFY_PREFERENCE_KEY) !== 'false' } catch { return true }
}

export function writeClarifyPreference(storage, enabled) {
  try { storage?.setItem(CLARIFY_PREFERENCE_KEY, String(!!enabled)) } catch { /* Private mode can deny storage. */ }
}

/** A clarify response is useful only when it offers something to choose. */
export function clarificationUsable(clarification) {
  return clarification?.status === 'ready' && ((clarification.interpretations?.length ?? 0) > 0 || (clarification.questions?.length ?? 0) > 0)
}

/**
 * The clarification object for POST /messages, or undefined when the user
 * chose nothing (the turn is then identical to an unclarified one).
 * `answers` maps question id to the chosen or typed answer.
 */
export function clarificationChoice(clarification, selectedID, answers = {}, extra = '') {
  const choice = {}
  const selected = clarification?.interpretations?.find((item) => item.id === selectedID)
  if (selected) { choice.interpretation_title = selected.title; choice.interpretation_goal = selected.goal }
  const answered = (clarification?.questions ?? [])
    .map((question) => ({ question: question.question, answer: String(answers[question.id] ?? '').trim() }))
    .filter((item) => item.answer)
  if (answered.length) choice.answers = answered
  const note = String(extra ?? '').trim().slice(0, CLARIFY_EXTRA_LIMIT)
  if (note) choice.extra = note
  return Object.keys(choice).length ? choice : undefined
}

/** Clarification stored on a user message, for the transcript summary. */
export function messageClarification(message) {
  const value = message?.role === 'user' ? message.metadata?.clarification : undefined
  if (!value || typeof value !== 'object') return null
  const title = typeof value.interpretation_title === 'string' ? value.interpretation_title : ''
  const goal = typeof value.interpretation_goal === 'string' ? value.interpretation_goal : ''
  const answers = Array.isArray(value.answers) ? value.answers.filter((item) => item && typeof item.answer === 'string' && item.answer).map((item) => ({ question: typeof item.question === 'string' ? item.question : '', answer: item.answer })) : []
  const extra = typeof value.extra === 'string' ? value.extra : ''
  if (!title && !goal && !answers.length && !extra) return null
  return { title, goal, answers, extra }
}
