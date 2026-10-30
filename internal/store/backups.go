package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"relaylab/internal/domain"
	"relaylab/internal/security"
	"time"
)

func (s *Store) BackupPolicy(ctx context.Context) (domain.BackupSchedule, error) {
	var raw string
	err := s.DB.QueryRowContext(ctx, "SELECT data FROM backup_policies WHERE id=1").Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.DefaultBackupSchedule(), nil
	}
	if err != nil {
		return domain.BackupSchedule{}, err
	}
	var p domain.BackupSchedule
	err = json.Unmarshal([]byte(raw), &p)
	return p, err
}
func (s *Store) ValidateBackupStorageKeys(ctx context.Context, vault *security.Vault) error {
	rows, err := s.DB.QueryContext(ctx, "SELECT id,encrypted_credentials FROM backup_storages")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, cipher string
		if err = rows.Scan(&id, &cipher); err != nil {
			return err
		}
		if _, err = vault.DecryptBackupCredentials(id, cipher); err != nil {
			return err
		}
	}
	return rows.Err()
}

// PrepareRestoredData fences restored work before either service can resume.
func (s *Store) PrepareRestoredData(ctx context.Context, now time.Time) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		for _, stmt := range []string{"DELETE FROM sessions", "DELETE FROM user_sessions", "DELETE FROM worker_lease"} {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		n := ts(now)
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET state=CASE WHEN state='dispatching' THEN 'uncertain' ELSE 'canceled' END,owner='',lease_token='',lease_until=NULL,finished_at=?,cancel_requested=1,data=json_set(data,'$.state',CASE WHEN state='dispatching' THEN 'uncertain' ELSE 'canceled' END,'$.owner','','$.lease_until',NULL,'$.finished_at',?,'$.cancel_requested',json('true'),'$.error_code','restored_snapshot','$.error_message','备份恢复后禁止重放旧任务') WHERE state IN ('queued','claimed','dispatching')`, n, n); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE online_test_runs SET state='interrupted',finished_at=?,data=json_set(data,'$.state','interrupted','$.finished_at',?,'$.error_code','restored_snapshot','$.error_message','服务从备份恢复，原请求结果未知') WHERE state IN ('created','running')`, n, n); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE schedules SET next_run_at=strftime('%Y-%m-%dT%H:%M:%f000000Z',datetime(?, '+' || interval_seconds || ' seconds')),data=json_set(data,'$.next_run_at',strftime('%Y-%m-%dT%H:%M:%f000000Z',datetime(?, '+' || interval_seconds || ' seconds')))`, n, n); err != nil {
			return err
		}
		defaultPolicy, _ := data(domain.DefaultBackupSchedule())
		if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO backup_policies(id,data) VALUES(1,?)", defaultPolicy); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE backup_policies SET data=json_set(data,'$.enabled',json('false'),'$.next_run_at',NULL,'$.reconciliation_required',json('true'))`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE backup_runs SET state='interrupted',data=json_set(data,'$.state','interrupted','$.finished_at',?,'$.error_code','restored_snapshot','$.message','恢复后原备份操作已中断') WHERE state NOT IN ('succeeded','failed','interrupted','canceled','skipped','unavailable','deleted')`, n)
		return err
	})
}
func (s *Store) SaveBackupPolicy(ctx context.Context, p domain.BackupSchedule) error {
	raw, err := data(p)
	if err != nil {
		return err
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO backup_policies(id,data) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data", raw)
		return err
	})
}
func (s *Store) SaveBackupStorage(ctx context.Context, v domain.BackupStorage, credentials string) error {
	raw, err := data(v)
	if err != nil {
		return err
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO backup_storages(id,revision,encrypted_credentials,data) VALUES(?,?,?,?)", v.ID, v.Revision, credentials, raw)
		return err
	})
}
func (s *Store) BackupStorage(ctx context.Context, id string) (domain.BackupStorage, string, error) {
	var raw, key string
	var err error
	if id == "" {
		policy, e := s.BackupPolicy(ctx)
		if e != nil {
			return domain.BackupStorage{}, "", e
		}
		if policy.StorageSelectionSet {
			if policy.ActiveStorageID == "" {
				return domain.BackupStorage{}, "", sql.ErrNoRows
			}
			err = s.DB.QueryRowContext(ctx, "SELECT data,encrypted_credentials FROM backup_storages WHERE id=?", policy.ActiveStorageID).Scan(&raw, &key)
		} else {
			err = s.DB.QueryRowContext(ctx, "SELECT data,encrypted_credentials FROM backup_storages ORDER BY revision DESC LIMIT 1").Scan(&raw, &key)
		}
	} else {
		err = s.DB.QueryRowContext(ctx, "SELECT data,encrypted_credentials FROM backup_storages WHERE id=?", id).Scan(&raw, &key)
	}
	if err != nil {
		return domain.BackupStorage{}, "", err
	}
	var v domain.BackupStorage
	err = json.Unmarshal([]byte(raw), &v)
	return v, key, err
}

// SaveBackupStorageAllocated assigns the revision while holding the SQLite
// writer transaction. Restored catalog revisions are display metadata and do
// not choose the active configuration or collide with newly saved settings.
func (s *Store) SaveBackupStorageAllocated(ctx context.Context, v domain.BackupStorage, credentials string, selectActive bool) (domain.BackupStorage, error) {
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(revision),0)+1 FROM backup_storages").Scan(&v.Revision); err != nil {
			return err
		}
		raw, err := data(v)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO backup_storages(id,revision,encrypted_credentials,data) VALUES(?,?,?,?)", v.ID, v.Revision, credentials, raw); err != nil {
			return err
		}
		if !selectActive {
			return nil
		}
		p, err := backupPolicyWithin(ctx, tx)
		if err != nil {
			return err
		}
		p.ActiveStorageID = v.ID
		p.StorageSelectionSet = true
		return saveBackupPolicyWithin(ctx, tx, p)
	})
	return v, err
}

func backupPolicyWithin(ctx context.Context, q queryer) (domain.BackupSchedule, error) {
	var raw string
	err := q.QueryRowContext(ctx, "SELECT data FROM backup_policies WHERE id=1").Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.DefaultBackupSchedule(), nil
	}
	if err != nil {
		return domain.BackupSchedule{}, err
	}
	var p domain.BackupSchedule
	err = json.Unmarshal([]byte(raw), &p)
	return p, err
}
func saveBackupPolicyWithin(ctx context.Context, q queryer, p domain.BackupSchedule) error {
	raw, err := data(p)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, "INSERT INTO backup_policies(id,data) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data", raw)
	return err
}

// FreezeBackupStorageSelection migrates the legacy maximum-revision selection
// before importing old catalogs, including an explicit empty selection.
func (s *Store) FreezeBackupStorageSelection(ctx context.Context) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		p, err := backupPolicyWithin(ctx, tx)
		if err != nil {
			return err
		}
		if p.StorageSelectionSet {
			return nil
		}
		var id string
		err = tx.QueryRowContext(ctx, "SELECT id FROM backup_storages ORDER BY revision DESC LIMIT 1").Scan(&id)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		p.ActiveStorageID = id
		p.StorageSelectionSet = true
		return saveBackupPolicyWithin(ctx, tx, p)
	})
}
func (s *Store) CreateBackupRun(ctx context.Context, v domain.BackupRun) (domain.BackupRun, bool, error) {
	raw, err := data(v)
	if err != nil {
		return v, false, err
	}
	var existing domain.BackupRun
	fresh := false
	err = s.transaction(ctx, func(tx *sql.Tx) error {
		var b string
		err := tx.QueryRowContext(ctx, "SELECT data FROM backup_runs WHERE request_id=?", v.RequestID).Scan(&b)
		if err == nil {
			return json.Unmarshal([]byte(b), &existing)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var slot any
		if v.ScheduledFor != nil {
			slot = ts(*v.ScheduledFor)
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO backup_runs(id,request_id,state,scheduled_for,created_at,data) VALUES(?,?,?,?,?,?)", v.ID, v.RequestID, v.State, slot, ts(v.CreatedAt), raw); err != nil {
			return err
		}
		fresh = true
		existing = v
		return nil
	})
	return existing, fresh, err
}
func (s *Store) SaveBackupRun(ctx context.Context, v domain.BackupRun) error {
	return s.transaction(ctx, func(tx *sql.Tx) error { return saveBackupRun(ctx, tx, v) })
}
func saveBackupRun(ctx context.Context, q queryer, v domain.BackupRun) error {
	raw, err := data(v)
	if err != nil {
		return err
	}
	if err := affected(q.ExecContext(ctx, "UPDATE backup_runs SET state=?,data=? WHERE id=?", v.State, raw, v.ID)); err != nil {
		return err
	}
	for _, location := range []string{"local", "s3"} {
		state, path := v.LocalState, v.LocalPath
		var storage any
		if location == "s3" {
			state, path = v.RemoteState, v.ObjectKey
			if v.StorageID != "" {
				storage = v.StorageID
			}
		}
		if state == "" {
			continue
		}
		if _, err := q.ExecContext(ctx, "INSERT INTO backup_copies(run_id,location,storage_id,state,path,sha256,size_bytes) VALUES(?,?,?,?,?,?,?) ON CONFLICT(run_id,location) DO UPDATE SET state=excluded.state,path=excluded.path,sha256=excluded.sha256,size_bytes=excluded.size_bytes,storage_id=excluded.storage_id", v.ID, location, storage, state, path, v.SHA256, v.SizeBytes); err != nil {
			return err
		}
	}
	return nil
}

// CompleteBackupRun commits the archive ownership and final run flags together.
func (s *Store) CompleteBackupRun(ctx context.Context, v domain.BackupRun, dates []string, retainCount int) (domain.BackupRun, error) {
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		for _, date := range dates {
			if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO backup_archive_slots(target_date,run_id,permanent,created_at) VALUES(?,?,?,?)", date, v.ID, retainCount == 0, ts(nowUTC())); err != nil {
				return err
			}
		}
		owned, permanent, err := backupArchiveProtection(ctx, tx, v.ID)
		if err != nil {
			return err
		}
		v.ArchiveDates = owned
		if len(owned) > 0 && !v.ArchiveReleased {
			v.MonthlyArchive = true
			v.ArchiveRetainCount = retainCount
			if permanent {
				v.ArchiveRetainCount = 0
			}
			v.ExpiresAt = nil
		}
		return saveBackupRun(ctx, tx, v)
	})
	return v, err
}
func backupArchiveProtection(ctx context.Context, q queryer, id string) ([]string, bool, error) {
	rows, err := q.QueryContext(ctx, "SELECT target_date,permanent FROM backup_archive_slots WHERE run_id=? ORDER BY target_date", id)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	dates := []string{}
	permanent := false
	for rows.Next() {
		var date string
		var flag bool
		if err = rows.Scan(&date, &flag); err != nil {
			return nil, false, err
		}
		dates = append(dates, date)
		permanent = permanent || flag
	}
	return dates, permanent, rows.Err()
}
func (s *Store) BackupArchiveProtection(ctx context.Context, id string) ([]string, bool, error) {
	return backupArchiveProtection(ctx, s.DB, id)
}
func (s *Store) UnfinishedBackupRuns(ctx context.Context) ([]domain.BackupRun, error) {
	return list[domain.BackupRun](ctx, s.DB, "SELECT data FROM backup_runs WHERE state NOT IN ('succeeded','failed','interrupted','canceled','skipped','deleted','unavailable') ORDER BY created_at,id")
}
func (s *Store) BackupOverview(ctx context.Context) (*domain.BackupRun, *domain.BackupRun, error) {
	var active, success *domain.BackupRun
	for _, query := range []struct {
		sql    string
		target **domain.BackupRun
	}{{"SELECT id FROM backup_runs WHERE state NOT IN ('succeeded','failed','interrupted','canceled','skipped','deleted','unavailable') ORDER BY created_at LIMIT 1", &active}, {"SELECT id FROM backup_runs WHERE state='succeeded' AND (json_extract(data,'$.local_state')='verified' OR json_extract(data,'$.remote_state')='verified') ORDER BY created_at DESC,id DESC LIMIT 1", &success}} {
		var id string
		err := s.DB.QueryRowContext(ctx, query.sql).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		v, err := s.BackupRun(ctx, id)
		if err != nil {
			return nil, nil, err
		}
		*query.target = &v
	}
	return active, success, nil
}
func (s *Store) BackupRun(ctx context.Context, id string) (domain.BackupRun, error) {
	v, err := load[domain.BackupRun](ctx, s.DB, "SELECT data FROM backup_runs WHERE id=?", id)
	if err != nil {
		return domain.BackupRun{}, err
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT location,path FROM backup_copies WHERE run_id=?", id)
	if err != nil {
		return *v, err
	}
	defer rows.Close()
	for rows.Next() {
		var loc, path string
		if err = rows.Scan(&loc, &path); err != nil {
			return *v, err
		}
		if loc == "local" {
			v.LocalPath = path
		} else {
			v.ObjectKey = path
		}
	}
	return *v, rows.Err()
}
func (s *Store) ListBackupRuns(ctx context.Context, limit, offset int) ([]domain.BackupRun, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	return list[domain.BackupRun](ctx, s.DB, "SELECT data FROM backup_runs ORDER BY created_at DESC,id DESC LIMIT ? OFFSET ?", limit, offset)
}
func (s *Store) DeleteBackupRun(ctx context.Context, id string) error {
	return s.transaction(ctx, func(tx *sql.Tx) error { return affected(tx.ExecContext(ctx, "DELETE FROM backup_runs WHERE id=?", id)) })
}
func (s *Store) ClaimBackupArchiveDates(ctx context.Context, id string, dates []string, permanent bool) ([]string, error) {
	claimed := []string{}
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		for _, date := range dates {
			r, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO backup_archive_slots(target_date,run_id,permanent,created_at) VALUES(?,?,?,?)", date, id, permanent, ts(nowUTC()))
			if err != nil {
				return err
			}
			n, err := r.RowsAffected()
			if err != nil {
				return err
			}
			if n == 1 {
				claimed = append(claimed, date)
			}
		}
		return nil
	})
	return claimed, err
}
