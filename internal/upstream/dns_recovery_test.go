package upstream

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDNSPrivateSiblingBlocksAndZeroTTLIsPreserved(t *testing.T) {
	resolver, _ := url.Parse("https://1.1.1.1/dns-query")
	for _, private := range []bool{false, true} {
		client := NewTestClient(doerFunc(func(req *http.Request) (*http.Response, error) {
			body := `{"Status":0,"Answer":[{"type":1,"data":"8.8.8.8","TTL":0}]}`
			if req.URL.Query().Get("type") == "AAAA" {
				address := "2001:4860:4860::8888"
				if private {
					address = "::1"
				}
				body = `{"Status":0,"Answer":[{"type":28,"data":"` + address + `","TTL":120}]}`
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
		}))
		ips, ttl, err := lookupDNSJSONResult(context.Background(), client, resolver, "model.example")
		if private {
			if errorCode(err) != "blocked_target" {
				t.Fatalf("private sibling accepted %v", err)
			}
		} else if err != nil || len(ips) != 2 || ttl != 0 {
			t.Fatalf("zero TTL lost: ttl=%v ips=%v err=%v", ttl, ips, err)
		}
	}
}

func TestDNSIPv6TransportFailureKeepsValidatedIPv4(t *testing.T) {
	resolver, _ := url.Parse("https://1.1.1.1/dns-query")
	client := NewTestClient(doerFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Query().Get("type") == "AAAA" {
			return nil, errors.New("temporary DNS connection failure")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"Status":0,"Answer":[{"type":1,"data":"8.8.8.8","TTL":60}]}`))}, nil
	}))
	ips, err := lookupDNSJSON(context.Background(), client, resolver, "model.example")
	if err != nil || len(ips) != 1 || ips[0].String() != "8.8.8.8" {
		t.Fatalf("lost valid IPv4 after IPv6 transport failure: ips=%v error=%v", ips, err)
	}
}

func TestDNSRecoversTemporaryDisconnectBeforeGeneration(t *testing.T) {
	resolver, _ := url.Parse("https://1.1.1.1/dns-query")
	var calls atomic.Int32
	client := NewTestClient(doerFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Query().Get("type") == "A" && calls.Add(1) == 1 {
			return nil, io.EOF
		}
		body := `{"Status":0,"Answer":[]}`
		if req.URL.Query().Get("type") == "A" {
			body = `{"Status":0,"Answer":[{"type":1,"data":"8.8.8.8","TTL":60}]}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	}))
	ips, err := lookupDNSJSON(context.Background(), client, resolver, "model.example")
	if err != nil || len(ips) != 1 || calls.Load() != 2 {
		t.Fatalf("DNS disconnect not recovered: ips=%v calls=%d err=%v", ips, calls.Load(), err)
	}
}
