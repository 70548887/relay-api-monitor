package upstream

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSharedDNSResolverKeepsFallbackPolicyAndCredentialIsolation(t *testing.T) {
	for _, tc := range []struct {
		name         string
		host         string
		ips          []string
		wantFallback bool
	}{
		{"fake hostname", "storage.example", []string{"198.18.0.1", "fdfe:dcba:9876::1"}, true},
		{"literal fake", "198.18.0.1", []string{"198.18.0.1"}, false},
		{"public", "storage.example", []string{"8.8.8.8"}, false},
		{"private", "storage.example", []string{"10.0.0.1"}, false},
		{"mixed fake private", "storage.example", []string{"198.18.0.1", "10.0.0.1"}, false},
		{"mixed fake public", "storage.example", []string{"198.18.0.1", "8.8.8.8"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			system := func(context.Context, string) ([]netip.Addr, error) {
				var out []netip.Addr
				for _, ip := range tc.ips {
					out = append(out, netip.MustParseAddr(ip))
				}
				return out, nil
			}
			dnsClient := NewTestClient(doerFunc(func(req *http.Request) (*http.Response, error) {
				calls.Add(1)
				if req.Header.Get("Authorization") != "" || req.Header.Get("Cookie") != "" || req.Header.Get("x-api-key") != "" || req.URL.User != nil || req.URL.Query().Get("name") != tc.host {
					t.Error("resolver received credentials or wrong query")
				}
				body := `{"Status":0,"Answer":[]}`
				if req.URL.Query().Get("type") == "A" {
					body = `{"Status":0,"Answer":[{"type":1,"data":"8.8.4.4","TTL":120}]}`
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
			}))
			resolve, err := newDNSResolver("https://1.1.1.1/dns-query", system, dnsClient)
			if err != nil {
				t.Fatal(err)
			}
			first, err := resolve(context.Background(), tc.host)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantFallback {
				if len(first) != 1 || first[0].String() != "8.8.4.4" || calls.Load() != 2 {
					t.Fatalf("fallback result=%v calls=%d", first, calls.Load())
				}
				first[0] = netip.MustParseAddr("127.0.0.1")
				cached, err := resolve(context.Background(), tc.host)
				if err != nil || cached[0].String() != "8.8.4.4" || calls.Load() != 2 {
					t.Fatalf("cache leaked mutation or repeated query: %v %v", cached, err)
				}
			} else if calls.Load() != 0 {
				t.Fatal("non-synthetic-only answer invoked fallback")
			}
		})
	}
}

func TestSharedDNSResolverRetainsErrorsAndConfigValidation(t *testing.T) {
	for _, endpoint := range []string{"http://1.1.1.1/dns-query", "https://127.0.0.1/dns-query", "https://user:secret@resolver.example/dns-query", "https://resolver.example/dns-query?key=secret"} {
		if _, err := NewDNSResolver(endpoint); err == nil {
			t.Fatal("unsafe shared resolver config accepted")
		}
	}
	failed := errors.New("system DNS failed")
	resolve, err := newDNSResolver("https://1.1.1.1/dns-query", func(context.Context, string) ([]netip.Addr, error) { return nil, failed }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = resolve(context.Background(), "storage.example"); !errors.Is(err, failed) {
		t.Fatalf("system error changed: %v", err)
	}
}
