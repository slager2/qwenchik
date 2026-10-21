CREATE TABLE conversations (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    mode TEXT NOT NULL CHECK(mode IN ('chat','research')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    archived_at TEXT,
    deleted_at TEXT,
    head_message_id TEXT,
    model_profile_json TEXT NOT NULL DEFAULT '{}',
    research_settings_json TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX idx_conversations_updated ON conversations(deleted_at,updated_at,id);
CREATE TABLE conversation_messages (
    id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL REFERENCES conversations(id),
    run_id TEXT NOT NULL REFERENCES tasks(id),
    role TEXT NOT NULL CHECK(role IN ('user','assistant','system','tool')),
    content TEXT NOT NULL,
    status TEXT NOT NULL,
    parent_message_id TEXT REFERENCES conversation_messages(id),
    position INTEGER NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    last_event_seq INTEGER NOT NULL DEFAULT 0,
    metadata_json TEXT NOT NULL DEFAULT '{}',
    stream_id TEXT NOT NULL DEFAULT '',
    chunk_index INTEGER NOT NULL DEFAULT -1,
    UNIQUE(conversation_id,position)
);
ALTER TABLE tasks ADD COLUMN conversation_id TEXT REFERENCES conversations(id);
ALTER TABLE tasks ADD COLUMN user_message_id TEXT REFERENCES conversation_messages(id);
ALTER TABLE tasks ADD COLUMN assistant_message_id TEXT REFERENCES conversation_messages(id);
ALTER TABLE tasks ADD COLUMN mode TEXT NOT NULL DEFAULT 'research';
ALTER TABLE tasks ADD COLUMN operation TEXT NOT NULL DEFAULT 'send';
ALTER TABLE tasks ADD COLUMN continue_from_message_id TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN retry_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE tasks ADD COLUMN cancel_requested INTEGER NOT NULL DEFAULT 0;
ALTER TABLE tasks ADD COLUMN started_at TEXT;
ALTER TABLE tasks ADD COLUMN completed_at TEXT;
ALTER TABLE tasks ADD COLUMN heartbeat_at TEXT;
ALTER TABLE tasks ADD COLUMN deadline_at TEXT;
ALTER TABLE tasks ADD COLUMN error_code TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN error_message TEXT NOT NULL DEFAULT '';
ALTER TABLE idempotency_keys ADD COLUMN request_sha256 TEXT;
CREATE UNIQUE INDEX idx_conversation_active_run ON tasks(conversation_id) WHERE conversation_id IS NOT NULL AND status IN ('inbox','queued','planning','running','waiting_approval','paused','verifying');
CREATE INDEX idx_conversation_runs ON tasks(conversation_id,created_at,id);
CREATE INDEX idx_conversation_messages_run ON conversation_messages(run_id);
