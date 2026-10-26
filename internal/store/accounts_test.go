package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"relaylab/internal/domain"
)

func TestSchemaOneAccountMigrationPreservesAdminAndSession(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "schema-one.db")
	db, err := sql.Open("sqlite", sqliteFileURI(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(migration); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO admins(id,username,password_hash) VALUES('original','admin','original-hash')"); err != nil {
		t.Fatal(err)
	}
	expiry := ts(nowUTC().Add(time.Hour))
	if _, err = db.Exec("INSERT INTO sessions(token_hash,admin_id,csrf_token,expires_at) VALUES('existing-token','original','existing-csrf',?)", expiry); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("PRAGMA user_version=1"); err != nil {
		t.Fatal(err)
	}
	if old, err := OpenExisting(path); err == nil {
		old.Close()
		t.Fatal("worker accepted an unmigrated account schema")
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	u, err := s.GetAdminByUsername(ctx, "admin")
	if err != nil || u.ID != "original" || u.PasswordHash != "original-hash" || u.Disabled || u.CreatedAt != "" {
		t.Fatalf("migration changed original administrator: %#v %v", u, err)
	}
	ss, err := s.GetSession(ctx, "existing-token")
	if err != nil || ss.AdminID != "original" || ss.CSRFToken != "existing-csrf" || ts(ss.ExpiresAt) != expiry {
		t.Fatalf("migration changed original session: %#v %v", ss, err)
	}
	if err = s.EnsureAdmin(ctx, "replacement", "new-hash"); !errors.Is(err, ErrConflict) {
		t.Fatal("initialization replaced an existing administrator", err)
	}
	if _, err = s.CreateMember(ctx, "admin", "member-hash"); err != nil {
		t.Fatal("member namespace was not independently created", err)
	}
	var version int
	if err = s.DB.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 2 {
		t.Fatal("migration version", version, err)
	}
}

func TestAccountMigrationFailureRollsBackDDLAndVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "malformed.db")
	db, err := sql.Open("sqlite", sqliteFileURI(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(migration); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("CREATE TABLE users (id TEXT PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("PRAGMA user_version=1"); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(path); err == nil {
		s.Close()
		t.Fatal("malformed member schema accepted")
	}
	var version, newColumns, newTables int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('admins') WHERE name IN ('disabled','created_at')").Scan(&newColumns); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='user_sessions'").Scan(&newTables); err != nil {
		t.Fatal(err)
	}
	if version != 1 || newColumns != 0 || newTables != 0 {
		t.Fatalf("failed migration committed changes: version=%d columns=%d tables=%d", version, newColumns, newTables)
	}
}

func TestAdminManagementProtectsCurrentAndLastActiveAccounts(t *testing.T) {
	s, ctx, _, _ := fixture(t)
	if err := s.EnsureAdmin(ctx, "original", "hash-1"); err != nil {
		t.Fatal(err)
	}
	one, err := s.GetAdminByUsername(ctx, "original")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetAdminDisabled(ctx, one.ID, one.ID, true); !errors.Is(err, ErrSelfAdmin) {
		t.Fatal("current administrator disabled itself", err)
	}
	if _, err = s.SetAdminDisabled(ctx, "another-actor", one.ID, true); !errors.Is(err, ErrLastActiveAdmin) {
		t.Fatal("last administrator disabled", err)
	}
	if err = s.ResetAdminPassword(ctx, one.ID, one.ID, "replacement"); !errors.Is(err, ErrSelfAdmin) {
		t.Fatal("current administrator bypassed password confirmation", err)
	}
	two, err := s.CreateAdmin(ctx, "second", "hash-2")
	if err != nil || two.CreatedAt == "" || two.PasswordHash != "" {
		t.Fatal("create administrator", two, err)
	}
	if _, err = s.CreateAdmin(ctx, "second", "another"); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate administrator accepted", err)
	}
	ss := domain.Session{AdminID: two.ID, TokenHash: "admin-session", CSRFToken: "csrf", ExpiresAt: nowUTC().Add(time.Hour)}
	if err = s.SaveAuthenticatedSession(ctx, ss, "hash-2"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetAdminDisabled(ctx, one.ID, two.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetSession(ctx, ss.TokenHash); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("disabled administrator session survived", err)
	}
	if err = s.SaveAuthenticatedSession(ctx, ss, "hash-2"); !errors.Is(err, ErrConflict) {
		t.Fatal("login created session after disable", err)
	}
	if _, err = s.SetAdminDisabled(ctx, one.ID, two.ID, false); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveAuthenticatedSession(ctx, ss, "hash-2"); err != nil {
		t.Fatal(err)
	}
	if err = s.ResetAdminPassword(ctx, one.ID, two.ID, "hash-3"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetSession(ctx, ss.TokenHash); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("reset password retained session", err)
	}
	if err = s.SaveAuthenticatedSession(ctx, ss, "hash-2"); !errors.Is(err, ErrConflict) {
		t.Fatal("stale login password minted session after reset", err)
	}
	list, err := s.ListAdmins(ctx)
	if err != nil || len(list) != 2 {
		t.Fatal("list administrators", list, err)
	}
	for _, u := range list {
		if u.PasswordHash != "" {
			t.Fatal("administrator list loaded a password hash")
		}
	}
}

func TestConcurrentAdminDisableKeepsOneActiveAcrossHandles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concurrent-admins.db")
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := OpenExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ctx := context.Background()
	one, err := a.CreateAdmin(ctx, "one", "hash-1")
	if err != nil {
		t.Fatal(err)
	}
	two, err := a.CreateAdmin(ctx, "two", "hash-2")
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, attempt := range []struct {
		store         *Store
		actor, target string
	}{{a, two.ID, one.ID}, {b, one.ID, two.ID}} {
		wg.Add(1)
		go func(st *Store, actor, target string) {
			defer wg.Done()
			<-start
			_, e := st.SetAdminDisabled(ctx, actor, target, true)
			results <- e
		}(attempt.store, attempt.actor, attempt.target)
	}
	close(start)
	wg.Wait()
	close(results)
	success, blocked := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrLastActiveAdmin) {
			blocked++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || blocked != 1 {
		t.Fatalf("concurrent disable results: success=%d blocked=%d", success, blocked)
	}
	var active int
	if err = a.DB.QueryRow("SELECT COUNT(*) FROM admins WHERE disabled=0").Scan(&active); err != nil || active != 1 {
		t.Fatal("last active administrator lost", active, err)
	}
}

func TestMemberSessionsAndPasswordRevocationStaySeparate(t *testing.T) {
	s, ctx, _, _ := fixture(t)
	a, err := s.CreateAdmin(ctx, "same-name", "admin-hash")
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.CreateMember(ctx, "same-name", "member-hash")
	if err != nil || u.CreatedAt == "" || u.PasswordHash != "" {
		t.Fatal("member create", u, err)
	}
	if _, err = s.CreateMember(ctx, "same-name", "different"); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate member accepted", err)
	}
	adminSession := domain.Session{AdminID: a.ID, TokenHash: "same-token", CSRFToken: "admin-csrf", ExpiresAt: nowUTC().Add(time.Hour)}
	memberSession := domain.MemberSession{MemberID: u.ID, TokenHash: "same-token", CSRFToken: "member-csrf", ExpiresAt: nowUTC().Add(time.Hour)}
	if err = s.SaveAuthenticatedSession(ctx, adminSession, "admin-hash"); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveAuthenticatedMemberSession(ctx, memberSession, "member-hash"); err != nil {
		t.Fatal(err)
	}
	if err = s.UpdateMemberPassword(ctx, u.ID, "new-member-hash"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetMemberSession(ctx, "same-token"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("member password retained member session", err)
	}
	if ss, err := s.GetSession(ctx, "same-token"); err != nil || ss.CSRFToken != "admin-csrf" {
		t.Fatal("member password revoked administrator session", ss, err)
	}
	if err = s.SaveAuthenticatedMemberSession(ctx, memberSession, "member-hash"); !errors.Is(err, ErrConflict) {
		t.Fatal("stale member password minted session", err)
	}
	if err = s.SaveAuthenticatedMemberSession(ctx, memberSession, "new-member-hash"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetMemberDisabled(ctx, u.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetMemberSession(ctx, "same-token"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("disabled member retained session", err)
	}
	if err = s.SaveAuthenticatedMemberSession(ctx, memberSession, "new-member-hash"); !errors.Is(err, ErrConflict) {
		t.Fatal("disabled member minted session", err)
	}
	items, err := s.ListMembers(ctx)
	if err != nil || len(items) != 1 || items[0].PasswordHash != "" {
		t.Fatal("member list included credentials", items, err)
	}
	encoded, err := json.Marshal(items)
	if err != nil || strings.Contains(string(encoded), "hash") {
		t.Fatal("member response included password", string(encoded), err)
	}
}

func TestAccountChangesAndSessionRevocationAreAtomic(t *testing.T) {
	s, ctx, _, _ := fixture(t)
	one, err := s.CreateAdmin(ctx, "actor", "actor-hash")
	if err != nil {
		t.Fatal(err)
	}
	two, err := s.CreateAdmin(ctx, "target", "old-hash")
	if err != nil {
		t.Fatal(err)
	}
	ss := domain.Session{AdminID: two.ID, TokenHash: "preserved", CSRFToken: "csrf", ExpiresAt: nowUTC().Add(time.Hour)}
	if err = s.SaveAuthenticatedSession(ctx, ss, "old-hash"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("CREATE TRIGGER reject_session_delete BEFORE DELETE ON sessions BEGIN SELECT RAISE(ABORT,'test session failure'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetAdminDisabled(ctx, one.ID, two.ID, true); err == nil {
		t.Fatal("disable accepted failed session deletion")
	}
	if err = s.ResetAdminPassword(ctx, one.ID, two.ID, "replacement"); err == nil {
		t.Fatal("password reset accepted failed session deletion")
	}
	u, err := s.GetAdminByID(ctx, two.ID)
	if err != nil || u.Disabled || u.PasswordHash != "old-hash" {
		t.Fatal("failed revocation partially changed administrator", u, err)
	}
	if _, err = s.GetSession(ctx, ss.TokenHash); err != nil {
		t.Fatal("failed transaction lost session", err)
	}
}

func TestCleanupExpiresBothSessionNamespaces(t *testing.T) {
	s, ctx, _, _ := fixture(t)
	a, err := s.CreateAdmin(ctx, "cleanup-admin", "admin-hash")
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.CreateMember(ctx, "cleanup-member", "member-hash")
	if err != nil {
		t.Fatal(err)
	}
	now := nowUTC()
	for _, item := range []struct {
		token  string
		expiry time.Time
	}{{"expired", now.Add(-time.Hour)}, {"active", now.Add(time.Hour)}} {
		if err = s.SaveAuthenticatedSession(ctx, domain.Session{AdminID: a.ID, TokenHash: item.token, CSRFToken: "admin-csrf", ExpiresAt: item.expiry}, "admin-hash"); err != nil {
			t.Fatal(err)
		}
		if err = s.SaveAuthenticatedMemberSession(ctx, domain.MemberSession{MemberID: u.ID, TokenHash: item.token, CSRFToken: "member-csrf", ExpiresAt: item.expiry}, "member-hash"); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.Cleanup(ctx, now); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"sessions", "user_sessions"} {
		var count int
		if err = s.DB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("cleanup %s count=%d error=%v", table, count, err)
		}
	}
}
