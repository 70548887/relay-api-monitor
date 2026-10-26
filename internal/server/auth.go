package server

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"relaylab/internal/domain"
	"relaylab/internal/security"
)

func (s *Server) setupStatus(c *gin.Context) {
	ok, e := s.Store.HasAdmin(c.Request.Context())
	if e != nil {
		dbError(c, e)
		return
	}
	c.JSON(200, gin.H{"initialized": ok})
}
func (s *Server) setup(c *gin.Context) {
	if !localRequest(c) || !localSetupHost(c) || !validLoginOrigin(c) {
		fail(c, 403, "local_setup_only", "初始化仅允许本机访问")
		return
	}
	exists, e := s.Store.HasAdmin(c.Request.Context())
	if e != nil {
		dbError(c, e)
		return
	}
	if exists {
		fail(c, 409, "already_initialized", "管理员已初始化，请登录")
		return
	}
	if !s.allowedRate("setup:"+c.ClientIP(), 3) {
		fail(c, 429, "rate_limited", "尝试次数过多，请一分钟后重试")
		return
	}
	if !s.authAdmission(c) {
		return
	}
	defer s.authRelease()
	var b struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !bind(c, &b) {
		return
	}
	b.Username = strings.TrimSpace(b.Username)
	if security.ValidateName(b.Username) != nil {
		fail(c, 400, "invalid_username", "请填写管理员名称")
		return
	}
	h, e := security.PasswordHash(b.Password)
	if e != nil {
		fail(c, 400, "invalid_password", e.Error())
		return
	}
	if e = s.Store.EnsureAdmin(c.Request.Context(), b.Username, h); e != nil {
		fail(c, 409, "already_initialized", "管理员已初始化，请登录")
		return
	}
	s.auditEvent(c.Request.Context(), "admin_initialized", "", "创建管理员")
	c.JSON(201, gin.H{"initialized": true})
}
func (s *Server) login(c *gin.Context) {
	if !validLoginOrigin(c) {
		fail(c, 403, "origin_mismatch", "请求来源不匹配")
		return
	}
	if !s.allowedRate("login:"+c.ClientIP(), 10) {
		fail(c, 429, "rate_limited", "尝试次数过多，请一分钟后重试")
		return
	}
	if !s.authAdmission(c) {
		return
	}
	defer s.authRelease()
	var b struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !bind(c, &b) {
		return
	}
	if len(b.Password) == 0 || len(b.Password) > 256 {
		fail(c, 401, "invalid_credentials", "账号或密码不正确")
		return
	}
	u, e := s.Store.GetAdminByUsername(c.Request.Context(), b.Username)
	// Keep an unknown account on the same Argon2 verification path as a wrong
	// password. The placeholder is never accepted as an account credential.
	encoded := "$argon2id$v=19$m=65536,t=3,p=2$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if e == nil && u != nil {
		encoded = u.PasswordHash
	}
	verified := security.VerifyPassword(encoded, b.Password)
	if e != nil || u == nil || !verified {
		fail(c, 401, "invalid_credentials", "账号或密码不正确")
		return
	}
	token := security.Token()
	ss := domain.Session{TokenHash: security.HashToken(token), AdminID: u.ID, CSRFToken: security.Token(), ExpiresAt: time.Now().UTC().Add(8 * time.Hour)}
	if e = s.Store.SaveSession(c.Request.Context(), ss); e != nil {
		dbError(c, e)
		return
	}
	http.SetCookie(c.Writer, &http.Cookie{Name: "relaylab_session", Value: token, Path: "/", HttpOnly: true, Secure: c.Request.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: 28800})
	c.JSON(200, gin.H{"user": gin.H{"id": u.ID, "username": u.Username}, "csrf_token": ss.CSRFToken})
}

// Argon2 uses 64 MiB per verification. This shared pool bounds setup and
// login work across source IPs before a request body or hash is processed.
func (s *Server) authAdmission(c *gin.Context) bool {
	select {
	case s.authSlots <- struct{}{}:
		return true
	default:
		c.Header("Retry-After", "1")
		fail(c, 429, "authentication_busy", "当前登录请求较多，请稍后重试")
		return false
	}
}

func (s *Server) authRelease() { <-s.authSlots }
func (s *Server) requireAuth(c *gin.Context) {
	if s.Maintenance() && c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead && !strings.HasPrefix(c.Request.URL.Path, "/api/v1/admin/updates") {
		fail(c, http.StatusServiceUnavailable, "maintenance_mode", "系统正在升级维护，请稍后重试")
		return
	}
	token, e := c.Cookie("relaylab_session")
	if e != nil {
		fail(c, 401, "unauthorized", "请先登录")
		return
	}
	ss, e := s.Store.GetSession(c.Request.Context(), security.HashToken(token))
	if e != nil || time.Now().After(ss.ExpiresAt) {
		fail(c, 401, "unauthorized", "登录已过期")
		return
	}
	u, e := s.Store.GetAdminByID(c.Request.Context(), ss.AdminID)
	if e != nil {
		fail(c, 401, "unauthorized", "请重新登录")
		return
	}
	if c.Request.Method != "GET" && c.Request.Method != "HEAD" {
		if !validOrigin(c) || !security.ConstantEqual(c.Request.Header.Get("X-CSRF-Token"), ss.CSRFToken) {
			fail(c, 403, "csrf_failed", "请求验证失败，请刷新页面")
			return
		}
	}
	c.Set("session", ss)
	c.Set("admin", u)
	c.Next()
}
func (s *Server) me(c *gin.Context) {
	u := c.MustGet("admin").(*domain.Admin)
	ss := c.MustGet("session").(*domain.Session)
	c.JSON(200, gin.H{"user": gin.H{"id": u.ID, "username": u.Username}, "csrf_token": ss.CSRFToken})
}
func (s *Server) logout(c *gin.Context) {
	ss := c.MustGet("session").(*domain.Session)
	if e := s.Store.DeleteSession(c.Request.Context(), ss.TokenHash); e != nil && !errors.Is(e, sql.ErrNoRows) {
		dbError(c, e)
		return
	}
	http.SetCookie(c.Writer, &http.Cookie{Name: "relaylab_session", Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	c.JSON(200, gin.H{"ok": true})
}
func (s *Server) password(c *gin.Context) {
	if !s.authAdmission(c) {
		return
	}
	defer s.authRelease()
	var b struct {
		Old string `json:"old_password"`
		New string `json:"new_password"`
	}
	if !bind(c, &b) {
		return
	}
	u := c.MustGet("admin").(*domain.Admin)
	if !security.VerifyPassword(u.PasswordHash, b.Old) {
		fail(c, 400, "invalid_password", "原密码不正确")
		return
	}
	h, e := security.PasswordHash(b.New)
	if e != nil {
		fail(c, 400, "invalid_password", e.Error())
		return
	}
	if e = s.Store.UpdatePassword(c.Request.Context(), u.ID, h); e != nil {
		dbError(c, e)
		return
	}
	s.auditEvent(c.Request.Context(), "password_changed", u.ID, "管理员修改密码")
	http.SetCookie(c.Writer, &http.Cookie{Name: "relaylab_session", Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	c.JSON(200, gin.H{"ok": true})
}
