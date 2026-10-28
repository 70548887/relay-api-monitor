// Package upstream implements the supported model APIs and validates every production egress.
package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/idna"
	"relaylab/internal/domain"
)

const (
	maxResponseBytes       = 2 << 20
	maxStreamBytes         = 32 << 20
	maxEventBytes          = 4 << 20
	maxTextBytes           = 2 << 20
	maxGenerationJSONBytes = 8 << 20
)

type Config struct {
	BaseURL, APIKey, Protocol, DiscoveryProtocol               string
	ModelsPath, GeneratePath, AnthropicVersion, ChatTokenField string
}

type Request struct {
	Config                        Config
	Model, Prompt                 string
	MaxTokens                     int
	Timeout                       time.Duration
	ReasoningEffort, ThinkingMode string
	ThinkingBudget                int
}

type Event struct {
	Type            string        `json:"type"`
	Text            string        `json:"text,omitempty"`
	Usage           *domain.Usage `json:"usage,omitempty"`
	HTTPStatus      int           `json:"http_status,omitempty"`
	ReturnedModel   string        `json:"returned_model,omitempty"`
	ReasoningEffort string        `json:"reasoning_effort,omitempty"`
	ThinkingMode    string        `json:"thinking_mode,omitempty"`
	ThinkingBudget  int           `json:"thinking_budget,omitempty"`
}

type Result struct {
	Text, ReturnedModel, Protocol, FinishReason string
	HTTPStatus                                  int
	FirstTextMS                                 *int64
	DurationMS                                  int64
	Usage                                       domain.Usage
	Complete                                    bool
	ReceivedBytes, TextBytes                    int64
	ReasoningEffort, ThinkingMode               string
	ThinkingBudget                              int
}

type Error struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

func (e *Error) Error() string { return e.Message }

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type Client struct {
	doer     HTTPDoer
	testOnly bool
	resolver func(context.Context, string) ([]netip.Addr, error)
}

type validatedIPsKey struct{}
type validatedIPs struct {
	hostname string
	addrs    []netip.Addr
}

// New creates a production client. Proxy environment variables are deliberately ignored.
func New() *Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy: nil, ForceAttemptHTTP2: true, MaxIdleConns: 100, MaxIdleConnsPerHost: 16,
		IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 10 * time.Second,
		ExpectContinueTimeout: time.Second,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, &Error{Code: "blocked_target", Message: "Invalid upstream address"}
			}
			approved, ok := ctx.Value(validatedIPsKey{}).(validatedIPs)
			if !ok || !strings.EqualFold(host, approved.hostname) || len(approved.addrs) == 0 {
				return nil, &Error{Code: "blocked_target", Message: "Upstream address was not validated"}
			}
			var lastErr error
			for _, ip := range approved.addrs {
				conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if err == nil {
					return conn, nil
				}
				lastErr = err
				if ctx.Err() != nil {
					break
				}
			}
			return nil, lastErr
		},
	}
	return &Client{doer: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, resolver: lookupIPs}
}

// NewTestClient is explicitly for test fixtures; it skips public-IP checks only.
// URL syntax validation and the protected-target rule still apply.
func NewTestClient(doer HTTPDoer) *Client {
	return &Client{doer: doer, testOnly: true, resolver: lookupIPs}
}

func lookupIPs(ctx context.Context, host string) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip.Unmap()}, nil
	}
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

var deniedPrefixes = func() []netip.Prefix {
	values := []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20", "5f00::/16"}
	result := make([]netip.Prefix, 0, len(values))
	for _, v := range values {
		result = append(result, netip.MustParsePrefix(v))
	}
	return result
}()

func publicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return false
	}
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	for _, prefix := range deniedPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

func endpoint(cfg Config, custom, suffix string) (*url.URL, error) {
	if len(cfg.BaseURL) > 4096 || len(custom) > 4096 {
		return nil, &Error{Code: "invalid_url", Message: "Upstream URL or endpoint path is too long"}
	}
	u, err := url.Parse(strings.TrimSpace(cfg.BaseURL))
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.Opaque != "" {
		return nil, &Error{Code: "invalid_url", Message: "Base URL must be an absolute HTTP or HTTPS URL"}
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" {
		return nil, &Error{Code: "invalid_url", Message: "Base URL cannot include credentials, query or fragment"}
	}
	host := u.Hostname()
	if ip, parseErr := netip.ParseAddr(host); parseErr == nil {
		host = ip.Unmap().String()
	} else {
		if strings.HasPrefix(u.Host, "[") {
			return nil, &Error{Code: "invalid_url", Message: "Invalid bracketed upstream address"}
		}
		host, err = idna.Lookup.ToASCII(host)
		if err != nil {
			return nil, &Error{Code: "invalid_url", Message: "Invalid upstream hostname"}
		}
		host = strings.ToLower(strings.TrimRight(host, "."))
	}
	if host == "api.zxcbug.com" || strings.HasSuffix(host, ".api.zxcbug.com") {
		return nil, &Error{Code: "protected_target", Message: "Requests to the protected upstream service are prohibited"}
	}
	if host == "" || strings.HasPrefix(host, ".") || strings.Contains(host, "..") || strings.ContainsAny(host, "%\\\x00\r\n\t ") {
		return nil, &Error{Code: "invalid_url", Message: "Invalid upstream hostname"}
	}
	if port := u.Port(); port != "" {
		value, parseErr := strconv.Atoi(port)
		if parseErr != nil || value < 1 || value > 65535 {
			return nil, &Error{Code: "invalid_url", Message: "Invalid upstream port"}
		}
		u.Host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	} else {
		u.Host = host
	}
	if custom != "" {
		p, err := url.Parse(custom)
		if err != nil || !strings.HasPrefix(custom, "/") || strings.HasPrefix(custom, "//") || strings.Contains(custom, "\\") || p.IsAbs() || p.Host != "" || p.User != nil || p.RawQuery != "" || p.ForceQuery || p.Fragment != "" {
			return nil, &Error{Code: "invalid_url", Message: "Endpoint override must be an absolute path without a query or fragment"}
		}
		u.Path = p.Path
		u.RawPath = p.RawPath
	} else {
		base := strings.TrimRight(u.Path, "/")
		if base == "" {
			base = "/v1"
		}
		u.Path = base + suffix
		u.RawPath = ""
	}
	return u, nil
}

// ValidatePublicURL validates saved and visitor configuration without DNS or
// HTTP. Production requests additionally validate and pin all resolved IPs.
func ValidatePublicURL(base, models, generate string) error {
	for _, path := range []string{models, generate} {
		u, err := endpoint(Config{BaseURL: base}, path, "/models")
		if err != nil {
			return err
		}
		if ip, parseErr := netip.ParseAddr(u.Hostname()); parseErr == nil && !publicIP(ip) {
			return &Error{Code: "blocked_target", Message: "Upstream URL must use a public network address"}
		}
	}
	return nil
}

func (c *Client) do(ctx context.Context, req *http.Request) (*http.Response, error) {
	if c.doer == nil {
		return nil, &Error{Code: "network_error", Message: "HTTP client is unavailable"}
	}
	if !c.testOnly {
		ips, err := c.resolver(ctx, req.URL.Hostname())
		if err != nil {
			return nil, normalizeTransport(ctx, err)
		}
		if len(ips) == 0 {
			return nil, &Error{Code: "blocked_target", Message: "Upstream hostname has no public IP addresses"}
		}
		for _, ip := range ips {
			if !publicIP(ip) {
				if fakeIP(ip) {
					return nil, &Error{Code: "blocked_target", Message: "域名被代理 DNS 解析为 Fake-IP；请在代理中将 API 域名加入 fake-ip-filter，或配置 upstream.dns_over_https 解析真实公网地址"}
				}
				return nil, &Error{Code: "blocked_target", Message: "Upstream hostname resolves to a non-public or reserved IP address"}
			}
		}
		ctx = context.WithValue(ctx, validatedIPsKey{}, validatedIPs{req.URL.Hostname(), ips})
		req = req.Clone(ctx)
	}
	resp, err := c.doer.Do(req)
	if err != nil {
		return nil, normalizeTransport(ctx, err)
	}
	return resp, nil
}

func normalizeTransport(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return &Error{Code: "timeout", Message: "上游请求达到总超时上限，已停止等待"}
	}
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return &Error{Code: "canceled", Message: "Upstream request was canceled"}
	}
	var typed *Error
	if errors.As(err, &typed) {
		return typed
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		var op *net.OpError
		if errors.As(err, &op) && op.Op == "dial" {
			return &Error{Code: "connection_timeout", Message: "连接上游超时，尚未建立模型请求连接"}
		}
		return &Error{Code: "timeout", Message: "连接上游或读取响应超时，可能早于配置的总超时"}
	}
	return &Error{Code: "network_error", Message: "Upstream connection failed"}
}

func protocol(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		value = "chat"
	}
	if value != "chat" && value != "responses" && value != "anthropic" {
		return "", &Error{Code: "invalid_protocol", Message: "Unsupported generation protocol"}
	}
	return value, nil
}

func setHeaders(req *http.Request, cfg Config, anthropic bool) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream, application/json")
	if anthropic {
		req.Header.Set("x-api-key", cfg.APIKey)
		version := cfg.AnthropicVersion
		if version == "" {
			version = "2023-06-01"
		}
		req.Header.Set("anthropic-version", version)
	} else {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
}

func readLimited(body io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(body, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxResponseBytes {
		return nil, &Error{Code: "response_too_large", Message: "上游目录或查询响应超过 2 MiB 限制"}
	}
	return b, nil
}

var bearerPattern = regexp.MustCompile(`(?i)\bBearer\s+[^\s,;"']+`)
var keyPattern = regexp.MustCompile(`\b(?:sk|sess)-[A-Za-z0-9_-]{8,}\b`)

func sanitizedMessage(value, key string) string {
	if key != "" {
		value = strings.ReplaceAll(value, key, "[redacted]")
	}
	value = bearerPattern.ReplaceAllString(value, "Bearer [redacted]")
	value = keyPattern.ReplaceAllString(value, "[redacted]")
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	if len(value) > 512 {
		value = value[:512]
		for !utf8Valid(value) {
			value = value[:len(value)-1]
		}
	}
	return value
}

func utf8Valid(value string) bool { return utf8.ValidString(value) }

func providerError(raw json.RawMessage, key string, status int) *Error {
	var payload struct{ Message, Type, Code string }
	if json.Unmarshal(raw, &payload) != nil {
		return &Error{Code: "upstream_error", Message: "Upstream reported an error", HTTPStatus: status}
	}
	message := payload.Message
	if strings.TrimSpace(message) == "" {
		message = "Upstream reported an error"
	}
	return &Error{Code: "upstream_error", Message: sanitizedMessage(message, key), HTTPStatus: status}
}

func httpError(resp *http.Response, key string) error {
	b, _ := readLimited(resp.Body)
	var envelope struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	if json.Unmarshal(b, &envelope) == nil {
		if len(envelope.Error) > 0 && string(envelope.Error) != "null" {
			err := providerError(envelope.Error, key, resp.StatusCode)
			err.Code = httpErrorCode(resp.StatusCode)
			return err
		}
		if envelope.Message != "" {
			return &Error{Code: httpErrorCode(resp.StatusCode), Message: sanitizedMessage(envelope.Message, key), HTTPStatus: resp.StatusCode}
		}
	}
	return &Error{Code: httpErrorCode(resp.StatusCode), Message: fmt.Sprintf("Upstream returned HTTP %d", resp.StatusCode), HTTPStatus: resp.StatusCode}
}

func httpErrorCode(status int) string {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "authentication_error"
	case http.StatusTooManyRequests:
		return "rate_limited"
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return "timeout"
	}
	if status >= 300 && status < 400 {
		return "redirect_blocked"
	}
	if status >= 500 {
		return "upstream_unavailable"
	}
	return "upstream_http"
}

func (c *Client) ListModels(ctx context.Context, cfg Config) ([]string, error) {
	discovery := strings.ToLower(strings.TrimSpace(cfg.DiscoveryProtocol))
	if discovery == "" {
		discovery = "openai"
		if cfg.Protocol == "anthropic" {
			discovery = "anthropic"
		}
	}
	if discovery != "openai" && discovery != "anthropic" {
		return nil, &Error{Code: "invalid_protocol", Message: "Unsupported model discovery protocol"}
	}
	u, err := endpoint(cfg, cfg.ModelsPath, "/models")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, &Error{Code: "invalid_request", Message: "Could not build model discovery request"}
	}
	setHeaders(req, cfg, discovery == "anthropic")
	resp, err := c.do(ctx, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, httpError(resp, cfg.APIKey)
	}
	b, err := readLimited(resp.Body)
	if err != nil {
		return nil, normalizeRead(ctx, err)
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(b, &payload) != nil {
		return nil, &Error{Code: "invalid_response", Message: "Model discovery returned invalid JSON", HTTPStatus: resp.StatusCode}
	}
	if len(payload.Error) > 0 && string(payload.Error) != "null" {
		return nil, providerError(payload.Error, cfg.APIKey, resp.StatusCode)
	}
	seen := map[string]bool{}
	result := []string{}
	for _, item := range payload.Data {
		if cfg.APIKey != "" && strings.Contains(item.ID, cfg.APIKey) {
			return nil, &Error{Code: "credential_exposure", Message: "Upstream model listing contained authentication material", HTTPStatus: resp.StatusCode}
		}
		if item.ID != "" && !seen[item.ID] {
			result = append(result, item.ID)
			seen[item.ID] = true
		}
	}
	sort.Strings(result)
	return result, nil
}

func normalizeRead(ctx context.Context, err error) error {
	var callback *callbackError
	if errors.As(err, &callback) {
		return callback.err
	}
	var typed *Error
	if errors.As(err, &typed) {
		return typed
	}
	if ctx.Err() != nil {
		return normalizeTransport(ctx, err)
	}
	return &Error{Code: "invalid_response", Message: "Upstream response could not be read"}
}

func (c *Client) Generate(ctx context.Context, req Request, onEvent func(Event) error) (result Result, err error) {
	started := time.Now()
	defer func() { result.DurationMS = time.Since(started).Milliseconds() }()
	result.Protocol, err = protocol(req.Config.Protocol)
	if err != nil {
		return result, err
	}
	if strings.TrimSpace(req.Model) == "" || strings.TrimSpace(req.Prompt) == "" {
		return result, &Error{Code: "invalid_request", Message: "Model and prompt are required"}
	}
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	if err = ValidateReasoning(result.Protocol, req.ReasoningEffort, req.ThinkingMode, req.ThinkingBudget, maxTokens); err != nil {
		return result, err
	}
	result.ReasoningEffort, result.ThinkingMode, result.ThinkingBudget = reasoningValue(req.ReasoningEffort), reasoningValue(req.ThinkingMode), req.ThinkingBudget
	body := map[string]any{"model": req.Model, "stream": true}
	suffix := "/chat/completions"
	switch result.Protocol {
	case "chat":
		body["messages"] = []map[string]string{{"role": "user", "content": req.Prompt}}
		field := req.Config.ChatTokenField
		if field == "" {
			field = "max_completion_tokens"
		}
		if field != "max_completion_tokens" && field != "max_tokens" {
			return result, &Error{Code: "invalid_request", Message: "Unsupported Chat token field"}
		}
		body[field] = maxTokens
		body["stream_options"] = map[string]bool{"include_usage": true}
		if result.ReasoningEffort != "" {
			body["reasoning_effort"] = result.ReasoningEffort
		}
	case "responses":
		suffix = "/responses"
		body["input"] = req.Prompt
		body["max_output_tokens"] = maxTokens
		if result.ReasoningEffort != "" {
			body["reasoning"] = map[string]string{"effort": result.ReasoningEffort}
		}
	case "anthropic":
		suffix = "/messages"
		body["messages"] = []map[string]string{{"role": "user", "content": req.Prompt}}
		body["max_tokens"] = maxTokens
		if result.ReasoningEffort != "" {
			body["output_config"] = map[string]string{"effort": result.ReasoningEffort}
		}
		if result.ThinkingMode != "" {
			thinking := map[string]any{"type": result.ThinkingMode}
			if result.ThinkingMode == "enabled" {
				thinking["budget_tokens"] = result.ThinkingBudget
			}
			body["thinking"] = thinking
		}
	}
	u, err := endpoint(req.Config, req.Config.GeneratePath, suffix)
	if err != nil {
		return result, err
	}
	encoded, _ := json.Marshal(body)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(encoded))
	if err != nil {
		return result, &Error{Code: "invalid_request", Message: "Could not build generation request"}
	}
	// A billable generation must never be replayed after a partially written
	// request, including HTTP/2 transport recovery on GOAWAY/REFUSED_STREAM.
	httpReq.GetBody = nil
	setHeaders(httpReq, req.Config, result.Protocol == "anthropic")
	resp, err := c.do(ctx, httpReq)
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()
	result.HTTPStatus = resp.StatusCode
	counter := &countingReader{reader: resp.Body}
	defer func() { result.ReceivedBytes = counter.bytes }()
	if onEvent != nil {
		if err = onEvent(Event{Type: "meta", HTTPStatus: resp.StatusCode, ReasoningEffort: result.ReasoningEffort, ThinkingMode: result.ThinkingMode, ThinkingBudget: result.ThinkingBudget}); err != nil {
			return result, err
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errorResponse := *resp
		errorResponse.Body = io.NopCloser(counter)
		return result, httpError(&errorResponse, req.Config.APIKey)
	}
	state := generationState{result: &result, started: started, callback: onEvent, key: req.Config.APIKey}
	defer func() {
		if flushErr := state.flushText(); err == nil && flushErr != nil {
			err = normalizeRead(ctx, flushErr)
			result.Complete = false
		}
		result.Text = state.textBuffer.String()
		result.ReceivedBytes = counter.bytes
		result.TextBytes = int64(len(result.Text))
		result.ReturnedModel = state.redact(result.ReturnedModel)
		result.FinishReason = state.redact(result.FinishReason)
	}()
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(contentType, "application/json") || strings.Contains(contentType, "+json") {
		var data []byte
		data, err = readGenerationJSON(counter)
		if err == nil {
			err = state.consumeJSON(data)
		}
	} else {
		err = readSSE(counter, func(event, data string) error { return state.consumeSSE(event, data) })
	}
	if err != nil {
		return result, normalizeRead(ctx, err)
	}
	if err = state.finish(); err != nil {
		return result, err
	}
	result.Complete = true
	return result, nil
}
