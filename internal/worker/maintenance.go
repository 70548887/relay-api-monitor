package worker

import (
	"context"
	"time"

	"relaylab/internal/domain"
	"relaylab/internal/runtimecontrol"
)

func (w *Worker) startMaintenance(ctx context.Context, now time.Time) {
	w.mu.Lock()
	if w.syncBusy || now.Before(w.nextMaintenance) || runtimecontrol.Paused(w.maintenanceFile) {
		w.mu.Unlock()
		return
	}
	w.syncBusy = true
	w.nextMaintenance = now.Add(time.Minute)
	w.mu.Unlock()
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		defer func() { w.mu.Lock(); w.syncBusy = false; w.mu.Unlock() }()
		w.maintenance(ctx, now)
	}()
}
func (w *Worker) maintenance(ctx context.Context, now time.Time) {
	if ctx.Err() != nil || runtimecontrol.Paused(w.maintenanceFile) {
		return
	}
	if !now.Before(w.nextCleanup) {
		if err := w.st.Cleanup(ctx, now); err == nil {
			w.nextCleanup = now.Add(time.Hour)
		} else {
			_ = w.st.AddAudit(ctx, domain.AuditEvent{Action: "cleanup_failed", Detail: "历史数据清理失败"})
		}
	}
	settings, err := w.st.GetSettings(ctx)
	if err != nil {
		return
	}
	timeout := settings.DiscoveryTimeoutSeconds
	if timeout <= 0 {
		timeout = 20
	}
	channels, err := w.st.ListChannels(ctx)
	if err != nil {
		return
	}
	for _, c := range channels {
		if ctx.Err() != nil || runtimecontrol.Paused(w.maintenanceFile) {
			return
		}
		if w.syncRevision[c.ID] != c.Revision {
			delete(w.nextSync, c.ID)
			w.syncRevision[c.ID] = c.Revision
		}
		if !c.Enabled || c.Archived || now.Before(w.nextSync[c.ID]) {
			continue
		}
		if c.LastSyncAt != nil && now.Sub(*c.LastSyncAt) < 24*time.Hour {
			continue
		}
		p, err := w.st.GetPlatform(ctx, c.PlatformID)
		if err != nil || p.Archived {
			continue
		}
		if !w.reserveDiscovery(c.ID, normalized(settings).ChannelConcurrency) {
			continue
		}
		w.nextSync[c.ID] = now.Add(time.Hour)
		key, err := w.vault.Decrypt(c.EncryptedKey)
		if err != nil {
			w.releaseDiscovery(c.ID)
			w.syncFailure(ctx, c.ID, "平台密钥解密失败", c.Revision)
			continue
		}
		child, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
		ids, err := w.client.ListModels(child, configFor(c, key, c.Protocol, c.GeneratePath))
		cancel()
		w.releaseDiscovery(c.ID)
		if err != nil {
			_, message := errorDetails(err)
			w.syncFailure(ctx, c.ID, message, c.Revision)
			continue
		}
		if err = w.st.SyncModels(ctx, c.ID, ids, c.Revision); err != nil {
			w.syncFailure(ctx, c.ID, "模型同步保存失败", c.Revision)
			continue
		}
		w.nextSync[c.ID] = now.Add(24 * time.Hour)
	}
}
func (w *Worker) syncFailure(ctx context.Context, channelID, message string, revision int) {
	if ctx.Err() != nil {
		return
	}
	if err := w.st.SetChannelSyncError(ctx, channelID, message, revision); err != nil {
		return
	}
	_ = w.st.AddAudit(ctx, domain.AuditEvent{Action: "model_sync_failed", EntityID: channelID, Detail: message})
}
func (w *Worker) reserveDiscovery(channelID string, limit int) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := w.maintenanceChannels[channelID]
	for _, a := range w.active {
		if a.channelID == channelID {
			n++
		}
	}
	if n >= limit {
		return false
	}
	w.maintenanceChannels[channelID]++
	return true
}
func (w *Worker) releaseDiscovery(channelID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.maintenanceChannels[channelID]--
	if w.maintenanceChannels[channelID] <= 0 {
		delete(w.maintenanceChannels, channelID)
	}
}
