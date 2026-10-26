package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"relaylab/internal/domain"
)

var ErrSelfAdmin = fmt.Errorf("%w: cannot change current administrator", ErrConflict)
var ErrLastActiveAdmin = fmt.Errorf("%w: at least one active administrator is required", ErrConflict)

// migrateAdminAccounts also accepts a migrated database whose legacy version
// metadata was reset. Every DDL statement remains inside the outer transaction.
func migrateAdminAccounts(ctx context.Context, tx *sql.Tx) error {
	var version int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version >= 2 {
		return nil
	}
	rows, err := tx.QueryContext(ctx, "PRAGMA table_info(admins)")
	if err != nil {
		return err
	}
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, kind string
		var defaultValue any
		if err = rows.Scan(&cid, &name, &kind, &notnull, &defaultValue, &pk); err != nil {
			rows.Close()
			return err
		}
		columns[name] = true
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, statement := range strings.Split(adminAccountsMigration, ";") {
		statement = strings.TrimSpace(statement)
		if statement == "" || (columns["disabled"] && strings.Contains(statement, "ADD COLUMN disabled")) || (columns["created_at"] && strings.Contains(statement, "ADD COLUMN created_at")) {
			continue
		}
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ListAdmins(ctx context.Context) ([]domain.Admin, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT id,username,disabled,created_at FROM admins ORDER BY username,id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.Admin, 0)
	for rows.Next() {
		var v domain.Admin
		if err = rows.Scan(&v.ID, &v.Username, &v.Disabled, &v.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, v)
	}
	return items, rows.Err()
}

func (s *Store) CreateAdmin(ctx context.Context, username, hash string) (*domain.Admin, error) {
	if username == "" || hash == "" {
		return nil, errors.New("store: username and password hash required")
	}
	u := &domain.Admin{ID: domain.NewID(), Username: username, CreatedAt: ts(nowUTC())}
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM admins WHERE username=?)", username).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return ErrConflict
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO admins(id,username,password_hash,created_at) VALUES(?,?,?,?)", u.ID, u.Username, hash, u.CreatedAt)
		return err
	})
	return u, err
}

// SetAdminDisabled keeps the enabled-admin invariant and session revocation in
// one immediate transaction, including across independent Store handles.
func (s *Store) SetAdminDisabled(ctx context.Context, actorID, id string, disabled bool) (*domain.Admin, error) {
	var result *domain.Admin
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		u, err := admin(ctx, tx, "id", id)
		if err != nil {
			return err
		}
		if disabled && actorID == id {
			return ErrSelfAdmin
		}
		if disabled && !u.Disabled {
			var active int
			if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM admins WHERE disabled=0").Scan(&active); err != nil {
				return err
			}
			if active <= 1 {
				return ErrLastActiveAdmin
			}
		}
		if err = affected(tx.ExecContext(ctx, "UPDATE admins SET disabled=? WHERE id=?", disabled, id)); err != nil {
			return err
		}
		if disabled {
			if _, err = tx.ExecContext(ctx, "DELETE FROM sessions WHERE admin_id=?", id); err != nil {
				return err
			}
		}
		u.Disabled = disabled
		u.PasswordHash = ""
		result = u
		return nil
	})
	return result, err
}

func (s *Store) ResetAdminPassword(ctx context.Context, actorID, id, hash string) error {
	if actorID == id {
		return ErrSelfAdmin
	}
	if hash == "" {
		return errors.New("store: password hash required")
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if _, err := admin(ctx, tx, "id", id); err != nil {
			return err
		}
		if err := affected(tx.ExecContext(ctx, "UPDATE admins SET password_hash=? WHERE id=?", hash, id)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE admin_id=?", id)
		return err
	})
}
