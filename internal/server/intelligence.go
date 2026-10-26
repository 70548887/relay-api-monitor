package server

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
	"relaylab/internal/domain"
	"relaylab/internal/upstream"
)

func (s *Server) verifyGroup(c *gin.Context) {
	ctx := c.Request.Context()
	ch, err := s.Store.GetChannel(ctx, c.Param("id"))
	if err != nil {
		dbError(c, err)
		return
	}
	key, err := s.Vault.Decrypt(ch.EncryptedKey)
	if err != nil {
		fail(c, 500, "decryption_failed", "密钥解密失败，请检查服务器密钥文件")
		return
	}
	settings, err := s.Store.GetSettings(ctx)
	if err != nil {
		dbError(c, err)
		return
	}
	timeout := settings.DiscoveryTimeoutSeconds
	if timeout <= 0 {
		timeout = 20
	}
	checkctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	v, checkErr := s.Client.VerifyGroup(checkctx, channelConfig(*ch, key), ch.GroupProvider, ch.GroupPath, ch.GroupField)
	if checkErr != nil && v.Status != "failed" {
		v.Status, v.Message = "failed", "分组验证请求失败"
	}
	if err = s.Store.SetGroupVerification(ctx, ch.ID, ch.Revision, v.Status, v.Group, ch.GroupProvider, v.Message, v.CheckedAt); err != nil {
		dbError(c, err)
		return
	}
	s.auditEvent(ctx, "group_verified", ch.ID, v.Status)
	c.JSON(200, gin.H{"status": v.Status, "group": v.Group, "message": v.Message, "provider": v.Provider, "checked_at": v.CheckedAt, "scope": "token_binding"})
}

func groupVerification(ch domain.Channel) gin.H {
	status, group, checkedAt := ch.GroupVerificationStatus, ch.VerifiedGroup, ch.GroupVerifiedAt
	message := ""
	if status == "" {
		status = "unverified"
	}
	if ch.GroupVerificationRevision != ch.Revision {
		status, group, checkedAt = "unverified", "", nil
	}
	switch status {
	case "verified":
		message = "已验证 Token 绑定分组；实际模型路由尚未确认"
		if group == "auto" {
			message = "Token 绑定分组为 auto；实际命中分组尚未确认"
		}
	case "unsupported":
		message = "提供方未提供可用的分组验证信息"
	case "failed":
		message = "分组元信息验证失败；此状态不代表模型 Key 不可用"
	default:
		status, group, message = "unverified", "", "尚未验证 Token 绑定分组"
	}
	return gin.H{"status": status, "group": group, "checked_at": checkedAt, "message": message, "provider": ch.GroupProvider, "scope": "token_binding"}
}

func modelProtocol(m domain.Model, ch domain.Channel) string {
	if m.Protocol != "" {
		return m.Protocol
	}
	if ch.Protocol != "" {
		return ch.Protocol
	}
	return "chat"
}

func (s *Server) intelligence(c *gin.Context) {
	ctx := c.Request.Context()
	catalog, err := s.publicCatalog(ctx)
	if err != nil {
		dbError(c, err)
		return
	}
	templateID := c.DefaultQuery("template_id", "cn_html")
	if _, err = s.Store.GetTemplate(ctx, templateID); err != nil {
		dbError(c, err)
		return
	}
	modelID, channelID := c.Query("model_id"), c.Query("channel_id")
	if modelID != "" {
		m, ok := catalog.modelByID[modelID]
		if !ok || (channelID != "" && m.ChannelID != channelID) {
			fail(c, 404, "not_found", "模型不存在")
			return
		}
		channelID = m.ChannelID
	}
	if channelID != "" {
		if _, ok := catalog.channelByID[channelID]; !ok {
			fail(c, 404, "not_found", "分组不存在")
			return
		}
	}
	settings, err := s.Store.GetSettings(ctx)
	if err != nil {
		dbError(c, err)
		return
	}
	scope := settings.ArtifactHistoryScope
	if scope != "model" {
		scope = "group"
	}
	schedules, err := s.Store.ListSchedules(ctx)
	if err != nil {
		dbError(c, err)
		return
	}
	scheduleByModel := map[string]domain.Schedule{}
	for _, item := range schedules {
		if item.Kind == "artifact" && item.TemplateID == templateID {
			scheduleByModel[item.ModelID] = item
		}
	}
	health, err := s.Store.WorkerHealth(ctx)
	if err != nil {
		dbError(c, err)
		return
	}
	groups := []gin.H{}
	for _, ch := range catalog.channels {
		if channelID != "" && ch.ID != channelID {
			continue
		}
		models := []gin.H{}
		candidates := []domain.Model{}
		for _, m := range catalog.models {
			if m.ChannelID != ch.ID {
				continue
			}
			models = append(models, gin.H{"id": m.ID, "upstream_id": m.UpstreamID, "display_name": m.DisplayName, "protocol": modelProtocol(m, ch)})
			candidates = append(candidates, m)
		}
		selected := domain.Model{}
		if modelID != "" {
			selected = catalog.modelByID[modelID]
		}
		if selected.ID == "" && ch.IntelligenceModelID != "" {
			if candidate, ok := catalog.modelByID[ch.IntelligenceModelID]; ok && candidate.ChannelID == ch.ID {
				selected = candidate
			}
		}
		if selected.ID == "" {
			for _, candidate := range candidates {
				enabled := (templateID == "cn_html" && candidate.ArtifactChinese) || (templateID == "en_svg" && candidate.ArtifactEnglish)
				if enabled && !candidate.Missing {
					selected = candidate
					break
				}
			}
		}
		if selected.ID == "" && len(candidates) > 0 {
			selected = candidates[0]
		}
		interval := selected.ArtifactIntervalSeconds
		if interval <= 0 {
			interval = settings.ArtifactIntervalSeconds
		}
		var nextRun *time.Time
		automatic := false
		if schedule, ok := scheduleByModel[selected.ID]; ok {
			interval, automatic = schedule.IntervalSeconds, schedule.Enabled && ch.Enabled && !selected.Missing
			if automatic {
				at := schedule.NextRunAt
				nextRun = &at
			}
		}
		var values []domain.Artifact
		var jobs []domain.Job
		if scope == "group" {
			values, err = s.Store.ListPublicChannelArtifactsSummary(ctx, ch.ID, templateID, 8)
			if err == nil {
				jobs, err = s.Store.LatestPublicChannelArtifactAttempts(ctx, ch.ID, templateID)
			}
		} else if selected.ID != "" {
			values, err = s.Store.ListArtifactsSummary(ctx, selected.ID)
			if err == nil {
				jobs, err = s.Store.LatestArtifactAttempts(ctx, selected.ID, templateID)
			}
		}
		if err != nil {
			dbError(c, err)
			return
		}
		history := []map[string]any{}
		artifactByJob := map[string]domain.Artifact{}
		for _, a := range values {
			if a.TemplateID != templateID || !visibleArtifact(a, catalog) {
				continue
			}
			history = append(history, publicArtifactMap(a, catalog, false))
			artifactByJob[a.JobID] = a
			if len(history) == 8 {
				break
			}
		}
		var latest any
		for _, job := range jobs {
			if _, ok := catalog.modelByID[job.ModelID]; ok && job.ChannelID == ch.ID && job.TemplateID == templateID {
				attempt := publicAttempt(job)
				if a, ok := artifactByJob[job.ID]; ok && job.State == "succeeded" && a.ModelID == job.ModelID && a.ChannelID == job.ChannelID {
					attempt["artifact_id"] = a.ID
					attempt["usage"] = a.Usage
					attempt["first_text_ms"] = a.FirstTextMS
					attempt["duration_ms"] = a.DurationMS
					attempt["finish_reason"] = a.FinishReason
				}
				latest = attempt
				break
			}
		}
		effort, mode, budget := upstream.ResolveReasoning(selected, ch)
		p := catalog.platformByID[ch.PlatformID]
		groups = append(groups, gin.H{"id": ch.ID, "channel_id": ch.ID, "channel_name": ch.Name, "platform_id": p.ID, "platform_name": p.Name, "model_id": selected.ID, "requested_model": selected.UpstreamID, "protocol": modelProtocol(selected, ch), "models": models, "group_verification": groupVerification(ch), "reasoning_effort": effort, "thinking_mode": mode, "thinking_budget": budget, "interval_seconds": interval, "next_run_at": nextRun, "automatic_enabled": automatic, "latest_attempt": latest, "history": history})
	}
	c.JSON(200, gin.H{"groups": groups, "history_scope": scope, "updated_at": time.Now().UTC(), "worker_online": health.Online})
}
