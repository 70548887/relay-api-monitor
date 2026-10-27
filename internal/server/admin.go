package server

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"relaylab/internal/artifact"
	"relaylab/internal/domain"
	"relaylab/internal/security"
	"relaylab/internal/upstream"
)

func (s *Server) platforms(c *gin.Context) {
	v, e := s.Store.ListPlatforms(c.Request.Context())
	if e != nil {
		dbError(c, e)
		return
	}
	if v == nil {
		v = []domain.Platform{}
	}
	c.JSON(200, v)
}
func (s *Server) savePlatform(c *gin.Context) {
	var b domain.Platform
	if !bind(c, &b) {
		return
	}
	if e := security.ValidateName(b.Name); e != nil {
		fail(c, 400, "invalid_name", e.Error())
		return
	}
	if c.Param("id") != "" {
		old, e := s.Store.GetPlatform(c.Request.Context(), c.Param("id"))
		if e != nil {
			dbError(c, e)
			return
		}
		b.ID = old.ID
		b.CreatedAt = old.CreatedAt
	} else {
		b.ID = domain.NewID()
		b.CreatedAt = time.Now().UTC()
	}
	if e := s.Store.SavePlatform(c.Request.Context(), b); e != nil {
		dbError(c, e)
		return
	}
	s.auditEvent(c.Request.Context(), "platform_saved", b.ID, b.Name)
	c.JSON(200, b)
}
func (s *Server) archivePlatform(c *gin.Context) {
	ctx := c.Request.Context()
	p, e := s.Store.GetPlatform(ctx, c.Param("id"))
	if e != nil {
		dbError(c, e)
		return
	}
	p.Archived = true
	p.Public = false
	if e = s.Store.SavePlatform(ctx, *p); e != nil {
		dbError(c, e)
		return
	}
	chs, e := s.Store.ListChannels(ctx, p.ID)
	if e != nil {
		dbError(c, e)
		return
	}
	for _, ch := range chs {
		if e = s.disableChannel(ctx, ch); e != nil {
			dbError(c, e)
			return
		}
	}
	s.auditEvent(ctx, "platform_archived", p.ID, p.Name)
	c.JSON(200, gin.H{"ok": true})
}
func (s *Server) channels(c *gin.Context) {
	v, e := s.Store.ListChannels(c.Request.Context())
	if e != nil {
		dbError(c, e)
		return
	}
	if v == nil {
		v = []domain.Channel{}
	}
	c.JSON(200, v)
}
func (s *Server) saveChannel(c *gin.Context) {
	var b struct {
		domain.Channel
		APIKey string `json:"api_key"`
	}
	if !bind(c, &b) {
		return
	}
	b.ReasoningEffort = strings.ToLower(strings.TrimSpace(b.ReasoningEffort))
	b.ThinkingMode = strings.ToLower(strings.TrimSpace(b.ThinkingMode))
	if security.ValidateName(b.Name) != nil || b.PlatformID == "" || !safeProtocol(b.Protocol) {
		fail(c, 400, "invalid_channel", "请填写名称、所属平台和有效协议")
		return
	}
	if (b.APIKey != "" && strings.TrimSpace(b.APIKey) == "") || len(b.APIKey) > 8192 || strings.ContainsAny(b.APIKey, "\r\n") || len(b.AnthropicVersion) > 100 || strings.ContainsAny(b.AnthropicVersion, "\r\n") {
		fail(c, 400, "invalid_credentials", "API Key 或 Anthropic Version 格式不正确")
		return
	}
	if _, e := s.Store.GetPlatform(c.Request.Context(), b.PlatformID); e != nil {
		dbError(c, e)
		return
	}
	if b.DiscoveryProtocol == "" {
		b.DiscoveryProtocol = "openai"
	}
	if b.DiscoveryProtocol != "openai" && b.DiscoveryProtocol != "anthropic" {
		fail(c, 400, "invalid_protocol", "模型发现协议不正确")
		return
	}
	if b.AnthropicVersion == "" {
		b.AnthropicVersion = "2023-06-01"
	}
	if b.ChatTokenField == "" {
		b.ChatTokenField = "max_completion_tokens"
	}
	if b.ChatTokenField != "max_completion_tokens" && b.ChatTokenField != "max_tokens" {
		fail(c, 400, "invalid_parameter", "输出上限参数不正确")
		return
	}
	if e := validateBaseURL(b.BaseURL, b.ModelsPath, b.GeneratePath); e != nil {
		fail(c, 400, "invalid_url", e.Error())
		return
	}
	if e := upstream.ValidateReasoning(b.Protocol, b.ReasoningEffort, b.ThinkingMode, b.ThinkingBudget, 131072); e != nil {
		fail(c, 400, "invalid_reasoning", e.Error())
		return
	}
	b.GroupProvider = strings.ToLower(strings.TrimSpace(b.GroupProvider))
	b.GroupPath, b.GroupField = strings.TrimSpace(b.GroupPath), strings.TrimSpace(b.GroupField)
	if e := upstream.ValidateGroupProvider(channelConfig(b.Channel, b.APIKey), b.GroupProvider, b.GroupPath, b.GroupField); e != nil {
		fail(c, 400, "invalid_group_provider", e.Error())
		return
	}
	b.BaseURL = strings.TrimRight(strings.TrimSpace(b.BaseURL), "/")
	ctx := c.Request.Context()
	if id := c.Param("id"); id != "" {
		old, e := s.Store.GetChannel(ctx, id)
		if e != nil {
			dbError(c, e)
			return
		}
		b.ID = old.ID
		b.CreatedAt = old.CreatedAt
		b.Revision = old.Revision + 1
		b.EncryptedKey = old.EncryptedKey
		b.KeyMask = old.KeyMask
		b.LastSyncAt = old.LastSyncAt
		b.SyncError = old.SyncError
		b.VerifiedGroup, b.GroupVerificationStatus = old.VerifiedGroup, old.GroupVerificationStatus
		b.GroupVerifiedAt, b.GroupVerificationError, b.GroupVerificationRevision = old.GroupVerifiedAt, old.GroupVerificationError, old.GroupVerificationRevision
	} else {
		b.ID = domain.NewID()
		b.CreatedAt = time.Now().UTC()
		b.Revision = 1
		b.IntelligenceSchedule = nil
		b.VerifiedGroup, b.GroupVerificationStatus, b.GroupVerificationError = "", "unverified", ""
		b.GroupVerifiedAt, b.GroupVerificationRevision = nil, 0
	}
	if b.IntelligenceModelID != "" {
		m, e := s.Store.GetModel(ctx, b.IntelligenceModelID)
		if e != nil || m.ChannelID != b.ID || m.Archived {
			fail(c, 400, "invalid_intelligence_model", "代表测试模型必须属于当前分组且未归档")
			return
		}
	}
	if b.APIKey != "" {
		cipher, e := s.Vault.Encrypt(b.APIKey)
		if e != nil {
			fail(c, 500, "encryption_failed", "密钥加密失败")
			return
		}
		b.EncryptedKey = cipher
		b.KeyMask = security.Mask(b.APIKey)
	}
	if b.EncryptedKey == "" {
		fail(c, 400, "missing_key", "请填写 API Key")
		return
	}
	if e := s.Store.SaveChannelWithSchedules(ctx, b.Channel); e != nil {
		dbError(c, e)
		return
	}
	if !b.Enabled {
		if e := s.cancelChannel(ctx, b.ID); e != nil {
			dbError(c, e)
			return
		}
	}
	s.auditEvent(ctx, "channel_saved", b.ID, b.Name)
	saved, e := s.Store.GetChannel(ctx, b.ID)
	if e != nil {
		dbError(c, e)
		return
	}
	c.JSON(200, saved)
}
func (s *Server) disableChannel(ctx context.Context, ch domain.Channel) error {
	ch.Archived = true
	ch.Enabled = false
	ch.Public = false
	ch.Revision++
	if e := s.Store.SaveChannelWithSchedules(ctx, ch); e != nil {
		return e
	}
	return s.cancelChannel(ctx, ch.ID)
}
func (s *Server) cancelChannel(ctx context.Context, id string) error {
	return s.Store.CancelJobsForChannel(ctx, id)
}
func (s *Server) archiveChannel(c *gin.Context) {
	ch, e := s.Store.GetChannel(c.Request.Context(), c.Param("id"))
	if e != nil {
		dbError(c, e)
		return
	}
	if e = s.disableChannel(c.Request.Context(), *ch); e != nil {
		dbError(c, e)
		return
	}
	s.auditEvent(c.Request.Context(), "channel_archived", ch.ID, ch.Name)
	c.JSON(200, gin.H{"ok": true})
}
func channelConfig(ch domain.Channel, key string) upstream.Config {
	return upstream.Config{BaseURL: ch.BaseURL, APIKey: key, Protocol: ch.Protocol, DiscoveryProtocol: ch.DiscoveryProtocol, ModelsPath: ch.ModelsPath, GeneratePath: ch.GeneratePath, AnthropicVersion: ch.AnthropicVersion, ChatTokenField: ch.ChatTokenField}
}
func (s *Server) syncModels(c *gin.Context) {
	// Clients may POST an empty JSON object. Consume it before writing the
	// potentially large catalogue so closing connections do not discard the
	// response while unread request bytes remain in the socket.
	if c.Request.ContentLength != 0 {
		var body struct{}
		if !bind(c, &body) {
			return
		}
	}
	ctx := c.Request.Context()
	ch, e := s.Store.GetChannel(ctx, c.Param("id"))
	if e != nil {
		dbError(c, e)
		return
	}
	key, e := s.Vault.Decrypt(ch.EncryptedKey)
	if e != nil {
		fail(c, 500, "decryption_failed", "密钥解密失败，请检查服务器密钥文件")
		return
	}
	settings, e := s.Store.GetSettings(ctx)
	if e != nil {
		dbError(c, e)
		return
	}
	reqctx, cancel := context.WithTimeout(ctx, time.Duration(settings.DiscoveryTimeoutSeconds)*time.Second)
	defer cancel()
	ids, e := s.Client.ListModels(reqctx, channelConfig(*ch, key))
	if e != nil {
		ch.SyncError = publicError(e)
		if updateErr := s.Store.SetChannelSyncError(ctx, ch.ID, ch.SyncError, ch.Revision); updateErr != nil {
			dbError(c, updateErr)
			return
		}
		fail(c, 502, "upstream_error", ch.SyncError)
		return
	}
	if e = s.Store.SyncModels(ctx, ch.ID, ids, ch.Revision); e != nil {
		dbError(c, e)
		return
	}
	models, e := s.Store.ListModels(ctx, ch.ID)
	if e != nil {
		dbError(c, e)
		return
	}
	s.auditEvent(ctx, "models_synced", ch.ID, "模型目录同步完成")
	c.JSON(200, gin.H{"count": len(ids), "models": models})
}
func (s *Server) models(c *gin.Context) {
	v, e := s.Store.ListModels(c.Request.Context())
	if e != nil {
		dbError(c, e)
		return
	}
	if v == nil {
		v = []domain.Model{}
	}
	c.JSON(200, v)
}
func (s *Server) saveModel(c *gin.Context) {
	var b domain.Model
	if !bind(c, &b) {
		return
	}
	b.ReasoningEffort = strings.ToLower(strings.TrimSpace(b.ReasoningEffort))
	b.ThinkingMode = strings.ToLower(strings.TrimSpace(b.ThinkingMode))
	if b.ChannelID == "" || strings.TrimSpace(b.UpstreamID) == "" || len(b.UpstreamID) > 256 {
		fail(c, 400, "invalid_model", "请填写分组和模型 ID")
		return
	}
	if b.Protocol != "" && !safeProtocol(b.Protocol) {
		fail(c, 400, "invalid_protocol", "协议不正确")
		return
	}
	if b.MaxTokens < 0 || b.MaxTokens > 131072 || b.TimeoutSeconds < 0 || b.TimeoutSeconds > 600 || b.ProbeIntervalSeconds < 0 || b.ArtifactIntervalSeconds < 0 || (b.ProbeIntervalSeconds > 0 && b.ProbeIntervalSeconds < 30) || (b.ArtifactIntervalSeconds > 0 && b.ArtifactIntervalSeconds < 60) {
		fail(c, 400, "invalid_limits", "模型参数超出允许范围")
		return
	}
	if b.GeneratePath != "" {
		if e := validateBaseURL("https://example.com/v1", "", b.GeneratePath); e != nil {
			fail(c, 400, "invalid_path", e.Error())
			return
		}
	}
	ctx := c.Request.Context()
	ch, e := s.Store.GetChannel(ctx, b.ChannelID)
	if e != nil {
		dbError(c, e)
		return
	}
	p := b.Protocol
	if p == "" {
		p = ch.Protocol
	}
	maxTokens := b.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 8192
	}
	effort, mode, budget := upstream.ResolveReasoning(b, *ch)
	if e = upstream.ValidateReasoning(p, effort, mode, budget, maxTokens); e != nil {
		fail(c, 400, "invalid_reasoning", e.Error())
		return
	}
	if b.ThinkingBudget < 0 || (b.ThinkingBudget > 0 && b.ThinkingMode != "" && b.ThinkingMode != "enabled") {
		fail(c, 400, "invalid_reasoning", "思考预算仅适用于 enabled 模式")
		return
	}
	if id := c.Param("id"); id != "" {
		old, e := s.Store.GetModel(ctx, id)
		if e != nil {
			dbError(c, e)
			return
		}
		if old.ChannelID != b.ChannelID || old.UpstreamID != b.UpstreamID {
			fail(c, 400, "immutable_model", "模型所属分组和上游 ID 不可修改，请新增模型")
			return
		}
		b.ID = old.ID
		b.CreatedAt = old.CreatedAt
		b.Source = old.Source
		b.Missing = old.Missing
	} else {
		b.ID = domain.NewID()
		b.CreatedAt = time.Now().UTC()
		b.Source = "manual"
	}
	if b.DisplayName == "" {
		b.DisplayName = b.UpstreamID
	}
	if (b.ProbeEnabled || b.ArtifactChinese || b.ArtifactEnglish) && (!ch.Enabled || ch.Archived) {
		fail(c, 400, "channel_disabled", "请先启用所属分组")
		return
	}
	if e = s.Store.SaveModelWithSchedules(ctx, b); e != nil {
		dbError(c, e)
		return
	}
	s.auditEvent(ctx, "model_saved", b.ID, b.UpstreamID)
	c.JSON(200, b)
}
func (s *Server) archiveModel(c *gin.Context) {
	ctx := c.Request.Context()
	m, e := s.Store.GetModel(ctx, c.Param("id"))
	if e != nil {
		dbError(c, e)
		return
	}
	m.Archived = true
	m.Public = false
	m.ProbeEnabled = false
	m.ArtifactChinese = false
	m.ArtifactEnglish = false
	if e = s.Store.SaveModelWithSchedules(ctx, *m); e != nil {
		dbError(c, e)
		return
	}
	if e = s.Store.CancelJobsForModel(ctx, m.ID); e != nil {
		dbError(c, e)
		return
	}
	c.JSON(200, gin.H{"ok": true})
}
func (s *Server) templates(c *gin.Context) {
	v, e := s.Store.ListTemplates(c.Request.Context())
	if e != nil {
		dbError(c, e)
		return
	}
	c.JSON(200, v)
}
func (s *Server) saveTemplate(c *gin.Context) {
	var b domain.PromptTemplate
	if !bind(c, &b) {
		return
	}
	if strings.TrimSpace(b.Prompt) == "" || len(b.Prompt) > 20000 {
		fail(c, 400, "invalid_prompt", "提示词不能为空或过长")
		return
	}
	old, e := s.Store.GetTemplate(c.Request.Context(), c.Param("id"))
	if e != nil {
		dbError(c, e)
		return
	}
	b.ID = old.ID
	b.Kind = old.Kind
	b.Version = old.Version + 1
	if e = s.Store.SaveTemplate(c.Request.Context(), b); e != nil {
		dbError(c, e)
		return
	}
	c.JSON(200, b)
}
func (s *Server) settings(c *gin.Context) {
	v, e := s.Store.GetSettings(c.Request.Context())
	if e != nil {
		dbError(c, e)
		return
	}
	c.JSON(200, v)
}
func (s *Server) saveSettings(c *gin.Context) {
	var b domain.Settings
	if !bind(c, &b) {
		return
	}
	if b.ArtifactHistoryScope == "" {
		b.ArtifactHistoryScope = "group"
	}
	if b.ArtifactHistoryScope != "group" && b.ArtifactHistoryScope != "model" {
		fail(c, 400, "invalid_settings", "作品历史范围须为 group 或 model")
		return
	}
	if b.OnlineTestRetentionDays == 0 {
		b.OnlineTestRetentionDays = 30
	}
	if b.ProbeConcurrency < 1 || b.ProbeConcurrency > 32 || b.ArtifactConcurrency < 1 || b.ArtifactConcurrency > 16 || b.ChannelConcurrency < 1 || b.ChannelConcurrency > 16 || b.ProbeIntervalSeconds < 30 || b.ProbeIntervalSeconds > 86400 || b.ArtifactIntervalSeconds < 60 || b.ArtifactIntervalSeconds > 604800 || b.ProbeTimeoutSeconds < 5 || b.ProbeTimeoutSeconds > 600 || b.ArtifactTimeoutSeconds < 5 || b.ArtifactTimeoutSeconds > 600 || b.DiscoveryTimeoutSeconds < 1 || b.DiscoveryTimeoutSeconds > 120 || b.OnlineTestRetentionDays < 1 || b.OnlineTestRetentionDays > 365 || b.RetentionDays < 1 || b.RetentionDays > 365 || b.SlowThresholdMS < 100 || b.SlowThresholdMS > 600000 || b.VisitorConcurrency < 1 || b.VisitorConcurrency > 64 || b.VisitorIPConcurrency < 1 || b.VisitorIPConcurrency > 8 || b.VisitorModelsPerMinute < 1 || b.VisitorModelsPerMinute > 120 || b.VisitorGenerationsPerMinute < 1 || b.VisitorGenerationsPerMinute > 120 {
		fail(c, 400, "invalid_settings", "设置超出允许范围")
		return
	}
	ctx := c.Request.Context()
	if e := s.Store.SaveSettingsAndReconcile(ctx, b); e != nil {
		dbError(c, e)
		return
	}
	s.auditEvent(ctx, "settings_saved", "", "更新系统设置")
	c.JSON(200, b)
}
func (s *Server) schedules(c *gin.Context) {
	v, e := s.Store.ListSchedules(c.Request.Context())
	if e != nil {
		dbError(c, e)
		return
	}
	c.JSON(200, v)
}
func (s *Server) jobs(c *gin.Context) {
	n, _ := security.ValidateInt(c.DefaultQuery("limit", "100"), 1, 1000)
	if n == 0 {
		n = 100
	}
	v, e := s.Store.ListJobs(c.Request.Context(), n)
	if e != nil {
		dbError(c, e)
		return
	}
	c.JSON(200, v)
}
func (s *Server) enqueue(c *gin.Context) {
	var b struct {
		ModelID    string `json:"model_id"`
		Kind       string `json:"kind"`
		TemplateID string `json:"template_id"`
	}
	if !bind(c, &b) {
		return
	}
	ctx := c.Request.Context()
	m, e := s.Store.GetModel(ctx, b.ModelID)
	if e != nil {
		dbError(c, e)
		return
	}
	ch, e := s.Store.GetChannel(ctx, m.ChannelID)
	if e != nil {
		dbError(c, e)
		return
	}
	p, e := s.Store.GetPlatform(ctx, ch.PlatformID)
	if e != nil {
		dbError(c, e)
		return
	}
	if m.Archived || m.Missing || ch.Archived || !ch.Enabled || p.Archived {
		fail(c, 400, "disabled", "模型缺失，或模型、分组及平台已停用或归档")
		return
	}
	if b.Kind != "probe" && b.Kind != "artifact" {
		fail(c, 400, "invalid_kind", "任务类型不正确")
		return
	}
	if b.Kind == "artifact" {
		if b.TemplateID == "" {
			b.TemplateID = "cn_html"
		}
		if _, e = s.Store.GetTemplate(ctx, b.TemplateID); e != nil {
			dbError(c, e)
			return
		}
	} else {
		b.TemplateID = ""
	}
	now := time.Now().UTC()
	j := domain.Job{ID: domain.NewID(), ModelID: m.ID, ChannelID: ch.ID, Kind: b.Kind, TemplateID: b.TemplateID, State: "queued", ChannelRevision: ch.Revision, ScheduledAt: now, CreatedAt: now}
	j, e = s.Store.EnqueueJob(ctx, j)
	if e != nil {
		dbError(c, e)
		return
	}
	s.auditEvent(ctx, "job_created", j.ID, b.Kind)
	c.JSON(201, j)
}
func (s *Server) cancelJob(c *gin.Context) {
	if e := s.Store.RequestCancel(c.Request.Context(), c.Param("id")); e != nil {
		dbError(c, e)
		return
	}
	c.JSON(200, gin.H{"ok": true})
}
func (s *Server) job(c *gin.Context) {
	v, e := s.Store.GetJob(c.Request.Context(), c.Param("id"))
	if e != nil {
		dbError(c, e)
		return
	}
	c.JSON(200, v)
}
func (s *Server) overview(c *gin.Context) {
	ctx := c.Request.Context()
	wh, e := s.Store.WorkerHealth(ctx)
	if e != nil {
		dbError(c, e)
		return
	}
	js, e := s.Store.ListJobs(ctx, 20)
	if e != nil {
		dbError(c, e)
		return
	}
	stateCounts, e := s.Store.JobStateCounts(ctx)
	if e != nil {
		dbError(c, e)
		return
	}
	counts := gin.H{"queued": stateCounts["queued"], "running": stateCounts["claimed"] + stateCounts["dispatching"], "failed": stateCounts["failed"], "uncertain": stateCounts["uncertain"]}
	c.JSON(200, gin.H{"worker": wh, "counts": counts, "recent_jobs": js})
}
func (s *Server) adminArtifacts(c *gin.Context) {
	v, e := s.Store.ListArtifactsSummary(c.Request.Context(), "")
	if e != nil {
		dbError(c, e)
		return
	}
	out := []map[string]any{}
	for _, a := range v {
		out = append(out, artifactMap(a, false))
	}
	c.JSON(200, out)
}
func artifactMap(a domain.Artifact, full bool) map[string]any {
	b, _ := json.Marshal(a)
	v := map[string]any{}
	_ = json.Unmarshal(b, &v)
	if !full {
		delete(v, "code")
		delete(v, "raw_response")
	}
	return v
}
func (s *Server) adminArtifact(c *gin.Context) {
	a, e := s.Store.GetArtifact(c.Request.Context(), c.Param("id"))
	if e != nil {
		dbError(c, e)
		return
	}
	v := artifactMap(*a, true)
	v["preview_document"] = artifact.Preview(a.Code, a.Kind)
	c.JSON(200, v)
}
func (s *Server) hideArtifact(c *gin.Context) {
	var b struct {
		Hidden bool `json:"hidden"`
	}
	if !bind(c, &b) {
		return
	}
	if e := s.Store.SetArtifactHidden(c.Request.Context(), c.Param("id"), b.Hidden); e != nil {
		dbError(c, e)
		return
	}
	c.JSON(200, gin.H{"ok": true})
}
func (s *Server) deleteArtifact(c *gin.Context) {
	if e := s.Store.DeleteArtifact(c.Request.Context(), c.Param("id")); e != nil {
		dbError(c, e)
		return
	}
	s.auditEvent(c.Request.Context(), "artifact_deleted", c.Param("id"), "删除生成作品")
	c.JSON(200, gin.H{"ok": true})
}
func (s *Server) audit(c *gin.Context) {
	v, e := s.Store.ListAudit(c.Request.Context(), 100)
	if e != nil {
		dbError(c, e)
		return
	}
	c.JSON(200, v)
}
