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
	terminal_outcome?: string
	pause_reason?: string
	resource_reason?: string
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

export interface VerificationCriterionRecord {
  step_id: string
  criterion_index: number
  classification: 'critical' | 'supplementary'
  met: boolean
  evidence_ids: string[]
}

export type MetricSource = 'exact' | 'estimated' | 'unavailable'
export type TelemetryMetricValue = string | number | boolean

export interface TelemetryMetric<T extends TelemetryMetricValue> {
  value: T | null
  source: MetricSource
  unit: string
  updated_at: string
  error?: string
}

export interface TelemetryCapabilities {
  cpu: boolean
  ram: boolean
  gpu: boolean
  gpu_temperature: boolean
  gpu_utilization: boolean
  power: boolean
  battery: boolean
  process_rss: boolean
  llama_health: boolean
  llama_timings: boolean
  llama_metrics: boolean
  llama_slots: boolean
}

export interface SystemMetadata {
  os?: string
  platform?: string
  platform_version?: string
  kernel_version?: string
  kernel_arch?: string
  uptime_seconds: TelemetryMetric<number>
}

export interface GPUTelemetry {
  name: TelemetryMetric<string>
  utilization: TelemetryMetric<number>
  temperature: TelemetryMetric<number>
  vram_used: TelemetryMetric<number>
  vram_free: TelemetryMetric<number>
  vram_total: TelemetryMetric<number>
  power_watts: TelemetryMetric<number>
}

export interface SystemTelemetry {
  metadata: SystemMetadata
  cpu_usage: TelemetryMetric<number>
  cpu_temperature: TelemetryMetric<number>
  ram_used: TelemetryMetric<number>
  ram_free: TelemetryMetric<number>
  ram_total: TelemetryMetric<number>
  gpus: GPUTelemetry[]
  agent_rss: TelemetryMetric<number>
  llama_server_rss: TelemetryMetric<number>
  power_source: TelemetryMetric<string>
  power_watts: TelemetryMetric<number>
  battery_percent: TelemetryMetric<number>
  battery_charging: TelemetryMetric<boolean>
  llama_health: TelemetryMetric<string>
  llama_slots_active: TelemetryMetric<number>
  llama_slots_total: TelemetryMetric<number>
}

export interface InferenceTelemetry {
  status: 'queued' | 'loading' | 'prefilling' | 'generating' | 'completed' | 'failed' | 'cancelled'
  source?: string
  updated_at: string
  requested_tokens: TelemetryMetric<number>
  prompt_tokens: TelemetryMetric<number>
  output_tokens: TelemetryMetric<number>
  total_tokens: TelemetryMetric<number>
  cached_prompt_tokens: TelemetryMetric<number>
  prompt_tokens_per_second: TelemetryMetric<number>
  output_tokens_per_second: TelemetryMetric<number>
  ttft_ms: TelemetryMetric<number>
  prompt_ms: TelemetryMetric<number>
  generation_ms: TelemetryMetric<number>
  context_tokens: TelemetryMetric<number>
  context_window: TelemetryMetric<number>
}

export interface InferenceSummary {
  invocation_id: string
  invocation_status: 'completed' | 'failed' | 'unknown'
  snapshot_at: string
  inference: InferenceTelemetry
}

export interface RuntimeTelemetry {
  timestamp: string
  stale_after_ms: number
  capabilities: TelemetryCapabilities
  system: SystemTelemetry
  inference: InferenceTelemetry
}

export interface RunRuntime {
  bundle_hash?: string
  bundle_version?: number
  telemetry?: RuntimeTelemetry
  telemetry_revision?: number
  runtime_warnings?: string[]
  inference_summary?: InferenceSummary
  task_id: string
  cursor: number
  status: TaskStatus
  stage?: string
  terminal_outcome?: string
  pause_reason?: string
  resource_reason?: string
  resume_after?: string
  resource_probe_attempts?: number
  profile?: string
  step?: string
  completed_steps?: number
  deadline_at?: string
  remaining_seconds?: number
  elapsed_seconds?: number
  budget?: Record<string, number>
  counters?: Record<string, number>
  budget_remaining?: Record<string, number>
  completion?: { continuations: number; verdict?: string; missing?: string[] }
  max_continuations?: number
  compactions?: number
  context_snapshot_hash?: string
  context_snapshot_number?: number
  contradictions?: string[]
  verdict?: string
  notification?: { status: string; attempt?: number; channel?: string }
  keep_awake_requested?: boolean
  runtime?: {
	 enabled?: boolean
    keep_awake?: boolean
    sleep_policy?: string
    warnings?: string[]
    active_warnings?: string[]
    resources?: { free_ram_bytes?: number | null; rss_bytes?: number | null; gpu_free_vram_mb?: number | null; gpu_temp_c?: number | null; snapshot_at?: string; power_source?: string; warnings?: string[]; active_warnings?: string[] }
    sleep?: { active?: boolean; available?: boolean; provider?: string; failure_reason?: string }
  }
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
