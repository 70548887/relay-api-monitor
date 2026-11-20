package store

import (
	"context"

	"relaylab/internal/domain"
)

// FingerprintHistory is how many detection rounds the store keeps for each model.
const FingerprintHistory = 30

// insertFingerprintResult stores one detection round and drops the rounds of its model beyond the newest
// FingerprintHistory. The retention period does not apply to rounds: detection runs on demand, so the current
// result of a model can be older than any retention period.
func insertFingerprintResult(ctx context.Context, q queryer, v domain.FingerprintResult) error {
	b, e := data(v)
	if e != nil {
		return e
	}
	if _, e = q.ExecContext(ctx, "INSERT INTO fingerprint_results(id,job_id,model_id,created_at,data) VALUES(?,?,?,?,?)", v.ID, v.JobID, v.ModelID, ts(v.CreatedAt), b); e != nil {
		return e
	}
	_, e = q.ExecContext(ctx, "DELETE FROM fingerprint_results WHERE model_id=? AND id NOT IN (SELECT id FROM fingerprint_results WHERE model_id=? ORDER BY created_at DESC,id DESC LIMIT ?)", v.ModelID, v.ModelID, FingerprintHistory)
	return e
}

// ListFingerprintResults returns the stored detection rounds of one model, newest first.
func (s *Store) ListFingerprintResults(ctx context.Context, modelID string) ([]domain.FingerprintResult, error) {
	return list[domain.FingerprintResult](ctx, s.DB, "SELECT data FROM fingerprint_results WHERE model_id=? ORDER BY created_at DESC,id DESC", modelID)
}

// ListLatestFingerprintResults returns the newest detection rounds of every model, at most perModel of each. The
// rounds of one model are adjacent and newest first.
func (s *Store) ListLatestFingerprintResults(ctx context.Context, perModel int) ([]domain.FingerprintResult, error) {
	if perModel < 1 {
		perModel = 1
	}
	return list[domain.FingerprintResult](ctx, s.DB, "SELECT data FROM (SELECT data,model_id,created_at,id,row_number() OVER(PARTITION BY model_id ORDER BY created_at DESC,id DESC) AS n FROM fingerprint_results) WHERE n<=? ORDER BY model_id,created_at DESC,id DESC", perModel)
}
