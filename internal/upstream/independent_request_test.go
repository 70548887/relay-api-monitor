package upstream

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestConsecutiveGenerationsNeverCarryEarlierContext(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			calls := 0
			client := NewTestClient(doerFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				raw, err := io.ReadAll(req.Body)
				if err != nil {
					t.Fatal(err)
				}
				var body map[string]json.RawMessage
				if json.Unmarshal(raw, &body) != nil {
					t.Fatal("invalid JSON")
				}
				for _, field := range []string{"previous_response_id", "conversation", "conversation_id", "session_id", "history", "cache_control", "prompt_cache_key"} {
					if _, ok := body[field]; ok {
						t.Fatalf("unexpected context field %s", field)
					}
				}
				prompt := "first prompt only"
				reply := "FIRST_REPLY_UNIQUE_MARKER"
				if calls == 2 {
					prompt = "second prompt only"
					reply = "SECOND_REPLY_UNIQUE_MARKER"
					if strings.Contains(string(raw), "first prompt only") || strings.Contains(string(raw), "FIRST_REPLY_UNIQUE_MARKER") {
						t.Fatal("history leaked into second request")
					}
				}
				if protocol == "responses" {
					var input string
					if json.Unmarshal(body["input"], &input) != nil || input != prompt {
						t.Fatal("unexpected input")
					}
				} else {
					var messages []struct{ Role, Content string }
					if json.Unmarshal(body["messages"], &messages) != nil || len(messages) != 1 || messages[0].Role != "user" || messages[0].Content != prompt {
						t.Fatal("unexpected message history")
					}
				}
				var result string
				switch protocol {
				case "chat":
					result = `{"choices":[{"message":{"content":"` + reply + `"},"finish_reason":"stop"}]}`
				case "responses":
					result = `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"` + reply + `"}]}]}`
				case "anthropic":
					result = `{"type":"message","content":[{"type":"text","text":"` + reply + `"}],"stop_reason":"end_turn"}`
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(result))}, nil
			}))
			for i, prompt := range []string{"first prompt only", "second prompt only"} {
				result, err := client.Generate(context.Background(), Request{Config: Config{BaseURL: "https://model.example", Protocol: protocol}, Model: "model", Prompt: prompt}, nil)
				expected := "FIRST_REPLY_UNIQUE_MARKER"
				if i == 1 {
					expected = "SECOND_REPLY_UNIQUE_MARKER"
				}
				if err != nil || result.Text != expected {
					t.Fatalf("request %d err=%v result=%s", i, err, result.Text)
				}
			}
			if calls != 2 {
				t.Fatalf("expected two fresh network requests, got %d", calls)
			}
		})
	}
}

func TestUsageReportsOnlyUpstreamCacheEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, usage     string
		cached, created *int64
	}{
		{"chat", `{"prompt_tokens":20,"prompt_tokens_details":{"cached_tokens":12}}`, usageNumber(12), nil},
		{"responses", `{"input_tokens":20,"input_tokens_details":{"cached_tokens":0}}`, usageNumber(0), nil},
		{"anthropic", `{"input_tokens":5,"cache_read_input_tokens":12,"cache_creation_input_tokens":3}`, usageNumber(12), usageNumber(3)},
		{"missing", `{"input_tokens":20}`, nil, nil},
		{"invalid", `{"cache_read_input_tokens":-1}`, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var wire usageWire
			if err := json.Unmarshal([]byte(tc.usage), &wire); err != nil {
				t.Fatal(err)
			}
			result := Result{}
			var emitted *domainUsageSnapshot
			state := generationState{result: &result, callback: func(event Event) error {
				if event.Usage != nil {
					emitted = &domainUsageSnapshot{event.Usage.CachedInputTokens, event.Usage.CacheCreationInputTokens}
				}
				return nil
			}}
			if err := state.usage(&wire); err != nil {
				t.Fatal(err)
			}
			if !sameUsageNumber(result.Usage.CachedInputTokens, tc.cached) || !sameUsageNumber(result.Usage.CacheCreationInputTokens, tc.created) {
				t.Fatalf("cache evidence mismatch %+v", result.Usage)
			}
			if tc.cached != nil && (emitted == nil || !sameUsageNumber(emitted.cached, tc.cached)) {
				t.Fatal("streamed usage omitted cache evidence")
			}
		})
	}
}

type domainUsageSnapshot struct{ cached, created *int64 }

func usageNumber(v int64) *int64 { return &v }
func sameUsageNumber(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
