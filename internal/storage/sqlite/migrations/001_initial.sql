CREATE TABLE IF NOT EXISTS tasks (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    description TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('inbox','queued','planning','running','waiting_approval','paused','verifying','completed','failed','cancelled')),
    stage TEXT,
    priority INTEGER NOT NULL DEFAULT 0,
    plan_json TEXT,
    version INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    idempotency_key TEXT UNIQUE
);

CREATE TABLE IF NOT EXISTS messages (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    role TEXT NOT NULL CHECK (role IN ('user','assistant','system')),
    content TEXT NOT NULL,
    created_at TEXT NOT NULL,
    idempotency_key TEXT UNIQUE
);
CREATE INDEX IF NOT EXISTS idx_messages_task_created ON messages(task_id, created_at);

CREATE TABLE IF NOT EXISTS task_steps (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    step_id TEXT NOT NULL,
    position INTEGER NOT NULL,
    kind TEXT NOT NULL,
    description TEXT NOT NULL,
    dependencies_json TEXT NOT NULL,
    expected_output TEXT NOT NULL,
    success_criteria_json TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending','running','completed','failed')),
    attempts INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(task_id, step_id)
);
CREATE INDEX IF NOT EXISTS idx_task_steps_task_status ON task_steps(task_id, status, position);

CREATE TABLE IF NOT EXISTS task_stages (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    stage TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('running','completed','failed','paused','cancelled')),
    started_at TEXT NOT NULL,
    finished_at TEXT,
    updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_task_stages_task_started ON task_stages(task_id, started_at);

CREATE TABLE IF NOT EXISTS events (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    seq INTEGER NOT NULL,
    type TEXT NOT NULL,
    schema_version INTEGER NOT NULL,
    created_at TEXT NOT NULL,
    stage TEXT,
    visibility TEXT NOT NULL CHECK (visibility IN ('user','internal','audit')),
    payload_json TEXT NOT NULL,
    UNIQUE(task_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_events_task_seq ON events(task_id, seq);

CREATE TABLE IF NOT EXISTS checkpoints (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    plan_version INTEGER NOT NULL,
    state_json TEXT NOT NULL,
    last_event_seq INTEGER NOT NULL,
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_checkpoints_task_created ON checkpoints(task_id, created_at);

CREATE TABLE IF NOT EXISTS approvals (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    tool_name TEXT NOT NULL,
    arguments_json TEXT NOT NULL,
    target TEXT NOT NULL DEFAULT '',
    risk_explanation TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending','approved','rejected','expired')),
    created_at TEXT NOT NULL,
    decision_at TEXT,
    actor TEXT NOT NULL DEFAULT '',
    idempotency_key TEXT UNIQUE
);
CREATE INDEX IF NOT EXISTS idx_approvals_task_status ON approvals(task_id, status, created_at);

CREATE TABLE IF NOT EXISTS artifacts (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    mime_type TEXT NOT NULL,
    sha256 TEXT NOT NULL,
    size_bytes INTEGER NOT NULL,
    storage_path TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_artifacts_task_created ON artifacts(task_id, created_at);

CREATE TABLE IF NOT EXISTS evidence (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    original_query TEXT NOT NULL DEFAULT '',
    original_url TEXT NOT NULL,
    canonical_url TEXT NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    language TEXT NOT NULL DEFAULT '',
    retrieved_at TEXT NOT NULL,
    http_status INTEGER NOT NULL DEFAULT 0,
    content_sha256 TEXT NOT NULL DEFAULT '',
    extraction_method TEXT NOT NULL DEFAULT '',
    raw_artifact_id TEXT REFERENCES artifacts(id) ON DELETE SET NULL,
    text_artifact_id TEXT REFERENCES artifacts(id) ON DELETE SET NULL,
    quote_spans_json TEXT NOT NULL DEFAULT '[]',
    quality_flags_json TEXT NOT NULL DEFAULT '[]',
    UNIQUE(task_id, canonical_url, content_sha256)
);
CREATE INDEX IF NOT EXISTS idx_evidence_task_retrieved ON evidence(task_id, retrieved_at);

CREATE TABLE IF NOT EXISTS memories (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('candidate','accepted','stale','superseded','rejected')),
    text TEXT NOT NULL,
    metadata_json TEXT NOT NULL,
    scope TEXT NOT NULL DEFAULT 'task',
    confidence REAL CHECK (confidence IS NULL OR (confidence >= 0 AND confidence <= 1)),
    source_evidence_id TEXT REFERENCES evidence(id) ON DELETE SET NULL,
    source_task_id TEXT REFERENCES tasks(id) ON DELETE SET NULL,
    user_editable INTEGER NOT NULL DEFAULT 1 CHECK (user_editable IN (0,1)),
    retention_policy TEXT NOT NULL DEFAULT 'until_deleted',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_memories_status_kind ON memories(status, kind, updated_at);

CREATE TABLE IF NOT EXISTS provider_runs (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    operation TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('running','completed','failed','cancelled')),
    request_hash TEXT NOT NULL DEFAULT '',
    started_at TEXT NOT NULL,
    finished_at TEXT,
    error_code TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_provider_runs_task_started ON provider_runs(task_id, started_at);

CREATE TABLE IF NOT EXISTS idempotency_keys (
    key TEXT PRIMARY KEY,
    operation TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    created_at TEXT NOT NULL
);
