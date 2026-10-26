package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"relaylab/internal/domain"
)

// OpenExisting opens a migrated database without creating files or running DDL.
// It is suitable for a worker that must wait for the server to initialize.
func OpenExisting(path string) (*Store, error) {
	return openExisting(path, false)
}

// OpenForRestore migrates an existing, structurally valid backup database.
// Callers must use an isolated extracted copy until key and integrity checks
// succeed. Workers continue to use OpenExisting and never run migrations.
func OpenForRestore(path string) (*Store, error) {
	s, err := openExisting(path, true)
	if err != nil {
		return nil, err
	}
	if err = s.Migrate(context.Background()); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func openExisting(path string, legacyBackup bool) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, errors.New("store: database path is a directory")
	}
	db, err := sql.Open("sqlite", sqliteFileURI(abs)+"?mode=rw&_txlock=immediate&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{DB: db}
	if err = checkSchemaVersion(context.Background(), db); err != nil {
		db.Close()
		return nil, err
	}
	validate := validateSchema
	if legacyBackup {
		validate = validateBaseSchema
	}
	if err = validate(context.Background(), db); err != nil {
		db.Close()
		return nil, err
	}
	var settings string
	if err = db.QueryRow("SELECT data FROM settings WHERE id=1").Scan(&settings); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) HasAdmin(ctx context.Context) (bool, error) {
	var n int
	e := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM admins").Scan(&n)
	return n > 0, e
}
func (s *Store) ReleaseWorkerLease(ctx context.Context, owner string) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, "DELETE FROM worker_lease WHERE id=1 AND owner=?", owner)
		return e
	})
}
func (s *Store) ListProbeResults(ctx context.Context, modelID string, since time.Time) ([]domain.ProbeResult, error) {
	if modelID == "" {
		return list[domain.ProbeResult](ctx, s.DB, "SELECT data FROM probe_results WHERE created_at>=? ORDER BY created_at DESC,id", ts(since))
	}
	return list[domain.ProbeResult](ctx, s.DB, "SELECT data FROM probe_results WHERE model_id=? AND created_at>=? ORDER BY created_at DESC,id", modelID, ts(since))
}
func (s *Store) ListAllProbeResults(ctx context.Context, since time.Time) ([]domain.ProbeResult, error) {
	return s.ListProbeResults(ctx, "", since)
}
func (s *Store) ListProbeResultsLimit(ctx context.Context, modelID string, since time.Time, limit int) ([]domain.ProbeResult, error) {
	if limit < 1 {
		limit = 1000
	}
	if limit > 5000 {
		limit = 5000
	}
	q := "SELECT data FROM probe_results WHERE created_at>=?"
	args := []any{ts(since)}
	if modelID != "" {
		q += " AND model_id=?"
		args = append(args, modelID)
	}
	args = append(args, limit)
	return list[domain.ProbeResult](ctx, s.DB, q+" ORDER BY created_at DESC,id LIMIT ?", args...)
}
func (s *Store) ListArtifacts(ctx context.Context, modelID string) ([]domain.Artifact, error) {
	if modelID == "" {
		return list[domain.Artifact](ctx, s.DB, "SELECT data FROM artifacts ORDER BY created_at DESC,id")
	}
	return list[domain.Artifact](ctx, s.DB, "SELECT data FROM artifacts WHERE model_id=? ORDER BY created_at DESC,id", modelID)
}

// ListArtifactsSummary excludes generated source and request/response payloads
// so listing previews does not deserialize every retained full-size artifact.
func (s *Store) ListArtifactsSummary(ctx context.Context, modelID string) ([]domain.Artifact, error) {
	q := "SELECT json_remove(data,'$.code','$.raw_response') FROM artifacts"
	var args []any
	if modelID != "" {
		q += " WHERE model_id=?"
		args = append(args, modelID)
	}
	return list[domain.Artifact](ctx, s.DB, q+" ORDER BY created_at DESC,id", args...)
}

// ListChannelArtifactsSummary supports channel history without loading source
// or raw response data, including artifacts written before channel snapshots.
func (s *Store) ListChannelArtifactsSummary(ctx context.Context, channelID, templateID string, limit int) ([]domain.Artifact, error) {
	return s.listChannelArtifactsSummary(ctx, channelID, templateID, limit, false)
}

// ListPublicChannelArtifactsSummary applies catalog visibility before LIMIT so
// hidden or private artifacts cannot displace the eight public history items.
func (s *Store) ListPublicChannelArtifactsSummary(ctx context.Context, channelID, templateID string, limit int) ([]domain.Artifact, error) {
	return s.listChannelArtifactsSummary(ctx, channelID, templateID, limit, true)
}

func (s *Store) listChannelArtifactsSummary(ctx context.Context, channelID, templateID string, limit int, public bool) ([]domain.Artifact, error) {
	if limit < 1 {
		limit = 8
	}
	if limit > 100 {
		limit = 100
	}
	q := "SELECT json_remove(artifacts.data,'$.code','$.raw_response') FROM artifacts JOIN models ON models.id=artifacts.model_id"
	if public {
		q += " JOIN channels ON channels.id=models.channel_id JOIN platforms ON platforms.id=channels.platform_id"
	}
	q += " WHERE 1=1"
	if public {
		q += " AND artifacts.hidden=0 AND " + publicCatalogCondition
	}
	var args []any
	if channelID != "" {
		q += " AND models.channel_id=?"
		args = append(args, channelID)
	}
	if templateID != "" {
		q += " AND artifacts.template_id=?"
		args = append(args, templateID)
	}
	args = append(args, limit)
	return list[domain.Artifact](ctx, s.DB, q+" ORDER BY artifacts.created_at DESC,artifacts.id DESC LIMIT ?", args...)
}
func (s *Store) GetArtifact(ctx context.Context, id string) (*domain.Artifact, error) {
	return load[domain.Artifact](ctx, s.DB, "SELECT data FROM artifacts WHERE id=?", id)
}
func (s *Store) SetArtifactHidden(ctx context.Context, id string, hidden bool) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		v, e := load[domain.Artifact](ctx, tx, "SELECT data FROM artifacts WHERE id=?", id)
		if e != nil {
			return e
		}
		v.Hidden = hidden
		b, e := data(v)
		if e != nil {
			return e
		}
		return affected(tx.ExecContext(ctx, "UPDATE artifacts SET hidden=?,data=? WHERE id=?", hidden, b, id))
	})
}
func (s *Store) DeleteArtifact(ctx context.Context, id string) error {
	return s.transaction(ctx, func(tx *sql.Tx) error { return affected(tx.ExecContext(ctx, "DELETE FROM artifacts WHERE id=?", id)) })
}
func trimArtifacts(ctx context.Context, q queryer, modelID, templateID string) error {
	cfg, e := load[domain.Settings](ctx, q, "SELECT data FROM settings WHERE id=1")
	if e != nil {
		return e
	}
	if normalizedSettings(*cfg).ArtifactHistoryScope == "model" {
		_, e = q.ExecContext(ctx, "DELETE FROM artifacts WHERE model_id=? AND template_id=? AND id NOT IN (SELECT id FROM artifacts WHERE model_id=? AND template_id=? ORDER BY created_at DESC,id DESC LIMIT 8)", modelID, templateID, modelID, templateID)
		return e
	}
	var channelID string
	if e = q.QueryRowContext(ctx, "SELECT channel_id FROM models WHERE id=?", modelID).Scan(&channelID); e != nil {
		return e
	}
	_, e = q.ExecContext(ctx, "DELETE FROM artifacts WHERE template_id=? AND model_id IN (SELECT id FROM models WHERE channel_id=?) AND id NOT IN (SELECT artifacts.id FROM artifacts JOIN models ON models.id=artifacts.model_id WHERE models.channel_id=? AND artifacts.template_id=? ORDER BY artifacts.created_at DESC,artifacts.id DESC LIMIT 8)", templateID, channelID, channelID, templateID)
	return e
}
func (s *Store) Cleanup(ctx context.Context, now time.Time) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		cfg, e := load[domain.Settings](ctx, tx, "SELECT data FROM settings WHERE id=1")
		if e != nil {
			return e
		}
		days := cfg.RetentionDays
		if days < 1 {
			days = 30
		}
		cutoff := ts(now.Add(-time.Duration(days) * 24 * time.Hour))
		for _, q := range []string{"DELETE FROM probe_results WHERE created_at<?", "DELETE FROM jobs WHERE state IN ('succeeded','failed','canceled','uncertain') AND created_at<?", "DELETE FROM audit_events WHERE created_at<?"} {
			if _, e = tx.ExecContext(ctx, q, cutoff); e != nil {
				return e
			}
		}
		if _, e = tx.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at<=?", ts(now)); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "DELETE FROM user_sessions WHERE expires_at<=?", ts(now)); e != nil {
			return e
		}
		partition := "artifacts.model_id"
		if normalizedSettings(*cfg).ArtifactHistoryScope == "group" {
			partition = "models.channel_id"
		}
		_, e = tx.ExecContext(ctx, "DELETE FROM artifacts WHERE id IN (SELECT id FROM (SELECT artifacts.id,row_number() OVER(PARTITION BY "+partition+",artifacts.template_id ORDER BY artifacts.created_at DESC,artifacts.id DESC) AS n FROM artifacts JOIN models ON models.id=artifacts.model_id) WHERE n>8)")
		return e
	})
}
func (s *Store) AddAudit(ctx context.Context, v domain.AuditEvent) error {
	v.ID = nonemptyID(v.ID)
	v.CreatedAt = created(v.CreatedAt)
	b, e := data(v)
	if e != nil {
		return e
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, "INSERT INTO audit_events(id,created_at,data) VALUES(?,?,?)", v.ID, ts(v.CreatedAt), b)
		return e
	})
}
func (s *Store) ListAudit(ctx context.Context, limit ...int) ([]domain.AuditEvent, error) {
	n := 200
	if len(limit) > 0 && limit[0] > 0 {
		n = limit[0]
	}
	return list[domain.AuditEvent](ctx, s.DB, "SELECT data FROM audit_events ORDER BY created_at DESC,id DESC LIMIT ?", n)
}

// Backup takes a consistent SQLite snapshot without copying live WAL files.
// VACUUM INTO refuses existing targets to avoid destroying user backups.
func (s *Store) Backup(ctx context.Context, targetPath string) error {
	if strings.TrimSpace(targetPath) == "" {
		return errors.New("store: backup target is required")
	}
	abs, e := filepath.Abs(targetPath)
	if e != nil {
		return e
	}
	if _, e = os.Stat(abs); e == nil {
		return errors.New("store: backup target already exists")
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(abs), 0700); e != nil {
		return e
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, e = s.DB.ExecContext(ctx, "VACUUM INTO ?", abs)
	return e
}
