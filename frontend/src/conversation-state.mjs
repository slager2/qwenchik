export const terminal = (status) => ['completed', 'failed', 'cancelled'].includes(status)

export function mergeMessages(current, incoming) {
  const next = new Map(current.map((message) => [message.id, message]))
  for (const message of incoming) {
    if (!message || typeof message.id !== 'string' || typeof message.content !== 'string') continue
    const previous = next.get(message.id)
    if (previous && (previous.last_event_seq ?? 0) > (message.last_event_seq ?? 0)) continue
    next.set(message.id, { ...message })
  }
  return [...next.values()].sort((a, b) => a.created_at.localeCompare(b.created_at))
}

export function createStreamState(runID) {
  return { runID, cursor: 0, streams: {}, events: [], needsSync: false }
}

export function applyRunEvent(state, messages, event) {
  if (!event || event.task_id !== state.runID || !Number.isSafeInteger(event.seq) || event.seq <= state.cursor || typeof event.type !== 'string' || !event.payload || typeof event.payload !== 'object') return { state, messages }
  const next = { ...state, cursor: event.seq }
  if (event.type !== 'message.delta') {
    next.events = [...state.events, event].slice(-150)
    return { state: next, messages }
  }
  const { stream_id, message_id, chunk_index, delta } = event.payload
  if (typeof stream_id !== 'string' || typeof message_id !== 'string' || typeof delta !== 'string' || !Number.isSafeInteger(chunk_index) || chunk_index < 0) return { state: { ...next, needsSync: true }, messages }
  const message = messages.find((item) => item.id === message_id && item.run_id === state.runID)
  if (!message) return { state: { ...next, needsSync: true }, messages }
  const previous = state.streams[message_id]
  if (previous?.id === stream_id && chunk_index <= previous.index) return { state: next, messages }
  if ((previous?.id !== stream_id && chunk_index !== 0) || (previous?.id === stream_id && chunk_index !== previous.index + 1)) return { state: { ...next, needsSync: true }, messages }
  next.streams = { ...state.streams, [message_id]: { id: stream_id, index: chunk_index } }
  if (event.seq <= message.last_event_seq) return { state: next, messages }
  const content = chunk_index === 0 ? delta : message.content + delta
  return { state: next, messages: messages.map((item) => item.id === message_id ? { ...item, content, status: 'streaming', last_event_seq: event.seq } : item) }
}

export function safeURL(value) {
  if (typeof value !== 'string') return ''
  try {
    const url = new URL(value)
    return ['https:', 'http:'].includes(url.protocol) && !url.username && !url.password ? url.href : ''
  } catch { return '' }
}

export function normalizeSources(value) {
  const items = Array.isArray(value) ? value : Array.isArray(value?.sources) ? value.sources : []
  return items.filter((item) => item && typeof item === 'object').map((item, index) => {
    const result = {
      id: String(item.id ?? item.source_id ?? item.source_fingerprint ?? item.fingerprint ?? `source-${index}`),
      title: typeof item.title === 'string' ? item.title : '',
      canonical_url: safeURL(item.canonical_url || item.url || item.original_url || item.target_url),
      status: String(item.status ?? item.retrieval_status ?? 'discovered'),
    }
    if (result.canonical_url && [...new URL(result.canonical_url).searchParams.keys()].some((key) => /token|secret|password|credential|authorization|api.?key|signature/i.test(key))) result.canonical_url = ''
    for (const key of ['provider', 'query', 'excerpt', 'snippet', 'fingerprint', 'source_fingerprint', 'evidence_id', 'attempt_id', 'step_id']) {
      if (typeof item[key] === 'string') result[key] = item[key].slice(0, 2000)
    }
    if (typeof item.error_code === 'string') result.error_code = sourceErrorCode(item.error_code)
    if (Array.isArray(item.quality_flags)) result.quality_flags = item.quality_flags.filter((flag) => typeof flag === 'string').slice(0, 64).map((flag) => flag.slice(0, 200))
    for (const key of ['declared_bytes', 'effective_limit_bytes', 'fetched_bytes', 'remaining_candidates', 'plan_attempt', 'event_seq']) {
      const maximum = key === 'remaining_candidates' || key === 'plan_attempt' ? 1000000 : key === 'event_seq' ? Number.MAX_SAFE_INTEGER : 2 ** 40
      if (Number.isSafeInteger(item[key]) && item[key] >= 0 && item[key] <= maximum) result[key] = item[key]
    }
    for (const key of ['retryable', 'try_next_source', 'user_retry_task', 'fallback_attempted']) {
      if (typeof item[key] === 'boolean') result[key] = item[key]
    }
    if (['application/pdf', 'text/html', 'application/xhtml+xml', 'text/plain', 'application/json', 'application/octet-stream'].includes(item.mime_type)) result.mime_type = item.mime_type
    for (const key of ['timestamp', 'retrieved_at']) {
      const stamp = item[key] ?? (key === 'timestamp' ? item.time : undefined)
      if (typeof stamp === 'string' && /^\d{4}-\d{2}-\d{2}T/.test(stamp) && Number.isFinite(Date.parse(stamp))) result[key] = stamp
    }
    return result
  })
}

const safeSourceCodes = new Set(['url_rejected_ssrf', 'url_validation_failed', 'search_validation_timeout', 'fetch_timeout', 'fetch_dns_failed', 'fetch_failed', 'fetch_source_unavailable', 'fetch_request_failed', 'fetch_budget_exceeded', 'task_fetch_budget_exceeded', 'source_response_too_large', 'pdf_source_too_large', 'pdf_extraction_input_too_large', 'pdf_page_limit_exceeded', 'pdf_text_limit_exceeded', 'pdf_resource_exceeded', 'pdf_timeout', 'pdf_extractor_unavailable', 'pdf_malformed', 'pdf_encrypted', 'fetch_outcome_unknown', 'extraction_empty', 'empty_after_extraction', 'extraction_challenge', 'challenge_detected', 'unsupported_content', 'unsupported_mime', 'pdf_extraction_failed', 'source_unavailable', 'verification_incomplete', 'source_failed', 'pdf_no_text', 'invalid_tool_arguments', 'approval_required', 'approval_expired', 'tool_denied', 'unknown_research_target'])
export const sourceErrorCode = (code) => code === '' || safeSourceCodes.has(code) ? code : 'source_failed'

export function citationReferences(evidence) {
  return evidence.filter((item) => item && typeof item.id === 'string' && !['rejected', 'failed', 'empty'].includes(item.status) && !(Array.isArray(item.quality_flags) ? item.quality_flags : []).some((flag) => /rejected|challenge|empty/.test(String(flag)))).map((item, index) => ({ id: item.id, label: String(index + 1), title: typeof item.title === 'string' && item.title ? item.title : 'Источник', url: safeURL(item.canonical_url || item.url || item.original_url) })).filter((item) => item.url)
}

export function legacyDraft(events, savedIDs = []) {
  let draft = null
  for (const event of [...events].sort((a, b) => a.seq - b.seq)) {
    if (event.type !== 'message.delta' || !Number.isSafeInteger(event.seq)) continue
    const payload = event.payload
    if (!payload || typeof payload.stream_id !== 'string' || typeof payload.message_id !== 'string' || typeof payload.delta !== 'string' || !Number.isSafeInteger(payload.chunk_index)) continue
    if (!draft || draft.stream_id !== payload.stream_id || draft.message_id !== payload.message_id) {
      if (payload.chunk_index !== 0) continue
      draft = { ...payload, content: payload.delta, seq: event.seq }
    } else if (event.seq > draft.seq && payload.chunk_index === draft.chunk_index + 1) {
      draft = { ...payload, content: draft.content + payload.delta, seq: event.seq }
    }
  }
  return draft && !savedIDs.includes(draft.message_id) ? draft : null
}
