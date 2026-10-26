package upstream

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"
)

func TestDNSCacheHonorsTTLAndDoesNotExposeMutableEntries(t *testing.T) {
	now := time.Now()
	calls := 0
	cache := newDNSCache(func(context.Context, string) ([]netip.Addr, time.Duration, error) {
		calls++
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, time.Minute, nil
	})
	cache.now = func() time.Time { return now }
	ips, err := cache.resolve(context.Background(), "model.example")
	if err != nil {
		t.Fatal(err)
	}
	ips[0] = netip.MustParseAddr("127.0.0.1")
	ips, err = cache.resolve(context.Background(), "model.example")
	if err != nil || calls != 1 || ips[0].String() != "8.8.8.8" {
		t.Fatalf("mutable cache/calls=%d ips=%v err=%v", calls, ips, err)
	}
	now = now.Add(time.Minute)
	if _, err = cache.resolve(context.Background(), "model.example"); err != nil || calls != 2 {
		t.Fatal("expired result reused", err, calls)
	}
}

func TestDNSCacheDoesNotCacheFailuresPartialOrPrivateResults(t *testing.T) {
	for _, kind := range []string{"failure", "partial", "private"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			cache := newDNSCache(func(context.Context, string) ([]netip.Addr, time.Duration, error) {
				calls++
				if kind == "failure" {
					return nil, 0, errors.New("lookup failure")
				}
				if kind == "private" {
					return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, time.Minute, nil
				}
				return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, 0, nil
			})
			_, _ = cache.resolve(context.Background(), "model.example")
			_, _ = cache.resolve(context.Background(), "model.example")
			if calls != 2 {
				t.Fatalf("%s was cached", kind)
			}
		})
	}
}

func TestDNSCacheBoundsAndCancellation(t *testing.T) {
	cache := newDNSCache(func(context.Context, string) ([]netip.Addr, time.Duration, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, 24 * time.Hour, nil
	})
	for i := 0; i < 300; i++ {
		if _, err := cache.resolve(context.Background(), string(rune(i))+".example"); err != nil {
			t.Fatal(err)
		}
	}
	if len(cache.entries) != 256 {
		t.Fatalf("unbounded cache: %d", len(cache.entries))
	}
	for _, entry := range cache.entries {
		if entry.expires.After(time.Now().Add(5 * time.Minute)) {
			t.Fatal("TTL exceeded maximum")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := cache.resolve(ctx, "model.example"); errorCode(err) != "canceled" {
		t.Fatalf("cancellation lost: %v", err)
	}
}
