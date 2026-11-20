package server

import (
	"strconv"
	"testing"
	"time"
)

func TestAllowedWithinCountsEachKeyInItsOwnWindow(t *testing.T) {
	s, _ := testServer(t)
	for call := 1; call <= 2; call++ {
		if allowed, wait := s.allowedWithin("hourly", 2, time.Hour); !allowed || wait != 0 {
			t.Fatalf("call %d of 2 in the hour: allowed %v, wait %v", call, allowed, wait)
		}
	}
	if allowed, wait := s.allowedWithin("hourly", 2, time.Hour); allowed || wait > time.Hour || wait < time.Hour-time.Second {
		t.Fatalf("a third call in the hour: allowed %v, wait %v; want a refusal that lasts the rest of the hour", allowed, wait)
	}
	if !s.allowedRate("hourly-other", 1) || s.allowedRate("hourly-other", 1) {
		t.Fatal("another key is not counted on its own")
	}

	// A window outlives a minute: forty minutes in, the hour is still counted and twenty minutes of it remain.
	s.rates["hourly"].Start = time.Now().Add(-40 * time.Minute)
	if allowed, wait := s.allowedWithin("hourly", 2, time.Hour); allowed || wait > 20*time.Minute || wait < 20*time.Minute-time.Second {
		t.Fatalf("a call forty minutes into the hour: allowed %v, wait %v", allowed, wait)
	}

	// A full table drops the windows that ended and keeps the ones that still count.
	ended := time.Now().Add(-2 * time.Minute)
	for i := 0; i <= 10000; i++ {
		s.rates["ended-"+strconv.Itoa(i)] = &rateBucket{Start: ended, Count: 1, Window: time.Minute}
	}
	if !s.allowedRate("new", 1) {
		t.Fatal("a new key was refused although the table held only ended windows")
	}
	if len(s.rates) != 3 || s.rates["hourly"] == nil || s.rates["hourly-other"] == nil {
		t.Fatalf("%d windows kept, want the hour, the running minute and the new one", len(s.rates))
	}
	if allowed, _ := s.allowedWithin("hourly", 2, time.Hour); allowed {
		t.Fatal("the hour started over when the table was cleaned")
	}

	s.rates["hourly"].Start = time.Now().Add(-time.Hour)
	if allowed, wait := s.allowedWithin("hourly", 2, time.Hour); !allowed || wait != 0 || s.rates["hourly"].Count != 1 {
		t.Fatalf("a call after the hour: allowed %v, wait %v, count %d; want a new hour", allowed, wait, s.rates["hourly"].Count)
	}

	for i := 0; i <= 10000; i++ {
		s.rates["live-"+strconv.Itoa(i)] = &rateBucket{Start: time.Now(), Count: 1, Window: time.Minute}
	}
	if allowed, wait := s.allowedWithin("late", 5, time.Hour); allowed || wait != time.Minute || s.rates["late"] != nil {
		t.Fatalf("a new key with a full table: allowed %v, wait %v; want it to try again in a minute", allowed, wait)
	}
}
