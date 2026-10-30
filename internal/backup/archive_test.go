package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"relaylab/internal/domain"
	"relaylab/internal/security"
	"relaylab/internal/store"
)

type archiveFixture struct {
	dir                 string
	store               *store.Store
	identity, recipient string
}

func newArchiveFixture(t *testing.T) archiveFixture {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	vault, err := security.LoadVault(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "relaylab.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if err = st.SavePlatform(ctx, domain.Platform{ID: "platform", Name: "platform"}); err != nil {
		t.Fatal(err)
	}
	cipher, err := vault.Encrypt("channel-backup-fixture-secret")
	if err != nil {
		t.Fatal(err)
	}
	if err = st.SaveChannel(ctx, domain.Channel{ID: "channel", PlatformID: "platform", Name: "channel", EncryptedKey: cipher}); err != nil {
		t.Fatal(err)
	}
	member, err := st.CreateMember(ctx, "archive-user", "fixture-hash")
	if err != nil {
		t.Fatal(err)
	}
	userCipher, err := vault.EncryptUserConnection(member.ID, "connection", "private-backup-fixture-secret")
	if err != nil {
		t.Fatal(err)
	}
	if err = st.SaveUserConnection(ctx, domain.UserConnection{ID: "connection", UserID: member.ID, Name: "private", BaseURL: "https://example.com/v1", EncryptedKey: userCipher, CredentialFingerprint: "fixture", Revision: 1}); err != nil {
		t.Fatal(err)
	}
	cloudCipher, err := vault.EncryptBackupCredentials("cloud-storage", "cloud-backup-fixture-secret")
	if err != nil {
		t.Fatal(err)
	}
	if err = st.SaveBackupStorage(ctx, domain.BackupStorage{ID: "cloud-storage", Revision: 1, Endpoint: "https://s3.example.com", Bucket: "fixture"}, cloudCipher); err != nil {
		t.Fatal(err)
	}
	private, public, err := GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	return archiveFixture{dir: dir, store: st, identity: private, recipient: public}
}

func TestCreateRejectsBrokenDatabaseRelationships(t *testing.T) {
	f := newArchiveFixture(t)
	if _, e := f.store.DB.Exec("PRAGMA foreign_keys=OFF"); e != nil {
		t.Fatal(e)
	}
	if _, e := f.store.DB.Exec("UPDATE channels SET platform_id='missing-platform'"); e != nil {
		t.Fatal(e)
	}
	out := filepath.Join(t.TempDir(), "broken.zip.age")
	e := CreateFromStore(context.Background(), f.store, f.dir, out, Options{Recipient: f.recipient, Schema: 4})
	if e == nil || !strings.Contains(e.Error(), "关联完整性") {
		t.Fatal("broken relationships accepted", e)
	}
	if _, e = os.Stat(out); !os.IsNotExist(e) {
		t.Fatal("invalid backup was published", e)
	}
}

func TestEncryptedRoundTripAndCredentialValidation(t *testing.T) {
	f := newArchiveFixture(t)
	ctx := context.Background()
	out := filepath.Join(t.TempDir(), "backup.zip.age")
	if err := CreateFromStore(ctx, f.store, f.dir, out, Options{Recipient: f.recipient, Version: "1.5.0", Commit: "fixture"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(raw, []byte("age-encryption.org/v1")) || bytes.Contains(raw, []byte("SQLite format")) {
		t.Fatal("backup is not encrypted")
	}
	wrong, _, err := GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Validate(ctx, out, wrong, StagingRoot(f.dir)); err == nil {
		t.Fatal("wrong private key accepted")
	}
	v, err := Validate(ctx, out, f.identity, StagingRoot(f.dir))
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	if v.Manifest.FormatVersion != 2 || v.Manifest.AppVersion != "1.5.0" || len(v.Manifest.Files) != 2 || v.Manifest.KeyID != RecipientID(f.recipient) {
		t.Fatalf("bad manifest: %#v", v.Manifest)
	}
	if _, err = os.Stat(filepath.Join(v.Dir, ".decrypted.zip")); !os.IsNotExist(err) {
		t.Fatal("decrypted archive retained")
	}
	target := filepath.Join(t.TempDir(), "restored")
	if err = Restore(ctx, target, out, f.identity); err != nil {
		t.Fatal(err)
	}
	restored, err := store.OpenExisting(filepath.Join(target, "relaylab.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	vault, err := security.LoadVault(target, true)
	if err != nil {
		t.Fatal(err)
	}
	channel, err := restored.GetChannel(ctx, "channel")
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := vault.Decrypt(channel.EncryptedKey); err != nil || plain != "channel-backup-fixture-secret" {
		t.Fatal("channel key lost", err)
	}
	connections, err := restored.ListUserConnections(ctx, "")
	if err != nil || len(connections) != 1 {
		t.Fatal("user connection missing", err)
	}
	c := connections[0]
	if plain, err := vault.DecryptUserConnection(c.UserID, c.ID, c.EncryptedKey); err != nil || plain != "private-backup-fixture-secret" {
		t.Fatal("user key lost", err)
	}
	_, cloudCipher, err := restored.BackupStorage(ctx, "cloud-storage")
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := vault.DecryptBackupCredentials("cloud-storage", cloudCipher); err != nil || plain != "cloud-backup-fixture-secret" {
		t.Fatal("cloud key lost", err)
	}
	if after, err := os.ReadFile(out); err != nil || !bytes.Equal(raw, after) {
		t.Fatal("restore modified input", err)
	}
	if err = Restore(ctx, target, out, f.identity); err == nil {
		t.Fatal("nonempty restore target accepted")
	}
}

func decryptedMembers(t *testing.T, input, identity string) map[string][]byte {
	t.Helper()
	f, err := os.Open(input)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	i, err := age.ParseX25519Identity(identity)
	if err != nil {
		t.Fatal(err)
	}
	r, err := age.Decrypt(f, i)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	items := map[string][]byte{}
	for _, item := range zr.File {
		r, err := item.Open()
		if err != nil {
			t.Fatal(err)
		}
		items[item.Name], err = io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	return items
}

func writeMembers(t *testing.T, items map[string][]byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "modified.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, b := range items {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	if err = zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestManifestDigestAndDatabaseSchemaCannotBeForgedByMetadata(t *testing.T) {
	f := newArchiveFixture(t)
	ctx := context.Background()
	out := filepath.Join(t.TempDir(), "backup.age")
	if err := CreateFromStore(ctx, f.store, f.dir, out, Options{Recipient: f.recipient}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, want string
		mutate     func(map[string][]byte)
	}{
		{"digest", "SHA-256", func(items map[string][]byte) { items["relaylab.db"][0] ^= 1 }},
		{"schema", "版本不一致", func(items map[string][]byte) {
			var m Manifest
			_ = json.Unmarshal(items["manifest.json"], &m)
			m.DatabaseSchema++
			items["manifest.json"], _ = json.Marshal(m)
		}},
		{"wrong master key", "解密", func(items map[string][]byte) {
			items["master.key"] = bytes.Repeat([]byte{0x73}, 32)
			var m Manifest
			_ = json.Unmarshal(items["manifest.json"], &m)
			h := sha256.Sum256(items["master.key"])
			m.Files["master.key"] = Member{Size: 32, SHA256: hex.EncodeToString(h[:])}
			items["manifest.json"], _ = json.Marshal(m)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := decryptedMembers(t, out, f.identity)
			tc.mutate(items)
			input := writeMembers(t, items)
			target := filepath.Join(t.TempDir(), "target")
			err := Restore(ctx, target, input, "")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %s got %v", tc.want, err)
			}
			if _, err = os.Stat(target); !os.IsNotExist(err) {
				t.Fatal("bad archive published")
			}
		})
	}
}

func TestLegacyArchiveAndNoOverwrite(t *testing.T) {
	f := newArchiveFixture(t)
	ctx := context.Background()
	out := filepath.Join(t.TempDir(), "legacy.zip")
	if err := CreateFromStore(ctx, f.store, f.dir, out, Options{Legacy: true, Version: "1.5.0"}); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.OpenReader(out)
	if err != nil {
		t.Fatal(err)
	}
	_ = zr.Close()
	v, err := Validate(ctx, out, "", StagingRoot(f.dir))
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	if v.Manifest.Schema != 1 || v.Manifest.FormatVersion != 1 {
		t.Fatal("legacy format changed")
	}
	before, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if err = CreateFromStore(ctx, f.store, f.dir, out, Options{Legacy: true}); err == nil {
		t.Fatal("existing backup overwritten")
	}
	after, err := os.ReadFile(out)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("existing backup changed", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	path := filepath.Join(t.TempDir(), "canceled.zip")
	if err = CreateFromStore(canceled, f.store, f.dir, path, Options{Legacy: true}); err == nil {
		t.Fatal("cancellation ignored")
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("canceled backup published")
	}
}

func TestLargeSnapshotDoesNotUseSystemTemporaryDirectory(t *testing.T) {
	if testing.Short() {
		t.Skip("large SQLite snapshot")
	}
	f := newArchiveFixture(t)
	if _, err := f.store.DB.Exec("CREATE TABLE large_payload(value BLOB); INSERT INTO large_payload VALUES(zeroblob(70*1024*1024))"); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(t.TempDir(), "blocked-temp")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"TMP", "TEMP", "TMPDIR"} {
		t.Setenv(key, blocked)
	}
	out := filepath.Join(filepath.Dir(f.dir), "backups", "large.zip")
	if err := CreateFromStore(context.Background(), f.store, f.dir, out, Options{Legacy: true}); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.OpenReader(out)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	for _, item := range zr.File {
		if item.Name == "relaylab.db" {
			if item.UncompressedSize64 <= 64<<20 {
				t.Fatal("fixture not larger than Docker tmpfs")
			}
			return
		}
	}
	t.Fatal("database not in backup")
}

func TestRejectsLinksUnknownAndDuplicateMembers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		headers []zip.FileHeader
	}{
		{"traversal", []zip.FileHeader{{Name: "../master.key"}}},
		{"duplicate", []zip.FileHeader{{Name: "relaylab.db"}, {Name: "relaylab.db"}}},
		{"symlink", func() []zip.FileHeader {
			h := zip.FileHeader{Name: "relaylab.db"}
			h.SetMode(os.ModeSymlink | 0600)
			return []zip.FileHeader{h}
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := filepath.Join(t.TempDir(), "invalid.zip")
			file, err := os.Create(input)
			if err != nil {
				t.Fatal(err)
			}
			zw := zip.NewWriter(file)
			for _, h := range tc.headers {
				w, err := zw.CreateHeader(&h)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = w.Write([]byte("payload"))
			}
			_ = zw.Close()
			_ = file.Close()
			root := t.TempDir()
			if _, err = Validate(context.Background(), input, "", root); err == nil {
				t.Fatal("malicious archive accepted")
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatal("failed validation retained sensitive files", err)
			}
		})
	}
}

func TestRestoreFinalizesQueuesSessionsAndFutureSchedules(t *testing.T) {
	f := newArchiveFixture(t)
	ctx := context.Background()
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	for i, state := range []string{"queued", "claimed", "dispatching"} {
		modelID := fmt.Sprintf("model-%d", i)
		if _, err := f.store.DB.Exec("INSERT INTO models(id,channel_id,upstream_id,data) VALUES(?,'channel',?,'{}')", modelID, modelID); err != nil {
			t.Fatal(err)
		}
		job := domain.Job{ID: "job-" + state, ModelID: modelID, ChannelID: "channel", Kind: "artifact", State: state}
		body, _ := json.Marshal(job)
		if _, err := f.store.DB.Exec("INSERT INTO jobs(id,model_id,channel_id,kind,state,scheduled_at,created_at,data) VALUES(?,?,?,'artifact',?,?,?,?)", job.ID, modelID, "channel", state, stamp, stamp, string(body)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.store.DB.Exec("INSERT INTO admins(id,username,password_hash) VALUES('admin','admin','fixture-hash'); INSERT INTO sessions VALUES('session','admin','csrf','2099-01-01')"); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "restore.zip.age")
	if err := CreateFromStore(ctx, f.store, f.dir, out, Options{Recipient: f.recipient}); err != nil {
		t.Fatal(err)
	}
	preflight, err := Validate(ctx, out, f.identity, StagingRoot(f.dir))
	if err != nil {
		t.Fatal(err)
	}
	defer preflight.Close()
	before, err := store.OpenExisting(preflight.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	var pending int
	if err = before.DB.QueryRow("SELECT COUNT(*) FROM jobs WHERE state IN ('queued','claimed','dispatching')").Scan(&pending); err != nil || pending != 3 {
		t.Fatal("preflight changed queue", pending, err)
	}
	_ = before.Close()
	target := filepath.Join(t.TempDir(), "restored")
	if err = Restore(ctx, target, out, f.identity); err != nil {
		t.Fatal(err)
	}
	st, err := store.OpenExisting(filepath.Join(target, "relaylab.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, state := range []string{"queued", "claimed", "dispatching"} {
		job, err := st.GetJob(ctx, "job-"+state)
		if err != nil {
			t.Fatal(err)
		}
		want := "canceled"
		if state == "dispatching" {
			want = "uncertain"
		}
		if job.State != want {
			t.Fatalf("%s restored to %s", state, job.State)
		}
	}
	if err = st.DB.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&pending); err != nil || pending != 0 {
		t.Fatal("old sessions retained", pending, err)
	}
	if err = st.DB.QueryRow("SELECT COUNT(*) FROM jobs WHERE state IN ('queued','claimed','dispatching')").Scan(&pending); err != nil || pending != 0 {
		t.Fatal("old queue runnable", pending, err)
	}
	if _, err = os.Stat(filepath.Join(target, "relaylab.db-wal")); err == nil {
		info, _ := os.Stat(filepath.Join(target, "relaylab.db-wal"))
		if info.Size() != 0 {
			t.Fatal("restored data depended on uncheckpointed WAL")
		}
	}
}
