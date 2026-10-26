package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"relaylab/internal/domain"
)

func fixture(t *testing.T) (*Store, context.Context, domain.Channel, domain.Model) {
	t.Helper()
	ctx := context.Background()
	s, e := Open(filepath.Join(t.TempDir(), "test.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	p := domain.Platform{ID: "platform", Name: "test", Public: true}
	if e = s.SavePlatform(ctx, p); e != nil {
		t.Fatal(e)
	}
	c := domain.Channel{ID: "channel", PlatformID: p.ID, Name: "test", Enabled: true, Public: true, EncryptedKey: "opaque-secret"}
	if e = s.SaveChannel(ctx, c); e != nil {
		t.Fatal(e)
	}
	m := domain.Model{ID: "model", ChannelID: c.ID, UpstreamID: "upstream", DisplayName: "Custom name", Source: "manual", Public: true, ProbeEnabled: true, ArtifactChinese: true, ArtifactEnglish: true, MaxTokens: 4096}
	if e = s.SaveModel(ctx, m); e != nil {
		t.Fatal(e)
	}
	return s, ctx, c, m
}
func claimed(t *testing.T, s *Store, ctx context.Context, m domain.Model, kind, template string) domain.Job {
	t.Helper()
	j, e := s.EnqueueJob(ctx, domain.Job{ModelID: m.ID, Kind: kind, TemplateID: template})
	if e != nil {
		t.Fatal(e)
	}
	p, e := s.ClaimJob(ctx, kind, "owner", nowUTC())
	if e != nil || p == nil {
		t.Fatalf("claim: %v %#v", e, p)
	}
	if p.ID != j.ID {
		t.Fatal("wrong job claimed")
	}
	return *p
}
func dispatch(t *testing.T, s *Store, ctx context.Context, j domain.Job) domain.Job {
	t.Helper()
	if e := s.MarkDispatching(ctx, j.ID, j.LeaseToken, nowUTC().Add(time.Minute), "snapshot"); e != nil {
		t.Fatal(e)
	}
	p, e := s.GetJob(ctx, j.ID)
	if e != nil {
		t.Fatal(e)
	}
	return *p
}

func TestSyncModelsPreservesConfigurationAndRollsBack(t *testing.T) {
	s, ctx, c, m := fixture(t)
	if _, e := s.DB.ExecContext(ctx, "CREATE TRIGGER sync_failure BEFORE INSERT ON models WHEN NEW.upstream_id='new' BEGIN SELECT RAISE(ABORT,'test rejected insert'); END"); e != nil {
		t.Fatal(e)
	}
	if e := s.SyncModels(ctx, c.ID, []string{"new"}); e == nil {
		t.Fatal("expected failing sync")
	}
	got, e := s.GetModel(ctx, m.ID)
	if e != nil {
		t.Fatal(e)
	}
	if got.Missing || got.DisplayName != m.DisplayName || !got.Public || !got.ProbeEnabled {
		t.Fatalf("failed sync changed model: %#v", got)
	}
	ch, e := s.GetChannel(ctx, c.ID)
	if e != nil {
		t.Fatal(e)
	}
	if ch.LastSyncAt != nil || ch.EncryptedKey != c.EncryptedKey {
		t.Fatal("failed sync changed channel")
	}
	if _, e = s.DB.ExecContext(ctx, "DROP TRIGGER sync_failure"); e != nil {
		t.Fatal(e)
	}
	if e = s.SyncModels(ctx, c.ID, []string{"upstream", "new", "new"}); e != nil {
		t.Fatal(e)
	}
	all, e := s.ListModels(ctx, c.ID)
	if e != nil {
		t.Fatal(e)
	}
	if len(all) != 2 {
		t.Fatalf("models=%d", len(all))
	}
	for _, v := range all {
		if v.ID == m.ID {
			if v.DisplayName != m.DisplayName || !v.ProbeEnabled || !v.Public || v.MaxTokens != m.MaxTokens {
				t.Fatal("manual fields overwritten")
			}
		} else if v.Public || v.ProbeEnabled || v.ArtifactChinese || v.ArtifactEnglish {
			t.Fatal("discovered model enabled by default")
		}
	}
	ch, e = s.GetChannel(ctx, c.ID)
	if e != nil {
		t.Fatal(e)
	}
	if ch.LastSyncAt == nil || ch.EncryptedKey != c.EncryptedKey {
		t.Fatal("sync did not preserve secret")
	}
	ch.EncryptedKey = ""
	ch.Name = "renamed"
	if e = s.SaveChannel(ctx, *ch); e != nil {
		t.Fatal(e)
	}
	ch, e = s.GetChannel(ctx, c.ID)
	if e != nil || ch.EncryptedKey != c.EncryptedKey {
		t.Fatal("editing channel lost key", e)
	}
}

func TestJobsEnforceSlotsAndModelKindExclusion(t *testing.T) {
	s, ctx, _, m := fixture(t)
	at := nowUTC()
	j, e := s.EnqueueJob(ctx, domain.Job{ScheduleID: "schedule", ScheduledAt: at, ModelID: m.ID, Kind: "probe"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.EnqueueJob(ctx, domain.Job{ModelID: m.ID, Kind: "probe"}); !errors.Is(e, ErrConflict) {
		t.Fatalf("active duplicate=%v", e)
	}
	if _, e = s.EnqueueJob(ctx, domain.Job{ModelID: m.ID, Kind: "artifact", TemplateID: "cn_html"}); e != nil {
		t.Fatalf("different kind blocked: %v", e)
	}
	if e = s.RequestCancel(ctx, j.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.EnqueueJob(ctx, domain.Job{ScheduleID: "schedule", ScheduledAt: at, ModelID: m.ID, Kind: "probe"}); !errors.Is(e, ErrConflict) {
		t.Fatalf("duplicate scheduled slot=%v", e)
	}
	if _, e = s.EnqueueJob(ctx, domain.Job{ScheduleID: "schedule", ScheduledAt: at.Add(time.Second), ModelID: m.ID, Kind: "probe"}); e != nil {
		t.Fatal(e)
	}
}

func TestRecoveryDistinguishesClaimedAndDispatched(t *testing.T) {
	s, ctx, _, m := fixture(t)
	j := claimed(t, s, ctx, m, "probe", "")
	if e := s.RecoverJobs(ctx, nowUTC().Add(time.Minute)); e != nil {
		t.Fatal(e)
	}
	p, e := s.GetJob(ctx, j.ID)
	if e != nil {
		t.Fatal(e)
	}
	if p.State != "queued" || p.LeaseToken != "" {
		t.Fatalf("claimed recovery=%#v", p)
	}
	reclaim, e := s.ClaimJob(ctx, "probe", "second", nowUTC())
	if e != nil || reclaim == nil {
		t.Fatal(e)
	}
	j.State = "failed"
	if e = s.FinishJob(ctx, j, nil, nil); !errors.Is(e, ErrConflict) {
		t.Fatalf("stale owner accepted: %v", e)
	}
	d := dispatch(t, s, ctx, *reclaim)
	if e = s.RecoverJobs(ctx, nowUTC().Add(2*time.Minute)); e != nil {
		t.Fatal(e)
	}
	p, e = s.GetJob(ctx, d.ID)
	if e != nil {
		t.Fatal(e)
	}
	if p.State != "uncertain" {
		t.Fatalf("dispatch recovery=%s", p.State)
	}
	if _, e = s.EnqueueJob(ctx, domain.Job{ModelID: m.ID, Kind: "probe"}); !errors.Is(e, ErrConflict) {
		t.Fatalf("uncertain grace ignored: %v", e)
	}
	d.State = "succeeded"
	if e = s.FinishJob(ctx, d, &domain.ProbeResult{Status: "ok"}, nil); !errors.Is(e, ErrConflict) {
		t.Fatalf("late completion accepted: %v", e)
	}
	if _, e = s.DB.ExecContext(ctx, "UPDATE jobs SET deadline=? WHERE id=?", ts(nowUTC().Add(-31*time.Second)), d.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.EnqueueJob(ctx, domain.Job{ModelID: m.ID, Kind: "probe"}); e != nil {
		t.Fatalf("grace elapsed still blocks: %v", e)
	}
}

func TestCompletionFencingAndCancellation(t *testing.T) {
	s, ctx, _, m := fixture(t)
	j := dispatch(t, s, ctx, claimed(t, s, ctx, m, "probe", ""))
	j.State = "succeeded"
	wrong := j
	wrong.LeaseToken = "wrong"
	if e := s.FinishJob(ctx, wrong, &domain.ProbeResult{Status: "ok"}, nil); !errors.Is(e, ErrConflict) {
		t.Fatalf("wrong token accepted: %v", e)
	}
	if e := s.RequestCancel(ctx, j.ID); e != nil {
		t.Fatal(e)
	}
	if e := s.FinishJob(ctx, j, &domain.ProbeResult{Status: "ok"}, nil); e != nil {
		t.Fatal(e)
	}
	p, e := s.GetJob(ctx, j.ID)
	if e != nil || p.State != "canceled" {
		t.Fatal("cancel not retained", e)
	}
	results, e := s.ListProbeResults(ctx, m.ID, time.Time{})
	if e != nil || len(results) != 0 {
		t.Fatal("cancellation counted as monitoring result", e)
	}
	if e = s.FinishJob(ctx, j, nil, nil); !errors.Is(e, ErrConflict) {
		t.Fatal("terminal overwritten", e)
	}
}

func TestArtifactRetentionAndAtomicCompletion(t *testing.T) {
	s, ctx, _, m := fixture(t)
	cfg, err := s.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ArtifactHistoryScope = "model"
	if err = s.SaveSettings(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	var last domain.Job
	var originalID string
	for i := 0; i < 10; i++ {
		j := dispatch(t, s, ctx, claimed(t, s, ctx, m, "artifact", "cn_html"))
		j.State = "succeeded"
		a := &domain.Artifact{Code: "<html>ok</html>", CreatedAt: nowUTC().Add(time.Duration(i) * time.Second)}
		if e := s.FinishJob(ctx, j, nil, a); e != nil {
			t.Fatal(e)
		}
		last = j
		originalID = a.ID
	}
	arts, e := s.ListArtifacts(ctx, m.ID)
	if e != nil {
		t.Fatal(e)
	}
	if len(arts) != 8 {
		t.Fatalf("retained %d", len(arts))
	}
	j := dispatch(t, s, ctx, claimed(t, s, ctx, m, "artifact", "cn_html"))
	j.State = "succeeded"
	if e = s.FinishJob(ctx, j, nil, &domain.Artifact{ID: originalID, Code: "duplicate"}); e == nil {
		t.Fatal("duplicate artifact should fail")
	}
	current, e := s.GetJob(ctx, j.ID)
	if e != nil || current.State != "dispatching" {
		t.Fatal("failed result write committed job", e)
	}
	if e = s.RequestCancel(ctx, j.ID); e != nil {
		t.Fatal(e)
	}
	j.State = "canceled"
	if e = s.FinishJob(ctx, j, nil, nil); e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB.ExecContext(ctx, "UPDATE jobs SET created_at=? WHERE id=?", ts(nowUTC().Add(-40*24*time.Hour)), last.ID); e != nil {
		t.Fatal(e)
	}
	if e = s.Cleanup(ctx, nowUTC()); e != nil {
		t.Fatal(e)
	}
	if _, e = s.GetJob(ctx, last.ID); !errors.Is(e, sql.ErrNoRows) {
		t.Fatalf("old job retained: %v", e)
	}
	if _, e = s.GetArtifact(ctx, originalID); e != nil {
		t.Fatalf("artifact lost when audit job cleaned: %v", e)
	}
}

func TestWorkerLeaseAndPasswordSessionInvalidation(t *testing.T) {
	s, ctx, _, _ := fixture(t)
	now := nowUTC()
	ok, e := s.AcquireWorkerLease(ctx, "one", now)
	if e != nil || !ok {
		t.Fatal(e)
	}
	ok, e = s.AcquireWorkerLease(ctx, "two", now.Add(time.Second))
	if e != nil || ok {
		t.Fatal("second worker accepted", e)
	}
	if e = s.RenewWorkerLease(ctx, "two", now); !errors.Is(e, ErrConflict) {
		t.Fatal("wrong owner renewed", e)
	}
	ok, e = s.AcquireWorkerLease(ctx, "two", now.Add(time.Minute))
	if e != nil || !ok {
		t.Fatal("expired lease blocked", e)
	}
	if e = s.RenewWorkerLease(ctx, "one", now.Add(time.Minute)); !errors.Is(e, ErrConflict) {
		t.Fatal("old owner renewed", e)
	}
	if e = s.ReleaseWorkerLease(ctx, "one"); e != nil {
		t.Fatal(e)
	}
	health, e := s.WorkerHealth(ctx)
	if e != nil || health.Owner != "two" {
		t.Fatal("stale release removed lease", e)
	}
	if e = s.EnsureAdmin(ctx, "admin", "hash"); e != nil {
		t.Fatal(e)
	}
	if e = s.EnsureAdmin(ctx, "other", "hash"); !errors.Is(e, ErrConflict) {
		t.Fatal("second initialization accepted", e)
	}
	admin, e := s.GetAdminByUsername(ctx, "admin")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.SaveSession(ctx, domain.Session{AdminID: admin.ID, TokenHash: "token", CSRFToken: "csrf", ExpiresAt: now.Add(time.Hour)}); e != nil {
		t.Fatal(e)
	}
	if e = s.UpdatePassword(ctx, admin.ID, "new"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.GetSession(ctx, "token"); !errors.Is(e, sql.ErrNoRows) {
		t.Fatal("password update retained session", e)
	}
}

func TestBackupContainsConsistentDatabaseAndRefusesOverwrite(t *testing.T) {
	s, ctx, c, m := fixture(t)
	target := filepath.Join(t.TempDir(), "backup.db")
	if e := s.Backup(ctx, target); e != nil {
		t.Fatal(e)
	}
	copy, e := OpenExisting(target)
	if e != nil {
		t.Fatal(e)
	}
	defer copy.Close()
	ch, e := copy.GetChannel(ctx, c.ID)
	if e != nil || ch.EncryptedKey != c.EncryptedKey {
		t.Fatal("backup lost secret", e)
	}
	if _, e = copy.GetModel(ctx, m.ID); e != nil {
		t.Fatal(e)
	}
	if e = s.Backup(ctx, target); e == nil {
		t.Fatal("backup overwrite accepted")
	}
	if _, e = OpenExisting(filepath.Join(t.TempDir(), "absent.db")); !errors.Is(e, os.ErrNotExist) {
		t.Fatalf("worker created database: %v", e)
	}
}

func TestSchedulesCoalesceMissedTicksAndReconcileFlags(t *testing.T) {
	s, ctx, _, m := fixture(t)
	if e := s.ReconcileSchedules(ctx, m); e != nil {
		t.Fatal(e)
	}
	all, e := s.ListSchedules(ctx, m.ID)
	if e != nil || len(all) != 3 {
		t.Fatalf("schedules=%d %v", len(all), e)
	}
	now := nowUTC()
	for _, v := range all {
		v.NextRunAt = now.Add(-time.Hour)
		if e = s.SaveSchedule(ctx, v); e != nil {
			t.Fatal(e)
		}
	}
	n, e := s.TickSchedules(ctx, now)
	if e != nil || n != 2 {
		t.Fatalf("tick=%d %v", n, e)
	}
	n, e = s.TickSchedules(ctx, now)
	if e != nil || n != 0 {
		t.Fatalf("replayed current slot=%d %v", n, e)
	}
	all, e = s.ListSchedules(ctx, m.ID)
	if e != nil {
		t.Fatal(e)
	}
	waitingSibling := 0
	for _, v := range all {
		if !v.NextRunAt.After(now) {
			if v.Kind != "artifact" {
				t.Fatal("historical probe backlog retained")
			}
			waitingSibling++
		}
	}
	if waitingSibling != 1 {
		t.Fatalf("expected one deferred template, got %d", waitingSibling)
	}
	m.Archived = true
	if e = s.SaveModel(ctx, m); e != nil {
		t.Fatal(e)
	}
	if e = s.ReconcileSchedules(ctx, m); e != nil {
		t.Fatal(e)
	}
	all, e = s.ListSchedules(ctx, m.ID)
	if e != nil {
		t.Fatal(e)
	}
	for _, v := range all {
		if v.Enabled {
			t.Fatal("archived model still scheduled")
		}
	}
}

func TestConnectionPragmasSurviveReconnectAndUnicodePath(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "中文目录", "监控.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.DB.SetMaxIdleConns(0)
	for _, check := range []struct {
		query string
		want  int
	}{{"PRAGMA foreign_keys", 1}, {"PRAGMA busy_timeout", 5000}} {
		var got int
		if err = s.DB.QueryRow(check.query).Scan(&got); err != nil || got != check.want {
			t.Fatalf("%s = %d, %v", check.query, got, err)
		}
	}
	var mode string
	if err = s.DB.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal mode=%q %v", mode, err)
	}
	if err = s.SaveModel(context.Background(), domain.Model{ID: "orphan", ChannelID: "absent", UpstreamID: "x"}); err == nil {
		t.Fatal("foreign key enforcement lost after reconnect")
	}
}

func TestWorkerLeaseVisibleAcrossDatabaseHandles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.db")
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
	now := nowUTC()
	if ok, err := a.AcquireWorkerLease(ctx, "first", now); err != nil || !ok {
		t.Fatal("first lease", ok, err)
	}
	if ok, err := b.AcquireWorkerLease(ctx, "second", now.Add(time.Second)); err != nil || ok {
		t.Fatal("concurrent worker admitted", ok, err)
	}
	if ok, err := b.AcquireWorkerLease(ctx, "second", now.Add(time.Minute)); err != nil || !ok {
		t.Fatal("takeover after expiry", ok, err)
	}
	if err := a.RenewWorkerLease(ctx, "first", now.Add(time.Minute)); !errors.Is(err, ErrConflict) {
		t.Fatal("stale process renewed", err)
	}
}
