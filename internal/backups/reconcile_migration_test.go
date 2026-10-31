package backups

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"relaylab/internal/backup"
	"relaylab/internal/domain"
	"relaylab/internal/durablejson"
	"relaylab/internal/operationlock"
	"relaylab/internal/security"
	"relaylab/internal/store"
)

func copyFixtureFile(t *testing.T, source, target string) {
	t.Helper()
	b, e := os.ReadFile(source)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.MkdirAll(filepath.Dir(target), 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(target, b, 0600); e != nil {
		t.Fatal(e)
	}
}

func TestReconcileRelocatedDirectoryRebindsVerifiedArchive(t *testing.T) {
	source, identity := newTestManager(t)
	ctx := context.Background()
	r, e := source.Create(ctx, "relocated-dataset-test")
	if e != nil {
		t.Fatal(e)
	}
	r = waitRun(t, source, r.ID)
	data := filepath.Join(t.TempDir(), "relocated", "data")
	if e = backup.Restore(ctx, data, r.LocalPath, identity); e != nil {
		t.Fatal(e)
	}
	s, e := store.OpenExisting(filepath.Join(data, "relaylab.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	vault, e := security.LoadVault(data, true)
	if e != nil {
		t.Fatal(e)
	}
	m, e := New(Options{Store: s, Vault: vault, DataDir: data, Enter: func() (func(), bool) { return func() {}, true }})
	if e != nil {
		t.Fatal(e)
	}
	defer m.Close()
	copyFixtureFile(t, r.LocalPath, filepath.Join(m.opts.BackupDir, r.FileName))
	copyFixtureFile(t, filepath.Join(source.opts.BackupDir, "catalog", r.ID+".json"), filepath.Join(m.opts.BackupDir, "catalog", r.ID+".json"))
	if e = m.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	actual, e := m.Record(ctx, r.ID)
	if e != nil {
		t.Fatal(e)
	}
	if actual.LocalPath != filepath.Join(m.opts.BackupDir, r.FileName) || actual.LocalState != "verified" || actual.SHA256 != r.SHA256 {
		t.Fatalf("not rebound: %+v", actual)
	}
	p, _ := m.opts.Store.BackupPolicy(ctx)
	if p.ReconciliationRequired || p.Enabled {
		t.Fatal("relocated dataset stayed frozen or auto-enabled")
	}
}

func TestReconcileRelocatedArchiveRejectsTamperingAndLinks(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()
	r, e := m.Create(ctx, "relocated-tamper-test")
	if e != nil {
		t.Fatal(e)
	}
	r = waitRun(t, m, r.ID)
	r.LocalPath = filepath.Join(t.TempDir(), r.FileName)
	newPath := filepath.Join(m.opts.BackupDir, r.FileName)
	b, e := os.ReadFile(newPath)
	if e != nil {
		t.Fatal(e)
	}
	b[len(b)-1] ^= 0xff
	if e = os.WriteFile(newPath, b, 0600); e != nil {
		t.Fatal(e)
	}
	if e = m.rebindLocalFile(&r); e == nil {
		t.Fatal("tampered relocated file accepted")
	}
	outside := filepath.Join(t.TempDir(), "outside.zip.age")
	if e = os.WriteFile(outside, b, 0600); e != nil {
		t.Fatal(e)
	}
	linkName := "linked.zip.age"
	link := filepath.Join(m.opts.BackupDir, linkName)
	if e = os.Symlink(outside, link); e == nil {
		r.FileName = linkName
		r.LocalPath = "old"
		if e = m.rebindLocalFile(&r); e != nil {
			t.Fatal(e)
		}
		if r.LocalPath == link {
			t.Fatal("symlink rebound")
		}
	}
	r.FileName = "../outside.zip.age"
	r.LocalPath = "old"
	if e = m.rebindLocalFile(&r); e != nil {
		t.Fatal(e)
	}
	if r.LocalPath != "old" {
		t.Fatal("traversal rebound")
	}
}

func TestReconcileSkipsUnreferencedDifferentVaultCatalogs(t *testing.T) {
	old, _ := newTestManager(t)
	ctx := context.Background()
	storage, e := old.SaveStorage(ctx, StorageInput{Endpoint: "https://storage.example.com", Bucket: "fixture-backups", AccessKeyID: "old-access", SecretAccessKey: "old-secret"})
	if e != nil {
		t.Fatal(e)
	}
	r, e := old.Create(ctx, "foreign-vault-local-test")
	if e != nil {
		t.Fatal(e)
	}
	r = waitRun(t, old, r.ID)
	current, _ := newTestManager(t)
	copyFixtureFile(t, filepath.Join(old.opts.BackupDir, "storages", storage.ID+".json"), filepath.Join(current.opts.BackupDir, "storages", storage.ID+".json"))
	copyFixtureFile(t, filepath.Join(old.opts.BackupDir, "catalog", r.ID+".json"), filepath.Join(current.opts.BackupDir, "catalog", r.ID+".json"))
	fresh, e := current.SaveStorage(ctx, StorageInput{Endpoint: "https://storage.example.com", Bucket: "current-backups", AccessKeyID: "new-access", SecretAccessKey: "new-secret"})
	if e != nil {
		t.Fatal(e)
	}
	freezeForTest(t, current)
	if e = current.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	p, e := current.opts.Store.BackupPolicy(ctx)
	if e != nil || p.ReconciliationRequired || len(p.ReconciliationWarnings) == 0 {
		t.Fatal("foreign catalog not quarantined", p, e)
	}
	active, _, e := current.opts.Store.BackupStorage(ctx, "")
	if e != nil || active.ID != fresh.ID {
		t.Fatal("foreign revision selected", active, e)
	}
	if _, e = current.Record(ctx, r.ID); e == nil {
		t.Fatal("foreign dataset run imported")
	}
	if _, e = os.Stat(filepath.Join(current.opts.BackupDir, "catalog", r.ID+".json")); e != nil {
		t.Fatal("foreign metadata erased", e)
	}
}

func TestRestoredStorageRevisionCollisionPreservesActiveSelection(t *testing.T) {
	m, identity := newTestManager(t)
	ctx := context.Background()
	in := StorageInput{Endpoint: "https://storage.example.com", Bucket: "fixture-backups", AccessKeyID: "test-access", SecretAccessKey: "test-secret"}
	first, e := m.SaveStorage(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	in.Prefix = "old-second"
	oldSecond, e := m.SaveStorage(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	// Simulate restoring the database before the second revision existed while
	// keeping its external catalog. A fresh explicit save now reuses display 2.
	if _, e = m.opts.Store.DB.ExecContext(ctx, "DELETE FROM backup_storages WHERE id=?", oldSecond.ID); e != nil {
		t.Fatal(e)
	}
	p, e := m.opts.Store.BackupPolicy(ctx)
	if e != nil {
		t.Fatal(e)
	}
	p.ActiveStorageID = first.ID
	p.StorageSelectionSet = true
	if e = m.opts.Store.SaveBackupPolicy(ctx, p); e != nil {
		t.Fatal(e)
	}
	in.Prefix = "fresh-replacement"
	fresh, e := m.SaveStorage(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	if fresh.Revision != oldSecond.Revision {
		t.Fatal("fixture did not collide", fresh.Revision, oldSecond.Revision)
	}
	// A trusted external row needs the old configuration, even though it never
	// claimed a complete remote object. Its revision can be safely reassigned.
	id := domain.NewID()
	now := time.Now().UTC()
	r := Run{ID: id, RequestID: "external-revision-test", State: "interrupted", StorageID: oldSecond.ID, RemoteState: "pending", LocalState: "pending", CreatedAt: now, StartedAt: now}
	if e = m.writeCatalog(r); e != nil {
		t.Fatal(e)
	}
	freezeForTest(t, m)
	if e = m.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	imported, _, e := m.opts.Store.BackupStorage(ctx, oldSecond.ID)
	if e != nil || imported.Revision <= fresh.Revision {
		t.Fatal("old revision collided", imported, e)
	}
	active, _, e := m.opts.Store.BackupStorage(ctx, "")
	if e != nil || active.ID != fresh.ID {
		t.Fatal("import changed activity", active, e)
	}
	p, _ = m.opts.Store.BackupPolicy(ctx)
	if p.ReconciliationRequired {
		t.Fatal("revision conflict froze dataset")
	}
	// A later full database snapshot must preserve the explicit selection even
	// though reconciliation assigned a larger revision to an older config.
	newRun, e := m.Create(ctx, "active-selection-new-dump")
	if e != nil {
		t.Fatal(e)
	}
	newRun = waitRun(t, m, newRun.ID)
	validated, e := backup.Validate(ctx, newRun.LocalPath, identity, filepath.Join(t.TempDir(), "validate"))
	if e != nil {
		t.Fatal(e)
	}
	defer validated.Close()
	dump, e := store.OpenExisting(validated.DBPath)
	if e != nil {
		t.Fatal(e)
	}
	defer dump.Close()
	selected, _, e := dump.BackupStorage(ctx, "")
	if e != nil || selected.ID != fresh.ID {
		t.Fatal("new snapshot selected imported old revision", selected, e)
	}
}

func TestLegacyDetachedCatalogIsPreservedAsWarning(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()
	id := domain.NewID()
	now := time.Now().UTC()
	entry := catalogEntry{Run: Run{ID: id, RequestID: "detached", State: "failed", CreatedAt: now, StartedAt: now}}
	if e := durablejson.Write(filepath.Join(m.opts.BackupDir, "catalog", id+".json"), entry); e != nil {
		t.Fatal(e)
	}
	freezeForTest(t, m)
	if e := m.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	p, _ := m.opts.Store.BackupPolicy(ctx)
	if p.ReconciliationRequired || len(p.ReconciliationWarnings) != 1 {
		t.Fatal(p)
	}
	if _, e := m.Record(ctx, id); e == nil {
		t.Fatal("unowned legacy file imported")
	}
	if m.opts.BackupDir != operationlock.BackupRoot(m.opts.DataDir) {
		t.Fatal("unexpected instance namespace")
	}
}
