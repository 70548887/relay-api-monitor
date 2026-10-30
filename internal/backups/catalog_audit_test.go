package backups

import (
	"context"
	"errors"
	"path/filepath"
	"relaylab/internal/backup"
	"relaylab/internal/domain"
	"testing"
	"time"
)

func TestReconcilePublishedCatalogRestoresOriginalPermanentArchive(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()
	policy, e := m.opts.Store.BackupPolicy(ctx)
	if e != nil {
		t.Fatal(e)
	}
	policy.MonthlyEnabled = true
	policy.MonthlyDays = []int{1}
	policy.MonthlyRetainCount = 0
	id := domain.NewID()
	now := time.Now().UTC()
	run := Run{ID: id, RequestID: "catalog-window-" + id, State: "encrypting", Trigger: "scheduled", CreatedAt: now, StartedAt: now, LocalState: "pending", RecipientID: policy.RecipientID, PolicySnapshot: &policy, FileName: "relaylab_fixture_" + id + ".zip.age"}
	run.LocalPath = filepath.Join(m.opts.BackupDir, run.FileName)
	stale := run
	stale.PolicySnapshot = nil
	if _, _, e = m.opts.Store.CreateBackupRun(ctx, run); e != nil {
		t.Fatal(e)
	}
	e = backup.CreateFromStore(ctx, m.opts.Store, m.opts.DataDir, run.LocalPath, backup.Options{Recipient: policy.Recipient, Schema: 4, BackupID: id, OnReady: func(member backup.Member) error {
		run.SHA256 = member.SHA256
		run.SizeBytes = member.Size
		return m.save(ctx, run)
	}})
	if e != nil {
		t.Fatal(e)
	}
	// Restore SQLite from a snapshot taken before publication. Its queue was
	// terminated, while the protected external catalog retains the ready digest.
	stale.State = "interrupted"
	if e = m.opts.Store.SaveBackupRun(ctx, stale); e != nil {
		t.Fatal(e)
	}
	policy.ReconciliationRequired = true
	if e = m.opts.Store.SaveBackupPolicy(ctx, policy); e != nil {
		t.Fatal(e)
	}
	if e = m.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	restored, e := m.Record(ctx, id)
	if e != nil {
		t.Fatal(e)
	}
	if restored.State != "succeeded" || restored.LocalState != "verified" || !restored.MonthlyArchive || restored.ArchiveRetainCount != 0 || len(restored.ArchiveDates) != 1 {
		t.Fatalf("archive protection lost: %+v", restored)
	}
	if e = m.Delete(ctx, id); !errors.Is(e, ErrProtected) {
		t.Fatal("permanent archive deletion was not blocked", e)
	}
}
