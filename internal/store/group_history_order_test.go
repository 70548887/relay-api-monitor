package store

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"relaylab/internal/domain"
)

func TestCatalogListsNewestFirstWithinExplicitSort(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "order.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	// Neither names nor IDs encode creation order. Whole and fractional seconds
	// must compare correctly, and identical times need a stable ID tie-breaker.
	entries := []struct {
		id, name string
		created  time.Time
	}{
		{"z-old", "zulu", base},
		{"b-fraction", "alpha", base.Add(time.Nanosecond)},
		{"c-tenth", "november", base.Add(100 * time.Millisecond)},
		{"d-eleven", "echo", base.Add(110 * time.Millisecond)},
		{"a-tie", "mike", base.Add(time.Hour)},
		{"z-tie", "bravo", base.Add(time.Hour)},
	}
	for _, entry := range entries {
		p := domain.Platform{ID: "p-" + entry.id, Name: entry.name, Public: true, CreatedAt: entry.created}
		if err = s.SavePlatform(ctx, p); err != nil {
			t.Fatal(err)
		}
		c := domain.Channel{ID: "c-" + entry.id, PlatformID: "p-z-old", Name: entry.name, Enabled: true, EncryptedKey: "opaque-secret", CreatedAt: entry.created}
		if err = s.SaveChannel(ctx, c); err != nil {
			t.Fatal(err)
		}
		m := domain.Model{ID: "m-" + entry.id, ChannelID: "c-z-old", UpstreamID: entry.id, DisplayName: entry.name, CreatedAt: entry.created}
		if err = s.SaveModel(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	check := func(want []string) {
		t.Helper()
		platforms, e := s.ListPlatforms(ctx)
		if e != nil {
			t.Fatal(e)
		}
		platformIDs := make([]string, len(platforms))
		for i, p := range platforms {
			platformIDs[i] = p.ID[2:]
		}
		if !reflect.DeepEqual(platformIDs, want) {
			t.Fatalf("platform order %v, want %v", platformIDs, want)
		}
		for _, filter := range []string{"", "p-z-old"} {
			channels, e := s.ListChannels(ctx, filter)
			if e != nil {
				t.Fatal(e)
			}
			ids := make([]string, len(channels))
			for i, c := range channels {
				ids[i] = c.ID[2:]
			}
			if !reflect.DeepEqual(ids, want) {
				t.Fatalf("channel order (filter %q) %v, want %v", filter, ids, want)
			}
		}
		for _, filter := range []string{"", "c-z-old"} {
			models, e := s.ListModels(ctx, filter)
			if e != nil {
				t.Fatal(e)
			}
			ids := make([]string, len(models))
			for i, m := range models {
				ids[i] = m.ID[2:]
			}
			if !reflect.DeepEqual(ids, want) {
				t.Fatalf("model order (filter %q) %v, want %v", filter, ids, want)
			}
		}
	}
	want := []string{"a-tie", "z-tie", "d-eleven", "c-tenth", "b-fraction", "z-old"}
	check(want)
	// Renaming never changes the added-time order.
	p, err := s.GetPlatform(ctx, "p-b-fraction")
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.GetChannel(ctx, "c-b-fraction")
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.GetModel(ctx, "m-b-fraction")
	if err != nil {
		t.Fatal(err)
	}
	p.Name, c.Name, m.DisplayName = "renamed", "renamed", "renamed"
	if err = s.SavePlatform(ctx, *p); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveChannel(ctx, *c); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveModel(ctx, *m); err != nil {
		t.Fatal(err)
	}
	check(want)
	// A manually pinned old entry still precedes the newest records.
	if err = s.SavePlatform(ctx, domain.Platform{ID: "p-pinned", Name: "pinned", Sort: -1, CreatedAt: base.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveChannel(ctx, domain.Channel{ID: "c-pinned", PlatformID: "p-z-old", Name: "pinned", Sort: -1, CreatedAt: base.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveModel(ctx, domain.Model{ID: "m-pinned", ChannelID: "c-z-old", UpstreamID: "pinned", Sort: -1, CreatedAt: base.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	check(append([]string{"pinned"}, want...))
	// A larger explicit sort value remains last even for a newer record.
	if err = s.SavePlatform(ctx, domain.Platform{ID: "p-last", Name: "last", Sort: 1, CreatedAt: base.Add(24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveChannel(ctx, domain.Channel{ID: "c-last", PlatformID: "p-z-old", Name: "last", Sort: 1, CreatedAt: base.Add(24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveModel(ctx, domain.Model{ID: "m-last", ChannelID: "c-z-old", UpstreamID: "last", Sort: 1, CreatedAt: base.Add(24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	check(append(append([]string{"pinned"}, want...), "last"))
}

func TestListChannelJobsIsScopedToOneGroup(t *testing.T) {
	s, ctx, c, m := fixture(t)
	other := domain.Channel{ID: "other-channel", PlatformID: c.PlatformID, Name: "other", Enabled: true, EncryptedKey: "opaque-secret"}
	if err := s.SaveChannel(ctx, other); err != nil {
		t.Fatal(err)
	}
	otherModel := domain.Model{ID: "other-model", ChannelID: other.ID, UpstreamID: "other-upstream"}
	if err := s.SaveModel(ctx, otherModel); err != nil {
		t.Fatal(err)
	}
	own, err := s.EnqueueJob(ctx, domain.Job{ModelID: m.ID, Kind: "probe"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.EnqueueJob(ctx, domain.Job{ModelID: otherModel.ID, Kind: "probe"}); err != nil {
		t.Fatal(err)
	}
	jobs, err := s.ListChannelJobs(ctx, c.ID, 10)
	if err != nil || len(jobs) != 1 || jobs[0].ID != own.ID || jobs[0].ChannelID != c.ID {
		t.Fatal(err, jobs)
	}
	if all, err := s.ListJobs(ctx); err != nil || len(all) != 2 {
		t.Fatal(err, all)
	}
}

func TestDiscoveredAnthropicModelsGetLargerOutputLimit(t *testing.T) {
	s, ctx, c, m := fixture(t)
	c.Protocol = "anthropic"
	if err := s.SaveChannel(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncModels(ctx, c.ID, []string{m.UpstreamID, "claude-new"}); err != nil {
		t.Fatal(err)
	}
	models, err := s.ListModels(ctx, c.ID)
	if err != nil || len(models) != 2 {
		t.Fatal(err, models)
	}
	for _, model := range models {
		want := domain.DefaultArtifactMaxTokens("anthropic")
		if model.ID == m.ID {
			// A limit that was already saved is never rewritten by discovery.
			want = m.MaxTokens
		}
		if model.MaxTokens != want {
			t.Fatalf("%s max tokens = %d, want %d", model.UpstreamID, model.MaxTokens, want)
		}
	}
	if domain.DefaultArtifactMaxTokens("chat") != 8192 || domain.DefaultArtifactMaxTokens("") != 8192 || domain.DefaultArtifactMaxTokens("anthropic") <= 8192 {
		t.Fatal("unexpected default output limits")
	}
}
