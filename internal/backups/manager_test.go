package backups

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"relaylab/internal/backup"
	"relaylab/internal/domain"
	"relaylab/internal/security"
	"relaylab/internal/store"
)

func newTestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), "data")
	s, e := store.Open(filepath.Join(dataDir, "relaylab.db"))
	if e != nil {
		t.Fatal(e)
	}
	v, e := security.LoadVault(dataDir, false)
	if e != nil {
		t.Fatal(e)
	}
	m, e := New(Options{Store: s, Vault: v, DataDir: dataDir, BuildVersion: "1.5.0", Enter: func() (func(), bool) { return func() {}, true }})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { m.Close(); s.Close() })
	identity, recipient, e := backup.GenerateIdentity()
	if e != nil {
		t.Fatal(e)
	}
	if e = m.SetRecipient(context.Background(), recipient); e != nil {
		t.Fatal(e)
	}
	if e = m.ValidateIdentity(context.Background(), identity); e != nil {
		t.Fatal(e)
	}
	return m, identity
}
func waitRun(t *testing.T, m *Manager, id string) Run {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		r, e := m.Record(context.Background(), id)
		if e != nil {
			t.Fatal(e)
		}
		if terminal(r.State) {
			m.mu.Lock()
			active := m.activeID
			m.mu.Unlock()
			if active == "" {
				return r
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("backup did not finish")
	return Run{}
}
func TestEncryptedBackupIdempotencyAndRestore(t *testing.T) {
	m, identity := newTestManager(t)
	ctx := context.Background()
	if e := m.opts.Store.EnsureAdmin(ctx, "backup-admin", "not-a-real-password-hash"); e != nil {
		t.Fatal(e)
	}
	r, e := m.Create(ctx, "request-one")
	if e != nil {
		t.Fatal(e)
	}
	duplicate, e := m.Create(ctx, "request-one")
	if e != nil || duplicate.ID != r.ID {
		t.Fatalf("idempotency %+v %v", duplicate, e)
	}
	result := waitRun(t, m, r.ID)
	if result.State != "succeeded" || result.LocalState != "verified" || result.SHA256 == "" {
		t.Fatalf("unexpected result %+v", result)
	}
	path, e := m.DownloadPath(ctx, r.ID)
	if e != nil {
		t.Fatal(e)
	}
	validated, e := backup.Validate(ctx, path, identity, filepath.Join(m.opts.BackupDir, ".staging"))
	if e != nil {
		t.Fatal(e)
	}
	defer validated.Close()
	if validated.Manifest.DatabaseSchema != 4 {
		t.Fatal(validated.Manifest)
	}
	snapshot, e := m.Snapshot(ctx)
	if e != nil || snapshot.LastSuccess == nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(snapshot)
	if strings.Contains(string(raw), "local_path") || strings.Contains(string(raw), identity) {
		t.Fatal("snapshot exposed private material")
	}
	wrong, _, _ := backup.GenerateIdentity()
	if _, e = backup.Validate(ctx, path, wrong, filepath.Join(m.opts.BackupDir, ".staging")); e == nil {
		t.Fatal("accepted wrong identity")
	}
}
func TestRecipientChangeDisablesSchedule(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()
	p, _ := m.opts.Store.BackupPolicy(ctx)
	p.Enabled = true
	if _, e := m.SaveSchedule(ctx, p); e != nil {
		t.Fatal(e)
	}
	_, recipient, _ := backup.GenerateIdentity()
	if e := m.SetRecipient(ctx, recipient); e != nil {
		t.Fatal(e)
	}
	p, _ = m.opts.Store.BackupPolicy(ctx)
	if p.Enabled || p.RecipientVerified || p.NextRunAt != nil {
		t.Fatal("key rotation kept backup active")
	}
	if _, e := m.Create(ctx, "unverified-request"); !errors.Is(e, ErrKeyNotVerified) {
		t.Fatalf("%v", e)
	}
}
func TestCronAndArchiveBoundaries(t *testing.T) {
	p := domain.DefaultBackupSchedule()
	at := time.Date(2028, 2, 29, 19, 0, 0, 0, time.UTC)
	p.MonthlyEnabled = true
	p.MonthlyDays = []int{1, 30, 31}
	p.MonthlyMonthEnd = true
	dates := archiveDates(at, p)
	if len(dates) != 1 || dates[0] != "2028-03-01" {
		t.Fatalf("cross-month day must not claim February: %v", dates)
	}
	at = time.Date(2028, 2, 29, 3, 0, 0, 0, time.UTC)
	dates = archiveDates(at, p)
	if len(dates) != 2 || dates[1] != "2028-02-29" {
		t.Fatalf("month clamp %v", dates)
	}
	next, e := previewAt(p, time.Date(2026, 10, 29, 17, 59, 0, 0, time.UTC))
	if e != nil || !next[0].Equal(time.Date(2026, 10, 29, 18, 0, 0, 0, time.UTC)) {
		t.Fatalf("timezone %v %v", next, e)
	}
	p.Cron = "* * * * * *"
	if _, e = Preview(p); e == nil {
		t.Fatal("accepted six-field cron")
	}
}
func TestRetentionProtectsNewestAndArchives(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()
	p := domain.DefaultBackupSchedule()
	p.RetainCount = 1
	p.RetainDays = 0
	now := time.Now().UTC()
	ids := []string{}
	for i := 0; i < 3; i++ {
		id := domain.NewID()
		path := filepath.Join(m.opts.BackupDir, id+".zip.age")
		os.WriteFile(path, []byte("encrypted fixture"), 0600)
		h, n, _ := hashFile(path)
		at := now.Add(-time.Duration(i) * time.Hour)
		r := Run{ID: id, RequestID: id, State: "succeeded", CreatedAt: at, StartedAt: at, LocalPath: path, LocalState: "verified", SHA256: h, SizeBytes: n}
		if i == 2 {
			r.MonthlyArchive = true
			r.ArchiveRetainCount = 0
		}
		if _, _, e := m.opts.Store.CreateBackupRun(ctx, r); e != nil {
			t.Fatal(e)
		}
		if e := m.save(ctx, r); e != nil {
			t.Fatal(e)
		}
		ids = append(ids, id)
	}
	m.cleanup(ctx, p)
	records, e := m.List(ctx, 100, 0)
	if e != nil || len(records) != 2 {
		t.Fatalf("retained %d: %v", len(records), e)
	}
	if _, e = m.Record(ctx, ids[0]); e != nil {
		t.Fatal("deleted latest")
	}
	if _, e = m.Record(ctx, ids[2]); e != nil {
		t.Fatal("deleted archive")
	}
	if e = m.Delete(ctx, ids[2]); !errors.Is(e, ErrProtected) {
		t.Fatalf("deleted protected %v", e)
	}
}
func TestStorageURLAndSecretMasking(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()
	in := StorageInput{Endpoint: "https://bucket.example.com", Bucket: "relaylab-backups", AccessKeyID: "access-credential", SecretAccessKey: "secret-credential"}
	saved, e := m.SaveStorage(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(saved)
	if strings.Contains(string(b), in.SecretAccessKey) || strings.Contains(string(b), in.AccessKeyID) {
		t.Fatal("secret exposed")
	}
	_, cipher, e := m.opts.Store.BackupStorage(ctx, saved.ID)
	if e != nil || strings.Contains(cipher, in.SecretAccessKey) {
		t.Fatal("plaintext stored")
	}
	in.Endpoint = "https://another.example.com"
	in.SecretAccessKey = ""
	if _, e = m.SaveStorage(ctx, in); e == nil {
		t.Fatal("sent old secret to new host")
	}
	in.Endpoint = "https://127.0.0.1"
	if _, e = validateStorage(in, nil); e == nil {
		t.Fatal("accepted loopback")
	}
}

func TestReconcileCatalogRestoresIndexWithoutScheduling(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()
	r, e := m.Create(ctx, "reconcile-test")
	if e != nil {
		t.Fatal(e)
	}
	r = waitRun(t, m, r.ID)
	if r.State != "succeeded" {
		t.Fatal(r)
	}
	if e = m.opts.Store.DeleteBackupRun(ctx, r.ID); e != nil {
		t.Fatal(e)
	}
	p, _ := m.opts.Store.BackupPolicy(ctx)
	p.Enabled = true
	p.ReconciliationRequired = true
	if e = m.opts.Store.SaveBackupPolicy(ctx, p); e != nil {
		t.Fatal(e)
	}
	if e = m.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	restored, e := m.Record(ctx, r.ID)
	if e != nil || restored.SHA256 != r.SHA256 {
		t.Fatalf("catalog restore %v %+v", e, restored)
	}
	p, _ = m.opts.Store.BackupPolicy(ctx)
	if p.Enabled || p.ReconciliationRequired {
		t.Fatal("reconcile enabled schedule or stayed frozen")
	}
	path, e := m.DownloadPath(ctx, r.ID)
	if e != nil || path == "" {
		t.Fatal(e)
	}
}
