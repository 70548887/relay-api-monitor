package store

import (
	"reflect"
	"testing"
	"time"

	"relaylab/internal/domain"
)

func controlsFixture(t *testing.T) (*Store, domain.Channel, domain.Model) {
	t.Helper()
	s, ctx, c, m := fixture(t)
	controls := domain.DefaultModelControls()
	c.ModelControls = &controls
	c.IntelligenceModelID = m.ID
	if err := s.SaveChannelWithModelControls(ctx, c, false); err != nil {
		t.Fatal(err)
	}
	current, err := s.GetChannel(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s, *current, m
}

func TestGroupModelControlsApplyOnlyChangedFieldsOrExplicitReset(t *testing.T) {
	s, c, m := controlsFixture(t)
	ctx := t.Context()
	other := m
	other.ID, other.UpstreamID = "other", "other"
	if err := s.SaveModelWithSchedules(ctx, other); err != nil {
		t.Fatal(err)
	}
	no := false
	m.Public, m.ProbeEnabled, m.ArtifactChinese, m.ArtifactEnglish = false, false, false, false
	m.ArtifactOverrides = &domain.ArtifactOverrides{Chinese: &no, English: &no}
	if err := s.SaveModelWithSchedules(ctx, m); err != nil {
		t.Fatal(err)
	}
	plansBefore, _ := s.ListSchedules(ctx, m.ID)
	c.Name = "ordinary edit"
	if err := s.SaveChannelWithModelControls(ctx, c, false); err != nil {
		t.Fatal(err)
	}
	current, _ := s.GetModel(ctx, m.ID)
	if current.Public || current.ProbeEnabled || current.ArtifactChinese || current.ArtifactEnglish || current.ArtifactOverrides == nil {
		t.Fatal("ordinary channel edits reset individual choices", current)
	}
	plansAfter, _ := s.ListSchedules(ctx, m.ID)
	if !reflect.DeepEqual(schedulesByID(plansBefore), schedulesByID(plansAfter)) {
		t.Fatal("an unchanged configuration restarted model schedules")
	}
	c.ModelControls.ArtifactEnglish = false
	if err := s.SaveChannelWithModelControls(ctx, c, false); err != nil {
		t.Fatal(err)
	}
	current, _ = s.GetModel(ctx, m.ID)
	second, _ := s.GetModel(ctx, other.ID)
	if current.ArtifactChinese || current.ArtifactEnglish || current.ArtifactOverrides.Chinese == nil || current.ArtifactOverrides.English != nil || second.ArtifactEnglish || !second.ArtifactChinese {
		t.Fatal("only the changed English master should be applied", current, second)
	}
	if err := s.SaveChannelWithModelControls(ctx, c, true); err != nil {
		t.Fatal(err)
	}
	current, _ = s.GetModel(ctx, m.ID)
	if !current.Public || !current.ProbeEnabled || !current.ArtifactChinese || current.ArtifactEnglish || current.ArtifactOverrides != nil {
		t.Fatal("explicit reset did not apply all group defaults", current)
	}
}

func TestFirstGroupControlsPreserveRepresentativePlan(t *testing.T) {
	s, ctx, c, m := fixture(t)
	m.ArtifactChinese, m.ArtifactEnglish = false, false
	if err := s.SaveModelWithSchedules(ctx, m); err != nil {
		t.Fatal(err)
	}
	other := m
	other.ID, other.UpstreamID = "other", "other"
	if err := s.SaveModelWithSchedules(ctx, other); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetIntelligenceSchedule(ctx, c.ID, domain.IntelligenceSchedule{Enabled: true, ModelID: m.ID, TemplateIDs: []string{"cn_html"}, IntervalSeconds: 120}); err != nil {
		t.Fatal(err)
	}
	controls := domain.DefaultModelControls()
	c.ModelControls = &controls
	if err := s.SaveChannelWithModelControls(ctx, c, false); err != nil {
		t.Fatal(err)
	}
	saved, _ := s.GetChannel(ctx, c.ID)
	if saved.IntelligenceSchedule == nil || saved.IntelligenceSchedule.ModelScope != "selected" || saved.IntelligenceSchedule.ModelID != m.ID || saved.IntelligenceSchedule.IntervalSeconds != 120 || len(saved.IntelligenceSchedule.TemplateIDs) != 2 {
		t.Fatal("adoption did not preserve the selected plan", saved.IntelligenceSchedule)
	}
	plans, _ := s.ListSchedules(ctx)
	artifacts := 0
	for _, plan := range plans {
		if plan.Kind == "artifact" && plan.Enabled {
			artifacts++
		}
	}
	if artifacts != 2 {
		t.Fatalf("wanted Chinese and English only for the selected model, got %d", artifacts)
	}
}

func TestOrdinaryLegacyAdoptionPreservesEffectivePlansPrivateFlagsAndOverrides(t *testing.T) {
	s, ctx, c, first := fixture(t)
	no := false
	first.Public, first.ProbeEnabled, first.ArtifactChinese, first.ArtifactEnglish = false, false, false, false
	if err := s.SaveModelWithSchedules(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.ID, second.UpstreamID = "other-legacy", "other-legacy"
	second.ArtifactChinese = true
	second.ArtifactOverrides = &domain.ArtifactOverrides{Chinese: &no}
	if err := s.SaveModelWithSchedules(ctx, second); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetIntelligenceSchedule(ctx, c.ID, domain.IntelligenceSchedule{Enabled: true, ModelID: first.ID, TemplateIDs: []string{"cn_html"}, IntervalSeconds: 120}); err != nil {
		t.Fatal(err)
	}
	c.Name = "ordinary legacy rename"
	c.ModelControls = &domain.ModelControls{ProbeEnabled: true, ArtifactChinese: true, ArtifactEnglish: false}
	if err := s.SaveChannelWithModelControls(ctx, c, false); err != nil {
		t.Fatal(err)
	}
	saved, _ := s.GetChannel(ctx, c.ID)
	one, _ := s.GetModel(ctx, first.ID)
	two, _ := s.GetModel(ctx, second.ID)
	if one.Public || one.ProbeEnabled || !one.ArtifactChinese || one.ArtifactEnglish || !saved.ScheduledArtifactEnabled(*one, "cn_html") {
		t.Fatal("legacy effective selected plan or private individual flags changed", one)
	}
	if two.Public || two.ProbeEnabled || two.ArtifactChinese || two.ArtifactEnglish || two.ArtifactOverrides == nil || two.ArtifactOverrides.Chinese == nil || *two.ArtifactOverrides.Chinese || saved.ScheduledArtifactEnabled(*two, "cn_html") {
		t.Fatal("ordinary adoption reopened an ineligible or explicitly disabled model", two)
	}
	if saved.IntelligenceSchedule.ModelScope != "selected" || saved.IntelligenceSchedule.ModelID != first.ID || saved.IntelligenceSchedule.IntervalSeconds != 120 {
		t.Fatal("adoption did not preserve interval and selected model range", saved.IntelligenceSchedule)
	}
	if err := s.SaveChannelWithModelControls(ctx, *saved, true); err != nil {
		t.Fatal(err)
	}
	one, _ = s.GetModel(ctx, first.ID)
	two, _ = s.GetModel(ctx, second.ID)
	for _, model := range []*domain.Model{one, two} {
		if !model.Public || !model.ProbeEnabled || !model.ArtifactChinese || model.ArtifactEnglish || model.ArtifactOverrides != nil {
			t.Fatal("explicit apply-all failed to reset every selected field", model)
		}
	}
}

func TestOrdinaryAdoptionWithoutLegacyPlanKeepsIndividualRawSwitches(t *testing.T) {
	s, ctx, c, m := fixture(t)
	m.Public, m.ProbeEnabled, m.ArtifactChinese, m.ArtifactEnglish = false, false, false, false
	if err := s.SaveModelWithSchedules(ctx, m); err != nil {
		t.Fatal(err)
	}
	controls := domain.DefaultModelControls()
	c.ModelControls = &controls
	if err := s.SaveChannelWithModelControls(ctx, c, false); err != nil {
		t.Fatal(err)
	}
	model, _ := s.GetModel(ctx, m.ID)
	if model.Public || model.ProbeEnabled || model.ArtifactChinese || model.ArtifactEnglish {
		t.Fatal("first ordinary group save overwrote existing individual switches", model)
	}
	plans, _ := s.ListSchedules(ctx, m.ID)
	for _, plan := range plans {
		if plan.Enabled {
			t.Fatal("ordinary legacy adoption unexpectedly enabled a schedule", plan)
		}
	}
}

func TestDiscoveryInheritsNewDefaultsAndPreservesExistingOptOuts(t *testing.T) {
	s, c, m := controlsFixture(t)
	ctx := t.Context()
	no := false
	m.Public, m.ProbeEnabled, m.ArtifactChinese = false, false, false
	m.ArtifactOverrides = &domain.ArtifactOverrides{Chinese: &no}
	if err := s.SaveModelWithSchedules(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncModels(ctx, c.ID, []string{m.UpstreamID, "new"}); err != nil {
		t.Fatal(err)
	}
	models, _ := s.ListModels(ctx, c.ID)
	for _, model := range models {
		if model.ID == m.ID {
			if model.Public || model.ProbeEnabled || model.ArtifactChinese || model.ArtifactOverrides == nil {
				t.Fatal("sync reopened a saved individual opt-out", model)
			}
		} else if !model.Public || !model.ProbeEnabled || !model.ArtifactChinese || !model.ArtifactEnglish || model.MaxTokens != 8192 || model.TimeoutSeconds <= 0 || model.ProbeIntervalSeconds <= 0 || model.ArtifactIntervalSeconds <= 0 {
			t.Fatal("new synchronized model did not inherit defaults", model)
		}
	}
	c.Public, c.ModelControls.ArtifactEnglish = false, false
	if err := s.SaveChannelWithModelControls(ctx, c, false); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncModels(ctx, c.ID, []string{m.UpstreamID, "new", "second-new"}); err != nil {
		t.Fatal(err)
	}
	models, _ = s.ListModels(ctx, c.ID)
	for _, model := range models {
		if model.UpstreamID == "second-new" && (model.Public || !model.ProbeEnabled || !model.ArtifactChinese || model.ArtifactEnglish) {
			t.Fatal("a new model bypassed its group controls", model)
		}
	}
}

func TestAllScopePlanRespectsModelOffAndRepeatedSave(t *testing.T) {
	s, c, m := controlsFixture(t)
	ctx := t.Context()
	other := m
	other.ID, other.UpstreamID = "other", "other"
	if err := s.SaveModelWithSchedules(ctx, other); err != nil {
		t.Fatal(err)
	}
	no := false
	m.ArtifactChinese = false
	m.ArtifactOverrides = &domain.ArtifactOverrides{Chinese: &no}
	if err := s.SaveModelWithSchedules(ctx, m); err != nil {
		t.Fatal(err)
	}
	plan := domain.IntelligenceSchedule{Enabled: true, ModelScope: "all", TemplateIDs: []string{"cn_html"}, IntervalSeconds: 120}
	if _, err := s.SetIntelligenceSchedule(ctx, c.ID, plan); err != nil {
		t.Fatal(err)
	}
	before, _ := s.ListSchedules(ctx)
	if _, err := s.SetIntelligenceSchedule(ctx, c.ID, plan); err != nil {
		t.Fatal(err)
	}
	after, _ := s.ListSchedules(ctx)
	if !reflect.DeepEqual(schedulesByID(before), schedulesByID(after)) {
		t.Fatal("repeat save reset schedule times")
	}
	active := 0
	for _, p := range after {
		if p.Kind == "artifact" && p.Enabled {
			active++
			if p.ModelID != other.ID || p.TemplateID != "cn_html" {
				t.Fatal("the individual opt-out was ignored", p)
			}
		}
	}
	if active != 1 {
		t.Fatalf("unexpected active artifact plans %d", active)
	}
	saved, _ := s.GetChannel(ctx, c.ID)
	if !saved.ModelControls.ProbeEnabled || !saved.ModelControls.ArtifactChinese || saved.ModelControls.ArtifactEnglish {
		t.Fatal("schedule edit did not synchronize only artifact masters", saved.ModelControls)
	}
}

func TestGroupControlTransactionRollsBackModelsPlansAndConfiguration(t *testing.T) {
	s, c, _ := controlsFixture(t)
	ctx := t.Context()
	before, _ := s.GetChannel(ctx, c.ID)
	models, _ := s.ListModels(ctx, c.ID)
	plans, _ := s.ListSchedules(ctx)
	if _, err := s.DB.ExecContext(ctx, "CREATE TRIGGER group_controls_failure BEFORE INSERT ON schedules WHEN NEW.template_id='en_svg' BEGIN SELECT RAISE(ABORT,'group controls rollback'); END"); err != nil {
		t.Fatal(err)
	}
	c.Public, c.ModelControls.ProbeEnabled = false, false
	if err := s.SaveChannelWithModelControls(ctx, c, false); err == nil {
		t.Fatal("expected a failed schedule write")
	}
	after, _ := s.GetChannel(ctx, c.ID)
	actualModels, _ := s.ListModels(ctx, c.ID)
	actualPlans, _ := s.ListSchedules(ctx)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(models, actualModels) || !reflect.DeepEqual(schedulesByID(plans), schedulesByID(actualPlans)) {
		t.Fatal("partial group-control changes committed")
	}
}

func TestControlledProbeDisablePreservesDispatchedAndManualRequests(t *testing.T) {
	s, c, m := controlsFixture(t)
	ctx := t.Context()
	plans, _ := s.ListSchedules(ctx, m.ID)
	var probe domain.Schedule
	for _, plan := range plans {
		if plan.Kind == "probe" {
			probe = plan
		}
	}
	if _, err := s.EnqueueJob(ctx, domain.Job{ModelID: m.ID, ScheduleID: probe.ID, Kind: "probe"}); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimJob(ctx, "probe", "controls-owner", nowUTC())
	if err != nil || claimed == nil {
		t.Fatal("claim failed", err)
	}
	if err = s.MarkDispatching(ctx, claimed.ID, claimed.LeaseToken, nowUTC().Add(time.Minute), "{}"); err != nil {
		t.Fatal(err)
	}
	manual, err := s.EnqueueJob(ctx, domain.Job{ModelID: m.ID, Kind: "artifact", TemplateID: "cn_html"})
	if err != nil {
		t.Fatal(err)
	}
	c.ModelControls.ProbeEnabled = false
	if err = s.SaveChannelWithModelControls(ctx, c, false); err != nil {
		t.Fatal(err)
	}
	dispatched, _ := s.GetJob(ctx, claimed.ID)
	queued, _ := s.GetJob(ctx, manual.ID)
	saved, _ := s.GetChannel(ctx, c.ID)
	if dispatched.State != "dispatching" || dispatched.CancelRequested || queued.State != "queued" || queued.CancelRequested || saved.Revision != manual.ChannelRevision {
		t.Fatal("switch-only edit invalidated in-flight or manual requests", dispatched, queued, saved.Revision)
	}
}
