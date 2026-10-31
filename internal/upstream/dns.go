package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

var fakeIPv4 = netip.MustParsePrefix("198.18.0.0/15")
var fakeIPv6 = netip.MustParsePrefix("fdfe:dcba:9876::/48")

func fakeIP(ip netip.Addr) bool {
	return fakeIPv4.Contains(ip.Unmap()) || fakeIPv6.Contains(ip)
}

// IsSyntheticDNSAddress identifies proxy Fake-IP ranges without exempting them
// from the caller's public-address checks.
func IsSyntheticDNSAddress(ip netip.Addr) bool { return fakeIP(ip) }

// DNSResolver resolves addresses only. Callers must validate every returned
// address and pin the approved addresses when establishing a connection.
type DNSResolver func(context.Context, string) ([]netip.Addr, error)

// NewDNSResolver shares the explicit HTTPS DNS-JSON fallback used by model
// requests. Only an all-synthetic hostname answer invokes the fallback; literal
// IPs, DNS errors and mixed public/private answers never gain an exemption.
func NewDNSResolver(endpoint string) (DNSResolver, error) {
	return newDNSResolver(endpoint, lookupIPs, New())
}

func newDNSResolver(endpoint string, system DNSResolver, dnsClient *Client) (DNSResolver, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return system, nil
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, errors.New("upstream: dns_over_https must be an HTTPS URL without credentials, query or fragment")
	}
	if err = ValidatePublicURL(endpoint, "", ""); err != nil {
		return nil, errors.New("upstream: dns_over_https must use a public, permitted resolver address")
	}
	cache := newDNSCache(func(ctx context.Context, host string) ([]netip.Addr, time.Duration, error) {
		return lookupDNSJSONResult(ctx, dnsClient, u, host)
	})
	return withDNSFallback(system, cache.resolve), nil
}

// NewWithDNS optionally replaces synthetic proxy DNS answers with an explicit
// administrator-configured DNS-over-HTTPS JSON resolver. Its answers still pass
// the normal public-IP validation and are pinned by the production dialer.
func NewWithDNS(endpoint string) (*Client, error) {
	c := New()
	resolver, err := NewDNSResolver(endpoint)
	if err != nil {
		return nil, err
	}
	c.resolver = resolver
	return c, nil
}

func withDNSFallback(system, fallback func(context.Context, string) ([]netip.Addr, error)) func(context.Context, string) ([]netip.Addr, error) {
	return func(ctx context.Context, host string) ([]netip.Addr, error) {
		ips, err := system(ctx, host)
		// A literal IP or a mixed public/private result must never gain an
		// exemption by going through a different resolver.
		if _, parseErr := netip.ParseAddr(host); parseErr == nil || err != nil || len(ips) == 0 {
			return ips, err
		}
		for _, ip := range ips {
			if !fakeIP(ip) {
				return ips, nil
			}
		}
		return fallback(ctx, host)
	}
}

func lookupDNSJSON(ctx context.Context, client *Client, resolver *url.URL, host string) ([]netip.Addr, error) {
	ips, _, err := lookupDNSJSONResult(ctx, client, resolver, host)
	return ips, err
}

type dnsFamilyResult struct {
	ips       []netip.Addr
	ttl       time.Duration
	err       error
	transient bool
}

func lookupDNSJSONResult(parent context.Context, client *Client, resolver *url.URL, host string) ([]netip.Addr, time.Duration, error) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	results := make(chan dnsFamilyResult, 2)
	for _, typ := range []string{"A", "AAAA"} {
		go func(typ string) { results <- queryDNSFamily(ctx, client, resolver, host, typ) }(typ)
	}
	addresses := make([]netip.Addr, 0, 4)
	var ttl time.Duration
	haveTTL := false
	var transientErr error
	partial := false
	for i := 0; i < 2; i++ {
		r := <-results
		if r.err != nil {
			if !r.transient {
				return nil, 0, r.err
			}
			partial = true
			transientErr = r.err
			continue
		}
		addresses = append(addresses, r.ips...)
		if len(r.ips) > 0 && (!haveTTL || r.ttl < ttl) {
			ttl = r.ttl
			haveTTL = true
		}
	}
	if parent.Err() != nil {
		return nil, 0, normalizeTransport(parent, parent.Err())
	}
	if len(addresses) == 0 {
		if transientErr != nil {
			return nil, 0, transientErr
		}
		return nil, 0, dnsFailure()
	}
	// A family that failed transport is never treated as a negative DNS
	// answer. Its validated sibling can be used, but the partial result is
	// not cached and private/invalid answers above always fail closed.
	if partial {
		ttl = 0
	}
	return addresses, ttl, nil
}

func queryDNSFamily(ctx context.Context, client *Client, resolver *url.URL, host, typ string) dnsFamilyResult {
	var result dnsFamilyResult
	for attempt := 0; attempt < 3; attempt++ {
		if ctx.Err() != nil {
			return dnsFamilyResult{err: normalizeTransport(ctx, ctx.Err()), transient: true}
		}
		attemptCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		result = queryDNSOnce(attemptCtx, client, resolver, host, typ)
		cancel()
		if result.err == nil || !result.transient || attempt == 2 {
			return result
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return dnsFamilyResult{err: normalizeTransport(ctx, ctx.Err()), transient: true}
		case <-timer.C:
		}
	}
	return result
}

func queryDNSOnce(ctx context.Context, client *Client, resolver *url.URL, host, typ string) dnsFamilyResult {
	u := *resolver
	q := u.Query()
	q.Set("name", host)
	q.Set("type", typ)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return dnsFamilyResult{err: dnsFailure()}
	}
	req.Header.Set("Accept", "application/dns-json")
	resp, err := client.do(ctx, req)
	if err != nil {
		var typed *Error
		if errors.As(err, &typed) && (typed.Code == "blocked_target" || typed.Code == "protected_target") {
			return dnsFamilyResult{err: typed}
		}
		return dnsFamilyResult{err: dnsFailure(), transient: true}
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, (64<<10)+1))
	resp.Body.Close()
	if readErr != nil || resp.StatusCode == 429 || resp.StatusCode >= 500 {
		return dnsFamilyResult{err: dnsFailure(), transient: true}
	}
	var answer struct {
		Status *int
		TC     bool
		Answer []struct {
			Type int
			Data string
			TTL  uint32
		}
	}
	if resp.StatusCode != 200 || len(body) > 64<<10 || json.Unmarshal(body, &answer) != nil || answer.Status == nil || answer.TC || len(answer.Answer) > 128 {
		return dnsFamilyResult{err: dnsFailure()}
	}
	if *answer.Status != 0 {
		return dnsFamilyResult{err: dnsFailure(), transient: *answer.Status == 2}
	}
	result := dnsFamilyResult{}
	first := true
	for _, item := range answer.Answer {
		if item.Type != 1 && item.Type != 28 {
			continue
		}
		ip, err := netip.ParseAddr(item.Data)
		if err != nil || !publicIP(ip) {
			return dnsFamilyResult{err: &Error{Code: "blocked_target", Message: "加密 DNS 返回了内网或保留地址，请核对 API 域名和 DNS 配置"}}
		}
		if (typ == "A" && item.Type != 1) || (typ == "AAAA" && item.Type != 28) {
			continue
		}
		result.ips = append(result.ips, ip.Unmap())
		ttl := time.Duration(item.TTL) * time.Second
		if first || ttl < result.ttl {
			result.ttl = ttl
			first = false
		}
	}
	return result
}

func dnsFailure() error {
	return &Error{Code: "network_error", Message: "加密 DNS 解析未完成；已对临时断线进行有限重试，请检查 upstream.dns_over_https 和本机网络，或在代理中为 API 域名关闭 Fake-IP"}
}
