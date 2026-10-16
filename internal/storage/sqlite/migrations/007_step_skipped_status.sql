DROP INDEX IF EXISTS idx_task_steps_task_status;

ALTER TABLE task_steps RENAME TO task_steps_before_skipped;

CREATE TABLE task_steps (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    step_id TEXT NOT NULL,
    position INTEGER NOT NULL,
    kind TEXT NOT NULL,
    description TEXT NOT NULL,
    dependencies_json TEXT NOT NULL,
    expected_output TEXT NOT NULL,
    success_criteria_json TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending','running','completed','failed','skipped')),
    attempts INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(task_id, step_id)
);

INSERT INTO task_steps(id,task_id,step_id,position,kind,description,dependencies_json,expected_output,success_criteria_json,status,attempts,created_at,updated_at)
SELECT id,task_id,step_id,position,kind,description,dependencies_json,expected_output,success_criteria_json,status,attempts,created_at,updated_at
FROM task_steps_before_skipped;

DROP TABLE task_steps_before_skipped;

CREATE INDEX idx_task_steps_task_status ON task_steps(task_id, status, position);
