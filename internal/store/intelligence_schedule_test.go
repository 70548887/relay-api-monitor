package store

import (
	"errors"
	"testing"
	"time"

	"relaylab/internal/domain"
)

func TestGroupPlanOwnsOnlySelectedModelAndTemplate(t *testing.T) {
	s, ctx, c, m := fixture(t)
	m.ArtifactChinese = false
	m.ArtifactEnglish = false
	if err := s.SaveModelWithSchedules(ctx, m); err != nil {
		t.Fatal(err)
	}
	other := m
	other.ID = "other"
	other.UpstreamID = "other"
	other.ArtifactChinese = true
	if err := s.SaveModelWithSchedules(ctx, other); err != nil {
		t.Fatal(err)
	}
	current, _ := s.GetChannel(ctx, c.ID)
	saved, err := s.SetIntelligenceSchedule(ctx, c.ID, domain.IntelligenceSchedule{Enabled: true, ModelID: m.ID, TemplateIDs: []string{"cn_html"}, IntervalSeconds: 120})
	if err != nil || saved.Revision != current.Revision {
		t.Fatalf("save %v revision changed", err)
	}
	plans, err := s.ListSchedules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	activeArtifacts := 0
	for _, plan := range plans {
		if plan.Kind == "artifact" && plan.Enabled {
			activeArtifacts++
			if plan.ModelID != m.ID || plan.TemplateID != "cn_html" || plan.IntervalSeconds != 120 {
				t.Fatalf("wrong artifact plan %+v", plan)
			}
		}
		if plan.Kind == "probe" && !plan.Enabled {
			t.Fatal("group artifact plan disabled monitoring")
		}
	}
	if activeArtifacts != 1 {
		t.Fatalf("active artifact plans=%d", activeArtifacts)
	}
	now := time.Now().UTC().Add(24 * time.Hour)
	if _, err = s.TickSchedules(ctx, now); err != nil {
		t.Fatal(err)
	}
	manual, err := s.EnqueueJob(ctx, domain.Job{ModelID: other.ID, Kind: "artifact", TemplateID: "en_svg"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetIntelligenceSchedule(ctx, c.ID, domain.IntelligenceSchedule{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	jobs, _ := s.ListJobs(ctx)
	for _, j := range jobs {
		if j.ScheduleID != "" && j.Kind == "artifact" && j.State != "canceled" {
			t.Fatalf("scheduled job not canceled %+v", j)
		}
		if j.ID == manual.ID && (j.State != "queued" || j.CancelRequested) {
			t.Fatal("manual job was canceled")
		}
	}
	if _, err = s.EnqueueJob(ctx, domain.Job{ModelID: m.ID, Kind: "artifact", TemplateID: "cn_html"}); err != nil {
		t.Fatalf("disabled group plan blocked manual test: %v", err)
	}
	// A stale general edit carries the new plan forward instead of reviving
	// previous per-model automatic calls.
	if err = s.SaveChannelWithSchedules(ctx, *current); err != nil {
		t.Fatal(err)
	}
	persisted, _ := s.GetChannel(ctx, c.ID)
	if persisted.IntelligenceSchedule == nil || persisted.IntelligenceSchedule.Enabled {
		t.Fatal("stale channel edit overwrote group plan")
	}
}

func TestGroupPlanDisableLetsDispatchedRequestFinish(t *testing.T) {
	s, ctx, c, m := fixture(t)
	_, err := s.SetIntelligenceSchedule(ctx, c.ID, domain.IntelligenceSchedule{Enabled: true, ModelID: m.ID, TemplateIDs: []string{"cn_html"}, IntervalSeconds: 120})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.TickSchedules(ctx, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcquireWorkerLease(ctx, "group-worker", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	j, err := s.ClaimJob(ctx, "artifact", "group-worker", time.Now().UTC())
	if err != nil || j == nil {
		t.Fatalf("claim %v", err)
	}
	if err = s.MarkDispatching(ctx, j.ID, j.LeaseToken, time.Now().UTC().Add(time.Minute), "{}"); err != nil {
		t.Fatal(err)
	}
	_, err = s.SetIntelligenceSchedule(ctx, c.ID, domain.IntelligenceSchedule{Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	current, _ := s.GetJob(ctx, j.ID)
	if current.CancelRequested || current.State != "dispatching" {
		t.Fatalf("in-flight job canceled %+v", current)
	}
	// Fully disabling the upstream channel still cancels all scheduled work.
	channel, err := s.GetChannel(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	channel.Enabled = false
	if err = s.SaveChannelWithSchedules(ctx, *channel); err != nil {
		t.Fatal(err)
	}
	current, _ = s.GetJob(ctx, j.ID)
	if !current.CancelRequested {
		t.Fatal("disabled channel failed to cancel its running job")
	}
}

func TestInvalidGroupPlanNeverPartiallyChangesSchedules(t *testing.T) {
	s, ctx, c, m := fixture(t)
	for _, plan := range []domain.IntelligenceSchedule{
		{Enabled: true, ModelID: m.ID, TemplateIDs: []string{"cn_html"}, IntervalSeconds: 59},
		{Enabled: true, ModelID: m.ID, TemplateIDs: []string{"cn_html"}, IntervalSeconds: 604801},
		{Enabled: true, ModelID: "missing", TemplateIDs: []string{"cn_html"}, IntervalSeconds: 120},
		{Enabled: true, ModelID: m.ID, TemplateIDs: []string{"unknown"}, IntervalSeconds: 120},
		{Enabled: true, ModelID: m.ID, TemplateIDs: []string{"cn_html", "cn_html"}, IntervalSeconds: 120},
		{Enabled: true, ModelID: m.ID, IntervalSeconds: 120},
	} {
		if _, err := s.SetIntelligenceSchedule(ctx, c.ID, plan); !errors.Is(err, ErrInvalidIntelligenceSchedule) {
			t.Fatalf("invalid plan accepted: %+v %v", plan, err)
		}
		ch, _ := s.GetChannel(ctx, c.ID)
		if ch.IntelligenceSchedule != nil {
			t.Fatal("invalid plan changed persisted channel")
		}
	}
}

func TestGroupPlanRollsBackWhenScheduleWriteFails(t *testing.T) {
	s, ctx, c, m := fixture(t)
	if _, err := s.DB.ExecContext(ctx, "CREATE TRIGGER reject_group_plan BEFORE INSERT ON schedules BEGIN SELECT RAISE(ABORT, 'fixture schedule failure'); END"); err != nil {
		t.Fatal(err)
	}
	_, err := s.SetIntelligenceSchedule(ctx, c.ID, domain.IntelligenceSchedule{Enabled: true, ModelID: m.ID, TemplateIDs: []string{"cn_html"}, IntervalSeconds: 120})
	if err == nil {
		t.Fatal("schedule failure was ignored")
	}
	current, err := s.GetChannel(ctx, c.ID)
	if err != nil || current.IntelligenceSchedule != nil {
		t.Fatal("partial plan was committed", err)
	}
}
