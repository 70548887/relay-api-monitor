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
