package store

import (
	"relaylab/internal/domain"
	"testing"
	"time"
)

func artifactPlan(t *testing.T, s *Store, model string) domain.Schedule {
	t.Helper()
	plans, err := s.ListSchedules(t.Context(), model)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range plans {
		if p.TemplateID == "cn_html" && p.Kind == "artifact" {
			return p
		}
	}
	t.Fatal("plan missing")
	return domain.Schedule{}
}
func TestImmediateEnableStableSaveAndIntervalChange(t *testing.T) {
	s, ctx, c, m := fixture(t)
	plan := domain.IntelligenceSchedule{Enabled: true, ModelID: m.ID, TemplateIDs: []string{"cn_html"}, IntervalSeconds: 6000}
	before := time.Now().UTC()
	if _, err := s.SetIntelligenceSchedule(ctx, c.ID, plan); err != nil {
		t.Fatal(err)
	}
	first := artifactPlan(t, s, m.ID)
	if first.NextRunAt.Before(before) || first.NextRunAt.After(time.Now().UTC()) {
		t.Fatal("first task not immediately due", first.NextRunAt)
	}
	if _, err := s.SetIntelligenceSchedule(ctx, c.ID, plan); err != nil {
		t.Fatal(err)
	}
	if !artifactPlan(t, s, m.ID).NextRunAt.Equal(first.NextRunAt) {
		t.Fatal("same save reset first run")
	}
	plan.IntervalSeconds = 300
	before = time.Now().UTC()
	if _, err := s.SetIntelligenceSchedule(ctx, c.ID, plan); err != nil {
		t.Fatal(err)
	}
	next := artifactPlan(t, s, m.ID)
	if next.NextRunAt.Before(before.Add(300*time.Second)) || next.NextRunAt.After(time.Now().UTC().Add(300*time.Second)) {
		t.Fatal("interval not based on save time", next.NextRunAt)
	}
	if _, err := s.SetIntelligenceSchedule(ctx, c.ID, domain.IntelligenceSchedule{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetIntelligenceSchedule(ctx, c.ID, plan); err != nil {
		t.Fatal(err)
	}
	if artifactPlan(t, s, m.ID).NextRunAt.After(time.Now().UTC()) {
		t.Fatal("re-enable not immediate")
	}
}
func TestManualWorkRetainsOnlyOneDueScheduleSlot(t *testing.T) {
	s, ctx, c, m := fixture(t)
	manual, err := s.EnqueueJob(ctx, domain.Job{ModelID: m.ID, Kind: "artifact", TemplateID: "cn_html"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetIntelligenceSchedule(ctx, c.ID, domain.IntelligenceSchedule{Enabled: true, ModelID: m.ID, TemplateIDs: []string{"cn_html"}, IntervalSeconds: 120}); err != nil {
		t.Fatal(err)
	}
	due := artifactPlan(t, s, m.ID).NextRunAt
	for i := 0; i < 3; i++ {
		if _, err = s.TickSchedules(ctx, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	if !artifactPlan(t, s, m.ID).NextRunAt.Equal(due) {
		t.Fatal("busy model lost pending cycle")
	}
	if err = s.RequestCancel(ctx, manual.ID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err = s.TickSchedules(ctx, now); err != nil {
		t.Fatal(err)
	}
	if !artifactPlan(t, s, m.ID).NextRunAt.Equal(now.Add(120 * time.Second)) {
		t.Fatal("successful enqueue did not advance interval")
	}
	if _, err = s.TickSchedules(ctx, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	jobs, err := s.ListJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, j := range jobs {
		if j.ScheduleID != "" && j.Kind == "artifact" {
			count++
		}
	}
	if count != 1 {
		t.Fatal("duplicate cycle", count)
	}
}
