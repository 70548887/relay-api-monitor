CREATE TABLE IF NOT EXISTS fingerprint_results (
  id TEXT PRIMARY KEY,
  job_id TEXT NOT NULL UNIQUE,
  model_id TEXT NOT NULL REFERENCES models(id),
  created_at TEXT NOT NULL,
  data TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS fingerprint_results_model_created ON fingerprint_results(model_id, created_at DESC, id DESC);
