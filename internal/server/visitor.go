package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
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
	BaseURL           string `json:"base_url"`
	APIKey            string `json:"api_key"`
	Protocol          string `json:"protocol"`
	DiscoveryProtocol string `json:"discovery_protocol"`
	ModelsPath        string `json:"models_path"`
	GeneratePath      string `json:"generate_path"`
	AnthropicVersion  string `json:"anthropic_version"`
	ChatTokenField    string `json:"chat_token_field"`
	Model             string `json:"model"`
	Prompt            string `json:"prompt"`
	TemplateID        string `json:"template_id"`
	MaxTokens         int    `json:"max_tokens"`
	TimeoutSeconds    int    `json:"timeout_seconds"`
	ReasoningEffort   string `json:"reasoning_effort"`
	ThinkingMode      string `json:"thinking_mode"`
	ThinkingBudget    int    `json:"thinking_budget"`
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
	if s.Maintenance() {
		fail(c, 503, "maintenance_mode", "系统正在升级维护，请稍后重试")
		return domain.Settings{}, nil, false
	}
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
		fail(c, 502, "upstream_error", strings.ReplaceAll(publicError(err), b.APIKey, "[redacted]"))
		return
	}
	if models == nil {
		models = []string{}
	}
	for index := range models {
		models[index] = strings.ReplaceAll(models[index], b.APIKey, "[redacted]")
	}
	c.JSON(200, gin.H{"models": models, "duration_ms": time.Since(started).Milliseconds()})
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
	ctx, cancel := context.WithTimeout(c.Request.Context(), time.Duration(b.TimeoutSeconds)*time.Second)
	defer cancel()
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
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		controller := http.NewResponseController(c.Writer)
		if err = controller.SetWriteDeadline(time.Now().Add(15 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			outputErr = err
			return err
		}
		defer func() { _ = controller.SetWriteDeadline(time.Time{}) }()
		if _, err = fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event, data); err != nil {
			outputErr = err
			return err
		}
		outputErr = controller.Flush()
		return outputErr
	}
	redactor := keyRedactor{key: b.APIKey}
	result, err := s.Client.Generate(ctx, upstream.Request{Config: b.config(), Model: b.Model, Prompt: b.Prompt, MaxTokens: b.MaxTokens, Timeout: time.Duration(b.TimeoutSeconds) * time.Second, ReasoningEffort: b.ReasoningEffort, ThinkingMode: b.ThinkingMode, ThinkingBudget: b.ThinkingBudget}, func(event upstream.Event) error {
		switch event.Type {
		case "meta":
			return send("meta", gin.H{"http_status": event.HTTPStatus, "protocol": b.Protocol, "reasoning_effort": event.ReasoningEffort, "thinking_mode": event.ThinkingMode, "thinking_budget": event.ThinkingBudget})
		case "text":
			text := redactor.push(event.Text)
			if text != "" {
				return send("delta", gin.H{"text": text})
			}
		case "usage":
			if event.Usage != nil {
				return send("usage", event.Usage)
			}
		}
		return nil
	})
	if c.Request.Context().Err() != nil || outputErr != nil {
		return
	}
	if tail := redactor.finish(); tail != "" {
		if writeErr := send("delta", gin.H{"text": tail}); writeErr != nil {
			return
		}
	}
	if err != nil {
		code := "upstream_error"
		var typed *upstream.Error
		if errors.As(err, &typed) {
			code = typed.Code
		}
		_ = send("error", gin.H{"code": code, "message": strings.ReplaceAll(publicError(err), b.APIKey, "[redacted]")})
		return
	}
	if !result.Complete {
		code := "incomplete"
		message := "上游响应未完成"
		if b.Protocol == "anthropic" {
			code = "anthropic_missing_terminal"
			message = "Anthropic 流缺少 message_stop/stop_reason，作品不会发布；可把 Thinking 设置为上游默认后重试"
		}
		_ = send("error", gin.H{"code": code, "message": message})
		return
	}
	if b.TemplateID != "" {
		kind := "html"
		if b.TemplateID == "en_svg" {
			kind = "svg"
		}
		code, actualKind, extractErr := artifact.Extract(strings.ReplaceAll(result.Text, b.APIKey, "[redacted]"), kind)
		if extractErr != nil {
			_ = send("error", gin.H{"code": "invalid_artifact", "message": "生成响应不包含完整的 HTML 或 SVG 作品"})
			return
		}
		if send("artifact", gin.H{"code": code, "kind": actualKind, "preview_document": artifact.Preview(code, actualKind)}) != nil {
			return
		}
	}
	_ = send("done", gin.H{"first_text_ms": result.FirstTextMS, "duration_ms": result.DurationMS, "usage": result.Usage, "finish_reason": result.FinishReason, "returned_model": strings.ReplaceAll(result.ReturnedModel, b.APIKey, "[redacted]"), "http_status": result.HTTPStatus, "reasoning_effort": result.ReasoningEffort, "thinking_mode": result.ThinkingMode, "thinking_budget": result.ThinkingBudget})
}
