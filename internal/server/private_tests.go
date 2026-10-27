package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"relaylab/internal/artifact"
	"relaylab/internal/domain"
	"relaylab/internal/security"
	"relaylab/internal/store"
)

func memberID(c *gin.Context) string { return c.MustGet("member").(*domain.Member).ID }
func redactPrivate(text, key string) string {
	if key != "" {
		text = strings.ReplaceAll(text, key, "[redacted]")
	}
	return publicBearer.ReplaceAllString(publicKey.ReplaceAllString(text, "[redacted]"), "Bearer [redacted]")
}
func privateHash(v any) string {
	b, _ := json.Marshal(v)
	hash := sha256.Sum256(b)
	return hex.EncodeToString(hash[:])
}
func connectionFingerprint(c domain.UserConnection, key string) string {
	return privateHash([]any{c.UserID, c.BaseURL, c.Protocol, c.DiscoveryProtocol, c.ModelsPath, c.GeneratePath, c.AnthropicVersion, c.ChatTokenField, key})
}
func (s *Server) connections(c *gin.Context) {
	user := ""
	if _, ok := c.Get("member"); ok {
		user = memberID(c)
	}
	items, e := s.Store.ListUserConnections(c.Request.Context(), user)
	if e != nil {
		dbError(c, e)
		return
	}
	c.JSON(200, items)
}
func (s *Server) saveConnection(c *gin.Context) {
	var b struct {
		domain.UserConnection
		APIKey string `json:"api_key"`
	}
	if !bind(c, &b) {
		return
	}
	now := time.Now().UTC()
	user := memberID(c)
	v := b.UserConnection
	v.UserID = user
	v.Username = ""
	v.ID = domain.NewID()
	v.CreatedAt = now
	v.Revision = 1
	v.UpdatedAt = now
	v.Disabled = false
	v.LastModelsAt = nil
	v.LastModelsError = ""
	v.LastModelsCount = 0
	if security.ValidateName(v.Name) != nil {
		fail(c, 400, "invalid_name", "请输入连接名称")
		return
	}
	if v.Protocol == "" {
		v.Protocol = "chat"
	}
	if v.DiscoveryProtocol == "" {
		v.DiscoveryProtocol = "openai"
		if v.Protocol == "anthropic" {
			v.DiscoveryProtocol = "anthropic"
		}
	}
	v.BaseURL = strings.TrimRight(strings.TrimSpace(v.BaseURL), "/")
	if v.AnthropicVersion == "" {
		v.AnthropicVersion = "2023-06-01"
	}
	if v.ChatTokenField == "" {
		v.ChatTokenField = "max_tokens"
	}
	if id := c.Param("id"); id != "" {
		old, e := s.Store.GetUserConnection(c.Request.Context(), user, id)
		if e != nil {
			dbError(c, e)
			return
		}
		if b.Revision != 0 && b.Revision != old.Revision {
			dbError(c, store.ErrConflict)
			return
		}
		v.ID, v.CreatedAt, v.Revision, v.Disabled = old.ID, old.CreatedAt, old.Revision+1, old.Disabled
		v.LastModelsAt, v.LastModelsCount, v.LastModelsError = old.LastModelsAt, old.LastModelsCount, old.LastModelsError
		if b.APIKey == "" {
			oldURL, _ := url.Parse(old.BaseURL)
			newURL, _ := url.Parse(v.BaseURL)
			if oldURL == nil || newURL == nil || oldURL.Scheme != newURL.Scheme || !strings.EqualFold(oldURL.Host, newURL.Host) {
				fail(c, 400, "key_required_for_host_change", "更换接口域名、端口或协议时必须重新输入 Key")
				return
			}
			b.APIKey, e = s.Vault.DecryptUserConnection(user, id, old.EncryptedKey)
			if e != nil {
				fail(c, 500, "decryption_failed", "连接 Key 解密失败")
				return
			}
		}
	}
	defer func() { b.APIKey = "" }()
	check := visitorRequest{BaseURL: v.BaseURL, APIKey: b.APIKey, Protocol: v.Protocol, DiscoveryProtocol: v.DiscoveryProtocol, ModelsPath: v.ModelsPath, GeneratePath: v.GeneratePath, AnthropicVersion: v.AnthropicVersion, ChatTokenField: v.ChatTokenField}
	if !validConnectionFields(c, check) {
		return
	}
	v.CredentialFingerprint = connectionFingerprint(v, b.APIKey)
	v.KeyMask = security.Mask(b.APIKey)
	if c.Param("id") == "" {
		existing, e := s.Store.FindUserConnection(c.Request.Context(), user, v.CredentialFingerprint)
		if e == nil {
			c.JSON(200, existing)
			return
		}
		if !errors.Is(e, sql.ErrNoRows) {
			dbError(c, e)
			return
		}
	}
	cipher, e := s.Vault.EncryptUserConnection(user, v.ID, b.APIKey)
	if e != nil {
		fail(c, 500, "encryption_failed", "连接 Key 保存失败")
		return
	}
	v.EncryptedKey = cipher
	if e = s.Store.SaveUserConnection(c.Request.Context(), v); e != nil {
		if errors.Is(e, store.ErrConnectionExists) {
			fail(c, 409, "connection_exists", "相同接口、协议和 Key 的连接已经存在，请使用已有连接")
			return
		}
		dbError(c, e)
		return
	}
	s.auditEvent(c.Request.Context(), "user_connection_saved", v.ID, "用户连接已保存")
	c.JSON(200, v)
}
func validConnectionFields(c *gin.Context, b visitorRequest) bool {
	if !safeProtocol(b.Protocol) || (b.DiscoveryProtocol != "openai" && b.DiscoveryProtocol != "anthropic") {
		fail(c, 400, "invalid_protocol", "请选择有效接口协议")
		return false
	}
	if b.ChatTokenField != "" && b.ChatTokenField != "max_tokens" && b.ChatTokenField != "max_completion_tokens" {
		fail(c, 400, "invalid_parameter", "输出 Token 字段不正确")
		return false
	}
	return validateVisitor(c, b)
}
func (s *Server) deleteConnection(c *gin.Context) {
	if e := s.Store.DeleteUserConnection(c.Request.Context(), memberID(c), c.Param("id")); e != nil {
		dbError(c, e)
		return
	}
	s.auditEvent(c.Request.Context(), "user_connection_deleted", c.Param("id"), "用户连接已删除")
	c.JSON(200, gin.H{"ok": true})
}
func (s *Server) connectionStatus(c *gin.Context) {
	var b struct {
		Disabled *bool `json:"disabled"`
	}
	if !bind(c, &b) {
		return
	}
	if b.Disabled == nil {
		fail(c, 400, "invalid_status", "请提供 disabled")
		return
	}
	if e := s.Store.SetUserConnectionDisabled(c.Request.Context(), c.Param("id"), *b.Disabled); e != nil {
		dbError(c, e)
		return
	}
	s.auditEvent(c.Request.Context(), "user_connection_status", c.Param("id"), fmt.Sprintf("disabled=%t", *b.Disabled))
	c.JSON(200, gin.H{"ok": true})
}

func (s *Server) resolveTestConnection(c *gin.Context, b *visitorRequest) (*domain.UserConnection, bool) {
	user := memberID(c)
	ctx := c.Request.Context()
	if b.ConnectionID != "" {
		if b.BaseURL != "" || b.APIKey != "" || b.ModelsPath != "" || b.GeneratePath != "" || b.AnthropicVersion != "" || b.ChatTokenField != "" || b.DiscoveryProtocol != "" {
			fail(c, 400, "mixed_connection", "已保存连接不能混用地址、Key 或接口路径配置")
			return nil, false
		}
		v, e := s.Store.GetUserConnection(ctx, user, b.ConnectionID)
		if e != nil {
			dbError(c, e)
			return nil, false
		}
		if v.Disabled {
			fail(c, 403, "connection_disabled", "该连接已被管理员禁用")
			return nil, false
		}
		if b.ConnectionRevision < 1 {
			fail(c, 400, "connection_revision_required", "请刷新连接后确认当前配置")
			return nil, false
		}
		if b.ConnectionRevision != v.Revision {
			fail(c, 409, "connection_changed", "连接配置已变化，请重新确认后测试")
			return nil, false
		}
		key, e := s.Vault.DecryptUserConnection(user, v.ID, v.EncryptedKey)
		if e != nil {
			fail(c, 500, "decryption_failed", "连接 Key 解密失败")
			return nil, false
		}
		b.BaseURL, b.APIKey, b.ModelsPath, b.GeneratePath, b.AnthropicVersion, b.ChatTokenField, b.DiscoveryProtocol = v.BaseURL, key, v.ModelsPath, v.GeneratePath, v.AnthropicVersion, v.ChatTokenField, v.DiscoveryProtocol
		if b.Protocol == "" {
			b.Protocol = v.Protocol
		}
		return v, true
	}
	if b.Protocol == "" {
		b.Protocol = "chat"
	}
	if b.DiscoveryProtocol == "" {
		b.DiscoveryProtocol = "openai"
		if b.Protocol == "anthropic" {
			b.DiscoveryProtocol = "anthropic"
		}
	}
	if !validConnectionFields(c, *b) {
		return nil, false
	}
	now := time.Now().UTC()
	host, _ := url.Parse(b.BaseURL)
	v := domain.UserConnection{ID: domain.NewID(), UserID: user, Name: host.Host, BaseURL: strings.TrimRight(strings.TrimSpace(b.BaseURL), "/"), Protocol: b.Protocol, DiscoveryProtocol: b.DiscoveryProtocol, ModelsPath: b.ModelsPath, GeneratePath: b.GeneratePath, AnthropicVersion: b.AnthropicVersion, ChatTokenField: b.ChatTokenField, KeyMask: security.Mask(b.APIKey), Revision: 1, CreatedAt: now, UpdatedAt: now}
	v.CredentialFingerprint = connectionFingerprint(v, b.APIKey)
	if existing, e := s.Store.FindUserConnection(ctx, user, v.CredentialFingerprint); e == nil {
		if existing.Disabled {
			fail(c, 403, "connection_disabled", "该连接已被管理员禁用")
			return nil, false
		}
		b.ConnectionID = existing.ID
		b.ConnectionRevision = existing.Revision
		return existing, true
	} else if !errors.Is(e, sql.ErrNoRows) {
		dbError(c, e)
		return nil, false
	}
	cipher, e := s.Vault.EncryptUserConnection(user, v.ID, b.APIKey)
	if e != nil {
		fail(c, 500, "encryption_failed", "连接 Key 保存失败")
		return nil, false
	}
	v.EncryptedKey = cipher
	if e = s.Store.SaveUserConnection(ctx, v); e != nil {
		if errors.Is(e, store.ErrConnectionExists) {
			if existing, getErr := s.Store.FindUserConnection(ctx, user, v.CredentialFingerprint); getErr == nil && !existing.Disabled {
				b.ConnectionID = existing.ID
				b.ConnectionRevision = existing.Revision
				return existing, true
			}
		}
		dbError(c, e)
		return nil, false
	}
	b.ConnectionID = v.ID
	b.ConnectionRevision = v.Revision
	return &v, true
}

func historyOwner(c *gin.Context) string {
	if _, ok := c.Get("member"); ok {
		return memberID(c)
	}
	return ""
}
func (s *Server) testRecords(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page > 1000000 {
		fail(c, 400, "invalid_page", "页码过大")
		return
	}
	f := domain.OnlineTestFilter{UserID: historyOwner(c), ConnectionID: c.Query("connection_id"), Model: c.Query("model"), TemplateID: c.Query("template_id"), State: c.Query("state"), Page: page, PageSize: size}
	if f.UserID == "" {
		f.UserID = c.Query("user_id")
	}
	for _, pair := range []struct {
		name string
		dst  *string
	}{{"from", &f.From}, {"to", &f.To}} {
		if raw := c.Query(pair.name); raw != "" {
			t, e := time.Parse(time.RFC3339, raw)
			if e != nil {
				fail(c, 400, "invalid_time", "时间必须使用 ISO 8601 格式")
				return
			}
			*pair.dst = t.UTC().Format(databaseTimeFormat)
		}
	}
	result, e := s.Store.ListOnlineTestRuns(c.Request.Context(), f)
	if e != nil {
		dbError(c, e)
		return
	}
	c.JSON(200, result)
}
func (s *Server) testRecord(c *gin.Context) {
	r, e := s.Store.GetOnlineTestRun(c.Request.Context(), historyOwner(c), c.Param("id"))
	if e != nil {
		dbError(c, e)
		return
	}
	b, _ := json.Marshal(r)
	var out gin.H
	_ = json.Unmarshal(b, &out)
	if r.State == "succeeded" && r.Code != "" {
		out["preview_document"] = artifact.Preview(r.Code, r.Kind)
	}
	c.JSON(200, out)
}
func (s *Server) testRecordSource(c *gin.Context) {
	r, e := s.Store.GetOnlineTestRun(c.Request.Context(), historyOwner(c), c.Param("id"))
	if e != nil {
		dbError(c, e)
		return
	}
	text := r.Text
	extension := "txt"
	if c.Query("format") != "text" && r.Code != "" {
		text = r.Code
		extension = "html"
		if r.Kind == "svg" {
			extension = "svg"
		}
	}
	c.Header("Content-Disposition", "attachment; filename=\"online-test-"+r.ID+"."+extension+"\"")
	c.Data(200, "text/plain; charset=utf-8", []byte(text))
}
func (s *Server) deleteTestRecords(c *gin.Context) {
	ids := []string{c.Param("id")}
	if ids[0] == "" {
		var b struct {
			IDs []string `json:"ids"`
		}
		if !bind(c, &b) {
			return
		}
		ids = b.IDs
	}
	if len(ids) < 1 || len(ids) > 100 {
		fail(c, 400, "invalid_ids", "请选择 1–100 条记录")
		return
	}
	if e := s.Store.DeleteOnlineTestRuns(c.Request.Context(), historyOwner(c), ids); e != nil {
		dbError(c, e)
		return
	}
	s.auditEvent(c.Request.Context(), "online_tests_deleted", "", fmt.Sprintf("删除 %d 条私有测试记录", len(ids)))
	c.JSON(200, gin.H{"ok": true})
}
func (s *Server) cancelTestRecord(c *gin.Context) {
	r, e := s.Store.GetOnlineTestRun(c.Request.Context(), memberID(c), c.Param("id"))
	if e != nil {
		dbError(c, e)
		return
	}
	s.mu.Lock()
	cancel := s.privateCancels[r.ID]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	} else if r.State == "created" || r.State == "running" {
		if e := s.recoverInactivePrivateRuns(c.Request.Context()); e != nil {
			dbError(c, e)
			return
		}
	}
	c.JSON(200, gin.H{"ok": true})
}

// One serve process owns the data directory runtime lock. Recover only its
// predecessors, never a running request in this instance.
func (s *Server) StartPrivateTestMaintenance(ctx context.Context) error {
	if e := s.Store.RecoverOnlineTestRuns(ctx, s.privateProcess, time.Now().UTC()); e != nil {
		return e
	}
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		nextCleanup := time.Time{}
		for {
			if ctx.Err() != nil {
				return
			}
			if leave, ok := s.gate.Enter(); ok {
				maintenance, cancel := context.WithTimeout(ctx, 5*time.Second)
				if err := s.recoverInactivePrivateRuns(maintenance); err != nil {
					log.Print("private history reconciliation failed; will retry")
				}
				if now := time.Now().UTC(); !now.Before(nextCleanup) {
					if err := s.Store.CleanupOnlineTestRuns(maintenance, now); err == nil {
						nextCleanup = now.Add(time.Hour)
					} else {
						log.Print("private history cleanup failed; will retry")
					}
				}
				cancel()
				leave()
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return nil
}

func (s *Server) recoverInactivePrivateRuns(ctx context.Context) error {
	s.privateLifecycle.Lock()
	defer s.privateLifecycle.Unlock()
	s.mu.Lock()
	ids := make([]string, 0, len(s.privateCancels))
	for id := range s.privateCancels {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	return s.Store.RecoverInactiveOnlineTestRuns(ctx, s.privateProcess, ids, time.Now().UTC())
}

func (s *Server) interruptPrivateRun(run domain.OnlineTestRun, message string) error {
	run.State = "interrupted"
	run.Code = ""
	run.Kind = ""
	run.PreviewStatus = "none"
	run.ErrorCode = "history_save_failed"
	run.ErrorMessage = message
	now := time.Now().UTC()
	run.FinishedAt = &now
	run.HeartbeatAt = now
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.Store.UpdateOnlineTestRun(ctx, run)
}

// Version 0 explicitly represents an unmatched/custom prompt. Historical
// versions are trusted only via an owned original record with identical text.
func (s *Server) privateTemplateVersion(c *gin.Context, b visitorRequest) (int, bool) {
	if b.TemplateID == "" {
		return 0, true
	}
	if b.SourceRecordID != "" {
		old, e := s.Store.GetOnlineTestRun(c.Request.Context(), memberID(c), b.SourceRecordID)
		if e != nil {
			dbError(c, e)
			return 0, false
		}
		if old.TemplateID != b.TemplateID || old.Prompt != redactPrivate(b.Prompt, b.APIKey) || (b.TemplateVersion != nil && *b.TemplateVersion != old.TemplateVersion) {
			fail(c, 409, "template_changed", "历史提示词或模板已变更，请重新选择测试内容")
			return 0, false
		}
		return old.TemplateVersion, true
	}
	template, e := s.Store.GetTemplate(c.Request.Context(), b.TemplateID)
	if e != nil {
		dbError(c, e)
		return 0, false
	}
	version := 0
	if template.Prompt == b.Prompt {
		version = template.Version
	} else if (b.TemplateID == "cn_html" && b.Prompt == domain.ChinesePrompt) || (b.TemplateID == "en_svg" && b.Prompt == domain.EnglishPrompt) {
		version = 1
	}
	if b.TemplateVersion != nil && *b.TemplateVersion != version {
		fail(c, 409, "template_changed", "模板版本与实际提示词不匹配，请刷新后重试")
		return 0, false
	}
	return version, true
}
