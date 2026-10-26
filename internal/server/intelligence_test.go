package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"relaylab/internal/domain"
	"relaylab/internal/upstream"
)

type intelligenceResponse struct {
	Groups []struct {
		ChannelID       string                                `json:"channel_id"`
		ModelID         string                                `json:"model_id"`
		ReasoningEffort string                                `json:"reasoning_effort"`
		Automatic       bool                                  `json:"automatic_enabled"`
		NextRun         *time.Time                            `json:"next_run_at"`
		Verification    struct{ Status, Group, Scope string } `json:"group_verification"`
		Latest          map[string]any                        `json:"latest_attempt"`
		History         []map[string]any                      `json:"history"`
	} `json:"groups"`
	HistoryScope string `json:"history_scope"`
}

func readIntelligence(t *testing.T, s *Server, query string) intelligenceResponse {
	t.Helper()
	w := publicGET(t, s, "/api/v1/public/intelligence"+query)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	for _, secret := range []string{"encrypted-key-private", "key-mask-private", "internal-upstream-private", "snapshot-secret", "private-api-key", "private-model-name"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Errorf("public intelligence leaked %s", secret)
		}
	}
	var response intelligenceResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func insertIntelligenceJob(t *testing.T, s *Server, job domain.Job) {
	t.Helper()
	raw, _ := json.Marshal(job)
	at := job.CreatedAt.UTC().Format(databaseTimeFormat)
	_, err := s.Store.DB.Exec(`INSERT INTO jobs(id,model_id,channel_id,kind,state,scheduled_at,created_at,data) VALUES(?,?,?,?,?,?,?,?)`, job.ID, job.ModelID, job.ChannelID, job.Kind, job.State, at, at, string(raw))
	if err != nil {
		t.Fatal(err)
	}
}

func TestPublicIntelligenceIncludesAttemptsWithoutSuccessfulArtifacts(t *testing.T) {
	s, _, ch, m := publicFixture(t)
	ctx := context.Background()
	ch.ReasoningEffort, ch.IntelligenceModelID, ch.GroupProvider = "high", m.ID, "newapi"
	if err := s.Store.SaveChannel(ctx, ch); err != nil {
		t.Fatal(err)
	}
	current, _ := s.Store.GetChannel(ctx, ch.ID)
	if err := s.Store.SetGroupVerification(ctx, ch.ID, current.Revision, "verified", "auto", "newapi", "private-api-key", time.Now()); err != nil {
		t.Fatal(err)
	}
	m.ArtifactChinese = true
	if err := s.Store.SaveModelWithSchedules(ctx, m); err != nil {
		t.Fatal(err)
	}
	progressAt := time.Now().UTC()
	insertIntelligenceJob(t, s, domain.Job{ID: "running-artifact", ModelID: m.ID, ChannelID: ch.ID, TemplateID: "cn_html", Kind: "artifact", State: "dispatching", CreatedAt: time.Now().UTC(), ProgressChars: 1024, ProgressAt: &progressAt, ReturnedModel: "served", Snapshot: `{"requested_model":"public-model-name","protocol":"chat","api_key":"snapshot-secret","base_url":"private-api-key","parameters":{"reasoning_effort":"high","thinking_mode":"","thinking_budget":0}}`})
	result := readIntelligence(t, s, "")
	if result.HistoryScope != "group" || len(result.Groups) != 1 {
		t.Fatalf("unexpected groups %+v", result)
	}
	g := result.Groups[0]
	if g.ModelID != m.ID || g.ReasoningEffort != "high" || !g.Automatic || g.NextRun == nil || len(g.History) != 0 || g.Verification.Status != "verified" || g.Verification.Group != "auto" || g.Verification.Scope != "token_binding" {
		t.Fatalf("group %+v", g)
	}
	if g.Latest["state"] != "dispatching" || g.Latest["progress_chars"] != float64(1024) || g.Latest["reasoning_effort"] != "high" || g.Latest["returned_model"] != "served" {
		t.Fatalf("attempt %+v", g.Latest)
	}
}

func TestPublicIntelligenceHistoryScopesAndVisibility(t *testing.T) {
	s, _, ch, first := publicFixture(t)
	ctx := context.Background()
	second := first
	second.ID, second.UpstreamID, second.ArtifactChinese = "second-model", "second-name", true
	if err := s.Store.SaveModel(ctx, second); err != nil {
		t.Fatal(err)
	}
	private := first
	private.ID, private.UpstreamID, private.Public = "private-model", "private-model-name", false
	if err := s.Store.SaveModel(ctx, private); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 11; i++ {
		model := first
		if i%2 == 1 {
			model = second
		}
		insertPublicArtifact(t, s, model.ID, ch.ID, "cn_html", false)
	}
	insertPublicArtifact(t, s, first.ID, ch.ID, "en_svg", false)
	insertPublicArtifact(t, s, first.ID, ch.ID, "cn_html", true)
	insertPublicArtifact(t, s, private.ID, ch.ID, "cn_html", false)
	insertIntelligenceJob(t, s, domain.Job{ID: "public-failed", ModelID: first.ID, ChannelID: ch.ID, TemplateID: "cn_html", Kind: "artifact", State: "failed", CreatedAt: time.Now().UTC().Add(-time.Second)})
	insertIntelligenceJob(t, s, domain.Job{ID: "private-failed", ModelID: private.ID, ChannelID: ch.ID, TemplateID: "cn_html", Kind: "artifact", State: "failed", CreatedAt: time.Now().UTC()})
	result := readIntelligence(t, s, "")
	if len(result.Groups) != 1 || result.Groups[0].ModelID != second.ID || len(result.Groups[0].History) != 8 || result.Groups[0].Latest["id"] != "public-failed" {
		t.Fatalf("group history %+v", result)
	}
	result = readIntelligence(t, s, "?model_id="+first.ID)
	if result.Groups[0].ModelID != first.ID || len(result.Groups[0].History) != 8 {
		t.Fatal("group history was incorrectly narrowed by representative model")
	}
	settings, _ := s.Store.GetSettings(ctx)
	settings.ArtifactHistoryScope = "model"
	if err := s.Store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	result = readIntelligence(t, s, "?model_id="+first.ID)
	if result.HistoryScope != "model" || len(result.Groups[0].History) != 6 {
		t.Fatalf("model history %+v", result)
	}
	for _, artifact := range result.Groups[0].History {
		if artifact["model_id"] != first.ID {
			t.Fatal("other model mixed into model scope")
		}
	}
	for _, query := range []string{"?model_id=" + private.ID, "?channel_id=private-channel", "?template_id=missing"} {
		if w := publicGET(t, s, "/api/v1/public/intelligence"+query); w.Code != 404 {
			t.Fatalf("invalid public selection accepted %s: %d", query, w.Code)
		}
	}
}

func TestPublicIntelligenceLatestAttemptUsesOnlyItsSuccessfulVisibleArtifact(t *testing.T) {
	for _, tc := range []struct {
		name, state, artifactJobID string
		hidden, wantMetrics        bool
	}{
		{"successful current artifact", "succeeded", "latest-attempt", false, true},
		{"failed after older success", "failed", "older-success", false, false},
		{"failed never adopts artifact metrics", "failed", "latest-attempt", false, false},
		{"hidden current artifact", "succeeded", "latest-attempt", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, ch, model := publicFixture(t)
			now := time.Now().UTC()
			input, output, total, first := int64(4242), int64(7126), int64(11368), int64(1800)
			a := domain.Artifact{ID: "current-artifact", JobID: tc.artifactJobID, ModelID: model.ID, ChannelID: ch.ID, TemplateID: "cn_html", Hidden: tc.hidden, Usage: domain.Usage{InputTokens: &input, OutputTokens: &output, TotalTokens: &total}, FirstTextMS: &first, DurationMS: 208000, FinishReason: "stop", CreatedAt: now.Add(-time.Second)}
			raw, err := json.Marshal(a)
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.Store.DB.Exec(`INSERT INTO artifacts(id,job_id,model_id,template_id,created_at,hidden,data) VALUES(?,?,?,?,?,?,?)`, a.ID, a.JobID, a.ModelID, a.TemplateID, a.CreatedAt.Format(databaseTimeFormat), a.Hidden, string(raw))
			if err != nil {
				t.Fatal(err)
			}
			insertIntelligenceJob(t, s, domain.Job{ID: "latest-attempt", ModelID: model.ID, ChannelID: ch.ID, TemplateID: "cn_html", Kind: "artifact", State: tc.state, CreatedAt: now})
			result := readIntelligence(t, s, "")
			if len(result.Groups) != 1 {
				t.Fatalf("missing group %+v", result)
			}
			latest := result.Groups[0].Latest
			if latest["id"] != "latest-attempt" {
				t.Fatalf("unexpected current attempt %+v", latest)
			}
			if tc.wantMetrics {
				usage, ok := latest["usage"].(map[string]any)
				if !ok || usage["input_tokens"] != float64(input) || usage["output_tokens"] != float64(output) || usage["total_tokens"] != float64(total) || latest["artifact_id"] != a.ID || latest["first_text_ms"] != float64(first) || latest["duration_ms"] != float64(a.DurationMS) || latest["finish_reason"] != a.FinishReason {
					t.Fatalf("current artifact metrics missing: %+v", latest)
				}
			} else {
				for _, field := range []string{"artifact_id", "usage", "first_text_ms", "duration_ms", "finish_reason"} {
					if _, exists := latest[field]; exists {
						t.Fatalf("non-current metrics leaked via %s: %+v", field, latest)
					}
				}
			}
		})
	}
}

func TestVisitorReasoningValidationAndActualMetadata(t *testing.T) {
	s, _, _, _ := publicFixture(t)
	cookie, csrf, _ := registerMemberForAuthTest(t, s.Handler(), "visitor-member")
	calls := 0
	s.Client = upstream.NewTestClient(visitorDoer(func(r *http.Request) (*http.Response, error) {
		calls++
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if payload["reasoning_effort"] != "high" {
			t.Fatalf("effort not forwarded: %+v", payload)
		}
		return visitorResponse(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`, "application/json"), nil
	}))
	body := visitorBody()
	body["reasoning_effort"] = "ultra"
	w := visitorPOSTWithMember(t, s, "/api/v1/test/generate", body, "", cookie, csrf)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_reasoning") || calls != 0 {
		t.Fatalf("validation %d %s calls=%d", w.Code, w.Body.String(), calls)
	}
	body["reasoning_effort"], body["timeout_seconds"] = "high", 0
	w = visitorPOSTWithMember(t, s, "/api/v1/test/generate", body, "", cookie, csrf)
	if w.Code != 200 || calls != 1 || strings.Count(w.Body.String(), `"reasoning_effort":"high"`) != 2 || !strings.Contains(w.Body.String(), "event: done") {
		t.Fatalf("actual metadata %d %s", w.Code, w.Body.String())
	}
}

func TestPublicArtifactAndAttemptExposeOnlyComparisonParameters(t *testing.T) {
	s, _, ch, m := publicFixture(t)
	catalog, err := s.publicCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a := domain.Artifact{ID: "reasoning-artifact", ModelID: m.ID, ChannelID: ch.ID, ReasoningEffort: "high", ThinkingMode: "adaptive", VerifiedGroup: "auto", GroupVerificationStatus: "verified", Parameters: `{"max_tokens":8192,"timeout_seconds":600,"reasoning_effort":"high","thinking_mode":"adaptive","api_key":"secret-parameter","base_url":"private-url"}`}
	view := publicArtifactMap(a, catalog, false)
	if view["reasoning_effort"] != "high" || view["thinking_mode"] != "adaptive" || view["verified_group"] != "auto" || view["group_verification_status"] != "verified" {
		t.Fatalf("actual artifact metadata missing %+v", view)
	}
	encoded, _ := json.Marshal(view)
	if strings.Contains(string(encoded), "secret-parameter") || strings.Contains(string(encoded), "private-url") {
		t.Fatal("transport parameter leak")
	}
	stale := groupVerification(domain.Channel{Revision: 5, GroupVerificationRevision: 4, VerifiedGroup: "stale", GroupVerificationStatus: "verified"})
	if stale["status"] != "unverified" || stale["group"] != "" || stale["checked_at"] != (*time.Time)(nil) {
		t.Fatalf("stale verification presented as current %+v", stale)
	}
}

func TestAdminGroupVerificationTrustAndStaleProtection(t *testing.T) {
	s, handler := testServer(t)
	cookie, csrf := loginForTest(t, handler)
	ctx := context.Background()
	p := domain.Platform{ID: "p", Name: "Fixture"}
	if err := s.Store.SavePlatform(ctx, p); err != nil {
		t.Fatal(err)
	}
	key, _ := s.Vault.Encrypt("fixture-token")
	ch := domain.Channel{ID: "ch", PlatformID: p.ID, Name: "Fixture channel", BaseURL: "https://example.test/v1", EncryptedKey: key, Protocol: "chat", Enabled: true, GroupProvider: "newapi"}
	if err := s.Store.SaveChannel(ctx, ch); err != nil {
		t.Fatal(err)
	}
	s.Client = upstream.NewTestClient(visitorDoer(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/token/self" || r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Fatal("wrong verification request")
		}
		return visitorResponse(`{"data":{"group":"auto","token":"fixture-token"}}`, "application/json"), nil
	}))
	w := request(t, handler, "POST", "/api/v1/admin/channels/ch/verify-group", nil, cookie, csrf)
	if w.Code != 200 || strings.Contains(w.Body.String(), "fixture-token") || !strings.Contains(w.Body.String(), `"status":"verified"`) {
		t.Fatalf("verification %d %s", w.Code, w.Body.String())
	}
	stored, _ := s.Store.GetChannel(ctx, ch.ID)
	stored.VerifiedGroup, stored.GroupVerificationStatus = "forged", "verified"
	w = request(t, handler, "PUT", "/api/v1/admin/channels/ch", stored, cookie, csrf)
	if w.Code != 200 {
		t.Fatalf("save %d %s", w.Code, w.Body.String())
	}
	stored, _ = s.Store.GetChannel(ctx, ch.ID)
	if stored.VerifiedGroup != "auto" || stored.GroupVerificationRevision != stored.Revision {
		t.Fatalf("trusted metadata overwritten %+v", stored)
	}
	stored.BaseURL = "https://changed.example/v1"
	w = request(t, handler, "PUT", "/api/v1/admin/channels/ch", stored, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	stored, _ = s.Store.GetChannel(ctx, ch.ID)
	if stored.VerifiedGroup != "" || stored.GroupVerificationRevision != 0 {
		t.Fatal("configuration change preserved stale verification")
	}
	s.Client = upstream.NewTestClient(visitorDoer(func(*http.Request) (*http.Response, error) {
		current, _ := s.Store.GetChannel(ctx, ch.ID)
		current.BaseURL = "https://concurrent.example/v1"
		if err := s.Store.SaveChannel(ctx, *current); err != nil {
			t.Fatal(err)
		}
		return visitorResponse(`{"data":{"group":"stale"}}`, "application/json"), nil
	}))
	w = request(t, handler, "POST", "/api/v1/admin/channels/ch/verify-group", nil, cookie, csrf)
	if w.Code != 409 {
		t.Fatalf("stale verification accepted %d %s", w.Code, w.Body.String())
	}
	stored, _ = s.Store.GetChannel(ctx, ch.ID)
	if stored.VerifiedGroup != "" {
		t.Fatal("stale result stored")
	}
	for _, update := range []func(*domain.Channel){func(v *domain.Channel) { v.ReasoningEffort = "ultra" }, func(v *domain.Channel) { v.IntelligenceModelID = "nonexistent" }, func(v *domain.Channel) { v.GroupProvider, v.GroupPath = "custom", "https://alternate.example/self" }} {
		invalid := *stored
		update(&invalid)
		w = request(t, handler, "PUT", "/api/v1/admin/channels/ch", invalid, cookie, csrf)
		if w.Code != 400 {
			t.Fatalf("invalid channel accepted %d %s", w.Code, w.Body.String())
		}
	}
	for i, model := range []domain.Model{{ChannelID: ch.ID, UpstreamID: "bad-openai", ReasoningEffort: "ultra"}, {ChannelID: ch.ID, UpstreamID: "bad-thinking", Protocol: "anthropic", ThinkingMode: "enabled", ThinkingBudget: 1023, MaxTokens: 4096}} {
		w = request(t, handler, "POST", "/api/v1/admin/models", model, cookie, csrf)
		if w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_reasoning") {
			t.Fatalf("invalid model %d accepted %d %s", i, w.Code, w.Body.String())
		}
	}
	settings, _ := s.Store.GetSettings(ctx)
	settings.ArtifactHistoryScope = "invalid"
	w = request(t, handler, "PUT", "/api/v1/admin/settings", settings, cookie, csrf)
	if w.Code != 400 {
		t.Fatal(fmt.Sprintf("invalid history scope accepted %d", w.Code))
	}
}
