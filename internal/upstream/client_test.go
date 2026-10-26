package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

type byteReader struct{ reader io.Reader }

func (r byteReader) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return r.reader.Read(p)
}

func fixture(body, contentType string, status int) *Client {
	return NewTestClient(doerFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(byteReader{strings.NewReader(body)}), Request: r}, nil
	}))
}
func request(protocol string) Request {
	return Request{Config: Config{BaseURL: "https://example.test", APIKey: "test-secret-key", Protocol: protocol}, Model: "requested", Prompt: "hello", MaxTokens: 123}
}
func errorCode(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestThreeProtocolStreaming(t *testing.T) {
	cases := []struct {
		name, body, want string
		input, output    int64
	}{
		{"chat", ": heartbeat\r\n\r\ndata: {\r\ndata: \"model\":\"served\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"ignored\"}}]}\r\n\r\ndata: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"鹈鹕🚲\"}}]}\r\n\r\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":3}}\r\n\r\ndata: [DONE]\r\n\r\n", "鹈鹕🚲", 2, 3},
		{"responses", "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"model\":\"served\",\"status\":\"in_progress\"}}\n\nevent: response.reasoning_text.delta\ndata: {\"type\":\"response.reasoning_text.delta\",\"delta\":\"ignored\"}\n\nevent: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"鹈鹕🚲\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"model\":\"served\",\"status\":\"completed\",\"usage\":{\"input_tokens\":2,\"output_tokens\":3}}}\n\n", "鹈鹕🚲", 2, 3},
		{"anthropic", "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"served\",\"usage\":{\"input_tokens\":2,\"output_tokens\":0}}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"ignored\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"鹈鹕🚲\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", "鹈鹕🚲", 2, 3},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			events := []Event{}
			result, err := fixture(test.body, "text/event-stream", 200).Generate(context.Background(), request(test.name), func(event Event) error { events = append(events, event); return nil })
			if err != nil {
				t.Fatal(err)
			}
			if !result.Complete || result.Text != test.want || result.FirstTextMS == nil || result.ReturnedModel != "served" {
				t.Fatalf("bad result: %+v", result)
			}
			if result.Usage.InputTokens == nil || *result.Usage.InputTokens != test.input || result.Usage.OutputTokens == nil || *result.Usage.OutputTokens != test.output || result.Usage.TotalTokens != nil {
				t.Fatalf("bad usage: %+v", result.Usage)
			}
			if len(events) < 2 || events[0].Type != "meta" || events[0].HTTPStatus != 200 {
				t.Fatalf("missing initial meta: %+v", events)
			}
		})
	}
}

func TestThreeProtocolJSON(t *testing.T) {
	for _, test := range []struct{ name, body string }{
		{"chat", `{"model":"served","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`},
		{"responses", `{"model":"served","status":"completed","output":[{"type":"reasoning","content":[{"type":"summary_text","text":"ignored"}]},{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`},
		{"anthropic", `{"type":"message","model":"served","content":[{"type":"thinking","text":"ignored"},{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := fixture(test.body, "application/json", 200).Generate(context.Background(), request(test.name), nil)
			if err != nil || result.Text != "ok" || !result.Complete {
				t.Fatalf("%+v, %v", result, err)
			}
			if result.Usage.InputTokens != nil || result.Usage.OutputTokens != nil || result.Usage.TotalTokens != nil {
				t.Fatal("invented missing usage")
			}
		})
	}
}

func TestIncompleteAndErrors(t *testing.T) {
	for _, test := range []struct{ name, protocol, body, contentType, want string }{
		{"only done", "chat", "data: [DONE]\n\n", "text/event-stream", "incomplete_stream"},
		{"eof before done", "chat", "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n", "text/event-stream", "incomplete_stream"},
		{"length", "chat", `{"choices":[{"message":{"content":"partial"},"finish_reason":"length"}]}`, "application/json", "truncated"},
		{"empty", "chat", `{"choices":[{"message":{"content":""},"finish_reason":"stop"}]}`, "application/json", "empty_output"},
		{"tool only", "chat", `{"choices":[{"message":{"tool_calls":[{}]},"finish_reason":"tool_calls"}]}`, "application/json", "empty_output"},
		{"responses limit", "responses", `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}`, "application/json", "truncated"},
		{"responses fail", "responses", "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"message\":\"failed\"}}}\n\n", "text/event-stream", "upstream_error"},
		{"anthropic missing stop", "anthropic", "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", "text/event-stream", "anthropic_missing_terminal"},
		{"event error", "anthropic", "event: error\ndata: {\"error\":{\"type\":\"overloaded_error\",\"message\":\"busy\"}}\n\n", "text/event-stream", "upstream_error"},
		{"unterminated terminal", "chat", "data: [DONE]", "text/event-stream", "incomplete_stream"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := fixture(test.body, test.contentType, 200).Generate(context.Background(), request(test.protocol), nil)
			if errorCode(err) != test.want || result.Complete {
				t.Fatalf("result=%+v error=%v code=%s", result, err, errorCode(err))
			}
		})
	}
}

func TestBodyAndEndpoints(t *testing.T) {
	for _, test := range []struct{ protocol, path, field string }{{"chat", "/v1/chat/completions", "max_completion_tokens"}, {"responses", "/v1/responses", "max_output_tokens"}, {"anthropic", "/v1/messages", "max_tokens"}} {
		t.Run(test.protocol, func(t *testing.T) {
			client := NewTestClient(doerFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path != test.path {
					t.Errorf("path=%s", r.URL.Path)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body[test.field] != float64(123) || body["stream"] != true || body["temperature"] != nil {
					t.Errorf("body=%+v", body)
				}
				if test.protocol == "anthropic" {
					if r.Header.Get("x-api-key") == "" || r.Header.Get("anthropic-version") != "2023-06-01" {
						t.Error("missing Anthropic headers")
					}
				} else if r.Header.Get("Authorization") != "Bearer test-secret-key" {
					t.Error("missing bearer")
				}
				return &http.Response{StatusCode: 401, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"bad test-secret-key Bearer test-secret-key"}}`))}, nil
			}))
			r, err := client.Generate(context.Background(), request(test.protocol), nil)
			if err == nil || r.HTTPStatus != 401 || strings.Contains(err.Error(), "test-secret-key") {
				t.Fatalf("%+v %v", r, err)
			}
		})
	}
	for _, test := range []struct{ base, path, want string }{{"https://example.test", "", "/v1/models"}, {"https://example.test/v2/", "", "/v2/models"}, {"https://example.test/api/v1", "/custom/models", "/custom/models"}} {
		u, err := endpoint(Config{BaseURL: test.base}, test.path, "/models")
		if err != nil || u.Path != test.want {
			t.Fatalf("%v %v", u, err)
		}
	}
}

func TestProtectedAndInvalidURLNeverRequest(t *testing.T) {
	client := NewTestClient(doerFunc(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected request"); return nil, nil }))
	for _, base := range []string{"https://api.zxcbug.com", "https://x.api.zxcbug.com/v1", "https://API.ZXCBUG.COM./", "https://u:p@example.test", "https://example.test?key=secret", "https://example.test/#secret", "file:///etc/passwd"} {
		_, err := client.ListModels(context.Background(), Config{BaseURL: base})
		if err == nil {
			t.Fatalf("accepted %s", base)
		}
	}
	for _, path := range []string{"https://other.test/models", "//other.test/models", "/models?api_key=secret", "/models#fragment", "/\\other.test/models"} {
		_, err := client.ListModels(context.Background(), Config{BaseURL: "https://example.test", ModelsPath: path})
		if errorCode(err) != "invalid_url" {
			t.Fatalf("accepted %s: %v", path, err)
		}
	}
}

func TestPublicIPAndDNSValidation(t *testing.T) {
	for _, value := range []string{"127.0.0.1", "10.0.0.1", "172.16.1.1", "192.168.0.1", "100.64.0.1", "169.254.169.254", "192.0.0.1", "192.0.2.1", "198.18.0.1", "198.51.100.1", "203.0.113.1", "224.0.0.1", "240.0.0.1", "::1", "::", "::ffff:127.0.0.1", "fc00::1", "fe80::1", "2001:db8::1", "2002:7f00:1::1", "64:ff9b::7f00:1"} {
		if publicIP(netip.MustParseAddr(value)) {
			t.Errorf("accepted reserved %s", value)
		}
	}
	for _, value := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		if !publicIP(netip.MustParseAddr(value)) {
			t.Errorf("rejected public %s", value)
		}
	}
	client := New()
	calls := 0
	client.doer = doerFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		approved, ok := r.Context().Value(validatedIPsKey{}).(validatedIPs)
		if !ok || len(approved.addrs) != 1 || approved.addrs[0].String() != "8.8.8.8" {
			t.Error("missing pinned validation")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"data":[]}`))}, nil
	})
	client.resolver = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("127.0.0.1")}, nil
	}
	_, err := client.ListModels(context.Background(), Config{BaseURL: "https://example.test"})
	if errorCode(err) != "blocked_target" || calls != 0 {
		t.Fatalf("%v calls=%d", err, calls)
	}
	client.resolver = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}
	_, err = client.ListModels(context.Background(), Config{BaseURL: "https://example.test"})
	if err != nil || calls != 1 {
		t.Fatalf("%v calls=%d", err, calls)
	}
	production := New().doer.(*http.Client)
	if production.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Error("redirects allowed")
	}
	if production.Transport.(*http.Transport).Proxy != nil {
		t.Error("environment proxy enabled")
	}
}

func TestListModelsAndCallback(t *testing.T) {
	client := fixture(`{"data":[{"id":"b"},{"id":"a"},{"id":"b"},{"id":""}]}`, "application/json", 200)
	models, err := client.ListModels(context.Background(), Config{BaseURL: "https://example.test"})
	if err != nil || strings.Join(models, ",") != "a,b" {
		t.Fatalf("%v %v", models, err)
	}
	sentinel := errors.New("caller stopped")
	_, err = fixture(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`, "application/json", 200).Generate(context.Background(), request("chat"), func(event Event) error {
		if event.Type == "text" {
			return sentinel
		}
		return nil
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("callback error lost: %v", err)
	}
}

func TestCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := request("chat")
	req.Config.BaseURL = server.URL
	req.Timeout = time.Second
	_, err := NewTestClient(server.Client()).Generate(ctx, req, func(event Event) error {
		if event.Type == "meta" {
			cancel()
		}
		return nil
	})
	if errorCode(err) != "canceled" {
		t.Fatalf("%v", err)
	}
}

func TestNoRedirectOrRetry(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Location", "/second")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	testHTTP := server.Client()
	testHTTP.CheckRedirect = New().doer.(*http.Client).CheckRedirect
	_, err := NewTestClient(testHTTP).ListModels(context.Background(), Config{BaseURL: server.URL})
	if errorCode(err) != "redirect_blocked" || hits != 1 {
		t.Fatalf("redirect or retry occurred: hits=%d err=%v", hits, err)
	}
}

func TestTerminalDoesNotWaitForConnectionClose(t *testing.T) {
	for _, test := range []struct{ protocol, body string }{
		{"chat", "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"},
		{"responses", "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}]}}\n\n"},
		{"anthropic", "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"},
	} {
		t.Run(test.protocol, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, test.body)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer server.Close()
			req := request(test.protocol)
			req.Config.BaseURL = server.URL
			req.Timeout = time.Second
			result, err := NewTestClient(server.Client()).Generate(context.Background(), req, nil)
			if err != nil || !result.Complete {
				t.Fatalf("%+v %v", result, err)
			}
		})
	}
}

func TestResponseSizeLimit(t *testing.T) {
	_, err := fixture(strings.Repeat("x", maxResponseBytes+1), "application/json", 200).Generate(context.Background(), request("chat"), nil)
	if errorCode(err) != "response_too_large" {
		t.Fatalf("%v", err)
	}
	err = readSSE(strings.NewReader(strings.Repeat(": heartbeat\n\n", maxResponseBytes/10)), func(string, string) error { return nil })
	if errorCode(err) != "response_too_large" {
		t.Fatalf("%v", err)
	}
}

func TestCredentialEchoRedactedBeforeCallbacksAndResult(t *testing.T) {
	key := "test-secret-key"
	for _, test := range []struct{ protocol, body string }{
		{"chat", `{"model":"test-secret-key","choices":[{"message":{"content":"before test-secret-key after"},"finish_reason":"stop"}]}`},
		{"responses", `{"model":"test-secret-key","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"before test-secret-key after"}]}]}`},
		{"anthropic", `{"model":"test-secret-key","content":[{"type":"text","text":"before test-secret-key after"}],"stop_reason":"end_turn"}`},
	} {
		t.Run(test.protocol, func(t *testing.T) {
			var streamed strings.Builder
			result, err := fixture(test.body, "application/json", 200).Generate(context.Background(), request(test.protocol), func(event Event) error {
				if strings.Contains(event.Text, key) || strings.Contains(event.ReturnedModel, key) {
					t.Error("credential reached callback")
				}
				streamed.WriteString(event.Text)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(result.Text, key) || strings.Contains(result.ReturnedModel, key) || result.Text != "before [redacted] after" || streamed.String() != result.Text {
				t.Fatalf("credential redaction failed: result=%+v callback=%q", result, streamed.String())
			}
		})
	}
	stream := "data: {\"model\":\"test-secret-key\",\"choices\":[{\"delta\":{\"content\":\"before test-\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"secret-key after\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	var all strings.Builder
	result, err := fixture(stream, "text/event-stream", 200).Generate(context.Background(), request("chat"), func(event Event) error { all.WriteString(event.Text); return nil })
	if err != nil || result.Text != "before [redacted] after" || all.String() != result.Text {
		t.Fatalf("split credential echo: %q %q %v", result.Text, all.String(), err)
	}
	_, err = fixture(`{"data":[{"id":"test-secret-key"}]}`, "application/json", 200).ListModels(context.Background(), Config{BaseURL: "https://example.test", APIKey: key})
	if errorCode(err) != "credential_exposure" {
		t.Fatalf("credential echo model ID accepted: %v", err)
	}
}

func TestGenerationPOSTHasNoReplayBodyOrIdempotencyHeader(t *testing.T) {
	client := NewTestClient(doerFunc(func(req *http.Request) (*http.Response, error) {
		if req.GetBody != nil || req.Header.Get("Idempotency-Key") != "" || req.Header.Get("X-Idempotency-Key") != "" {
			t.Error("generation request permits replay")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))}, nil
	}))
	if _, err := client.Generate(context.Background(), request("chat"), nil); err != nil {
		t.Fatal(err)
	}
}

func TestPartiallyReceivedGenerationIsNeverResent(t *testing.T) {
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[]}`)
			return
		}
		posts.Add(1)
		var firstByte [1]byte
		_, _ = req.Body.Read(firstByte[:])
		connection, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = connection.Close()
	}))
	defer server.Close()
	client := NewTestClient(server.Client())
	cfg := Config{BaseURL: server.URL, APIKey: "test-secret-key", Protocol: "chat"}
	if _, err := client.ListModels(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	req := request("chat")
	req.Config = cfg
	req.Timeout = time.Second
	_, err := client.Generate(context.Background(), req, nil)
	if err == nil || posts.Load() != 1 {
		t.Fatalf("partial POST resent: count=%d err=%v", posts.Load(), err)
	}
}
