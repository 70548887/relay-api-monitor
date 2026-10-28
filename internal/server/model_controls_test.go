package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"relaylab/internal/domain"
)

func TestModelControlsNewAPIGroupAndModelsDefaultOn(t *testing.T) {
	s, h := testServer(t)
	cookie, csrf := loginForTest(t, h)
	ctx := context.Background()
	platform := domain.Platform{ID: "controls-platform", Name: "Controls platform", Public: true}
	if err := s.Store.SavePlatform(ctx, platform); err != nil {
		t.Fatal(err)
	}
	response := request(t, h, http.MethodPost, "/api/v1/admin/channels", map[string]any{
		"platform_id": platform.ID, "name": "New default group", "base_url": "https://example.com/v1", "protocol": "chat", "api_key": "fixture-not-used-for-network",
	}, cookie, csrf)
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	var channel domain.Channel
	if err := json.Unmarshal(response.Body.Bytes(), &channel); err != nil {
		t.Fatal(err)
	}
	if !channel.Enabled || !channel.Public || channel.ModelControls == nil || !channel.ModelControls.ProbeEnabled || !channel.ModelControls.ArtifactChinese || !channel.ModelControls.ArtifactEnglish {
		t.Fatalf("new group did not inherit enabled defaults: %+v", channel.ModelControls)
	}
	response = request(t, h, http.MethodPost, "/api/v1/admin/models", map[string]any{"channel_id": channel.ID, "upstream_id": "gpt-6.1-sol"}, cookie, csrf)
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	var model domain.Model
	if err := json.Unmarshal(response.Body.Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	if !model.Public || !model.ProbeEnabled || !model.ArtifactChinese || !model.ArtifactEnglish || model.MaxTokens == 0 || model.TimeoutSeconds == 0 {
		t.Fatalf("new model defaults not enabled: %+v", model)
	}
	saved, err := s.Store.GetChannel(ctx, channel.ID)
	if err != nil || saved.IntelligenceSchedule == nil || saved.IntelligenceSchedule.ModelScope != "selected" || saved.IntelligenceSchedule.ModelID != model.ID {
		t.Fatal("default model was not selected exclusively", saved, err)
	}
	plans, err := s.Store.ListSchedules(ctx, model.ID)
	if err != nil || len(plans) != 3 {
		t.Fatal(plans, err)
	}
	for _, plan := range plans {
		if !plan.Enabled {
			t.Fatalf("new model has a disabled schedule: %+v", plan)
		}
	}
	// No worker is run: this verifies persistence without a model API request.
	jobs, err := s.Store.ListJobs(ctx)
	if err != nil || len(jobs) != 0 {
		t.Fatal("saving defaults dispatched a model call", jobs, err)
	}
}

func TestModelControlsExplicitFalseAndDirtyValidation(t *testing.T) {
	s, _, channel, _ := publicFixture(t)
	h := s.Handler()
	cookie, csrf := loginForTest(t, h)
	response := request(t, h, http.MethodPost, "/api/v1/admin/models", map[string]any{
		"channel_id": channel.ID, "upstream_id": "explicitly-off", "public": false, "probe_enabled": false, "artifact_chinese": false, "artifact_english": false,
	}, cookie, csrf)
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	var model domain.Model
	if err := json.Unmarshal(response.Body.Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	if model.Public || model.ProbeEnabled || model.ArtifactChinese || model.ArtifactEnglish {
		t.Fatalf("explicit false was replaced by a default: %+v", model)
	}
	response = request(t, h, http.MethodPut, "/api/v1/admin/models/"+model.ID, map[string]any{
		"channel_id": channel.ID, "upstream_id": model.UpstreamID, "model_control_changes": []string{"artifact_chinese"},
	}, cookie, csrf)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_model_controls") {
		t.Fatal("missing dirty value accepted", response.Code, response.Body.String())
	}
}

func TestModelControlsNewModelInheritsClosedGroupAndCannotReopenParent(t *testing.T) {
	s, _, channel, _ := publicFixture(t)
	ctx := context.Background()
	channel.ModelControls = &domain.ModelControls{}
	if err := s.Store.SaveChannelWithModelControls(ctx, channel, true); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	cookie, csrf := loginForTest(t, h)
	response := request(t, h, http.MethodPost, "/api/v1/admin/models", map[string]any{"channel_id": channel.ID, "upstream_id": "inherits-group-off"}, cookie, csrf)
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	var model domain.Model
	if err := json.Unmarshal(response.Body.Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	if !model.Public || model.ProbeEnabled || model.ArtifactChinese || model.ArtifactEnglish {
		t.Fatalf("new model did not inherit group switches: %+v", model)
	}
	response = request(t, h, http.MethodPut, "/api/v1/admin/models/"+model.ID, map[string]any{
		"channel_id": channel.ID, "upstream_id": model.UpstreamID, "probe_enabled": true, "model_control_changes": []string{"probe_enabled"},
	}, cookie, csrf)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "model_controls_disabled") {
		t.Fatal("individual model reopened a closed group master", response.Code, response.Body.String())
	}
	stored, err := s.Store.GetModel(ctx, model.ID)
	if err != nil || stored.ProbeEnabled {
		t.Fatal("rejected parent override mutated the model", stored, err)
	}
}

func TestModelControlsLegacyPlanIndividualOffSurvivesSaveAndSync(t *testing.T) {
	s, _, channel, model := publicFixture(t)
	ctx := context.Background()
	model.ProbeEnabled, model.ArtifactChinese, model.ArtifactEnglish = false, false, false
	if err := s.Store.SaveModelWithSchedules(ctx, model); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.SetIntelligenceSchedule(ctx, channel.ID, domain.IntelligenceSchedule{Enabled: true, ModelID: model.ID, TemplateIDs: []string{"cn_html"}, IntervalSeconds: 120}); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Store.TickSchedules(ctx, time.Now().UTC().Add(time.Hour)); err != nil || n != 1 {
		t.Fatalf("legacy schedule should be enabled initially: %d %v", n, err)
	}
	h := s.Handler()
	cookie, csrf := loginForTest(t, h)
	response := request(t, h, http.MethodPut, "/api/v1/admin/models/"+model.ID, map[string]any{
		"channel_id": channel.ID, "upstream_id": model.UpstreamID, "artifact_chinese": false, "model_control_changes": []string{"artifact_chinese"},
	}, cookie, csrf)
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	current, err := s.Store.GetModel(ctx, model.ID)
	if err != nil || current.ArtifactOverrides == nil || current.ArtifactOverrides.Chinese == nil || *current.ArtifactOverrides.Chinese {
		t.Fatal("legacy off was not recorded", current, err)
	}
	plans, _ := s.Store.ListSchedules(ctx, model.ID)
	for _, plan := range plans {
		if plan.Kind == "artifact" && plan.TemplateID == "cn_html" && plan.Enabled {
			t.Fatal("individual switch did not disable the group-owned schedule")
		}
	}
	jobs, _ := s.Store.ListJobs(ctx)
	if len(jobs) != 1 || jobs[0].State != "canceled" {
		t.Fatal("unsent group job was not canceled", jobs)
	}
	// A stale general form carries an opposite raw value, but marks no control
	// changes. Neither ordinary save nor model discovery may undo the opt-out.
	response = request(t, h, http.MethodPut, "/api/v1/admin/models/"+model.ID, map[string]any{
		"channel_id": channel.ID, "upstream_id": model.UpstreamID, "display_name": "Renamed only", "artifact_chinese": true, "model_control_changes": []string{},
	}, cookie, csrf)
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	if err = s.Store.SyncModels(ctx, channel.ID, []string{model.UpstreamID}); err != nil {
		t.Fatal(err)
	}
	current, err = s.Store.GetModel(ctx, model.ID)
	channelCurrent, channelErr := s.Store.GetChannel(ctx, channel.ID)
	if err != nil || channelErr != nil || current.DisplayName != "Renamed only" || channelCurrent.ScheduledArtifactEnabled(*current, "cn_html") {
		t.Fatal("rename or discovery revived an individual opt-out", current, err, channelErr)
	}
	if n, err := s.Store.TickSchedules(ctx, time.Now().UTC().Add(2*time.Hour)); err != nil || n != 0 {
		t.Fatalf("disabled task was dispatched again: %d %v", n, err)
	}
}

func TestModelControlsGroupApplyPublishesAllAndOrdinarySaveKeepsException(t *testing.T) {
	s, _, channel, model := publicFixture(t)
	ctx := context.Background()
	model.Public, model.ProbeEnabled, model.ArtifactChinese, model.ArtifactEnglish = false, false, false, false
	if err := s.Store.SaveModelWithSchedules(ctx, model); err != nil {
		t.Fatal(err)
	}
	channel.BaseURL = "https://example.com/v1"
	if err := s.Store.SaveChannelWithSchedules(ctx, channel); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	cookie, csrf := loginForTest(t, h)
	write := map[string]any{"platform_id": channel.PlatformID, "name": channel.Name, "base_url": channel.BaseURL, "protocol": channel.Protocol, "enabled": true, "public": true, "model_controls": domain.DefaultModelControls(), "apply_model_controls": true}
	response := request(t, h, http.MethodPut, "/api/v1/admin/channels/"+channel.ID, write, cookie, csrf)
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	current, err := s.Store.GetModel(ctx, model.ID)
	if err != nil || !current.Public || !current.ProbeEnabled || !current.ArtifactChinese || !current.ArtifactEnglish {
		t.Fatal("group apply did not enable all model controls", current, err)
	}
	current.ProbeEnabled = false
	if err = s.Store.SaveModelWithSchedules(ctx, *current); err != nil {
		t.Fatal(err)
	}
	delete(write, "apply_model_controls")
	response = request(t, h, http.MethodPut, "/api/v1/admin/channels/"+channel.ID, write, cookie, csrf)
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	current, err = s.Store.GetModel(ctx, model.ID)
	if err != nil || current.ProbeEnabled {
		t.Fatal("ordinary unchanged group save reopened individual probe", current, err)
	}
}

func TestModelControlsAllScopePublicPlanIgnoresPrivateRepresentative(t *testing.T) {
	s, _, channel, private := publicFixture(t)
	ctx := context.Background()
	private.Public, private.ArtifactChinese, private.ProbeEnabled = false, true, false
	private.UpstreamID = "private-representative-name"
	if err := s.Store.SaveModelWithSchedules(ctx, private); err != nil {
		t.Fatal(err)
	}
	visible := private
	visible.ID, visible.UpstreamID, visible.DisplayName, visible.Public = "visible-model", "visible-model-name", "Visible model", true
	if err := s.Store.SaveModelWithSchedules(ctx, visible); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.SetIntelligenceSchedule(ctx, channel.ID, domain.IntelligenceSchedule{Enabled: true, ModelScope: "all", ModelID: private.ID, TemplateIDs: []string{"cn_html"}, IntervalSeconds: 120}); err != nil {
		t.Fatal(err)
	}
	response := publicGET(t, s, "/api/v1/public/intelligence?template_id=cn_html")
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), private.UpstreamID) || strings.Contains(response.Body.String(), private.ID) {
		t.Fatal("all-scope representative leaked or public response failed", response.Code, response.Body.String())
	}
	var data struct {
		Groups []struct {
			ModelID   string `json:"model_id"`
			Automatic bool   `json:"automatic_enabled"`
			State     string `json:"schedule_state"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Groups) != 1 || data.Groups[0].ModelID != visible.ID || !data.Groups[0].Automatic || data.Groups[0].State == "no_public_plan" {
		t.Fatalf("private display representative hid valid all-model plans: %+v", data)
	}
	h := s.Handler()
	cookie, _ := loginForTest(t, h)
	response = request(t, h, http.MethodGet, "/api/v1/admin/intelligence-status", nil, cookie, "")
	var status domain.IntelligenceStatusResponse
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if len(status.Groups) != 1 || len(status.Groups[0].Plans) != 2 {
		t.Fatalf("administrator all-scope plans missing a model: %+v", status)
	}
}
