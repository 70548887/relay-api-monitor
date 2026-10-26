package upstream

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"relaylab/internal/domain"
)

func TestReasoningWireSnapshots(t *testing.T) {
	for _, tc := range []struct {
		name, protocol, effort, mode, response, path, want string
		budget                                             int
	}{
		{"chat high", "chat", "high", "", `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`, "/v1/chat/completions", `{"model":"requested","stream":true,"messages":[{"role":"user","content":"hello"}],"max_completion_tokens":4096,"stream_options":{"include_usage":true},"reasoning_effort":"high"}`, 0},
		{"responses xhigh", "responses", "xhigh", "", `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`, "/v1/responses", `{"model":"requested","stream":true,"input":"hello","max_output_tokens":4096,"reasoning":{"effort":"xhigh"}}`, 0},
		{"claude adaptive high", "anthropic", "high", "adaptive", `{"type":"message","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`, "/v1/messages", `{"model":"requested","stream":true,"messages":[{"role":"user","content":"hello"}],"max_tokens":4096,"output_config":{"effort":"high"},"thinking":{"type":"adaptive"}}`, 0},
		{"claude enabled max", "anthropic", "max", "enabled", `{"type":"message","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`, "/v1/messages", `{"model":"requested","stream":true,"messages":[{"role":"user","content":"hello"}],"max_tokens":4096,"output_config":{"effort":"max"},"thinking":{"type":"enabled","budget_tokens":1024}}`, 1024},
		{"default omitted", "chat", "default", "default", `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`, "/v1/chat/completions", `{"model":"requested","stream":true,"messages":[{"role":"user","content":"hello"}],"max_completion_tokens":4096,"stream_options":{"include_usage":true}}`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.path {
					t.Errorf("path %s", r.URL.Path)
				}
				if tc.protocol == "anthropic" && (r.Header.Get("anthropic-version") != "2023-06-01" || r.Header.Get("x-api-key") != "fixture-key") {
					t.Error("Anthropic header mismatch")
				}
				var got, want any
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Error(err)
				}
				_ = json.Unmarshal([]byte(tc.want), &want)
				if !reflect.DeepEqual(got, want) {
					t.Errorf("request snapshot\ngot %v\nwant %v", got, want)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.response)
			}))
			defer server.Close()
			var metadata Event
			result, err := NewTestClient(server.Client()).Generate(context.Background(), Request{Config: Config{BaseURL: server.URL, APIKey: "fixture-key", Protocol: tc.protocol}, Model: "requested", Prompt: "hello", MaxTokens: 4096, ReasoningEffort: tc.effort, ThinkingMode: tc.mode, ThinkingBudget: tc.budget}, func(e Event) error {
				if e.Type == "meta" {
					metadata = e
				}
				return nil
			})
			if err != nil || !result.Complete || result.Text != "ok" {
				t.Fatalf("result %+v error %v", result, err)
			}
			if result.ReasoningEffort != reasoningValue(tc.effort) || metadata.ReasoningEffort != result.ReasoningEffort || result.ThinkingMode != reasoningValue(tc.mode) || result.ThinkingBudget != tc.budget {
				t.Fatalf("actual parameter mismatch %+v %+v", result, metadata)
			}
		})
	}
}

func TestInvalidReasoningRejectedBeforeDispatch(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	for _, tc := range []struct {
		protocol, effort, mode string
		budget, max            int
	}{
		{"chat", "max", "", 0, 4096}, {"responses", "adaptive", "", 0, 4096}, {"chat", "high", "enabled", 1024, 4096},
		{"anthropic", "xhigh", "", 0, 4096}, {"anthropic", "high", "disabled", 0, 4096}, {"anthropic", "high", "enabled", 1023, 4096},
		{"anthropic", "high", "enabled", 4096, 4096}, {"anthropic", "high", "adaptive", 1024, 4096}, {"anthropic", "high", "", -1, 4096},
	} {
		_, err := NewTestClient(server.Client()).Generate(context.Background(), Request{Config: Config{BaseURL: server.URL, Protocol: tc.protocol}, Model: "m", Prompt: "p", MaxTokens: tc.max, ReasoningEffort: tc.effort, ThinkingMode: tc.mode, ThinkingBudget: tc.budget}, nil)
		if errorCode(err) != "invalid_reasoning" {
			t.Fatalf("case %+v error %v", tc, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid configuration dispatched a billable request")
	}
}

func TestResolveReasoningOverrides(t *testing.T) {
	channel := domain.Channel{ReasoningEffort: "high", ThinkingMode: "enabled", ThinkingBudget: 2048}
	for _, tc := range []struct {
		model        domain.Model
		effort, mode string
		budget       int
	}{
		{domain.Model{}, "high", "enabled", 2048},
		{domain.Model{ReasoningEffort: "default", ThinkingMode: "default"}, "", "", 0},
		{domain.Model{ReasoningEffort: "low", ThinkingMode: "adaptive"}, "low", "adaptive", 0},
		{domain.Model{ThinkingBudget: 1024}, "high", "enabled", 1024},
	} {
		effort, mode, budget := ResolveReasoning(tc.model, channel)
		if effort != tc.effort || mode != tc.mode || budget != tc.budget {
			t.Fatalf("got %s %s %d want %+v", effort, mode, budget, tc)
		}
	}
}

func TestVerifyGroupSingleTokenBindingRequest(t *testing.T) {
	for _, tc := range []struct {
		name, provider, path, field, body, status, group string
		httpStatus, calls                                int
	}{
		{"none", "none", "", "", "", "unverified", "", 200, 0},
		{"newapi", "newapi", "/ignored", "ignored", `{"data":{"group":"high","key":"fixture-key","accounts":["private"]}}`, "verified", "high", 200, 1},
		{"auto token binding", "newapi", "", "", `{"data":{"group":"auto"}}`, "verified", "auto", 200, 1},
		{"custom", "custom", "/metadata/token", "binding.group", `{"binding":{"group":"dedicated"}}`, "verified", "dedicated", 200, 1},
		{"missing", "newapi", "", "", `{"data":{"user_group":"private"}}`, "unsupported", "", 200, 1},
		{"404", "newapi", "", "", `{"message":"fixture-key"}`, "unsupported", "", 404, 1},
		{"405", "newapi", "", "", `{}`, "unsupported", "", 405, 1},
		{"501", "newapi", "", "", `{}`, "unsupported", "", 501, 1},
		{"401", "newapi", "", "", `{"message":"fixture-key"}`, "failed", "", 401, 1},
		{"403", "newapi", "", "", `{}`, "failed", "", 403, 1},
		{"secret group", "newapi", "", "", `{"data":{"group":"fixture-key"}}`, "failed", "", 200, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				wantPath := "/api/token/self"
				if tc.provider == "custom" {
					wantPath = tc.path
				}
				if r.Method != "GET" || r.URL.Path != wantPath || r.Header.Get("Authorization") != "Bearer fixture-key" {
					t.Errorf("unexpected metadata request %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(tc.httpStatus)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			v, err := NewTestClient(server.Client()).VerifyGroup(context.Background(), Config{BaseURL: server.URL + "/v1", APIKey: "fixture-key"}, tc.provider, tc.path, tc.field)
			if err != nil || v.Status != tc.status || v.Group != tc.group || v.CheckedAt.IsZero() || calls != tc.calls {
				t.Fatalf("verification %+v error %v calls %d", v, err, calls)
			}
			encoded, _ := json.Marshal(v)
			for _, secret := range []string{"fixture-key", "accounts", "user_group"} {
				if strings.Contains(string(encoded), secret) {
					t.Errorf("metadata leak %s", secret)
				}
			}
		})
	}
}

func TestGroupVerifierRejectsAlternateOriginsWithoutEgress(t *testing.T) {
	calls := 0
	client := NewTestClient(doerFunc(func(*http.Request) (*http.Response, error) {
		calls++
		t.Fatal("invalid endpoint dispatched")
		return nil, nil
	}))
	for _, path := range []string{"https://alternate.example/self", "//alternate.example/self", "/meta?key=secret", "/meta#token", ""} {
		_, err := client.VerifyGroup(context.Background(), Config{BaseURL: "https://example.test"}, "custom", path, "data.group")
		if err == nil {
			t.Fatalf("path accepted %q", path)
		}
	}
	_, err := client.VerifyGroup(context.Background(), Config{BaseURL: "https://api.zxcbug.com"}, "newapi", "", "")
	if errorCode(err) != "protected_target" || calls != 0 {
		t.Fatalf("protected target error %v calls %d", err, calls)
	}
}
