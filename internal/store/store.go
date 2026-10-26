package store

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
	"relaylab/internal/domain"
)

// ErrConflict identifies a duplicate active job, stale lease, or invalid transition.
var ErrConflict = errors.New("store: conflicting state or stale lease")

var ErrConfigurationChanged = fmt.Errorf("%w: job configuration changed", ErrConflict)

const schemaVersion = 2

const timeFormat = "2006-01-02T15:04:05.000000000Z"
const WorkerLeaseDuration = 45 * time.Second

const publicCatalogCondition = "COALESCE(json_extract(models.data,'$.public'),0)=1 AND COALESCE(json_extract(models.data,'$.archived'),0)=0 AND COALESCE(json_extract(channels.data,'$.public'),0)=1 AND COALESCE(json_extract(channels.data,'$.archived'),0)=0 AND COALESCE(json_extract(platforms.data,'$.public'),0)=1 AND COALESCE(json_extract(platforms.data,'$.archived'),0)=0"

// Store uses one SQLite connection. Immediate write transactions also serialize
// read/modify/write operations against other Store handles and processes.
type Store struct {
	DB      *sql.DB
	writeMu sync.Mutex
}

//go:embed migrations/001_init.sql
var migration string

//go:embed migrations/002_admin_accounts.sql
var adminAccountsMigration string

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("store: database path is required")
	}
	var dsn string
	if path == ":memory:" {
		dsn = "file:" + domain.NewID() + "?mode=memory&cache=shared"
	} else {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		if err = os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
			return nil, err
		}
		dsn = sqliteFileURI(abs)
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	dsn += sep + "_txlock=immediate&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{DB: db}
	if err = s.Migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error { return s.DB.Close() }
func sqliteFileURI(path string) string {
	p := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" && !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}
func ts(t time.Time) string { return t.UTC().Format(timeFormat) }
func nowUTC() time.Time     { return time.Now().UTC() }
func ptrTS(t *time.Time) any {
	if t == nil {
		return nil
	}
	return ts(*t)
}
func data(v any) (string, error) { b, e := json.Marshal(v); return string(b), e }
func nonemptyID(id string) string {
	if id == "" {
		return domain.NewID()
	}
	return id
}
func created(t time.Time) time.Time {
	if t.IsZero() {
		return nowUTC()
	}
	return t.UTC()
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func load[T any](ctx context.Context, q queryer, stmt string, args ...any) (*T, error) {
	var raw string
	if err := q.QueryRowContext(ctx, stmt, args...).Scan(&raw); err != nil {
		return nil, err
	}
	var v T
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil, err
	}
	return &v, nil
}
func list[T any](ctx context.Context, q queryer, stmt string, args ...any) ([]T, error) {
	rows, err := q.QueryContext(ctx, stmt, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	v := make([]T, 0)
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var item T
		if err = json.Unmarshal([]byte(raw), &item); err != nil {
			return nil, err
		}
		v = append(v, item)
	}
	return v, rows.Err()
}
func affected(r sql.Result, e error) error {
	if e != nil {
		return e
	}
	n, e := r.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}
func (s *Store) transaction(ctx context.Context, fn func(*sql.Tx) error) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = fn(tx); e != nil {
		return e
	}
	return tx.Commit()
}

func (s *Store) Migrate(ctx context.Context) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if err := checkSchemaVersion(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migration); err != nil {
			return err
		}
		if err := migrateAdminAccounts(ctx, tx); err != nil {
			return err
		}
		settings, e := data(domain.DefaultSettings())
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "INSERT OR IGNORE INTO settings(id,data) VALUES(1,?)", settings); e != nil {
			return e
		}
		for _, t := range []domain.PromptTemplate{{ID: "cn_html", Name: "中文 HTML 动画", Kind: "html", Prompt: domain.ChinesePrompt, Version: 1}, {ID: "en_svg", Name: "English SVG animation", Kind: "svg", Prompt: domain.EnglishPrompt, Version: 1}} {
			b, e := data(t)
			if e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, "INSERT OR IGNORE INTO templates(id,data) VALUES(?,?)", t.ID, b); e != nil {
				return e
			}
		}
		if err := validateSchema(ctx, tx); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "PRAGMA user_version=2")
		return err
	})
}

func checkSchemaVersion(ctx context.Context, q queryer) error {
	var version int
	if err := q.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version < 0 || version > schemaVersion {
		return fmt.Errorf("store: unsupported database schema version %d (supported %d)", version, schemaVersion)
	}
	return nil
}

func validateSchema(ctx context.Context, q queryer) error {
	if err := validateBaseSchema(ctx, q); err != nil {
		return err
	}
	return validateSchemaColumns(ctx, q, []string{
		"id,username,password_hash,disabled,created_at FROM admins",
		"id,username,password_hash,disabled,created_at FROM users", "token_hash,user_id,csrf_token,expires_at FROM user_sessions",
	})
}

// validateBaseSchema checks the complete schema supported by legacy backups
// before a restore is allowed to create or migrate any tables.
func validateBaseSchema(ctx context.Context, q queryer) error {
	return validateSchemaColumns(ctx, q, []string{
		"id,data FROM platforms", "id,platform_id,encrypted_key,revision,data FROM channels",
		"id,channel_id,upstream_id,data FROM models", "id,data FROM settings", "id,data FROM templates",
		"id,username,password_hash FROM admins", "token_hash,admin_id,csrf_token,expires_at FROM sessions",
		"id,model_id,kind,template_id,interval_seconds,next_run_at,enabled,data FROM schedules",
		"id,schedule_id,model_id,channel_id,kind,state,owner,lease_token,lease_until,deadline,scheduled_at,created_at,finished_at,cancel_requested,data FROM jobs",
		"id,job_id,model_id,created_at,data FROM probe_results", "id,job_id,model_id,template_id,created_at,hidden,data FROM artifacts",
		"id,created_at,data FROM audit_events", "id,owner,heartbeat_at,lease_until FROM worker_lease",
	})
}

func validateSchemaColumns(ctx context.Context, q queryer, tables []string) error {
	for _, columns := range tables {
		rows, err := q.QueryContext(ctx, "SELECT "+columns+" LIMIT 0")
		if err != nil {
			return fmt.Errorf("store: database migration required: %w", err)
		}
		if err = rows.Close(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ListPlatforms(ctx context.Context) ([]domain.Platform, error) {
	return list[domain.Platform](ctx, s.DB, "SELECT data FROM platforms ORDER BY json_extract(data,'$.sort'),json_extract(data,'$.name'),id")
}
func (s *Store) GetPlatform(ctx context.Context, id string) (*domain.Platform, error) {
	return load[domain.Platform](ctx, s.DB, "SELECT data FROM platforms WHERE id=?", id)
}
func (s *Store) SavePlatform(ctx context.Context, v domain.Platform) error {
	v.ID = nonemptyID(v.ID)
	v.CreatedAt = created(v.CreatedAt)
	b, e := data(v)
	if e != nil {
		return e
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if _, e := tx.ExecContext(ctx, "INSERT INTO platforms(id,data) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data", v.ID, b); e != nil {
			return e
		}
		models, e := list[domain.Model](ctx, tx, "SELECT models.data FROM models JOIN channels ON channels.id=models.channel_id WHERE channels.platform_id=?", v.ID)
		if e != nil {
			return e
		}
		for _, m := range models {
			if e = reconcileSchedules(ctx, tx, m); e != nil {
				return e
			}
		}
		return nil
	})
}

func (s *Store) ListChannels(ctx context.Context, platformID ...string) ([]domain.Channel, error) {
	q := "SELECT data,encrypted_key FROM channels"
	args := []any{}
	if len(platformID) > 0 && platformID[0] != "" {
		q += " WHERE platform_id=?"
		args = append(args, platformID[0])
	}
	q += " ORDER BY json_extract(data,'$.sort'),json_extract(data,'$.name'),id"
	rows, e := s.DB.QueryContext(ctx, q, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := make([]domain.Channel, 0)
	for rows.Next() {
		var v domain.Channel
		var b, k string
		if e = rows.Scan(&b, &k); e != nil {
			return nil, e
		}
		if e = json.Unmarshal([]byte(b), &v); e != nil {
			return nil, e
		}
		v.EncryptedKey = k
		out = append(out, v)
	}
	return out, rows.Err()
}
func channel(ctx context.Context, q queryer, id string) (*domain.Channel, error) {
	var b, k string
	if e := q.QueryRowContext(ctx, "SELECT data,encrypted_key FROM channels WHERE id=?", id).Scan(&b, &k); e != nil {
		return nil, e
	}
	var v domain.Channel
	if e := json.Unmarshal([]byte(b), &v); e != nil {
		return nil, e
	}
	v.EncryptedKey = k
	return &v, nil
}
func (s *Store) GetChannel(ctx context.Context, id string) (*domain.Channel, error) {
	return channel(ctx, s.DB, id)
}
func saveChannel(ctx context.Context, q queryer, v domain.Channel) error {
	b, e := data(v)
	if e != nil {
		return e
	}
	_, e = q.ExecContext(ctx, "INSERT INTO channels(id,platform_id,encrypted_key,revision,data) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET platform_id=excluded.platform_id,encrypted_key=excluded.encrypted_key,revision=excluded.revision,data=excluded.data", v.ID, v.PlatformID, v.EncryptedKey, v.Revision, b)
	return e
}
func (s *Store) SaveChannel(ctx context.Context, v domain.Channel) error {
	return s.transaction(ctx, func(tx *sql.Tx) error { _, err := saveChannelConfiguration(ctx, tx, v); return err })
}

func saveChannelConfiguration(ctx context.Context, tx queryer, v domain.Channel) (domain.Channel, error) {
	v.ID = nonemptyID(v.ID)
	v.CreatedAt = created(v.CreatedAt)
	old, e := channel(ctx, tx, v.ID)
	if e == nil {
		v.Revision = old.Revision + 1
		// Group scheduling is edited by its dedicated atomic operation. A
		// stale general configuration form must not overwrite a newer plan.
		v.IntelligenceSchedule = old.IntelligenceSchedule
		if v.EncryptedKey == "" {
			v.EncryptedKey = old.EncryptedKey
		}
		if v.BaseURL != old.BaseURL || v.EncryptedKey != old.EncryptedKey || v.GroupProvider != old.GroupProvider || v.GroupPath != old.GroupPath || v.GroupField != old.GroupField {
			clearGroupVerification(&v)
		} else {
			// Verification is server-owned. A harmless configuration edit may
			// carry it forward, but supplied client fields can never assert it.
			v.VerifiedGroup = old.VerifiedGroup
			v.GroupVerificationStatus = old.GroupVerificationStatus
			v.GroupVerifiedAt = old.GroupVerifiedAt
			v.GroupVerificationError = old.GroupVerificationError
			v.GroupVerificationRevision = old.GroupVerificationRevision
			if old.GroupVerificationRevision == old.Revision {
				v.GroupVerificationRevision = v.Revision
			}
		}
		if v.BaseURL != old.BaseURL || v.EncryptedKey != old.EncryptedKey || v.DiscoveryProtocol != old.DiscoveryProtocol || v.ModelsPath != old.ModelsPath || v.AnthropicVersion != old.AnthropicVersion {
			v.LastSyncAt = nil
			v.SyncError = ""
		}
		v.CreatedAt = old.CreatedAt
	} else if !errors.Is(e, sql.ErrNoRows) {
		return v, e
	} else {
		v.Revision = 1
		clearGroupVerification(&v)
	}
	return v, saveChannel(ctx, tx, v)
}

func clearGroupVerification(c *domain.Channel) {
	c.VerifiedGroup = ""
	c.GroupVerificationStatus = ""
	c.GroupVerifiedAt = nil
	c.GroupVerificationError = ""
	c.GroupVerificationRevision = 0
}

// SetGroupVerification saves trusted verification health without changing the
// channel configuration revision or its separately stored encrypted key.
func (s *Store) SetGroupVerification(ctx context.Context, id string, expectedRevision int, status, group, provider, message string, checkedAt time.Time) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		c, err := channel(ctx, tx, id)
		if err != nil {
			return err
		}
		if c.Revision != expectedRevision || provider != c.GroupProvider {
			return ErrConflict
		}
		c.GroupVerificationStatus = strings.TrimSpace(status)
		c.VerifiedGroup = ""
		if c.GroupVerificationStatus == "verified" {
			c.VerifiedGroup = strings.TrimSpace(group)
			if c.VerifiedGroup == "" {
				return errors.New("store: verified group is required")
			}
		}
		n := created(checkedAt)
		c.GroupVerifiedAt = &n
		c.GroupVerificationRevision = expectedRevision
		c.GroupVerificationError = message
		return saveChannel(ctx, tx, *c)
	})
}
func (s *Store) ListModels(ctx context.Context, channelID ...string) ([]domain.Model, error) {
	q := "SELECT data FROM models"
	var args []any
	if len(channelID) > 0 && channelID[0] != "" {
		q += " WHERE channel_id=?"
		args = append(args, channelID[0])
	}
	return list[domain.Model](ctx, s.DB, q+" ORDER BY json_extract(data,'$.sort'),json_extract(data,'$.display_name'),id", args...)
}
func (s *Store) GetModel(ctx context.Context, id string) (*domain.Model, error) {
	return load[domain.Model](ctx, s.DB, "SELECT data FROM models WHERE id=?", id)
}
func saveModel(ctx context.Context, q queryer, v domain.Model) error {
	b, e := data(v)
	if e != nil {
		return e
	}
	_, e = q.ExecContext(ctx, "INSERT INTO models(id,channel_id,upstream_id,data) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET channel_id=excluded.channel_id,upstream_id=excluded.upstream_id,data=excluded.data", v.ID, v.ChannelID, v.UpstreamID, b)
	return e
}
func (s *Store) SaveModel(ctx context.Context, v domain.Model) error {
	v.ID = nonemptyID(v.ID)
	v.CreatedAt = created(v.CreatedAt)
	return s.transaction(ctx, func(tx *sql.Tx) error { return saveModel(ctx, tx, v) })
}
func (s *Store) SyncModels(ctx context.Context, channelID string, upstreamIDs []string, expectedRevision ...int) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		c, e := channel(ctx, tx, channelID)
		if e != nil {
			return e
		}
		if len(expectedRevision) > 0 && c.Revision != expectedRevision[0] {
			return ErrConflict
		}
		models, e := list[domain.Model](ctx, tx, "SELECT data FROM models WHERE channel_id=?", channelID)
		if e != nil {
			return e
		}
		seen := map[string]bool{}
		for _, id := range upstreamIDs {
			id = strings.TrimSpace(id)
			if id != "" {
				seen[id] = true
			}
		}
		for _, m := range models {
			m.Missing = !seen[m.UpstreamID]
			delete(seen, m.UpstreamID)
			if e = saveModel(ctx, tx, m); e != nil {
				return e
			}
		}
		for id := range seen {
			m := domain.Model{ID: domain.NewID(), ChannelID: channelID, UpstreamID: id, DisplayName: id, Source: "sync", CreatedAt: nowUTC()}
			if e = saveModel(ctx, tx, m); e != nil {
				return e
			}
		}
		n := nowUTC()
		c.LastSyncAt = &n
		c.SyncError = ""
		if e = saveChannel(ctx, tx, *c); e != nil {
			return e
		}
		all, e := list[domain.Model](ctx, tx, "SELECT data FROM models WHERE channel_id=?", channelID)
		if e != nil {
			return e
		}
		for _, m := range all {
			if e = reconcileSchedules(ctx, tx, m); e != nil {
				return e
			}
		}
		return nil
	})
}

// SetChannelSyncError updates discovery health without changing config revision.
func (s *Store) SetChannelSyncError(ctx context.Context, id, message string, expectedRevision ...int) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		c, err := channel(ctx, tx, id)
		if err != nil {
			return err
		}
		if len(expectedRevision) > 0 && c.Revision != expectedRevision[0] {
			return ErrConflict
		}
		c.SyncError = message
		return saveChannel(ctx, tx, *c)
	})
}
func (s *Store) GetSettings(ctx context.Context) (domain.Settings, error) {
	v, e := load[domain.Settings](ctx, s.DB, "SELECT data FROM settings WHERE id=1")
	if e != nil {
		return domain.Settings{}, e
	}
	return normalizedSettings(*v), nil
}
func normalizedSettings(v domain.Settings) domain.Settings {
	if v.ArtifactHistoryScope != "model" {
		v.ArtifactHistoryScope = "group"
	}
	return v
}
func (s *Store) SaveSettings(ctx context.Context, v domain.Settings) error {
	v = normalizedSettings(v)
	b, e := data(v)
	if e != nil {
		return e
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, "INSERT INTO settings(id,data) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data", b)
		return e
	})
}
func (s *Store) ListTemplates(ctx context.Context) ([]domain.PromptTemplate, error) {
	return list[domain.PromptTemplate](ctx, s.DB, "SELECT data FROM templates ORDER BY id")
}
func (s *Store) GetTemplate(ctx context.Context, id string) (*domain.PromptTemplate, error) {
	return load[domain.PromptTemplate](ctx, s.DB, "SELECT data FROM templates WHERE id=?", id)
}
func (s *Store) SaveTemplate(ctx context.Context, v domain.PromptTemplate) error {
	v.ID = nonemptyID(v.ID)
	if v.Version < 1 {
		v.Version = 1
	}
	b, e := data(v)
	if e != nil {
		return e
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, "INSERT INTO templates(id,data) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data", v.ID, b)
		return e
	})
}

func (s *Store) EnsureAdmin(ctx context.Context, username, hash string) error {
	if username == "" || hash == "" {
		return errors.New("store: username and password hash required")
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		var n int
		if e := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM admins").Scan(&n); e != nil {
			return e
		}
		if n > 0 {
			return fmt.Errorf("%w: administrator already initialized", ErrConflict)
		}
		_, e := tx.ExecContext(ctx, "INSERT INTO admins(id,username,password_hash,created_at) VALUES(?,?,?,?)", domain.NewID(), username, hash, ts(nowUTC()))
		return e
	})
}
func admin(ctx context.Context, q queryer, where string, arg string) (*domain.Admin, error) {
	var v domain.Admin
	e := q.QueryRowContext(ctx, "SELECT id,username,password_hash,disabled,created_at FROM admins WHERE "+where+"=?", arg).Scan(&v.ID, &v.Username, &v.PasswordHash, &v.Disabled, &v.CreatedAt)
	return &v, e
}
func (s *Store) GetAdminByUsername(ctx context.Context, v string) (*domain.Admin, error) {
	return admin(ctx, s.DB, "username", v)
}
func (s *Store) GetAdminByID(ctx context.Context, v string) (*domain.Admin, error) {
	return admin(ctx, s.DB, "id", v)
}
func (s *Store) UpdatePassword(ctx context.Context, id, hash string, expectedHash ...string) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if len(expectedHash) > 0 {
			current, err := admin(ctx, tx, "id", id)
			if err != nil {
				return err
			}
			if current.Disabled || current.PasswordHash != expectedHash[0] {
				return ErrConflict
			}
		}
		if e := affected(tx.ExecContext(ctx, "UPDATE admins SET password_hash=? WHERE id=?", hash, id)); e != nil {
			return e
		}
		_, e := tx.ExecContext(ctx, "DELETE FROM sessions WHERE admin_id=?", id)
		return e
	})
}
func (s *Store) SaveSession(ctx context.Context, v domain.Session) error {
	return s.saveSession(ctx, v, "")
}

// SaveAuthenticatedSession rejects credentials changed or disabled after the
// login password was verified, in the same transaction that creates a session.
func (s *Store) SaveAuthenticatedSession(ctx context.Context, v domain.Session, passwordHash string) error {
	if passwordHash == "" {
		return ErrConflict
	}
	return s.saveSession(ctx, v, passwordHash)
}

func (s *Store) saveSession(ctx context.Context, v domain.Session, passwordHash string) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		u, e := admin(ctx, tx, "id", v.AdminID)
		if errors.Is(e, sql.ErrNoRows) {
			return ErrConflict
		}
		if e != nil {
			return e
		}
		if u.Disabled || (passwordHash != "" && u.PasswordHash != passwordHash) {
			return ErrConflict
		}
		_, e = tx.ExecContext(ctx, "INSERT INTO sessions(token_hash,admin_id,csrf_token,expires_at) VALUES(?,?,?,?) ON CONFLICT(token_hash) DO UPDATE SET admin_id=excluded.admin_id,csrf_token=excluded.csrf_token,expires_at=excluded.expires_at", v.TokenHash, v.AdminID, v.CSRFToken, ts(v.ExpiresAt))
		return e
	})
}
func (s *Store) GetSession(ctx context.Context, hash string) (*domain.Session, error) {
	var v domain.Session
	var expiry string
	e := s.DB.QueryRowContext(ctx, "SELECT token_hash,admin_id,csrf_token,expires_at FROM sessions WHERE token_hash=? AND expires_at>?", hash, ts(nowUTC())).Scan(&v.TokenHash, &v.AdminID, &v.CSRFToken, &expiry)
	if e != nil {
		return nil, e
	}
	v.ExpiresAt, e = time.Parse(timeFormat, expiry)
	return &v, e
}
func (s *Store) DeleteSession(ctx context.Context, hash string) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash=?", hash)
		return e
	})
}
