package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func reverseProxyRequest(t *testing.T, h http.Handler, path, host, origin string, body any, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	t.Helper()
	var b bytes.Buffer
	if err := json.NewEncoder(&b).Encode(body); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, path, &b)
	r.Host, r.RemoteAddr = host, "172.18.0.1:40000"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", origin)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	// These attacker-controllable headers must never determine the policy.
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "relay.example:8443")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestConfiguredHTTPSOriginWorksThroughHTTPProxyWithoutRelaxingCSRF(t *testing.T) {
	s, initial := testServer(t)
	loginForTest(t, initial)
	if err := s.SetPublicURL("https://relay.example:8443"); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	credentials := map[string]string{"username": "tester", "password": "password-test-123456"}
	w := reverseProxyRequest(t, h, "/api/v1/auth/login", "relay.example:8443", "https://relay.example:8443", credentials, nil, "")
	if w.Code != http.StatusOK {
		t.Fatalf("HTTPS proxy login=%d %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("HTTPS proxy session cookie is not protected")
	}
	var session struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		host, origin, csrf string
		want               int
	}{
		{"relay.example:8443", "https://relay.example:8443", session.CSRF, 200},
		{"relay.example:8443", "https://relay.example:8443", "wrong-token", 403},
		{"relay.example:8443", "https://evil.example:8443", session.CSRF, 403},
		{"evil.example:8443", "https://relay.example:8443", session.CSRF, 403},
		{"localhost:42783", "https://relay.example:8443", session.CSRF, 403},
		{"relay.example:8443", "http://relay.example:8443", session.CSRF, 403},
		{"relay.example:8443", "https://relay.example", session.CSRF, 403},
	} {
		w = reverseProxyRequest(t, h, "/api/v1/admin/platforms", tc.host, tc.origin, map[string]any{"name": "Reverse proxy fixture"}, cookies[0], tc.csrf)
		if w.Code != tc.want {
			t.Fatalf("host=%s origin=%s status=%d want=%d", tc.host, tc.origin, w.Code, tc.want)
		}
	}
	w = reverseProxyRequest(t, h, "/api/v1/auth/logout", "relay.example:8443", "https://relay.example:8443", map[string]any{}, cookies[0], session.CSRF)
	if w.Code != 200 || len(w.Result().Cookies()) != 1 || !w.Result().Cookies()[0].Secure || w.Result().Cookies()[0].MaxAge >= 0 {
		t.Fatal("HTTPS proxy logout did not expire the protected cookie")
	}
}

func TestMemberSessionUsesConfiguredHTTPSAndRejectsForwardedHeaderSpoofing(t *testing.T) {
	s, _ := testServer(t)
	if err := s.SetPublicURL("https://relay.example:8443"); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	w := reverseProxyRequest(t, h, "/api/v1/user/auth/register", "relay.example:8443", "https://relay.example:8443", map[string]string{"username": "proxy-member", "password": "Member_Fixture_2026"}, nil, "")
	if w.Code != 201 {
		t.Fatalf("proxied registration=%d %s", w.Code, w.Body.String())
	}
	cookie, csrf, _ := memberAuthSessionResponse(t, w, 201)
	if !cookie.Secure {
		t.Fatal("user cookie missing Secure")
	}
	w = reverseProxyRequest(t, h, "/api/v1/user/auth/logout", "relay.example:8443", "https://evil.example:8443", map[string]any{}, cookie, csrf)
	if w.Code != 403 {
		t.Fatal("wrong-origin member logout accepted")
	}
	w = reverseProxyRequest(t, h, "/api/v1/user/auth/logout", "relay.example:8443", "https://relay.example:8443", map[string]any{}, cookie, csrf)
	if w.Code != 200 || len(w.Result().Cookies()) != 1 || !w.Result().Cookies()[0].Secure {
		t.Fatal("member secure logout failed")
	}
	if err := s.SetPublicURL(""); err != nil {
		t.Fatal(err)
	}
	w = reverseProxyRequest(t, s.Handler(), "/api/v1/user/auth/login", "relay.example:8443", "https://relay.example:8443", map[string]string{"username": "proxy-member", "password": "Member_Fixture_2026"}, nil, "")
	if w.Code != 403 {
		t.Fatal("unconfigured server trusted spoofed forwarded HTTPS headers")
	}
}

func TestConfiguredHTTPOriginDoesNotInventSecureTransport(t *testing.T) {
	s, _ := testServer(t)
	if err := s.SetPublicURL("http://relay.example"); err != nil {
		t.Fatal(err)
	}
	w := reverseProxyRequest(t, s.Handler(), "/api/v1/user/auth/register", "relay.example", "http://relay.example", map[string]string{"username": "http-member", "password": "Member_Fixture_2026"}, nil, "")
	if w.Code != 201 || len(w.Result().Cookies()) != 1 || w.Result().Cookies()[0].Secure {
		t.Fatal("HTTP origin cookie policy is incorrect")
	}
}
