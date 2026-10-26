package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"relaylab/internal/security"
	"relaylab/internal/store"
)

func testServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	d := t.TempDir()
	st, e := store.Open(filepath.Join(d, "relaylab.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { st.Close() })
	v, e := security.LoadVault(d, false)
	if e != nil {
		t.Fatal(e)
	}
	s := New(st, v)
	return s, s.Handler()
}
func request(t *testing.T, h http.Handler, method, path string, body any, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if e := json.NewEncoder(&buf).Encode(body); e != nil {
			t.Fatal(e)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.RemoteAddr = "127.0.0.1:54321"
	req.Host = "localhost"
	req.Header.Set("Content-Type", "application/json")
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set("Origin", "http://localhost")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}
func loginForTest(t *testing.T, h http.Handler) (*http.Cookie, string) {
	t.Helper()
	w := request(t, h, "POST", "/api/v1/setup", map[string]string{"username": "tester", "password": "password-test-123456"}, nil, "")
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request(t, h, "POST", "/api/v1/auth/login", map[string]string{"username": "tester", "password": "password-test-123456"}, nil, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var b struct {
		CSRF string `json:"csrf_token"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &b); e != nil {
		t.Fatal(e)
	}
	cookies := w.Result().Cookies()
	if len(cookies) == 0 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("session cookie policy")
	}
	return cookies[0], b.CSRF
}
func TestAdminAuthenticationCSRFAndEncryptedCredentials(t *testing.T) {
	s, h := testServer(t)
	w := request(t, h, "GET", "/api/v1/admin/channels", nil, nil, "")
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	cookie, csrf := loginForTest(t, h)
	w = request(t, h, "POST", "/api/v1/admin/platforms", map[string]any{"name": "Example", "public": true}, cookie, "")
	if w.Code != 403 {
		t.Fatal("missing CSRF accepted", w.Code)
	}
	w = request(t, h, "POST", "/api/v1/admin/platforms", map[string]any{"name": "Example", "public": true}, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var p struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &p)
	key := "sk-private-platform-test-key-abc123"
	w = request(t, h, "POST", "/api/v1/admin/channels", map[string]any{"platform_id": p.ID, "name": "main", "base_url": "https://example.com/v1", "api_key": key, "protocol": "chat", "enabled": true, "public": true}, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), key) || strings.Contains(w.Body.String(), "encrypted_key") {
		t.Fatal("response leaked key")
	}
	chs, e := s.Store.ListChannels(context.Background())
	if e != nil || len(chs) != 1 {
		t.Fatal(chs, e)
	}
	if chs[0].EncryptedKey == key || chs[0].EncryptedKey == "" {
		t.Fatal("plaintext key storage")
	}
	plain, e := s.Vault.Decrypt(chs[0].EncryptedKey)
	if e != nil || plain != key {
		t.Fatal(e)
	}
	w = request(t, h, "GET", "/api/v1/public/monitor", nil, nil, "")
	if strings.Contains(w.Body.String(), key) || strings.Contains(w.Body.String(), "base_url") {
		t.Fatal("public key/url leak")
	}
	w = request(t, h, "POST", "/api/v1/auth/password", map[string]string{"old_password": "password-test-123456", "new_password": "changed-password-98765"}, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request(t, h, "GET", "/api/v1/auth/me", nil, cookie, "")
	if w.Code != 401 {
		t.Fatal("old session survived password change")
	}
}
func TestSetupIsLocalAndOriginsAreChecked(t *testing.T) {
	_, h := testServer(t)
	r := httptest.NewRequest("POST", "/api/v1/setup", strings.NewReader(`{"username":"a","password":"long-test-password"}`))
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = "192.0.2.1:1234"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	r = httptest.NewRequest("POST", "/api/v1/setup", strings.NewReader(`{"username":"a","password":"long-test-password"}`))
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://other.example")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross origin setup accepted")
	}
}
