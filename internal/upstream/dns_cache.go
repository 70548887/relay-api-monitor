package upstream

import (
	"context"
	"net/netip"
	"sync"
	"time"
)

type dnsCacheEntry struct {
	ips     []netip.Addr
	expires time.Time
}
type dnsCache struct {
	mu      sync.Mutex
	entries map[string]dnsCacheEntry
	lookup  func(context.Context, string) ([]netip.Addr, time.Duration, error)
	now     func() time.Time
}

func newDNSCache(lookup func(context.Context, string) ([]netip.Addr, time.Duration, error)) *dnsCache {
	return &dnsCache{entries: map[string]dnsCacheEntry{}, lookup: lookup, now: time.Now}
}

func (c *dnsCache) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if ctx.Err() != nil {
		return nil, normalizeTransport(ctx, ctx.Err())
	}
	c.mu.Lock()
	if entry, ok := c.entries[host]; ok && entry.expires.After(c.now()) {
		ips := append([]netip.Addr(nil), entry.ips...)
		c.mu.Unlock()
		return ips, nil
	}
	delete(c.entries, host)
	c.mu.Unlock()
	ips, ttl, err := c.lookup(ctx, host)
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, normalizeTransport(ctx, ctx.Err())
	}
	for _, ip := range ips {
		if !publicIP(ip) {
			return nil, &Error{Code: "blocked_target", Message: "加密 DNS 返回了内网或保留地址"}
		}
	}
	if ttl > 5*time.Minute {
		ttl = 5 * time.Minute
	}
	if ttl > 0 && len(ips) > 0 {
		c.mu.Lock()
		if len(c.entries) >= 256 {
			var oldest string
			var expiry time.Time
			for name, entry := range c.entries {
				if oldest == "" || entry.expires.Before(expiry) {
					oldest = name
					expiry = entry.expires
				}
			}
			delete(c.entries, oldest)
		}
		c.entries[host] = dnsCacheEntry{ips: append([]netip.Addr(nil), ips...), expires: c.now().Add(ttl)}
		c.mu.Unlock()
	}
	return ips, nil
}
