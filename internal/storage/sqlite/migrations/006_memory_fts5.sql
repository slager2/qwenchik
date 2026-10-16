CREATE VIRTUAL TABLE IF NOT EXISTS memory_fts USING fts5(
    memory_id UNINDEXED,
    text
);

CREATE TRIGGER IF NOT EXISTS memories_fts_insert
AFTER INSERT ON memories
BEGIN
    INSERT INTO memory_fts(memory_id, text) VALUES (new.id, new.text);
END;

CREATE TRIGGER IF NOT EXISTS memories_fts_update
AFTER UPDATE ON memories
BEGIN
    DELETE FROM memory_fts WHERE memory_id = old.id;
    INSERT INTO memory_fts(memory_id, text) VALUES (new.id, new.text);
END;

CREATE TRIGGER IF NOT EXISTS memories_fts_delete
AFTER DELETE ON memories
BEGIN
    DELETE FROM memory_fts WHERE memory_id = old.id;
END;

INSERT INTO memory_fts(memory_id, text)
SELECT id, text FROM memories
WHERE id NOT IN (SELECT memory_id FROM memory_fts);
