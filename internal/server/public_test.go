package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"relaylab/internal/domain"
	"relaylab/internal/security"
	"relaylab/internal/store"
)

func publicFixture(t *testing.T) (*Server, domain.Platform, domain.Channel, domain.Model) {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	p := domain.Platform{ID: "public-platform", Name: "Public Platform", Public: true}
	ch := domain.Channel{ID: "public-channel", PlatformID: p.ID, Name: "Public Channel", Public: true, Enabled: true, Protocol: "chat", EncryptedKey: "encrypted-key-private", KeyMask: "key-mask-private", BaseURL: "https://internal-upstream-private.example/v1"}
	model := domain.Model{ID: "public-model", ChannelID: ch.ID, UpstreamID: "public-model-name", DisplayName: "Public model", Public: true, ProbeEnabled: true}
	for _, save := range []func() error{func() error { return db.SavePlatform(ctx, p) }, func() error { return db.SaveChannel(ctx, ch) }, func() error { return db.SaveModel(ctx, model) }} {
		if err = save(); err != nil {
			t.Fatal(err)
		}
	}
	vault, e := security.LoadVault(t.TempDir(), false)
	if e != nil {
		t.Fatal(e)
	}
	return New(db, vault), p, ch, model
}

func publicGET(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	return response
}
func insertPublicProbe(t *testing.T, s *Server, modelID, status string, at time.Time) {
	t.Helper()
	id := domain.NewID()
	probe := domain.ProbeResult{ID: id, ModelID: modelID, Status: status, CreatedAt: at, ErrorMessage: "Bearer private-secret-from-provider"}
	raw, _ := json.Marshal(probe)
	_, err := s.Store.DB.Exec(`INSERT INTO probe_results(id,job_id,model_id,created_at,data) VALUES(?,?,?,?,?)`, id, "probe-job-"+id, modelID, at.UTC().Format(databaseTimeFormat), string(raw))
	if err != nil {
		t.Fatal(err)
	}
}
func insertPublicArtifact(t *testing.T, s *Server, modelID, channelID, templateID string, hidden bool) string {
	t.Helper()
	id := domain.NewID()
	at := time.Now().UTC()
	a := domain.Artifact{ID: id, ModelID: modelID, ChannelID: channelID, TemplateID: templateID, Hidden: hidden, Code: "<html><body>public art<script>document.body.dataset.ready='yes'</script></body></html>", RawResponse: "raw-secret-from-provider", Parameters: "private-parameters", Kind: "html", CreatedAt: at}
	raw, _ := json.Marshal(a)
	_, err := s.Store.DB.Exec(`INSERT INTO artifacts(id,job_id,model_id,template_id,created_at,hidden,data) VALUES(?,?,?,?,?,?,?)`, id, "artifact-job-"+id, modelID, templateID, at.Format(databaseTimeFormat), hidden, string(raw))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestPublicMonitorAggregationAndSecretFiltering(t *testing.T) {
	s, p, ch, model := publicFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, probe := range []struct {
		status string
		at     time.Time
	}{{"success", now.Add(-time.Minute)}, {"slow", now.Add(-time.Hour)}, {"failed", now.Add(-2 * time.Hour)}, {"failed", now.Add(-8 * 24 * time.Hour)}} {
		insertPublicProbe(t, s, model.ID, probe.status, probe.at)
	}
	privatePlatform := p
	privatePlatform.ID = "private-platform"
	privatePlatform.Name = "PRIVATE-PLATFORM-NAME"
	privatePlatform.Public = false
	if err := s.Store.SavePlatform(ctx, privatePlatform); err != nil {
		t.Fatal(err)
	}
	privateChannel := ch
	privateChannel.ID = "private-channel"
	privateChannel.PlatformID = privatePlatform.ID
	privateChannel.Name = "PRIVATE-CHANNEL-NAME"
	if err := s.Store.SaveChannel(ctx, privateChannel); err != nil {
		t.Fatal(err)
	}
	privateModel := model
	privateModel.ID = "private-model"
	privateModel.ChannelID = privateChannel.ID
	privateModel.UpstreamID = "PRIVATE-MODEL-NAME"
	if err := s.Store.SaveModel(ctx, privateModel); err != nil {
		t.Fatal(err)
	}
	response := publicGET(t, s, "/api/v1/public/monitor")
	if response.Code != 200 {
		t.Fatalf("%d %s", response.Code, response.Body.String())
	}
	for _, secret := range []string{"encrypted-key-private", "key-mask-private", "internal-upstream-private", "PRIVATE-PLATFORM-NAME", "PRIVATE-CHANNEL-NAME", "PRIVATE-MODEL-NAME", "private-secret-from-provider"} {
		if strings.Contains(response.Body.String(), secret) {
			t.Errorf("exposed %s", secret)
		}
	}
	var payload struct {
		Models []publicModel `json:"models"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Models) != 1 {
		t.Fatalf("models=%d", len(payload.Models))
	}
	view := payload.Models[0]
	if view.Status != "ok" || view.LastProbe == nil || view.LastProbe.Status != "ok" || view.Samples != 3 || view.SuccessRate == nil || *view.SuccessRate < 66 || *view.SuccessRate > 67 || len(view.Timeline) != 24 {
		t.Fatalf("bad aggregate: %+v", view)
	}
	if response := publicGET(t, s, "/api/v1/public/models/private-model/history"); response.Code != 404 {
		t.Fatalf("private history status=%d", response.Code)
	}
	ch.Enabled = false
	if err := s.Store.SaveChannel(ctx, ch); err != nil {
		t.Fatal(err)
	}
	response = publicGET(t, s, "/api/v1/public/monitor")
	if !strings.Contains(response.Body.String(), `"status":"disabled"`) {
		t.Fatal(response.Body.String())
	}
}

func TestPublicArtifactsDetailAndDownload(t *testing.T) {
	s, _, _, model := publicFixture(t)
	publicID := insertPublicArtifact(t, s, model.ID, model.ChannelID, "cn_html", false)
	hiddenID := insertPublicArtifact(t, s, model.ID, model.ChannelID, "cn_html", true)
	response := publicGET(t, s, "/api/v1/public/artifacts?template_id=cn_html")
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	body := response.Body.String()
	for _, secret := range []string{hiddenID, "raw-secret-from-provider", "private-parameters", `"code":`, `"raw_response":`} {
		if strings.Contains(body, secret) {
			t.Errorf("list exposed %s", secret)
		}
	}
	response = publicGET(t, s, "/api/v1/public/artifacts/"+publicID)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "preview_document") || !strings.Contains(response.Body.String(), `"code":`) {
		t.Fatal(response.Body.String())
	}
	if strings.Contains(response.Body.String(), "raw-secret-from-provider") {
		t.Error("raw response exposed")
	}
	if response = publicGET(t, s, "/api/v1/public/artifacts/"+hiddenID); response.Code != 404 {
		t.Fatalf("hidden status=%d", response.Code)
	}
	response = publicGET(t, s, "/api/v1/public/artifacts/"+publicID+"/source")
	if response.Code != 200 || !strings.HasPrefix(response.Header().Get("Content-Type"), "text/plain") || !strings.HasPrefix(response.Header().Get("Content-Disposition"), "attachment") || response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("unsafe source response: %+v", response.Header())
	}
}

func TestLastAttemptExposesOnlyPublicFields(t *testing.T) {
	s, _, _, model := publicFixture(t)
	insertPublicArtifact(t, s, model.ID, model.ChannelID, "cn_html", false)
	job := domain.Job{ID: "failed-latest-job", ModelID: model.ID, ChannelID: model.ChannelID, Kind: "artifact", TemplateID: "cn_html", State: "failed", Owner: "private-owner", Snapshot: "private-key-snapshot", ErrorMessage: "Bearer private-original-key", ErrorCode: "upstream_error", CreatedAt: time.Now().UTC()}
	raw, _ := json.Marshal(job)
	_, err := s.Store.DB.Exec(`INSERT INTO jobs(id,model_id,channel_id,kind,state,owner,lease_token,scheduled_at,created_at,data) VALUES(?,?,?,?,?,?,?,?,?,?)`, job.ID, job.ModelID, job.ChannelID, job.Kind, job.State, job.Owner, "private-lease-token", job.CreatedAt.Format(databaseTimeFormat), job.CreatedAt.Format(databaseTimeFormat), string(raw))
	if err != nil {
		t.Fatal(err)
	}
	response := publicGET(t, s, "/api/v1/public/artifacts")
	if response.Code != 200 || !strings.Contains(response.Body.String(), "failed-latest-job") {
		t.Fatal(response.Body.String())
	}
	for _, secret := range []string{"private-owner", "private-key-snapshot", "private-original-key", "private-lease-token"} {
		if strings.Contains(response.Body.String(), secret) {
			t.Error(fmt.Sprintf("exposed %s", secret))
		}
	}
}

func TestMonitorNoSamplesUsesNullRateAndVisibilityChanges(t *testing.T) {
	s, _, ch, model := publicFixture(t)
	response := publicGET(t, s, "/api/v1/public/monitor")
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"success_rate":null`) || !strings.Contains(response.Body.String(), `"last_probe":null`) || !strings.Contains(response.Body.String(), `"status":"unknown"`) {
		t.Fatal(response.Body.String())
	}
	ctx := context.Background()
	id := insertPublicArtifact(t, s, model.ID, model.ChannelID, "cn_html", false)
	model.Public = false
	if err := s.Store.SaveModel(ctx, model); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/public/models/" + model.ID + "/history", "/api/v1/public/artifacts/" + id, "/api/v1/public/artifacts/" + id + "/source"} {
		if response := publicGET(t, s, path); response.Code != 404 {
			t.Errorf("private model leaked via %s: %d", path, response.Code)
		}
	}
	model.Public = true
	if err := s.Store.SaveModel(ctx, model); err != nil {
		t.Fatal(err)
	}
	ch.Public = false
	if err := s.Store.SaveChannel(ctx, ch); err != nil {
		t.Fatal(err)
	}
	response = publicGET(t, s, "/api/v1/public/monitor")
	if strings.Contains(response.Body.String(), model.UpstreamID) || strings.Contains(response.Body.String(), ch.Name) {
		t.Fatal("private channel or model leaked")
	}
}
