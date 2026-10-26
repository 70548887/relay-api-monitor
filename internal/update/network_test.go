package update

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type updateRoundTripper func(*http.Request) (*http.Response, error)

func (f updateRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGitHubGETRetriesOnlyTransientTransportFailures(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		err                   error
		httpStatus, wantCalls int
	}{
		{"EOF recovers", io.EOF, 200, 2},
		{"unexpected EOF recovers", io.ErrUnexpectedEOF, 200, 2},
		{"permanent TLS error", errors.New("x509: certificate is not trusted"), 200, 1},
		{"HTTP forbidden", nil, 403, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, m := newFixture(t)
			calls := 0
			m.client.Transport = updateRoundTripper(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 && tc.err != nil {
					return nil, tc.err
				}
				if r.Method != http.MethodGet {
					t.Fatal("only GET expected")
				}
				return &http.Response{StatusCode: tc.httpStatus, Body: io.NopCloser(strings.NewReader("{}")), Header: http.Header{}}, nil
			})
			resp, _ := m.request(context.Background(), "https://api.github.com/repos/example/example/releases/latest", "application/json")
			if resp != nil {
				resp.Body.Close()
			}
			if calls != tc.wantCalls {
				t.Fatalf("calls=%d want=%d", calls, tc.wantCalls)
			}
		})
	}
}

func TestGitHubRetryLimitAndCancellation(t *testing.T) {
	_, m := newFixture(t)
	calls := 0
	m.client.Transport = updateRoundTripper(func(*http.Request) (*http.Response, error) { calls++; return nil, io.EOF })
	_, err := m.request(context.Background(), "https://api.github.com/repos/example/example/releases/latest", "application/json")
	if calls != 3 || classifyError(err) != "github_connection_closed" {
		t.Fatalf("calls=%d err=%v code=%s", calls, err, classifyError(err))
	}
	calls = 0
	ctx, cancel := context.WithCancel(context.Background())
	m.client.Transport = updateRoundTripper(func(*http.Request) (*http.Response, error) { calls++; cancel(); return nil, io.EOF })
	_, _ = m.request(ctx, "https://api.github.com/repos/example/example/releases/latest", "application/json")
	if calls != 1 {
		t.Fatalf("canceled request retried %d times", calls)
	}
}

func TestUpdateNetworkErrorCategories(t *testing.T) {
	for _, tc := range []struct{ message, code string }{
		{`Get "https://api.github.com": EOF`, "github_connection_closed"},
		{`Get "https://api.github.com": context deadline exceeded`, "timeout"},
		{`Get "https://api.github.com": proxyconnect tcp: connection refused`, "proxy_error"},
		{"update: GitHub returned HTTP 401", "github_http_error"},
	} {
		if got := classifyError(errors.New(tc.message)); got != tc.code {
			t.Fatalf("%s: got=%s want=%s", tc.message, got, tc.code)
		}
	}
}
