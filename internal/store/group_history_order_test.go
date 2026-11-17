package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"relaylab/internal/domain"
)

func TestPlatformsAndChannelsListInAddedOrder(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "order.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	// Names and IDs sort in the opposite direction of the creation times.
	names := []string{"zulu", "mike", "alpha"}
	for i, name := range names {
		p := domain.Platform{ID: "p-" + name, Name: name, Public: true, CreatedAt: base.Add(time.Duration(i) * time.Hour)}
		if err = s.SavePlatform(ctx, p); err != nil {
			t.Fatal(err)
		}
		c := domain.Channel{ID: "c-" + name, PlatformID: p.ID, Name: name, Enabled: true, EncryptedKey: "opaque-secret", CreatedAt: base.Add(time.Duration(i) * time.Hour)}
		if err = s.SaveChannel(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	platforms, err := s.ListPlatforms(ctx)
	if err != nil || len(platforms) != len(names) {
		t.Fatal(err, platforms)
	}
	channels, err := s.ListChannels(ctx)
	if err != nil || len(channels) != len(names) {
		t.Fatal(err, channels)
	}
	for i, name := range names {
		if platforms[i].Name != name || channels[i].Name != name {
			t.Fatalf("position %d: platform %q channel %q, want %q", i, platforms[i].Name, channels[i].Name, name)
		}
	}
	// An explicit sort value still takes precedence over the creation time.
	pinned := domain.Platform{ID: "p-pinned", Name: "pinned", Public: true, Sort: -1, CreatedAt: base.Add(24 * time.Hour)}
	if err = s.SavePlatform(ctx, pinned); err != nil {
		t.Fatal(err)
	}
	if platforms, err = s.ListPlatforms(ctx); err != nil || len(platforms) != len(names)+1 || platforms[0].ID != pinned.ID || platforms[1].Name != names[0] {
		t.Fatal(err, platforms)
	}
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
