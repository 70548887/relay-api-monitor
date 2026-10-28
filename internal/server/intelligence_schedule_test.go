package server

import (
	"context"
	"encoding/json"
	"testing"

	"relaylab/internal/domain"
)

func TestGroupScheduleAuthValidationAndManualTest(t *testing.T) {
	s, _, c, m := publicFixture(t)
	h := s.Handler()
	cookie, csrf := loginForTest(t, h)
	path := "/api/v1/admin/channels/" + c.ID + "/intelligence-schedule"
	plan := domain.IntelligenceSchedule{Enabled: true, ModelID: m.ID, TemplateIDs: []string{"cn_html", "en_svg"}, IntervalSeconds: 1800}
	if w := request(t, h, "PUT", path, plan, nil, ""); w.Code != 401 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request(t, h, "PUT", path, plan, cookie, ""); w.Code != 403 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request(t, h, "PUT", path, map[string]any{"model_id": m.ID}, cookie, csrf); w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	w := request(t, h, "PUT", path, plan, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var saved domain.Channel
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil || saved.IntelligenceSchedule == nil || !saved.IntelligenceSchedule.Enabled {
		t.Fatal(err, w.Body.String())
	}
	if w = request(t, h, "PUT", path, map[string]any{"enabled": false}, cookie, csrf); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request(t, h, "POST", "/api/v1/admin/jobs", map[string]string{"model_id": m.ID, "kind": "artifact", "template_id": "en_svg"}, cookie, csrf)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var job domain.Job
	_ = json.Unmarshal(w.Body.Bytes(), &job)
	if job.ModelID != m.ID || job.ChannelID != c.ID || job.TemplateID != "en_svg" || job.ScheduleID != "" || job.State != "queued" {
		t.Fatalf("manual job mismatch %+v", job)
	}
	if duplicate := request(t, h, "POST", "/api/v1/admin/jobs", map[string]string{"model_id": m.ID, "kind": "artifact", "template_id": "en_svg"}, cookie, csrf); duplicate.Code != 409 {
		t.Fatal("duplicate job accepted", duplicate.Code)
	}
	plans, err := s.Store.ListSchedules(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range plans {
		if p.Kind == "artifact" && p.Enabled {
			t.Fatal("disabled group has enabled schedule")
		}
	}
}

func TestScheduleAPIOmittedScopeNeverExpandsToAllModels(t *testing.T) {
	s, _, c, first := publicFixture(t)
	ctx := t.Context()
	second := first
	second.ID, second.UpstreamID = "second-scope-model", "different-upstream"
	if err := s.Store.SaveModelWithSchedules(ctx, second); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.SetIntelligenceSchedule(ctx, c.ID, domain.IntelligenceSchedule{Enabled: true, ModelScope: "all", TemplateIDs: []string{"cn_html"}, IntervalSeconds: 120}); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	cookie, csrf := loginForTest(t, h)
	path := "/api/v1/admin/channels/" + c.ID + "/intelligence-schedule"
	response := request(t, h, "PUT", path, map[string]any{"enabled": true, "model_id": first.ID, "template_ids": []string{"cn_html"}, "interval_seconds": 120}, cookie, csrf)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	saved, _ := s.Store.GetChannel(ctx, c.ID)
	if saved.IntelligenceSchedule.ModelScope != "selected" || saved.IntelligenceSchedule.ModelID != first.ID {
		t.Fatal("omitted scope retained or expanded all-model execution", saved.IntelligenceSchedule)
	}
	plans, _ := s.Store.ListSchedules(ctx, second.ID)
	for _, plan := range plans {
		if plan.Enabled {
			t.Fatal("unselected automatic plan remained active", plan)
		}
	}
	// A foreign model cannot become the selected target, and rejection must
	// preserve the already saved target and its schedules.
	foreignChannel := c
	foreignChannel.ID = "foreign-scope-channel"
	if err := s.Store.SaveChannel(ctx, foreignChannel); err != nil {
		t.Fatal(err)
	}
	foreign := first
	foreign.ID, foreign.ChannelID = "foreign-scope-model", foreignChannel.ID
	if err := s.Store.SaveModel(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	response = request(t, h, "PUT", path, map[string]any{"enabled": true, "model_id": foreign.ID, "template_ids": []string{"cn_html"}, "interval_seconds": 120}, cookie, csrf)
	if response.Code != 400 {
		t.Fatal("foreign selected model accepted", response.Code, response.Body.String())
	}
	saved, _ = s.Store.GetChannel(ctx, c.ID)
	if saved.IntelligenceSchedule.ModelID != first.ID {
		t.Fatal("rejected foreign selection partially changed the plan")
	}
}

func TestChannelEditRetainsArchivedTargetButRejectsNewArchivedSelection(t *testing.T) {
	s, _, c, model := publicFixture(t)
	ctx := t.Context()
	controls := domain.DefaultModelControls()
	c.ModelControls = &controls
	c.IntelligenceModelID = model.ID
	if err := s.Store.SaveChannelWithModelControls(ctx, c, false); err != nil {
		t.Fatal(err)
	}
	model.Archived = true
	if err := s.Store.SaveModelWithSchedules(ctx, model); err != nil {
		t.Fatal(err)
	}
	saved, _ := s.Store.GetChannel(ctx, c.ID)
	saved.Name = "rename with archived selection"
	h := s.Handler()
	cookie, csrf := loginForTest(t, h)
	response := request(t, h, "PUT", "/api/v1/admin/channels/"+c.ID, saved, cookie, csrf)
	if response.Code != 200 {
		t.Fatal("ordinary edit rejected its existing archived target", response.Code, response.Body.String())
	}
	saved, _ = s.Store.GetChannel(ctx, c.ID)
	if saved.IntelligenceSchedule.ModelID != model.ID || saved.IntelligenceModelID != model.ID {
		t.Fatal("ordinary edit replaced an archived target")
	}
	other := model
	other.ID, other.UpstreamID = "another-archived-target", "another-archived-upstream"
	if err := s.Store.SaveModel(ctx, other); err != nil {
		t.Fatal(err)
	}
	saved.IntelligenceModelID = other.ID
	response = request(t, h, "PUT", "/api/v1/admin/channels/"+c.ID, saved, cookie, csrf)
	if response.Code != 400 {
		t.Fatal("a newly chosen archived target was accepted", response.Code, response.Body.String())
	}
	plans, _ := s.Store.ListSchedules(ctx, model.ID)
	for _, plan := range plans {
		if plan.Enabled {
			t.Fatal("archived target kept an automatic plan enabled", plan)
		}
	}
}

func TestChangingModelVisibilityPreservesGroupSchedule(t *testing.T) {
	s, _, c, m := publicFixture(t)
	plan := domain.IntelligenceSchedule{Enabled: true, ModelID: m.ID, TemplateIDs: []string{"cn_html"}, IntervalSeconds: 1800}
	if _, err := s.Store.SetIntelligenceSchedule(context.Background(), c.ID, plan); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	cookie, csrf := loginForTest(t, h)
	m.Public = false
	w := request(t, h, "PUT", "/api/v1/admin/models/"+m.ID, m, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	saved := m
	saved.Public = true
	w = request(t, h, "PUT", "/api/v1/admin/models/"+m.ID, saved, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	got, err := s.Store.GetModel(context.Background(), m.ID)
	if err != nil || !got.Public {
		t.Fatal("model did not become public", err)
	}
	channel, err := s.Store.GetChannel(context.Background(), c.ID)
	if err != nil || channel.IntelligenceSchedule == nil || !channel.IntelligenceSchedule.Enabled || channel.IntelligenceSchedule.ModelID != m.ID {
		t.Fatal("visibility update changed the group-owned schedule", channel.IntelligenceSchedule, err)
	}
}

func TestPublicGroupShowsScheduledModelAndTemplateStatus(t *testing.T) {
	s, _, c, m := publicFixture(t)
	other := m
	other.ID = "group-selected"
	other.UpstreamID = "group-selected-upstream"
	if err := s.Store.SaveModel(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	c.IntelligenceModelID = m.ID
	if err := s.Store.SaveChannelWithSchedules(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.SetIntelligenceSchedule(context.Background(), c.ID, domain.IntelligenceSchedule{Enabled: true, ModelID: other.ID, TemplateIDs: []string{"en_svg"}, IntervalSeconds: 123}); err != nil {
		t.Fatal(err)
	}
	for _, template := range []string{"cn_html", "en_svg"} {
		w := publicGET(t, s, "/api/v1/public/intelligence?template_id="+template)
		var data struct {
			Groups []struct {
				ModelID  string  `json:"model_id"`
				Enabled  bool    `json:"automatic_enabled"`
				Interval int     `json:"interval_seconds"`
				Next     *string `json:"next_run_at"`
			} `json:"groups"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &data) != nil || len(data.Groups) != 1 {
			t.Fatal(w.Code, w.Body.String())
		}
		g := data.Groups[0]
		if g.ModelID != other.ID || g.Interval != 123 || g.Enabled != (template == "en_svg") || (g.Next != nil) != (template == "en_svg") {
			t.Fatalf("template=%s group=%+v", template, g)
		}
	}
}
