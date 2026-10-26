package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type GroupVerification struct {
	Status    string    `json:"status"`
	Group     string    `json:"group"`
	Message   string    `json:"message"`
	Provider  string    `json:"provider"`
	CheckedAt time.Time `json:"checked_at"`
}

var groupFieldPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+)*$`)

// ValidateGroupProvider validates the administrator-selected single metadata
// endpoint. It does not make a request or enumerate provider endpoints.
func ValidateGroupProvider(cfg Config, provider, path, field string) error {
	provider = strings.ToLower(strings.TrimSpace(provider))
	switch provider {
	case "", "none":
		return nil
	case "newapi":
		_, err := endpoint(cfg, "/api/token/self", "")
		return err
	case "custom":
		if path == "" {
			return &Error{Code: "invalid_group_provider", Message: "Custom verification requires an absolute endpoint path"}
		}
		if _, err := endpoint(cfg, path, ""); err != nil {
			return err
		}
		if field == "" {
			field = "data.group"
		}
		if len(field) > 256 || !groupFieldPattern.MatchString(field) {
			return &Error{Code: "invalid_group_provider", Message: "Group field must be a JSON dot path"}
		}
		return nil
	default:
		return &Error{Code: "invalid_group_provider", Message: "Unsupported group verification provider"}
	}
}

// VerifyGroup checks only token binding metadata, never model routing or
// account lists. It uses the same validated and DNS-pinned egress as generation.
func (c *Client) VerifyGroup(ctx context.Context, cfg Config, provider, path, field string) (v GroupVerification, err error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	v = GroupVerification{Status: "unverified", Provider: provider, CheckedAt: time.Now().UTC()}
	if provider == "" || provider == "none" {
		v.Message = "未配置分组验证提供方"
		return v, nil
	}
	if err = ValidateGroupProvider(cfg, provider, path, field); err != nil {
		v.Status, v.Message = "failed", "分组验证配置不正确"
		return v, err
	}
	if provider == "newapi" {
		path, field = "/api/token/self", "data.group"
	}
	if field == "" {
		field = "data.group"
	}
	u, err := endpoint(cfg, path, "")
	if err != nil {
		return v, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return v, &Error{Code: "invalid_request", Message: "Could not build group verification request"}
	}
	setHeaders(req, cfg, false)
	req.Header.Set("Accept", "application/json")
	resp, err := c.do(ctx, req)
	if err != nil {
		v.Status, v.Message = "failed", "分组验证连接失败"
		return v, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		v.Status, v.Message = "unsupported", "提供方不支持此分组验证接口"
		return v, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		v.Status, v.Message = "failed", "分组元信息接口认证失败；不能据此判断模型 Key 是否可用"
		return v, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		v.Status, v.Message = "failed", "分组元信息接口请求失败"
		return v, nil
	}
	raw, err := readLimited(resp.Body)
	if err != nil {
		v.Status, v.Message = "failed", "分组元信息响应读取失败"
		return v, normalizeRead(ctx, err)
	}
	var data any
	if json.Unmarshal(raw, &data) != nil {
		v.Status, v.Message = "unsupported", "分组元信息响应不是有效 JSON"
		return v, nil
	}
	for _, part := range strings.Split(field, ".") {
		object, ok := data.(map[string]any)
		if !ok {
			data = nil
			break
		}
		data, ok = object[part]
		if !ok {
			data = nil
			break
		}
	}
	group, ok := data.(string)
	group = strings.TrimSpace(group)
	if !ok || group == "" {
		v.Status, v.Message = "unsupported", "响应未提供可用的分组字段"
		return v, nil
	}
	if len(group) > 256 || (cfg.APIKey != "" && strings.Contains(group, cfg.APIKey)) || keyPattern.MatchString(group) || bearerPattern.MatchString(group) {
		v.Status, v.Message = "failed", "分组字段包含不适合公开的内容"
		return v, nil
	}
	v.Status, v.Group = "verified", sanitizedMessage(group, cfg.APIKey)
	v.Message = "已验证 Token 绑定分组；此结果不证明模型请求的实际路由"
	if strings.EqualFold(group, "auto") {
		v.Message = "Token 绑定分组为 auto；模型请求的实际命中分组尚未确认"
	}
	return v, nil
}
