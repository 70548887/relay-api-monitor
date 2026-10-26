package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"relaylab/internal/domain"
	"relaylab/internal/runtimecontrol"
	"relaylab/internal/security"
	"relaylab/internal/store"
	"relaylab/internal/update"
	"relaylab/internal/upstream"
	"relaylab/internal/webassets"
)

type Server struct {
	Store            *store.Store
	Vault            *security.Vault
	Client           *upstream.Client
	mu               sync.Mutex
	rates            map[string]*rateBucket
	active           map[string]int
	total            int
	authSlots        chan struct{}
	gate             runtimecontrol.Gate
	updateBusy       atomic.Bool
	updateContext    context.Context
	launcherManaged  bool
	launcherProtocol int
	updates          *update.Manager
}
type rateBucket struct {
	Start time.Time
	Count int
}

func New(st *store.Store, v *security.Vault) *Server {
	return &Server{Store: st, Vault: v, Client: upstream.New(), rates: map[string]*rateBucket{}, active: map[string]int{}, authSlots: make(chan struct{}, 2)}
}

// SetUpdateManager wires the online updater without changing the existing
// constructor used by tests and manually embedded deployments.
func (s *Server) SetUpdateManager(m *update.Manager) { s.updates = m }
func (s *Server) UpdateManager() *update.Manager     { return s.updates }
func (s *Server) SetMaintenance(v bool)              { s.gate.Set(v) }
func (s *Server) Maintenance() bool                  { paused, _ := s.gate.Snapshot(); return paused }
func (s *Server) SetMaintenanceFile(path string)     { s.gate.Configure(path) }
func (s *Server) RuntimeState() (bool, int, bool) {
	paused, active := s.gate.Snapshot()
	return paused, active, true
}
func (s *Server) ConfigureUpdates(ctx context.Context, managed bool, protocol int) {
	s.updateContext = ctx
	s.launcherManaged = managed
	s.launcherProtocol = protocol
}

func (s *Server) Handler() http.Handler {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	_ = r.SetTrustedProxies(nil)
	r.Use(func(c *gin.Context) {
		c.Header("X-Request-ID", domain.NewID())
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Referrer-Policy", "no-referrer")
		if strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.Header("Cache-Control", "no-store")
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2<<20)
		}
		c.Next()
	})
	// The fence is checked at admission, not by a delayed polling goroutine.
	// Count all business writes, including login/setup, discovery and streaming.
	r.Use(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api/v1/") && c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead && !strings.HasPrefix(c.Request.URL.Path, "/api/v1/admin/updates") {
			leave, ok := s.gate.Enter()
			if !ok {
				c.Header("Retry-After", "5")
				fail(c, 503, "maintenance_mode", "系统正在升级维护，请稍后重试")
				return
			}
			defer leave()
		}
		c.Next()
	})
	a := r.Group("/api/v1")
	a.GET("/setup/status", s.setupStatus)
	a.POST("/setup", s.setup)
	a.POST("/auth/login", s.login)
	a.POST("/user/auth/register", s.registerMember)
	a.POST("/user/auth/login", s.loginMember)
	memberAuth := a.Group("/user/auth")
	memberAuth.Use(s.requireMemberAuth)
	memberAuth.GET("/me", s.memberMe)
	memberAuth.POST("/logout", s.memberLogout)
	memberAuth.POST("/password", s.memberPassword)
	auth := a.Group("/auth")
	auth.Use(s.requireAuth)
	auth.GET("/me", s.me)
	auth.POST("/logout", s.logout)
	auth.POST("/password", s.password)
	admin := a.Group("/admin")
	admin.Use(s.requireAuth)
	admin.GET("/users", s.adminUsers)
	admin.POST("/users", s.createAdminUser)
	admin.PATCH("/users/:id", s.updateAdminUser)
	admin.POST("/users/:id/reset-password", s.resetAdminUserPassword)
	admin.GET("/members", s.adminMembers)
	admin.POST("/members", s.createAdminMember)
	admin.PATCH("/members/:id", s.updateAdminMember)
	admin.POST("/members/:id/reset-password", s.resetAdminMemberPassword)
	admin.GET("/overview", s.overview)
	admin.GET("/platforms", s.platforms)
	admin.POST("/platforms", s.savePlatform)
	admin.PUT("/platforms/:id", s.savePlatform)
	admin.DELETE("/platforms/:id", s.archivePlatform)
	admin.GET("/channels", s.channels)
	admin.POST("/channels", s.saveChannel)
	admin.PUT("/channels/:id", s.saveChannel)
	admin.DELETE("/channels/:id", s.archiveChannel)
	admin.POST("/channels/:id/sync-models", s.syncModels)
	admin.POST("/channels/:id/verify-group", s.verifyGroup)
	admin.PUT("/channels/:id/intelligence-schedule", s.saveIntelligenceSchedule)
	admin.GET("/models", s.models)
	admin.POST("/models", s.saveModel)
	admin.PUT("/models/:id", s.saveModel)
	admin.DELETE("/models/:id", s.archiveModel)
	admin.GET("/templates", s.templates)
	admin.PUT("/templates/:id", s.saveTemplate)
	admin.GET("/settings", s.settings)
	admin.PUT("/settings", s.saveSettings)
	admin.GET("/schedules", s.schedules)
	admin.GET("/jobs", s.jobs)
	admin.POST("/jobs", s.enqueue)
	admin.POST("/jobs/:id/cancel", s.cancelJob)
	admin.GET("/jobs/:id", s.job)
	admin.GET("/artifacts", s.adminArtifacts)
	admin.GET("/artifacts/:id", s.adminArtifact)
	admin.PATCH("/artifacts/:id", s.hideArtifact)
	admin.DELETE("/artifacts/:id", s.deleteArtifact)
	admin.GET("/audit", s.audit)
	admin.GET("/updates", s.updatesSnapshot)
	admin.POST("/updates/check", s.updatesCheck)
	admin.POST("/updates/download", s.updatesDownload)
	admin.POST("/updates/apply", s.updatesApply)
	admin.POST("/updates/rollback", s.updatesRollback)
	admin.GET("/updates/status", s.updatesSnapshot)
	admin.GET("/updates/history", s.updatesHistory)
	a.GET("/public/monitor", s.monitor)
	a.GET("/public/models/:id/history", s.history)
	a.GET("/public/templates", s.templates)
	a.GET("/public/artifacts", s.publicArtifacts)
	a.GET("/public/intelligence", s.intelligence)
	a.GET("/public/artifacts/:id", s.publicArtifact)
	a.GET("/public/artifacts/:id/source", s.source)
	memberTest := a.Group("/test")
	memberTest.Use(s.requireMemberAuth)
	memberTest.POST("/models", s.testModels)
	memberTest.POST("/generate", s.testGenerate)
	r.GET("/healthz", func(c *gin.Context) {
		if e := s.Store.DB.PingContext(c.Request.Context()); e != nil {
			fail(c, 503, "database_unavailable", "数据库不可用")
			return
		}
		c.JSON(200, gin.H{"status": "ok"})
	})
	files := webassets.Files()
	static := http.FileServer(http.FS(files))
	r.NoRoute(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api/") {
			fail(c, 404, "not_found", "接口不存在")
			return
		}
		if c.Request.Method != "GET" && c.Request.Method != "HEAD" {
			c.Status(405)
			return
		}
		path := strings.TrimPrefix(c.Request.URL.Path, "/")
		if path != "" {
			if f, e := fs.Stat(files, path); e == nil && !f.IsDir() {
				static.ServeHTTP(c.Writer, c.Request)
				return
			}
		}
		index, e := fs.ReadFile(files, "index.html")
		if e != nil {
			c.Status(503)
			return
		}
		c.Header("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self'; frame-src 'self' blob:; object-src 'none'; base-uri 'self'; frame-ancestors 'none'")
		c.Data(200, "text/html; charset=utf-8", index)
	})
	return r
}
func fail(c *gin.Context, status int, code, msg string) {
	c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"code": code, "message": msg}, "request_id": c.Writer.Header().Get("X-Request-ID")})
}
func dbError(c *gin.Context, e error) {
	if errors.Is(e, sql.ErrNoRows) {
		fail(c, 404, "not_found", "记录不存在")
	} else if errors.Is(e, store.ErrConflict) {
		fail(c, 409, "conflict", "记录或任务状态已变化，请刷新后重试")
	} else {
		fail(c, 500, "storage_error", "数据库操作失败")
	}
}
func bind(c *gin.Context, v any) bool {
	decoder := json.NewDecoder(c.Request.Body)
	if e := decoder.Decode(v); e != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(e, &tooLarge) {
			fail(c, 413, "request_too_large", "请求内容超过允许大小")
			return false
		}
		fail(c, 400, "invalid_request", "请求格式不正确")
		return false
	}
	// A JSON body is exactly one document. Reading through EOF also enforces
	// MaxBytesReader when valid JSON is followed by an oversized whitespace tail.
	var extra any
	if e := decoder.Decode(&extra); e != io.EOF {
		var tooLarge *http.MaxBytesError
		if errors.As(e, &tooLarge) {
			fail(c, 413, "request_too_large", "请求内容超过允许大小")
		} else {
			fail(c, 400, "invalid_request", "请求格式不正确")
		}
		return false
	}
	return true
}
func safeProtocol(p string) bool { return p == "chat" || p == "responses" || p == "anthropic" }
func (s *Server) auditEvent(ctx context.Context, action, id, detail string) {
	_ = s.Store.AddAudit(ctx, domain.AuditEvent{ID: domain.NewID(), Action: action, EntityID: id, Detail: detail, CreatedAt: time.Now().UTC()})
}
func (s *Server) allowedRate(key string, limit int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if len(s.rates) > 10000 {
		for k, b := range s.rates {
			if now.Sub(b.Start) > time.Minute {
				delete(s.rates, k)
			}
		}
		if len(s.rates) > 10000 {
			return false
		}
	}
	b := s.rates[key]
	if b == nil || now.Sub(b.Start) >= time.Minute {
		s.rates[key] = &rateBucket{now, 1}
		return true
	}
	if b.Count >= limit {
		return false
	}
	b.Count++
	return true
}
func localRequest(c *gin.Context) bool {
	host, _, e := net.SplitHostPort(c.Request.RemoteAddr)
	if e != nil {
		host = c.Request.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
func validOrigin(c *gin.Context) bool {
	if site := c.Request.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	origin := c.Request.Header.Get("Origin")
	if origin == "" {
		return c.Request.Header.Get("Sec-Fetch-Site") == ""
	}
	u, e := url.Parse(origin)
	if e != nil || u.User != nil || u.Opaque != "" || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || len(c.Request.Header.Values("Origin")) != 1 {
		return false
	}
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	if u.Scheme != scheme {
		return false
	}
	target := &url.URL{Scheme: scheme, Host: c.Request.Host}
	port := func(value *url.URL) string {
		if value.Port() != "" {
			return value.Port()
		}
		if value.Scheme == "https" {
			return "443"
		}
		return "80"
	}
	return u.Hostname() != "" && strings.EqualFold(u.Hostname(), target.Hostname()) && port(u) == port(target)
}

func validLoginOrigin(c *gin.Context) bool {
	if !validOrigin(c) {
		return false
	}
	if c.Request.Header.Get("Origin") != "" {
		return true
	}
	// Browser state changes must carry a matching Origin. JSON API clients
	// without browser Fetch Metadata can authenticate without fabricating one.
	for name := range c.Request.Header {
		if strings.HasPrefix(strings.ToLower(name), "sec-fetch-") {
			return false
		}
	}
	contentType, _, err := mime.ParseMediaType(c.Request.Header.Get("Content-Type"))
	return err == nil && contentType == "application/json"
}

func localSetupHost(c *gin.Context) bool {
	host := (&url.URL{Host: c.Request.Host}).Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
func closeBody(r io.ReadCloser) { _ = r.Close() }
