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

export interface ProviderOutcome {
  provider: string
  state: string
  code: string
  message: string
  retryable: boolean
  http_status: number
  latency_ms: number
  checked_at: string
  result_count: number
  distinct_domains: number
}

export interface ProviderStatus {
  configured: boolean
  enabled: boolean
  health: string
  endpoint: string
  last_checked: string | null
  last_outcome: ProviderOutcome | null
}

export interface ProvidersResponse {
  search: { providers: Record<string, ProviderStatus> }
}

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
  status?: string
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

export type ConversationMode = 'chat' | 'research'

export interface Conversation {
  id: string
  title: string
  mode: ConversationMode
  created_at: string
  updated_at: string
  active_run_id?: string
  latest_run_id?: string
  archived_at?: string
  research_settings?: { web_enabled?: boolean }
  model_profile?: Record<string, number>
}

export interface Citation {
  id?: string
  evidence_id: string
  source_id?: string
  url?: string
  label?: string
}

export interface ConversationMessage {
  id: string
  conversation_id: string
  run_id: string
  role: 'user' | 'assistant' | 'system' | 'tool'
  content: string
  status: string
  parent_message_id?: string
  created_at: string
  updated_at: string
  last_event_seq: number
  metadata?: Record<string, unknown>
  citations?: Citation[]
}

export interface Run {
  id: string
  conversation_id: string
  user_message_id: string
  assistant_message_id: string
  mode: ConversationMode
  operation: string
  status: TaskStatus
  stage?: string
  phase?: string
  error_code?: string
  error_message?: string
  retry_count: number
}

export interface Submission {
  conversation_id: string
  user_message: ConversationMessage
  assistant_message: ConversationMessage
  run: Run
}

export interface SourceRecord {
  id: string
  title: string
  url?: string
  canonical_url?: string
  original_url?: string
  provider?: string
  query?: string
  status?: string
  retrieval_status?: string
  extraction_status?: string
  retrieved_at?: string
  error_code?: string
  error_message?: string
  excerpt?: string
  snippet?: string
  fingerprint?: string
  source_fingerprint?: string
  evidence_id?: string
  attempt_id?: string
  plan_attempt?: number
  step_id?: string
  event_seq?: number
  retryable?: boolean
  try_next_source?: boolean
  user_retry_task?: boolean
  declared_bytes?: number
  effective_limit_bytes?: number
  fetched_bytes?: number
  mime_type?: string
  remaining_candidates?: number
  timestamp?: string
  fallback_attempted?: boolean
}

export interface ResearchPlan {
  goal?: string
  plan?: TaskPlan
  subtasks?: PlanStep[]
  steps?: TaskStep[]
  current_step?: string
}

export interface DoctorResponse {
  status: string
  checks: Record<string, { status: string; message: string }>
  research_limits?: {
    configured: boolean
    pdf_allow_unsandboxed_fallback: boolean
    task_fetch_budget_bytes?: number
    source_fetch_bytes?: number
    pdf_bytes?: number
    pdf_pages?: number
    pdf_text_runes?: number
    pdf_extraction_timeout_seconds?: number
    pdf_download_timeout_seconds?: number
    pdf_memory_bytes?: number
    pdf_temp_bytes?: number
    pdf_cpu_limit?: number
    pdf_pid_limit?: number
    artifact_max_bytes?: number
    sandbox_max_file_bytes?: number
    fetch_temp_task_bytes?: number
    fetch_temp_global_bytes?: number
  }
  pdf_extractor?: { isolated_available: boolean; isolated_status: 'available' | 'unavailable'; fallback_allowed: boolean }
}
