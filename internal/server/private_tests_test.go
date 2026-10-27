package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"relaylab/internal/domain"
	"relaylab/internal/upstream"
)

func privateConnectionForTest(t *testing.T, s *Server, cookie *http.Cookie, csrf string) domain.UserConnection {
	t.Helper()
	response := visitorPOSTWithMember(t, s, "/api/v1/user/connections", map[string]any{"name": "My connection", "base_url": "https://public.example/v1", "api_key": "sk-member-private-very-secret-key", "protocol": "chat", "discovery_protocol": "openai"}, "", cookie, csrf)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var c domain.UserConnection
	if e := json.Unmarshal(response.Body.Bytes(), &c); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(response.Body.String(), "sk-member-private-very-secret-key") || strings.Contains(response.Body.String(), "encrypted_key") {
		t.Fatal("connection exposed secret")
	}
	return c
}
func memberRequest(t *testing.T, s *Server, method, path string, body any, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	t.Helper()
	return request(t, s.Handler(), method, path, body, cookie, csrf)
}
func TestPrivateConnectionsAndHistoryAreOwnedAndIdempotent(t *testing.T) {
	s, _, _, _ := publicFixture(t)
	a, tokenA, userA := registerMemberForAuthTest(t, s.Handler(), "alice-private")
	b, tokenB, _ := registerMemberForAuthTest(t, s.Handler(), "bob-private")
	connection := privateConnectionForTest(t, s, a, tokenA)
	stored, err := s.Store.GetUserConnection(context.Background(), userA.ID, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	key, err := s.Vault.DecryptUserConnection(userA.ID, connection.ID, stored.EncryptedKey)
	if err != nil || key != "sk-member-private-very-secret-key" {
		t.Fatal("stored key not reusable", err)
	}
	if _, err = s.Vault.DecryptUserConnection("other", connection.ID, stored.EncryptedKey); err == nil {
		t.Fatal("ciphertext not bound to owner")
	}
	response := memberRequest(t, s, "DELETE", "/api/v1/user/connections/"+connection.ID, nil, b, tokenB)
	if response.Code != 404 {
		t.Fatal("other user deleted connection", response.Code)
	}
	var calls atomic.Int32
	s.Client = upstream.NewTestClient(visitorDoer(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+key {
			t.Error("saved key not used")
		}
		return visitorResponse(`{"model":"actual","choices":[{"message":{"content":"first reply"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`, "application/json"), nil
	}))
	body := map[string]any{"connection_id": connection.ID, "connection_revision": connection.Revision, "client_request_id": "same-request", "protocol": "chat", "model": "model", "prompt": "independent", "max_tokens": 128, "timeout_seconds": 10}
	response = visitorPOSTWithMember(t, s, "/api/v1/test/generate", body, "", a, tokenA)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "event: record") || !strings.Contains(response.Body.String(), "event: done") {
		t.Fatal(response.Code, response.Body.String())
	}
	page, err := s.Store.ListOnlineTestRuns(context.Background(), domain.OnlineTestFilter{UserID: userA.ID})
	if err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	id := page.Items[0].ID
	if page.Items[0].Text != "" || page.Items[0].Prompt != "" {
		t.Fatal("list loaded private large payload")
	}
	run, err := s.Store.GetOnlineTestRun(context.Background(), userA.ID, id)
	if err != nil || run.Text != "first reply" || run.State != "succeeded" {
		t.Fatal(run, err)
	}
	response = visitorPOSTWithMember(t, s, "/api/v1/test/generate", body, "", a, tokenA)
	if !strings.Contains(response.Body.String(), `"reused":true`) || calls.Load() != 1 {
		t.Fatal("generation replayed", response.Body.String(), calls.Load())
	}
	body["prompt"] = "changed"
	response = visitorPOSTWithMember(t, s, "/api/v1/test/generate", body, "", a, tokenA)
	if response.Code != 409 || calls.Load() != 1 {
		t.Fatal("changed duplicate accepted", response.Code)
	}
	for _, path := range []string{"/api/v1/user/test-records/" + id, "/api/v1/user/test-records/" + id + "/source"} {
		response = memberRequest(t, s, "GET", path, nil, b, tokenB)
		if response.Code != 404 {
			t.Fatal("other user accessed record", response.Code, path)
		}
	}
	response = memberRequest(t, s, "DELETE", "/api/v1/user/test-records/"+id, nil, b, tokenB)
	if response.Code != 404 {
		t.Fatal("other user removed record")
	}
	admin, adminToken := loginForTest(t, s.Handler())
	response = memberRequest(t, s, "GET", "/api/v1/admin/test-records/"+id, nil, admin, adminToken)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "first reply") {
		t.Fatal("admin cannot audit", response.Body.String())
	}
	response = memberRequest(t, s, "GET", "/api/v1/admin/user-connections", nil, admin, adminToken)
	if response.Code != 200 || strings.Contains(response.Body.String(), key) || strings.Contains(response.Body.String(), stored.EncryptedKey) {
		t.Fatal("admin exposed saved key")
	}
	response = memberRequest(t, s, "POST", "/api/v1/test/generate", body, admin, adminToken)
	if response.Code != 401 {
		t.Fatal("admin used member connection", response.Code)
	}
	response = memberRequest(t, s, "GET", "/api/v1/public/artifacts/"+id, nil, nil, "")
	if response.Code != 404 {
		t.Fatal("private result published")
	}
	response = memberRequest(t, s, "DELETE", "/api/v1/user/connections/"+connection.ID, nil, a, tokenA)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	run, err = s.Store.GetOnlineTestRun(context.Background(), userA.ID, id)
	if err != nil || run.ConnectionID != "" || run.Text != "first reply" {
		t.Fatal("deleting connection destroyed history", run, err)
	}
}

func TestSavedConnectionHostChangeDisabledAndMixedRequest(t *testing.T) {
	s, _, _, _ := publicFixture(t)
	cookie, csrf, user := registerMemberForAuthTest(t, s.Handler(), "saved-host")
	c := privateConnectionForTest(t, s, cookie, csrf)
	encoded, _ := json.Marshal(c)
	var body map[string]any
	_ = json.Unmarshal(encoded, &body)
	body["base_url"] = "https://another.example/v1"
	body["api_key"] = ""
	res := memberRequest(t, s, "PUT", "/api/v1/user/connections/"+c.ID, body, cookie, csrf)
	if res.Code != 400 {
		t.Fatal("host changed without new key", res.Code)
	}
	body["base_url"] = c.BaseURL
	body["disabled"] = false
	if err := s.Store.SetUserConnectionDisabled(context.Background(), c.ID, true); err != nil {
		t.Fatal(err)
	}
	res = memberRequest(t, s, "PUT", "/api/v1/user/connections/"+c.ID, body, cookie, csrf)
	if res.Code != 409 {
		t.Fatal("stale edit accepted")
	}
	current, _ := s.Store.GetUserConnection(context.Background(), user.ID, c.ID)
	body["revision"] = current.Revision
	res = memberRequest(t, s, "PUT", "/api/v1/user/connections/"+c.ID, body, cookie, csrf)
	if res.Code != 200 {
		t.Fatal(res.Body.String())
	}
	current, _ = s.Store.GetUserConnection(context.Background(), user.ID, c.ID)
	if !current.Disabled {
		t.Fatal("member enabled disabled connection")
	}
	res = visitorPOSTWithMember(t, s, "/api/v1/test/models", map[string]any{"connection_id": c.ID}, "", cookie, csrf)
	if res.Code != 403 {
		t.Fatal("disabled connection called")
	}
	res = visitorPOSTWithMember(t, s, "/api/v1/test/models", map[string]any{"connection_id": c.ID, "base_url": "https://another.example"}, "", cookie, csrf)
	if res.Code != 400 {
		t.Fatal("mixed endpoint accepted")
	}
}

func TestPrivatePartialResponseCancellationAndFailureAreSaved(t *testing.T) {
	for _, cancelRequest := range []bool{false, true} {
		t.Run(map[bool]string{false: "truncated", true: "canceled"}[cancelRequest], func(t *testing.T) {
			s, _, _, _ := publicFixture(t)
			cookie, csrf, user := registerMemberForAuthTest(t, s.Handler(), "partial-save")
			connection := privateConnectionForTest(t, s, cookie, csrf)
			requestCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelRequest {
				s.Client = upstream.NewTestClient(visitorDoer(func(r *http.Request) (*http.Response, error) {
					pr, pw := io.Pipe()
					go func() {
						_, _ = pw.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"partial saved output\"}}]}\n\n"))
						time.Sleep(40 * time.Millisecond)
						cancel()
						_ = pw.CloseWithError(context.Canceled)
					}()
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: pr}, nil
				}))
			} else {
				s.Client = upstream.NewTestClient(visitorDoer(func(*http.Request) (*http.Response, error) {
					return visitorResponse("data: {\"choices\":[{\"delta\":{\"content\":\"partial saved output\"}}]}\n\n", "text/event-stream"), nil
				}))
			}
			encoded, _ := json.Marshal(map[string]any{"connection_id": connection.ID, "connection_revision": connection.Revision, "model": "model", "prompt": "single prompt", "max_tokens": 128, "timeout_seconds": 10})
			req := httptest.NewRequest("POST", "/api/v1/test/generate", bytes.NewReader(encoded)).WithContext(requestCtx)
			req.AddCookie(cookie)
			req.Header.Set("X-CSRF-Token", csrf)
			response := httptest.NewRecorder()
			s.Handler().ServeHTTP(response, req)
			list, e := s.Store.ListOnlineTestRuns(context.Background(), domain.OnlineTestFilter{UserID: user.ID})
			if e != nil || len(list.Items) != 1 {
				t.Fatal(e, list)
			}
			run, e := s.Store.GetOnlineTestRun(context.Background(), user.ID, list.Items[0].ID)
			if e != nil || run.Text != "partial saved output" {
				t.Fatal("partial text missing", run, e)
			}
			want := "failed"
			if cancelRequest {
				want = "canceled"
			}
			if run.State != want || run.Code != "" {
				t.Fatal("incorrect partial state", run.State, run.Code)
			}
		})
	}
}

func TestPrivateHistoryStorageFailuresNeverReplayOrClaimSaved(t *testing.T) {
	for _, phase := range []string{"insert", "finish"} {
		t.Run(phase, func(t *testing.T) {
			s, _, _, _ := publicFixture(t)
			cookie, csrf, _ := registerMemberForAuthTest(t, s.Handler(), "storage-fault")
			connection := privateConnectionForTest(t, s, cookie, csrf)
			var calls atomic.Int32
			s.Client = upstream.NewTestClient(visitorDoer(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return visitorResponse(`{"choices":[{"message":{"content":"received result"},"finish_reason":"stop"}]}`, "application/json"), nil
			}))
			trigger := `CREATE TRIGGER fault BEFORE INSERT ON online_test_runs BEGIN SELECT RAISE(FAIL,'injected storage failure'); END`
			if phase == "finish" {
				trigger = `CREATE TRIGGER fault BEFORE UPDATE ON online_test_runs WHEN NEW.state='succeeded' BEGIN SELECT RAISE(FAIL,'injected storage failure'); END`
			}
			if _, e := s.Store.DB.Exec(trigger); e != nil {
				t.Fatal(e)
			}
			response := visitorPOSTWithMember(t, s, "/api/v1/test/generate", map[string]any{"connection_id": connection.ID, "connection_revision": connection.Revision, "model": "model", "prompt": "hi", "max_tokens": 128, "timeout_seconds": 5}, "", cookie, csrf)
			if phase == "insert" {
				if calls.Load() != 0 || response.Code != 500 {
					t.Fatal("upstream called before record was committed", calls.Load(), response.Code)
				}
			} else {
				if calls.Load() != 1 || !strings.Contains(response.Body.String(), `"saved":false`) || strings.Contains(response.Body.String(), "event: done") {
					t.Fatal("storage failure claimed success", calls.Load(), response.Body.String())
				}
			}
		})
	}
}

func TestPrivateHistoryAllProtocolsPersistOnlySuccessfulArtifact(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			s, _, _, _ := publicFixture(t)
			cookie, csrf, user := registerMemberForAuthTest(t, s.Handler(), "protocol-history")
			connection := privateConnectionForTest(t, s, cookie, csrf)
			code := `<svg xmlns="http://www.w3.org/2000/svg"><circle r="3"/></svg>`
			s.Client = upstream.NewTestClient(visitorDoer(func(r *http.Request) (*http.Response, error) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if _, exists := body["previous_response_id"]; exists {
					t.Fatal("history became conversation context")
				}
				var payload any
				switch protocol {
				case "chat":
					payload = map[string]any{"model": "served", "choices": []any{map[string]any{"message": map[string]string{"content": code}, "finish_reason": "stop"}}}
				case "responses":
					payload = map[string]any{"model": "served", "status": "completed", "output": []any{map[string]any{"type": "message", "content": []any{map[string]string{"type": "output_text", "text": code}}}}}
				case "anthropic":
					payload = map[string]any{"model": "served", "type": "message", "content": []any{map[string]string{"type": "text", "text": code}}, "stop_reason": "end_turn"}
				}
				encoded, _ := json.Marshal(payload)
				return visitorResponse(string(encoded), "application/json"), nil
			}))
			response := visitorPOSTWithMember(t, s, "/api/v1/test/generate", map[string]any{"connection_id": connection.ID, "connection_revision": connection.Revision, "protocol": protocol, "model": "model", "prompt": domain.EnglishPrompt, "template_id": "en_svg", "max_tokens": 128, "timeout_seconds": 5}, "", cookie, csrf)
			if !strings.Contains(response.Body.String(), "event: done") || !strings.Contains(response.Body.String(), "event: artifact") {
				t.Fatal(response.Body.String())
			}
			page, err := s.Store.ListOnlineTestRuns(context.Background(), domain.OnlineTestFilter{UserID: user.ID})
			if err != nil || len(page.Items) != 1 {
				t.Fatal(page, err)
			}
			run, err := s.Store.GetOnlineTestRun(context.Background(), user.ID, page.Items[0].ID)
			if err != nil || run.State != "succeeded" || run.Code != code || run.Kind != "svg" {
				t.Fatal(run, err)
			}
			if public := publicGET(t, s, "/api/v1/public/artifacts"); strings.Contains(public.Body.String(), run.ID) {
				t.Fatal("private generated artifact became public")
			}
		})
	}
}
