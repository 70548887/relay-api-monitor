package fingerprint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"relaylab/internal/domain"
	"relaylab/internal/upstream"
)

// upstreamFunc plays the upstream of a test client.
type upstreamFunc func(*http.Request) (*http.Response, error)

func (f upstreamFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

func reply(status int, contentType string, body io.ReadCloser) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{contentType}}, Body: body}
}

// chatDelta is one event of a chat stream that adds text to the reply.
func chatDelta(text string) string {
	chunk, _ := json.Marshal(map[string]any{"model": "returned-model", "choices": []any{map[string]any{"delta": map[string]any{"content": text}}}})
	return fmt.Sprintf("data: %s\n\n", chunk)
}

// countingStream is a chat stream that adds one number to the reply with every read. It ends, without the events
// that complete a stream, after limit numbers.
type countingStream struct {
	limit, sent int
	closed      bool
}

func (s *countingStream) Read(p []byte) (int, error) {
	if s.sent >= s.limit {
		return 0, io.EOF
	}
	s.sent++
	return copy(p, chatDelta(strconv.Itoa(s.sent)+", ")), nil
}

func (s *countingStream) Close() error { s.closed = true; return nil }

// counted is the reply of a countingStream after count numbers.
func counted(count int) string {
	parts := make([]string, count)
	for i := range parts {
		parts[i] = strconv.Itoa(i + 1)
	}
	return strings.Join(parts, ", ")
}

func TestAskReadsTheReplyToOneQuestion(t *testing.T) {
	challenge := domain.FingerprintChallenge{ID: "q1", Prompt: "write 300 numbers", ExpectedCount: 300}
	request := upstream.Request{Config: upstream.Config{BaseURL: "https://upstream.example/v1", APIKey: "sk-ask-test-key", Protocol: "chat"}, Model: "asked-model", MaxTokens: 2048}
	ask := func(t *testing.T, onEvent func(upstream.Event) error, respond func(*http.Request) *http.Response) (upstream.Result, Answer, error) {
		t.Helper()
		client := upstream.NewTestClient(upstreamFunc(func(r *http.Request) (*http.Response, error) { return respond(r), nil }))
		return Ask(context.Background(), client, request, challenge, onEvent)
	}

	t.Run("a complete reply", func(t *testing.T) {
		var sent struct {
			Model    string `json:"model"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		var events []string
		response, answer, err := ask(t, func(event upstream.Event) error { events = append(events, event.Type); return nil }, func(r *http.Request) *http.Response {
			if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
				t.Error(err)
			}
			body, _ := json.Marshal(map[string]any{"model": "returned-model", "choices": []any{map[string]any{"message": map[string]any{"content": numbers(300)}, "finish_reason": "stop"}}})
			return reply(200, "application/json", io.NopCloser(strings.NewReader(string(body))))
		})
		if err != nil || answer != (Answer{Text: numbers(300), ExpectedCount: 300}) {
			t.Fatalf("answer %+v, error %v; want the whole reply", answer, err)
		}
		if sent.Model != "asked-model" || len(sent.Messages) != 1 || sent.Messages[0].Content != challenge.Prompt {
			t.Fatalf("sent %+v, want the prompt of the question", sent)
		}
		if !response.Complete || response.ReturnedModel != "returned-model" || !slices.Contains(events, "text") {
			t.Fatalf("response %+v, events %v; want the response of the request and its events passed on", response, events)
		}
	})

	t.Run("a reply with more numbers than asked", func(t *testing.T) {
		stream := &countingStream{limit: 5000}
		_, answer, err := ask(t, nil, func(*http.Request) *http.Response { return reply(200, "text/event-stream", stream) })
		if err != nil || answer != (Answer{Text: counted(300), ExpectedCount: 300}) {
			t.Fatalf("answer of %d numbers, error %v; want the 300 numbers asked for and no failure", strings.Count(answer.Text, ",")+1, err)
		}
		if stream.sent > 320 || !stream.closed {
			t.Fatalf("read %d numbers, closed %v; want the request stopped right after the 300 asked for", stream.sent, stream.closed)
		}
	})

	t.Run("a reply cut short", func(t *testing.T) {
		response, answer, err := ask(t, nil, func(*http.Request) *http.Response { return reply(200, "text/event-stream", &countingStream{limit: 40}) })
		var failure *upstream.Error
		if !errors.As(err, &failure) || failure.Code != "incomplete_stream" || response.Complete {
			t.Fatalf("error %v, complete %v; want the failure of the request", err, response.Complete)
		}
		if want := counted(40) + ", "; answer != (Answer{Text: want, ExpectedCount: 300}) {
			t.Fatalf("answer %+v, want the part of the reply that arrived", answer)
		}
	})

	t.Run("a refused request", func(t *testing.T) {
		_, answer, err := ask(t, nil, func(*http.Request) *http.Response {
			return reply(403, "text/plain", io.NopCloser(strings.NewReader("blocked")))
		})
		var failure *upstream.Error
		if !errors.As(err, &failure) || failure.Code != "authentication_error" || answer != (Answer{ExpectedCount: 300}) {
			t.Fatalf("answer %+v, error %v; want an empty answer and the failure of the request", answer, err)
		}
	})

	t.Run("a listener that stops the request", func(t *testing.T) {
		stop := errors.New("stop")
		stream := &countingStream{limit: 5000}
		_, answer, err := ask(t, func(event upstream.Event) error {
			if event.Type == "text" && stream.sent >= 3 {
				return stop
			}
			return nil
		}, func(*http.Request) *http.Response { return reply(200, "text/event-stream", stream) })
		if !errors.Is(err, stop) || stream.sent > 50 {
			t.Fatalf("error %v after %d numbers, want the request stopped with the error of the listener", err, stream.sent)
		}
		if !strings.HasPrefix(counted(50), strings.TrimSuffix(answer.Text, ", ")) || answer.Text == "" {
			t.Fatalf("answer %q, want the numbers that arrived before the stop", answer.Text)
		}
	})
}
