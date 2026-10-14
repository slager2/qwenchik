CREATE UNIQUE INDEX IF NOT EXISTS idx_artifacts_task_sha_mime ON artifacts(task_id, sha256, mime_type);
