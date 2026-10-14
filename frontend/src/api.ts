import type { AgentEvent, Checkpoint, Message, Task, TaskStep } from './types'

export class ApiError extends Error {
  status: number

  constructor(message: string, status: number) {
    super(message)
    this.status = status
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, {
    ...init,
    headers: { 'Content-Type': 'application/json', ...init?.headers },
  })
  if (!response.ok) {
    let message = `Запрос завершился с кодом ${response.status}`
    try {
      const body = await response.json()
      message = body.error?.message ?? message
    } catch {
      // Keep the status-based message when the response has no JSON body.
    }
    throw new ApiError(message, response.status)
  }
  if (response.status === 204) return undefined as T
  return response.json() as Promise<T>
}

export const api = {
  tasks: () => request<Task[]>('/api/tasks'),
  task: (id: string) => request<Task>(`/api/tasks/${encodeURIComponent(id)}`),
  messages: (id: string) => request<Message[]>(`/api/tasks/${encodeURIComponent(id)}/messages`),
  events: (id: string, after = 0) => request<AgentEvent[]>(`/api/tasks/${encodeURIComponent(id)}/events?after=${after}`),
  checkpoints: (id: string) => request<Checkpoint[]>(`/api/tasks/${encodeURIComponent(id)}/checkpoints`),
  steps: (id: string) => request<TaskStep[]>(`/api/tasks/${encodeURIComponent(id)}/steps`),
  createTask: (title: string, description: string) =>
    request<Task>('/api/tasks', { method: 'POST', body: JSON.stringify({ title, description }) }),
  sendMessage: (id: string, content: string) =>
    request<Message>(`/api/tasks/${encodeURIComponent(id)}/messages`, { method: 'POST', body: JSON.stringify({ content }) }),
  run: (id: string) => request<Task>(`/api/tasks/${encodeURIComponent(id)}/run`, { method: 'POST' }),
  pause: (id: string) => request<Task>(`/api/tasks/${encodeURIComponent(id)}/pause`, { method: 'POST' }),
  resume: (id: string) => request<Task>(`/api/tasks/${encodeURIComponent(id)}/resume`, { method: 'POST' }),
  cancel: (id: string) => request<Task>(`/api/tasks/${encodeURIComponent(id)}/cancel`, { method: 'POST' }),
}
