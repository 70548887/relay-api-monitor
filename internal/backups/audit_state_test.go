package backups

import (
	"context"
	"errors"
	"os"
	"relaylab/internal/domain"
	"relaylab/internal/operationlock"
	"testing"
	"time"
)

func auditIdle(t *testing.T, m *Manager) {
	t.Helper()
	for i := 0; i < 500; i++ {
		m.mu.Lock()
		active := m.activeID
		m.mu.Unlock()
		if active == "" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("backup did not release its operation")
}

func TestAuditPermanentArchiveSurvivesInterruptedFinalCommit(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()
	r, e := m.Create(ctx, "audit-permanent-crash")
	if e != nil {
		t.Fatal(e)
	}
	r = waitRun(t, m, r.ID)
	auditIdle(t, m)
	r.State = "encrypting"
	r.MonthlyArchive = false
	r.ArchiveDates = nil
	if e = m.opts.Store.SaveBackupRun(ctx, r); e != nil {
		t.Fatal(e)
	}
	if _, e = m.opts.Store.ClaimBackupArchiveDates(ctx, r.ID, []string{"2026-10-01"}, true); e != nil {
		t.Fatal(e)
	}
	if !m.recoverInterrupted() {
		t.Fatal("recovery failed")
	}
	r, e = m.Record(ctx, r.ID)
	if e != nil || !r.MonthlyArchive || r.ArchiveRetainCount != 0 {
		t.Fatalf("permanent ownership lost: %+v %v", r, e)
	}
	if e = m.Delete(ctx, r.ID); !errors.Is(e, ErrProtected) {
		t.Fatalf("permanent backup deletion allowed: %v", e)
	}
	if e = m.Protect(ctx, r.ID, false); e != nil {
		t.Fatal(e)
	}
	if e = m.Delete(ctx, r.ID); e != nil {
		t.Fatal("explicit release failed", e)
	}
}

func TestAuditCompleteFileRecoveredWithOriginalMonthlyPolicy(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()
	original, e := m.opts.Store.BackupPolicy(ctx)
	if e != nil {
		t.Fatal(e)
	}
	original.MonthlyEnabled = true
	original.MonthlyDays = []int{1}
	r := publishBeforeFinalCommit(t, m, original, true)
	if !m.recoverInterrupted() {
		t.Fatal("recovery failed")
	}
	r, e = m.Record(ctx, r.ID)
	if e != nil || r.State != "succeeded" || r.LocalState != "verified" || r.SizeBytes == 0 || r.SHA256 == "" || !r.MonthlyArchive {
		t.Fatalf("completed file was not recovered %+v %v", r, e)
	}
	if _, e = m.DownloadPath(ctx, r.ID); e != nil {
		t.Fatal(e)
	}
}

func TestAuditHistoryOverviewAndRecoveryAreNotPageLimited(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()
	success, e := m.Create(ctx, "audit-history-success")
	if e != nil {
		t.Fatal(e)
	}
	success = waitRun(t, m, success.ID)
	auditIdle(t, m)
	old := success.CreatedAt.Add(-time.Hour)
	unfinished := Run{ID: domain.NewID(), RequestID: "audit-old-unfinished", State: "queued", CreatedAt: old, StartedAt: old}
	if _, _, e = m.opts.Store.CreateBackupRun(ctx, unfinished); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 1001; i++ {
		now := time.Now().UTC().Add(time.Duration(i) * time.Second)
		r := Run{ID: domain.NewID(), RequestID: domain.NewID(), State: "failed", CreatedAt: now, StartedAt: now}
		if _, _, e = m.opts.Store.CreateBackupRun(ctx, r); e != nil {
			t.Fatal(e)
		}
	}
	snap, e := m.Snapshot(ctx)
	if e != nil || snap.LastSuccess == nil || snap.LastSuccess.ID != success.ID || snap.Active == nil || snap.Active.ID != unfinished.ID {
		t.Fatalf("overview omitted old important records %+v %v", snap, e)
	}
	release, e := operationlock.Acquire(m.opts.DataDir)
	if e != nil {
		t.Fatal(e)
	}
	if m.recoverInterrupted() {
		t.Fatal("recovery reported complete while locked")
	}
	_ = release()
	if !m.recoverInterrupted() {
		t.Fatal("recovery failed after lock release")
	}
	r, e := m.Record(ctx, unfinished.ID)
	if e != nil || r.State != "interrupted" {
		t.Fatalf("old unfinished record not recovered %+v %v", r, e)
	}
}

func TestAuditRetentionPreviewMatchesPeriodicCleanup(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		r, e := m.Create(ctx, domain.NewID())
		if e != nil {
			t.Fatal(e)
		}
		r = waitRun(t, m, r.ID)
		if r.State != "succeeded" {
			t.Fatal(r)
		}
		auditIdle(t, m)
	}
	p, e := m.opts.Store.BackupPolicy(ctx)
	if e != nil {
		t.Fatal(e)
	}
	p.RetainCount = 1
	p.RetainDays = 0
	preview, e := m.PreviewRetention(ctx, p)
	if e != nil || preview.DeleteCount != 2 {
		t.Fatalf("preview %+v %v", preview, e)
	}
	if e = m.opts.Store.SaveBackupPolicy(ctx, p); e != nil {
		t.Fatal(e)
	}
	if !m.periodicCleanup(time.Now()) {
		t.Fatal("periodic cleanup did not execute")
	}
	runs, e := m.List(ctx, 100, 0)
	if e != nil || len(runs) != 1 {
		t.Fatalf("preview/cleanup disagreed %d %v", len(runs), e)
	}
	path, e := m.DownloadPath(ctx, runs[0].ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(path); e != nil {
		t.Fatal("last restore point was deleted", e)
	}
}

func TestAuditFiniteArchivePolicyChangesDoNotDowngradePermanent(t *testing.T) {
	now := time.Now().UTC()
	runs := []Run{}
	for i := 0; i < 5; i++ {
		runs = append(runs, Run{ID: domain.NewID(), State: "succeeded", LocalState: "verified", MonthlyArchive: true, ArchiveRetainCount: 10, StartedAt: now.Add(-time.Duration(i) * time.Hour)})
	}
	permanent := Run{ID: "permanent", State: "succeeded", LocalState: "verified", MonthlyArchive: true, ArchiveRetainCount: 0, StartedAt: now.Add(-24 * time.Hour)}
	runs = append(runs, permanent)
	p := domain.DefaultBackupSchedule()
	p.MonthlyEnabled = true
	p.MonthlyRetainCount = 2
	result := retentionCandidates(runs, p, now)
	if len(result) != 3 {
		t.Fatalf("new finite policy not applied: %+v", result)
	}
	for _, r := range result {
		if r.ID == permanent.ID {
			t.Fatal("permanent archive was downgraded")
		}
	}
}
