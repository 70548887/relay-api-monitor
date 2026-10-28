package store

import (
	"reflect"
	"testing"
	"time"

	"relaylab/internal/domain"
)

func selectedDefaults(t *testing.T) (*Store, domain.Channel) {
	t.Helper()
	s, ctx, c, _ := fixture(t)
	controls := domain.DefaultModelControls()
	c.ModelControls = &controls
	if err := s.SaveChannelWithModelControls(ctx, c, false); err != nil {
		t.Fatal(err)
	}
	saved, err := s.GetChannel(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.IntelligenceSchedule == nil || saved.IntelligenceSchedule.ModelScope != "selected" || saved.IntelligenceSchedule.ModelID != "" || !saved.IntelligenceSchedule.Enabled {
		t.Fatal("default group should wait for the exact model", saved.IntelligenceSchedule)
	}
	return s, *saved
}

func TestSelectedDefaultsDiscoverOnlyExactModelAndKeepScheduleTime(t *testing.T) {
	s, c := selectedDefaults(t)
	ctx := t.Context()
	ids := []string{"upstream", "gpt-6.1-sol", "gpt-5.4", "gpt-image-1", "prefix-gpt-6.1-sol", "gpt-6.1-sol-suffix"}
	if err := s.SyncModels(ctx, c.ID, ids); err != nil {
		t.Fatal(err)
	}
	models, _ := s.ListModels(ctx, c.ID)
	var selected domain.Model
	for _, model := range models {
		if model.UpstreamID == defaultTestModel {
			selected = model
		}
	}
	if selected.ID == "" {
		t.Fatal("default model not discovered")
	}
	saved, _ := s.GetChannel(ctx, c.ID)
	if saved.IntelligenceSchedule.ModelID != selected.ID {
		t.Fatal("default model was not bound", saved.IntelligenceSchedule)
	}
	plans, _ := s.ListSchedules(ctx)
	enabled := 0
	for _, plan := range plans {
		if plan.Enabled {
			enabled++
			if plan.ModelID != selected.ID {
				t.Fatal("an unselected model gained a paid automatic plan", plan)
			}
		}
	}
	if enabled != 3 {
		t.Fatalf("wanted selected probe and two templates, got %d", enabled)
	}
	if err := s.SyncModels(ctx, c.ID, ids); err != nil {
		t.Fatal(err)
	}
	after, _ := s.ListSchedules(ctx)
	if !reflect.DeepEqual(schedulesByID(plans), schedulesByID(after)) {
		t.Fatal("repeat discovery reset plans or their next trigger time")
	}
	if _, err := s.TickSchedules(ctx, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	jobs, _ := s.ListJobs(ctx)
	for _, job := range jobs {
		if job.ModelID != selected.ID {
			t.Fatal("discovery enqueued work for an unselected model", job)
		}
	}
}

func TestSelectedDefaultMissingNeverFallsBackAndManualAddBindsIt(t *testing.T) {
	s, c := selectedDefaults(t)
	ctx := t.Context()
	if err := s.SyncModels(ctx, c.ID, []string{"gpt-5.4", "gpt-6.1-sol-other"}); err != nil {
		t.Fatal(err)
	}
	if count, err := s.TickSchedules(ctx, time.Now().UTC().Add(time.Hour)); err != nil || count != 0 {
		t.Fatal("unresolved default sent automatic work", count, err)
	}
	model := domain.Model{ID: "exact-default-manual", ChannelID: c.ID, UpstreamID: defaultTestModel, Public: true, ProbeEnabled: true, ArtifactChinese: true, ArtifactEnglish: true}
	if err := s.SaveModelWithSchedules(ctx, model); err != nil {
		t.Fatal(err)
	}
	saved, _ := s.GetChannel(ctx, c.ID)
	if saved.IntelligenceSchedule.ModelID != model.ID {
		t.Fatal("manual default did not resolve the pending selection")
	}
	if err := s.SyncModels(ctx, c.ID, []string{"gpt-5.4"}); err != nil {
		t.Fatal(err)
	}
	saved, _ = s.GetChannel(ctx, c.ID)
	if saved.IntelligenceSchedule.ModelID != model.ID {
		t.Fatal("missing target silently changed to a different model")
	}
	if count, err := s.TickSchedules(ctx, time.Now().UTC().Add(2*time.Hour)); err != nil || count != 0 {
		t.Fatal("missing selected target sent automatic work", count, err)
	}
}

func TestEditorSelectionCancelsUnsentScheduledJobsAndPreservesManualWork(t *testing.T) {
	s, c := selectedDefaults(t)
	ctx := t.Context()
	if err := s.SyncModels(ctx, c.ID, []string{defaultTestModel, "custom-selected"}); err != nil {
		t.Fatal(err)
	}
	models, _ := s.ListModels(ctx, c.ID)
	var first, second domain.Model
	for _, model := range models {
		if model.UpstreamID == defaultTestModel {
			first = model
		} else if model.UpstreamID == "custom-selected" {
			second = model
		}
	}
	if _, err := s.SetIntelligenceSchedule(ctx, c.ID, domain.IntelligenceSchedule{Enabled: true, ModelID: first.ID, TemplateIDs: []string{"cn_html"}, IntervalSeconds: 120}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TickSchedules(ctx, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcquireWorkerLease(ctx, "selected-worker", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimJob(ctx, "artifact", "selected-worker", time.Now().UTC())
	if err != nil || claimed == nil {
		t.Fatal("claim scheduled job", err)
	}
	manual, err := s.EnqueueJob(ctx, domain.Job{ModelID: second.ID, Kind: "artifact", TemplateID: "en_svg"})
	if err != nil {
		t.Fatal(err)
	}
	curr, _ := s.GetChannel(ctx, c.ID)
	beforeRevision := curr.Revision
	curr.IntelligenceModelID = second.ID
	if err := s.SaveChannelWithModelControls(ctx, *curr, false); err != nil {
		t.Fatal(err)
	}
	saved, _ := s.GetChannel(ctx, c.ID)
	if saved.Revision != beforeRevision || saved.IntelligenceSchedule.ModelScope != "selected" || saved.IntelligenceSchedule.ModelID != second.ID {
		t.Fatal("selection did not update independently of request revision", saved)
	}
	old, _ := s.GetJob(ctx, claimed.ID)
	if !old.CancelRequested {
		t.Fatal("claimed old scheduled job was not canceled")
	}
	if err := s.MarkDispatching(ctx, claimed.ID, claimed.LeaseToken, time.Now().UTC().Add(time.Minute), "{}"); err == nil {
		t.Fatal("old selected job still dispatched")
	}
	untouched, _ := s.GetJob(ctx, manual.ID)
	if untouched.CancelRequested || untouched.State != "queued" {
		t.Fatal("selection change canceled independent manual work", untouched)
	}
	plans, _ := s.ListSchedules(ctx)
	for _, plan := range plans {
		if plan.Enabled && plan.ModelID != second.ID {
			t.Fatal("old target still enabled", plan)
		}
	}
	// Clearing the explicit override returns to the exact default, rather than
	// keeping a stale custom target or widening the group to all models.
	saved.IntelligenceModelID = ""
	if err := s.SaveChannelWithModelControls(ctx, *saved, false); err != nil {
		t.Fatal(err)
	}
	saved, _ = s.GetChannel(ctx, c.ID)
	if saved.IntelligenceSchedule.ModelID != first.ID || saved.IntelligenceSchedule.ModelScope != "selected" {
		t.Fatal("clearing explicit selection failed to restore exact default", saved.IntelligenceSchedule)
	}
}

func TestEditorExplicitTargetNarrowsLegacyAllScopeAndCancelsOtherQueuedWork(t *testing.T) {
	s, c := selectedDefaults(t)
	ctx := t.Context()
	if err := s.SyncModels(ctx, c.ID, []string{defaultTestModel, "different-target"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetIntelligenceSchedule(ctx, c.ID, domain.IntelligenceSchedule{Enabled: true, ModelScope: "all", TemplateIDs: []string{"cn_html"}, IntervalSeconds: 120}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TickSchedules(ctx, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	models, _ := s.ListModels(ctx, c.ID)
	var selected domain.Model
	for _, model := range models {
		if model.UpstreamID == "different-target" {
			selected = model
		}
	}
	saved, _ := s.GetChannel(ctx, c.ID)
	saved.IntelligenceModelID = selected.ID
	if err := s.SaveChannelWithModelControls(ctx, *saved, false); err != nil {
		t.Fatal(err)
	}
	saved, _ = s.GetChannel(ctx, c.ID)
	if saved.IntelligenceSchedule.ModelScope != "selected" || saved.IntelligenceSchedule.ModelID != selected.ID {
		t.Fatal("explicit editor selection left legacy all-model scope active", saved.IntelligenceSchedule)
	}
	plans, _ := s.ListSchedules(ctx)
	for _, plan := range plans {
		if plan.Enabled && plan.ModelID != selected.ID {
			t.Fatal("unselected plan survived explicit editor selection", plan)
		}
	}
	jobs, _ := s.ListJobs(ctx)
	for _, job := range jobs {
		if job.ScheduleID != "" && job.ModelID != selected.ID && (job.State != "canceled" || !job.CancelRequested) {
			t.Fatal("unselected queued job survived explicit editor selection", job)
		}
	}
}
