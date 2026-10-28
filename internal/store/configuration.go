package store

import (
	"context"
	"database/sql"
	"errors"
	"reflect"

	"relaylab/internal/domain"
)

// SaveModelWithSchedules atomically updates a model and its complete schedule set.
func (s *Store) SaveModelWithSchedules(ctx context.Context, m domain.Model) error {
	m.ID = nonemptyID(m.ID)
	m.CreatedAt = created(m.CreatedAt)
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if err := saveModel(ctx, tx, m); err != nil {
			return err
		}
		c, err := channel(ctx, tx, m.ChannelID)
		if err != nil {
			return err
		}
		changed, err := bindSelectedModel(ctx, tx, c, false)
		if err != nil {
			return err
		}
		if changed {
			if err = saveChannel(ctx, tx, *c); err != nil {
				return err
			}
			return reconcileChannelModels(ctx, tx, c.ID)
		}
		return reconcileSchedules(ctx, tx, m)
	})
}

// SaveSettingsAndReconcile keeps the defaults and every dependent schedule in
// one transaction; a canceled request or failed schedule write commits neither.
func (s *Store) SaveSettingsAndReconcile(ctx context.Context, v domain.Settings) error {
	v = normalizedSettings(v)
	b, err := data(v)
	if err != nil {
		return err
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO settings(id,data) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data", b); err != nil {
			return err
		}
		models, err := list[domain.Model](ctx, tx, "SELECT data FROM models ORDER BY id")
		if err != nil {
			return err
		}
		for _, m := range models {
			if err = reconcileSchedules(ctx, tx, m); err != nil {
				return err
			}
		}
		return nil
	})
}

// SaveChannelWithSchedules has the same revision and secret-preservation rules
// as SaveChannel and atomically refreshes every model using this channel.
func (s *Store) SaveChannelWithSchedules(ctx context.Context, c domain.Channel) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		saved, err := saveChannelConfiguration(ctx, tx, c)
		if err != nil {
			return err
		}
		models, err := list[domain.Model](ctx, tx, "SELECT data FROM models WHERE channel_id=? ORDER BY id", saved.ID)
		if err != nil {
			return err
		}
		for _, m := range models {
			if err = reconcileSchedules(ctx, tx, m); err != nil {
				return err
			}
		}
		return nil
	})
}

type modelControlChanges struct {
	public, probe, chinese, english bool
}

func (changes modelControlChanges) apply(m *domain.Model, c domain.Channel) {
	if changes.public {
		m.Public = c.Public
	}
	if c.ModelControls != nil {
		if changes.probe {
			m.ProbeEnabled = c.ModelControls.ProbeEnabled
		}
		if changes.chinese {
			m.ArtifactChinese = c.ModelControls.ArtifactChinese
		}
		if changes.english {
			m.ArtifactEnglish = c.ModelControls.ArtifactEnglish
		}
	}
	if m.ArtifactOverrides != nil {
		copy := *m.ArtifactOverrides
		if changes.chinese {
			copy.Chinese = nil
		}
		if changes.english {
			copy.English = nil
		}
		m.ArtifactOverrides = &copy
		if copy.Chinese == nil && copy.English == nil {
			m.ArtifactOverrides = nil
		}
	}
}

// SaveChannelWithModelControls applies only changed group switches, unless the
// administrator explicitly requests a full reset. Existing per-model choices
// therefore survive discovery and ordinary saves of an unchanged group form.
func (s *Store) SaveChannelWithModelControls(ctx context.Context, c domain.Channel, applyAll bool, selectedModelOnly ...bool) error {
	// The editor explicitly sends its single-model selection intent. Older
	// callers omit it and retain their existing scheduling compatibility.
	selectedOnly := len(selectedModelOnly) > 0 && selectedModelOnly[0]
	return s.transaction(ctx, func(tx *sql.Tx) error {
		old, err := channel(ctx, tx, c.ID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if c.ModelControls == nil && old != nil {
			c.ModelControls = old.ModelControls
		}
		if c.ModelControls == nil && applyAll {
			controls := domain.DefaultModelControls()
			c.ModelControls = &controls
		}
		initialize := c.ModelControls != nil && (old == nil || old.ModelControls == nil)
		adoptLegacy := old != nil && old.ModelControls == nil && c.ModelControls != nil
		baseline := domain.DefaultModelControls()
		if old != nil && old.ModelControls != nil {
			baseline = *old.ModelControls
		} else if old != nil && old.IntelligenceSchedule != nil {
			baseline.ArtifactChinese, baseline.ArtifactEnglish = false, false
			if old.IntelligenceSchedule.Enabled {
				for _, template := range old.IntelligenceSchedule.TemplateIDs {
					if template == "cn_html" {
						baseline.ArtifactChinese = true
					}
					if template == "en_svg" {
						baseline.ArtifactEnglish = true
					}
				}
			}
		}
		changes := modelControlChanges{public: applyAll || old == nil || old.Public != c.Public}
		if c.ModelControls != nil {
			changes.probe = applyAll || old == nil || baseline.ProbeEnabled != c.ModelControls.ProbeEnabled
			changes.chinese = applyAll || old == nil || baseline.ArtifactChinese != c.ModelControls.ArtifactChinese
			changes.english = applyAll || old == nil || baseline.ArtifactEnglish != c.ModelControls.ArtifactEnglish
		}
		saved, err := saveChannelConfiguration(ctx, tx, c)
		if err != nil {
			return err
		}
		if old != nil {
			// Group visibility and switches do not alter an upstream request.
			// Keep already queued manual requests valid for these changes alone.
			comparison := saved
			comparison.Public, comparison.ModelControls, comparison.IntelligenceModelID = old.Public, old.ModelControls, old.IntelligenceModelID
			comparison.Revision, comparison.GroupVerificationRevision = old.Revision, old.GroupVerificationRevision
			if reflect.DeepEqual(comparison, *old) {
				saved.Revision, saved.GroupVerificationRevision = old.Revision, old.GroupVerificationRevision
			}
		}
		if saved.ModelControls != nil && (initialize || changes.chinese || changes.english) {
			plan := domain.IntelligenceSchedule{ModelScope: "selected"}
			if saved.IntelligenceSchedule != nil {
				plan = *saved.IntelligenceSchedule
			}
			if plan.IntervalSeconds <= 0 {
				cfg, err := load[domain.Settings](ctx, tx, "SELECT data FROM settings WHERE id=1")
				if err != nil {
					return err
				}
				plan.IntervalSeconds = cfg.ArtifactIntervalSeconds
				if plan.IntervalSeconds <= 0 {
					plan.IntervalSeconds = 1800
				}
			}
			plan.TemplateIDs = []string{}
			if saved.ModelControls.ArtifactChinese {
				plan.TemplateIDs = append(plan.TemplateIDs, "cn_html")
			}
			if saved.ModelControls.ArtifactEnglish {
				plan.TemplateIDs = append(plan.TemplateIDs, "en_svg")
			}
			plan.Enabled = len(plan.TemplateIDs) > 0
			saved.IntelligenceSchedule = &plan
		}
		if selectedOnly && saved.IntelligenceSchedule == nil {
			saved.IntelligenceSchedule = &domain.IntelligenceSchedule{ModelScope: "selected", IntervalSeconds: 1800}
		}
		selectionChanged := old != nil && saved.IntelligenceModelID != old.IntelligenceModelID
		applySelection := selectionChanged || selectedOnly
		preservePreviousTarget := applySelection && old != nil && saved.IntelligenceModelID != "" &&
			(saved.IntelligenceModelID == old.IntelligenceModelID || old.IntelligenceSchedule != nil && saved.IntelligenceModelID == old.IntelligenceSchedule.ModelID)
		if applySelection && saved.IntelligenceSchedule != nil {
			plan := *saved.IntelligenceSchedule
			plan.ModelScope, plan.ModelID = "selected", ""
			if preservePreviousTarget {
				plan.ModelID = saved.IntelligenceModelID
			}
			saved.IntelligenceSchedule = &plan
		}
		if _, err = bindSelectedModel(ctx, tx, &saved, applySelection && !preservePreviousTarget); err != nil {
			return err
		}
		if err = saveChannel(ctx, tx, saved); err != nil {
			return err
		}
		models, err := list[domain.Model](ctx, tx, "SELECT data FROM models WHERE channel_id=? ORDER BY id", saved.ID)
		if err != nil {
			return err
		}
		for _, m := range models {
			if !m.Archived {
				if adoptLegacy && !applyAll {
					// Legacy selected-model plans ignore raw artifact booleans.
					// Materialize their effective state before adopting new master
					// controls, so ordinary edits neither stop a running plan nor
					// activate previously ineligible or individually disabled models.
					m.ArtifactChinese, m.ArtifactEnglish = old.ScheduledArtifactEnabled(m, "cn_html"), old.ScheduledArtifactEnabled(m, "en_svg")
				}
				changes.apply(&m, saved)
				if err = saveModel(ctx, tx, m); err != nil {
					return err
				}
			}
			if err = reconcileSchedules(ctx, tx, m); err != nil {
				return err
			}
		}
		return nil
	})
}
