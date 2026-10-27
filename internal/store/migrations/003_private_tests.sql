CREATE TABLE IF NOT EXISTS user_connections (
 id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id), encrypted_key TEXT NOT NULL,
 fingerprint TEXT NOT NULL, revision INTEGER NOT NULL, data TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS user_connections_owner ON user_connections(user_id);
CREATE UNIQUE INDEX IF NOT EXISTS user_connections_dedup ON user_connections(user_id,fingerprint);
CREATE TABLE IF NOT EXISTS online_test_runs (
 id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id), connection_id TEXT REFERENCES user_connections(id) ON DELETE SET NULL,
 client_request_id TEXT NOT NULL, request_hash TEXT NOT NULL, state TEXT NOT NULL, process_id TEXT NOT NULL,
 created_at TEXT NOT NULL, finished_at TEXT, data TEXT NOT NULL, UNIQUE(user_id,client_request_id)
);
CREATE INDEX IF NOT EXISTS online_tests_owner_time ON online_test_runs(user_id,created_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS online_tests_owner_state ON online_test_runs(user_id,state,created_at DESC);
CREATE INDEX IF NOT EXISTS online_tests_time ON online_test_runs(created_at);
