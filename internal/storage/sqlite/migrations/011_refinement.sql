CREATE TABLE IF NOT EXISTS harness_bundles (
    hash TEXT PRIMARY KEY,
    base_hash TEXT NOT NULL,
    version INTEGER NOT NULL CHECK(version > 0),
    parent_hash TEXT NOT NULL DEFAULT '',
    spec_json TEXT NOT NULL CHECK(json_valid(spec_json)),
    patch_json TEXT,
    evaluation_json TEXT,
    task_id TEXT REFERENCES tasks(id),
    status TEXT NOT NULL CHECK(status IN ('candidate','evaluating','ready','approved','rejected')),
    active INTEGER NOT NULL DEFAULT 0 CHECK(active IN (0,1)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(base_hash,version)
);
CREATE UNIQUE INDEX IF NOT EXISTS harness_active_bundle ON harness_bundles(base_hash) WHERE active=1;
CREATE INDEX IF NOT EXISTS harness_bundle_task ON harness_bundles(task_id);
