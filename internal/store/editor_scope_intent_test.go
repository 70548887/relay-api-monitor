package store

import (
	"reflect"
	"testing"
	"time"

	"relaylab/internal/domain"
)

func TestEditorSelectedIntentNarrowsAllWhenRepresentativeIsUnchanged(t *testing.T) {
	s, c := selectedDefaults(t)
	ctx := t.Context()
	if err := s.SyncModels(ctx, c.ID, []string{defaultTestModel, "custom-model"}); err != nil {
		t.Fatal(err)
	}
	models, _ := s.ListModels(ctx, c.ID)
	var target, other domain.Model
	for _, m := range models {
		if m.UpstreamID == defaultTestModel {
			target = m
		} else if m.UpstreamID == "custom-model" {
			other = m
		}
	}
	current, _ := s.GetChannel(ctx, c.ID)
	current.IntelligenceModelID = target.ID
	if err := s.SaveChannelWithModelControls(ctx, *current, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetIntelligenceSchedule(ctx, c.ID, domain.IntelligenceSchedule{Enabled: true, ModelScope: "all", TemplateIDs: []string{"cn_html"}, IntervalSeconds: 120}); err != nil {
		t.Fatal(err)
	}
	manual, err := s.EnqueueJob(ctx, domain.Job{ModelID: other.ID, Kind: "artifact", TemplateID: "en_svg"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.TickSchedules(ctx, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	current, _ = s.GetChannel(ctx, c.ID)
	if current.IntelligenceModelID != target.ID {
		t.Fatal("fixture representative changed")
	}
	if err := s.SaveChannelWithModelControls(ctx, *current, false); err != nil {
		t.Fatal(err)
	}
	current, _ = s.GetChannel(ctx, c.ID)
	if current.IntelligenceSchedule.ModelScope != "all" {
		t.Fatal("older callers must retain their existing scope compatibility")
	}
	if err := s.SaveChannelWithModelControls(ctx, *current, false, true); err != nil {
		t.Fatal(err)
	}
	saved, _ := s.GetChannel(ctx, c.ID)
	if saved.IntelligenceSchedule.ModelScope != "selected" || saved.IntelligenceSchedule.ModelID != target.ID {
		t.Fatal("editor intent retained all-model scheduling", saved.IntelligenceSchedule)
	}
	plans, _ := s.ListSchedules(ctx)
	for _, plan := range plans {
		if plan.Enabled && plan.ModelID != target.ID {
			t.Fatal("non-target automatic plan remains enabled", plan)
		}
	}
	jobs, _ := s.ListJobs(ctx)
	for _, job := range jobs {
		if job.ScheduleID != "" && job.ModelID != target.ID && (!job.CancelRequested || job.State != "canceled") {
			t.Fatal("non-target queued work survived narrowing", job)
		}
	}
	untouched, _ := s.GetJob(ctx, manual.ID)
	if untouched.CancelRequested || untouched.State != "queued" {
		t.Fatal("explicit single-model intent canceled independent manual work", untouched)
	}
}

func TestEditorSelectedIntentCanResetUnsetRepresentativeToDefault(t *testing.T) {
	s, c := selectedDefaults(t)
	ctx := t.Context()
	if err := s.SyncModels(ctx, c.ID, []string{defaultTestModel, "custom-model"}); err != nil {
		t.Fatal(err)
	}
	models, _ := s.ListModels(ctx, c.ID)
	var target, custom domain.Model
	for _, m := range models {
		if m.UpstreamID == defaultTestModel {
			target = m
		} else if m.UpstreamID == "custom-model" {
			custom = m
		}
	}
	if _, err := s.SetIntelligenceSchedule(ctx, c.ID, domain.IntelligenceSchedule{Enabled: true, ModelScope: "selected", ModelID: custom.ID, TemplateIDs: []string{"cn_html"}, IntervalSeconds: 120}); err != nil {
		t.Fatal(err)
	}
	current, _ := s.GetChannel(ctx, c.ID)
	if current.IntelligenceModelID != "" {
		t.Fatal("fixture must have no representative override")
	}
	if err := s.SaveChannelWithModelControls(ctx, *current, false, true); err != nil {
		t.Fatal(err)
	}
	saved, _ := s.GetChannel(ctx, c.ID)
	if saved.IntelligenceSchedule.ModelScope != "selected" || saved.IntelligenceSchedule.ModelID != target.ID {
		t.Fatal("cleared editor target kept the stale custom plan", saved.IntelligenceSchedule)
	}
	before, _ := s.ListSchedules(ctx)
	beforeRevision := saved.Revision
	for range 2 {
		if err := s.SaveChannelWithModelControls(ctx, *saved, false, true); err != nil {
			t.Fatal(err)
		}
		saved, _ = s.GetChannel(ctx, c.ID)
	}
	after, _ := s.ListSchedules(ctx)
	if !reflect.DeepEqual(schedulesByID(before), schedulesByID(after)) {
		t.Fatal("repeated explicit editor save reset schedule time")
	}
	if saved.Revision != beforeRevision {
		t.Fatal("selection-only save changed request revision and invalidated independent jobs")
	}
}
