package store

import (
	"context"
	"database/sql"

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

// SaveFingerprintModelSettings changes the fingerprint settings of a model, leaving its other fields as stored, and
// reconciles its schedules.
func (s *Store) SaveFingerprintModelSettings(ctx context.Context, modelID string, v domain.FingerprintModelSettings) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		m, e := load[domain.Model](ctx, tx, "SELECT data FROM models WHERE id=?", modelID)
		if e != nil {
			return e
		}
		m.FingerprintMode, m.FingerprintModel, m.FingerprintQuestions, m.FingerprintScheduled = v.Mode, v.Model, v.Questions, v.Scheduled
		if e = saveModel(ctx, tx, *m); e != nil {
			return e
		}
		return reconcileSchedules(ctx, tx, *m)
	})
}

// ListLatestFingerprintJobs returns the newest fingerprint job of every model that has one. Unlike a stored round,
// the job may still be running, or may have failed without a round.
func (s *Store) ListLatestFingerprintJobs(ctx context.Context) ([]domain.Job, error) {
	// The inner lookup runs once per model on an index over kind and model, so the probe jobs are never scanned.
	return jobs(ctx, s.DB, "WHERE id IN (SELECT (SELECT j.id FROM jobs j WHERE j.model_id=models.id AND j.kind='fingerprint' ORDER BY j.created_at DESC,j.id DESC LIMIT 1) FROM models)")
}
