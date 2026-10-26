package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"relaylab/internal/domain"
	"relaylab/internal/upstream"
)

func TestOriginPolicyChecksSchemeSyntaxAndBrowserMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, host, origin, site string
		want                     bool
	}{
		{"same origin", "example.com", "http://example.com", "same-origin", true},
		{"case and default port", "example.com:80", "http://EXAMPLE.COM", "", true},
		{"CLI authenticated by CSRF", "example.com", "", "", true},
		{"browser missing origin", "example.com", "", "same-origin", false},
		{"cross site", "example.com", "http://example.com", "cross-site", false},
		{"same site other origin", "example.com", "http://example.com", "same-site", false},
		{"scheme mismatch", "example.com", "https://example.com", "", false},
		{"port mismatch", "example.com", "http://example.com:81", "", false},
		{"opaque origin", "example.com", "null", "", false},
		{"origin with credentials", "example.com", "http://user@example.com", "", false},
		{"origin with path", "example.com", "http://example.com/path", "", false},
		{"origin with fragment", "example.com", "http://example.com/#part", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
			c.Request.Host = tc.host
			if tc.origin != "" {
				c.Request.Header.Set("Origin", tc.origin)
			}
			c.Request.Header.Set("Sec-Fetch-Site", tc.site)
			if got := validOrigin(c); got != tc.want {
				t.Fatalf("validOrigin=%v want %v", got, tc.want)
			}
		})
	}
}

func TestSetupRejectsMissingOriginAndHostRebinding(t *testing.T) {
	s, h := testServer(t)
	for _, tc := range []struct{ host, origin string }{{"localhost", ""}, {"attacker.example", "http://attacker.example"}, {"127.0.0.1", "https://127.0.0.1"}} {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/setup", strings.NewReader(`{"username":"tester","password":"password-test-123456"}`))
		r.RemoteAddr = "127.0.0.1:12345"
		r.Host = tc.host
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("setup accepted host=%q origin=%q: %d", tc.host, tc.origin, w.Code)
		}
	}
	if initialized, err := s.Store.HasAdmin(context.Background()); err != nil || initialized {
		t.Fatalf("rejected setup changed database: initialized=%v error=%v", initialized, err)
	}
}

func TestCLISetupAndLoginAllowJSONWithoutBrowserMetadata(t *testing.T) {
	_, h := testServer(t)
	for _, path := range []string{"/api/v1/setup", "/api/v1/auth/login"} {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"username":"tester","password":"password-test-123456"}`))
		r.RemoteAddr = "127.0.0.1:12345"
		r.Host = "localhost"
		r.Header.Set("Content-Type", "application/json; charset=utf-8")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		want := http.StatusOK
		if path == "/api/v1/setup" {
			want = http.StatusCreated
		}
		if w.Code != want {
			t.Fatalf("JSON CLI %s status=%d: %s", path, w.Code, w.Body.String())
		}
	}
	for _, metadata := range []string{"Sec-Fetch-Site", "Sec-Fetch-Mode", "Sec-Fetch-Dest"} {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"tester","password":"password-test-123456"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set(metadata, "same-origin")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("browser metadata without Origin accepted: %s status=%d", metadata, w.Code)
		}
	}
}

func TestAuthenticationPoolBoundsSetupAndLoginBeforeReadingBody(t *testing.T) {
	for _, path := range []string{"/api/v1/setup", "/api/v1/auth/login"} {
		s, h := testServer(t)
		s.authSlots <- struct{}{}
		s.authSlots <- struct{}{}
		body := &countingBody{Reader: strings.NewReader(`{}`)}
		r := httptest.NewRequest(http.MethodPost, path, body)
		r.RemoteAddr = "127.0.0.1:12345"
		r.Host = "localhost"
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusTooManyRequests || body.reads != 0 || len(s.authSlots) != 2 {
			t.Fatalf("authentication admission failed: %s status=%d reads=%d slots=%d", path, w.Code, body.reads, len(s.authSlots))
		}
		<-s.authSlots
		r = httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{`))
		r.RemoteAddr = "127.0.0.1:12345"
		r.Host = "localhost"
		r.Header.Set("Content-Type", "application/json")
		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest || len(s.authSlots) != 1 {
			t.Fatalf("failed parse did not release authentication slot: status=%d slots=%d", w.Code, len(s.authSlots))
		}
		<-s.authSlots
	}
}

func TestLoginRejectsEmptyAndOversizedPasswordsUniformly(t *testing.T) {
	s, h := testServer(t)
	_, _ = loginForTest(t, h)
	for _, username := range []string{"tester", "unknown-user"} {
		for _, password := range []string{"", strings.Repeat("x", 257)} {
			w := request(t, h, http.MethodPost, "/api/v1/auth/login", map[string]string{"username": username, "password": password}, nil, "")
			if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), `"code":"invalid_credentials"`) || len(s.authSlots) != 0 {
				t.Fatalf("invalid password response differs: %d %s slots=%d", w.Code, w.Body.String(), len(s.authSlots))
			}
		}
	}
}

func TestAuthenticatedCLIWithoutOriginStillRequiresCSRF(t *testing.T) {
	_, h := testServer(t)
	cookie, csrf := loginForTest(t, h)
	for _, tc := range []struct {
		csrf string
		want int
	}{{"", 403}, {csrf, 200}} {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", strings.NewReader(`{}`))
		r.AddCookie(cookie)
		r.Header.Set("X-CSRF-Token", tc.csrf)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("CLI logout status=%d want %d: %s", w.Code, tc.want, w.Body.String())
		}
	}
}

func TestAdminChannelValidatesCredentialHeaders(t *testing.T) {
	s, h := testServer(t)
	cookie, csrf := loginForTest(t, h)
	p := domain.Platform{ID: "credentials-platform", Name: "Credentials"}
	if err := s.Store.SavePlatform(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	for _, change := range []map[string]string{{"api_key": "key\r\nheader"}, {"api_key": "   "}, {"api_key": strings.Repeat("x", 8193)}, {"anthropic_version": "2023-06-01\r\nheader"}, {"anthropic_version": strings.Repeat("x", 101)}} {
		body := map[string]any{"platform_id": p.ID, "name": "main", "base_url": "https://example.test/v1", "api_key": "test-valid-key", "protocol": "chat"}
		for key, value := range change {
			body[key] = value
		}
		response := request(t, h, http.MethodPost, "/api/v1/admin/channels", body, cookie, csrf)
		if response.Code != 400 {
			t.Fatalf("invalid header credential accepted: %d %s", response.Code, response.Body.String())
		}
	}
	if channels, err := s.Store.ListChannels(context.Background()); err != nil || len(channels) != 0 {
		t.Fatalf("invalid credentials persisted: channels=%d error=%v", len(channels), err)
	}
}

func TestManualDiscoveryCannotOverwriteAChangedChannel(t *testing.T) {
	for _, upstreamFails := range []bool{false, true} {
		s, h := testServer(t)
		cookie, csrf := loginForTest(t, h)
		ctx := context.Background()
		p := domain.Platform{ID: "sync-platform", Name: "Sync"}
		if err := s.Store.SavePlatform(ctx, p); err != nil {
			t.Fatal(err)
		}
		cipher, err := s.Vault.Encrypt("test-sync-key")
		if err != nil {
			t.Fatal(err)
		}
		ch := domain.Channel{ID: "sync-channel", PlatformID: p.ID, Name: "Before", BaseURL: "https://example.test", EncryptedKey: cipher, Protocol: "chat", Revision: 1, Enabled: true}
		if err = s.Store.SaveChannel(ctx, ch); err != nil {
			t.Fatal(err)
		}
		s.Client = upstream.NewTestClient(visitorDoer(func(*http.Request) (*http.Response, error) {
			ch.Name, ch.Revision = "New config", 2
			if err := s.Store.SaveChannel(ctx, ch); err != nil {
				t.Fatal(err)
			}
			response := visitorResponse(`{"data":[{"id":"stale-model"}]}`, "application/json")
			if upstreamFails {
				response.StatusCode = http.StatusServiceUnavailable
			}
			return response, nil
		}))
		response := request(t, h, http.MethodPost, "/api/v1/admin/channels/"+ch.ID+"/sync-models", map[string]any{}, cookie, csrf)
		if response.Code != http.StatusConflict {
			t.Fatalf("changed config was not protected: status=%d body=%s", response.Code, response.Body.String())
		}
		current, err := s.Store.GetChannel(ctx, ch.ID)
		if err != nil || current.Name != "New config" || current.Revision != 2 || current.SyncError != "" {
			t.Fatalf("stale sync changed channel: %+v %v", current, err)
		}
		if models, err := s.Store.ListModels(ctx, ch.ID); err != nil || len(models) != 0 {
			t.Fatalf("stale sync inserted models: %+v %v", models, err)
		}
	}
}

func TestVisitorInvalidBodiesConsumeAdmissionAndEnforceWholeJSONLimit(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"malformed", `{`, 400},
		{"multiple documents", `{} {}`, 400},
		{"oversized string", `{"prompt":"` + strings.Repeat("x", 2<<20) + `"}`, 413},
		{"oversized trailing whitespace", `{}` + strings.Repeat(" ", 2<<20), 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, _ := publicFixture(t)
			settings, _ := s.Store.GetSettings(context.Background())
			settings.VisitorModelsPerMinute = 1
			if err := s.Store.SaveSettings(context.Background(), settings); err != nil {
				t.Fatal(err)
			}
			s.Client = upstream.NewTestClient(visitorDoer(func(*http.Request) (*http.Response, error) {
				t.Fatal("invalid or rate limited request reached upstream")
				return nil, nil
			}))
			h := s.Handler()
			r := httptest.NewRequest(http.MethodPost, "/api/v1/test/models", strings.NewReader(tc.body))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("invalid body status=%d want %d: %s", w.Code, tc.want, w.Body.String())
			}
			body := &countingBody{Reader: strings.NewReader(`{}`)}
			r = httptest.NewRequest(http.MethodPost, "/api/v1/test/models", body)
			w = httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 429 || body.reads != 0 || s.total != 0 || len(s.active) != 0 {
				t.Fatalf("admission failed: status=%d reads=%d active=%d/%v", w.Code, body.reads, s.total, s.active)
			}
		})
	}
}

type countingBody struct {
	io.Reader
	reads int
}

func (b *countingBody) Read(p []byte) (int, error) { b.reads++; return b.Reader.Read(p) }

func TestPublicDTOHidesInternalConfigAndAllowsOnlyComparableParameters(t *testing.T) {
	s, _, _, model := publicFixture(t)
	model.GeneratePath = "/private-route-secret"
	model.MaxTokens = 72819
	model.TimeoutSeconds = 517
	if err := s.Store.SaveModel(context.Background(), model); err != nil {
		t.Fatal(err)
	}
	response := publicGET(t, s, "/api/v1/public/monitor")
	for _, secret := range []string{"private-route-secret", "generate_path", "max_tokens", "timeout_seconds", "artifact_interval_seconds", "probe_interval_seconds"} {
		if strings.Contains(response.Body.String(), secret) {
			t.Errorf("monitor exposes internal configuration %q", secret)
		}
	}
	id := insertPublicArtifact(t, s, model.ID, model.ChannelID, "cn_html", false)
	a, err := s.Store.GetArtifact(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	a.Prompt = "public comparison prompt snapshot"
	a.Parameters = `{"max_tokens":123,"timeout_seconds":60,"api_key":"private-parameter-key","base_url":"private-parameter-url"}`
	raw, _ := json.Marshal(a)
	if _, err = s.Store.DB.Exec(`UPDATE artifacts SET data=? WHERE id=?`, string(raw), id); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/public/artifacts", "/api/v1/public/artifacts/" + id} {
		response = publicGET(t, s, path)
		if response.Code != 200 || !strings.Contains(response.Body.String(), a.Prompt) || !strings.Contains(response.Body.String(), "max_tokens") {
			t.Fatal(response.Code, response.Body.String())
		}
		for _, secret := range []string{"private-parameter-key", "private-parameter-url", "raw_response", "job_id", "hidden"} {
			if strings.Contains(response.Body.String(), secret) {
				t.Errorf("public artifact exposed %q", secret)
			}
		}
	}
}

func TestPublicHistoryHasBoundedValidatedLimit(t *testing.T) {
	s, _, _, model := publicFixture(t)
	for i := 0; i < 3; i++ {
		insertPublicProbe(t, s, model.ID, "success", time.Now().Add(-time.Duration(i)*time.Minute))
	}
	r := publicGET(t, s, "/api/v1/public/models/"+model.ID+"/history?limit=2")
	var history []json.RawMessage
	if err := json.Unmarshal(r.Body.Bytes(), &history); err != nil || r.Code != 200 || len(history) != 2 {
		t.Fatalf("history status=%d len=%d error=%v", r.Code, len(history), err)
	}
	for _, value := range []string{"0", "5001", "abc"} {
		if r := publicGET(t, s, "/api/v1/public/models/"+model.ID+"/history?limit="+value); r.Code != 400 {
			t.Fatalf("invalid history limit %q accepted: %d", value, r.Code)
		}
	}
}

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
	failWrite bool
}

func (w *deadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	w.deadlines = append(w.deadlines, deadline)
	return nil
}

func (w *deadlineRecorder) Write(body []byte) (int, error) {
	if w.failWrite {
		return 0, errors.New("client write timed out")
	}
	return w.ResponseRecorder.Write(body)
}

func TestVisitorSSEBoundsEachWriteAndStopsAfterWriteFailure(t *testing.T) {
	for _, failWrite := range []bool{false, true} {
		s, _, _, _ := publicFixture(t)
		s.Client = upstream.NewTestClient(visitorDoer(func(*http.Request) (*http.Response, error) {
			return visitorResponse(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`, "application/json"), nil
		}))
		encoded, _ := json.Marshal(visitorBody())
		r := httptest.NewRequest(http.MethodPost, "/api/v1/test/generate", strings.NewReader(string(encoded)))
		w := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder(), failWrite: failWrite}
		s.Handler().ServeHTTP(w, r)
		if len(w.deadlines) < 2 || len(w.deadlines)%2 != 0 || !w.deadlines[len(w.deadlines)-1].IsZero() {
			t.Fatalf("write deadline was not bounded and cleared: %v", w.deadlines)
		}
		for i, deadline := range w.deadlines {
			if i%2 == 0 && (deadline.IsZero() || time.Until(deadline) > 16*time.Second) {
				t.Fatalf("invalid write deadline %v", deadline)
			}
			if i%2 == 1 && !deadline.IsZero() {
				t.Fatal("write deadline remained active while awaiting upstream")
			}
		}
		if failWrite && len(w.deadlines) != 2 {
			t.Fatalf("retried failed client write: %d deadlines", len(w.deadlines))
		}
		if s.total != 0 || len(s.active) != 0 {
			t.Fatal("write failure leaked concurrency slot")
		}
	}
}
