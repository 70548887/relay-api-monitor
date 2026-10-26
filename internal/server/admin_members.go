package server

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"
	"relaylab/internal/security"
	"relaylab/internal/store"
)

func (s *Server) adminMembers(c *gin.Context) {
	items, err := s.Store.ListMembers(c.Request.Context())
	if err != nil {
		dbError(c, err)
		return
	}
	c.JSON(200, gin.H{"items": items})
}

func (s *Server) createAdminMember(c *gin.Context) {
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
	s.auditEvent(c.Request.Context(), "member_created", u.ID, "创建用户账户")
	c.JSON(201, u)
}

func (s *Server) updateAdminMember(c *gin.Context) {
	var b struct {
		Disabled *bool `json:"disabled"`
	}
	if !bind(c, &b) {
		return
	}
	if b.Disabled == nil {
		fail(c, 400, "invalid_request", "请指定用户启用状态")
		return
	}
	u, err := s.Store.SetMemberDisabled(c.Request.Context(), c.Param("id"), *b.Disabled)
	if err != nil {
		dbError(c, err)
		return
	}
	action, detail := "member_enabled", "启用用户账户"
	if u.Disabled {
		action, detail = "member_disabled", "停用用户账户"
	}
	s.auditEvent(c.Request.Context(), action, u.ID, detail)
	c.JSON(200, u)
}

func (s *Server) resetAdminMemberPassword(c *gin.Context) {
	if !s.authAdmission(c) {
		return
	}
	defer s.authRelease()
	var b struct {
		Password string `json:"password"`
	}
	if !bind(c, &b) {
		return
	}
	hash, err := security.PasswordHash(b.Password)
	if err != nil {
		fail(c, 400, "invalid_password", err.Error())
		return
	}
	if err = s.Store.UpdateMemberPassword(c.Request.Context(), c.Param("id"), hash); err != nil {
		dbError(c, err)
		return
	}
	s.auditEvent(c.Request.Context(), "member_password_reset", c.Param("id"), "重置用户密码并撤销登录会话")
	c.JSON(200, gin.H{"ok": true})
}
