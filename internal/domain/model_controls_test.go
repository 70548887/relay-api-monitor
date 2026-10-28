package domain

import "testing"

func TestControlledAndLegacyArtifactPlansRespectIndividualOverrides(t *testing.T) {
	no := false
	m := Model{ID: "first", ArtifactChinese: false, ArtifactEnglish: false, ProbeEnabled: true}
	c := Channel{IntelligenceModelID: "first", IntelligenceSchedule: &IntelligenceSchedule{Enabled: true, ModelID: "first", TemplateIDs: []string{"cn_html", "en_svg"}}}
	if !c.ScheduledArtifactEnabled(m, "cn_html") {
		t.Fatal("legacy group-owned plans must preserve their enabled state despite old raw flags")
	}
	m.ArtifactOverrides = &ArtifactOverrides{Chinese: &no}
	if c.ScheduledArtifactEnabled(m, "cn_html") || !c.ScheduledArtifactEnabled(m, "en_svg") {
		t.Fatal("a deliberate Chinese opt-out must not disable the English plan")
	}
	controls := DefaultModelControls()
	c.ModelControls = &controls
	if c.ScheduledArtifactEnabled(m, "en_svg") {
		t.Fatal("new controls must also honor the model raw switch")
	}
	m.ArtifactEnglish = true
	if !c.ScheduledArtifactEnabled(m, "en_svg") {
		t.Fatal("enabled individual and group switches should execute")
	}
	controls.ArtifactEnglish = false
	if c.ScheduledArtifactEnabled(m, "en_svg") {
		t.Fatal("individual flags cannot bypass the group master switch")
	}
	controls.ProbeEnabled = false
	if c.ScheduledProbeEnabled(m) {
		t.Fatal("probe master switch was bypassed")
	}
}

func TestAllModelScopeUsesEveryEnabledModelAndRepresentativeOnlyForDisplay(t *testing.T) {
	controls := DefaultModelControls()
	c := Channel{ModelControls: &controls, IntelligenceModelID: "representative"}
	m := Model{ID: "other", ArtifactChinese: true, ArtifactEnglish: true, ProbeEnabled: true}
	if !c.ScheduledArtifactEnabled(m, "cn_html") || !c.ScheduledProbeEnabled(m) {
		t.Fatal("legacy groups without a group-owned plan must preserve their old model range")
	}
	c.IntelligenceSchedule = &IntelligenceSchedule{Enabled: true, ModelScope: "all", ModelID: "representative", TemplateIDs: []string{"cn_html"}}
	if !c.ScheduledArtifactEnabled(m, "cn_html") || c.ScheduledArtifactEnabled(m, "en_svg") {
		t.Fatal("all scope must apply only its selected templates")
	}
	c.IntelligenceSchedule.ModelScope = "selected"
	if c.ScheduledArtifactEnabled(m, "cn_html") {
		t.Fatal("selected scope compatibility was lost")
	}
	c.ModelControls = nil
	c.IntelligenceSchedule = nil
	if c.ScheduledArtifactEnabled(m, "cn_html") {
		t.Fatal("legacy representative-only scheduling must remain unchanged")
	}
}

func TestSelectedScopeLimitsBothAutomaticKindsAndWaitsForTarget(t *testing.T) {
	controls := DefaultModelControls()
	c := Channel{ModelControls: &controls, IntelligenceSchedule: &IntelligenceSchedule{Enabled: true, ModelScope: "selected", TemplateIDs: []string{"cn_html"}}}
	model := Model{ID: "chosen", ProbeEnabled: true, ArtifactChinese: true}
	if c.ScheduledProbeEnabled(model) || c.ScheduledArtifactEnabled(model, "cn_html") {
		t.Fatal("unresolved target allowed automatic requests")
	}
	c.IntelligenceSchedule.ModelID = model.ID
	if !c.ScheduledProbeEnabled(model) || !c.ScheduledArtifactEnabled(model, "cn_html") {
		t.Fatal("selected model was not eligible for both automatic kinds")
	}
	other := model
	other.ID = "other"
	if c.ScheduledProbeEnabled(other) || c.ScheduledArtifactEnabled(other, "cn_html") {
		t.Fatal("unselected model allowed automatic requests")
	}
	c.IntelligenceSchedule.Enabled = false
	if !c.ScheduledProbeEnabled(model) || c.ScheduledArtifactEnabled(model, "cn_html") {
		t.Fatal("creative schedule and health-monitoring switches must stay independent")
	}
}
