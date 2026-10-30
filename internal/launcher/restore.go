package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"relaylab/internal/backup"
	"relaylab/internal/operationlock"
	"relaylab/internal/restorestate"
	"time"
)

type RestoreRequest struct {
	OperationID string `json:"operation_id"`
	PreflightID string `json:"preflight_id"`
}

func (s *Supervisor) restoreCurrent() (*restorestate.Operation, error) {
	var pointer restorestate.Current
	e := restorestate.ReadCurrent(s.opts.DataDir, &pointer)
	if os.IsNotExist(e) {
		canonical, ownerErr := operationlock.CanonicalDataDir(s.opts.DataDir)
		if ownerErr != nil {
			return nil, ownerErr
		}
		legacy := filepath.Join(filepath.Dir(canonical), "backups", ".restore", "current.json")
		if _, legacyErr := os.Lstat(legacy); legacyErr == nil {
			return nil, errors.New("发现旧版无归属恢复日志，请先核对原数据目录；不会自动迁移或执行恢复")
		} else if !os.IsNotExist(legacyErr) {
			return nil, legacyErr
		}
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	if !restorestate.ValidID(pointer.ID) {
		return nil, errors.New("invalid restore pointer")
	}
	var op restorestate.Operation
	e = restorestate.ReadOperation(s.opts.DataDir, pointer.ID, &op)
	return &op, e
}
func restoreTerminal(state string) bool {
	return state == "succeeded" || state == "rolled_back" || state == "failed"
}

// Public lifecycle controls cannot lift a recovery fence. Only the recovery
// transaction may call the internal *Locked methods before a terminal commit.
func (s *Supervisor) guardRestoreControl() error {
	op, e := s.restoreCurrent()
	if e != nil {
		return e
	}
	if op != nil && !restoreTerminal(op.State) {
		return errors.New("launcher: restore recovery must complete before starting or resuming services")
	}
	return nil
}
func (s *Supervisor) restorePhase(op *restorestate.Operation, state, message, code string) error {
	op.State, op.Message, op.ErrorCode, op.UpdatedAt = state, message, code, time.Now().UTC()
	return restorestate.SaveOperation(s.opts.DataDir, *op)
}
func (s *Supervisor) loadRestorePreflight(id string) (restorestate.Preflight, error) {
	var p restorestate.Preflight
	if !restorestate.ValidID(id) {
		return p, errors.New("invalid preflight identifier")
	}
	e := restorestate.ReadPreflight(s.opts.DataDir, id, &p)
	if e != nil {
		return p, e
	}
	if p.ID != id || time.Now().After(p.ExpiresAt) || !restorestate.Contained(restorestate.Stage(s.opts.DataDir, id), p.Dir) {
		return p, errors.New("preflight expired or escaped staging")
	}
	for _, v := range []struct{ name, hash string }{{"relaylab.db", p.DBHash}, {"master.key", p.KeyHash}} {
		path := filepath.Join(p.Dir, v.name)
		if !restorestate.Contained(p.Dir, path) {
			return p, errors.New("preflight member escaped staging")
		}
		hash, _, e := SHA256File(path)
		if e != nil || hash != v.hash {
			return p, errors.New("preflight changed; repeat validation")
		}
	}
	return p, nil
}
func (s *Supervisor) handleRestore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	var req RestoreRequest
	if e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); e != nil || !restorestate.ValidID(req.OperationID) || !restorestate.ValidID(req.PreflightID) {
		writeJSON(w, 400, map[string]string{"error": "invalid_restore"})
		return
	}
	if !s.transactionMu.TryLock() {
		writeJSON(w, 409, map[string]string{"error": "operation_in_progress"})
		return
	}
	fail := func(code int) {
		s.transactionMu.Unlock()
		writeJSON(w, code, map[string]string{"error": "restore_not_ready"})
	}
	if current, e := s.restoreCurrent(); e != nil || (current != nil && !restoreTerminal(current.State)) {
		fail(409)
		return
	}
	if t := s.transactionCopy(); t != nil && t.State != "succeeded" && t.State != "rolled_back" && t.State != "failed" {
		fail(409)
		return
	}
	if _, e := s.loadRestorePreflight(req.PreflightID); e != nil {
		fail(400)
		return
	}
	var op restorestate.Operation
	if e := restorestate.ReadOperation(s.opts.DataDir, req.OperationID, &op); e != nil || op.ID != req.OperationID || op.PreflightID != req.PreflightID || op.State != "accepted" {
		fail(400)
		return
	}
	if e := restorestate.SaveCurrent(s.opts.DataDir, op.ID); e != nil {
		fail(500)
		return
	}
	go func() {
		defer s.transactionMu.Unlock()
		s.opMu.Lock()
		defer s.opMu.Unlock()
		ctx, cancel := context.WithTimeout(s.lifetime, 30*time.Minute)
		defer cancel()
		_ = s.restoreLocked(ctx, &op)
	}()
	writeJSON(w, 202, map[string]any{"accepted": true, "operation_id": op.ID})
}
func (s *Supervisor) restoreLocked(ctx context.Context, op *restorestate.Operation) error {
	release, e := operationlock.Acquire(s.opts.DataDir)
	if e != nil {
		_ = s.restorePhase(op, "failed", "另一个备份或升级操作正在执行", "operation_busy")
		return e
	}
	defer release()
	p, e := s.loadRestorePreflight(op.PreflightID)
	if e != nil {
		_ = s.restorePhase(op, "failed", "备份预检已过期或文件改变", "preflight_invalid")
		return e
	}
	if e = s.restorePhase(op, "draining", "正在排空请求与任务", ""); e != nil {
		return e
	}
	if e = s.stopLocked(ctx); e != nil {
		return s.resumeBeforeRestore(op, e, "drain_failed")
	}
	usage, e := s.holdDataUsage()
	if e != nil {
		return s.restoreProblem(op, e)
	}
	held := true
	defer func() {
		if held {
			usage()
		}
	}()
	op.SafetyDir = filepath.Join(restorestate.Root(s.opts.DataDir), "safety", op.ID)
	if e = os.MkdirAll(op.SafetyDir, 0700); e == nil {
		e = backup.ProtectDirectory(op.SafetyDir)
	}
	if e == nil {
		var info os.FileInfo
		info, e = os.Stat(filepath.Join(s.opts.DataDir, "relaylab.db"))
		if e == nil {
			e = backup.EnsureSpace(op.SafetyDir, uint64(info.Size())*4+(64<<20))
		}
	}
	if e == nil {
		e = s.restorePhase(op, "backing_up", "正在保存恢复前的数据安全快照", "")
	}
	if e == nil {
		e = backup.Create(ctx, s.opts.DataDir, filepath.Join(op.SafetyDir, "before-restore.zip"), backup.Options{Legacy: true})
	}
	// The validated VACUUM snapshot excludes live WAL files and is the only safety source.
	var safety *backup.Validated
	if e == nil {
		safety, e = backup.Validate(ctx, filepath.Join(op.SafetyDir, "before-restore.zip"), "", op.SafetyDir)
	}
	if e == nil {
		e = copyFile(safety.DBPath, filepath.Join(op.SafetyDir, "relaylab.db"))
	}
	if e == nil {
		e = copyFile(safety.KeyPath, filepath.Join(op.SafetyDir, "master.key"))
	}
	if safety != nil {
		_ = safety.Close()
	}
	if e == nil {
		op.SafetyDBHash, _, e = SHA256File(filepath.Join(op.SafetyDir, "relaylab.db"))
	}
	if e == nil {
		op.SafetyKeyHash, _, e = SHA256File(filepath.Join(op.SafetyDir, "master.key"))
	}
	if e != nil {
		usage()
		held = false
		return s.resumeBeforeRestore(op, e, "safety_snapshot_failed")
	}
	e = backup.FinalizeRestored(ctx, p.Dir)
	if e != nil {
		usage()
		held = false
		return s.resumeBeforeRestore(op, e, "staging_failed")
	}
	op.DataChanged = true
	if e = s.restorePhase(op, "replacing", "正在事务性替换数据库与主密钥", ""); e != nil {
		usage()
		held = false
		return s.rollbackRestore(ctx, op, e)
	}
	if e = s.installRestoreFiles(p.Dir); e != nil {
		usage()
		held = false
		return s.rollbackRestore(ctx, op, e)
	}
	usage()
	held = false
	if e = s.restorePhase(op, "starting", "维护状态下启动并验证服务", ""); e != nil {
		return s.rollbackRestore(ctx, op, e)
	}
	if e = s.startLocked(ctx); e != nil {
		return s.rollbackRestore(ctx, op, e)
	}
	if e = s.restorePhase(op, "committed", "恢复验证通过，正在解除维护", ""); e != nil {
		return s.rollbackRestore(ctx, op, e)
	}
	if e = s.resumeLocked(); e != nil {
		_ = s.restorePhase(op, "committed", "恢复数据已验证，请重启启动器完成维护解除", "resume_pending")
		return e
	}
	if e = s.restorePhase(op, "succeeded", "恢复完成；请重新登录并核对备份索引", ""); e != nil {
		return e
	}
	_ = os.RemoveAll(restorestate.Stage(s.opts.DataDir, op.PreflightID))
	return nil
}

// A failure before replacement must leave both original services available.
// Its recovery context is independent of an expired restore request.
func (s *Supervisor) resumeBeforeRestore(op *restorestate.Operation, cause error, code string) error {
	recovery, cancel := context.WithTimeout(context.Background(), s.opts.DrainTimeout+2*s.opts.GracePeriod+s.opts.StartupTimeout+time.Second)
	defer cancel()
	children := s.children()
	restart := (children[0] == nil) != (children[1] == nil)
	for _, child := range children {
		if child == nil {
			continue
		}
		select {
		case <-child.done:
			restart = true
		default:
		}
		if _, e := os.Stat(filepath.Join(s.opts.DataDir, child.role+".stop.request")); e == nil {
			restart = true
		}
	}
	if restart {
		if e := s.stopLocked(recovery); e != nil {
			return s.restoreProblem(op, errors.Join(cause, e))
		}
	}
	children = s.children()
	if children[0] == nil && children[1] == nil {
		if e := s.startLocked(recovery); e != nil {
			return s.restoreProblem(op, errors.Join(cause, e))
		}
	}
	if e := s.resumeLocked(); e != nil {
		return s.restoreProblem(op, errors.Join(cause, e))
	}
	if e := s.restorePhase(op, "failed", "恢复未替换数据，原服务已恢复", code); e != nil {
		return s.restoreProblem(op, errors.Join(cause, e))
	}
	return cause
}
func (s *Supervisor) installRestoreFiles(source string) error {
	for _, name := range []string{"relaylab.db-wal", "relaylab.db-shm"} {
		if e := removeIfExists(filepath.Join(s.opts.DataDir, name)); e != nil {
			return e
		}
	}
	for _, name := range []string{"relaylab.db", "master.key"} {
		if e := copyFile(filepath.Join(source, name), filepath.Join(s.opts.DataDir, name)); e != nil {
			return e
		}
		if e := backup.ProtectFile(filepath.Join(s.opts.DataDir, name)); e != nil {
			return e
		}
	}
	return syncDirectory(s.opts.DataDir)
}
func (s *Supervisor) restoreProblem(op *restorestate.Operation, cause error) error {
	_ = s.drainLocked()
	_ = s.restorePhase(op, "recovery_required", "自动恢复尚未完成，请保留安全快照并检查启动器", "recovery_required")
	s.setState(StateFailed, errors.New("launcher: data restore recovery required"))
	return cause
}
func (s *Supervisor) rollbackRestore(ctx context.Context, op *restorestate.Operation, cause error) error {
	recovery, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if e := s.drainLocked(); e != nil {
		return s.restoreProblem(op, e)
	}
	if e := s.stopLocked(recovery); e != nil {
		return s.restoreProblem(op, e)
	}
	if e := s.quiesceOrphans(recovery); e != nil {
		return s.restoreProblem(op, e)
	}
	usage, e := s.holdDataUsage()
	if e != nil {
		return s.restoreProblem(op, e)
	}
	if !restorestate.Contained(filepath.Join(restorestate.Root(s.opts.DataDir), "safety"), op.SafetyDir) {
		usage()
		return s.restoreProblem(op, errors.New("invalid safety snapshot path"))
	}
	for _, v := range []struct{ name, hash string }{{"relaylab.db", op.SafetyDBHash}, {"master.key", op.SafetyKeyHash}} {
		hash, _, err := SHA256File(filepath.Join(op.SafetyDir, v.name))
		if err != nil || v.hash == "" || hash != v.hash {
			usage()
			return s.restoreProblem(op, errors.New("safety snapshot validation failed"))
		}
	}
	if e = s.restorePhase(op, "rolling_back", "恢复未通过，正在恢复操作前数据", "restore_validation_failed"); e == nil {
		e = s.installRestoreFiles(op.SafetyDir)
	}
	usage()
	if e != nil {
		return s.restoreProblem(op, e)
	}
	if e = s.startLocked(recovery); e != nil {
		return s.restoreProblem(op, e)
	}
	op.DataChanged = false
	if e = s.restorePhase(op, "rolled_back", "恢复失败，已恢复操作前数据", "restore_failed"); e != nil {
		return s.restoreProblem(op, e)
	}
	if e = s.resumeLocked(); e != nil {
		return s.restoreProblem(op, e)
	}
	return cause
}
func (s *Supervisor) recoverRestore(ctx context.Context) (bool, error) {
	op, e := s.restoreCurrent()
	if e != nil {
		return true, e
	}
	if op == nil || restoreTerminal(op.State) {
		return false, nil
	}
	release, e := operationlock.Acquire(s.opts.DataDir)
	if e != nil {
		return true, e
	}
	defer release()
	if e = s.drainLocked(); e != nil {
		return true, e
	}
	if e = s.quiesceOrphans(ctx); e != nil {
		return true, e
	}
	if op.State == "committed" {
		// Validated restored data may already contain new writes. Finish the
		// committed operation; never restore the pre-operation data here.
		if e = s.restorePhase(op, "succeeded", "已恢复已提交操作，数据保留", ""); e != nil {
			return true, e
		}
		return false, nil
	}
	if op.DataChanged {
		e = s.rollbackRestore(ctx, op, errors.New("interrupted data restore"))
		if e != nil && op.State == "rolled_back" {
			return true, nil
		}
		return true, e
	}
	if e = s.restorePhase(op, "failed", "恢复在替换数据前中断，当前数据保留", "interrupted"); e != nil {
		return true, e
	}
	return false, nil
}
