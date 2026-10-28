package server

import (
	"context"
	"encoding/json"
	"net/http"
	"relaylab/internal/domain"
	"relaylab/internal/upstream"
	"strings"
	"testing"
)

func TestFailedOnlineStreamReturnsPersistedMetricsAndNoSuccess(t *testing.T) {
	s, _, _, _ := publicFixture(t)
	cookie, csrf, user := registerMemberForAuthTest(t, s.Handler(), "metrics-member")
	connection := privateConnectionForTest(t, s, cookie, csrf)
	s.Client = upstream.NewTestClient(visitorDoer(func(r *http.Request) (*http.Response, error) {
		return visitorResponse("data: {\"choices\":[{\"delta\":{\"content\":\"partial saved output\"}}]}\n\n", "text/event-stream"), nil
	}))
	response := visitorPOSTWithMember(t, s, "/api/v1/test/generate", map[string]any{"connection_id": connection.ID, "connection_revision": connection.Revision, "client_request_id": "metrics-one", "protocol": "chat", "model": "m", "prompt": "current prompt only", "max_tokens": 128, "timeout_seconds": 10}, "", cookie, csrf)
	if response.Code != 200 || strings.Contains(response.Body.String(), "event: done") {
		t.Fatal(response.Code, response.Body.String())
	}
	var event map[string]any
	for _, part := range strings.Split(response.Body.String(), "\n\n") {
		if strings.HasPrefix(part, "event: error\n") {
			if err := json.Unmarshal([]byte(strings.TrimPrefix(part, "event: error\ndata: ")), &event); err != nil {
				t.Fatal(err)
			}
		}
	}
	if event == nil || event["saved"] != true || event["state"] != "failed" || event["first_text_ms"] == nil || event["http_status"] != float64(200) || event["context_policy"] != "independent" || event["text_bytes"] != float64(20) {
		t.Fatalf("metrics event %+v", event)
	}
	run, err := s.Store.GetOnlineTestRun(context.Background(), user.ID, event["record_id"].(string))
	if err != nil || run.Text != "partial saved output" || run.FirstTextMS == nil || int64(event["duration_ms"].(float64)) != run.DurationMS || run.Code != "" {
		t.Fatalf("saved run %+v %v", run, err)
	}
	page, err := s.Store.ListOnlineTestRuns(context.Background(), domain.OnlineTestFilter{UserID: user.ID})
	if err != nil || len(page.Items) != 1 {
		t.Fatal(err)
	}
}
