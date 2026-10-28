package store

import (
	"context"
	"database/sql"
	"errors"

	"relaylab/internal/domain"
)

const defaultTestModel = "gpt-6.1-sol"

// bindSelectedModel never falls back to a different upstream model. An empty
// target is a valid waiting configuration until discovery finds the default.
// Existing selected targets survive discovery; an explicit editor change can
// reset that selection to the exact default by clearing IntelligenceModelID.
func bindSelectedModel(ctx context.Context, q queryer, c *domain.Channel, reset bool) (bool, error) {
	if c.IntelligenceSchedule == nil || c.IntelligenceSchedule.ModelScope == "all" {
		return false, nil
	}
	plan := *c.IntelligenceSchedule
	before := plan.ModelID
	plan.ModelScope = "selected"
	switch {
	case !reset && plan.ModelID != "":
		// Missing or archived targets stay selected and are stopped by normal
		// eligibility checks; another model must not silently take their place.
	case c.IntelligenceModelID != "":
		model, err := load[domain.Model](ctx, q, "SELECT data FROM models WHERE id=?", c.IntelligenceModelID)
		if err != nil || model.ChannelID != c.ID {
			return false, ErrInvalidIntelligenceSchedule
		}
		plan.ModelID = c.IntelligenceModelID
	default:
		model, err := load[domain.Model](ctx, q, "SELECT data FROM models WHERE channel_id=? AND upstream_id=?", c.ID, defaultTestModel)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
		plan.ModelID = ""
		if err == nil && !model.Archived && !model.Missing {
			plan.ModelID = model.ID
		}
	}
	c.IntelligenceSchedule = &plan
	return before != plan.ModelID, nil
}

func reconcileChannelModels(ctx context.Context, q queryer, channelID string) error {
	models, err := list[domain.Model](ctx, q, "SELECT data FROM models WHERE channel_id=? ORDER BY id", channelID)
	if err != nil {
		return err
	}
	for _, model := range models {
		if err = reconcileSchedules(ctx, q, model); err != nil {
			return err
		}
	}
	return nil
}
