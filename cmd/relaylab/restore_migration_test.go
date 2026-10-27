package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"relaylab/internal/domain"
	"relaylab/internal/security"
	"relaylab/internal/store"
)

type legacyRestoreFixture struct {
	parts       []zipPart
	adminHash   string
	sessionHash string
}

func schemaOneRestoreFixture(t *testing.T, mutate func(*sql.DB)) legacyRestoreFixture {
	t.Helper()
	dir := t.TempDir()
	vault, err := security.LoadVault(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	key, err := os.ReadFile(filepath.Join(dir, "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := vault.Encrypt("legacy-channel-secret")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "relaylab.db")
	db, err := sql.Open("sqlite", filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ddl, err := os.ReadFile(filepath.Join("..", "..", "internal", "store", "migrations", "001_init.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(ddl)); err != nil {
		t.Fatal(err)
	}
	hash, err := security.PasswordHash("legacy-admin-password-123456")
	if err != nil {
		t.Fatal(err)
	}
	settings, err := json.Marshal(domain.DefaultSettings())
	if err != nil {
		t.Fatal(err)
	}
	platform, err := json.Marshal(domain.Platform{ID: "legacy-platform", Name: "旧平台", Public: true})
	if err != nil {
		t.Fatal(err)
	}
	channel, err := json.Marshal(domain.Channel{ID: "legacy-channel", PlatformID: "legacy-platform", Name: "旧渠道", Revision: 1, Enabled: true, Public: true})
	if err != nil {
		t.Fatal(err)
	}
	sessionHash := security.HashToken("legacy-admin-session")
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{"INSERT INTO settings(id,data) VALUES(1,?)", []any{string(settings)}},
		{"INSERT INTO admins(id,username,password_hash) VALUES('legacy-admin','legacy',?)", []any{hash}},
		{"INSERT INTO sessions(token_hash,admin_id,csrf_token,expires_at) VALUES(?,'legacy-admin','legacy-csrf',?)", []any{sessionHash, time.Now().UTC().Add(time.Hour).Format("2006-01-02T15:04:05.000000000Z")}},
		{"INSERT INTO platforms(id,data) VALUES('legacy-platform',?)", []any{string(platform)}},
		{"INSERT INTO channels(id,platform_id,encrypted_key,revision,data) VALUES('legacy-channel','legacy-platform',?,1,?)", []any{cipher, string(channel)}},
		{"PRAGMA user_version=1", nil},
	} {
		if _, err = db.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	if mutate != nil {
		mutate(db)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	database, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return legacyRestoreFixture{parts: []zipPart{{name: "relaylab.db", data: database}, {name: "master.key", data: key}, {name: "manifest.json", data: []byte(`{"schema":1,"version":"v1.2.2"}`)}}, adminHash: hash, sessionHash: sessionHash}
}

func TestRestoreMigratesSchemaOneBackupAndPreservesAccountsAndKeys(t *testing.T) {
	fixture := schemaOneRestoreFixture(t, nil)
	archive := writeTestZIP(t, fixture.parts, false)
	original, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "restored")
	if err = restore(target, archive); err != nil {
		t.Fatal("schema-one restore", err)
	}
	ctx := context.Background()
	s, err := store.OpenExisting(filepath.Join(target, "relaylab.db"))
	if err != nil {
		t.Fatal("restored database was not migrated", err)
	}
	defer s.Close()
	var version int
	if err = s.DB.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 3 {
		t.Fatal("restored schema version", version, err)
	}
	admin, err := s.GetAdminByUsername(ctx, "legacy")
	if err != nil || admin.ID != "legacy-admin" || admin.PasswordHash != fixture.adminHash || admin.Disabled {
		t.Fatalf("administrator changed during restore: %#v %v", admin, err)
	}
	if session, err := s.GetSession(ctx, fixture.sessionHash); err != nil || session.AdminID != admin.ID || session.CSRFToken != "legacy-csrf" {
		t.Fatal("existing session changed during restore", session, err)
	}
	if _, err = s.CreateMember(ctx, "new-user", "fixture-hash"); err != nil {
		t.Fatal("restored member schema unavailable", err)
	}
	vault, err := security.LoadVault(target, true)
	if err != nil {
		t.Fatal(err)
	}
	channel, err := s.GetChannel(ctx, "legacy-channel")
	if err != nil {
		t.Fatal(err)
	}
	if secret, err := vault.Decrypt(channel.EncryptedKey); err != nil || secret != "legacy-channel-secret" {
		t.Fatal("restored channel key changed", err)
	}
	current, err := os.ReadFile(archive)
	if err != nil || !bytes.Equal(current, original) {
		t.Fatal("restore modified original backup", err)
	}
}

func TestRestoreRejectsInvalidSchemaOneBackupsBeforePublishing(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mutate   func(*sql.DB)
		wrongKey bool
		want     string
	}{
		{name: "wrong key", wrongKey: true, want: "解密"},
		{name: "future database", mutate: func(db *sql.DB) {
			if _, err := db.Exec("PRAGMA user_version=4"); err != nil {
				t.Fatal(err)
			}
		}, want: "unsupported database schema"},
		{name: "missing legacy table", mutate: func(db *sql.DB) {
			if _, err := db.Exec("DROP TABLE schedules"); err != nil {
				t.Fatal(err)
			}
		}, want: "migration required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := schemaOneRestoreFixture(t, tc.mutate)
			if tc.wrongKey {
				fixture.parts[1].data = bytes.Repeat([]byte{0x5a}, 32)
			}
			archive := writeTestZIP(t, fixture.parts, false)
			target := filepath.Join(t.TempDir(), "not-published")
			err := restore(target, archive)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q got %v", tc.want, err)
			}
			if _, err = os.Stat(target); !os.IsNotExist(err) {
				t.Fatal("invalid legacy backup published a directory", err)
			}
		})
	}
}
