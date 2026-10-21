CREATE TABLE research_sessions (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL UNIQUE REFERENCES tasks(id) ON DELETE CASCADE,
    conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    goal TEXT NOT NULL,
    status TEXT NOT NULL,
    profile TEXT NOT NULL,
    policy_json TEXT NOT NULL,
    budget_json TEXT NOT NULL,
    model_snapshot_json TEXT NOT NULL,
    counters_json TEXT NOT NULL,
    current_round INTEGER NOT NULL DEFAULT 0,
    current_phase TEXT NOT NULL DEFAULT '',
    plan_version INTEGER NOT NULL DEFAULT 0,
    stop_reason TEXT NOT NULL DEFAULT '',
    started_at TEXT,
    deadline_at TEXT NOT NULL,
    heartbeat_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX idx_research_sessions_conversation ON research_sessions(conversation_id, created_at, id);
