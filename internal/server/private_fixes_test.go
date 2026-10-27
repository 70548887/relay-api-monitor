package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"relaylab/internal/domain"
	"relaylab/internal/upstream"
)

func TestPrivateHistoryWritesCannotLeaveUnstoppableRows(t *testing.T) {
	for _, phase := range []string{"start", "finish", "all"} {
		t.Run(phase, func(t *testing.T) {
			s, _, _, _ := publicFixture(t)
			cookie, csrf, user := registerMemberForAuthTest(t, s.Handler(), "orphan-fix")
			connection := privateConnectionForTest(t, s, cookie, csrf)
			calls := 0
			s.Client = upstream.NewTestClient(visitorDoer(func(*http.Request) (*http.Response, error) {
				calls++
				return visitorResponse(`{"choices":[{"message":{"content":"completed output"},"finish_reason":"stop"}]}`, "application/json"), nil
			}))
			condition := "NEW.state='running'"
			if phase == "finish" {
				condition = "NEW.state='succeeded'"
			}
			if phase == "all" {
				condition = "1=1"
			}
			_, err := s.Store.DB.Exec(`CREATE TRIGGER audit_write_failure BEFORE UPDATE ON online_test_runs WHEN ` + condition + ` BEGIN SELECT RAISE(FAIL,'temporary write failure'); END`)
			if err != nil {
				t.Fatal(err)
			}
			response := visitorPOSTWithMember(t, s, "/api/v1/test/generate", map[string]any{"connection_id": connection.ID, "connection_revision": connection.Revision, "model": "fixture", "prompt": "isolated regression", "max_tokens": 128, "timeout_seconds": 30}, "", cookie, csrf)
			if _, err = s.Store.DB.Exec("DROP TRIGGER audit_write_failure"); err != nil {
				t.Fatal(err)
			}
			if phase == "finish" {
				if calls != 1 || !strings.Contains(response.Body.String(), `"saved":false`) {
					t.Fatal(calls, response.Body.String())
				}
			} else if calls != 0 {
				t.Fatal("called without saved running state")
			}
			items, err := s.Store.ListOnlineTestRuns(context.Background(), domain.OnlineTestFilter{UserID: user.ID})
			if err != nil || len(items.Items) != 1 {
				t.Fatal(items, err)
			}
			id := items.Items[0].ID
			// The request has ended. Reconciliation must not wait for a program restart.
			if err = s.recoverInactivePrivateRuns(context.Background()); err != nil {
				t.Fatal(err)
			}
			record, err := s.Store.GetOnlineTestRun(context.Background(), user.ID, id)
			if err != nil {
				t.Fatal(err)
			}
			if record.State != "interrupted" || record.FinishedAt == nil || record.Code != "" {
				t.Fatalf("orphaned state %+v", record)
			}
			if phase == "finish" && record.Text != "completed output" {
				t.Fatal("final received output lost")
			}
			stopped := memberRequest(t, s, "POST", "/api/v1/user/test-records/"+id+"/cancel", map[string]any{}, cookie, csrf)
			if stopped.Code != 200 {
				t.Fatal(stopped.Body.String())
			}
			removed := memberRequest(t, s, "DELETE", "/api/v1/user/test-records/"+id, nil, cookie, csrf)
			if removed.Code != 200 {
				t.Fatal("record cannot be removed", removed.Body.String())
			}
		})
	}
}

func TestPrivateConnectionRevisionRejectsStaleOrMissingBeforeDispatch(t *testing.T) {
	s, _, _, _ := publicFixture(t)
	cookie, csrf, _ := registerMemberForAuthTest(t, s.Handler(), "revision-fix")
	connection := privateConnectionForTest(t, s, cookie, csrf)
	encoded, _ := json.Marshal(connection)
	var edit map[string]any
	_ = json.Unmarshal(encoded, &edit)
	edit["base_url"] = "https://changed-fixture.example/v2"
	edit["api_key"] = "fixture-new-key-after-edit"
	changed := memberRequest(t, s, "PUT", "/api/v1/user/connections/"+connection.ID, edit, cookie, csrf)
	if changed.Code != 200 {
		t.Fatal(changed.Body.String())
	}
	calls := 0
	s.Client = upstream.NewTestClient(visitorDoer(func(*http.Request) (*http.Response, error) {
		calls++
		return visitorResponse(`{"data":[],"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`, "application/json"), nil
	}))
	for _, path := range []string{"/api/v1/test/models", "/api/v1/test/generate"} {
		for _, rev := range []int{0, connection.Revision} {
			body := map[string]any{"connection_id": connection.ID, "model": "m", "prompt": "prompt", "max_tokens": 128, "timeout_seconds": 5}
			if rev > 0 {
				body["connection_revision"] = rev
			}
			response := visitorPOSTWithMember(t, s, path, body, "", cookie, csrf)
			want := 409
			if rev == 0 {
				want = 400
			}
			if response.Code != want {
				t.Fatal(path, rev, response.Code, response.Body.String())
			}
		}
	}
	if calls != 0 {
		t.Fatal("stale client sent billable request")
	}
	response := visitorPOSTWithMember(t, s, "/api/v1/test/generate", map[string]any{"connection_id": connection.ID, "connection_revision": connection.Revision + 1, "model": "m", "prompt": "prompt", "max_tokens": 128, "timeout_seconds": 5}, "", cookie, csrf)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "event: done") || calls != 1 {
		t.Fatal("confirmed revision failed", response.Body.String(), calls)
	}
}

func TestPrivateTemplateProvenanceMatchesSentPrompt(t *testing.T) {
	s, _, _, _ := publicFixture(t)
	cookie, csrf, user := registerMemberForAuthTest(t, s.Handler(), "template-fix")
	c := privateConnectionForTest(t, s, cookie, csrf)
	current, _ := s.Store.GetTemplate(context.Background(), "cn_html")
	current.Version = 2
	current.Prompt = "Version two prompt"
	encoded, _ := json.Marshal(current)
	_, err := s.Store.DB.Exec("UPDATE templates SET data=? WHERE id='cn_html'", string(encoded))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	s.Client = upstream.NewTestClient(visitorDoer(func(*http.Request) (*http.Response, error) {
		calls++
		return visitorResponse(`{"choices":[{"message":{"content":"<html><body>ok</body></html>"},"finish_reason":"stop"}]}`, "application/json"), nil
	}))
	for _, test := range []struct {
		prompt  string
		version int
	}{{domain.ChinesePrompt, 1}, {current.Prompt, 2}, {"Custom original content", 0}} {
		response := visitorPOSTWithMember(t, s, "/api/v1/test/generate", map[string]any{"connection_id": c.ID, "connection_revision": c.Revision, "model": "m", "prompt": test.prompt, "template_id": "cn_html", "max_tokens": 128, "timeout_seconds": 5}, "", cookie, csrf)
		if !strings.Contains(response.Body.String(), "event: done") {
			t.Fatal(response.Body.String())
		}
		var recordID string
		for _, line := range strings.Split(response.Body.String(), "\n") {
			if strings.HasPrefix(line, "data: ") {
				var event struct {
					RecordID string `json:"record_id"`
				}
				_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event)
				if event.RecordID != "" {
					recordID = event.RecordID
					break
				}
			}
		}
		record, e := s.Store.GetOnlineTestRun(context.Background(), user.ID, recordID)
		if e != nil || record.TemplateVersion != test.version || record.Prompt != test.prompt {
			t.Fatal(record, e)
		}
	}
	response := visitorPOSTWithMember(t, s, "/api/v1/test/generate", map[string]any{"connection_id": c.ID, "connection_revision": c.Revision, "model": "m", "prompt": domain.ChinesePrompt, "template_id": "cn_html", "template_version": 2, "max_tokens": 128, "timeout_seconds": 5}, "", cookie, csrf)
	if response.Code != 409 || calls != 3 {
		t.Fatal("unmatched declared version was dispatched", response.Code, calls)
	}
}

func TestDuplicateConnectionEditReturnsConflict(t *testing.T) {
	s, _, _, _ := publicFixture(t)
	cookie, csrf, _ := registerMemberForAuthTest(t, s.Handler(), "duplicate-fix")
	first := privateConnectionForTest(t, s, cookie, csrf)
	body := map[string]any{"name": "second", "base_url": "https://second.example/v1", "api_key": "sk-member-private-very-secret-key", "protocol": "chat", "discovery_protocol": "openai"}
	created := memberRequest(t, s, "POST", "/api/v1/user/connections", body, cookie, csrf)
	if created.Code != 200 {
		t.Fatal(created.Body.String())
	}
	var second domain.UserConnection
	_ = json.Unmarshal(created.Body.Bytes(), &second)
	body["base_url"] = first.BaseURL
	body["revision"] = second.Revision
	response := memberRequest(t, s, "PUT", "/api/v1/user/connections/"+second.ID, body, cookie, csrf)
	if response.Code != 409 || !strings.Contains(response.Body.String(), "connection_exists") {
		t.Fatal(response.Code, response.Body.String())
	}
}

func TestPrivateReaperPreservesActiveRequest(t *testing.T) {
	s, _, _, _ := publicFixture(t)
	cookie, csrf, user := registerMemberForAuthTest(t, s.Handler(), "active-preserved")
	c := privateConnectionForTest(t, s, cookie, csrf)
	now := time.Now().UTC()
	run := domain.OnlineTestRun{ID: "still-active", UserID: user.ID, ConnectionID: c.ID, ConnectionRevision: c.Revision, State: "running", ProcessID: s.privateProcess, ClientRequestID: "id", RequestHash: "h", CreatedAt: now, StartedAt: now}
	if _, _, e := s.Store.CreateOnlineTestRun(context.Background(), run); e != nil {
		t.Fatal(e)
	}
	s.privateCancels[run.ID] = func() {}
	if e := s.recoverInactivePrivateRuns(context.Background()); e != nil {
		t.Fatal(e)
	}
	current, _ := s.Store.GetOnlineTestRun(context.Background(), user.ID, run.ID)
	if current.State != "running" {
		t.Fatal("reaper interrupted live request")
	}
}

func TestPrivateReplayPreservesOwnedHistoricalTemplateVersion(t *testing.T) {
	s, _, _, _ := publicFixture(t)
	cookie, csrf, user := registerMemberForAuthTest(t, s.Handler(), "historical-provenance")
	connection := privateConnectionForTest(t, s, cookie, csrf)
	now := time.Now().UTC()
	old := domain.OnlineTestRun{ID: "original-record", UserID: user.ID, ConnectionID: connection.ID, ConnectionRevision: connection.Revision, ClientRequestID: "old", RequestHash: "old", State: "created", ProcessID: "old-process", CreatedAt: now, StartedAt: now, Prompt: "An older customized prompt", TemplateID: "cn_html", TemplateVersion: 7}
	if _, _, e := s.Store.CreateOnlineTestRun(context.Background(), old); e != nil {
		t.Fatal(e)
	}
	old.State = "succeeded"
	old.FinishedAt = &now
	if e := s.Store.UpdateOnlineTestRun(context.Background(), old); e != nil {
		t.Fatal(e)
	}
	calls := 0
	s.Client = upstream.NewTestClient(visitorDoer(func(*http.Request) (*http.Response, error) {
		calls++
		return visitorResponse(`{"choices":[{"message":{"content":"<html><body>unchanged historical prompt</body></html>"},"finish_reason":"stop"}]}`, "application/json"), nil
	}))
	body := map[string]any{"connection_id": connection.ID, "connection_revision": connection.Revision, "model": "m", "prompt": old.Prompt, "template_id": "cn_html", "template_version": 7, "source_record_id": old.ID, "max_tokens": 128, "timeout_seconds": 5}
	response := visitorPOSTWithMember(t, s, "/api/v1/test/generate", body, "", cookie, csrf)
	if !strings.Contains(response.Body.String(), "event: done") {
		t.Fatal(response.Body.String())
	}
	list, _ := s.Store.ListOnlineTestRuns(context.Background(), domain.OnlineTestFilter{UserID: user.ID})
	record, _ := s.Store.GetOnlineTestRun(context.Background(), user.ID, list.Items[0].ID)
	if record.TemplateVersion != 7 || record.Prompt != old.Prompt {
		t.Fatal(record)
	}
	other, otherCSRF, _ := registerMemberForAuthTest(t, s.Handler(), "other-historical")
	otherConnection := privateConnectionForTest(t, s, other, otherCSRF)
	body["connection_id"] = otherConnection.ID
	body["connection_revision"] = otherConnection.Revision
	response = visitorPOSTWithMember(t, s, "/api/v1/test/generate", body, "", other, otherCSRF)
	if response.Code != 404 || calls != 1 {
		t.Fatal("foreign provenance usable", response.Code, calls)
	}
}
