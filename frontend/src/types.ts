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
  status: 'pending' | 'running' | 'completed' | 'failed'
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
