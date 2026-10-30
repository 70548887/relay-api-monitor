package server

import (
	"context"
	"errors"
	"net/http"
	"relaylab/internal/update"
	"time"

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
	c.JSON(http.StatusOK, s.updateSnapshot(c.Request.Context(), m))
}

func (s *Server) updateSnapshot(ctx context.Context, m *update.Manager) any {
	paused, active, _ := s.RuntimeState()
	counts, _ := s.Store.JobStateCounts(ctx)
	worker, _ := s.Store.WorkerHealth(ctx)
	return struct {
		update.Snapshot
		LauncherManaged  bool `json:"launcher_managed"`
		LauncherProtocol int  `json:"launcher_protocol"`
		AutoRestart      bool `json:"auto_restart"`
		Maintenance      any  `json:"maintenance"`
	}{m.Snapshot(), s.launcherManaged, s.launcherProtocol, m.Config().AutoRestart,
		gin.H{"active": paused, "draining_requests": active, "running_jobs": counts["claimed"] + counts["dispatching"], "worker_online": worker.Online}}
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
	s.startUpdateOperation(c, "check")
}

func (s *Server) updatesDownload(c *gin.Context) {
	s.startUpdateOperation(c, "download")
}

func (s *Server) updatesApply(c *gin.Context) {
	s.startUpdateOperation(c, "apply")
}

func (s *Server) updatesRollback(c *gin.Context) {
	s.startUpdateOperation(c, "rollback")
}

// Long downloads are owned by the server lifetime. Browser disconnects do not
// abandon a verified package or leave an install half-started.
func (s *Server) startUpdateOperation(c *gin.Context, action string) {
	m, ok := s.updateManager(c)
	if !ok {
		return
	}
	if !m.Config().Enabled {
		updateError(c, update.ErrDisabled)
		return
	}
	if action != "check" && !m.CanInstall() {
		updateError(c, update.ErrContainerManaged)
		return
	}
	release, reserved := m.TryReserveOperation()
	if !reserved {
		updateError(c, update.ErrBusy)
		return
	}
	handedOff := false
	defer func() {
		if !handedOff {
			release()
		}
	}()
	snapshot := m.Snapshot()
	switch snapshot.State {
	case "accepted", "checking", "downloading", "draining", "stopping", "stopped", "backing_up", "backed_up", "replacing", "starting", "validating", "rolling_back", "recovering", "recovery_required", "pending_restart":
		fail(c, http.StatusConflict, "update_busy", "已有升级操作正在执行或等待重启")
		return
	}
	if action == "apply" && snapshot.State != "ready" {
		fail(c, http.StatusConflict, "update_not_ready", "请先下载并校验目标版本")
		return
	}
	if (action == "apply" || action == "rollback") && !s.launcherManaged {
		fail(c, http.StatusConflict, "launcher_required", "需要协议 2 的启动器；旧版请先使用完整安装包迁移")
		return
	}
	if !s.updateBusy.CompareAndSwap(false, true) {
		fail(c, 409, "update_busy", "已有升级操作正在执行")
		return
	}
	parent := s.updateContext
	if parent == nil {
		parent = context.Background()
	}
	go func() {
		defer release()
		defer s.updateBusy.Store(false)
		ctx, cancel := context.WithTimeout(parent, 15*time.Minute)
		defer cancel()
		switch action {
		case "check":
			_, _ = m.Check(ctx)
		case "download":
			_, _ = m.Download(ctx)
		case "apply":
			_, _ = m.Apply(ctx)
		case "rollback":
			_, _ = m.Rollback(ctx)
		}
	}()
	handedOff = true
	c.JSON(http.StatusAccepted, s.updateSnapshot(c.Request.Context(), m))
}

func updateError(c *gin.Context, err error) {
	status, code, message := http.StatusBadGateway, "update_failed", "在线升级失败"
	switch {
	case errors.Is(err, update.ErrBusy):
		status, code, message = http.StatusConflict, "update_busy", "已有升级操作正在执行"
	case errors.Is(err, update.ErrDisabled):
		status, code, message = http.StatusNotImplemented, "update_disabled", "在线升级未启用"
	case errors.Is(err, update.ErrContainerManaged):
		status, code, message = http.StatusConflict, "update_container_managed", "Docker 部署支持检查新版本；请在宝塔重新构建并创建容器以完成升级，保留原有数据卷"
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
