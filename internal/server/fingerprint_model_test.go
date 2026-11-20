package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"relaylab/internal/domain"
)

func TestSavingAModelKeepsItsFingerprintOptions(t *testing.T) {
	s, _, channel, _ := publicFixture(t)
	h := s.Handler()
	cookie, csrf := loginForTest(t, h)
	ctx := context.Background()
	save := func(method, path string, body map[string]any) domain.Model {
		t.Helper()
		response := request(t, h, method, path, body, cookie, csrf)
		if response.Code != http.StatusOK {
			t.Fatal(response.Code, response.Body.String())
		}
		var model domain.Model
		if err := json.Unmarshal(response.Body.Bytes(), &model); err != nil {
			t.Fatal(err)
		}
		return model
	}
	unset := func(m domain.Model) bool {
		return m.FingerprintMode == "" && m.FingerprintModel == "" && m.FingerprintQuestions == 0 && !m.FingerprintScheduled
	}
	kept := func(m domain.Model) bool {
		return m.FingerprintMode == domain.FingerprintModeManual && m.FingerprintModel == "reference-model" && m.FingerprintQuestions == 2 && m.FingerprintScheduled
	}

	model := save(http.MethodPost, "/api/v1/admin/models", map[string]any{
		"channel_id": channel.ID, "upstream_id": "fingerprint-options",
		"fingerprint_mode": "manual", "fingerprint_model": "other-model", "fingerprint_questions": 1, "fingerprint_scheduled": true,
	})
	if !unset(model) {
		t.Fatalf("a new model took fingerprint options from the model endpoint: %+v", model)
	}

	stored, err := s.Store.GetModel(ctx, model.ID)
	if err != nil || !unset(*stored) {
		t.Fatal(stored, err)
	}
	stored.FingerprintMode, stored.FingerprintModel, stored.FingerprintQuestions, stored.FingerprintScheduled = domain.FingerprintModeManual, "reference-model", 2, true
	if err = s.Store.SaveModel(ctx, *stored); err != nil {
		t.Fatal(err)
	}

	renamed := save(http.MethodPut, "/api/v1/admin/models/"+model.ID, map[string]any{"channel_id": channel.ID, "upstream_id": model.UpstreamID, "display_name": "Renamed"})
	if renamed.DisplayName != "Renamed" || !kept(renamed) {
		t.Fatalf("saving without fingerprint options dropped them: %+v", renamed)
	}
	overwritten := save(http.MethodPut, "/api/v1/admin/models/"+model.ID, map[string]any{
		"channel_id": channel.ID, "upstream_id": model.UpstreamID, "display_name": "Renamed again",
		"fingerprint_mode": "unlisted", "fingerprint_model": "", "fingerprint_questions": 3, "fingerprint_scheduled": false,
	})
	if overwritten.DisplayName != "Renamed again" || !kept(overwritten) {
		t.Fatalf("the model endpoint changed the fingerprint options: %+v", overwritten)
	}
	stored, err = s.Store.GetModel(ctx, model.ID)
	if err != nil || stored.DisplayName != "Renamed again" || !kept(*stored) {
		t.Fatal(stored, err)
	}
}
