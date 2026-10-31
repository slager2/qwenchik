CREATE TABLE spaces (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    archived_at TEXT,
    deleted_at TEXT
);

CREATE INDEX idx_spaces_updated ON spaces(deleted_at, archived_at, updated_at, id);

ALTER TABLE conversations ADD COLUMN space_id TEXT REFERENCES spaces(id) ON DELETE SET NULL;

CREATE INDEX idx_conversations_space_updated
    ON conversations(space_id, deleted_at, updated_at, id);
