CREATE INDEX IF NOT EXISTS idx_memories_status_scope_source_updated
    ON memories(status, scope, source_task_id, updated_at);
