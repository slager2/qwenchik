-- The application keeps a process-local, verified checkpoint tail for the
-- ordinary append path. This durable generation makes that optimization
-- fail-closed: every checkpoint mutation and every mutation of an event
-- referenced by a checkpoint invalidates the tail. Unrelated append-only
-- events do not alter the already-verified chain.
CREATE TABLE IF NOT EXISTS checkpoint_chain_state (
    task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
    generation INTEGER NOT NULL DEFAULT 0
);

INSERT OR IGNORE INTO checkpoint_chain_state(task_id, generation)
SELECT id, 0 FROM tasks;

CREATE TRIGGER IF NOT EXISTS checkpoint_chain_event_update
AFTER UPDATE ON events
WHEN EXISTS (
    SELECT 1 FROM checkpoints
    WHERE task_id = OLD.task_id AND last_event_seq = OLD.seq
)
BEGIN
    UPDATE checkpoint_chain_state SET generation = generation + 1 WHERE task_id = OLD.task_id;
END;

CREATE TRIGGER IF NOT EXISTS checkpoint_chain_event_delete
AFTER DELETE ON events
WHEN EXISTS (
    SELECT 1 FROM checkpoints
    WHERE task_id = OLD.task_id AND last_event_seq = OLD.seq
)
BEGIN
    UPDATE checkpoint_chain_state SET generation = generation + 1 WHERE task_id = OLD.task_id;
END;

CREATE TRIGGER IF NOT EXISTS checkpoint_chain_checkpoint_insert
AFTER INSERT ON checkpoints
BEGIN
    INSERT INTO checkpoint_chain_state(task_id, generation) VALUES(NEW.task_id, 1)
    ON CONFLICT(task_id) DO UPDATE SET generation = generation + 1;
END;

CREATE TRIGGER IF NOT EXISTS checkpoint_chain_checkpoint_update
AFTER UPDATE ON checkpoints
BEGIN
    UPDATE checkpoint_chain_state SET generation = generation + 1 WHERE task_id = NEW.task_id;
END;

CREATE TRIGGER IF NOT EXISTS checkpoint_chain_checkpoint_delete
AFTER DELETE ON checkpoints
BEGIN
    UPDATE checkpoint_chain_state SET generation = generation + 1 WHERE task_id = OLD.task_id;
END;
