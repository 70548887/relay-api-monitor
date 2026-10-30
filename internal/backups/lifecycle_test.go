package backups

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestMaterializePinsIndependentCopyAcrossDeletion(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()
	r, e := m.Create(ctx, "materialize-test")
	if e != nil {
		t.Fatal(e)
	}
	r = waitRun(t, m, r.ID)
	if r.State != "succeeded" {
		t.Fatal(r)
	}
	copyPath, e := m.Materialize(ctx, r.ID)
	if e != nil {
		t.Fatal(e)
	}
	defer os.Remove(copyPath)
	original, e := m.DownloadPath(ctx, r.ID)
	if e != nil {
		t.Fatal(e)
	}
	if original == copyPath {
		t.Fatal("preflight points at mutable backup path")
	}
	if e = m.Delete(ctx, r.ID); e != nil {
		t.Fatal(e)
	}
	hash, n, e := hashFile(copyPath)
	if e != nil || hash != r.SHA256 || n != r.SizeBytes {
		t.Fatalf("preflight copy lost %v", e)
	}
	if e = m.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = m.Record(ctx, r.ID); e == nil {
		t.Fatal("tombstone revived deleted backup")
	}
}
func TestScheduleSaveStableAndRestartDoesNotReplayMissedPeriods(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()
	p, _ := m.opts.Store.BackupPolicy(ctx)
	p.Enabled = true
	p.Cron = "*/5 * * * *"
	p, e := m.SaveSchedule(ctx, p)
	if e != nil {
		t.Fatal(e)
	}
	first := *p.NextRunAt
	p, e = m.SaveSchedule(ctx, p)
	if e != nil || !p.NextRunAt.Equal(first) {
		t.Fatalf("save reset schedule %+v %v", p, e)
	}
	old := time.Now().UTC().Add(-24 * time.Hour)
	p.NextRunAt = &old
	if e = m.opts.Store.SaveBackupPolicy(ctx, p); e != nil {
		t.Fatal(e)
	}
	m.recoverInterrupted()
	p, _ = m.opts.Store.BackupPolicy(ctx)
	if p.NextRunAt == nil || !p.NextRunAt.After(time.Now().UTC()) {
		t.Fatal("restart retained old slot")
	}
	records, _ := m.List(ctx, 100, 0)
	if len(records) != 0 {
		t.Fatal("restart enqueued historical slots")
	}
}
