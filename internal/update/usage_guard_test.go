package update

import (
	"context"
	"encoding/json"
	"testing"
)

func TestManifestUsageGuardCompatibility(t *testing.T) {
	for _, test := range []struct {
		name, current, state string
		guard                int
		omit                 bool
	}{
		{name: "new_guarded_release", current: "1.2.1", state: "available", guard: 1},
		{name: "new_legacy_release", current: "1.2.1", state: "failed", omit: true},
		{name: "new_zero_guard", current: "1.2.1", state: "failed"},
		{name: "equal_legacy_release", current: "1.2.2", state: "idle", omit: true},
		{name: "older_legacy_release", current: "1.2.3", state: "idle", omit: true},
		{name: "older_zero_guard", current: "1.2.3", state: "idle"},
		{name: "negative_guard", current: "1.2.3", state: "failed", guard: -1},
		{name: "unknown_guard", current: "1.2.3", state: "failed", guard: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, m := newFixture(t)
			m.build.Version = test.current
			f.manifest.UsageGuard = test.guard
			if test.omit {
				data, err := json.Marshal(f.manifest)
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(data, &fields); err != nil {
					t.Fatal(err)
				}
				delete(fields, "usage_guard")
				f.rawManifest, err = json.Marshal(fields)
				if err != nil {
					t.Fatal(err)
				}
			}
			snap, err := m.Check(context.Background())
			if snap.State != test.state {
				t.Fatalf("unexpected usage guard state: %s %v", snap.State, err)
			}
			if test.state == "failed" {
				if err == nil || snap.Operation == nil || snap.Operation.ErrorCode != "invalid_manifest" {
					t.Fatalf("incompatible usage guard accepted: %#v %v", snap, err)
				}
				return
			}
			if err != nil || snap.Latest == nil || snap.Latest.UsageGuard != test.guard || snap.Release.UsageGuard != test.guard || snap.Latest.Available != (test.state == "available") {
				t.Fatalf("usage guard compatibility metadata lost: %#v %v", snap, err)
			}
		})
	}
}

func TestUsageGuardIsPartOfStagedTargetIdentity(t *testing.T) {
	_, m := newFixture(t)
	if _, err := m.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	guarded := *m.state.Latest
	legacy := guarded
	legacy.UsageGuard = 0
	if targetBinding(guarded) == targetBinding(legacy) || !validInfo(guarded) || validInfo(legacy) {
		t.Fatal("legacy usage behavior can reuse a guarded update identity")
	}
}
