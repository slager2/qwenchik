import type { AgentEvent, Approval, Artifact, Checkpoint, Evidence, Memory, MemoryMatch, MemoryStatus, Message, ProvidersResponse, Task, TaskStep } from './types'
import type { Conversation, ConversationMessage, ConversationMode, DoctorResponse, ResearchPlan, Run, RunRuntime, SourceRecord, Submission } from './types'
import { recordHTTP } from './network-debug'

export class ApiError extends Error {
  status: number
  code: string
  retryable: boolean

  constructor(message: string, status: number, code = '', retryable = false) {
    super(message)
    this.status = status
    this.code = code
    this.retryable = retryable
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const started = performance.now()
  const method = init?.method || 'GET'
  let status: number | undefined
  let responseBytes: number | undefined
  let recorded = false
  const record = (aborted = false) => {
    if (recorded) return
    recorded = true
    recordHTTP({ method, path, status, duration_ms: performance.now() - started, response_bytes: responseBytes, aborted })
  }
  try {
    const response = await fetch(path, {
      ...init,
      headers: { 'Content-Type': 'application/json', ...init?.headers },
    })
    status = response.status
    const length = response.headers.get('content-length')
    if (length && /^\d{1,15}$/.test(length)) responseBytes = Number(length)
    if (!response.ok) {
      let message = `Запрос завершился с кодом ${response.status}`
      let code = ''
      let retryable = false
      try {
        const body = await response.json()
        message = body.error?.message ?? message
        code = body.error?.code ?? ''
        retryable = body.error?.retryable === true
      } catch {
      }
      record()
      throw new ApiError(message, response.status, code, retryable)
    }
    if (response.status === 204) { record(); return undefined as T }
    const result = await response.json() as T
    record()
    return result
  } catch (caught) {
    record(init?.signal?.aborted === true || caught instanceof DOMException && caught.name === 'AbortError')
    throw caught
  }
}

export const api = {
  health: (signal?: AbortSignal) => request<{ status: string; database: string }>('/api/health', { signal }),
  conversations: (signal?: AbortSignal) => request<Conversation[]>('/api/conversations', { signal }),
  conversation: (id: string, signal?: AbortSignal) => request<Conversation>(`/api/conversations/${encodeURIComponent(id)}`, { signal }),
  createConversation: (title: string, mode: ConversationMode, key: string) => request<Conversation>('/api/conversations', { method: 'POST', headers: { 'Idempotency-Key': key }, body: JSON.stringify({ title, mode }) }),
  updateConversation: (id: string, patch: { title?: string; mode?: ConversationMode }) => request<Conversation>(`/api/conversations/${encodeURIComponent(id)}`, { method: 'PATCH', body: JSON.stringify(patch) }),
  deleteConversation: (id: string) => request<void>(`/api/conversations/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  conversationMessages: (id: string, signal?: AbortSignal) => request<ConversationMessage[]>(`/api/conversations/${encodeURIComponent(id)}/messages`, { signal }),
  submitMessage: (id: string, content: string, mode: ConversationMode, key: string) => request<Submission>(`/api/conversations/${encodeURIComponent(id)}/messages`, { method: 'POST', headers: { 'Idempotency-Key': key }, body: JSON.stringify({ content, mode }) }),
  conversationRun: (id: string, signal?: AbortSignal) => request<Run>(`/api/runs/${encodeURIComponent(id)}`, { signal }),
  runRuntime: (id: string, signal?: AbortSignal) => request<RunRuntime>(`/api/runs/${encodeURIComponent(id)}/runtime`, { signal }),
  cancelRun: (id: string) => request<Run>(`/api/runs/${encodeURIComponent(id)}/cancel`, { method: 'POST' }),
  retryRun: (id: string, key: string) => request<Submission>(`/api/runs/${encodeURIComponent(id)}/retry`, { method: 'POST', headers: { 'Idempotency-Key': key } }),
  continueRun: (id: string, key: string) => request<Submission>(`/api/runs/${encodeURIComponent(id)}/continue`, { method: 'POST', headers: { 'Idempotency-Key': key } }),
  runEvents: (id: string, after = 0, signal?: AbortSignal) => request<AgentEvent[]>(`/api/runs/${encodeURIComponent(id)}/events?after=${after}`, { signal }),
  runSources: (id: string, signal?: AbortSignal) => request<SourceRecord[]>(`/api/runs/${encodeURIComponent(id)}/sources`, { signal }),
  runEvidence: (id: string, signal?: AbortSignal) => request<Evidence[]>(`/api/runs/${encodeURIComponent(id)}/evidence`, { signal }),
  runPlan: (id: string, signal?: AbortSignal) => request<ResearchPlan>(`/api/runs/${encodeURIComponent(id)}/plan`, { signal }),
  doctor: (signal?: AbortSignal) => request<DoctorResponse>('/api/doctor', { signal }),
  providers: (signal?: AbortSignal) => request<ProvidersResponse>('/api/providers', { signal }),
  tasks: () => request<Task[]>('/api/tasks'),
  task: (id: string) => request<Task>(`/api/tasks/${encodeURIComponent(id)}`),
  messages: (id: string) => request<Message[]>(`/api/tasks/${encodeURIComponent(id)}/messages`),
  events: (id: string, after = 0) => request<AgentEvent[]>(`/api/tasks/${encodeURIComponent(id)}/events?after=${after}`),
  checkpoints: (id: string) => request<Checkpoint[]>(`/api/tasks/${encodeURIComponent(id)}/checkpoints`),
  steps: (id: string) => request<TaskStep[]>(`/api/tasks/${encodeURIComponent(id)}/steps`),
  artifacts: (id: string) => request<Artifact[]>(`/api/tasks/${encodeURIComponent(id)}/artifacts`),
  evidence: (id: string) => request<Evidence[]>(`/api/tasks/${encodeURIComponent(id)}/evidence`),
  approvals: (id: string) => request<Approval[]>(`/api/tasks/${encodeURIComponent(id)}/approvals`),
  createTask: (title: string, description: string) =>
    request<Task>('/api/tasks', { method: 'POST', body: JSON.stringify({ title, description }) }),
  sendMessage: (id: string, content: string) =>
    request<Message>(`/api/tasks/${encodeURIComponent(id)}/messages`, { method: 'POST', body: JSON.stringify({ content }) }),
  run: (id: string) => request<Task>(`/api/tasks/${encodeURIComponent(id)}/run`, { method: 'POST' }),
  pause: (id: string) => request<Task>(`/api/tasks/${encodeURIComponent(id)}/pause`, { method: 'POST' }),
  resume: (id: string) => request<Task>(`/api/tasks/${encodeURIComponent(id)}/resume`, { method: 'POST' }),
  cancel: (id: string) => request<Task>(`/api/tasks/${encodeURIComponent(id)}/cancel`, { method: 'POST' }),
  approve: (id: string) => request<Approval>(`/api/approvals/${encodeURIComponent(id)}/approve`, { method: 'POST' }),
  reject: (id: string) => request<Approval>(`/api/approvals/${encodeURIComponent(id)}/reject`, { method: 'POST' }),
  memories: () => request<Memory[]>('/api/memories'),
  searchMemories: (query: string) => request<MemoryMatch[]>(`/api/memories/search?q=${encodeURIComponent(query)}`),
  createMemory: (kind: string, text: string) => request<Memory>('/api/memories', { method: 'POST', body: JSON.stringify({ kind, text, scope: 'global' }) }),
  updateMemory: (id: string, kind: string, text: string, metadata: Record<string, unknown>) =>
    request<Memory>(`/api/memories/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify({ kind, text, metadata }) }),
  decideMemory: (id: string, status: Extract<MemoryStatus, 'accepted' | 'rejected' | 'stale' | 'superseded'>) =>
    request<Memory>(`/api/memories/${encodeURIComponent(id)}/${status === 'accepted' ? 'accept' : status === 'rejected' ? 'reject' : status === 'superseded' ? 'supersede' : 'stale'}`, { method: 'POST' }),
}
