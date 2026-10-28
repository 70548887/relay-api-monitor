package server

import (
	"context"
	"encoding/json"
	"relaylab/internal/domain"
	"strings"
	"testing"
	"time"
)

func TestPrivateScheduledModelDoesNotMasqueradeAsDisabledOrLeak(t *testing.T) {
	s, _, ch, m := publicFixture(t)
	ctx := context.Background()
	m.Public = false
	m.UpstreamID = "PRIVATE-SELECTED-MODEL"
	if err := s.Store.SaveModelWithSchedules(ctx, m); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.SetIntelligenceSchedule(ctx, ch.ID, domain.IntelligenceSchedule{Enabled: true, ModelID: m.ID, TemplateIDs: []string{"cn_html"}, IntervalSeconds: 6000}); err != nil {
		t.Fatal(err)
	}
	artifactID := insertPublicArtifact(t, s, m.ID, ch.ID, "cn_html", false)
	response := publicGET(t, s, "/api/v1/public/intelligence")
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"schedule_state":"no_public_plan"`) || strings.Contains(response.Body.String(), m.UpstreamID) || strings.Contains(response.Body.String(), artifactID) {
		t.Fatal("public state or privacy", response.Code, response.Body.String())
	}
	h := s.Handler()
	if w := request(t, h, "GET", "/api/v1/admin/intelligence-status", nil, nil, ""); w.Code != 401 {
		t.Fatal("missing admin auth", w.Code)
	}
	cookie, _ := loginForTest(t, h)
	response = request(t, h, "GET", "/api/v1/admin/intelligence-status", nil, cookie, "")
	var status domain.IntelligenceStatusResponse
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if len(status.Groups) != 1 || len(status.Groups[0].Plans) != 1 || !status.Groups[0].Plans[0].Enabled || status.Groups[0].Plans[0].NextRunAt == nil || strings.Join(status.Groups[0].Plans[0].VisibilityReasons, "") != "模型未公开" {
		t.Fatalf("admin plan %+v", status)
	}
	m.Public = true
	if err := s.Store.SaveModelWithSchedules(ctx, m); err != nil {
		t.Fatal(err)
	}
	response = publicGET(t, s, "/api/v1/public/intelligence")
	if !strings.Contains(response.Body.String(), artifactID) || !strings.Contains(response.Body.String(), `"automatic_enabled":true`) {
		t.Fatal("existing artifact not visible after explicit publish", response.Body.String())
	}
}
func TestScheduleStatusExplainsExecutionFences(t *testing.T) {
	now := time.Now().UTC()
	p := domain.Platform{Public: true}
	ch := domain.Channel{Enabled: true, Public: true}
	m := domain.Model{ID: "m", Public: true}
	plan := domain.Schedule{Enabled: true, TemplateID: "cn_html", NextRunAt: now.Add(-time.Second)}
	for _, tc := range []struct {
		name           string
		online, paused bool
		active         []domain.Job
		want           string
	}{
		{"due", true, false, nil, "due"}, {"offline", false, false, nil, "worker_offline"}, {"maintenance", true, true, nil, "maintenance"},
		{"queued", true, false, []domain.Job{{ModelID: "m", Kind: "artifact", TemplateID: "cn_html", State: "queued"}}, "queued"},
		{"running", true, false, []domain.Job{{ModelID: "m", Kind: "artifact", TemplateID: "cn_html", State: "dispatching"}}, "running"},
		{"sibling", true, false, []domain.Job{{ModelID: "m", Kind: "artifact", TemplateID: "en_svg", State: "dispatching"}}, "waiting"},
		{"unknown", true, false, []domain.Job{{ModelID: "m", Kind: "artifact", State: "uncertain"}}, "cooldown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := planStatus(p, ch, m, plan, tc.active, tc.online, tc.paused, now)
			if got.State != tc.want || got.BlockingReason == "" {
				t.Fatalf("%+v", got)
			}
		})
	}
}

func TestUnconfiguredGroupLookupIsOptionalEvenAfterOldFailure(t *testing.T) {
	view := groupVerification(domain.Channel{GroupProvider: "none", GroupVerificationStatus: "failed", Revision: 1, GroupVerificationRevision: 1})
	if view["status"] != "unconfigured" || view["group"] != "" || !strings.Contains(view["message"].(string), "不影响") {
		t.Fatalf("%+v", view)
	}
}
