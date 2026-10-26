package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"relaylab/internal/domain"
)

func TestWriteTransactionSerializesIndependentHandles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "writers.db")
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	read := make(chan struct{})
	release := make(chan struct{})
	first := make(chan error, 1)
	go func() {
		first <- a.transaction(ctx, func(tx *sql.Tx) error {
			var raw string
			if err := tx.QueryRowContext(ctx, "SELECT data FROM settings WHERE id=1").Scan(&raw); err != nil {
				return err
			}
			close(read)
			<-release
			_, err := tx.ExecContext(ctx, "UPDATE settings SET data=? WHERE id=1", raw)
			return err
		})
	}()
	select {
	case <-read:
	case err := <-first:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	second := make(chan error, 1)
	go func() {
		cfg, e := b.GetSettings(ctx)
		if e == nil {
			cfg.ProbeConcurrency++
			e = b.SaveSettings(ctx, cfg)
		}
		second <- e
	}()
	// A deferred transaction lets B commit here, making A's read snapshot stale.
	time.Sleep(100 * time.Millisecond)
	close(release)
	if err = <-first; err != nil {
		t.Fatalf("read/modify/write lost its snapshot across handles: %v", err)
	}
	if err = <-second; err != nil {
		t.Fatalf("second writer: %v", err)
	}
}

func TestWorkerLeaseTakeoverFencesJobMutations(t *testing.T) {
	for _, operation := range []string{"dispatch", "renew", "finish", "claim"} {
		t.Run(operation, func(t *testing.T) {
			s, ctx, _, m := fixture(t)
			j := claimed(t, s, ctx, m, "probe", "")
			if ok, err := s.AcquireWorkerLease(ctx, j.Owner, nowUTC()); err != nil || !ok {
				t.Fatal(ok, err)
			}
			if operation != "dispatch" && operation != "claim" {
				j = dispatch(t, s, ctx, j)
			}
			if ok, err := s.AcquireWorkerLease(ctx, "replacement", nowUTC().Add(time.Minute)); err != nil || !ok {
				t.Fatal(ok, err)
			}
			var err error
			switch operation {
			case "dispatch":
				err = s.MarkDispatching(ctx, j.ID, j.LeaseToken, nowUTC().Add(time.Minute), "snapshot")
			case "renew":
				err = s.RenewJob(ctx, j.ID, j.LeaseToken, nowUTC().Add(time.Minute))
			case "finish":
				j.State = "succeeded"
				err = s.FinishJob(ctx, j, &domain.ProbeResult{Status: "ok"}, nil)
			case "claim":
				_, err = s.ClaimJob(ctx, "artifact", j.Owner, nowUTC())
			}
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("old process %s was accepted: %v", operation, err)
			}
			results, err := s.ListProbeResults(ctx, m.ID, time.Time{})
			if err != nil || len(results) != 0 {
				t.Fatal("stale process committed statistics", results, err)
			}
		})
	}
}

func TestDiscoveryRevisionFenceAndMissingModelReappearance(t *testing.T) {
	s, ctx, c, m := fixture(t)
	before, err := s.GetChannel(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	changed := *before
	changed.BaseURL = "http://localhost/new-configuration"
	if err = s.SaveChannelWithSchedules(ctx, changed); err != nil {
		t.Fatal(err)
	}
	if err = s.SyncModels(ctx, c.ID, []string{"stale-model"}, before.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale list committed: %v", err)
	}
	if err = s.SetChannelSyncError(ctx, c.ID, "stale failure", before.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale health committed: %v", err)
	}
	current, err := s.GetChannel(ctx, c.ID)
	if err != nil || current.SyncError != "" || current.LastSyncAt != nil {
		t.Fatal(current, err)
	}
	if err = s.SyncModels(ctx, c.ID, nil, current.Revision); err != nil {
		t.Fatal(err)
	}
	absent, err := s.GetModel(ctx, m.ID)
	if err != nil || !absent.Missing || !absent.ProbeEnabled {
		t.Fatal("missing changed explicit flags", absent, err)
	}
	plans, err := s.ListSchedules(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range plans {
		if p.Enabled {
			t.Fatal("missing model still scheduled", p)
		}
	}
	if err = s.SyncModels(ctx, c.ID, []string{m.UpstreamID}, current.Revision); err != nil {
		t.Fatal(err)
	}
	reappeared, err := s.GetModel(ctx, m.ID)
	if err != nil || reappeared.Missing || reappeared.ID != m.ID || reappeared.MaxTokens != m.MaxTokens {
		t.Fatal("reappeared model lost configuration", reappeared, err)
	}
	plans, err = s.ListSchedules(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range plans {
		if !p.Enabled {
			t.Fatal("reappeared model did not resume configured plan", p)
		}
	}
	latest, err := s.GetChannel(ctx, c.ID)
	if err != nil || latest.Revision != current.Revision {
		t.Fatal("discovery changed config revision", latest, err)
	}
}

func TestDisablingScheduleCancelsOldWorkAcrossStates(t *testing.T) {
	for _, state := range []string{"queued", "claimed", "dispatching"} {
		t.Run(state, func(t *testing.T) {
			s, ctx, _, m := fixture(t)
			if err := s.SaveModelWithSchedules(ctx, m); err != nil {
				t.Fatal(err)
			}
			plans, err := s.ListSchedules(ctx, m.ID)
			if err != nil {
				t.Fatal(err)
			}
			var plan domain.Schedule
			for _, p := range plans {
				if p.Kind == "probe" {
					plan = p
				}
			}
			j, err := s.EnqueueJob(ctx, domain.Job{ModelID: m.ID, ScheduleID: plan.ID, Kind: "probe"})
			if err != nil {
				t.Fatal(err)
			}
			if state != "queued" {
				v, e := s.ClaimJob(ctx, "probe", "owner", nowUTC())
				if e != nil || v == nil {
					t.Fatal(v, e)
				}
				j = *v
			}
			if state == "dispatching" {
				j = dispatch(t, s, ctx, j)
			}
			m.ProbeEnabled = false
			if err = s.SaveModelWithSchedules(ctx, m); err != nil {
				t.Fatal(err)
			}
			m.ProbeEnabled = true
			if err = s.SaveModelWithSchedules(ctx, m); err != nil {
				t.Fatal(err)
			}
			got, err := s.GetJob(ctx, j.ID)
			if err != nil || !got.CancelRequested || state == "queued" && got.State != "canceled" {
				t.Fatal("old scheduled work survived disable/re-enable", got, err)
			}
			if state != "queued" {
				j.State = "succeeded"
				if err = s.FinishJob(ctx, j, &domain.ProbeResult{Status: "ok"}, nil); err != nil {
					t.Fatal(err)
				}
				got, err = s.GetJob(ctx, j.ID)
				if err != nil || got.State != "canceled" {
					t.Fatal(got, err)
				}
			}
		})
	}
}

func TestPlatformArchiveReconcilesAndCancelsInSameTransaction(t *testing.T) {
	s, ctx, c, m := fixture(t)
	if err := s.SaveModelWithSchedules(ctx, m); err != nil {
		t.Fatal(err)
	}
	plans, err := s.ListSchedules(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	var plan domain.Schedule
	for _, p := range plans {
		if p.Kind == "probe" {
			plan = p
		}
	}
	j, err := s.EnqueueJob(ctx, domain.Job{ModelID: m.ID, ScheduleID: plan.ID, Kind: "probe"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.GetPlatform(ctx, c.PlatformID)
	if err != nil {
		t.Fatal(err)
	}
	p.Archived = true
	if err = s.SavePlatform(ctx, *p); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetJob(ctx, j.ID)
	if err != nil || got.State != "canceled" {
		t.Fatal(got, err)
	}
	plans, err = s.ListSchedules(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range plans {
		if v.Enabled {
			t.Fatal("archived platform retained enabled schedule", v)
		}
	}
	p.Archived = false
	if err = s.SavePlatform(ctx, *p); err != nil {
		t.Fatal(err)
	}
	plans, err = s.ListSchedules(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range plans {
		if !v.Enabled {
			t.Fatal("restored platform did not resume configured schedule", v)
		}
	}
}

func TestJobQueriesAndCancellationIncludeOldRowsBeyondListLimit(t *testing.T) {
	s, ctx, c, m := fixture(t)
	old := nowUTC().Add(-24 * time.Hour)
	j, err := s.EnqueueJob(ctx, domain.Job{ID: "old-pending", ModelID: m.ID, Kind: "artifact", TemplateID: "cn_html", CreatedAt: old})
	if err != nil {
		t.Fatal(err)
	}
	err = s.transaction(ctx, func(tx *sql.Tx) error {
		for i := 0; i < 10001; i++ {
			v := domain.Job{ID: fmt.Sprintf("history-%05d", i), ModelID: m.ID, ChannelID: c.ID, Kind: "probe", State: "succeeded", CreatedAt: old.Add(time.Duration(i+1) * time.Second), ScheduledAt: old}
			if e := saveJob(ctx, tx, v, true); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	counts, err := s.JobStateCounts(ctx)
	if err != nil || counts["queued"] != 1 || counts["succeeded"] != 10001 {
		t.Fatal(counts, err)
	}
	latest, err := s.LatestArtifactAttempts(ctx, m.ID, "cn_html")
	if err != nil || len(latest) != 1 || latest[0].ID != j.ID {
		t.Fatal("old artifact attempt missing", latest, err)
	}
	if err = s.CancelJobsForChannel(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetJob(ctx, j.ID)
	if err != nil || got.State != "canceled" {
		t.Fatal("old queued task escaped bulk cancel", got, err)
	}
}

func TestArtifactSummaryPreservesComparisonMetadata(t *testing.T) {
	s, ctx, _, m := fixture(t)
	j := dispatch(t, s, ctx, claimed(t, s, ctx, m, "artifact", "cn_html"))
	j.State = "succeeded"
	a := &domain.Artifact{Code: "<html>large source</html>", RawResponse: "large response", Prompt: "comparison prompt", Parameters: `{"max_tokens":8192}`, TemplateVersion: 3}
	if err := s.FinishJob(ctx, j, nil, a); err != nil {
		t.Fatal(err)
	}
	items, err := s.ListArtifactsSummary(ctx, m.ID)
	if err != nil || len(items) != 1 {
		t.Fatal(items, err)
	}
	if items[0].Code != "" || items[0].RawResponse != "" || items[0].Prompt != a.Prompt || items[0].Parameters != a.Parameters || items[0].TemplateVersion != 3 {
		t.Fatal("incorrect summary projection", items[0])
	}
}

func TestDispatchRevisionCheckIsInsideStateTransition(t *testing.T) {
	s, ctx, c, m := fixture(t)
	j := claimed(t, s, ctx, m, "probe", "")
	current, err := s.GetChannel(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	current.GeneratePath = "/changed-endpoint"
	if err = s.SaveChannel(ctx, *current); err != nil {
		t.Fatal(err)
	}
	if err = s.MarkDispatching(ctx, j.ID, j.LeaseToken, nowUTC().Add(time.Minute), "old snapshot"); !errors.Is(err, ErrConfigurationChanged) {
		t.Fatalf("stale revision dispatched: %v", err)
	}
	got, err := s.GetJob(ctx, j.ID)
	if err != nil || got.State != "claimed" || got.Snapshot != "" {
		t.Fatal("rejected dispatch changed state", got, err)
	}
}

func TestFutureSchemaIsRejectedWithoutCreatingTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.db")
	db, err := sql.Open("sqlite", sqliteFileURI(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("PRAGMA user_version=2"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("CREATE TABLE future_marker (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	for _, open := range []func(string) (*Store, error){Open, OpenExisting} {
		st, e := open(path)
		if e == nil {
			st.Close()
			t.Fatal("future schema was accepted")
		}
		var version, tables int
		if e = db.QueryRow("PRAGMA user_version").Scan(&version); e != nil {
			t.Fatal(e)
		}
		if e = db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table'").Scan(&tables); e != nil {
			t.Fatal(e)
		}
		if version != 2 || tables != 1 {
			t.Fatalf("future database was changed: version=%d tables=%d", version, tables)
		}
	}
}

func TestCompatibleVersionZeroRemainsReadableAndMigratesToOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err = a.DB.Exec("PRAGMA user_version=0"); err != nil {
		t.Fatal(err)
	}
	b, err := OpenExisting(path)
	if err != nil {
		t.Fatal("legacy backup cannot be validated", err)
	}
	var version int
	if err = b.DB.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		b.Close()
		t.Fatal(err)
	}
	b.Close()
	if version != 0 {
		t.Fatal("validation migrated legacy backup", version)
	}
	c, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err = c.DB.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 1 {
		t.Fatal("legacy schema was not upgraded", version, err)
	}
}

func TestChangingScheduleIntervalDoesNotRetainOldLongDelay(t *testing.T) {
	s, ctx, _, m := fixture(t)
	m.ProbeIntervalSeconds = 86400
	if err := s.SaveModelWithSchedules(ctx, m); err != nil {
		t.Fatal(err)
	}
	plans, err := s.ListSchedules(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range plans {
		if p.Kind == "probe" {
			p.NextRunAt = nowUTC().Add(12 * time.Hour)
			if err = s.SaveSchedule(ctx, p); err != nil {
				t.Fatal(err)
			}
		}
	}
	m.ProbeIntervalSeconds = 10
	before := nowUTC()
	if err = s.SaveModelWithSchedules(ctx, m); err != nil {
		t.Fatal(err)
	}
	plans, err = s.ListSchedules(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range plans {
		if p.Kind == "probe" && (p.IntervalSeconds != 10 || p.NextRunAt.Before(before) || p.NextRunAt.After(nowUTC().Add(10*time.Second))) {
			t.Fatal("new interval retained previous 12-hour due time", p)
		}
	}
}
