package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"relaylab/internal/domain"
	"relaylab/internal/upstream"
)

func auditCompletedPrivateRun(t *testing.T, s *Server, user domain.Member, connection domain.UserConnection, text string) domain.OnlineTestRun {
	t.Helper()
	now := time.Now().UTC()
	run := domain.OnlineTestRun{ID: domain.NewID(), UserID: user.ID, ConnectionID: connection.ID,
		ConnectionRevision: connection.Revision, ClientRequestID: domain.NewID(), RequestHash: "audit-fixture",
		State: "created", ProcessID: s.privateProcess, CreatedAt: now, StartedAt: now,
		Prompt: "independent fixture prompt", Model: "audit-fixture-model"}
	if _, _, err := s.Store.CreateOnlineTestRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	run.State, run.Text, run.Code, run.Kind = "succeeded", text, "<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>", "svg"
	run.FinishedAt = &now
	if err := s.Store.UpdateOnlineTestRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	return run
}

func TestAuditPrivateHistoryMixedOwnerBatchIsAtomicAndQueryCannotChangeOwner(t *testing.T) {
	s, _, _, _ := publicFixture(t)
	alice, aliceCSRF, userA := registerMemberForAuthTest(t, s.Handler(), "audit-history-alice")
	bob, bobCSRF, userB := registerMemberForAuthTest(t, s.Handler(), "audit-history-bob")
	connectionA := privateConnectionForTest(t, s, alice, aliceCSRF)
	connectionB := privateConnectionForTest(t, s, bob, bobCSRF)
	owned := auditCompletedPrivateRun(t, s, userA, connectionA, "alice private fixture reply")
	foreign := auditCompletedPrivateRun(t, s, userB, connectionB, "bob private fixture reply")

	// An injected user_id never overrides the owner from a member session.
	list := memberRequest(t, s, http.MethodGet, "/api/v1/user/test-records?user_id="+userB.ID, nil, alice, "")
	if list.Code != http.StatusOK {
		t.Fatal(list.Code, list.Body.String())
	}
	var page domain.OnlineTestPage
	if err := json.Unmarshal(list.Body.Bytes(), &page); err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != owned.ID {
		t.Fatal("query changed session ownership", err, list.Body.String())
	}
	if strings.Contains(list.Body.String(), "bob private fixture reply") || strings.Contains(list.Body.String(), foreign.ID) {
		t.Fatal("foreign record appeared in private listing")
	}

	// A mixed batch must not remove Alice's row before discovering Bob's row.
	for _, ids := range [][]string{{owned.ID, foreign.ID}, {foreign.ID, owned.ID}} {
		response := memberRequest(t, s, http.MethodPost, "/api/v1/user/test-records/batch-delete", map[string]any{"ids": ids}, alice, aliceCSRF)
		if response.Code != http.StatusNotFound {
			t.Fatal("mixed-owner batch accepted", response.Code, response.Body.String())
		}
		for _, pair := range []struct{ user, id string }{{userA.ID, owned.ID}, {userB.ID, foreign.ID}} {
			if _, err := s.Store.GetOnlineTestRun(context.Background(), pair.user, pair.id); err != nil {
				t.Fatal("mixed batch was not atomic", err)
			}
		}
	}
	for _, path := range []string{
		"/api/v1/user/test-records/" + foreign.ID,
		"/api/v1/user/test-records/" + foreign.ID + "/source",
		"/api/v1/user/test-records/" + foreign.ID + "/source?format=text",
	} {
		response := memberRequest(t, s, http.MethodGet, path, nil, alice, "")
		if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), foreign.Text) {
			t.Fatal("foreign content downloadable", path, response.Code)
		}
	}
	stop := memberRequest(t, s, http.MethodPost, "/api/v1/user/test-records/"+foreign.ID+"/cancel", map[string]any{}, alice, aliceCSRF)
	if stop.Code != http.StatusNotFound {
		t.Fatal("foreign record cancellation accepted", stop.Code)
	}
	content := memberRequest(t, s, http.MethodGet, "/api/v1/user/test-records/"+owned.ID+"/source?format=text", nil, alice, "")
	if content.Code != http.StatusOK || content.Body.String() != owned.Text || !strings.HasPrefix(content.Header().Get("Content-Type"), "text/plain") || !strings.Contains(content.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatal("owned download did not use authenticated plain-text attachment", content.Code)
	}
}

func TestAuditSavedConnectionCrossRoleAndForeignDispatchCannotReachUpstream(t *testing.T) {
	s, _, _, _ := publicFixture(t)
	alice, aliceCSRF, userA := registerMemberForAuthTest(t, s.Handler(), "audit-connection-alice")
	bob, bobCSRF, userB := registerMemberForAuthTest(t, s.Handler(), "audit-connection-bob")
	connection := privateConnectionForTest(t, s, alice, aliceCSRF)
	admin, adminCSRF := loginForTest(t, s.Handler())
	var calls atomic.Int32
	s.Client = upstream.NewTestClient(visitorDoer(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return visitorResponse(`{"choices":[{"message":{"content":"must not be called"},"finish_reason":"stop"}]}`, "application/json"), nil
	}))

	list := memberRequest(t, s, http.MethodGet, "/api/v1/user/connections?user_id="+userA.ID, nil, bob, "")
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), connection.ID) {
		t.Fatal("foreign connection query visible", list.Code, list.Body.String())
	}
	edit := memberRequest(t, s, http.MethodPut, "/api/v1/user/connections/"+connection.ID, map[string]any{
		"name": "foreign overwrite", "base_url": connection.BaseURL, "revision": connection.Revision,
		"protocol": "chat", "user_id": userA.ID, "api_key": "audit-placeholder-not-real",
	}, bob, bobCSRF)
	if edit.Code != http.StatusNotFound {
		t.Fatal("foreign connection edited", edit.Code, edit.Body.String())
	}
	for _, endpoint := range []string{"/api/v1/test/models", "/api/v1/test/generate"} {
		body := map[string]any{"connection_id": connection.ID, "connection_revision": connection.Revision,
			"client_request_id": "audit-private-id", "model": "fixture", "prompt": "single request", "max_tokens": 128, "timeout_seconds": 5}
		for _, account := range []struct {
			cookie *http.Cookie
			csrf   string
			want   int
		}{{bob, bobCSRF, http.StatusNotFound}, {admin, adminCSRF, http.StatusUnauthorized}} {
			response := memberRequest(t, s, http.MethodPost, endpoint, body, account.cookie, account.csrf)
			if response.Code != account.want {
				t.Fatal(endpoint, response.Code, account.want, response.Body.String())
			}
		}
	}
	for _, path := range []string{"/api/v1/admin/user-connections", "/api/v1/admin/test-records"} {
		response := memberRequest(t, s, http.MethodGet, path, nil, bob, "")
		if response.Code != http.StatusUnauthorized {
			t.Fatal("member read administrative records", path, response.Code)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("denied request reached upstream", calls.Load())
	}
	for _, user := range []domain.Member{userA, userB} {
		page, err := s.Store.ListOnlineTestRuns(context.Background(), domain.OnlineTestFilter{UserID: user.ID})
		if err != nil || page.Total != 0 {
			t.Fatal("denied request created a private test row", err, page.Total)
		}
	}
}
