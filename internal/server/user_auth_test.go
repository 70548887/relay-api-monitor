package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"relaylab/internal/domain"
	"relaylab/internal/security"
)

const memberAuthPassword = "member-password-test-123456"

func memberAuthSessionResponse(t *testing.T, w *httptest.ResponseRecorder, status int) (*http.Cookie, string, domain.Member) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("member session status=%d want=%d: %s", w.Code, status, w.Body.String())
	}
	var body struct {
		User domain.Member `json:"user"`
		CSRF string        `json:"csrf_token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.User.ID == "" || body.User.CreatedAt == "" || body.CSRF == "" || strings.Contains(w.Body.String(), "password_hash") || strings.Contains(w.Body.String(), "$argon2") {
		t.Fatalf("invalid member session payload: %s", w.Body.String())
	}
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == memberSessionCookie {
			if cookie.Value == "" || !cookie.HttpOnly || cookie.Path != "/" || cookie.SameSite != http.SameSiteStrictMode || cookie.MaxAge != 28800 {
				t.Fatalf("member cookie policy: %+v", cookie)
			}
			return cookie, body.CSRF, body.User
		}
	}
	t.Fatal("member session cookie missing")
	return nil, "", domain.Member{}
}

func registerMemberForAuthTest(t *testing.T, h http.Handler, username string) (*http.Cookie, string, domain.Member) {
	t.Helper()
	w := request(t, h, http.MethodPost, "/api/v1/user/auth/register", map[string]string{"username": username, "password": memberAuthPassword}, nil, "")
	return memberAuthSessionResponse(t, w, http.StatusCreated)
}

func TestMemberRegistrationAndAdministratorSessionsAreIsolated(t *testing.T) {
	s, h := testServer(t)
	adminCookie, adminCSRF := loginForTest(t, h)
	w := request(t, h, http.MethodPost, "/api/v1/user/auth/register", map[string]any{"username": "  tester  ", "password": memberAuthPassword, "role": "admin", "is_admin": true}, nil, "")
	memberCookie, memberCSRF, member := memberAuthSessionResponse(t, w, http.StatusCreated)
	if member.Username != "tester" || member.Disabled || memberCookie.Name == adminCookie.Name || memberCSRF == adminCSRF {
		t.Fatal("member and administrator identities were not separated")
	}
	stored, err := s.Store.GetMemberByID(context.Background(), member.ID)
	if err != nil || !security.VerifyPassword(stored.PasswordHash, memberAuthPassword) {
		t.Fatalf("registration did not store password hash: %v", err)
	}
	if w = request(t, h, http.MethodGet, "/api/v1/user/auth/me", nil, memberCookie, ""); w.Code != 200 || !strings.Contains(w.Body.String(), member.ID) {
		t.Fatalf("registered session not authenticated: %d %s", w.Code, w.Body.String())
	}
	for _, tc := range []struct {
		path   string
		cookie *http.Cookie
	}{
		{"/api/v1/auth/me", memberCookie},
		{"/api/v1/admin/users", memberCookie},
		{"/api/v1/admin/members", memberCookie},
		{"/api/v1/user/auth/me", adminCookie},
		{"/api/v1/auth/me", &http.Cookie{Name: adminCookie.Name, Value: memberCookie.Value}},
		{"/api/v1/user/auth/me", &http.Cookie{Name: memberCookie.Name, Value: adminCookie.Value}},
	} {
		if w = request(t, h, http.MethodGet, tc.path, nil, tc.cookie, ""); w.Code != 401 {
			t.Fatalf("cross-account cookie accepted at %s: %d %s", tc.path, w.Code, w.Body.String())
		}
	}
	if w = request(t, h, http.MethodGet, "/api/v1/auth/me", nil, adminCookie, ""); w.Code != 200 || strings.Contains(w.Body.String(), member.ID) {
		t.Fatalf("registration modified administrator session: %d %s", w.Code, w.Body.String())
	}
	if w = request(t, h, http.MethodPost, "/api/v1/user/auth/logout", nil, memberCookie, adminCSRF); w.Code != 403 {
		t.Fatalf("administrator CSRF authenticated member write: %d %s", w.Code, w.Body.String())
	}
	if w = request(t, h, http.MethodPost, "/api/v1/auth/logout", nil, adminCookie, memberCSRF); w.Code != 403 {
		t.Fatalf("member CSRF authenticated administrator write: %d %s", w.Code, w.Body.String())
	}
	if w = request(t, h, http.MethodPost, "/api/v1/user/auth/register", map[string]string{"username": "tester", "password": memberAuthPassword}, nil, ""); w.Code != 409 {
		t.Fatalf("duplicate username accepted: %d %s", w.Code, w.Body.String())
	}
}

func TestMemberRegistrationIsRemoteAccessibleAndChecksBrowserOrigin(t *testing.T) {
	_, h := testServer(t)
	for _, path := range []string{"/api/v1/user/auth/register", "/api/v1/user/auth/login"} {
		for _, origin := range []string{"https://other.example", ""} {
			r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"username":"remote-member","password":"member-password-test-123456"}`))
			r.RemoteAddr = "203.0.113.7:12345"
			r.Host = "monitor.example"
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			r.Header.Set("Origin", origin)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusForbidden {
				t.Fatalf("invalid browser origin accepted at %s: %d %s", path, w.Code, w.Body.String())
			}
		}
	}
	for _, tc := range []struct {
		path   string
		status int
	}{
		{"/api/v1/user/auth/register", http.StatusCreated},
		{"/api/v1/user/auth/login", http.StatusOK},
	} {
		r := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(`{"username":"remote-member","password":"member-password-test-123456"}`))
		r.RemoteAddr = "203.0.113.7:12345"
		r.Host = "monitor.example"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://monitor.example")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		memberAuthSessionResponse(t, w, tc.status)
	}
	r := httptest.NewRequest(http.MethodPost, "https://monitor.example/api/v1/user/auth/login", strings.NewReader(`{"username":"remote-member","password":"member-password-test-123456"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://monitor.example")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	cookie, _, _ := memberAuthSessionResponse(t, w, http.StatusOK)
	if !cookie.Secure {
		t.Fatal("HTTPS member login did not set a Secure cookie")
	}
}

func TestMemberAuthenticationSharedAdmissionAndRateLimitsBeforeBodyRead(t *testing.T) {
	for _, path := range []string{"/api/v1/user/auth/register", "/api/v1/user/auth/login"} {
		t.Run(path, func(t *testing.T) {
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
			if w.Code != 429 || body.reads != 0 || len(s.authSlots) != 2 || !strings.Contains(w.Body.String(), "authentication_busy") {
				t.Fatalf("admission failed: %d reads=%d slots=%d %s", w.Code, body.reads, len(s.authSlots), w.Body.String())
			}
			<-s.authSlots
			<-s.authSlots
			prefix, limit := "member-login:", 10
			if strings.HasSuffix(path, "register") {
				prefix, limit = "member-register:", 3
			}
			s.rates[prefix+"127.0.0.1"] = &rateBucket{Start: time.Now(), Count: limit}
			body = &countingBody{Reader: strings.NewReader(`{}`)}
			r = httptest.NewRequest(http.MethodPost, path, body)
			r.RemoteAddr = "127.0.0.1:12345"
			r.Host = "localhost"
			r.Header.Set("Content-Type", "application/json")
			w = httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 429 || body.reads != 0 || !strings.Contains(w.Body.String(), "rate_limited") {
				t.Fatalf("rate admission failed: %d reads=%d %s", w.Code, body.reads, w.Body.String())
			}
		})
	}
}

func TestMemberLoginRejectsUnknownDisabledAndInvalidPasswordsUniformly(t *testing.T) {
	s, h := testServer(t)
	cookie, _, member := registerMemberForAuthTest(t, h, "member-login")
	for _, username := range []string{member.Username, "unknown-member"} {
		for _, password := range []string{"", strings.Repeat("x", 257), "incorrect-password"} {
			w := request(t, h, http.MethodPost, "/api/v1/user/auth/login", map[string]string{"username": username, "password": password}, nil, "")
			if w.Code != 401 || !strings.Contains(w.Body.String(), `"code":"invalid_credentials"`) || !strings.Contains(w.Body.String(), "账号或密码不正确") || len(s.authSlots) != 0 {
				t.Fatalf("login failure differs for %q: %d %s", username, w.Code, w.Body.String())
			}
		}
	}
	if _, err := s.Store.SetMemberDisabled(context.Background(), member.ID, true); err != nil {
		t.Fatal(err)
	}
	w := request(t, h, http.MethodPost, "/api/v1/user/auth/login", map[string]string{"username": member.Username, "password": memberAuthPassword}, nil, "")
	if w.Code != 401 || !strings.Contains(w.Body.String(), `"code":"invalid_credentials"`) {
		t.Fatalf("disabled member login accepted: %d %s", w.Code, w.Body.String())
	}
	if w = request(t, h, http.MethodGet, "/api/v1/user/auth/me", nil, cookie, ""); w.Code != 401 {
		t.Fatalf("disabled member session survived: %d %s", w.Code, w.Body.String())
	}
}

func TestMemberSessionExpiryAndCSRFProtection(t *testing.T) {
	s, h := testServer(t)
	cookie, csrf, member := registerMemberForAuthTest(t, h, "member-csrf")
	for _, path := range []string{"/api/v1/user/auth/logout", "/api/v1/user/auth/password"} {
		w := request(t, h, http.MethodPost, path, map[string]string{"old_password": memberAuthPassword, "new_password": "new-member-password-123456"}, cookie, "")
		if w.Code != 403 {
			t.Fatalf("missing CSRF accepted at %s: %d %s", path, w.Code, w.Body.String())
		}
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		r.Host = "localhost"
		r.Header.Set("Origin", "https://other.example")
		r.Header.Set("X-CSRF-Token", csrf)
		r.AddCookie(cookie)
		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("cross-origin CSRF accepted at %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	u, err := s.Store.GetMemberByID(context.Background(), member.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Store.SaveAuthenticatedMemberSession(context.Background(), domain.MemberSession{TokenHash: security.HashToken(cookie.Value), MemberID: member.ID, CSRFToken: csrf, ExpiresAt: time.Now().Add(-time.Hour)}, u.PasswordHash); err != nil {
		t.Fatal(err)
	}
	if w := request(t, h, http.MethodGet, "/api/v1/user/auth/me", nil, cookie, ""); w.Code != 401 {
		t.Fatalf("expired member session accepted: %d %s", w.Code, w.Body.String())
	}
}

func TestMemberPasswordChangeRevokesEveryMemberSessionAndPreservesAdminSession(t *testing.T) {
	_, h := testServer(t)
	adminCookie, _ := loginForTest(t, h)
	first, csrf, member := registerMemberForAuthTest(t, h, "member-password")
	w := request(t, h, http.MethodPost, "/api/v1/user/auth/login", map[string]string{"username": member.Username, "password": memberAuthPassword}, nil, "")
	second, _, _ := memberAuthSessionResponse(t, w, http.StatusOK)
	for _, body := range []map[string]string{
		{"old_password": "wrong-password", "new_password": "new-member-password-123456"},
		{"old_password": memberAuthPassword, "new_password": "short"},
	} {
		if w = request(t, h, http.MethodPost, "/api/v1/user/auth/password", body, first, csrf); w.Code != 400 {
			t.Fatalf("invalid password change accepted: %d %s", w.Code, w.Body.String())
		}
	}
	updatedPassword := "updated-member-password-123456"
	w = request(t, h, http.MethodPost, "/api/v1/user/auth/password", map[string]string{"old_password": memberAuthPassword, "new_password": updatedPassword}, first, csrf)
	if w.Code != 200 {
		t.Fatalf("password change failed: %d %s", w.Code, w.Body.String())
	}
	cleared := w.Result().Cookies()
	if len(cleared) != 1 || cleared[0].Name != memberSessionCookie || cleared[0].MaxAge != -1 {
		t.Fatal("password change did not expire the member cookie")
	}
	for _, cookie := range []*http.Cookie{first, second} {
		if w = request(t, h, http.MethodGet, "/api/v1/user/auth/me", nil, cookie, ""); w.Code != 401 {
			t.Fatalf("member session survived password change: %d %s", w.Code, w.Body.String())
		}
	}
	if w = request(t, h, http.MethodGet, "/api/v1/auth/me", nil, adminCookie, ""); w.Code != 200 {
		t.Fatalf("member password change revoked administrator session: %d %s", w.Code, w.Body.String())
	}
	if w = request(t, h, http.MethodPost, "/api/v1/user/auth/login", map[string]string{"username": member.Username, "password": memberAuthPassword}, nil, ""); w.Code != 401 {
		t.Fatalf("old member password accepted: %d %s", w.Code, w.Body.String())
	}
	w = request(t, h, http.MethodPost, "/api/v1/user/auth/login", map[string]string{"username": member.Username, "password": updatedPassword}, nil, "")
	memberAuthSessionResponse(t, w, http.StatusOK)
}

func TestMemberLogoutRevokesOnlyTheCurrentSession(t *testing.T) {
	_, h := testServer(t)
	first, csrf, member := registerMemberForAuthTest(t, h, "member-logout")
	w := request(t, h, http.MethodPost, "/api/v1/user/auth/login", map[string]string{"username": member.Username, "password": memberAuthPassword}, nil, "")
	second, _, _ := memberAuthSessionResponse(t, w, http.StatusOK)
	w = request(t, h, http.MethodPost, "/api/v1/user/auth/logout", nil, first, csrf)
	if w.Code != 200 {
		t.Fatalf("logout failed: %d %s", w.Code, w.Body.String())
	}
	if w = request(t, h, http.MethodGet, "/api/v1/user/auth/me", nil, first, ""); w.Code != 401 {
		t.Fatal("logged-out member session survived")
	}
	if w = request(t, h, http.MethodGet, "/api/v1/user/auth/me", nil, second, ""); w.Code != 200 {
		t.Fatalf("logout revoked another session: %d %s", w.Code, w.Body.String())
	}
}

func TestMemberRegistrationInputAndJSONValidation(t *testing.T) {
	for _, tc := range []struct {
		name, username, password string
	}{
		{"empty name", "", memberAuthPassword},
		{"whitespace name", " \t ", memberAuthPassword},
		{"oversized name", strings.Repeat("x", 201), memberAuthPassword},
		{"weak password", "new-member", "short"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, h := testServer(t)
			w := request(t, h, http.MethodPost, "/api/v1/user/auth/register", map[string]string{"username": tc.username, "password": tc.password}, nil, "")
			if w.Code != 400 || len(w.Result().Cookies()) != 0 || len(s.authSlots) != 0 {
				t.Fatalf("invalid registration accepted: %d %s", w.Code, w.Body.String())
			}
		})
	}
	for _, path := range []string{"/api/v1/user/auth/register", "/api/v1/user/auth/login"} {
		_, h := testServer(t)
		var body bytes.Buffer
		body.WriteString(`{"username":"json-member","password":"member-password-test-123456"} {}`)
		r := httptest.NewRequest(http.MethodPost, path, &body)
		r.Host = "localhost"
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("multiple JSON documents accepted at %s: %d %s", path, w.Code, w.Body.String())
		}
	}
}
