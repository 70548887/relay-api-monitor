package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
	"relaylab/internal/artifact"
	"relaylab/internal/domain"
	"relaylab/internal/upstream"
)

func validateBaseURL(base, models, gen string) error {
	return upstream.ValidatePublicURL(base, models, gen)
}

var publicBearer = regexp.MustCompile(`(?i)\bBearer\s+[^\s,;"']+`)
var publicKey = regexp.MustCompile(`\b(?:sk|sess)-[A-Za-z0-9_-]{8,}\b`)

func publicError(err error) string {
	var upstreamErr *upstream.Error
	if !errors.As(err, &upstreamErr) {
		return "上游请求失败，请稍后重试"
	}
	message := publicBearer.ReplaceAllString(upstreamErr.Message, "Bearer [redacted]")
	message = publicKey.ReplaceAllString(message, "[redacted]")
	message = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, message)
	runes := []rune(message)
	if len(runes) > 512 {
		message = string(runes[:512])
	}
	if strings.TrimSpace(message) == "" {
		return "上游请求失败，请稍后重试"
	}
	return message
}

type visitorRequest struct {
	ConnectionID       string `json:"connection_id"`
	ConnectionRevision int    `json:"connection_revision"`
	TemplateVersion    *int   `json:"template_version"`
	SourceRecordID     string `json:"source_record_id"`
	ClientRequestID    string `json:"client_request_id"`
	BaseURL            string `json:"base_url"`
	APIKey             string `json:"api_key"`
	Protocol           string `json:"protocol"`
	DiscoveryProtocol  string `json:"discovery_protocol"`
	ModelsPath         string `json:"models_path"`
	GeneratePath       string `json:"generate_path"`
	AnthropicVersion   string `json:"anthropic_version"`
	ChatTokenField     string `json:"chat_token_field"`
	Model              string `json:"model"`
	Prompt             string `json:"prompt"`
	TemplateID         string `json:"template_id"`
	MaxTokens          int    `json:"max_tokens"`
	TimeoutSeconds     int    `json:"timeout_seconds"`
	ReasoningEffort    string `json:"reasoning_effort"`
	ThinkingMode       string `json:"thinking_mode"`
	ThinkingBudget     int    `json:"thinking_budget"`
}

func (b visitorRequest) config() upstream.Config {
	return upstream.Config{BaseURL: b.BaseURL, APIKey: b.APIKey, Protocol: b.Protocol, DiscoveryProtocol: b.DiscoveryProtocol, ModelsPath: b.ModelsPath, GeneratePath: b.GeneratePath, AnthropicVersion: b.AnthropicVersion, ChatTokenField: b.ChatTokenField}
}
func validateVisitor(c *gin.Context, b visitorRequest) bool {
	if err := validateBaseURL(b.BaseURL, b.ModelsPath, b.GeneratePath); err != nil {
		fail(c, 400, "invalid_url", err.Error())
		return false
	}
	if strings.TrimSpace(b.APIKey) == "" || len(b.APIKey) > 8192 || strings.ContainsAny(b.APIKey, "\r\n") {
		fail(c, 400, "invalid_key", "API Key 不能为空或包含换行")
		return false
	}
	if len(b.AnthropicVersion) > 100 || strings.ContainsAny(b.AnthropicVersion, "\r\n") {
		fail(c, 400, "invalid_version", "Anthropic Version 不正确")
		return false
	}
	return true
}

// Admission precedes decoding, so malformed and oversized bodies consume the
// same rate and concurrency budget as valid visitor requests.
func (s *Server) visitorAdmission(c *gin.Context, discovery bool) (domain.Settings, func(), bool) {
	settings, err := s.Store.GetSettings(c.Request.Context())
	if err != nil {
		dbError(c, err)
		return domain.Settings{}, nil, false
	}
	ip := c.ClientIP()
	limit := settings.VisitorGenerationsPerMinute
	bucket := "generation:"
	if discovery {
		limit = settings.VisitorModelsPerMinute
		bucket = "models:"
	}
	if limit < 1 {
		limit = 1
	}
	if !s.allowedRate(bucket+ip, limit) {
		c.Header("Retry-After", "60")
		fail(c, 429, "rate_limited", "请求过于频繁，请一分钟后重试")
		return settings, nil, false
	}
	global := settings.VisitorConcurrency
	if global < 1 {
		global = 1
	}
	if discovery && global > 8 {
		global = 8
	}
	perIP := settings.VisitorIPConcurrency
	if perIP < 1 {
		perIP = 1
	}
	s.mu.Lock()
	if s.total >= global || s.active[ip] >= perIP {
		s.mu.Unlock()
		c.Header("Retry-After", "5")
		fail(c, 429, "concurrency_limited", "当前测试请求较多，请稍后重试")
		return settings, nil, false
	}
	s.total++
	s.active[ip]++
	s.mu.Unlock()
	release := func() {
		s.mu.Lock()
		s.total--
		s.active[ip]--
		if s.active[ip] <= 0 {
			delete(s.active, ip)
		}
		s.mu.Unlock()
	}
	return settings, release, true
}

func (s *Server) testModels(c *gin.Context) {
	if !validOrigin(c) {
		fail(c, 403, "invalid_origin", "请求来源不匹配")
		return
	}
	settings, release, ok := s.visitorAdmission(c, true)
	if !ok {
		return
	}
	defer release()
	var b visitorRequest
	if !bind(c, &b) {
		return
	}
	defer func() { b.APIKey = "" }()
	connection, resolved := s.resolveTestConnection(c, &b)
	if !resolved {
		return
	}
	if b.DiscoveryProtocol == "" {
		b.DiscoveryProtocol = "openai"
	}
	if b.DiscoveryProtocol != "openai" && b.DiscoveryProtocol != "anthropic" {
		fail(c, 400, "invalid_protocol", "模型发现协议不正确")
		return
	}
	if !validateVisitor(c, b) {
		return
	}
	timeout := settings.DiscoveryTimeoutSeconds
	if timeout < 1 {
		timeout = 20
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), time.Duration(timeout)*time.Second)
	defer cancel()
	started := time.Now()
	models, err := s.Client.ListModels(ctx, b.config())
	if err != nil {
		if c.Request.Context().Err() != nil {
			return
		}
		message := strings.ReplaceAll(publicError(err), b.APIKey, "[redacted]")
		updateCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		_ = s.Store.UpdateUserConnectionDiscovery(updateCtx, memberID(c), connection.ID, connection.Revision, 0, message)
		stop()
		fail(c, 502, "upstream_error", message)
		return
	}
	if models == nil {
		models = []string{}
	}
	for index := range models {
		models[index] = strings.ReplaceAll(models[index], b.APIKey, "[redacted]")
	}
	_ = s.Store.UpdateUserConnectionDiscovery(ctx, memberID(c), connection.ID, connection.Revision, len(models), "")
	c.JSON(200, gin.H{"models": models, "duration_ms": time.Since(started).Milliseconds(), "connection_id": connection.ID})
}

type keyRedactor struct{ key, pending string }

func (r *keyRedactor) push(value string) string {
	value = strings.ReplaceAll(r.pending+value, r.key, "[redacted]")
	r.pending = ""
	max := len(r.key) - 1
	if max > len(value) {
		max = len(value)
	}
	for n := max; n > 0; n-- {
		if strings.HasSuffix(value, r.key[:n]) {
			r.pending = value[len(value)-n:]
			return value[:len(value)-n]
		}
	}
	return value
}
func (r *keyRedactor) finish() string { value := r.pending; r.pending = ""; return value }

func (s *Server) testGenerate(c *gin.Context) {
	if !validOrigin(c) {
		fail(c, 403, "invalid_origin", "请求来源不匹配")
		return
	}
	_, release, ok := s.visitorAdmission(c, false)
	if !ok {
		return
	}
	defer release()
	var b visitorRequest
	if !bind(c, &b) {
		return
	}
	defer func() { b.APIKey = "" }()
	var connection *domain.UserConnection
	if b.ConnectionID != "" {
		var ok bool
		connection, ok = s.resolveTestConnection(c, &b)
		if !ok {
			return
		}
	}
	if b.Protocol == "" {
		b.Protocol = "chat"
	}
	if !safeProtocol(b.Protocol) {
		fail(c, 400, "invalid_protocol", "生成协议不正确")
		return
	}
	if b.TimeoutSeconds == 0 {
		b.TimeoutSeconds = 600
	}
	if strings.TrimSpace(b.Model) == "" || len(b.Model) > 256 || strings.TrimSpace(b.Prompt) == "" || len(b.Prompt) > 128<<10 || b.MaxTokens < 1 || b.MaxTokens > 131072 || b.TimeoutSeconds < 1 || b.TimeoutSeconds > 600 {
		fail(c, 400, "invalid_request", "模型、提示词、输出上限或超时参数不正确")
		return
	}
	if b.ChatTokenField != "" && b.ChatTokenField != "max_tokens" && b.ChatTokenField != "max_completion_tokens" {
		fail(c, 400, "invalid_parameter", "输出 Token 参数不正确")
		return
	}
	if e := upstream.ValidateReasoning(b.Protocol, b.ReasoningEffort, b.ThinkingMode, b.ThinkingBudget, b.MaxTokens); e != nil {
		fail(c, 400, "invalid_reasoning", e.Error())
		return
	}
	if b.TemplateID != "" && b.TemplateID != "cn_html" && b.TemplateID != "en_svg" {
		fail(c, 400, "invalid_template", "模板不正确")
		return
	}
	if !validateVisitor(c, b) {
		return
	}
	if connection == nil {
		var ok bool
		connection, ok = s.resolveTestConnection(c, &b)
		if !ok {
			return
		}
	}
	if b.ClientRequestID == "" {
		b.ClientRequestID = domain.NewID()
	}
	if len(b.ClientRequestID) > 100 || strings.TrimSpace(b.ClientRequestID) == "" {
		fail(c, 400, "invalid_request_id", "请求标识不正确")
		return
	}
	user := memberID(c)
	now := time.Now().UTC()
	snapshot := b
	snapshot.APIKey = ""
	snapshot.ClientRequestID = ""
	requestHash := privateHash([]any{snapshot, connection.Revision})
	version, valid := s.privateTemplateVersion(c, b)
	if !valid {
		return
	}
	run := domain.OnlineTestRun{ID: domain.NewID(), UserID: user, ConnectionID: connection.ID, ConnectionRevision: connection.Revision, ClientRequestID: b.ClientRequestID, RequestHash: requestHash, ConnectionName: connection.Name, BaseURL: b.BaseURL, Protocol: b.Protocol, ModelsPath: b.ModelsPath, GeneratePath: b.GeneratePath, AnthropicVersion: b.AnthropicVersion, ChatTokenField: b.ChatTokenField, Model: b.Model, Prompt: redactPrivate(b.Prompt, b.APIKey), TemplateID: b.TemplateID, TemplateVersion: version, MaxTokens: b.MaxTokens, TimeoutSeconds: b.TimeoutSeconds, ReasoningEffort: b.ReasoningEffort, ThinkingMode: b.ThinkingMode, ThinkingBudget: b.ThinkingBudget, State: "created", PreviewStatus: "none", ProcessID: s.privateProcess, HeartbeatAt: now, Deadline: now.Add(time.Duration(b.TimeoutSeconds) * time.Second), CreatedAt: now, StartedAt: now}
	s.privateLifecycle.Lock()
	saved, created, e := s.Store.CreateOnlineTestRun(c.Request.Context(), run)
	if e != nil {
		s.privateLifecycle.Unlock()
		dbError(c, e)
		return
	}
	if !created {
		s.privateLifecycle.Unlock()
		c.JSON(200, gin.H{"record_id": saved.ID, "saved": true, "reused": true, "state": saved.State})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), time.Duration(b.TimeoutSeconds)*time.Second)
	defer cancel()
	s.mu.Lock()
	s.privateCancels[run.ID] = cancel
	s.mu.Unlock()
	s.privateLifecycle.Unlock()
	defer func() { s.mu.Lock(); delete(s.privateCancels, run.ID); s.mu.Unlock() }()
	run.State = "running"
	if e = s.Store.UpdateOnlineTestRun(ctx, run); e != nil {
		if ctx.Err() != nil {
			run.State = "canceled"
			run.ErrorCode = "canceled_before_dispatch"
			run.ErrorMessage = "请求在发送前已取消"
			finished := time.Now().UTC()
			run.FinishedAt = &finished
			saveCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
			_ = s.Store.UpdateOnlineTestRun(saveCtx, run)
			stop()
		} else {
			_ = s.interruptPrivateRun(run, "请求尚未发送，记录状态写入失败；可重新发起测试")
		}
		dbError(c, e)
		return
	}
	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-cache, no-store")
	c.Header("X-Accel-Buffering", "no")
	c.Status(200)
	var outputErr error
	send := func(event string, value any) error {
		if outputErr != nil {
			return outputErr
		}
		if err := c.Request.Context().Err(); err != nil {
			return err
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		controller := http.NewResponseController(c.Writer)
		if err = controller.SetWriteDeadline(time.Now().Add(15 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			outputErr = err
			return err
		}
		defer func() { _ = controller.SetWriteDeadline(time.Time{}) }()
		if _, err = fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event, encoded); err != nil {
			outputErr = err
			return err
		}
		outputErr = controller.Flush()
		return outputErr
	}
	var mu sync.Mutex
	var persistMu sync.Mutex
	stopProgress := make(chan struct{})
	progressDone := make(chan struct{})
	var persistErr error
	persist := func() error {
		persistMu.Lock()
		defer persistMu.Unlock()
		mu.Lock()
		copy := run
		mu.Unlock()
		copy.HeartbeatAt = time.Now().UTC()
		saveCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		return s.Store.UpdateOnlineTestRun(saveCtx, copy)
	}
	go func() {
		defer close(progressDone)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopProgress:
				return
			case <-ticker.C:
				if err := persist(); err != nil {
					mu.Lock()
					persistErr = err
					mu.Unlock()
					cancel()
					return
				}
			}
		}
	}()
	sentBytes := 0
	recordErr := send("record", gin.H{"record_id": run.ID, "saved": true})
	redactor := keyRedactor{key: b.APIKey}
	result := upstream.Result{}
	var requestErr error
	if recordErr != nil {
		requestErr = recordErr
	} else {
		result, requestErr = s.Client.Generate(ctx, upstream.Request{Config: b.config(), Model: b.Model, Prompt: b.Prompt, MaxTokens: b.MaxTokens, Timeout: time.Duration(b.TimeoutSeconds) * time.Second, ReasoningEffort: b.ReasoningEffort, ThinkingMode: b.ThinkingMode, ThinkingBudget: b.ThinkingBudget}, func(event upstream.Event) error {
			switch event.Type {
			case "meta":
				mu.Lock()
				run.HTTPStatus = event.HTTPStatus
				mu.Unlock()
				return send("meta", gin.H{"record_id": run.ID, "requested_model": b.Model, "protocol": b.Protocol, "http_status": event.HTTPStatus, "reasoning_effort": event.ReasoningEffort, "thinking_mode": event.ThinkingMode, "thinking_budget": event.ThinkingBudget})
			case "text":
				text := redactor.push(event.Text)
				if text == "" {
					return nil
				}
				mu.Lock()
				run.Text += text
				if run.FirstTextMS == nil {
					ms := time.Since(now).Milliseconds()
					run.FirstTextMS = &ms
				}
				needSave := len(run.Text)-sentBytes >= 64<<10
				if needSave {
					sentBytes = len(run.Text)
				}
				mu.Unlock()
				if needSave {
					if err := persist(); err != nil {
						mu.Lock()
						persistErr = err
						mu.Unlock()
						return err
					}
				}
				return send("delta", gin.H{"text": text})
			case "usage":
				if event.Usage != nil {
					mu.Lock()
					run.Usage = *event.Usage
					mu.Unlock()
					return send("usage", event.Usage)
				}
			}
			return nil
		})
	}
	close(stopProgress)
	<-progressDone
	// Read the adapter's final accumulated text even when downstream delivery was
	// canceled. No additional upstream request is issued to reconstruct it.
	tail := redactor.finish()
	run.Text = redactPrivate(result.Text, b.APIKey)
	run.ReturnedModel = redactPrivate(result.ReturnedModel, b.APIKey)
	run.Usage = result.Usage
	run.HTTPStatus = result.HTTPStatus
	run.DurationMS = result.DurationMS
	if run.DurationMS == 0 {
		run.DurationMS = time.Since(now).Milliseconds()
	}
	run.FirstTextMS = result.FirstTextMS
	run.FinishReason = result.FinishReason
	run.State = "succeeded"
	if requestErr != nil || !result.Complete {
		run.State = "failed"
		run.ErrorCode = "incomplete_stream"
		run.ErrorMessage = "上游响应未完整结束"
		if requestErr != nil {
			run.ErrorMessage = redactPrivate(publicError(requestErr), b.APIKey)
			var typed *upstream.Error
			if errors.As(requestErr, &typed) {
				run.ErrorCode = typed.Code
			}
		}
		if errors.Is(ctx.Err(), context.Canceled) || c.Request.Context().Err() != nil || outputErr != nil {
			run.State = "canceled"
			run.ErrorCode = "canceled"
			run.ErrorMessage = "测试已停止；仅保存已接收内容，上游是否停止计费由提供方决定"
		}
	}
	if run.State == "succeeded" && b.TemplateID != "" {
		kind := "html"
		if b.TemplateID == "en_svg" {
			kind = "svg"
		}
		code, actualKind, extractErr := artifact.Extract(run.Text, kind)
		if extractErr != nil {
			run.State = "failed"
			run.ErrorCode = "invalid_artifact"
			run.ErrorMessage = "响应不包含完整 HTML/SVG，正文已保存"
			run.PreviewStatus = "invalid"
		} else {
			run.Code, run.Kind, run.PreviewStatus = code, actualKind, "ready"
		}
	}
	finished := time.Now().UTC()
	run.FinishedAt = &finished
	run.HeartbeatAt = finished
	saveCtx, stopSave := context.WithTimeout(context.Background(), 5*time.Second)
	saveErr := s.Store.UpdateOnlineTestRun(saveCtx, run)
	stopSave()
	if persistErr != nil || saveErr != nil {
		if saveErr != nil {
			_ = s.interruptPrivateRun(run, "最终结果保存失败；已保留可写入的接收内容，不会自动重新调用")
		}
		_ = send("error", gin.H{"record_id": run.ID, "saved": false, "code": "history_save_failed", "message": "结果保存失败，请检查测试记录；不会重新调用上游"})
		return
	}
	if c.Request.Context().Err() != nil || outputErr != nil {
		return
	}
	if tail != "" {
		if err := send("delta", gin.H{"text": tail}); err != nil {
			return
		}
	}
	if run.State != "succeeded" {
		_ = send("error", gin.H{"record_id": run.ID, "saved": true, "code": run.ErrorCode, "message": run.ErrorMessage})
		return
	}
	if run.Code != "" {
		if send("artifact", gin.H{"code": run.Code, "kind": run.Kind, "preview_document": artifact.Preview(run.Code, run.Kind)}) != nil {
			return
		}
	}
	_ = send("done", gin.H{"record_id": run.ID, "saved": true, "requested_model": run.Model, "first_text_ms": run.FirstTextMS, "duration_ms": run.DurationMS, "usage": run.Usage, "finish_reason": run.FinishReason, "returned_model": run.ReturnedModel, "http_status": run.HTTPStatus, "reasoning_effort": result.ReasoningEffort, "thinking_mode": result.ThinkingMode, "thinking_budget": result.ThinkingBudget})
}
