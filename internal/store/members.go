package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"relaylab/internal/domain"
)

func member(ctx context.Context, q queryer, where, arg string) (*domain.Member, error) {
	var u domain.Member
	err := q.QueryRowContext(ctx, "SELECT id,username,password_hash,disabled,created_at FROM users WHERE "+where+"=?", arg).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Disabled, &u.CreatedAt)
	return &u, err
}

func (s *Store) GetMemberByUsername(ctx context.Context, username string) (*domain.Member, error) {
	return member(ctx, s.DB, "username", username)
}

func (s *Store) GetMemberByID(ctx context.Context, id string) (*domain.Member, error) {
	return member(ctx, s.DB, "id", id)
}

func (s *Store) ListMembers(ctx context.Context) ([]domain.Member, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT id,username,disabled,created_at FROM users ORDER BY username,id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.Member, 0)
	for rows.Next() {
		var u domain.Member
		if err = rows.Scan(&u.ID, &u.Username, &u.Disabled, &u.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, u)
	}
	return items, rows.Err()
}

func (s *Store) CreateMember(ctx context.Context, username, hash string) (*domain.Member, error) {
	if username == "" || hash == "" {
		return nil, errors.New("store: username and password hash required")
	}
	u := &domain.Member{ID: domain.NewID(), Username: username, CreatedAt: ts(nowUTC())}
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE username=?)", username).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return ErrConflict
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO users(id,username,password_hash,created_at) VALUES(?,?,?,?)", u.ID, u.Username, hash, u.CreatedAt)
		return err
	})
	return u, err
}

func (s *Store) SetMemberDisabled(ctx context.Context, id string, disabled bool) (*domain.Member, error) {
	var result *domain.Member
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		u, err := member(ctx, tx, "id", id)
		if err != nil {
			return err
		}
		if err = affected(tx.ExecContext(ctx, "UPDATE users SET disabled=? WHERE id=?", disabled, id)); err != nil {
			return err
		}
		if disabled {
			if _, err = tx.ExecContext(ctx, "DELETE FROM user_sessions WHERE user_id=?", id); err != nil {
				return err
			}
		}
		u.Disabled, u.PasswordHash = disabled, ""
		result = u
		return nil
	})
	return result, err
}

func (s *Store) UpdateMemberPassword(ctx context.Context, id, hash string, expectedHash ...string) error {
	if hash == "" {
		return errors.New("store: password hash required")
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		current, err := member(ctx, tx, "id", id)
		if err != nil {
			return err
		}
		if len(expectedHash) > 0 && (current.Disabled || current.PasswordHash != expectedHash[0]) {
			return ErrConflict
		}
		if err := affected(tx.ExecContext(ctx, "UPDATE users SET password_hash=? WHERE id=?", hash, id)); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "DELETE FROM user_sessions WHERE user_id=?", id)
		return err
	})
}

func (s *Store) SaveAuthenticatedMemberSession(ctx context.Context, v domain.MemberSession, passwordHash string) error {
	if passwordHash == "" {
		return ErrConflict
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		u, err := member(ctx, tx, "id", v.MemberID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		if u.Disabled || u.PasswordHash != passwordHash {
			return ErrConflict
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO user_sessions(token_hash,user_id,csrf_token,expires_at) VALUES(?,?,?,?) ON CONFLICT(token_hash) DO UPDATE SET user_id=excluded.user_id,csrf_token=excluded.csrf_token,expires_at=excluded.expires_at", v.TokenHash, v.MemberID, v.CSRFToken, ts(v.ExpiresAt))
		return err
	})
}

func (s *Store) GetMemberSession(ctx context.Context, hash string) (*domain.MemberSession, error) {
	var v domain.MemberSession
	var expiry string
	err := s.DB.QueryRowContext(ctx, "SELECT token_hash,user_id,csrf_token,expires_at FROM user_sessions WHERE token_hash=? AND expires_at>?", hash, ts(nowUTC())).Scan(&v.TokenHash, &v.MemberID, &v.CSRFToken, &expiry)
	if err != nil {
		return nil, err
	}
	v.ExpiresAt, err = time.Parse(timeFormat, expiry)
	return &v, err
}

func (s *Store) DeleteMemberSession(ctx context.Context, hash string) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM user_sessions WHERE token_hash=?", hash)
		return err
	})
}
