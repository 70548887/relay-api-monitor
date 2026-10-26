package upstream

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestIDNANormalizationBlocksProtectedNamesBeforeDNS(t *testing.T) {
	client := New()
	client.resolver = func(context.Context, string) ([]netip.Addr, error) {
		t.Fatal("protected hostname reached DNS")
		return nil, nil
	}
	client.doer = doerFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("protected hostname reached HTTP")
		return nil, nil
	})
	for _, base := range []string{
		"https://api.zxcbug.com", "https://x.api.zxcbug.com", "https://API.ZXCBUG.COM.",
		"https://ａｐｉ.zxcbug.com", "https://api。zxcbug.com", "https://api．zxcbug.com", "https://x｡ａｐｉ.zxcbug.com",
	} {
		if _, err := client.ListModels(context.Background(), Config{BaseURL: base}); errorCode(err) != "protected_target" {
			t.Fatalf("protected URL was not rejected locally: %q: %v", base, err)
		}
		req := request("chat")
		req.Config.BaseURL = base
		if _, err := client.Generate(context.Background(), req, nil); errorCode(err) != "protected_target" {
			t.Fatalf("protected generation URL was not rejected locally: %q: %v", base, err)
		}
	}
	u, err := endpoint(Config{BaseURL: "https://bücher.example/v1"}, "", "/models")
	if err != nil || u.Hostname() != "xn--bcher-kva.example" {
		t.Fatalf("IDNA URL not canonicalized: %v %v", u, err)
	}
}

func TestPublicURLValidationRejectsReservedAddressesAndInvalidPorts(t *testing.T) {
	for _, base := range []string{"http://[::ffff:127.0.0.1]", "http://[64:ff9b::7f00:1]", "http://127.0.0.1", "https://example.test:0", "https://example.test:65536", "https://example..test", "https://[invalid-bracket-host]", "https://.example.test"} {
		if err := ValidatePublicURL(base, "", ""); err == nil {
			t.Fatalf("invalid URL accepted: %q", base)
		}
	}
	if err := ValidatePublicURL("https://example.test", "", "/"+strings.Repeat("x", 4096)); err == nil {
		t.Fatal("oversized endpoint override accepted")
	}
}

func TestDNSRebindingIsRejectedEvenWithIdleConnection(t *testing.T) {
	var requests atomic.Int32
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	defer fixture.Close()
	client := New()
	transport := client.doer.(*http.Client).Transport.(*http.Transport)
	defer transport.CloseIdleConnections()
	var dials atomic.Int32
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		approved, ok := ctx.Value(validatedIPsKey{}).(validatedIPs)
		if !ok || approved.hostname != "rebind.example" || len(approved.addrs) != 1 || approved.addrs[0].String() != "8.8.8.8" {
			t.Error("request did not carry its approved DNS snapshot")
		}
		dials.Add(1)
		return (&net.Dialer{}).DialContext(ctx, network, fixture.Listener.Addr().String())
	}
	resolved := 0
	client.resolver = func(context.Context, string) ([]netip.Addr, error) {
		resolved++
		if resolved == 1 {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	cfg := Config{BaseURL: "http://rebind.example", APIKey: "fixture-key"}
	if _, err := client.ListModels(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListModels(context.Background(), cfg); errorCode(err) != "blocked_target" {
		t.Fatalf("rebound request accepted: %v", err)
	}
	if resolved != 2 || requests.Load() != 1 || dials.Load() != 1 {
		t.Fatalf("unexpected rebinding behavior: resolutions=%d requests=%d dials=%d", resolved, requests.Load(), dials.Load())
	}
}

func TestResponsesCompletionRequiresMatchingObjectStatus(t *testing.T) {
	for _, stream := range []string{
		"data: {\"type\":\"response.created\",\"response\":{\"status\":\"completed\"}}\n\ndata: {\"type\":\"response.completed\",\"response\":{}}\n\n",
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"in_progress\"}}\n\n",
		"data: {\"type\":\"response.completed\",\"response\":null}\n\n",
	} {
		result, err := fixture(stream, "text/event-stream", 200).Generate(context.Background(), request("responses"), nil)
		if errorCode(err) != "invalid_response" || result.Complete {
			t.Fatalf("inconsistent Responses terminal accepted: %+v error=%v", result, err)
		}
	}
}

func TestCRTerminalDoesNotWaitForConnectionClose(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	written := make(chan error, 1)
	go func() {
		_, err := io.WriteString(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\r\rdata: [DONE]\r\r")
		written <- err
	}()
	client := NewTestClient(doerFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: reader}, nil
	}))
	finished := make(chan error, 1)
	go func() {
		result, err := client.Generate(context.Background(), request("chat"), nil)
		if err == nil && (!result.Complete || result.Text != "ok") {
			err = invalidResponse("incorrect CR-terminated result")
		}
		finished <- err
	}()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal CR event waited for a later byte or connection close")
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
}

func TestPartialOutputRemainsAvailableAfterErrors(t *testing.T) {
	for _, tc := range []struct{ body, kind, code string }{
		{`{"choices":[{"message":{"content":"partial"},"finish_reason":"length"}]}`, "application/json", "truncated"},
		{"data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n", "text/event-stream", "incomplete_stream"},
	} {
		var delivered strings.Builder
		result, err := fixture(tc.body, tc.kind, 200).Generate(context.Background(), request("chat"), func(event Event) error {
			delivered.WriteString(event.Text)
			return nil
		})
		if errorCode(err) != tc.code || result.Text != "partial" || delivered.String() != "partial" || result.Complete {
			t.Fatalf("partial output lost: %+v delivered=%q error=%v", result, delivered.String(), err)
		}
	}
	callbackErr := errors.New("caller canceled output")
	result, err := fixture(`{"choices":[{"message":{"content":"partial"},"finish_reason":"stop"}]}`, "application/json", 200).Generate(context.Background(), request("chat"), func(event Event) error {
		if event.Type == "text" {
			return callbackErr
		}
		return nil
	})
	if !errors.Is(err, callbackErr) || result.Text != "partial" || result.Complete {
		t.Fatalf("callback failure lost partial result: %+v %v", result, err)
	}
}
