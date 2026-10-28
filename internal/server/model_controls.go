package server

import "relaylab/internal/domain"

// Optional wire flags distinguish an omitted default from an explicit false.
type modelWriteRequest struct {
	domain.Model
	Public          *bool     `json:"public"`
	ProbeEnabled    *bool     `json:"probe_enabled"`
	ArtifactChinese *bool     `json:"artifact_chinese"`
	ArtifactEnglish *bool     `json:"artifact_english"`
	ControlChanges  *[]string `json:"model_control_changes"`
}

func boolOr(value *bool, fallback bool) bool {
	if value != nil {
		return *value
	}
	return fallback
}

func cloneArtifactOverrides(value *domain.ArtifactOverrides) *domain.ArtifactOverrides {
	if value == nil {
		return nil
	}
	copy := *value
	if value.Chinese != nil {
		v := *value.Chinese
		copy.Chinese = &v
	}
	if value.English != nil {
		v := *value.English
		copy.English = &v
	}
	return &copy
}

func modelControlChanges(request modelWriteRequest) (map[string]bool, bool) {
	changes := map[string]bool{}
	if request.ControlChanges == nil {
		return changes, true
	}
	flags := map[string]*bool{"public": request.Public, "probe_enabled": request.ProbeEnabled, "artifact_chinese": request.ArtifactChinese, "artifact_english": request.ArtifactEnglish}
	for _, name := range *request.ControlChanges {
		if flags[name] == nil || changes[name] {
			return nil, false
		}
		changes[name] = true
	}
	return changes, true
}

func resolveModelControls(request modelWriteRequest, channel domain.Channel, old *domain.Model) domain.Model {
	model := request.Model
	defaults := domain.DefaultModelControls()
	if channel.ModelControls != nil {
		defaults = *channel.ModelControls
	}
	model.Public = boolOr(request.Public, channel.Public)
	model.ProbeEnabled = boolOr(request.ProbeEnabled, defaults.ProbeEnabled)
	model.ArtifactChinese = boolOr(request.ArtifactChinese, defaults.ArtifactChinese)
	model.ArtifactEnglish = boolOr(request.ArtifactEnglish, defaults.ArtifactEnglish)
	model.ArtifactOverrides = nil
	if old == nil {
		return model
	}
	model.ArtifactOverrides = cloneArtifactOverrides(old.ArtifactOverrides)
	changes, _ := modelControlChanges(request)
	changed := func(name string) bool { return request.ControlChanges == nil || changes[name] }
	if request.Public == nil || !changed("public") {
		model.Public = old.Public
	}
	if request.ProbeEnabled == nil || !changed("probe_enabled") {
		model.ProbeEnabled = old.ProbeEnabled
	}
	if request.ArtifactChinese == nil || !changed("artifact_chinese") {
		model.ArtifactChinese = old.ArtifactChinese
	}
	if request.ArtifactEnglish == nil || !changed("artifact_english") {
		model.ArtifactEnglish = old.ArtifactEnglish
	}
	if channel.IntelligenceSchedule != nil {
		setOverride := func(template string, desired bool) {
			if channel.ScheduledArtifactEnabled(*old, template) == desired {
				return
			}
			if model.ArtifactOverrides == nil {
				model.ArtifactOverrides = &domain.ArtifactOverrides{}
			}
			value := desired
			if template == "cn_html" {
				model.ArtifactOverrides.Chinese = &value
			} else {
				model.ArtifactOverrides.English = &value
			}
		}
		if request.ArtifactChinese != nil && changed("artifact_chinese") {
			setOverride("cn_html", model.ArtifactChinese)
		}
		if request.ArtifactEnglish != nil && changed("artifact_english") {
			setOverride("en_svg", model.ArtifactEnglish)
		}
	}
	return model
}
