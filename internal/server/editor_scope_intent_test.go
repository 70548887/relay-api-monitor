package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"relaylab/internal/domain"
)

func TestEditorSelectedIntentAPIUsesSameRepresentativeToNarrowAll(t *testing.T) {
	s, _, channel, target := publicFixture(t)
	ctx := t.Context()
	channel.BaseURL = "https://example.com/v1"
	channel.IntelligenceModelID = target.ID
	controls := domain.DefaultModelControls()
	channel.ModelControls = &controls
	if err := s.Store.SaveChannelWithModelControls(ctx, channel, false); err != nil {
		t.Fatal(err)
	}
	other := target
	other.ID, other.UpstreamID = "editor-other", "editor-other"
	other.ProbeEnabled, other.ArtifactChinese = true, true
	if err := s.Store.SaveModelWithSchedules(ctx, other); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.SetIntelligenceSchedule(ctx, channel.ID, domain.IntelligenceSchedule{Enabled: true, ModelScope: "all", TemplateIDs: []string{"cn_html"}, IntervalSeconds: 120}); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	cookie, csrf := loginForTest(t, h)
	write := map[string]any{"platform_id": channel.PlatformID, "name": channel.Name, "base_url": channel.BaseURL, "protocol": channel.Protocol, "enabled": true, "public": true, "intelligence_model_id": target.ID, "selected_model_only": true}
	response := request(t, h, http.MethodPut, "/api/v1/admin/channels/"+channel.ID, write, cookie, csrf)
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	var saved domain.Channel
	if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.IntelligenceSchedule.ModelScope != "selected" || saved.IntelligenceSchedule.ModelID != target.ID {
		t.Fatal("HTTP save ignored explicit single-model intent", saved.IntelligenceSchedule)
	}
	plans, _ := s.Store.ListSchedules(ctx, other.ID)
	for _, plan := range plans {
		if plan.Enabled {
			t.Fatal("HTTP single-model save left another model enabled", plan)
		}
	}
}

func TestEditorSelectedIntentAPIClearReturnsToExactDefault(t *testing.T) {
	s, _, channel, custom := publicFixture(t)
	ctx := t.Context()
	channel.BaseURL = "https://example.com/v1"
	controls := domain.DefaultModelControls()
	channel.ModelControls = &controls
	if err := s.Store.SaveChannelWithModelControls(ctx, channel, false); err != nil {
		t.Fatal(err)
	}
	target := custom
	target.ID, target.UpstreamID = "editor-sol", "gpt-6.1-sol"
	target.ProbeEnabled, target.ArtifactChinese = true, true
	if err := s.Store.SaveModelWithSchedules(ctx, target); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.SetIntelligenceSchedule(ctx, channel.ID, domain.IntelligenceSchedule{Enabled: true, ModelScope: "selected", ModelID: custom.ID, TemplateIDs: []string{"cn_html"}, IntervalSeconds: 120}); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	cookie, csrf := loginForTest(t, h)
	write := map[string]any{"platform_id": channel.PlatformID, "name": channel.Name, "base_url": channel.BaseURL, "protocol": channel.Protocol, "enabled": true, "public": true, "intelligence_model_id": "", "selected_model_only": true}
	response := request(t, h, http.MethodPut, "/api/v1/admin/channels/"+channel.ID, write, cookie, csrf)
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	saved, _ := s.Store.GetChannel(ctx, channel.ID)
	if saved.IntelligenceSchedule.ModelScope != "selected" || saved.IntelligenceSchedule.ModelID != target.ID {
		t.Fatal("HTTP clear retained the stale custom target", saved.IntelligenceSchedule)
	}
}

func TestEditorSelectedIntentAPIPreservesExistingArchivedTarget(t *testing.T) {
	s, _, channel, target := publicFixture(t)
	ctx := t.Context()
	channel.BaseURL, channel.IntelligenceModelID = "https://example.com/v1", target.ID
	controls := domain.DefaultModelControls()
	channel.ModelControls = &controls
	if err := s.Store.SaveChannelWithModelControls(ctx, channel, false); err != nil {
		t.Fatal(err)
	}
	target.Archived = true
	if err := s.Store.SaveModelWithSchedules(ctx, target); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	cookie, csrf := loginForTest(t, h)
	write := map[string]any{"platform_id": channel.PlatformID, "name": "Rename with archived target", "base_url": channel.BaseURL, "protocol": channel.Protocol, "enabled": true, "intelligence_model_id": target.ID, "selected_model_only": true}
	response := request(t, h, http.MethodPut, "/api/v1/admin/channels/"+channel.ID, write, cookie, csrf)
	if response.Code != http.StatusOK {
		t.Fatal("ordinary editor save rejected its archived target", response.Code, response.Body.String())
	}
	saved, _ := s.Store.GetChannel(ctx, channel.ID)
	if saved.IntelligenceModelID != target.ID || saved.IntelligenceSchedule.ModelID != target.ID {
		t.Fatal("ordinary editor save replaced its archived target", saved.IntelligenceSchedule)
	}
	plans, _ := s.Store.ListSchedules(ctx, target.ID)
	for _, plan := range plans {
		if plan.Enabled {
			t.Fatal("archived target was scheduled again", plan)
		}
	}
}
