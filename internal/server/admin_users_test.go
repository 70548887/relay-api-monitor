package server

import (
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

func TestAdminUsersCreateDisableAndResetPassword(t *testing.T) {
	s, h := testServer(t)
	cookie, csrf := loginForTest(t, h)
	for _, path := range []string{"/api/v1/admin/users", "/api/v1/admin/members"} {
		if w := request(t, h, http.MethodGet, path, nil, nil, ""); w.Code != http.StatusUnauthorized {
			t.Fatal("anonymous account listing accepted", path, w.Code)
		}
	}
	w := request(t, h, http.MethodGet, "/api/v1/admin/users", nil, cookie, "")
	var list struct {
		Items []adminUserResponse `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != 200 || len(list.Items) != 1 || !list.Items[0].IsCurrent || list.Items[0].CreatedAt == "" {
		t.Fatalf("administrator list=%s error=%v", w.Body.String(), err)
	}
	currentID := list.Items[0].ID
	w = request(t, h, http.MethodPatch, "/api/v1/admin/users/"+currentID, map[string]bool{"disabled": true}, cookie, csrf)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "current_admin_protected") {
		t.Fatal("current administrator was disabled", w.Code, w.Body.String())
	}
	w = request(t, h, http.MethodPost, "/api/v1/admin/users/"+currentID+"/reset-password", map[string]string{"password": "another-strong-password"}, cookie, csrf)
	if w.Code != http.StatusConflict {
		t.Fatal("current administrator reset password without old password", w.Code)
	}
	body := map[string]string{"username": "  second  ", "password": "second-password-12345"}
	w = request(t, h, http.MethodPost, "/api/v1/admin/users", body, cookie, "")
	if w.Code != http.StatusForbidden {
		t.Fatal("account creation bypassed CSRF", w.Code)
	}
	w = request(t, h, http.MethodPost, "/api/v1/admin/users", body, cookie, csrf)
	var second adminUserResponse
	if err := json.Unmarshal(w.Body.Bytes(), &second); err != nil || w.Code != 201 || second.Username != "second" || second.Disabled || second.IsCurrent || second.CreatedAt == "" {
		t.Fatal("create administrator", w.Code, w.Body.String(), err)
	}
	if strings.Contains(w.Body.String(), "password") || strings.Contains(w.Body.String(), "argon2") {
		t.Fatal("create response exposed password material")
	}
	w = request(t, h, http.MethodPost, "/api/v1/admin/users", body, cookie, csrf)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "username_exists") {
		t.Fatal("duplicate administrator accepted", w.Code, w.Body.String())
	}
	secondCookie, secondCSRF := loginNamedAdmin(t, h, "second", body["password"])
	w = request(t, h, http.MethodPost, "/api/v1/admin/platforms", map[string]string{"name": "second-admin-platform"}, secondCookie, secondCSRF)
	if w.Code != http.StatusOK {
		t.Fatal("new administrator lacks full permissions", w.Code, w.Body.String())
	}
	w = request(t, h, http.MethodPatch, "/api/v1/admin/users/"+second.ID, map[string]bool{"disabled": true}, cookie, csrf)
	if w.Code != http.StatusOK {
		t.Fatal("disable administrator", w.Code, w.Body.String())
	}
	if w = request(t, h, http.MethodGet, "/api/v1/auth/me", nil, secondCookie, ""); w.Code != http.StatusUnauthorized {
		t.Fatal("disabled administrator retained valid session", w.Code)
	}
	w = request(t, h, http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "second", "password": body["password"]}, nil, "")
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "invalid_credentials") {
		t.Fatal("disabled login differs from bad credentials", w.Code, w.Body.String())
	}
	// Even a session inserted outside the account-management transaction cannot
	// authenticate a disabled administrator.
	if _, err := s.Store.DB.Exec("INSERT INTO sessions(token_hash,admin_id,csrf_token,expires_at) VALUES(?,?,?,?)", security.HashToken("disabled-token"), second.ID, "csrf", time.Now().UTC().Add(time.Hour).Format("2006-01-02T15:04:05.000000000Z")); err != nil {
		t.Fatal(err)
	}
	w = request(t, h, http.MethodGet, "/api/v1/auth/me", nil, &http.Cookie{Name: "relaylab_session", Value: "disabled-token"}, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatal("requireAuth accepted a disabled administrator", w.Code)
	}
	w = request(t, h, http.MethodPatch, "/api/v1/admin/users/"+second.ID, map[string]bool{"disabled": false}, cookie, csrf)
	if w.Code != http.StatusOK {
		t.Fatal("enable administrator", w.Code, w.Body.String())
	}
	secondCookie, _ = loginNamedAdmin(t, h, "second", body["password"])
	w = request(t, h, http.MethodPost, "/api/v1/admin/users/"+second.ID+"/reset-password", map[string]string{"password": "replacement-password-54321"}, cookie, csrf)
	if w.Code != http.StatusOK {
		t.Fatal("reset administrator password", w.Code, w.Body.String())
	}
	if w = request(t, h, http.MethodGet, "/api/v1/auth/me", nil, secondCookie, ""); w.Code != http.StatusUnauthorized {
		t.Fatal("password reset retained target session")
	}
	w = request(t, h, http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "second", "password": body["password"]}, nil, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatal("old password survived reset", w.Code)
	}
	_, _ = loginNamedAdmin(t, h, "second", "replacement-password-54321")
	w = request(t, h, http.MethodGet, "/api/v1/admin/audit", nil, cookie, "")
	if strings.Contains(w.Body.String(), body["password"]) || strings.Contains(w.Body.String(), "replacement-password-54321") || strings.Contains(w.Body.String(), "argon2") || strings.Contains(w.Body.String(), "password_hash") {
		t.Fatal("account audit exposed credentials")
	}
}

func loginNamedAdmin(t *testing.T, h http.Handler, username, password string) (*http.Cookie, string) {
	t.Helper()
	w := request(t, h, http.MethodPost, "/api/v1/auth/login", map[string]string{"username": username, "password": password}, nil, "")
	var result struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != http.StatusOK || len(w.Result().Cookies()) != 1 {
		t.Fatal("login administrator", w.Code, w.Body.String(), err)
	}
	return w.Result().Cookies()[0], result.CSRF
}

func TestAccountPasswordHashAdmissionPrecedesRequestBody(t *testing.T) {
	s, h := testServer(t)
	cookie, csrf := loginForTest(t, h)
	u, err := s.Store.CreateAdmin(context.Background(), "target", "existing-hash")
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.Store.CreateMember(context.Background(), "member-target", "member-hash")
	if err != nil {
		t.Fatal(err)
	}
	s.authSlots <- struct{}{}
	s.authSlots <- struct{}{}
	for _, path := range []string{"/api/v1/admin/users", "/api/v1/admin/users/" + u.ID + "/reset-password", "/api/v1/admin/members", "/api/v1/admin/members/" + m.ID + "/reset-password"} {
		body := &countingBody{Reader: strings.NewReader(`{}`)}
		r := httptest.NewRequest(http.MethodPost, path, body)
		r.Host = "localhost"
		r.Header.Set("Origin", "http://localhost")
		r.Header.Set("X-CSRF-Token", csrf)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusTooManyRequests || body.reads != 0 || len(s.authSlots) != 2 {
			t.Fatalf("unbounded password hashing %s: status=%d reads=%d slots=%d", path, w.Code, body.reads, len(s.authSlots))
		}
	}
}

func TestAdminMemberManagementStaysInMemberNamespace(t *testing.T) {
	s, h := testServer(t)
	cookie, csrf := loginForTest(t, h)
	w := request(t, h, http.MethodPost, "/api/v1/admin/members", map[string]string{"username": "tester", "password": "member-test-password"}, cookie, csrf)
	var member domain.Member
	if err := json.Unmarshal(w.Body.Bytes(), &member); err != nil || w.Code != http.StatusCreated || member.ID == "" || member.PasswordHash != "" {
		t.Fatal("admin create member", w.Code, w.Body.String(), err)
	}
	stored, err := s.Store.GetMemberByID(context.Background(), member.ID)
	if err != nil || !security.VerifyPassword(stored.PasswordHash, "member-test-password") {
		t.Fatal("member password not hashed", err)
	}
	ss := domain.MemberSession{MemberID: member.ID, TokenHash: "member-target-session", CSRFToken: "member-csrf", ExpiresAt: time.Now().UTC().Add(time.Hour)}
	if err = s.Store.SaveAuthenticatedMemberSession(context.Background(), ss, stored.PasswordHash); err != nil {
		t.Fatal(err)
	}
	w = request(t, h, http.MethodPost, "/api/v1/admin/members/"+member.ID+"/reset-password", map[string]string{"password": "member-replacement-password"}, cookie, csrf)
	if w.Code != http.StatusOK {
		t.Fatal("admin reset member", w.Code, w.Body.String())
	}
	if _, err = s.Store.GetMemberSession(context.Background(), ss.TokenHash); err == nil {
		t.Fatal("member reset retained login session")
	}
	w = request(t, h, http.MethodPatch, "/api/v1/admin/members/"+member.ID, map[string]bool{"disabled": true}, cookie, csrf)
	if w.Code != http.StatusOK {
		t.Fatal("admin disable member", w.Code, w.Body.String())
	}
	w = request(t, h, http.MethodGet, "/api/v1/admin/members", nil, cookie, "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"disabled":true`) || strings.Contains(w.Body.String(), "password") {
		t.Fatal("member list", w.Code, w.Body.String())
	}
	w = request(t, h, http.MethodGet, "/api/v1/auth/me", nil, cookie, "")
	if w.Code != http.StatusOK {
		t.Fatal("member management changed administrator session", w.Code)
	}
	w = request(t, h, http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "tester", "password": "password-test-123456"}, nil, "")
	if w.Code != http.StatusOK {
		t.Fatal("member reset changed same-name administrator password", w.Code, w.Body.String())
	}
}
