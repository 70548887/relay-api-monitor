package store

import (
	"errors"
	"testing"
	"time"

	"relaylab/internal/domain"
)

func TestManualJobDispatchRejectsNewlyMissingModel(t *testing.T) {
	s, ctx, _, m := fixture(t)
	j := claimed(t, s, ctx, m, "artifact", "cn_html")
	m.Missing = true
	if err := s.SaveModel(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkDispatching(ctx, j.ID, j.LeaseToken, nowUTC().Add(time.Minute), "{}"); !errors.Is(err, ErrConfigurationChanged) {
		t.Fatalf("missing model dispatched: %v", err)
	}
}

func TestDualTemplateScheduleDoesNotDiscardWaitingSibling(t *testing.T) {
	s, ctx, c, m := fixture(t)
	m.ProbeEnabled = false
	if err := s.SaveModelWithSchedules(ctx, m); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetIntelligenceSchedule(ctx, c.ID, domain.IntelligenceSchedule{Enabled: true, ModelID: m.ID, TemplateIDs: []string{"cn_html", "en_svg"}, IntervalSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	now := nowUTC()
	plans, _ := s.ListSchedules(ctx, m.ID)
	for _, p := range plans {
		p.NextRunAt = now.Add(-time.Minute)
		if err := s.SaveSchedule(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.TickSchedules(ctx, now); err != nil || n != 1 {
		t.Fatalf("first tick=%d %v", n, err)
	}
	first, err := s.ClaimJob(ctx, "artifact", "audit-owner", nowUTC())
	if err != nil || first == nil {
		t.Fatalf("first claim: %v", err)
	}
	first.State = "succeeded"
	if err := s.FinishJob(ctx, *first, nil, nil); err != nil {
		t.Fatal(err)
	}
	if n, err := s.TickSchedules(ctx, now.Add(time.Second)); err != nil || n != 1 {
		t.Fatalf("second selected template lost its due cycle: count=%d error=%v", n, err)
	}
	second, err := s.ClaimJob(ctx, "artifact", "audit-owner", nowUTC())
	if err != nil || second == nil {
		t.Fatalf("second claim: %v", err)
	}
	if second.TemplateID == first.TemplateID {
		t.Fatal("same template ran twice while sibling was waiting")
	}
	if n, err := s.TickSchedules(ctx, now.Add(2*time.Second)); err != nil || n != 0 {
		t.Fatalf("duplicate scheduled job: %d %v", n, err)
	}
}

func TestEnqueueRejectsMismatchedAndUnavailableTargets(t *testing.T) {
	for _, scenario := range []string{"wrong-channel", "disabled-channel", "missing-model", "archived-platform"} {
		t.Run(scenario, func(t *testing.T) {
			s, ctx, c, m := fixture(t)
			job := domain.Job{ModelID: m.ID, ChannelID: c.ID, Kind: "artifact", TemplateID: "cn_html"}
			switch scenario {
			case "wrong-channel":
				other := c
				other.ID = "other-channel"
				if err := s.SaveChannel(ctx, other); err != nil {
					t.Fatal(err)
				}
				job.ChannelID = other.ID
			case "disabled-channel":
				c.Enabled = false
				if err := s.SaveChannel(ctx, c); err != nil {
					t.Fatal(err)
				}
			case "missing-model":
				m.Missing = true
				if err := s.SaveModel(ctx, m); err != nil {
					t.Fatal(err)
				}
			case "archived-platform":
				p, err := s.GetPlatform(ctx, c.PlatformID)
				if err != nil {
					t.Fatal(err)
				}
				p.Archived = true
				if err = s.SavePlatform(ctx, *p); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.EnqueueJob(ctx, job); err == nil {
				t.Fatalf("invalid target %s admitted", scenario)
			}
			jobs, _ := s.ListJobs(ctx)
			if len(jobs) != 0 {
				t.Fatal("invalid target left a queued job")
			}
		})
	}
}
