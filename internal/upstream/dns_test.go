package upstream

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"testing"
)

func TestDNSFallbackOnlyForSyntheticHostnameAnswers(t *testing.T) {
	for _, tc := range []struct {
		host     string
		ips      []string
		fallback bool
	}{
		{"api.example", []string{"198.18.0.9", "fdfe:dcba:9876::8"}, true},
		{"198.18.0.9", []string{"198.18.0.9"}, false},
		{"api.example", []string{"8.8.8.8"}, false},
		{"api.example", []string{"127.0.0.1"}, false},
		{"api.example", []string{"198.18.0.9", "10.0.0.1"}, false},
		{"api.example", []string{"198.18.0.9", "8.8.8.8"}, false},
	} {
		t.Run(tc.host+strings.Join(tc.ips, "-"), func(t *testing.T) {
			calls := 0
			resolve := withDNSFallback(func(context.Context, string) ([]netip.Addr, error) {
				var ips []netip.Addr
				for _, value := range tc.ips {
					ips = append(ips, netip.MustParseAddr(value))
				}
				return ips, nil
			}, func(context.Context, string) ([]netip.Addr, error) {
				calls++
				return []netip.Addr{netip.MustParseAddr("8.8.4.4")}, nil
			})
			_, err := resolve(context.Background(), tc.host)
			if err != nil || (calls == 1) != tc.fallback {
				t.Fatalf("fallback=%d err=%v", calls, err)
			}
		})
	}
}

func TestDNSJSONAnswersAndCredentialIsolation(t *testing.T) {
	resolver, _ := url.Parse("https://1.1.1.1/dns-query")
	for _, tc := range []struct{ name, body, code string }{
		{"valid", `{"Status":0,"Answer":[{"type":1,"data":"8.8.8.8"}]}`, ""},
		{"private", `{"Status":0,"Answer":[{"type":1,"data":"127.0.0.1"}]}`, "blocked_target"},
		{"mixed", `{"Status":0,"Answer":[{"type":1,"data":"8.8.8.8"},{"type":28,"data":"::1"}]}`, "blocked_target"},
		{"truncated", `{"Status":0,"TC":true}`, "network_error"},
		{"nxdomain", `{"Status":3}`, "network_error"},
		{"missing status", `{"Answer":[{"type":1,"data":"8.8.8.8"}]}`, "network_error"},
		{"too large", strings.Repeat(" ", 65537), "network_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := NewTestClient(doerFunc(func(req *http.Request) (*http.Response, error) {
				if req.Header.Get("Authorization") != "" || req.Header.Get("x-api-key") != "" || req.URL.Query().Get("name") != "model.example" || req.Header.Get("Accept") != "application/dns-json" {
					t.Fatal("incorrect resolver request")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			}))
			ips, err := lookupDNSJSON(context.Background(), client, resolver, "model.example")
			if tc.code == "" {
				if err != nil || len(ips) == 0 {
					t.Fatalf("%v %v", ips, err)
				}
			} else if errorCode(err) != tc.code {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestDNSFallbackPinsAndStillRejectsPrivateAnswers(t *testing.T) {
	for _, answer := range []string{"8.8.8.8", "127.0.0.1"} {
		client := New()
		calls := 0
		client.resolver = withDNSFallback(func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("198.18.0.9")}, nil
		}, func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr(answer)}, nil
		})
		client.doer = doerFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			approved, ok := req.Context().Value(validatedIPsKey{}).(validatedIPs)
			if !ok || len(approved.addrs) != 1 || approved.addrs[0].String() != answer {
				t.Fatal("resolved IP not pinned")
			}
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"data":[]}`))}, nil
		})
		_, err := client.ListModels(context.Background(), Config{BaseURL: "https://api.example"})
		if answer == "127.0.0.1" {
			if errorCode(err) != "blocked_target" || calls != 0 {
				t.Fatalf("private address dispatched: %v", err)
			}
		} else if err != nil || calls != 1 {
			t.Fatalf("%v calls=%d", err, calls)
		}
	}
}

func TestDNSResolverConfigRejectsUnsafeEndpoints(t *testing.T) {
	for _, endpoint := range []string{"http://1.1.1.1/dns-query", "https://127.0.0.1/dns-query", "https://user:password@resolver.example/dns-query", "https://resolver.example/dns-query?token=secret"} {
		if _, err := NewWithDNS(endpoint); err == nil {
			t.Fatalf("accepted %s", endpoint)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := withDNSFallback(func(context.Context, string) ([]netip.Addr, error) { return nil, context.Canceled }, func(context.Context, string) ([]netip.Addr, error) {
		t.Fatal("canceled lookup retried")
		return nil, nil
	})(ctx, "api.example")
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
