package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"relaylab/internal/domain"
)

const dayOfSeconds = 86400

// fingerprintSchedule returns the fingerprint schedule of a model, or nil when the model has none.
func fingerprintSchedule(t *testing.T, s *Store, ctx context.Context, modelID string) *domain.Schedule {
	t.Helper()
	all, e := s.ListSchedules(ctx, modelID)
	if e != nil {
		t.Fatal(e)
	}
	for i := range all {
		if all[i].Kind == "fingerprint" {
			return &all[i]
		}
	}
	return nil
}

func fingerprintJobs(t *testing.T, s *Store, ctx context.Context) []domain.Job {
	t.Helper()
	all, e := s.ListJobs(ctx, 1000)
	if e != nil {
		t.Fatal(e)
	}
	out := []domain.Job{}
	for _, j := range all {
		if j.Kind == "fingerprint" {
			out = append(out, j)
		}
	}
	return out
}

func switchScheduledFingerprint(t *testing.T, s *Store, ctx context.Context, modelID string, on bool) {
	t.Helper()
	if e := s.SaveFingerprintModelSettings(ctx, modelID, domain.FingerprintModelSettings{Mode: domain.FingerprintModeAuto, Questions: 3, Scheduled: on}); e != nil {
		t.Fatal(e)
	}
}

// dueFingerprintSchedule switches scheduled detection on and moves its first run into the past.
func dueFingerprintSchedule(t *testing.T, s *Store, ctx context.Context, modelID string, now time.Time) domain.Schedule {
	t.Helper()
	switchScheduledFingerprint(t, s, ctx, modelID, true)
	plan := fingerprintSchedule(t, s, ctx, modelID)
	if plan == nil {
		t.Fatal("no fingerprint schedule after switching scheduled detection on")
	}
	plan.NextRunAt = now.Add(-time.Hour)
	if e := s.SaveSchedule(ctx, *plan); e != nil {
		t.Fatal(e)
	}
	return *plan
}

func TestFingerprintScheduleExistsOnlyAfterSwitchingItOn(t *testing.T) {
	s, ctx, _, m := fixture(t)
	if e := s.SaveModelWithSchedules(ctx, m); e != nil {
		t.Fatal(e)
	}
	all, e := s.ListSchedules(ctx, m.ID)
	if e != nil || len(all) != 3 || fingerprintSchedule(t, s, ctx, m.ID) != nil {
		t.Fatalf("a model without scheduled detection has schedules %+v %v", all, e)
	}
	if n, e := s.TickSchedules(ctx, nowUTC().Add(365*24*time.Hour)); e != nil || len(fingerprintJobs(t, s, ctx)) != 0 {
		t.Fatalf("scheduled detection is off, yet a tick of %d jobs queued a detection: %v", n, e)
	}

	before := nowUTC()
	switchScheduledFingerprint(t, s, ctx, m.ID, true)
	plan := fingerprintSchedule(t, s, ctx, m.ID)
	if plan == nil || !plan.Enabled || plan.IntervalSeconds != dayOfSeconds {
		t.Fatalf("schedule after switching on = %+v", plan)
	}
	if plan.NextRunAt.Before(before.Add(-time.Second)) || plan.NextRunAt.After(nowUTC().Add(dayOfSeconds*time.Second)) {
		t.Fatalf("first run at %s is not within one interval of %s", plan.NextRunAt, before)
	}
	if jobs := fingerprintJobs(t, s, ctx); len(jobs) != 0 {
		t.Fatalf("switching on queued %d detections by itself", len(jobs))
	}

	switchScheduledFingerprint(t, s, ctx, m.ID, false)
	if plan = fingerprintSchedule(t, s, ctx, m.ID); plan == nil || plan.Enabled {
		t.Fatalf("schedule after switching off = %+v", plan)
	}
	if _, e = s.TickSchedules(ctx, nowUTC().Add(365*24*time.Hour)); e != nil || len(fingerprintJobs(t, s, ctx)) != 0 {
		t.Fatalf("a switched-off schedule queued a detection: %v", e)
	}

	// A model that cannot run keeps the switch but pauses the schedule.
	switchScheduledFingerprint(t, s, ctx, m.ID, true)
	stored, e := s.GetModel(ctx, m.ID)
	if e != nil {
		t.Fatal(e)
	}
	stored.Missing = true
	if e = s.SaveModelWithSchedules(ctx, *stored); e != nil {
		t.Fatal(e)
	}
	if plan = fingerprintSchedule(t, s, ctx, m.ID); plan == nil || plan.Enabled {
		t.Fatalf("schedule of a model missing upstream = %+v", plan)
	}
}

func TestDueFingerprintScheduleQueuesOneDetectionPerInterval(t *testing.T) {
	s, ctx, _, m := fixture(t)
	now := nowUTC()
	plan := dueFingerprintSchedule(t, s, ctx, m.ID, now)
	if _, e := s.TickSchedules(ctx, now); e != nil {
		t.Fatal(e)
	}
	jobs := fingerprintJobs(t, s, ctx)
	if len(jobs) != 1 || jobs[0].ScheduleID != plan.ID || jobs[0].ModelID != m.ID || jobs[0].State != "queued" {
		t.Fatalf("jobs after the due tick = %+v", jobs)
	}
	next := fingerprintSchedule(t, s, ctx, m.ID).NextRunAt
	if want := now.Add(dayOfSeconds * time.Second); next.Sub(want).Abs() > time.Second {
		t.Fatalf("next run at %s, want %s", next, want)
	}
	if _, e := s.TickSchedules(ctx, now.Add(time.Hour)); e != nil || len(fingerprintJobs(t, s, ctx)) != 1 {
		t.Fatalf("a tick before the next run queued again: %d jobs %v", len(fingerprintJobs(t, s, ctx)), e)
	}

	// The first detection is still waiting when the next run is due: it is not doubled, and the run is not kept
	// to fire right after it.
	later := now.Add((dayOfSeconds + 1) * time.Second)
	if _, e := s.TickSchedules(ctx, later); e != nil || len(fingerprintJobs(t, s, ctx)) != 1 {
		t.Fatalf("a waiting detection was doubled: %d jobs %v", len(fingerprintJobs(t, s, ctx)), e)
	}
	if next = fingerprintSchedule(t, s, ctx, m.ID).NextRunAt; !next.After(later) {
		t.Fatalf("the skipped run stays due at %s", next)
	}
}

func TestSwitchingScheduledFingerprintOffCancelsItsQueuedJob(t *testing.T) {
	s, ctx, _, m := fixture(t)
	now := nowUTC()
	dueFingerprintSchedule(t, s, ctx, m.ID, now)
	if _, e := s.TickSchedules(ctx, now); e != nil {
		t.Fatal(e)
	}
	switchScheduledFingerprint(t, s, ctx, m.ID, false)
	jobs := fingerprintJobs(t, s, ctx)
	if len(jobs) != 1 || jobs[0].State != "canceled" {
		t.Fatalf("jobs after switching off = %+v", jobs)
	}
	// A detection started by hand is not part of the schedule and stays.
	manual, e := s.EnqueueJob(ctx, domain.Job{ModelID: m.ID, Kind: "fingerprint"})
	if e != nil {
		t.Fatal(e)
	}
	switchScheduledFingerprint(t, s, ctx, m.ID, true)
	switchScheduledFingerprint(t, s, ctx, m.ID, false)
	if kept, e := s.GetJob(ctx, manual.ID); e != nil || kept.State != "queued" {
		t.Fatalf("manual detection after switching off = %+v %v", kept, e)
	}
}

func TestScheduledFingerprintJobIsDispatchedOnlyWhileSwitchedOn(t *testing.T) {
	for _, on := range []bool{true, false} {
		s, ctx, _, m := fixture(t)
		now := nowUTC()
		dueFingerprintSchedule(t, s, ctx, m.ID, now)
		if _, e := s.TickSchedules(ctx, now); e != nil {
			t.Fatal(e)
		}
		j, e := s.ClaimJob(ctx, "fingerprint", "owner", nowUTC())
		if e != nil || j == nil {
			t.Fatalf("claim: %v %+v", e, j)
		}
		if !on {
			// The switch changes without the schedule being reconciled, as it would between two steps of a save.
			stored, e := s.GetModel(ctx, m.ID)
			if e != nil {
				t.Fatal(e)
			}
			stored.FingerprintScheduled = false
			if e = saveModel(ctx, s.DB, *stored); e != nil {
				t.Fatal(e)
			}
		}
		e = s.MarkDispatching(ctx, j.ID, j.LeaseToken, nowUTC().Add(time.Minute), "snapshot")
		if on && e != nil {
			t.Fatalf("a scheduled detection of a switched-on model was refused: %v", e)
		}
		if !on && !errors.Is(e, ErrConfigurationChanged) {
			t.Fatalf("a scheduled detection of a switched-off model was dispatched: %v", e)
		}
	}
}

func TestFingerprintScheduleFollowsTheIntervalSetting(t *testing.T) {
	s, ctx, _, m := fixture(t)
	cfg, e := s.GetSettings(ctx)
	if e != nil {
		t.Fatal(e)
	}

	// Settings saved before the fingerprint feature carry no interval.
	legacy := cfg
	legacy.FingerprintIntervalSeconds = 0
	raw, e := data(legacy)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB.ExecContext(ctx, "UPDATE settings SET data=? WHERE id=1", raw); e != nil {
		t.Fatal(e)
	}
	switchScheduledFingerprint(t, s, ctx, m.ID, true)
	if plan := fingerprintSchedule(t, s, ctx, m.ID); plan == nil || plan.IntervalSeconds != dayOfSeconds {
		t.Fatalf("schedule under settings without an interval = %+v", plan)
	}

	cfg.FingerprintIntervalSeconds = 3600
	before := nowUTC()
	if e = s.SaveSettingsAndReconcile(ctx, cfg); e != nil {
		t.Fatal(e)
	}
	plan := fingerprintSchedule(t, s, ctx, m.ID)
	if plan == nil || !plan.Enabled || plan.IntervalSeconds != 3600 {
		t.Fatalf("schedule after changing the interval = %+v", plan)
	}
	if want := before.Add(time.Hour); plan.NextRunAt.Sub(want).Abs() > 5*time.Second {
		t.Fatalf("next run at %s, want about %s", plan.NextRunAt, want)
	}
}
