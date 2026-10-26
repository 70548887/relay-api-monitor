package store

import (
	"errors"
	"testing"
	"time"

	"relaylab/internal/domain"
)

func TestSettingsLegacyHistoryScopeKeepsSavedTimeout(t *testing.T) {
	s, ctx, _, _ := fixture(t)
	cfg, err := s.GetSettings(ctx)
	if err != nil || cfg.ArtifactTimeoutSeconds != 600 || cfg.ArtifactHistoryScope != "group" {
		t.Fatal("new defaults", cfg, err)
	}
	if _, err = s.DB.ExecContext(ctx, `UPDATE settings SET data='{"artifact_timeout_seconds":180,"retention_days":30}' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	cfg, err = s.GetSettings(ctx)
	if err != nil || cfg.ArtifactHistoryScope != "group" || cfg.ArtifactTimeoutSeconds != 180 {
		t.Fatal("legacy setting changed", cfg, err)
	}
}

func TestArtifactRetentionSupportsGroupAndModelScopes(t *testing.T) {
	for _, scope := range []string{"group", "model"} {
		t.Run(scope, func(t *testing.T) {
			s, ctx, c, first := fixture(t)
			cfg, err := s.GetSettings(ctx)
			if err != nil {
				t.Fatal(err)
			}
			cfg.ArtifactHistoryScope = scope
			if err = s.SaveSettings(ctx, cfg); err != nil {
				t.Fatal(err)
			}
			second := first
			second.ID, second.UpstreamID = "second", "other-model"
			if err = s.SaveModel(ctx, second); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 12; i++ {
				m := first
				if i%2 == 1 {
					m = second
				}
				j := dispatch(t, s, ctx, claimed(t, s, ctx, m, "artifact", "cn_html"))
				j.State = "succeeded"
				if err = s.FinishJob(ctx, j, nil, &domain.Artifact{Code: "<html>ok</html>", RawResponse: "source", CreatedAt: nowUTC().Add(time.Duration(i) * time.Second)}); err != nil {
					t.Fatal(err)
				}
			}
			want := 8
			if scope == "model" {
				want = 12
			}
			arts, err := s.ListChannelArtifactsSummary(ctx, c.ID, "cn_html", 100)
			if err != nil || len(arts) != want {
				t.Fatal("history scope", len(arts), want, err)
			}
			for _, a := range arts {
				if a.Code != "" || a.RawResponse != "" || a.ChannelID != c.ID {
					t.Fatal("incorrect summary", a)
				}
			}
			// Switching scopes is applied by cleanup without scheduling a call.
			cfg.ArtifactHistoryScope = "group"
			if err = s.SaveSettings(ctx, cfg); err != nil {
				t.Fatal(err)
			}
			if err = s.Cleanup(ctx, nowUTC()); err != nil {
				t.Fatal(err)
			}
			arts, err = s.ListChannelArtifactsSummary(ctx, c.ID, "cn_html", 100)
			if err != nil || len(arts) != 8 {
				t.Fatal("cleanup scope", len(arts), err)
			}
			jobs, err := s.ListJobs(ctx)
			if err != nil || len(jobs) != 12 {
				t.Fatal("history query or cleanup scheduled extra work", len(jobs), err)
			}
		})
	}
}

func TestGroupHistorySeparatesChannelsAndTemplates(t *testing.T) {
	s, ctx, c, m := fixture(t)
	otherChannel := c
	otherChannel.ID = "other-channel"
	if err := s.SaveChannel(ctx, otherChannel); err != nil {
		t.Fatal(err)
	}
	other := m
	other.ID, other.ChannelID = "other-model", otherChannel.ID
	if err := s.SaveModel(ctx, other); err != nil {
		t.Fatal(err)
	}
	for _, pair := range []struct {
		model    domain.Model
		template string
	}{{m, "cn_html"}, {m, "en_svg"}, {other, "cn_html"}} {
		for i := 0; i < 9; i++ {
			j := dispatch(t, s, ctx, claimed(t, s, ctx, pair.model, "artifact", pair.template))
			j.State = "succeeded"
			if err := s.FinishJob(ctx, j, nil, &domain.Artifact{Code: "<html>ok</html>"}); err != nil {
				t.Fatal(err)
			}
		}
		arts, err := s.ListChannelArtifactsSummary(ctx, pair.model.ChannelID, pair.template, 8)
		if err != nil || len(arts) != 8 {
			t.Fatal(pair, len(arts), err)
		}
	}
	all, err := s.ListArtifacts(ctx, "")
	if err != nil || len(all) != 24 {
		t.Fatal("independent histories collided", len(all), err)
	}
}

func TestGroupVerificationIsTrustedRevisionFencedAndInvalidated(t *testing.T) {
	for _, changed := range []string{"base_url", "key", "provider", "path", "field"} {
		t.Run(changed, func(t *testing.T) {
			s, ctx, c, _ := fixture(t)
			c.GroupProvider = "newapi"
			c.VerifiedGroup, c.GroupVerificationStatus = "forged", "verified"
			if err := s.SaveChannel(ctx, c); err != nil {
				t.Fatal(err)
			}
			current, err := s.GetChannel(ctx, c.ID)
			if err != nil || current.VerifiedGroup != "" || current.GroupVerifiedAt != nil {
				t.Fatal("client forged verification", current, err)
			}
			revision := current.Revision
			if err = s.SetGroupVerification(ctx, c.ID, revision, "verified", "premium", "newapi", "", nowUTC()); err != nil {
				t.Fatal(err)
			}
			current, err = s.GetChannel(ctx, c.ID)
			if err != nil || current.Revision != revision || current.EncryptedKey != c.EncryptedKey || current.GroupVerificationRevision != revision || current.VerifiedGroup != "premium" {
				t.Fatal("verification changed config", current, err)
			}
			current.Name = "rename"
			current.VerifiedGroup = "client-overwrite"
			if err = s.SaveChannel(ctx, *current); err != nil {
				t.Fatal(err)
			}
			current, err = s.GetChannel(ctx, c.ID)
			if err != nil || current.VerifiedGroup != "premium" || current.GroupVerificationRevision != current.Revision {
				t.Fatal("benign edit lost trusted verification", current, err)
			}
			if err = s.SetGroupVerification(ctx, c.ID, revision, "verified", "stale", "newapi", "", nowUTC()); !errors.Is(err, ErrConflict) {
				t.Fatal("stale verification accepted", err)
			}
			switch changed {
			case "base_url":
				current.BaseURL = "https://example.invalid/v2"
			case "key":
				current.EncryptedKey = "new-encrypted-key"
			case "provider":
				current.GroupProvider = "custom"
			case "path":
				current.GroupPath = "/group"
			case "field":
				current.GroupField = "data.group"
			}
			if err = s.SaveChannelWithSchedules(ctx, *current); err != nil {
				t.Fatal(err)
			}
			current, err = s.GetChannel(ctx, c.ID)
			if err != nil || current.VerifiedGroup != "" || current.GroupVerificationStatus != "" || current.GroupVerificationRevision != 0 || current.GroupVerifiedAt != nil {
				t.Fatal("credential/routing edit retained verification", current, err)
			}
		})
	}
}

func TestJobProgressMonotonicCompletionAndOwnershipFencing(t *testing.T) {
	s, ctx, _, m := fixture(t)
	j := dispatch(t, s, ctx, claimed(t, s, ctx, m, "artifact", "cn_html"))
	if err := s.UpdateJobProgress(ctx, j.ID, j.LeaseToken, 5, "model-a"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateJobProgress(ctx, j.ID, j.LeaseToken, 3, "old-model"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateJobProgress(ctx, j.ID, "stale", 100, "bad"); !errors.Is(err, ErrConflict) {
		t.Fatal("stale token accepted", err)
	}
	j.State, j.ProgressChars, j.ReturnedModel = "failed", 1, "stale-model"
	if err := s.FinishJob(ctx, j, nil, nil); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetJob(ctx, j.ID)
	if err != nil || got.ProgressChars != 5 || got.ReturnedModel != "model-a" || got.ProgressAt == nil {
		t.Fatal("completion overwrote newer progress", got, err)
	}
	if err = s.UpdateJobProgress(ctx, j.ID, j.LeaseToken, 6, "late"); !errors.Is(err, ErrConflict) {
		t.Fatal("terminal progress accepted", err)
	}
	j = dispatch(t, s, ctx, claimed(t, s, ctx, m, "artifact", "cn_html"))
	if ok, err := s.AcquireWorkerLease(ctx, j.Owner, nowUTC()); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, err := s.AcquireWorkerLease(ctx, "replacement", nowUTC().Add(time.Minute)); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err = s.UpdateJobProgress(ctx, j.ID, j.LeaseToken, 8, "old-owner"); !errors.Is(err, ErrConflict) {
		t.Fatal("global ownership bypassed", err)
	}
}

func TestRepresentativeModelSchedulesAndAttemptWithoutSuccessfulArtifact(t *testing.T) {
	s, ctx, c, first := fixture(t)
	second := first
	second.ID, second.UpstreamID = "second", "other"
	for _, m := range []domain.Model{first, second} {
		if err := s.SaveModelWithSchedules(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	assertPlans := func(representative string) {
		t.Helper()
		plans, err := s.ListSchedules(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range plans {
			want := p.Kind == "probe" || representative == "" || p.ModelID == representative
			if p.Enabled != want {
				t.Fatal("wrong representative plan", p, representative)
			}
		}
	}
	assertPlans("")
	c.IntelligenceModelID = first.ID
	if err := s.SaveChannelWithSchedules(ctx, c); err != nil {
		t.Fatal(err)
	}
	assertPlans(first.ID)
	plans, err := s.ListSchedules(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	var plan domain.Schedule
	for _, p := range plans {
		if p.Kind == "artifact" && p.TemplateID == "cn_html" {
			plan = p
		}
	}
	j, err := s.EnqueueJob(ctx, domain.Job{ModelID: first.ID, Kind: "artifact", TemplateID: "cn_html", ScheduleID: plan.ID})
	if err != nil {
		t.Fatal(err)
	}
	c.IntelligenceModelID = second.ID
	if err = s.SaveChannelWithSchedules(ctx, c); err != nil {
		t.Fatal(err)
	}
	assertPlans(second.ID)
	got, err := s.GetJob(ctx, j.ID)
	if err != nil || got.State != "canceled" {
		t.Fatal("old representative queued work survived", got, err)
	}
	latest, err := s.LatestChannelArtifactAttempts(ctx, c.ID, "cn_html")
	if err != nil || len(latest) != 1 || latest[0].ID != j.ID || latest[0].State != "canceled" {
		t.Fatal("attempt without success hidden", latest, err)
	}
	arts, err := s.ListChannelArtifactsSummary(ctx, c.ID, "cn_html", 8)
	if err != nil || len(arts) != 0 {
		t.Fatal(arts, err)
	}
}

func TestPublicChannelHistoryFiltersBeforeLatestAndLimit(t *testing.T) {
	s, ctx, c, m := fixture(t)
	cfg, err := s.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ArtifactHistoryScope = "model"
	if err = s.SaveSettings(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	private := m
	private.ID, private.UpstreamID, private.Public = "private", "private-upstream", false
	if err = s.SaveModel(ctx, private); err != nil {
		t.Fatal(err)
	}
	for _, pair := range []struct {
		model  domain.Model
		hidden bool
	}{{m, false}, {m, true}, {private, false}} {
		j := dispatch(t, s, ctx, claimed(t, s, ctx, pair.model, "artifact", "cn_html"))
		j.State = "succeeded"
		if err = s.FinishJob(ctx, j, nil, &domain.Artifact{Code: "<html>ok</html>", Hidden: pair.hidden}); err != nil {
			t.Fatal(err)
		}
	}
	arts, err := s.ListPublicChannelArtifactsSummary(ctx, c.ID, "cn_html", 1)
	if err != nil || len(arts) != 1 || arts[0].ModelID != m.ID || arts[0].Hidden {
		t.Fatal("invisible history displaced public result", arts, err)
	}
	latest, err := s.LatestPublicChannelArtifactAttempts(ctx, c.ID, "cn_html")
	if err != nil || len(latest) != 1 || latest[0].ModelID != m.ID {
		t.Fatal("private latest displaced public attempt", latest, err)
	}
	p, err := s.GetPlatform(ctx, c.PlatformID)
	if err != nil {
		t.Fatal(err)
	}
	p.Public = false
	if err = s.SavePlatform(ctx, *p); err != nil {
		t.Fatal(err)
	}
	arts, err = s.ListPublicChannelArtifactsSummary(ctx, c.ID, "cn_html", 8)
	if err != nil || len(arts) != 0 {
		t.Fatal("private platform history visible", arts, err)
	}
	latest, err = s.LatestPublicChannelArtifactAttempts(ctx, "", "")
	if err != nil || len(latest) != 0 {
		t.Fatal("private platform attempt visible", latest, err)
	}
}
