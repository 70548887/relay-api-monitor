package upstream

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestLargeSSEEnvelopeWithSmallOutputCompletesExactlyOnce(t *testing.T) {
	body := strings.Repeat(": ping\n\n", (2<<20)/8+3) + `event: response.output_text.delta
 data: {"type":"response.output_text.delta","delta":"fresh output"}

 event: response.completed
 data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7},"output":[{"type":"message","content":[{"type":"output_text","text":"fresh output"}]}]}}

 `
	body = strings.ReplaceAll(body, "\n ", "\n")
	var output strings.Builder
	result, err := fixture(body, "text/event-stream", 200).Generate(context.Background(), request("responses"), func(e Event) error {
		if e.Type == "text" {
			output.WriteString(e.Text)
		}
		return nil
	})
	if err != nil || !result.Complete || result.Text != "fresh output" || output.String() != result.Text {
		t.Fatalf("result=%+v callback=%q err=%v", result, output.String(), err)
	}
	if result.ReceivedBytes <= 2<<20 || result.TextBytes != 12 || result.Usage.TotalTokens == nil || *result.Usage.TotalTokens != 7 {
		t.Fatalf("metrics %+v", result)
	}
}
func TestSeparateGenerationLimits(t *testing.T) {
	for _, tc := range []struct{ name, body, code string }{
		{"line", "data: " + strings.Repeat("x", maxEventBytes) + "\n\n", "response_event_too_large"},
		{"event", strings.Repeat("data: "+strings.Repeat("x", 256<<10)+"\n", 17) + "\n", "response_event_too_large"},
		{"transport", strings.Repeat(": ping\n\n", maxStreamBytes/8+1), "response_stream_too_large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := readSSE(strings.NewReader(tc.body), func(string, string) error { return nil })
			if errorCode(err) != tc.code {
				t.Fatalf("%v", err)
			}
		})
	}
	if _, err := readGenerationJSON(strings.NewReader(strings.Repeat("x", maxGenerationJSONBytes+1))); errorCode(err) != "response_json_too_large" {
		t.Fatal(err)
	}
	if _, err := readLimited(strings.NewReader(strings.Repeat("x", maxResponseBytes+1))); errorCode(err) != "response_too_large" {
		t.Fatal("metadata limit changed", err)
	}
}
func TestTextLimitPreservesValidUTF8PartialOutput(t *testing.T) {
	text := strings.Repeat("界", maxTextBytes/3+100)
	body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": text}, "finish_reason": "stop"}}})
	result, err := fixture(string(body), "application/json", 200).Generate(context.Background(), request("chat"), nil)
	if errorCode(err) != "response_text_too_large" || result.Complete || len(result.Text) > maxTextBytes || len(result.Text) < maxTextBytes-3 || !utf8.ValidString(result.Text) {
		t.Fatalf("code=%v bytes=%d complete=%v", err, len(result.Text), result.Complete)
	}
	if result.TextBytes != int64(len(result.Text)) || result.ReceivedBytes != int64(len(body)) || result.FirstTextMS == nil {
		t.Fatal("partial metrics missing")
	}
}
func TestLargeStreamWithoutTerminalNeverBecomesSuccess(t *testing.T) {
	body := strings.Repeat(": ping\n\n", (2<<20)/8+2) + "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"
	result, err := fixture(body, "text/event-stream", 200).Generate(context.Background(), request("responses"), nil)
	if err == nil || result.Complete || result.Text != "partial" || result.ReceivedBytes <= 2<<20 {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestHTTPFailureRetainsObservedStatusAndBytes(t *testing.T) {
	body := `{"error":{"message":"upstream temporarily unavailable"}}`
	result, err := fixture(body, "application/json", 503).Generate(context.Background(), request("chat"), nil)
	if err == nil || result.Complete || result.HTTPStatus != 503 || result.ReceivedBytes != int64(len(body)) || result.TextBytes != 0 {
		t.Fatalf("failure metrics %+v %v", result, err)
	}
}
