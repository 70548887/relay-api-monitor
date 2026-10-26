package server

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"
	"relaylab/internal/domain"
	"relaylab/internal/security"
	"relaylab/internal/store"
)

type adminUserResponse struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	Disabled  bool   `json:"disabled"`
	CreatedAt string `json:"created_at"`
	IsCurrent bool   `json:"is_current"`
}

func adminUser(u domain.Admin, currentID string) adminUserResponse {
	return adminUserResponse{ID: u.ID, Username: u.Username, Disabled: u.Disabled, CreatedAt: u.CreatedAt, IsCurrent: u.ID == currentID}
}

func (s *Server) adminUsers(c *gin.Context) {
	items, err := s.Store.ListAdmins(c.Request.Context())
	if err != nil {
		dbError(c, err)
		return
	}
	current := c.MustGet("admin").(*domain.Admin)
	out := make([]adminUserResponse, 0, len(items))
	for _, u := range items {
		out = append(out, adminUser(u, current.ID))
	}
	c.JSON(200, gin.H{"items": out})
}

func (s *Server) createAdminUser(c *gin.Context) {
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
	u, err := s.Store.CreateAdmin(c.Request.Context(), b.Username, hash)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			fail(c, 409, "username_exists", "该管理员名称已存在")
			return
		}
		dbError(c, err)
		return
	}
	s.auditEvent(c.Request.Context(), "admin_created", u.ID, "创建管理员账户")
	current := c.MustGet("admin").(*domain.Admin)
	c.JSON(201, adminUser(*u, current.ID))
}

func (s *Server) updateAdminUser(c *gin.Context) {
	var b struct {
		Disabled *bool `json:"disabled"`
	}
	if !bind(c, &b) {
		return
	}
	if b.Disabled == nil {
		fail(c, 400, "invalid_request", "请指定管理员启用状态")
		return
	}
	current := c.MustGet("admin").(*domain.Admin)
	u, err := s.Store.SetAdminDisabled(c.Request.Context(), current.ID, c.Param("id"), *b.Disabled)
	if err != nil {
		if adminUserError(c, err) {
			return
		}
		dbError(c, err)
		return
	}
	action, detail := "admin_enabled", "启用管理员账户"
	if u.Disabled {
		action, detail = "admin_disabled", "停用管理员账户"
	}
	s.auditEvent(c.Request.Context(), action, u.ID, detail)
	c.JSON(200, adminUser(*u, current.ID))
}

func (s *Server) resetAdminUserPassword(c *gin.Context) {
	current := c.MustGet("admin").(*domain.Admin)
	if current.ID == c.Param("id") {
		fail(c, 409, "current_admin_protected", "请通过修改密码功能更新当前账户密码")
		return
	}
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
	if err = s.Store.ResetAdminPassword(c.Request.Context(), current.ID, c.Param("id"), hash); err != nil {
		if adminUserError(c, err) {
			return
		}
		dbError(c, err)
		return
	}
	s.auditEvent(c.Request.Context(), "admin_password_reset", c.Param("id"), "重置管理员密码并撤销登录会话")
	c.JSON(200, gin.H{"ok": true})
}

func adminUserError(c *gin.Context, err error) bool {
	if errors.Is(err, store.ErrSelfAdmin) {
		fail(c, 409, "current_admin_protected", "当前登录账户不可停用，请保留至少一个可用管理员")
		return true
	}
	if errors.Is(err, store.ErrLastActiveAdmin) {
		fail(c, 409, "last_active_admin", "必须保留至少一个可用管理员")
		return true
	}
	return false
}
