CREATE INDEX IF NOT EXISTS idx_checkpoints_task_event_seq
ON checkpoints(task_id, last_event_seq DESC);
