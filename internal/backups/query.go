package backups

import (
	"context"
	"relaylab/internal/store"
)

type Filter = store.BackupFilter

func (m *Manager) ListFiltered(ctx context.Context, f Filter) ([]Run, error) {
	return m.opts.Store.ListBackupRunsFiltered(ctx, f)
}
