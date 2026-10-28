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
	"relaylab/internal/store"
)

const memberSessionCookie = "relaylab_user_session"

func (s *Server) registerMember(c *gin.Context) {
	if !validLoginOrigin(c) {
		fail(c, 403, "origin_mismatch", "请求来源不匹配")
		return
	}
	if !s.allowedRate("member-register:"+c.ClientIP(), 3) {
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
		fail(c, 400, "invalid_username", "名称不能为空且不能超过200字节")
		return
	}
	hash, err := security.PasswordHash(b.Password)
	if err != nil {
		fail(c, 400, "invalid_password", err.Error())
		return
	}
	u, err := s.Store.CreateMember(c.Request.Context(), b.Username, hash)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			fail(c, 409, "username_exists", "该用户名称已存在")
			return
		}
		dbError(c, err)
		return
	}
	if !s.startMemberSession(c, u, hash, http.StatusCreated) {
		return
	}
	s.auditEvent(c.Request.Context(), "member_registered", u.ID, "用户注册账户")
}

func (s *Server) loginMember(c *gin.Context) {
	if !validLoginOrigin(c) {
		fail(c, 403, "origin_mismatch", "请求来源不匹配")
		return
	}
	if !s.allowedRate("member-login:"+c.ClientIP(), 10) {
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
	u, err := s.Store.GetMemberByUsername(c.Request.Context(), b.Username)
	encoded := "$argon2id$v=19$m=65536,t=3,p=2$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if err == nil && u != nil {
		encoded = u.PasswordHash
	}
	verified := security.VerifyPassword(encoded, b.Password)
	if err != nil || u == nil || !verified || u.Disabled {
		fail(c, 401, "invalid_credentials", "账号或密码不正确")
		return
	}
	s.startMemberSession(c, u, u.PasswordHash, http.StatusOK)
}

// Member and administrator sessions use separate cookies, tables and context
// keys, so an ordinary account never inherits administrator authentication.
func (s *Server) startMemberSession(c *gin.Context, u *domain.Member, passwordHash string, status int) bool {
	token := security.Token()
	ss := domain.MemberSession{TokenHash: security.HashToken(token), MemberID: u.ID, CSRFToken: security.Token(), ExpiresAt: time.Now().UTC().Add(8 * time.Hour)}
	if err := s.Store.SaveAuthenticatedMemberSession(c.Request.Context(), ss, passwordHash); err != nil {
		if errors.Is(err, store.ErrConflict) {
			fail(c, 401, "invalid_credentials", "账号或密码不正确")
		} else {
			dbError(c, err)
		}
		return false
	}
	http.SetCookie(c.Writer, &http.Cookie{Name: memberSessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: secureSessionCookie(c), SameSite: http.SameSiteStrictMode, MaxAge: 28800})
	c.JSON(status, gin.H{"user": u, "csrf_token": ss.CSRFToken})
	return true
}

func (s *Server) requireMemberAuth(c *gin.Context) {
	token, err := c.Cookie(memberSessionCookie)
	if err != nil || token == "" {
		fail(c, 401, "unauthorized", "请先登录")
		return
	}
	ss, err := s.Store.GetMemberSession(c.Request.Context(), security.HashToken(token))
	if err != nil || ss == nil || !time.Now().Before(ss.ExpiresAt) {
		fail(c, 401, "unauthorized", "登录已过期")
		return
	}
	u, err := s.Store.GetMemberByID(c.Request.Context(), ss.MemberID)
	if err != nil || u == nil || u.Disabled {
		fail(c, 401, "unauthorized", "请重新登录")
		return
	}
	if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
		if !validOrigin(c) || !security.ConstantEqual(c.Request.Header.Get("X-CSRF-Token"), ss.CSRFToken) {
			fail(c, 403, "csrf_failed", "请求验证失败，请刷新页面")
			return
		}
	}
	c.Set("member_session", ss)
	c.Set("member", u)
	c.Next()
}

func (s *Server) memberMe(c *gin.Context) {
	u := c.MustGet("member").(*domain.Member)
	ss := c.MustGet("member_session").(*domain.MemberSession)
	c.JSON(200, gin.H{"user": u, "csrf_token": ss.CSRFToken})
}

func clearMemberCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{Name: memberSessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: secureSessionCookie(c), SameSite: http.SameSiteStrictMode, MaxAge: -1})
}

func (s *Server) memberLogout(c *gin.Context) {
	ss := c.MustGet("member_session").(*domain.MemberSession)
	if err := s.Store.DeleteMemberSession(c.Request.Context(), ss.TokenHash); err != nil && !errors.Is(err, sql.ErrNoRows) {
		dbError(c, err)
		return
	}
	clearMemberCookie(c)
	c.JSON(200, gin.H{"ok": true})
}

func (s *Server) memberPassword(c *gin.Context) {
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
	u := c.MustGet("member").(*domain.Member)
	if len(b.Old) == 0 || len(b.Old) > 256 || !security.VerifyPassword(u.PasswordHash, b.Old) {
		fail(c, 400, "invalid_password", "原密码不正确")
		return
	}
	hash, err := security.PasswordHash(b.New)
	if err != nil {
		fail(c, 400, "invalid_password", err.Error())
		return
	}
	if err = s.Store.UpdateMemberPassword(c.Request.Context(), u.ID, hash, u.PasswordHash); err != nil {
		if errors.Is(err, store.ErrConflict) {
			clearMemberCookie(c)
			fail(c, 401, "credentials_changed", "账号状态已变更，请重新登录")
			return
		}
		dbError(c, err)
		return
	}
	s.auditEvent(c.Request.Context(), "member_password_changed", u.ID, "用户修改密码")
	clearMemberCookie(c)
	c.JSON(200, gin.H{"ok": true})
}
