package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"relaylab/internal/upstream"
)

type visitorDoer func(*http.Request) (*http.Response, error)

func (f visitorDoer) Do(request *http.Request) (*http.Response, error) { return f(request) }
func visitorPOST(t *testing.T, s *Server, path string, body any, origin string) *httptest.ResponseRecorder {
	t.Helper()
	encoded, _ := json.Marshal(body)
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, request)
	return response
}
func visitorBody() map[string]any {
	return map[string]any{"base_url": "https://public-upstream.example/v1", "api_key": "sk-original-test-key-secret", "protocol": "chat", "model": "served-model", "prompt": "hello", "max_tokens": 100, "timeout_seconds": 5}
}
func visitorResponse(body, contentType string) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestVisitorModelsRateLimitAndOrigin(t *testing.T) {
	s, _, _, _ := publicFixture(t)
	calls := 0
	s.Client = upstream.NewTestClient(visitorDoer(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.Header.Get("Authorization") != "Bearer sk-original-test-key-secret" {
			t.Error("key not forwarded")
		}
		return visitorResponse(`{"data":[{"id":"model-one"}]}`, "application/json"), nil
	}))
	response := visitorPOST(t, s, "/api/v1/test/models", visitorBody(), "https://evil.example")
	if response.Code != 403 || calls != 0 {
		t.Fatalf("origin accepted: %d calls=%d", response.Code, calls)
	}
	settings, _ := s.Store.GetSettings(context.Background())
	settings.VisitorModelsPerMinute = 1
	if err := s.Store.SaveSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	response = visitorPOST(t, s, "/api/v1/test/models", visitorBody(), "")
	if response.Code != 200 || !strings.Contains(response.Body.String(), "model-one") || strings.Contains(response.Body.String(), "sk-original-test-key-secret") {
		t.Fatal(response.Body.String())
	}
	response = visitorPOST(t, s, "/api/v1/test/models", visitorBody(), "")
	if response.Code != 429 || calls != 1 {
		t.Fatalf("rate limit: %d calls=%d", response.Code, calls)
	}
	if s.total != 0 || len(s.active) != 0 {
		t.Fatalf("concurrency leak: %+v", s.active)
	}
}

func TestVisitorSSEAndKeyRedactionAcrossFragments(t *testing.T) {
	s, _, _, _ := publicFixture(t)
	s.Client = upstream.NewTestClient(visitorDoer(func(*http.Request) (*http.Response, error) {
		return visitorResponse("data: {\"model\":\"served\",\"choices\":[{\"delta\":{\"content\":\"hi sk-original-\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"test-key-secret done\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n", "text/event-stream"), nil
	}))
	response := visitorPOST(t, s, "/api/v1/test/generate", visitorBody(), "")
	if response.Code != 200 || !strings.Contains(response.Body.String(), "event: meta") || !strings.Contains(response.Body.String(), "event: delta") || !strings.Contains(response.Body.String(), "event: usage") || !strings.Contains(response.Body.String(), "event: done") {
		t.Fatal(response.Body.String())
	}
	for _, secret := range []string{"sk-original-", "test-key-secret"} {
		if strings.Contains(response.Body.String(), secret) {
			t.Errorf("stream key segment exposed: %s", secret)
		}
	}
}

func TestVisitorArtifactAndValidation(t *testing.T) {
	s, _, _, _ := publicFixture(t)
	calls := 0
	s.Client = upstream.NewTestClient(visitorDoer(func(*http.Request) (*http.Response, error) {
		calls++
		return visitorResponse(`{"choices":[{"message":{"content":"<html><body>animated</body></html>"},"finish_reason":"stop"}]}`, "application/json"), nil
	}))
	body := visitorBody()
	body["template_id"] = "cn_html"
	response := visitorPOST(t, s, "/api/v1/test/generate", body, "")
	if !strings.Contains(response.Body.String(), "event: artifact") || !strings.Contains(response.Body.String(), "preview_document") {
		t.Fatal(response.Body.String())
	}
	for _, change := range []map[string]any{{"base_url": "https://api.zxcbug.com"}, {"base_url": "http://127.0.0.1"}, {"max_tokens": 0}, {"timeout_seconds": 601}, {"prompt": strings.Repeat("x", (128<<10)+1)}, {"generate_path": "//evil.example"}} {
		invalid := visitorBody()
		for key, value := range change {
			invalid[key] = value
		}
		response = visitorPOST(t, s, "/api/v1/test/generate", invalid, "")
		if response.Code != 400 {
			t.Fatalf("accepted invalid %+v: %d", change, response.Code)
		}
	}
	if calls != 1 {
		t.Fatalf("invalid requests reached upstream: %d", calls)
	}
}

func TestVisitorCancellationReleasesConcurrency(t *testing.T) {
	s, _, _, _ := publicFixture(t)
	started := make(chan struct{})
	var canceled atomic.Bool
	s.Client = upstream.NewTestClient(visitorDoer(func(request *http.Request) (*http.Response, error) {
		close(started)
		<-request.Context().Done()
		canceled.Store(true)
		return nil, request.Context().Err()
	}))
	encoded, _ := json.Marshal(visitorBody())
	request := httptest.NewRequest(http.MethodPost, "/api/v1/test/generate", bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	ctx, cancel := context.WithCancel(request.Context())
	request = request.WithContext(ctx)
	finished := make(chan struct{})
	response := httptest.NewRecorder()
	go func() { s.Handler().ServeHTTP(response, request); close(finished) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream did not start")
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("canceled request did not stop")
	}
	if !canceled.Load() || s.total != 0 || len(s.active) != 0 {
		t.Fatalf("cancellation/concurrency failure: canceled=%v total=%d", canceled.Load(), s.total)
	}
}

func TestVisitorTimeoutStillSendsSSEError(t *testing.T) {
	s, _, _, _ := publicFixture(t)
	s.Client = upstream.NewTestClient(visitorDoer(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	}))
	body := visitorBody()
	body["timeout_seconds"] = 1
	response := visitorPOST(t, s, "/api/v1/test/generate", body, "")
	if !strings.Contains(response.Body.String(), "event: error") || !strings.Contains(response.Body.String(), `"code":"timeout"`) {
		t.Fatal(response.Body.String())
	}
}

func TestPublicErrorOnlyTrustsNormalizedErrors(t *testing.T) {
	if strings.Contains(publicError(errors.New("private arbitrary error")), "private") {
		t.Fatal("generic error exposed")
	}
	message := publicError(&upstream.Error{Message: "failed Bearer sk-secret-token-value"})
	if strings.Contains(message, "sk-secret-token-value") {
		t.Fatal("normalized error exposed credential")
	}
}

func TestVisitorDiscoveryConcurrencyCappedAtEight(t *testing.T) {
	s, _, _, _ := publicFixture(t)
	settings, _ := s.Store.GetSettings(context.Background())
	settings.VisitorConcurrency = 64
	settings.VisitorIPConcurrency = 8
	if err := s.Store.SaveSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	s.total = 8
	s.Client = upstream.NewTestClient(visitorDoer(func(*http.Request) (*http.Response, error) {
		t.Fatal("ninth discovery reached upstream")
		return nil, nil
	}))
	response := visitorPOST(t, s, "/api/v1/test/models", visitorBody(), "")
	if response.Code != 429 || !strings.Contains(response.Body.String(), "concurrency_limited") {
		t.Fatalf("%d %s", response.Code, response.Body.String())
	}
	if s.total != 8 {
		t.Fatalf("rejected request changed active total: %d", s.total)
	}
}

func TestVisitorConcurrentRejectionDoesNotBlockCancellation(t *testing.T) {
	s, _, _, _ := publicFixture(t)
	settings, _ := s.Store.GetSettings(context.Background())
	settings.VisitorConcurrency = 1
	settings.VisitorIPConcurrency = 1
	if err := s.Store.SaveSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	s.Client = upstream.NewTestClient(visitorDoer(func(request *http.Request) (*http.Response, error) {
		close(started)
		<-request.Context().Done()
		return nil, request.Context().Err()
	}))
	encoded, _ := json.Marshal(visitorBody())
	request := httptest.NewRequest(http.MethodPost, "/api/v1/test/generate", bytes.NewReader(encoded))
	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	request = request.WithContext(ctx)
	done := make(chan struct{})
	go func() { s.Handler().ServeHTTP(httptest.NewRecorder(), request); close(done) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream did not start")
	}
	response := visitorPOST(t, s, "/api/v1/test/models", visitorBody(), "")
	if response.Code != 429 {
		t.Fatalf("second request status=%d", response.Code)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("mutex remained held while upstream blocked")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.total != 0 || len(s.active) != 0 {
		t.Fatal("active slots were not released")
	}
}

func TestOneHundredConcurrentVisitorsEnforceEightGlobalAndTwoPerIP(t *testing.T) {
	s, _, _, _ := publicFixture(t)
	ctx := context.Background()
	settings, _ := s.Store.GetSettings(ctx)
	settings.VisitorConcurrency = 8
	settings.VisitorIPConcurrency = 2
	settings.VisitorGenerationsPerMinute = 120
	if err := s.Store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	type ipKey struct{}
	type observed struct {
		kind   string
		status int
	}
	events := make(chan observed, 200)
	gate := make(chan struct{})
	var closeGate sync.Once
	defer closeGate.Do(func() { close(gate) })
	var countMu sync.Mutex
	current, peak := 0, 0
	perIP := map[string]int{}
	peakPerIP := map[string]int{}
	s.Client = upstream.NewTestClient(visitorDoer(func(request *http.Request) (*http.Response, error) {
		ip, _ := request.Context().Value(ipKey{}).(string)
		countMu.Lock()
		current++
		perIP[ip]++
		if current > peak {
			peak = current
		}
		if perIP[ip] > peakPerIP[ip] {
			peakPerIP[ip] = perIP[ip]
		}
		countMu.Unlock()
		events <- observed{kind: "accepted"}
		select {
		case <-gate:
		case <-request.Context().Done():
			return nil, request.Context().Err()
		}
		countMu.Lock()
		current--
		perIP[ip]--
		if current < 0 || perIP[ip] < 0 {
			t.Error("negative upstream activity")
		}
		countMu.Unlock()
		return visitorResponse(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`, "application/json"), nil
	}))
	handler := s.Handler()
	start := make(chan struct{})
	var group sync.WaitGroup
	for index := 0; index < 100; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			ip := fmt.Sprintf("198.51.100.%d", index%4+1)
			encoded, _ := json.Marshal(visitorBody())
			request := httptest.NewRequest(http.MethodPost, "/api/v1/test/generate", bytes.NewReader(encoded))
			request.RemoteAddr = ip + ":12345"
			request = request.WithContext(context.WithValue(request.Context(), ipKey{}, ip))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			events <- observed{kind: "finished", status: response.Code}
		}(index)
	}
	close(start)
	accepted, rejected := 0, 0
	deadline := time.After(4 * time.Second)
	for accepted+rejected < 100 {
		select {
		case event := <-events:
			if event.kind == "accepted" {
				accepted++
			} else if event.status == 429 {
				rejected++
			} else {
				t.Fatalf("unexpected status before release: %d", event.status)
			}
		case <-deadline:
			t.Fatalf("admission blocked: accepted=%d rejected=%d", accepted, rejected)
		}
	}
	if accepted != 8 || rejected != 92 {
		t.Fatalf("admission counts: accepted=%d rejected=%d", accepted, rejected)
	}
	closeGate.Do(func() { close(gate) })
	group.Wait()
	countMu.Lock()
	if peak != 8 || current != 0 {
		t.Errorf("global concurrency: peak=%d current=%d", peak, current)
	}
	for ip, peak := range peakPerIP {
		if peak != 2 || perIP[ip] != 0 {
			t.Errorf("IP %s: peak=%d current=%d", ip, peak, perIP[ip])
		}
	}
	countMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.total != 0 || len(s.active) != 0 {
		t.Errorf("active slots leaked: total=%d active=%v", s.total, s.active)
	}
}
