package store

import (
	"reflect"
	"testing"
	"time"

	"relaylab/internal/domain"
)

func auditSelectedModels(t *testing.T) (*Store, domain.Channel, domain.Model, domain.Model) {
	t.Helper()
	s, c := selectedDefaults(t)
	if err := s.SyncModels(t.Context(), c.ID, []string{defaultTestModel, "explicit-custom"}); err != nil {
		t.Fatal(err)
	}
	models, err := s.ListModels(t.Context(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	var sol, custom domain.Model
	for _, model := range models {
		if model.UpstreamID == defaultTestModel {
			sol = model
		} else if model.UpstreamID == "explicit-custom" {
			custom = model
		}
	}
	if sol.ID == "" || custom.ID == "" {
		t.Fatal("two-model audit fixture incomplete")
	}
	return s, c, sol, custom
}

func TestAuditDisabledArtworkPreservesOnlySelectedHealthProbe(t *testing.T) {
	s, c, sol, _ := auditSelectedModels(t)
	ctx := t.Context()
	if _, err := s.SetIntelligenceSchedule(ctx, c.ID, domain.IntelligenceSchedule{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	before, err := s.ListSchedules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, plan := range before {
		if plan.Enabled != (plan.Kind == "probe" && plan.ModelID == sol.ID) {
			t.Fatalf("disabled creation changed independent probe targeting: %+v", plan)
		}
	}
	if _, err := s.SetIntelligenceSchedule(ctx, c.ID, domain.IntelligenceSchedule{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	after, err := s.ListSchedules(ctx)
	if err != nil || !reflect.DeepEqual(schedulesByID(before), schedulesByID(after)) {
		t.Fatal("repeated artwork disable reset the still-active health schedule", err)
	}
	if _, err := s.TickSchedules(ctx, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	jobs, err := s.ListJobs(ctx)
	if err != nil || len(jobs) != 1 || jobs[0].Kind != "probe" || jobs[0].ModelID != sol.ID {
		t.Fatal("disabled artwork emitted a non-target or creative request", jobs, err)
	}
}

func TestAuditArchivedExplicitTargetNeverFallsBackAndReopensItself(t *testing.T) {
	s, c, sol, custom := auditSelectedModels(t)
	ctx := t.Context()
	if _, err := s.SetIntelligenceSchedule(ctx, c.ID, domain.IntelligenceSchedule{Enabled: true, ModelScope: "selected", ModelID: custom.ID, TemplateIDs: []string{"cn_html", "en_svg"}, IntervalSeconds: 120}); err != nil {
		t.Fatal(err)
	}
	custom.Archived = true
	if err := s.SaveModelWithSchedules(ctx, custom); err != nil {
		t.Fatal(err)
	}
	for _, item := range []domain.Model{sol, custom} {
		if err := s.ReconcileSchedules(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	if count, err := s.TickSchedules(ctx, time.Now().UTC().Add(time.Hour)); err != nil || count != 0 {
		t.Fatal("archived explicit target fell back to the available default", count, err)
	}
	saved, err := s.GetChannel(ctx, c.ID)
	if err != nil || saved.IntelligenceSchedule.ModelID != custom.ID {
		t.Fatal("archiving discarded the explicit selection", saved, err)
	}
	custom.Archived = false
	if err := s.SaveModelWithSchedules(ctx, custom); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TickSchedules(ctx, time.Now().UTC().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	jobs, err := s.ListJobs(ctx)
	if err != nil || len(jobs) == 0 {
		t.Fatal("reopened explicit target did not resume", err)
	}
	for _, job := range jobs {
		if job.ModelID != custom.ID {
			t.Fatal("reopening the original target widened automatic calls", job)
		}
	}
}

func TestAuditCancelingOneDueTemplateDoesNotDiscardItsWaitingSibling(t *testing.T) {
	s, c, sol, _ := auditSelectedModels(t)
	ctx := t.Context()
	if _, err := s.SetIntelligenceSchedule(ctx, c.ID, domain.IntelligenceSchedule{Enabled: true, ModelScope: "selected", ModelID: sol.ID, TemplateIDs: []string{"cn_html", "en_svg"}, IntervalSeconds: 120}); err != nil {
		t.Fatal(err)
	}
	// Changing the existing interval schedules the next cycle from save time;
	// advance beyond that deadline before testing sibling cancellation.
	now := time.Now().UTC().Add(time.Hour)
	if _, err := s.TickSchedules(ctx, now); err != nil {
		t.Fatal(err)
	}
	jobs, err := s.ListJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var first domain.Job
	for _, job := range jobs {
		if job.Kind == "artifact" {
			if first.ID != "" {
				t.Fatal("same model dispatched two artifact jobs at once")
			}
			first = job
		}
	}
	if first.ID == "" {
		t.Fatal("no due artifact job")
	}
	if err := s.RequestCancel(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TickSchedules(ctx, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	jobs, err = s.ListJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := 0
	for _, job := range jobs {
		if job.Kind != "artifact" {
			continue
		}
		artifacts++
		if job.ID != first.ID && (job.TemplateID == first.TemplateID || job.State != "queued") {
			t.Fatal("canceled template was duplicated or sibling was not retained", job)
		}
	}
	if artifacts != 2 {
		t.Fatalf("wanted canceled template plus one waiting sibling, got %d", artifacts)
	}
}
