package store

import (
	"reflect"
	"testing"

	"relaylab/internal/domain"
)

func schedulesByID(values []domain.Schedule) map[string]domain.Schedule {
	out := map[string]domain.Schedule{}
	for _, v := range values {
		out[v.ID] = v
	}
	return out
}

func TestAtomicModelConfigurationRollsBackOnScheduleFailure(t *testing.T) {
	s, ctx, _, m := fixture(t)
	if err := s.ReconcileSchedules(ctx, m); err != nil {
		t.Fatal(err)
	}
	before, err := s.ListSchedules(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.ExecContext(ctx, "CREATE TRIGGER schedule_failure BEFORE INSERT ON schedules WHEN NEW.template_id='en_svg' BEGIN SELECT RAISE(ABORT,'forced schedule failure'); END"); err != nil {
		t.Fatal(err)
	}
	changed := m
	changed.DisplayName = "must rollback"
	changed.ProbeEnabled = false
	changed.ArtifactIntervalSeconds = 73
	if err = s.SaveModelWithSchedules(ctx, changed); err == nil {
		t.Fatal("expected failed atomic configuration")
	}
	after, err := s.GetModel(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.DisplayName != m.DisplayName || after.ProbeEnabled != m.ProbeEnabled || after.ArtifactIntervalSeconds != m.ArtifactIntervalSeconds {
		t.Fatal("model changed despite failed schedule", after)
	}
	schedules, err := s.ListSchedules(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(schedulesByID(before), schedulesByID(schedules)) {
		t.Fatal("part of schedule set committed")
	}
	if _, err = s.DB.ExecContext(ctx, "DROP TRIGGER schedule_failure"); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveModelWithSchedules(ctx, changed); err != nil {
		t.Fatal(err)
	}
	schedules, err = s.ListSchedules(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range schedules {
		if v.Kind == "probe" && v.Enabled {
			t.Fatal("probe remained enabled")
		}
		if v.Kind == "artifact" && v.IntervalSeconds != 73 {
			t.Fatal("artifact interval not reconciled")
		}
	}
}

func TestAtomicSettingsRollBackAllModelsOnLateScheduleFailure(t *testing.T) {
	s, ctx, _, m := fixture(t)
	if err := s.SaveModelWithSchedules(ctx, m); err != nil {
		t.Fatal(err)
	}
	other := m
	other.ID = "z-second"
	other.UpstreamID = "other"
	if err := s.SaveModelWithSchedules(ctx, other); err != nil {
		t.Fatal(err)
	}
	before, err := s.ListSchedules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := s.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.ExecContext(ctx, "CREATE TRIGGER settings_failure BEFORE INSERT ON schedules WHEN NEW.model_id='z-second' AND NEW.template_id='en_svg' BEGIN SELECT RAISE(ABORT,'late failure'); END"); err != nil {
		t.Fatal(err)
	}
	changed := settings
	changed.ProbeIntervalSeconds = 97
	changed.ArtifactIntervalSeconds = 211
	changed.ProbeConcurrency = 7
	if err = s.SaveSettingsAndReconcile(ctx, changed); err == nil {
		t.Fatal("expected settings rollback")
	}
	after, err := s.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after != settings {
		t.Fatal("settings committed before schedule failure")
	}
	schedules, err := s.ListSchedules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(schedulesByID(before), schedulesByID(schedules)) {
		t.Fatal("earlier model schedules committed before later model failure")
	}
	if _, err = s.DB.ExecContext(ctx, "DROP TRIGGER settings_failure"); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveSettingsAndReconcile(ctx, changed); err != nil {
		t.Fatal(err)
	}
	schedules, err = s.ListSchedules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range schedules {
		if v.Kind == "probe" && v.IntervalSeconds != 97 || v.Kind == "artifact" && v.IntervalSeconds != 211 {
			t.Fatal("global interval not propagated", v)
		}
	}
}

func TestAtomicChannelSchedulesPreserveKeyRevisionAndRollback(t *testing.T) {
	s, ctx, c, m := fixture(t)
	if err := s.SaveModelWithSchedules(ctx, m); err != nil {
		t.Fatal(err)
	}
	before, err := s.GetChannel(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	plans, err := s.ListSchedules(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.ExecContext(ctx, "CREATE TRIGGER channel_failure BEFORE INSERT ON schedules WHEN NEW.template_id='en_svg' BEGIN SELECT RAISE(ABORT,'channel plan failure'); END"); err != nil {
		t.Fatal(err)
	}
	changed := *before
	changed.Name = "disabled"
	changed.Enabled = false
	changed.EncryptedKey = ""
	changed.Revision += 99
	if err = s.SaveChannelWithSchedules(ctx, changed); err == nil {
		t.Fatal("expected channel rollback")
	}
	after, err := s.GetChannel(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("channel configuration committed on schedule error")
	}
	schedules, err := s.ListSchedules(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(schedulesByID(plans), schedulesByID(schedules)) {
		t.Fatal("channel schedules partially disabled")
	}
	if _, err = s.DB.ExecContext(ctx, "DROP TRIGGER channel_failure"); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveChannelWithSchedules(ctx, changed); err != nil {
		t.Fatal(err)
	}
	after, err = s.GetChannel(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != before.Revision+1 || after.EncryptedKey != before.EncryptedKey || after.Enabled {
		t.Fatal("channel save semantics changed", after)
	}
	schedules, err = s.ListSchedules(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range schedules {
		if v.Enabled {
			t.Fatal("disabled channel left enabled schedule")
		}
	}
	changed = *after
	changed.Enabled = true
	if err = s.SaveChannelWithSchedules(ctx, changed); err != nil {
		t.Fatal(err)
	}
	schedules, err = s.ListSchedules(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range schedules {
		if !v.Enabled {
			t.Fatal("reenabling channel did not restore model plan flags")
		}
	}
}

func TestSyncModelsAndSchedulesRollbackTogether(t *testing.T) {
	s, ctx, c, m := fixture(t)
	if err := s.SaveModelWithSchedules(ctx, m); err != nil {
		t.Fatal(err)
	}
	before, err := s.ListSchedules(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.ExecContext(ctx, "CREATE TRIGGER sync_plan_failure BEFORE INSERT ON schedules WHEN NEW.template_id='en_svg' BEGIN SELECT RAISE(ABORT,'sync plans failed'); END"); err != nil {
		t.Fatal(err)
	}
	if err = s.SyncModels(ctx, c.ID, []string{"new-upstream"}); err == nil {
		t.Fatal("expected atomic sync rollback")
	}
	got, err := s.GetModel(ctx, m.ID)
	if err != nil || got.Missing {
		t.Fatal("sync committed missing flag before failed plans", got, err)
	}
	models, err := s.ListModels(ctx, c.ID)
	if err != nil || len(models) != 1 {
		t.Fatal("new model committed before failed plans", models, err)
	}
	channel, err := s.GetChannel(ctx, c.ID)
	if err != nil || channel.LastSyncAt != nil {
		t.Fatal("sync health committed early", channel, err)
	}
	after, err := s.ListSchedules(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(schedulesByID(before), schedulesByID(after)) {
		t.Fatal("partial sync schedule update")
	}
}
