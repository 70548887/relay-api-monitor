package backups

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"relaylab/internal/domain"
	"relaylab/internal/runtimecontrol"
)

func freezeForTest(t *testing.T, m *Manager) {
	t.Helper()
	p, e := m.opts.Store.BackupPolicy(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	p.Enabled = false
	p.ReconciliationRequired = true
	if e = m.opts.Store.SaveBackupPolicy(context.Background(), p); e != nil {
		t.Fatal(e)
	}
}

func TestNewVolumeWithoutExternalCatalogKeepsMissingDatabaseCopiesFrozen(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()
	r, e := m.Create(ctx, "new-volume-local-test")
	if e != nil {
		t.Fatal(e)
	}
	r = waitRun(t, m, r.ID)
	path, e := m.DownloadPath(ctx, r.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(path); e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(filepath.Join(m.opts.BackupDir, "catalog", r.ID+".json")); e != nil {
		t.Fatal(e)
	}
	freezeForTest(t, m)
	if e = m.Reconcile(ctx); e == nil {
		t.Fatal("empty external catalog trusted stale SQLite verified state")
	}
	p, _ := m.opts.Store.BackupPolicy(ctx)
	if !p.ReconciliationRequired {
		t.Fatal("new volume cleanup was unfrozen")
	}
	actual, e := m.Record(ctx, r.ID)
	if e != nil || actual.LocalState != "missing" || actual.SHA256 != r.SHA256 {
		t.Fatalf("missing index was lost %+v %v", actual, e)
	}
}

func TestNewVolumeValidatesRemoteDatabaseCopiesBeforeUnfreezing(t *testing.T) {
	payload := []byte("encrypted cloud backup fixture")
	hash := sha256.Sum256(payload)
	var remoteOK atomic.Bool
	remoteOK.Store(true)
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			http.Error(w, "unexpected request", 400)
			return
		}
		if !remoteOK.Load() {
			http.Error(w, "forbidden", 403)
			return
		}
		w.Write(payload)
	}))
	defer fixture.Close()
	m, _ := newTestManager(t)
	u, _ := url.Parse(fixture.URL)
	m.opts.AllowedStorageHosts = []string{u.Hostname()}
	ctx := context.Background()
	v, e := m.SaveStorage(ctx, StorageInput{Endpoint: fixture.URL, Bucket: "relaylab-backups", Region: "auto", Prefix: "backups", AccessKeyID: "fixture", SecretAccessKey: "fixture-secret", ForcePathStyle: true})
	if e != nil {
		t.Fatal(e)
	}
	id := domain.NewID()
	now := time.Now().UTC()
	r := Run{ID: id, RequestID: id, State: "succeeded", CreatedAt: now, StartedAt: now, LocalState: "verified", LocalPath: filepath.Join(m.opts.BackupDir, "not-in-new-volume.zip.age"), RemoteState: "verified", StorageID: v.ID, ObjectKey: "backups/fixture/" + id + "/backup.zip.age", SHA256: hex.EncodeToString(hash[:]), SizeBytes: int64(len(payload))}
	if _, _, e = m.opts.Store.CreateBackupRun(ctx, r); e != nil {
		t.Fatal(e)
	}
	if e = m.opts.Store.SaveBackupRun(ctx, r); e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(filepath.Join(m.opts.BackupDir, "storages", v.ID+".json")); e != nil {
		t.Fatal(e)
	}
	freezeForTest(t, m)
	if e = m.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	actual, e := m.Record(ctx, r.ID)
	if e != nil || actual.LocalState != "missing" || actual.RemoteState != "verified" || actual.ObjectKey != r.ObjectKey {
		t.Fatalf("remote recovery %+v %v", actual, e)
	}
	p, _ := m.opts.Store.BackupPolicy(ctx)
	if p.ReconciliationRequired || p.Enabled {
		t.Fatal("verified remote point was not reconciled")
	}
	remoteOK.Store(false)
	freezeForTest(t, m)
	if e = m.Reconcile(ctx); e == nil {
		t.Fatal("unverifiable remote copy unfroze cleanup")
	}
	p, _ = m.opts.Store.BackupPolicy(ctx)
	if !p.ReconciliationRequired {
		t.Fatal("remote failure did not keep freeze")
	}
}

func TestInterruptedSnapshotWithoutCompletedCopyDoesNotInventRecoveryPoint(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()
	now := time.Now().UTC()
	id := domain.NewID()
	r := Run{ID: id, RequestID: id, State: "interrupted", CreatedAt: now, StartedAt: now, LocalState: "pending"}
	if _, _, e := m.opts.Store.CreateBackupRun(ctx, r); e != nil {
		t.Fatal(e)
	}
	freezeForTest(t, m)
	if e := m.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	p, _ := m.opts.Store.BackupPolicy(ctx)
	if p.ReconciliationRequired {
		t.Fatal("uncompleted snapshot blocked index reconciliation")
	}
	actual, e := m.Record(ctx, id)
	if e != nil || actual.LocalState == "verified" {
		t.Fatal("invented completed copy")
	}
}

func TestExplicitLocalLossAcknowledgementPreservesProtectedIndex(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()
	r, e := m.Create(ctx, "acknowledge-local-loss")
	if e != nil {
		t.Fatal(e)
	}
	r = waitRun(t, m, r.ID)
	if e = m.AcknowledgeMissing(ctx, r.ID); e == nil {
		t.Fatal("acknowledged an existing verified file")
	}
	r.MonthlyArchive = true
	r.ArchiveRetainCount = 0
	r.ArchiveDates = []string{"2026-10-01"}
	if _, e = m.opts.Store.ClaimBackupArchiveDates(ctx, r.ID, r.ArchiveDates, true); e != nil {
		t.Fatal(e)
	}
	if e = m.save(ctx, r); e != nil {
		t.Fatal(e)
	}
	path, e := m.DownloadPath(ctx, r.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(path); e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(filepath.Join(m.opts.BackupDir, "catalog", r.ID+".json")); e != nil {
		t.Fatal(e)
	}
	freezeForTest(t, m)
	if e = m.Reconcile(ctx); e == nil {
		t.Fatal("loss did not freeze automatically")
	}
	if e = m.AcknowledgeMissing(ctx, r.ID); e != nil {
		t.Fatal(e)
	}
	confirmed, e := m.Record(ctx, r.ID)
	if e != nil || confirmed.State != "unavailable" || !confirmed.Protected || !confirmed.MonthlyArchive || len(confirmed.ArchiveDates) != 1 {
		t.Fatalf("lost protected index %+v %v", confirmed, e)
	}
	if e = m.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	p, _ := m.opts.Store.BackupPolicy(ctx)
	if p.Enabled || p.ReconciliationRequired {
		t.Fatal("confirmation did not allow explicit reconciliation")
	}
	if _, e = m.Record(ctx, r.ID); e != nil {
		t.Fatal("acknowledgement deleted record")
	}
	if e = m.Delete(ctx, r.ID); e == nil {
		t.Fatal("acknowledged permanent index lost protection")
	}
	claimed, e := m.opts.Store.ClaimBackupArchiveDates(ctx, "different", []string{"2026-10-01"}, true)
	if e != nil || len(claimed) != 0 {
		t.Fatal("archived slot reused")
	}
}

func TestLossAcknowledgementCannotBypassCloudValidation(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()
	now := time.Now().UTC()
	r := Run{ID: domain.NewID(), RequestID: "cloud-ack-denied", State: "succeeded", LocalState: "missing", RemoteState: "verified", StorageID: "cloud-revision", CreatedAt: now, StartedAt: now}
	if _, _, e := m.opts.Store.CreateBackupRun(ctx, r); e != nil {
		t.Fatal(e)
	}
	// The JSON storage association suffices: a caller must never use explicit
	// local loss acknowledgement to waive any declared remote copy.
	if e := m.AcknowledgeMissing(ctx, r.ID); e == nil {
		t.Fatal("cloud verification bypassed")
	}
}
func TestMissingCatalogFileKeepsCleanupFrozen(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()
	r, e := m.Create(ctx, "missing-file-test")
	if e != nil {
		t.Fatal(e)
	}
	r = waitRun(t, m, r.ID)
	path, e := m.DownloadPath(ctx, r.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(path); e != nil {
		t.Fatal(e)
	}
	freezeForTest(t, m)
	if e = m.Reconcile(ctx); e == nil {
		t.Fatal("missing recovery point accepted")
	}
	p, _ := m.opts.Store.BackupPolicy(ctx)
	if !p.ReconciliationRequired {
		t.Fatal("cleanup unfrozen with missing file")
	}
}
func TestPermanentProtectionCanBeExplicitlyClearedWithoutSlotReuse(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()
	r, e := m.Create(ctx, "permanent-test")
	if e != nil {
		t.Fatal(e)
	}
	r = waitRun(t, m, r.ID)
	r.MonthlyArchive = true
	r.ArchiveRetainCount = 0
	r.ArchiveDates = []string{"2026-10-01"}
	if _, e = m.opts.Store.ClaimBackupArchiveDates(ctx, r.ID, r.ArchiveDates, true); e != nil {
		t.Fatal(e)
	}
	if e = m.save(ctx, r); e != nil {
		t.Fatal(e)
	}
	if e = m.Protect(ctx, r.ID, false); e != nil {
		t.Fatal(e)
	}
	if e = m.Delete(ctx, r.ID); e != nil {
		t.Fatal(e)
	}
	freezeForTest(t, m)
	if e = m.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	claimed, e := m.opts.Store.ClaimBackupArchiveDates(ctx, "another-backup", []string{"2026-10-01"}, true)
	if e != nil || len(claimed) != 0 {
		t.Fatalf("reused slot %v %v", claimed, e)
	}
	if _, e = m.Record(ctx, r.ID); e == nil {
		t.Fatal("revived tombstone")
	}
}
func TestWrongStorageCatalogAADRefusesReconciliation(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()
	id := domain.NewID()
	cipher, e := m.opts.Vault.EncryptBackupCredentials("different-storage", `{"access_key_id":"test","secret_access_key":"test"}`)
	if e != nil {
		t.Fatal(e)
	}
	entry := storageCatalog{Storage: Storage{ID: id, Revision: 1, Endpoint: "https://storage.example.com", Bucket: "relaylab-backups", CreatedAt: time.Now()}, Cipher: cipher}
	if e = runtimecontrol.WriteJSON(filepath.Join(m.opts.BackupDir, "storages", id+".json"), entry); e != nil {
		t.Fatal(e)
	}
	// Unreferenced catalogs from another restored dataset are ignored. The
	// invalid credential must still block when this database actually needs it.
	now := time.Now().UTC()
	if _, _, e = m.opts.Store.CreateBackupRun(ctx, Run{ID: domain.NewID(), RequestID: "wrong-aad-reference", State: "interrupted", StorageID: id, RemoteState: "pending", CreatedAt: now, StartedAt: now}); e != nil {
		t.Fatal(e)
	}
	freezeForTest(t, m)
	if e = m.Reconcile(ctx); e == nil {
		t.Fatal("accepted credentials for another storage")
	}
	p, _ := m.opts.Store.BackupPolicy(ctx)
	if !p.ReconciliationRequired {
		t.Fatal("lost freeze")
	}
	raw, _ := json.Marshal(entry.Storage)
	if string(raw) == "" {
		t.Fatal("unexpected empty JSON")
	}
}
