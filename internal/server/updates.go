package server

import (
	"context"
	"errors"
	"net/http"
	"relaylab/internal/update"

	"github.com/gin-gonic/gin"
)

func (s *Server) updateManager(c *gin.Context) (*update.Manager, bool) {
	if s.updates == nil {
		fail(c, http.StatusNotImplemented, "update_disabled", "在线升级未配置")
		return nil, false
	}
	return s.updates, true
}

func (s *Server) updatesSnapshot(c *gin.Context) {
	m, ok := s.updateManager(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, m.Snapshot())
}

func (s *Server) updatesHistory(c *gin.Context) {
	m, ok := s.updateManager(c)
	if !ok {
		return
	}
	snap := m.Snapshot()
	c.JSON(http.StatusOK, gin.H{"history": snap.History})
}

func (s *Server) updatesCheck(c *gin.Context) {
	m, ok := s.updateManager(c)
	if !ok {
		return
	}
	snap, err := m.Check(c.Request.Context())
	if err != nil && !errors.Is(err, update.ErrNoRelease) {
		updateError(c, err)
		return
	}
	c.JSON(http.StatusOK, snap)
}

func (s *Server) updatesDownload(c *gin.Context) {
	m, ok := s.updateManager(c)
	if !ok {
		return
	}
	snap, err := m.Download(c.Request.Context())
	if err != nil {
		updateError(c, err)
		return
	}
	c.JSON(http.StatusOK, snap)
}

func (s *Server) updatesApply(c *gin.Context) {
	m, ok := s.updateManager(c)
	if !ok {
		return
	}
	snap, err := m.Apply(c.Request.Context())
	if err != nil {
		updateError(c, err)
		return
	}
	c.JSON(http.StatusOK, snap)
}

func (s *Server) updatesRollback(c *gin.Context) {
	m, ok := s.updateManager(c)
	if !ok {
		return
	}
	snap, err := m.Rollback(c.Request.Context())
	if err != nil {
		updateError(c, err)
		return
	}
	c.JSON(http.StatusOK, snap)
}

func updateError(c *gin.Context, err error) {
	status, code, message := http.StatusBadGateway, "update_failed", "在线升级失败"
	switch {
	case errors.Is(err, update.ErrDisabled):
		status, code, message = http.StatusNotImplemented, "update_disabled", "在线升级未启用"
	case errors.Is(err, update.ErrApplyUnavailable):
		status, code, message = http.StatusConflict, "launcher_required", "升级包已就绪，需要启动器执行替换和重启"
	case errors.Is(err, update.ErrRollbackUnavailable):
		status, code, message = http.StatusConflict, "launcher_required", "回滚需要启动器执行"
	case errors.Is(err, update.ErrNoRelease):
		status, code, message = http.StatusConflict, "no_compatible_release", "没有可用的兼容版本"
	case errors.Is(err, context.Canceled):
		status, code, message = http.StatusRequestTimeout, "canceled", "操作已取消"
	}
	fail(c, status, code, message)
}
