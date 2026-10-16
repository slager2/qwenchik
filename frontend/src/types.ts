export type TaskStatus =
  | 'inbox'
  | 'queued'
  | 'planning'
  | 'running'
  | 'waiting_approval'
  | 'paused'
  | 'verifying'
  | 'completed'
  | 'failed'
  | 'cancelled'

export type TaskStage =
  | 'intake'
  | 'planning'
  | 'search'
  | 'fetch'
  | 'extract'
  | 'browser'
  | 'code_execution'
  | 'synthesis'
  | 'verification'
  | 'finalization'

export interface PlanStep {
  id: string
  kind: string
  description: string
  dependencies: string[]
  expected_output: string
  success_criteria: string[]
}

export interface TaskPlan {
  schema_version: number
  goal: string
  subtasks: PlanStep[]
  max_steps: number
  final_deliverable: string
}

export interface Task {
  id: string
  title: string
  description: string
  status: TaskStatus
  stage?: TaskStage
  priority: number
  plan?: TaskPlan
  version: number
  created_at: string
  updated_at: string
}

export interface TaskStep {
  id: string
  task_id: string
  step_id: string
  position: number
  kind: string
  description: string
  dependencies: string[]
  expected_output: string
  success_criteria: string[]
  status: 'pending' | 'running' | 'completed' | 'failed' | 'skipped'
  attempts: number
  created_at: string
  updated_at: string
}

export interface Message {
  id: string
  task_id: string
  role: 'user' | 'assistant' | 'system'
  content: string
  created_at: string
}

export interface MessageDeltaPayload {
  stream_id: string
  message_id: string
  chunk_index: number
  delta: string
}

export interface DraftMessage extends MessageDeltaPayload {
  content: string
  seq: number
}

export interface AgentEvent {
  id: string
  task_id: string
  seq: number
  type: string
  schema_version: number
  created_at: string
  stage?: TaskStage
  visibility: 'user' | 'internal' | 'audit'
  payload: Record<string, unknown>
}

export interface Checkpoint {
  id: string
  task_id: string
  plan_version: number
  state: unknown
  last_event_seq: number
  created_at: string
}

export interface Artifact {
  id: string
  task_id: string
  name: string
  mime_type: string
  sha256: string
  size_bytes: number
  created_at: string
}

export interface QuoteSpan {
  start: number
  end: number
  page?: number
  text: string
}

export interface Evidence {
  id: string
  task_id: string
  provider: string
  original_query: string
  original_url: string
  canonical_url: string
  title: string
  language: string
  retrieved_at: string
  http_status: number
  content_sha256: string
  extraction_method: string
  raw_artifact_id?: string
  text_artifact_id?: string
  excerpt: string
  quote_spans: QuoteSpan[]
  quality_flags: string[]
  taint: string
}

export interface Approval {
  id: string
  task_id: string
  tool_name: string
  arguments: Record<string, unknown>
  target: string
  risk_explanation: string
  expires_at: string
  status: 'pending' | 'approved' | 'rejected' | 'expired'
  created_at: string
  decision_at?: string
  actor?: string
  consumed_at?: string
}

export type MemoryStatus = 'candidate' | 'accepted' | 'stale' | 'superseded' | 'rejected'

export interface Memory {
  id: string
  kind: string
  status: MemoryStatus
  text: string
  metadata: Record<string, unknown>
  scope: 'global' | 'task'
  confidence?: number
  source_evidence_id?: string
  source_task_id?: string
  user_editable: boolean
  retention_policy: string
  created_at: string
  updated_at: string
}

export interface MemoryMatch {
  memory: Memory
  relevance: number
}
