CREATE TABLE IF NOT EXISTS backup_storages (
 id TEXT PRIMARY KEY, revision INTEGER NOT NULL UNIQUE, encrypted_credentials TEXT NOT NULL, data TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS backup_policies (
 id INTEGER PRIMARY KEY CHECK(id=1), data TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS backup_runs (
 id TEXT PRIMARY KEY, request_id TEXT NOT NULL UNIQUE, state TEXT NOT NULL,
 scheduled_for TEXT UNIQUE, created_at TEXT NOT NULL, data TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS backup_runs_created ON backup_runs(created_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS backup_runs_state ON backup_runs(state);
CREATE TABLE IF NOT EXISTS backup_copies (
 run_id TEXT NOT NULL REFERENCES backup_runs(id) ON DELETE CASCADE,
 location TEXT NOT NULL, storage_id TEXT REFERENCES backup_storages(id), state TEXT NOT NULL,
 path TEXT NOT NULL, sha256 TEXT NOT NULL, size_bytes INTEGER NOT NULL,
 PRIMARY KEY(run_id,location)
);
CREATE TABLE IF NOT EXISTS backup_archive_slots (
 target_date TEXT PRIMARY KEY, run_id TEXT NOT NULL, permanent INTEGER NOT NULL,
 created_at TEXT NOT NULL
);
