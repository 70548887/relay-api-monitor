package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"relaylab/internal/domain"
)

func readyFingerprintBank() *fakeFingerprintBank {
	updated := time.Date(2026, 11, 19, 6, 0, 0, 0, time.UTC)
	return &fakeFingerprintBank{
		status: domain.FingerprintBank{Ready: true, Source: "online", BuiltAt: "2026-11-19T05:26:09Z", Models: 2, UpdatedAt: &updated},
		models: []domain.FingerprintModel{{ID: "claude-sonnet-4.6", DisplayName: "claude-sonnet-4.6", Family: "claude", FamilyName: "Claude"}, {ID: "gpt-5.5", DisplayName: "gpt-5.5", Family: "gpt", FamilyName: "GPT"}},
	}
}

// finishFingerprintJob takes the queued fingerprint job through the steps of the worker and stores round for it.
func finishFingerprintJob(t *testing.T, s *Server, round domain.FingerprintResult) domain.Job {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	claimed, err := s.Store.ClaimJob(ctx, "fingerprint", "test-worker", now)
	if err != nil || claimed == nil {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	if err = s.Store.MarkDispatching(ctx, claimed.ID, claimed.LeaseToken, now.Add(time.Minute), "snapshot"); err != nil {
		t.Fatal(err)
	}
	job, err := s.Store.GetJob(ctx, claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	job.State = "succeeded"
	if err = s.Store.FinishFingerprintJob(ctx, *job, &round); err != nil {
		t.Fatal(err)
	}
	return *job
}

func adminFingerprintOverview(t *testing.T, h http.Handler, cookie *http.Cookie) (domain.AdminFingerprintOverview, string) {
	t.Helper()
	w := request(t, h, "GET", "/api/v1/admin/fingerprint", nil, cookie, "")
	var overview domain.AdminFingerprintOverview
	if err := json.Unmarshal(w.Body.Bytes(), &overview); err != nil || w.Code != 200 {
		t.Fatalf("overview: %d %s %v", w.Code, w.Body.String(), err)
	}
	return overview, w.Body.String()
}

func fingerprintRow(t *testing.T, overview domain.AdminFingerprintOverview, modelID string) domain.AdminFingerprintModel {
	t.Helper()
	for _, row := range overview.Models {
		if row.ID == modelID {
			return row
		}
	}
	t.Fatalf("model %s is not listed: %+v", modelID, overview.Models)
	return domain.AdminFingerprintModel{}
}

func errorMessage(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body %q: %v", w.Body.String(), err)
	}
	return body.Error.Message
}

func TestAdminFingerprintOverviewListsEveryModelWithItsDetectionState(t *testing.T) {
	s, p, ch, m := publicFixture(t)
	ctx := context.Background()
	paused := domain.Channel{ID: "paused-channel", PlatformID: p.ID, Name: "Paused Channel", Enabled: false, Protocol: "chat", EncryptedKey: "encrypted-key-private", BaseURL: "https://internal-upstream-private.example/v1"}
	if err := s.Store.SaveChannel(ctx, paused); err != nil {
		t.Fatal(err)
	}
	for _, model := range []domain.Model{
		{ID: "paused-model", ChannelID: paused.ID, UpstreamID: "paused-model-name", DisplayName: "Paused model", FingerprintMode: domain.FingerprintModeManual, FingerprintModel: "gpt-5.5", FingerprintQuestions: 1, FingerprintScheduled: true},
		{ID: "archived-model", ChannelID: ch.ID, UpstreamID: "archived-model-name", DisplayName: "Archived model", Archived: true},
	} {
		if err := s.Store.SaveModel(ctx, model); err != nil {
			t.Fatal(err)
		}
	}
	bank := readyFingerprintBank()
	bank.expect = func(model domain.Model) string {
		if model.FingerprintMode == domain.FingerprintModeManual {
			return model.FingerprintModel
		}
		return "claude-sonnet-4.6"
	}
	s.Fingerprint = bank
	h := s.Handler()
	if w := request(t, h, "GET", "/api/v1/admin/fingerprint", nil, nil, ""); w.Code != 401 {
		t.Fatalf("overview without a session: %d %s", w.Code, w.Body.String())
	}
	cookie, csrf := loginForTest(t, h)

	overview, _ := adminFingerprintOverview(t, h, cookie)
	if !overview.Bank.Ready || overview.Bank.BuiltAt != "2026-11-19T05:26:09Z" || len(overview.References) != 2 || overview.References[1].ID != "gpt-5.5" {
		t.Fatalf("bank = %+v, references = %+v", overview.Bank, overview.References)
	}
	if len(overview.Models) != 2 {
		t.Fatalf("listed %d models, want the two that are not archived: %+v", len(overview.Models), overview.Models)
	}
	row := fingerprintRow(t, overview, m.ID)
	if row.UpstreamID != "public-model-name" || row.DisplayName != "Public model" || row.ChannelID != ch.ID || row.ChannelName != "Public Channel" || row.PlatformID != p.ID || row.PlatformName != "Public Platform" {
		t.Fatalf("row names = %+v", row)
	}
	if !row.Runnable || row.Settings != (domain.FingerprintModelSettings{Mode: "auto", Questions: 3}) || row.Expected != "claude-sonnet-4.6" || row.Latest != nil || row.Job != nil {
		t.Fatalf("untouched model = %+v", row)
	}
	stopped := fingerprintRow(t, overview, "paused-model")
	if stopped.Runnable || stopped.Settings != (domain.FingerprintModelSettings{Mode: "manual", Model: "gpt-5.5", Questions: 1, Scheduled: true}) || stopped.Expected != "gpt-5.5" {
		t.Fatalf("model of a disabled channel = %+v", stopped)
	}

	w := request(t, h, "POST", "/api/v1/admin/jobs", map[string]string{"model_id": m.ID, "kind": "fingerprint", "template_id": "cn_html", "reasoning_effort": "high"}, cookie, csrf)
	var queued domain.Job
	if err := json.Unmarshal(w.Body.Bytes(), &queued); err != nil || w.Code != 201 {
		t.Fatalf("trigger: %d %s %v", w.Code, w.Body.String(), err)
	}
	if queued.Kind != "fingerprint" || queued.State != "queued" || queued.TemplateID != "" || queued.ReasoningEffort != "" {
		t.Fatalf("a detection job carries no template and no reasoning setting: %+v", queued)
	}
	overview, _ = adminFingerprintOverview(t, h, cookie)
	if row = fingerprintRow(t, overview, m.ID); row.Job == nil || row.Job.ID != queued.ID || row.Job.State != "queued" || row.Latest != nil {
		t.Fatalf("row with a queued job = %+v", row)
	}
	w = request(t, h, "POST", "/api/v1/admin/jobs", map[string]string{"model_id": m.ID, "kind": "fingerprint"}, cookie, csrf)
	if message := errorMessage(t, w); w.Code != 409 || errorCode(t, w) != "model_busy" || !strings.Contains(message, "指纹检测") || !strings.Contains(message, queued.ID[:8]) {
		t.Fatalf("second trigger: %d %s", w.Code, w.Body.String())
	}
	// Another kind of job on the same model is not held back by the detection.
	if w = request(t, h, "POST", "/api/v1/admin/jobs", map[string]string{"model_id": m.ID, "kind": "probe"}, cookie, csrf); w.Code != 201 {
		t.Fatalf("probe beside a detection: %d %s", w.Code, w.Body.String())
	}

	finishFingerprintJob(t, s, domain.FingerprintResult{Questions: 3, Analysis: domain.FingerprintAnalysis{Verdict: domain.FingerprintMatch, Level: domain.FingerprintLevelFull, ValidAnswers: 3}})
	overview, _ = adminFingerprintOverview(t, h, cookie)
	row = fingerprintRow(t, overview, m.ID)
	if row.Job == nil || row.Job.ID != queued.ID || row.Job.State != "succeeded" || row.Latest == nil || row.Latest.JobID != queued.ID || row.Latest.Analysis.Verdict != domain.FingerprintMatch {
		t.Fatalf("row after the round: job %+v, latest %+v", row.Job, row.Latest)
	}

	bank.status, bank.models, bank.asked = domain.FingerprintBank{Source: "online", Error: "download failed: host unreachable"}, nil, nil
	overview, body := adminFingerprintOverview(t, h, cookie)
	if overview.Bank.Ready || overview.Bank.Error != "download failed: host unreachable" || !strings.Contains(body, `"references":[]`) {
		t.Fatalf("an administrator sees why the bank is not ready: %s", body)
	}
	if row = fingerprintRow(t, overview, m.ID); row.Expected != "" || len(bank.asked) != 0 || row.Latest == nil {
		t.Fatalf("row without a bank = %+v, asked %d", row, len(bank.asked))
	}

	s.Fingerprint = nil
	if overview, _ = adminFingerprintOverview(t, h, cookie); overview.Bank.Ready || len(overview.Models) != 2 {
		t.Fatalf("overview without a bank = %+v", overview)
	}
}

func TestSaveFingerprintModelSettings(t *testing.T) {
	s, _, _, m := publicFixture(t)
	ctx := context.Background()
	bank := readyFingerprintBank()
	s.Fingerprint = bank
	h := s.Handler()
	cookie, csrf := loginForTest(t, h)
	path := "/api/v1/admin/fingerprint/models/" + m.ID
	stored := func() domain.Model {
		t.Helper()
		model, err := s.Store.GetModel(ctx, m.ID)
		if err != nil {
			t.Fatal(err)
		}
		return *model
	}

	w := request(t, h, "PUT", path, map[string]any{"mode": "manual", "model": " gpt-5.5 ", "questions": 2, "scheduled": true}, cookie, csrf)
	var saved domain.FingerprintModelSettings
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil || w.Code != 200 {
		t.Fatalf("save: %d %s %v", w.Code, w.Body.String(), err)
	}
	want := domain.FingerprintModelSettings{Mode: "manual", Model: "gpt-5.5", Questions: 2, Scheduled: true}
	if saved != want {
		t.Fatalf("saved %+v, want %+v", saved, want)
	}
	model := stored()
	if model.FingerprintMode != "manual" || model.FingerprintModel != "gpt-5.5" || model.FingerprintQuestions != 2 || !model.FingerprintScheduled {
		t.Fatalf("stored fingerprint settings = %+v", model)
	}
	if model.UpstreamID != m.UpstreamID || model.DisplayName != m.DisplayName || !model.Public || !model.ProbeEnabled || model.ChannelID != m.ChannelID {
		t.Fatalf("saving the fingerprint settings changed other fields: %+v", model)
	}
	events, err := s.Store.ListAudit(ctx)
	if err != nil || len(events) == 0 || events[0].Action != "fingerprint_model_saved" || events[0].EntityID != m.ID || !strings.Contains(events[0].Detail, "model=gpt-5.5") {
		t.Fatalf("audit = %+v, %v", events, err)
	}

	// The other modes keep no picked model, and a missing question count is a full round.
	w = request(t, h, "PUT", path, map[string]any{"mode": "unlisted", "model": "gpt-5.5"}, cookie, csrf)
	if model = stored(); w.Code != 200 || model.FingerprintMode != "unlisted" || model.FingerprintModel != "" || model.FingerprintQuestions != 3 || model.FingerprintScheduled {
		t.Fatalf("unlisted: %d %s, stored %+v", w.Code, w.Body.String(), model)
	}
	w = request(t, h, "PUT", path, map[string]any{"questions": 1}, cookie, csrf)
	if model = stored(); w.Code != 200 || model.FingerprintMode != "auto" || model.FingerprintQuestions != 1 {
		t.Fatalf("a missing mode is auto: %d %s, stored %+v", w.Code, w.Body.String(), model)
	}

	before := stored()
	for name, c := range map[string]struct {
		body   map[string]any
		status int
		code   string
	}{
		"unknown mode":         {map[string]any{"mode": "guess", "questions": 3}, 400, "invalid_fingerprint_settings"},
		"too many questions":   {map[string]any{"mode": "auto", "questions": 4}, 400, "invalid_fingerprint_settings"},
		"negative questions":   {map[string]any{"mode": "auto", "questions": -1}, 400, "invalid_fingerprint_settings"},
		"manual without model": {map[string]any{"mode": "manual", "questions": 3}, 400, "invalid_fingerprint_model"},
		"model not in bank":    {map[string]any{"mode": "manual", "model": "gpt-9", "questions": 3}, 400, "invalid_fingerprint_model"},
	} {
		w = request(t, h, "PUT", path, c.body, cookie, csrf)
		if w.Code != c.status || errorCode(t, w) != c.code {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	if w = request(t, h, "PUT", "/api/v1/admin/fingerprint/models/no-such-model", map[string]any{"mode": "auto"}, cookie, csrf); w.Code != 404 {
		t.Fatalf("unknown model: %d %s", w.Code, w.Body.String())
	}
	if w = request(t, h, "PUT", path, map[string]any{"mode": "auto"}, cookie, ""); w.Code != 403 {
		t.Fatalf("save without the CSRF token: %d %s", w.Code, w.Body.String())
	}
	// Whether a picked model exists cannot be told without the bank; the other modes do not need it.
	bank.status, bank.models = domain.FingerprintBank{Source: "online", Error: "download failed"}, nil
	w = request(t, h, "PUT", path, map[string]any{"mode": "manual", "model": "gpt-5.5", "questions": 3}, cookie, csrf)
	if w.Code != 503 || errorCode(t, w) != "fingerprint_unavailable" {
		t.Fatalf("manual without a bank: %d %s", w.Code, w.Body.String())
	}
	if after := stored(); after.FingerprintMode != before.FingerprintMode || after.FingerprintModel != before.FingerprintModel || after.FingerprintQuestions != before.FingerprintQuestions || after.FingerprintScheduled != before.FingerprintScheduled {
		t.Fatalf("a rejected save changed the model: %+v", after)
	}
	w = request(t, h, "PUT", path, map[string]any{"mode": "unlisted", "questions": 2, "scheduled": true}, cookie, csrf)
	if model = stored(); w.Code != 200 || model.FingerprintMode != "unlisted" || model.FingerprintQuestions != 2 || !model.FingerprintScheduled {
		t.Fatalf("unlisted without a bank: %d %s, stored %+v", w.Code, w.Body.String(), model)
	}
}

func TestAdminFingerprintResultsListTheRoundsOfOneModel(t *testing.T) {
	s, _, _, m := publicFixture(t)
	ctx := context.Background()
	h := s.Handler()
	cookie, _ := loginForTest(t, h)
	path := "/api/v1/admin/fingerprint/models/" + m.ID + "/results"
	if w := request(t, h, "GET", path, nil, cookie, ""); w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("a model without rounds: %d %s", w.Code, w.Body.String())
	}
	base := time.Now().UTC().Add(-time.Hour)
	for i, verdict := range []string{domain.FingerprintMismatch, domain.FingerprintMatch} {
		if _, err := s.Store.EnqueueJob(ctx, domain.Job{ModelID: m.ID, Kind: "fingerprint"}); err != nil {
			t.Fatal(err)
		}
		finishFingerprintJob(t, s, domain.FingerprintResult{Questions: 3, Analysis: domain.FingerprintAnalysis{Verdict: verdict}, CreatedAt: base.Add(time.Duration(i) * time.Minute)})
	}
	w := request(t, h, "GET", path, nil, cookie, "")
	var rounds []domain.FingerprintResult
	if err := json.Unmarshal(w.Body.Bytes(), &rounds); err != nil || w.Code != 200 {
		t.Fatalf("results: %d %s %v", w.Code, w.Body.String(), err)
	}
	if len(rounds) != 2 || rounds[0].Analysis.Verdict != domain.FingerprintMatch || rounds[1].Analysis.Verdict != domain.FingerprintMismatch || rounds[0].ModelID != m.ID {
		t.Fatalf("rounds, newest first = %+v", rounds)
	}
	if w = request(t, h, "GET", "/api/v1/admin/fingerprint/models/no-such-model/results", nil, cookie, ""); w.Code != 404 {
		t.Fatalf("unknown model: %d %s", w.Code, w.Body.String())
	}
	if w = request(t, h, "GET", path, nil, nil, ""); w.Code != 401 {
		t.Fatalf("results without a session: %d %s", w.Code, w.Body.String())
	}
}

func TestRefreshFingerprintBank(t *testing.T) {
	s, _, _, _ := publicFixture(t)
	ctx := context.Background()
	h := s.Handler()
	cookie, csrf := loginForTest(t, h)
	path := "/api/v1/admin/fingerprint/bank/refresh"
	if w := request(t, h, "POST", path, nil, cookie, csrf); w.Code != 503 || errorCode(t, w) != "fingerprint_unavailable" {
		t.Fatalf("refresh without a bank: %d %s", w.Code, w.Body.String())
	}

	bank := readyFingerprintBank()
	updated := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	bank.refreshed, bank.changed = domain.FingerprintBank{Ready: true, Source: "online", BuiltAt: "2026-11-20T07:00:00Z", Models: 3, UpdatedAt: &updated}, true
	s.Fingerprint = bank
	w := request(t, h, "POST", path, nil, cookie, csrf)
	var refresh domain.FingerprintBankRefresh
	if err := json.Unmarshal(w.Body.Bytes(), &refresh); err != nil || w.Code != 200 {
		t.Fatalf("refresh: %d %s %v", w.Code, w.Body.String(), err)
	}
	if !refresh.Changed || !refresh.Bank.Ready || refresh.Bank.BuiltAt != "2026-11-20T07:00:00Z" || refresh.Bank.Models != 3 || bank.refreshes != 1 {
		t.Fatalf("refresh = %+v after %d downloads", refresh, bank.refreshes)
	}
	events, err := s.Store.ListAudit(ctx)
	if err != nil || len(events) == 0 || events[0].Action != "fingerprint_bank_refreshed" || events[0].Detail != "2026-11-20T07:00:00Z" {
		t.Fatalf("audit = %+v, %v", events, err)
	}
	if w = request(t, h, "POST", path, nil, cookie, ""); w.Code != 403 || bank.refreshes != 1 {
		t.Fatalf("refresh without the CSRF token: %d %s", w.Code, w.Body.String())
	}

	bank.refreshErr = errors.New("fingerprint: reference bank unavailable: host unreachable")
	w = request(t, h, "POST", path, nil, cookie, csrf)
	if w.Code != 502 || errorCode(t, w) != "fingerprint_refresh_failed" || !strings.Contains(errorMessage(t, w), "host unreachable") {
		t.Fatalf("failed refresh: %d %s", w.Code, w.Body.String())
	}
	bank.refreshErr = nil

	// While one download runs, a second request is turned away instead of starting another.
	bank.entered, bank.release = make(chan struct{}), make(chan struct{})
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- request(t, h, "POST", path, nil, cookie, csrf) }()
	<-bank.entered
	if w = request(t, h, "POST", path, nil, cookie, csrf); w.Code != 409 || errorCode(t, w) != "fingerprint_refresh_running" {
		t.Fatalf("refresh during a refresh: %d %s", w.Code, w.Body.String())
	}
	close(bank.release)
	if w = <-first; w.Code != 200 || bank.refreshes != 3 {
		t.Fatalf("the running refresh: %d %s after %d downloads", w.Code, w.Body.String(), bank.refreshes)
	}
	bank.entered, bank.release = nil, nil
	if w = request(t, h, "POST", path, nil, cookie, csrf); w.Code != 200 || bank.refreshes != 4 {
		t.Fatalf("refresh after the running one ended: %d %s", w.Code, w.Body.String())
	}

	// A local bank directory is read as it is; there is nothing to download.
	settings, err := s.Store.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.FingerprintBankDir = t.TempDir()
	if err = s.Store.SaveSettingsAndReconcile(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if w = request(t, h, "POST", path, nil, cookie, csrf); w.Code != 400 || errorCode(t, w) != "fingerprint_bank_local" || bank.refreshes != 4 {
		t.Fatalf("refresh with a local bank: %d %s after %d downloads", w.Code, w.Body.String(), bank.refreshes)
	}
}
