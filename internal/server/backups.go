package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"relaylab/internal/backup"
	"relaylab/internal/backups"
	"relaylab/internal/domain"
	"relaylab/internal/launcher"
	"relaylab/internal/operationlock"
	"relaylab/internal/restorestate"
	"relaylab/internal/security"
)

const backupImportLimit int64 = 3 << 30
const backupUploadTimeout = 30 * time.Minute

// Applied only after administrator authentication, reauthentication and I/O
// admission. Other request bodies keep the server's short default deadline.
func extendBackupReadDeadline(w http.ResponseWriter, duration time.Duration) error {
	err := http.NewResponseController(w).SetReadDeadline(time.Now().Add(duration))
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}
func backupImportReadError(c *gin.Context, e error) {
	var limit *http.MaxBytesError
	var timeout net.Error
	if errors.As(e, &limit) {
		fail(c, 413, "import_too_large", "备份导入超过 3 GiB 限制")
		return
	}
	if errors.As(e, &timeout) && timeout.Timeout() || errors.Is(e, context.DeadlineExceeded) {
		fail(c, 408, "import_timeout", "备份上传超时，请检查连接速度后重试")
		return
	}
	if errors.Is(e, context.Canceled) {
		fail(c, 408, "import_canceled", "备份上传已取消")
		return
	}
	fail(c, 400, "import_read_failed", "备份上传未完整接收，请重新上传")
}

type restoreSubmission struct {
	OperationID string    `json:"operation_id"`
	Unknown     bool      `json:"unknown"`
	At          time.Time `json:"at"`
}

func submissionPath(dataDir, id string) string {
	return filepath.Join(restorestate.Root(dataDir), "operations", id+".submission.json")
}
func restorePublicState(dataDir string, op restorestate.Operation) map[string]any {
	v := restorestate.Public(op)
	if op.State == "accepted" {
		var submission restoreSubmission
		if restorestate.Read(submissionPath(dataDir, op.ID), &submission) == nil && submission.OperationID == op.ID && submission.Unknown {
			v["state"] = "unknown"
			v["message"] = "提交结果待核对，请查看启动器状态；请勿重复执行"
			v["error_code"] = "launcher_submission_unknown"
		}
	}
	return v
}

type backupHTTP struct {
	manager    *backups.Manager
	dataDir    string
	ctx        context.Context
	native     bool
	deployment string
	apply      func(context.Context, launcher.RestoreRequest) error
	mu         sync.Mutex
	verified   map[string]time.Time
	ioSlots    chan struct{}
}

func (s *Server) ConfigureBackups(ctx context.Context, m *backups.Manager, dataDir string) {
	s.backupHTTP = &backupHTTP{manager: m, dataDir: dataDir, ctx: ctx, deployment: "unmanaged", verified: map[string]time.Time{}, ioSlots: make(chan struct{}, 2)}
	if os.Getenv("RELAYLAB_CONTAINER") == "true" {
		s.backupHTTP.deployment = "docker"
	}
	if os.Getenv("RELAYLAB_UPDATE_INSTALL_MODE") == "container" {
		s.backupHTTP.deployment = "docker"
	}
	if _, e := os.Stat("/.dockerenv"); e == nil {
		s.backupHTTP.deployment = "docker"
	}
	go func() {
		cleanupOrphanRestoreSubmissions(dataDir, time.Now())
		restorestate.Cleanup(dataDir, time.Now())
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				cleanupOrphanRestoreSubmissions(dataDir, now)
				restorestate.Cleanup(dataDir, now)
			}
		}
	}()
}

func (s *Server) backupIOAdmission(c *gin.Context) (func(), bool) {
	select {
	case s.backupHTTP.ioSlots <- struct{}{}:
		return func() { <-s.backupHTTP.ioSlots }, true
	default:
		c.Header("Retry-After", "2")
		fail(c, 429, "backup_io_busy", "备份传输或校验正在执行，请稍后重试")
		return nil, false
	}
}

func cleanupOrphanRestoreSubmissions(dataDir string, now time.Time) {
	release, e := operationlock.Acquire(dataDir)
	if e != nil {
		return
	}
	defer release()
	var current restorestate.Current
	currentErr := restorestate.ReadCurrent(dataDir, &current)
	if currentErr != nil && !os.IsNotExist(currentErr) {
		return
	}
	entries, e := os.ReadDir(filepath.Join(restorestate.Root(dataDir), "operations"))
	if e != nil {
		return
	}
	for _, entry := range entries {
		id := strings.TrimSuffix(entry.Name(), ".json")
		if entry.IsDir() || !restorestate.ValidID(id) {
			continue
		}
		var op restorestate.Operation
		if restorestate.ReadOperation(dataDir, id, &op) != nil || op.State != "accepted" || op.DataChanged || op.StartedAt.IsZero() || now.Sub(op.StartedAt) < time.Hour || current.ID == id {
			continue
		}
		var p restorestate.Preflight
		if restorestate.ReadPreflight(dataDir, op.PreflightID, &p) != nil || !now.After(p.ExpiresAt) {
			continue
		}
		op.State = "failed"
		op.ErrorCode = "submission_expired"
		op.Message = "恢复提交未获确认且预检已过期，当前没有对应恢复事务"
		op.UpdatedAt = now.UTC()
		_ = restorestate.SaveOperation(dataDir, op)
	}
}
func (s *Server) ConfigureBackupRestore(native bool, apply func(context.Context, launcher.RestoreRequest) error) {
	if s.backupHTTP != nil {
		s.backupHTTP.native = native
		s.backupHTTP.apply = apply
		if native && s.backupHTTP.deployment != "docker" {
			s.backupHTTP.deployment = "launcher"
		}
	}
}
func (s *Server) EnterBackupActivity() (func(), bool) { return s.gate.Enter() }
func (s *Server) backupManager(c *gin.Context) (*backups.Manager, bool) {
	if s.backupHTTP == nil || s.backupHTTP.manager == nil {
		fail(c, 503, "backup_unavailable", "备份管理器未运行")
		return nil, false
	}
	return s.backupHTTP.manager, true
}
func backupError(c *gin.Context, e error) {
	if errors.Is(e, sql.ErrNoRows) || errors.Is(e, os.ErrNotExist) {
		fail(c, 404, "backup_not_found", "备份或操作不存在")
		return
	}
	if errors.Is(e, backups.ErrBusy) || errors.Is(e, operationlock.ErrBusy) || errors.Is(e, backups.ErrFrozen) || errors.Is(e, backups.ErrProtected) || errors.Is(e, backups.ErrKeyNotVerified) {
		fail(c, 409, "backup_conflict", e.Error())
		return
	}
	// Service errors never include request bodies or credential values.
	fail(c, 400, "backup_failed", e.Error())
}
func (s *Server) registerBackups(a *gin.RouterGroup) {
	b := a.Group("/backups")
	b.Use(func(c *gin.Context) {
		c.Next()
		if c.Writer.Status() < 300 && c.Request.Method != "GET" && c.Request.Method != "HEAD" && c.FullPath() != "/api/v1/admin/backups/storage/test" {
			s.auditEvent(c.Request.Context(), "backup_management", c.Param("id"), "管理员操作 "+c.Request.Method+" "+c.FullPath())
		}
	})
	b.GET("/status", s.backupStatus)
	b.GET("/storage", s.backupStorage)
	b.PUT("/storage", s.backupStorageSave)
	b.POST("/storage/test", s.backupStorageTest)
	b.GET("/schedule", s.backupSchedule)
	b.PUT("/schedule", s.backupScheduleSave)
	b.POST("/schedule/preview", s.backupSchedulePreview)
	b.POST("/schedule/retention-preview", s.backupRetentionPreview)
	b.GET("/records", s.backupRecords)
	b.POST("/records", s.backupCreate)
	b.GET("/records/:id", s.backupRecord)
	b.GET("/records/:id/download", s.backupDownload)
	b.POST("/records/:id/retry-upload", s.backupRetry)
	b.PATCH("/records/:id/protection", s.backupProtect)
	b.DELETE("/records/:id", s.backupDelete)
	b.POST("/records/:id/acknowledge-missing", s.backupAcknowledgeMissing)
	b.POST("/reauth", s.backupReauth)
	b.POST("/keys/generate", s.backupGenerateKey)
	b.PUT("/keys/recipient", s.backupRecipient)
	b.POST("/keys/verify", s.backupVerifyKey)
	b.POST("/restore/preflight", s.backupPreflight)
	b.POST("/restore/apply", s.backupApply)
	b.POST("/reconcile", s.backupReconcile)
	b.POST("/cloud/scan", s.backupCloudScan)
	b.POST("/cloud/import", s.backupCloudImport)
}

func (s *Server) backupCloudScan(c *gin.Context) {
	m, ok := s.backupManager(c)
	if !ok {
		return
	}
	leave, ok := s.backupIOAdmission(c)
	if !ok {
		return
	}
	defer leave()
	var b struct {
		Continuation string `json:"continuation_token"`
	}
	if !bind(c, &b) {
		return
	}
	page, e := m.ScanCloud(c.Request.Context(), b.Continuation)
	if e != nil {
		backupError(c, e)
		return
	}
	c.JSON(200, page)
}
func (s *Server) backupCloudImport(c *gin.Context) {
	m, ok := s.backupManager(c)
	if !ok || !s.backupRecentlyVerified(c) {
		return
	}
	leave, ok := s.backupIOAdmission(c)
	if !ok {
		return
	}
	defer leave()
	var b struct {
		CandidateID string `json:"candidate_id"`
		Identity    string `json:"identity"`
	}
	if !bind(c, &b) {
		return
	}
	run, e := m.ImportCloud(c.Request.Context(), b.CandidateID, b.Identity)
	b.Identity = ""
	if e != nil {
		backupError(c, e)
		return
	}
	c.JSON(200, run)
}
func (s *Server) backupStatus(c *gin.Context) {
	m, ok := s.backupManager(c)
	if !ok {
		return
	}
	v, e := m.Snapshot(c.Request.Context())
	if e != nil {
		backupError(c, e)
		return
	}
	raw, _ := json.Marshal(v)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	body["native_restore_supported"] = s.backupHTTP.native && s.backupHTTP.deployment != "docker"
	body["deployment_kind"] = s.backupHTTP.deployment
	var pointer restorestate.Current
	if restorestate.ReadCurrent(s.backupHTTP.dataDir, &pointer) == nil {
		var op restorestate.Operation
		if restorestate.ReadOperation(s.backupHTTP.dataDir, pointer.ID, &op) == nil {
			body["restore_operation"] = restorePublicState(s.backupHTTP.dataDir, op)
		}
	}
	c.JSON(200, body)
}
func (s *Server) backupStorage(c *gin.Context) {
	m, ok := s.backupManager(c)
	if !ok {
		return
	}
	v, e := m.Snapshot(c.Request.Context())
	if e != nil {
		backupError(c, e)
		return
	}
	c.JSON(200, v.Storage)
}
func (s *Server) backupStorageSave(c *gin.Context) {
	m, ok := s.backupManager(c)
	if !ok {
		return
	}
	var b backups.StorageInput
	if !bind(c, &b) {
		return
	}
	v, e := m.SaveStorage(c.Request.Context(), b)
	if e != nil {
		backupError(c, e)
		return
	}
	c.JSON(200, v)
}
func (s *Server) backupStorageTest(c *gin.Context) {
	m, ok := s.backupManager(c)
	if !ok {
		return
	}
	leave, ok := s.backupIOAdmission(c)
	if !ok {
		return
	}
	defer leave()
	var b backups.StorageInput
	if !bind(c, &b) {
		s.recordStorageTest(c, domain.BackupStorageTestResult{Status: "failed", Stage: "validating", ErrorCode: "invalid_request", Message: "连接测试请求格式无效，未发送云端请求"})
		return
	}
	log.Printf("backup_storage_test_started request_id=%s", c.Writer.Header().Get("X-Request-ID"))
	result, err := m.TestStorageDetailed(c.Request.Context(), b)
	result.RequestID = c.Writer.Header().Get("X-Request-ID")
	if err != nil {
		// Never use the raw SDK error or request body as an API/log message.
		if result.ErrorCode == "" {
			result.Status, result.ErrorCode, result.Message = "failed", "storage_test_failed", "存储连接测试失败，请使用诊断编号核对日志"
		}
		s.recordStorageTest(c, result)
		status := http.StatusBadRequest
		if strings.Contains(result.ErrorCode, "timeout") {
			status = http.StatusGatewayTimeout
		} else if result.ErrorCode == "canceled" {
			status = http.StatusRequestTimeout
		} else if result.HTTPStatus >= 400 {
			// An upstream 401/403 must not revoke the administrator's session.
			status = http.StatusBadGateway
		}
		c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"code": result.ErrorCode, "message": result.Message}, "result": result, "request_id": result.RequestID})
		return
	}
	s.recordStorageTest(c, result)
	c.JSON(200, gin.H{"ok": true, "result": result})
}

func (s *Server) recordStorageTest(c *gin.Context, result domain.BackupStorageTestResult) {
	result.RequestID = c.Writer.Header().Get("X-Request-ID")
	// The result contains only fixed diagnostic categories/messages, no endpoint,
	// object path, credentials, authorization headers or upstream response text.
	encoded, err := json.Marshal(result)
	if err != nil {
		return
	}
	log.Printf("backup_storage_test %s", encoded)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err = s.Store.AddAudit(ctx, domain.AuditEvent{ID: domain.NewID(), Action: "backup_storage_test", EntityID: result.RequestID, Detail: string(encoded), CreatedAt: time.Now().UTC()}); err != nil {
		log.Printf("backup_storage_test_audit_failed request_id=%s", result.RequestID)
	}
}
func (s *Server) backupSchedule(c *gin.Context) {
	m, ok := s.backupManager(c)
	if !ok {
		return
	}
	v, e := m.Snapshot(c.Request.Context())
	if e != nil {
		backupError(c, e)
		return
	}
	c.JSON(200, v.Schedule)
}
func (s *Server) backupScheduleSave(c *gin.Context) {
	m, ok := s.backupManager(c)
	if !ok {
		return
	}
	var b backups.Schedule
	if !bind(c, &b) {
		return
	}
	v, e := m.SaveSchedule(c.Request.Context(), b)
	if e != nil {
		backupError(c, e)
		return
	}
	c.JSON(200, v)
}
func (s *Server) backupSchedulePreview(c *gin.Context) {
	m, ok := s.backupManager(c)
	if !ok {
		return
	}
	var b backups.Schedule
	if !bind(c, &b) {
		return
	}
	v, e := m.Preview(b)
	if e != nil {
		backupError(c, e)
		return
	}
	c.JSON(200, gin.H{"next_runs": v})
}

func (s *Server) backupRetentionPreview(c *gin.Context) {
	m, ok := s.backupManager(c)
	if !ok {
		return
	}
	var policy backups.Schedule
	if !bind(c, &policy) {
		return
	}
	// A caller cannot clear the post-restore freeze through a preview body.
	snapshot, e := m.Snapshot(c.Request.Context())
	if e != nil {
		backupError(c, e)
		return
	}
	policy.ReconciliationRequired = snapshot.Schedule.ReconciliationRequired
	preview, e := m.PreviewRetention(c.Request.Context(), policy)
	if e != nil {
		backupError(c, e)
		return
	}
	items := make([]backups.Run, 0, len(preview.BackupIDs))
	local, remote := 0, 0
	for _, id := range preview.BackupIDs {
		run, e := m.Record(c.Request.Context(), id)
		if e != nil {
			backupError(c, e)
			return
		}
		items = append(items, run)
		if run.LocalState == "verified" {
			local++
		}
		if run.RemoteState == "verified" {
			remote++
		}
	}
	c.JSON(200, gin.H{"count": preview.DeleteCount, "local_copies": local, "remote_copies": remote, "total_bytes": preview.LocalBytes + preview.RemoteBytes, "items": items})
}
func (s *Server) backupRecords(c *gin.Context) {
	m, ok := s.backupManager(c)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	v, e := m.ListFiltered(c.Request.Context(), backups.Filter{Limit: limit, Offset: offset, State: c.Query("state"), Trigger: c.Query("trigger"), Protection: c.Query("protection"), From: c.Query("from"), To: c.Query("to")})
	if e != nil {
		backupError(c, e)
		return
	}
	c.JSON(200, v)
}
func (s *Server) backupRecord(c *gin.Context) {
	m, ok := s.backupManager(c)
	if !ok {
		return
	}
	v, e := m.Record(c.Request.Context(), c.Param("id"))
	if e != nil {
		backupError(c, e)
		return
	}
	c.JSON(200, v)
}
func (s *Server) backupCreate(c *gin.Context) {
	m, ok := s.backupManager(c)
	if !ok {
		return
	}
	var b struct {
		RequestID string `json:"request_id"`
	}
	if !bind(c, &b) {
		return
	}
	if len(b.RequestID) < 8 || len(b.RequestID) > 100 {
		fail(c, 400, "invalid_request_id", "请提供有效的幂等请求标识")
		return
	}
	v, e := m.Create(c.Request.Context(), b.RequestID)
	if e != nil {
		backupError(c, e)
		return
	}
	c.JSON(202, v)
}
func (s *Server) backupRetry(c *gin.Context) {
	m, ok := s.backupManager(c)
	if !ok {
		return
	}
	v, e := m.RetryUpload(c.Request.Context(), c.Param("id"))
	if e != nil {
		backupError(c, e)
		return
	}
	c.JSON(202, v)
}
func (s *Server) backupReauth(c *gin.Context) {
	if _, ok := s.backupManager(c); !ok {
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
	u := c.MustGet("admin").(*domain.Admin)
	if !security.VerifyPassword(u.PasswordHash, b.Password) {
		fail(c, 403, "invalid_password", "管理员密码不正确")
		return
	}
	expires := time.Now().Add(5 * time.Minute)
	s.backupHTTP.mu.Lock()
	for k, t := range s.backupHTTP.verified {
		if time.Now().After(t) {
			delete(s.backupHTTP.verified, k)
		}
	}
	s.backupHTTP.verified[c.MustGet("session").(*domain.Session).TokenHash] = expires
	s.backupHTTP.mu.Unlock()
	c.JSON(200, gin.H{"ok": true, "expires_at": expires})
}
func (s *Server) backupRecentlyVerified(c *gin.Context) bool {
	if _, ok := s.backupManager(c); !ok {
		return false
	}
	ss := c.MustGet("session").(*domain.Session)
	s.backupHTTP.mu.Lock()
	expires := s.backupHTTP.verified[ss.TokenHash]
	s.backupHTTP.mu.Unlock()
	if time.Now().After(expires) {
		fail(c, 403, "reauth_required", "请再次验证管理员密码")
		return false
	}
	return true
}
func (s *Server) backupDownload(c *gin.Context) {
	m, ok := s.backupManager(c)
	if !ok || !s.backupRecentlyVerified(c) {
		return
	}
	leave, ok := s.gate.Enter()
	if !ok {
		fail(c, 503, "maintenance_mode", "系统正在维护，请稍后下载")
		return
	}
	defer leave()
	leaveIO, ok := s.backupIOAdmission(c)
	if !ok {
		return
	}
	defer leaveIO()
	f, run, e := m.OpenDownload(c.Request.Context(), c.Param("id"))
	if e != nil {
		remote, recordErr := m.Record(c.Request.Context(), c.Param("id"))
		if recordErr != nil || remote.LocalState == "verified" || remote.RemoteState != "verified" {
			backupError(c, e)
			return
		}
		materialized, err := m.Materialize(c.Request.Context(), remote.ID)
		if err != nil {
			backupError(c, err)
			return
		}
		defer os.Remove(materialized)
		f, err = os.Open(materialized)
		if err != nil {
			backupError(c, err)
			return
		}
		run = remote
	}
	paced := backups.PaceTransfer(c.Request.Context(), f)
	defer paced.Close()
	c.DataFromReader(200, run.SizeBytes, "application/octet-stream", paced, map[string]string{"Content-Disposition": mime.FormatMediaType("attachment", map[string]string{"filename": run.FileName})})
}
func (s *Server) backupProtect(c *gin.Context) {
	m, ok := s.backupManager(c)
	if !ok {
		return
	}
	var b struct {
		Protected bool `json:"protected"`
	}
	if !bind(c, &b) {
		return
	}
	if !b.Protected && !s.backupRecentlyVerified(c) {
		return
	}
	if e := m.Protect(c.Request.Context(), c.Param("id"), b.Protected); e != nil {
		backupError(c, e)
		return
	}
	c.JSON(200, gin.H{"ok": true})
}
func (s *Server) backupDelete(c *gin.Context) {
	m, ok := s.backupManager(c)
	if !ok || !s.backupRecentlyVerified(c) {
		return
	}
	if e := m.Delete(c.Request.Context(), c.Param("id")); e != nil {
		backupError(c, e)
		return
	}
	c.JSON(200, gin.H{"ok": true})
}
func (s *Server) backupAcknowledgeMissing(c *gin.Context) {
	m, ok := s.backupManager(c)
	if !ok || !s.backupRecentlyVerified(c) {
		return
	}
	var b struct {
		Confirm string `json:"confirm"`
	}
	if !bind(c, &b) {
		return
	}
	if b.Confirm != "确认副本遗失" {
		fail(c, 400, "confirmation_required", "请明确确认旧本地副本已遗失")
		return
	}
	if e := m.AcknowledgeMissing(c.Request.Context(), c.Param("id")); e != nil {
		backupError(c, e)
		return
	}
	c.JSON(200, gin.H{"ok": true})
}
func (s *Server) backupGenerateKey(c *gin.Context) {
	if _, ok := s.backupManager(c); !ok {
		return
	}
	identity, recipient, e := backup.GenerateIdentity()
	if e != nil {
		fail(c, 500, "key_generation_failed", "恢复密钥生成失败")
		return
	}
	c.JSON(200, gin.H{"identity": identity, "recipient": recipient})
}
func (s *Server) backupRecipient(c *gin.Context) {
	m, ok := s.backupManager(c)
	if !ok {
		return
	}
	var b struct {
		Recipient string `json:"recipient"`
	}
	if !bind(c, &b) {
		return
	}
	if e := m.SetRecipient(c.Request.Context(), b.Recipient); e != nil {
		backupError(c, e)
		return
	}
	c.JSON(200, gin.H{"ok": true})
}
func (s *Server) backupVerifyKey(c *gin.Context) {
	m, ok := s.backupManager(c)
	if !ok {
		return
	}
	var b struct {
		Identity string `json:"identity"`
	}
	if !bind(c, &b) {
		return
	}
	if e := m.ValidateIdentity(c.Request.Context(), b.Identity); e != nil {
		backupError(c, e)
		return
	}
	c.JSON(200, gin.H{"ok": true})
}
func (s *Server) backupReconcile(c *gin.Context) {
	m, ok := s.backupManager(c)
	if !ok || !s.backupRecentlyVerified(c) {
		return
	}
	leave, ok := s.backupIOAdmission(c)
	if !ok {
		return
	}
	defer leave()
	if e := m.Reconcile(c.Request.Context()); e != nil {
		backupError(c, e)
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

func (s *Server) backupPreflight(c *gin.Context) {
	m, ok := s.backupManager(c)
	if !ok || !s.backupRecentlyVerified(c) {
		return
	}
	leave, ok := s.backupIOAdmission(c)
	if !ok {
		return
	}
	defer leave()
	id := domain.NewID()
	root := restorestate.Stage(s.backupHTTP.dataDir, id)
	if e := os.MkdirAll(root, 0700); e != nil {
		fail(c, 500, "staging_failed", "无法创建恢复暂存目录")
		return
	}
	if e := backup.ProtectDirectory(root); e != nil {
		_ = os.RemoveAll(root)
		fail(c, 500, "staging_failed", "恢复暂存目录权限设置失败")
		return
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(root)
		}
	}()
	var recordID, identity, input string
	if strings.HasPrefix(c.ContentType(), "multipart/") {
		if e := extendBackupReadDeadline(c.Writer, backupUploadTimeout); e != nil {
			fail(c, 500, "import_deadline_failed", "无法设置备份上传期限")
			return
		}
		mr, e := c.Request.MultipartReader()
		if e != nil {
			fail(c, 400, "invalid_import", "无效的备份导入")
			return
		}
		for {
			part, e := mr.NextPart()
			if e == io.EOF {
				break
			}
			if e != nil {
				backupImportReadError(c, e)
				return
			}
			switch part.FormName() {
			case "identity", "record_id":
				b, e := io.ReadAll(io.LimitReader(part, 8193))
				if e != nil {
					backupImportReadError(c, e)
					return
				}
				if len(b) > 8192 {
					fail(c, 400, "invalid_import", "导入字段过长")
					return
				}
				if part.FormName() == "identity" {
					identity = string(b)
				} else {
					recordID = string(b)
				}
			case "file":
				if input != "" {
					fail(c, 400, "invalid_import", "仅允许一个备份文件")
					return
				}
				input = filepath.Join(root, "input.archive")
				var required uint64 = 64 << 20
				if c.Request.ContentLength > 0 {
					required += uint64(c.Request.ContentLength)
				}
				if e := backup.EnsureSpace(root, required); e != nil {
					fail(c, 507, "insufficient_storage", "恢复导入暂存空间不足")
					return
				}
				f, e := os.OpenFile(input, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
				if e != nil {
					fail(c, 500, "staging_failed", "暂存失败")
					return
				}
				n, e := io.Copy(f, io.LimitReader(part, backupImportLimit+1))
				syncErr := f.Sync()
				closeErr := f.Close()
				if e != nil {
					backupImportReadError(c, e)
					return
				}
				if n > backupImportLimit {
					fail(c, 413, "import_too_large", "备份文件超过 3 GiB 限制")
					return
				}
				if syncErr != nil || closeErr != nil {
					fail(c, 507, "import_storage_failed", "备份暂存写入失败，请检查磁盘空间")
					return
				}
			default:
				fail(c, 400, "invalid_import", "不支持的导入字段")
				return
			}
			_ = part.Close()
		}
	} else {
		var b struct {
			RecordID string `json:"record_id"`
			Identity string `json:"identity"`
		}
		if !bind(c, &b) {
			return
		}
		recordID, identity = b.RecordID, b.Identity
	}
	if input != "" && recordID != "" {
		fail(c, 400, "invalid_import", "请选择备份记录或导入文件其中之一")
		return
	}
	if input == "" {
		var e error
		input, e = m.Materialize(c.Request.Context(), recordID)
		if e != nil {
			backupError(c, e)
			return
		}
		defer os.Remove(input)
	}
	validated, e := backup.Validate(c.Request.Context(), input, identity, root)
	identity = ""
	if e != nil {
		fail(c, 400, "invalid_backup", "备份、恢复私钥或数据校验失败；当前数据未改变")
		return
	}
	dbhash, _, e := launcher.SHA256File(validated.DBPath)
	if e != nil {
		fail(c, 500, "preflight_failed", "快照读取失败")
		return
	}
	keyhash, _, e := launcher.SHA256File(validated.KeyPath)
	if e != nil {
		fail(c, 500, "preflight_failed", "主密钥读取失败")
		return
	}
	manifest, _ := json.Marshal(validated.Manifest)
	p := restorestate.Preflight{ID: id, BackupID: recordID, Dir: validated.Dir, DBHash: dbhash, KeyHash: keyhash, Manifest: manifest, ExpiresAt: time.Now().UTC().Add(30 * time.Minute)}
	if e = restorestate.SavePreflight(s.backupHTTP.dataDir, p); e != nil {
		fail(c, 500, "preflight_failed", "预检结果保存失败")
		return
	}
	keep = true
	instructions := []string{"下载并保管加密备份及对应恢复私钥。", "停止服务，使用随包 restore-docker.sh 将备份恢复到新的空命名卷。", "验证新卷后设置 RELAYLAB_DATA_VOLUME 并重新创建容器；保留旧卷。"}
	if s.backupHTTP.deployment != "docker" {
		instructions = []string{
			"下载并独立保管加密备份及对应恢复私钥；先停止 launcher、serve 和 worker，保留原数据目录。",
			"使用 relaylab verify-backup --input 备份路径 --identity-file 私钥文件 先校验。",
			"使用 relaylab restore --input 备份路径 --identity-file 私钥文件 --data-dir 新的空数据目录；旧版明文 ZIP 可省略私钥参数。",
			"恢复校验成功后修改配置指向新数据目录，再启动新版 launcher 并核对历史与备份索引；保留原数据目录。",
		}
	}
	c.JSON(200, gin.H{"id": id, "backup_id": recordID, "manifest": validated.Manifest, "expires_at": p.ExpiresAt, "native_restore_supported": s.backupHTTP.native && s.backupHTTP.deployment != "docker", "deployment_kind": s.backupHTTP.deployment, "docker_instructions": instructions})
}
func (s *Server) backupApply(c *gin.Context) {
	if _, ok := s.backupManager(c); !ok || !s.backupRecentlyVerified(c) {
		return
	}
	b := s.backupHTTP
	if !b.native || b.apply == nil || b.deployment == "docker" {
		fail(c, 409, "restore_launcher_required", "当前部署请使用恢复脚本；原生在线恢复需要支持 restore_v1 的启动器")
		return
	}
	var req struct {
		ID string `json:"preflight_id"`
	}
	if !bind(c, &req) {
		return
	}
	if !restorestate.ValidID(req.ID) {
		fail(c, 404, "preflight_not_found", "预检不存在")
		return
	}
	b.mu.Lock()
	var p restorestate.Preflight
	if e := restorestate.ReadPreflight(b.dataDir, req.ID, &p); e != nil || time.Now().After(p.ExpiresAt) {
		b.mu.Unlock()
		fail(c, 409, "preflight_expired", "请重新预检备份")
		return
	}
	if p.OperationID != "" {
		b.mu.Unlock()
		c.JSON(409, gin.H{"error": gin.H{"code": "preflight_consumed", "message": "该预检已提交，请查看原操作结果或重新预检"}, "operation_id": p.OperationID})
		return
	}
	token := security.Token()
	id := domain.NewID()
	op := restorestate.Operation{ID: id, PreflightID: p.ID, State: "accepted", Message: "恢复任务已受理", StartedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), TokenHash: security.HashToken(token), TokenExpiresAt: time.Now().Add(time.Hour)}
	if e := restorestate.SaveOperation(b.dataDir, op); e != nil {
		b.mu.Unlock()
		fail(c, 500, "restore_failed", "保存恢复任务失败")
		return
	}
	p.OperationID = id
	if e := restorestate.SavePreflight(b.dataDir, p); e != nil {
		b.mu.Unlock()
		fail(c, 500, "restore_failed", "保存预检提交标记失败，未发送恢复请求")
		return
	}
	b.mu.Unlock()
	// The HTTP admission must return before the launcher waits for active writes.
	go func() {
		ctx, cancel := context.WithTimeout(b.ctx, 15*time.Minute)
		defer cancel()
		if e := b.apply(ctx, launcher.RestoreRequest{OperationID: id, PreflightID: p.ID}); e != nil {
			var rejected interface{ RestoreRejected() bool }
			if errors.As(e, &rejected) && rejected.RestoreRejected() {
				op.State = "failed"
				op.Message = "启动器已拒绝恢复，当前数据未改变"
				op.ErrorCode = "launcher_rejected"
				op.UpdatedAt = time.Now().UTC()
				_ = restorestate.SaveOperation(b.dataDir, op)
			} else {
				// A missing response cannot overwrite the launcher's concurrently
				// advanced recovery journal. This file is display-only metadata.
				_ = restorestate.Save(submissionPath(b.dataDir, id), restoreSubmission{OperationID: id, Unknown: true, At: time.Now().UTC()})
			}
		}
	}()
	c.JSON(202, gin.H{"operation_id": id, "status_token": token})
}
func (s *Server) backupOperation(c *gin.Context) {
	if s.backupHTTP == nil || !restorestate.ValidID(c.Param("id")) {
		fail(c, 404, "operation_not_found", "操作不存在")
		return
	}
	var op restorestate.Operation
	e := restorestate.ReadOperation(s.backupHTTP.dataDir, c.Param("id"), &op)
	if e != nil {
		fail(c, 404, "operation_not_found", "操作不存在")
		return
	}
	token := c.GetHeader("X-Restore-Status-Token")
	if token == "" {
		s.requireAuth(c)
		if c.IsAborted() {
			return
		}
	} else if time.Now().After(op.TokenExpiresAt) || !security.ConstantEqual(security.HashToken(token), op.TokenHash) {
		fail(c, 404, "operation_not_found", "操作不存在")
		return
	}
	c.JSON(200, restorePublicState(s.backupHTTP.dataDir, op))
}
