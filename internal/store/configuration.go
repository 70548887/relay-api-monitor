package store

import (
	"context"
	"database/sql"

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
