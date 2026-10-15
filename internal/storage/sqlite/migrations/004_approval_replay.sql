ALTER TABLE approvals ADD COLUMN arguments_sha256 TEXT NOT NULL DEFAULT '';
ALTER TABLE approvals ADD COLUMN consumed_at TEXT;
CREATE INDEX IF NOT EXISTS idx_approvals_task_tool_hash ON approvals(task_id, tool_name, arguments_sha256, status);
CREATE INDEX IF NOT EXISTS idx_approvals_status_expiry ON approvals(status, expires_at);
