package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"relaylab/internal/domain"
)

var ErrConnectionExists = errors.New("store: duplicate user connection")

func userConnection(ctx context.Context, q queryer, userID, id string) (*domain.UserConnection, error) {
	query := "SELECT c.data,c.encrypted_key,c.fingerprint,c.revision,u.username FROM user_connections c JOIN users u ON u.id=c.user_id WHERE c.id=?"
	args := []any{id}
	if userID != "" {
		query += " AND c.user_id=?"
		args = append(args, userID)
	}
	var raw string
	var c domain.UserConnection
	var cipher, fp, user string
	var revision int
	if err := q.QueryRowContext(ctx, query, args...).Scan(&raw, &cipher, &fp, &revision, &user); err != nil {
		return nil, err
	}
	if err := decodePrivate(raw, &c); err != nil {
		return nil, err
	}
	c.EncryptedKey, c.CredentialFingerprint, c.Revision, c.Username = cipher, fp, revision, user
	return &c, nil
}
func (s *Store) GetUserConnection(ctx context.Context, userID, id string) (*domain.UserConnection, error) {
	return userConnection(ctx, s.DB, userID, id)
}
func (s *Store) FindUserConnection(ctx context.Context, userID, fp string) (*domain.UserConnection, error) {
	var id string
	err := s.DB.QueryRowContext(ctx, "SELECT id FROM user_connections WHERE user_id=? AND fingerprint=?", userID, fp).Scan(&id)
	if err != nil {
		return nil, err
	}
	return s.GetUserConnection(ctx, userID, id)
}
func (s *Store) ListUserConnections(ctx context.Context, userID string) ([]domain.UserConnection, error) {
	query := "SELECT c.id FROM user_connections c"
	args := []any{}
	if userID != "" {
		query += " WHERE c.user_id=?"
		args = append(args, userID)
	}
	query += " ORDER BY json_extract(c.data,'$.created_at') DESC,c.id"
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	items := []domain.UserConnection{}
	for _, id := range ids {
		c, e := s.GetUserConnection(ctx, userID, id)
		if errors.Is(e, sql.ErrNoRows) {
			continue
		}
		if e != nil {
			return nil, e
		}
		items = append(items, *c)
	}
	return items, nil
}
func (s *Store) SaveUserConnection(ctx context.Context, c domain.UserConnection) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		old, err := userConnection(ctx, tx, c.UserID, c.ID)
		if errors.Is(err, sql.ErrNoRows) {
			if c.Revision != 1 {
				return ErrConflict
			}
			var count int
			if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM user_connections WHERE user_id=?", c.UserID).Scan(&count); err != nil {
				return err
			}
			if count >= 100 {
				return ErrConflict
			}
			b, e := data(c)
			if e != nil {
				return e
			}
			_, e = tx.ExecContext(ctx, "INSERT INTO user_connections(id,user_id,encrypted_key,fingerprint,revision,data) VALUES(?,?,?,?,?,?)", c.ID, c.UserID, c.EncryptedKey, c.CredentialFingerprint, c.Revision, b)
			if e != nil && strings.Contains(e.Error(), "UNIQUE") {
				return ErrConnectionExists
			}
			return e
		}
		if err != nil {
			return err
		}
		if old.Revision+1 != c.Revision {
			return ErrConflict
		}
		b, e := data(c)
		if e != nil {
			return e
		}
		result, err := tx.ExecContext(ctx, "UPDATE user_connections SET encrypted_key=?,fingerprint=?,revision=?,data=? WHERE id=? AND user_id=? AND revision=?", c.EncryptedKey, c.CredentialFingerprint, c.Revision, b, c.ID, c.UserID, old.Revision)
		if err != nil && strings.Contains(err.Error(), "UNIQUE") {
			return ErrConnectionExists
		}
		return affected(result, err)
	})
}
func (s *Store) DeleteUserConnection(ctx context.Context, userID, id string) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if _, err := userConnection(ctx, tx, userID, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE online_test_runs SET data=json_set(data,'$.connection_id','') WHERE connection_id=?", id)
		if err != nil {
			return err
		}
		return affected(tx.ExecContext(ctx, "DELETE FROM user_connections WHERE id=? AND user_id=?", id, userID))
	})
}
func (s *Store) SetUserConnectionDisabled(ctx context.Context, id string, disabled bool) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		c, e := userConnection(ctx, tx, "", id)
		if e != nil {
			return e
		}
		c.Disabled = disabled
		c.Revision++
		c.UpdatedAt = nowUTC()
		b, e := data(c)
		if e != nil {
			return e
		}
		return affected(tx.ExecContext(ctx, "UPDATE user_connections SET revision=?,data=? WHERE id=?", c.Revision, b, id))
	})
}
func (s *Store) UpdateUserConnectionDiscovery(ctx context.Context, userID, id string, revision, count int, message string) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		c, e := userConnection(ctx, tx, userID, id)
		if e != nil {
			return e
		}
		if c.Revision != revision {
			return ErrConflict
		}
		now := nowUTC()
		c.LastModelsAt = &now
		c.LastModelsCount = count
		c.LastModelsError = message
		b, e := data(c)
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, "UPDATE user_connections SET data=? WHERE id=? AND revision=?", b, id, revision)
		return e
	})
}

func onlineRun(ctx context.Context, q queryer, userID, id string, summary bool) (*domain.OnlineTestRun, error) {
	field := "r.data"
	if summary {
		field = "json_remove(r.data,'$.prompt','$.text','$.code')"
	}
	query := "SELECT " + field + ",COALESCE(r.connection_id,''),r.request_hash,r.process_id,u.username FROM online_test_runs r JOIN users u ON u.id=r.user_id WHERE r.id=?"
	args := []any{id}
	if userID != "" {
		query += " AND r.user_id=?"
		args = append(args, userID)
	}
	var raw, cid, hash, proc, user string
	if err := q.QueryRowContext(ctx, query, args...).Scan(&raw, &cid, &hash, &proc, &user); err != nil {
		return nil, err
	}
	var r domain.OnlineTestRun
	if err := decodePrivate(raw, &r); err != nil {
		return nil, err
	}
	r.ConnectionID, r.RequestHash, r.ProcessID, r.Username = cid, hash, proc, user
	return &r, nil
}
func (s *Store) GetOnlineTestRun(ctx context.Context, userID, id string) (*domain.OnlineTestRun, error) {
	return onlineRun(ctx, s.DB, userID, id, false)
}
func (s *Store) CreateOnlineTestRun(ctx context.Context, r domain.OnlineTestRun) (saved *domain.OnlineTestRun, created bool, err error) {
	err = s.transaction(ctx, func(tx *sql.Tx) error {
		var id string
		e := tx.QueryRowContext(ctx, "SELECT id FROM online_test_runs WHERE user_id=? AND client_request_id=?", r.UserID, r.ClientRequestID).Scan(&id)
		if e == nil {
			saved, e = onlineRun(ctx, tx, r.UserID, id, false)
			if e != nil {
				return e
			}
			if saved.RequestHash != r.RequestHash {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		connection, e := userConnection(ctx, tx, r.UserID, r.ConnectionID)
		if e != nil {
			return e
		}
		if connection.Disabled || connection.Revision != r.ConnectionRevision {
			return ErrConflict
		}
		b, e := data(r)
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, "INSERT INTO online_test_runs(id,user_id,connection_id,client_request_id,request_hash,state,process_id,created_at,data) VALUES(?,?,?,?,?,?,?,?,?)", r.ID, r.UserID, r.ConnectionID, r.ClientRequestID, r.RequestHash, r.State, r.ProcessID, ts(r.CreatedAt), b)
		if e == nil {
			saved = &r
			created = true
		}
		return e
	})
	return
}
func (s *Store) UpdateOnlineTestRun(ctx context.Context, r domain.OnlineTestRun) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		current, e := onlineRun(ctx, tx, r.UserID, r.ID, true)
		if e != nil {
			return e
		}
		if current.State != "running" && current.State != "created" {
			return ErrConflict
		}
		if current.State == "created" && r.State == "running" {
			connection, e := userConnection(ctx, tx, r.UserID, current.ConnectionID)
			if e != nil {
				return e
			}
			if connection.Disabled || connection.Revision != r.ConnectionRevision {
				return ErrConflict
			}
		}
		r.ConnectionID = current.ConnectionID
		b, e := data(r)
		if e != nil {
			return e
		}
		return affected(tx.ExecContext(ctx, "UPDATE online_test_runs SET state=?,finished_at=?,data=? WHERE id=? AND user_id=? AND state IN ('created','running')", r.State, ptrTS(r.FinishedAt), b, r.ID, r.UserID))
	})
}
func (s *Store) ListOnlineTestRuns(ctx context.Context, f domain.OnlineTestFilter) (domain.OnlineTestPage, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PageSize < 1 {
		f.PageSize = 20
	}
	if f.PageSize > 100 {
		f.PageSize = 100
	}
	out := domain.OnlineTestPage{Items: []domain.OnlineTestRun{}, Page: f.Page, PageSize: f.PageSize}
	where := " WHERE 1=1"
	args := []any{}
	add := func(column, value string) {
		if value != "" {
			where += " AND " + column + "=?"
			args = append(args, value)
		}
	}
	add("user_id", f.UserID)
	add("connection_id", f.ConnectionID)
	add("state", f.State)
	add("json_extract(data,'$.model')", f.Model)
	if f.TemplateID == "plain" {
		f.TemplateID = ""
		where += " AND json_extract(data,'$.template_id')=''"
	} else {
		add("json_extract(data,'$.template_id')", f.TemplateID)
	}
	if f.From != "" {
		where += " AND created_at>=?"
		args = append(args, f.From)
	}
	if f.To != "" {
		where += " AND created_at<=?"
		args = append(args, f.To)
	}
	if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM online_test_runs"+where, args...).Scan(&out.Total); err != nil {
		return out, err
	}
	values := append(append([]any{}, args...), f.PageSize, (f.Page-1)*f.PageSize)
	rows, err := s.DB.QueryContext(ctx, "SELECT id FROM online_test_runs"+where+" ORDER BY created_at DESC,id DESC LIMIT ? OFFSET ?", values...)
	if err != nil {
		return out, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return out, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	for _, id := range ids {
		r, e := onlineRun(ctx, s.DB, f.UserID, id, true)
		if errors.Is(e, sql.ErrNoRows) {
			continue
		}
		if e != nil {
			return out, e
		}
		out.Items = append(out.Items, *r)
	}
	return out, nil
}
func (s *Store) DeleteOnlineTestRuns(ctx context.Context, userID string, ids []string) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		for _, id := range ids {
			r, e := onlineRun(ctx, tx, userID, id, true)
			if e != nil {
				return e
			}
			if r.State == "created" || r.State == "running" {
				return ErrConflict
			}
		}
		for _, id := range ids {
			if _, e := tx.ExecContext(ctx, "DELETE FROM online_test_runs WHERE id=?", id); e != nil {
				return e
			}
		}
		return nil
	})
}
func (s *Store) CleanupOnlineTestRuns(ctx context.Context, now time.Time) error {
	settings, err := s.GetSettings(ctx)
	if err != nil {
		return err
	}
	days := settings.OnlineTestRetentionDays
	if days < 1 || days > 365 {
		days = 30
	}
	for {
		var count int64
		err := s.transaction(ctx, func(tx *sql.Tx) error {
			result, e := tx.ExecContext(ctx, "DELETE FROM online_test_runs WHERE id IN (SELECT id FROM online_test_runs WHERE state NOT IN ('created','running') AND created_at<? ORDER BY created_at LIMIT 500)", ts(now.AddDate(0, 0, -days)))
			if e != nil {
				return e
			}
			count, e = result.RowsAffected()
			return e
		})
		if err != nil {
			return err
		}
		if count < 500 {
			return nil
		}
	}
}
func (s *Store) RecoverOnlineTestRuns(ctx context.Context, process string, now time.Time) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE online_test_runs SET state='interrupted',finished_at=?,data=json_set(data,'$.state','interrupted','$.finished_at',?,'$.error_code','process_interrupted','$.error_message','服务进程中断，上游结果未知；不会自动重新调用') WHERE state IN ('created','running') AND process_id<>?", ts(now), ts(now), process)
		return err
	})
}

func decodePrivate(raw string, dst any) error { return json.Unmarshal([]byte(raw), dst) }

// The server holds its admission/registry mutex while taking this snapshot.
// Missing live executors in this process are never retried upstream.
func (s *Store) RecoverInactiveOnlineTestRuns(ctx context.Context, process string, activeIDs []string, now time.Time) error {
	query := `UPDATE online_test_runs SET state='interrupted',finished_at=?,data=json_set(data,'$.state','interrupted','$.finished_at',?,'$.error_code','history_save_failed','$.error_message','请求处理已结束，最终结果未能确认保存；不会自动重新调用','$.preview_status','none','$.code','') WHERE state IN ('created','running') AND process_id=?`
	args := []any{ts(now), ts(now), process}
	if len(activeIDs) > 0 {
		query += " AND id NOT IN (" + strings.TrimSuffix(strings.Repeat("?,", len(activeIDs)), ",") + ")"
		for _, id := range activeIDs {
			args = append(args, id)
		}
	}
	return s.transaction(ctx, func(tx *sql.Tx) error { _, e := tx.ExecContext(ctx, query, args...); return e })
}
