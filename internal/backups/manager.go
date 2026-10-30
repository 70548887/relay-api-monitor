package backups

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"filippo.io/age"

	"relaylab/internal/backup"
	"relaylab/internal/domain"
	"relaylab/internal/durablejson"
	"relaylab/internal/operationlock"
)

type Manager struct {
	opts     Options
	mu       sync.Mutex
	policyMu sync.Mutex
	activeID string
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	started  bool
	closed   bool
}

func New(opts Options) (*Manager, error) {
	if opts.Store == nil || opts.Vault == nil || opts.DataDir == "" {
		return nil, errors.New("备份管理器配置不完整")
	}
	var err error
	opts.DataDir, err = filepath.Abs(opts.DataDir)
	if err != nil {
		return nil, err
	}
	if opts.BackupDir == "" {
		opts.BackupDir = operationlock.BackupRoot(opts.DataDir)
	}
	opts.BackupDir, err = filepath.Abs(opts.BackupDir)
	if err != nil {
		return nil, err
	}
	if opts.BackupDir == opts.DataDir || filepath.Dir(opts.BackupDir) == opts.BackupDir {
		return nil, errors.New("备份目录必须为独立专用目录")
	}
	for _, dir := range []string{opts.BackupDir, filepath.Join(opts.BackupDir, ".staging"), filepath.Join(opts.BackupDir, "catalog"), filepath.Join(opts.BackupDir, "storages")} {
		if err = os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
		if err = backup.ProtectDirectory(dir); err != nil {
			return nil, err
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{opts: opts, ctx: ctx, cancel: cancel}, nil
}
func (m *Manager) Start(ctx context.Context) {
	m.mu.Lock()
	if m.started || m.closed {
		m.mu.Unlock()
		return
	}
	m.started = true
	m.wg.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		recovered := m.recoverInterrupted()
		lastCleanup := time.Now().UTC()
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				m.cancel()
				return
			case <-m.ctx.Done():
				return
			case now := <-ticker.C:
				if !recovered {
					recovered = m.recoverInterrupted()
					continue
				}
				m.tick(now.UTC())
				if now.Sub(lastCleanup) >= time.Hour && m.periodicCleanup(now.UTC()) {
					lastCleanup = now.UTC()
				}
			}
		}
	}()
}
func (m *Manager) Close() { m.mu.Lock(); m.closed = true; m.cancel(); m.mu.Unlock(); m.wg.Wait() }
func (m *Manager) recoverInterrupted() bool {
	release, e := operationlock.Acquire(m.opts.DataDir)
	if e != nil {
		return false
	}
	defer release()
	recoveryCtx, cancelRecovery := context.WithTimeout(m.ctx, 30*time.Minute)
	defer cancelRecovery()
	m.policyMu.Lock()
	policy, policyErr := m.opts.Store.BackupPolicy(m.ctx)
	if policyErr == nil && policy.Enabled && policy.NextRunAt != nil && !policy.NextRunAt.After(time.Now().UTC()) {
		if next, err := Preview(policy); err == nil {
			policy.NextRunAt = &next[0]
			if err = m.opts.Store.SaveBackupPolicy(m.ctx, policy); err != nil {
				m.policyMu.Unlock()
				return false
			}
		}
	}
	m.policyMu.Unlock()
	records, e := m.opts.Store.UnfinishedBackupRuns(m.ctx)
	if e != nil {
		return false
	}
	for _, r := range records {
		if terminal(r.State) {
			continue
		}
		r, e = m.Record(m.ctx, r.ID)
		if e != nil {
			continue
		}
		// Final filenames are only installed after the archive writer has closed,
		// synced and atomically published the complete encrypted package.
		localRecovered := m.recoverLocalFile(&r)
		remoteRecovered := false
		if r.StorageID != "" && r.SHA256 != "" && r.SizeBytes > 0 {
			r.RemoteState = "pending"
			remoteRecovered = m.verifyRemote(recoveryCtx, &r) == nil
		}
		if localRecovered || remoteRecovered {
			if r.StorageID == "" && localRecovered || remoteRecovered {
				r.State = "succeeded"
				r.ErrorCode = ""
				r.Message = "已核对中断前完成的备份"
				now := time.Now().UTC()
				r.FinishedAt = &now
				policy := policy
				if r.PolicySnapshot != nil {
					policy = *r.PolicySnapshot
				}
				var completionErr error
				r, completionErr = m.complete(m.ctx, r, policy)
				if completionErr == nil {
					continue
				}
			}
		}
		r.State = "interrupted"
		r.ErrorCode = "process_interrupted"
		r.Message = "服务中断，已保留可用本地文件；不会自动重发"
		if r.LocalState == "unverified" {
			r.ErrorCode = "local_verification_required"
			r.Message = "中断后的本地文件缺少可信摘要或校验不一致，请重新导入预检"
		}
		n := time.Now().UTC()
		r.FinishedAt = &n
		if e = m.save(m.ctx, r); e != nil {
			return false
		}
	}
	return true
}
func (m *Manager) Snapshot(ctx context.Context) (Snapshot, error) {
	p, e := m.opts.Store.BackupPolicy(ctx)
	if e != nil {
		return Snapshot{}, e
	}
	runs, e := m.List(ctx, 100, 0)
	if e != nil {
		return Snapshot{}, e
	}
	v := Snapshot{Schedule: p, Records: runs, BackupDir: m.opts.BackupDir, NextRuns: []time.Time{}}
	v.Warnings = append([]string{}, p.ReconciliationWarnings...)
	if p.Enabled && !p.ReconciliationRequired {
		v.NextRuns, _ = Preview(p)
	}
	if st, _, e := m.opts.Store.BackupStorage(ctx, ""); e == nil {
		v.Storage = &st
	} else if !errors.Is(e, sql.ErrNoRows) {
		return v, e
	}
	v.Active, v.LastSuccess, e = m.opts.Store.BackupOverview(ctx)
	if e != nil {
		return v, e
	}
	return v, nil
}
func (m *Manager) List(ctx context.Context, limit, offset int) ([]Run, error) {
	return m.opts.Store.ListBackupRuns(ctx, limit, offset)
}
func (m *Manager) Record(ctx context.Context, id string) (Run, error) {
	r, e := m.opts.Store.BackupRun(ctx, id)
	if e != nil {
		return r, e
	}
	dates, permanent, e := m.opts.Store.BackupArchiveProtection(ctx, id)
	if e != nil {
		return r, e
	}
	if len(dates) > 0 && !r.ArchiveReleased {
		r.MonthlyArchive = true
		r.ArchiveDates = dates
		if permanent {
			r.ArchiveRetainCount = 0
		}
		r.ExpiresAt = nil
	}
	return r, nil
}
func (m *Manager) SaveSchedule(ctx context.Context, in Schedule) (Schedule, error) {
	m.policyMu.Lock()
	defer m.policyMu.Unlock()
	old, e := m.opts.Store.BackupPolicy(ctx)
	if e != nil {
		return in, e
	}
	in.Recipient, in.RecipientID, in.RecipientVerified = old.Recipient, old.RecipientID, old.RecipientVerified
	in.ReconciliationRequired = old.ReconciliationRequired
	in.ActiveStorageID, in.StorageSelectionSet = old.ActiveStorageID, old.StorageSelectionSet
	in.ReconciliationWarnings = append([]string{}, old.ReconciliationWarnings...)
	if e = validateSchedule(&in); e != nil {
		return in, e
	}
	if in.Enabled && (in.ReconciliationRequired || !in.RecipientVerified) {
		return in, ErrKeyNotVerified
	}
	if in.Mode == "local_s3" {
		if _, _, e = m.opts.Store.BackupStorage(ctx, ""); e != nil {
			return in, errors.New("请先配置S3存储")
		}
	}
	if in.Enabled {
		if old.Enabled && old.Cron == in.Cron && old.Timezone == in.Timezone && old.NextRunAt != nil {
			in.NextRunAt = old.NextRunAt
		} else {
			next, e := Preview(in)
			if e != nil {
				return in, e
			}
			in.NextRunAt = &next[0]
		}
	} else {
		in.NextRunAt = nil
	}
	return in, m.opts.Store.SaveBackupPolicy(ctx, in)
}
func (m *Manager) SetRecipient(ctx context.Context, recipient string) error {
	recipient = strings.TrimSpace(recipient)
	if _, e := age.ParseX25519Recipient(recipient); e != nil {
		return errors.New("恢复公钥格式不正确")
	}
	m.policyMu.Lock()
	defer m.policyMu.Unlock()
	p, e := m.opts.Store.BackupPolicy(ctx)
	if e != nil {
		return e
	}
	if p.Recipient != recipient {
		p.Recipient = recipient
		p.RecipientID = backup.RecipientID(recipient)
		p.RecipientVerified = false
		p.Enabled = false
		p.NextRunAt = nil
	}
	return m.opts.Store.SaveBackupPolicy(ctx, p)
}
func (m *Manager) ValidateIdentity(ctx context.Context, identity string) error {
	m.policyMu.Lock()
	defer m.policyMu.Unlock()
	p, e := m.opts.Store.BackupPolicy(ctx)
	if e != nil {
		return e
	}
	if e = backup.VerifyIdentity(p.Recipient, identity); e != nil {
		return errors.New("恢复私钥与当前公钥不匹配")
	}
	p.RecipientVerified = true
	return m.opts.Store.SaveBackupPolicy(ctx, p)
}
func (m *Manager) Create(ctx context.Context, requestID string) (Run, error) {
	return m.create(ctx, requestID, nil)
}
func (m *Manager) create(ctx context.Context, requestID string, slot *time.Time) (Run, error) {
	if len(requestID) < 8 || len(requestID) > 160 {
		return Run{}, errors.New("备份请求ID需为8至160字符")
	}
	// Duplicate client requests are answered before acquiring an operation lock.
	var raw string
	if e := m.opts.Store.DB.QueryRowContext(ctx, "SELECT data FROM backup_runs WHERE request_id=?", requestID).Scan(&raw); e == nil {
		var r Run
		e = json.Unmarshal([]byte(raw), &r)
		return r, e
	} else if !errors.Is(e, sql.ErrNoRows) {
		return Run{}, e
	}
	m.mu.Lock()
	if m.activeID != "" || m.closed {
		m.mu.Unlock()
		return Run{}, ErrBusy
	}
	m.activeID = "reserving"
	m.wg.Add(1)
	m.mu.Unlock()
	reset := true
	defer func() {
		if reset {
			m.mu.Lock()
			m.activeID = ""
			m.mu.Unlock()
			m.wg.Done()
		}
	}()
	release, e := operationlock.Acquire(m.opts.DataDir)
	if e != nil {
		return Run{}, ErrBusy
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()
	leave := func() {}
	if m.opts.Enter != nil {
		var ok bool
		leave, ok = m.opts.Enter()
		if !ok {
			return Run{}, ErrBusy
		}
	}
	admitted := false
	defer func() {
		if !admitted {
			leave()
		}
	}()
	p, e := m.opts.Store.BackupPolicy(ctx)
	if e != nil {
		return Run{}, e
	}
	if p.ReconciliationRequired {
		return Run{}, ErrFrozen
	}
	if !p.RecipientVerified {
		return Run{}, ErrKeyNotVerified
	}
	now := time.Now().UTC()
	id := domain.NewID()
	r := Run{ID: id, RequestID: requestID, State: "queued", Trigger: "manual", CreatedAt: now, StartedAt: now, HeartbeatAt: now, Version: m.opts.BuildVersion, DatabaseSchema: 4, RecipientID: p.RecipientID, FileName: fmt.Sprintf("relaylab_%s_%s.zip.age", now.Format("20060102T150405Z"), id), ScheduledFor: slot, LocalState: "pending"}
	r.PolicySnapshot = &p
	if slot != nil {
		r.Trigger = "scheduled"
	}
	r.LocalPath = filepath.Join(m.opts.BackupDir, r.FileName)
	if p.Mode == "local_s3" {
		st, _, err := m.opts.Store.BackupStorage(ctx, "")
		if err != nil {
			return r, errors.New("未配置S3存储")
		}
		r.StorageID = st.ID
		r.RemoteState = "pending"
		r.ObjectKey = st.Prefix + "/" + m.instanceID() + "/" + now.Format("2006/01") + "/" + id + "/" + r.FileName
	}
	if p.RetainDays > 0 {
		expiry := now.AddDate(0, 0, p.RetainDays)
		r.ExpiresAt = &expiry
	}
	r, fresh, e := m.opts.Store.CreateBackupRun(ctx, r)
	if e != nil {
		return r, e
	}
	if !fresh {
		return r, nil
	}
	m.mu.Lock()
	m.activeID = id
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		defer leave()
		defer release()
		defer func() { m.mu.Lock(); m.activeID = ""; m.mu.Unlock() }()
		m.execute(r, p, false)
	}()
	reset = false
	released = true
	admitted = true
	return r, nil
}
func (m *Manager) execute(r Run, p Schedule, retry bool) {
	ctx, cancel := context.WithTimeout(m.ctx, 30*time.Minute)
	defer cancel()
	fail := func(code, message string) {
		r.State = "failed"
		r.ErrorCode = code
		r.Message = message
		if r.RemoteState != "" && r.RemoteState != "verified" {
			r.RemoteState = "failed"
		}
		n := time.Now().UTC()
		r.FinishedAt = &n
		m.saveFinal(r)
	}
	defer func() {
		if recover() != nil {
			fail("internal_error", "备份操作异常中断，未自动重试")
		}
	}()
	done := make(chan struct{})
	heartbeatExited := make(chan struct{})
	heartbeatID := r.ID
	go func() {
		defer close(heartbeatExited)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				_, _ = m.opts.Store.DB.ExecContext(ctx, "UPDATE backup_runs SET data=json_set(data,'$.heartbeat_at',?) WHERE id=?", time.Now().UTC().Format(time.RFC3339Nano), heartbeatID)
			}
		}
	}()
	defer func() { close(done); <-heartbeatExited }()
	if !retry {
		r.State = "snapshotting"
		if e := m.save(ctx, r); e != nil {
			fail("result_save_failed", "备份状态保存失败，未继续执行")
			return
		}
		opts := backup.Options{Recipient: p.Recipient, Version: m.opts.BuildVersion, Commit: m.opts.Commit, Schema: 4, BackupID: r.ID, StagingDir: filepath.Join(m.opts.BackupDir, ".staging"), OnPhase: func(state string) error { r.State = state; return m.save(ctx, r) }, OnReady: func(member backup.Member) error {
			r.SHA256, r.SizeBytes = member.SHA256, member.Size
			// The file is not yet visible at its final name. Preserve pending until
			// publication and the independent post-publication read both succeed.
			r.LocalState = "pending"
			return m.save(ctx, r)
		}}
		if e := backup.CreateFromStore(ctx, m.opts.Store, m.opts.DataDir, r.LocalPath, opts); e != nil {
			fail("snapshot_failed", "快照或加密失败，请检查磁盘空间与数据完整性")
			return
		}
		var e error
		hash, size, hashErr := hashFile(r.LocalPath)
		e = hashErr
		if e != nil || r.SHA256 == "" || hash != r.SHA256 || size != r.SizeBytes {
			fail("local_validation_failed", "本地加密包校验失败")
			return
		}
		r.LocalState = "verified"
		r.State = "local_ready"
		if e = m.save(ctx, r); e != nil {
			fail("result_save_failed", "本地包已生成，备份索引保存失败")
			return
		}
	}
	if r.StorageID != "" {
		r.State = "uploading"
		r.RemoteState = "uploading"
		if e := m.save(ctx, r); e != nil {
			fail("result_save_failed", "上传前状态保存失败，本地包已保留")
			return
		}
		if e := m.upload(ctx, &r); e != nil {
			fail("s3_upload_failed", e.Error())
			return
		}
	}
	r.State = "succeeded"
	r.ErrorCode = ""
	r.Message = "备份已完成并校验"
	n := time.Now().UTC()
	r.FinishedAt = &n
	var completionErr error
	r, completionErr = m.complete(ctx, r, p)
	if completionErr != nil {
		fail("result_save_failed", "备份文件已生成，最终状态保存失败；请核对索引")
		return
	}
	if current, e := m.opts.Store.BackupPolicy(ctx); e == nil {
		m.cleanup(ctx, current)
	}
}

func (m *Manager) complete(ctx context.Context, r Run, p Schedule) (Run, error) {
	dates := []string{}
	if r.Trigger == "scheduled" && p.MonthlyEnabled && !r.ArchiveReleased {
		dates = archiveDates(r.StartedAt, p)
	}
	r.HeartbeatAt = time.Now().UTC()
	saved, e := m.opts.Store.CompleteBackupRun(ctx, r, dates, p.MonthlyRetainCount)
	if e != nil {
		return r, e
	}
	return saved, m.writeCatalog(saved)
}
func (m *Manager) recoverLocalFile(r *Run) bool {
	ownedFile := m.ownedPath(r.LocalPath)
	if ownedFile {
		r.LocalState = "unverified"
	}
	// The digest is committed before atomic publication. A legacy run without
	// that trusted value cannot establish integrity by hashing an unknown file.
	if len(r.SHA256) != 64 || r.SizeBytes <= 0 {
		return false
	}
	if r.FileName != filepath.Base(r.FileName) || !strings.HasSuffix(r.FileName, "_"+r.ID+".zip.age") || filepath.Clean(r.LocalPath) != filepath.Join(m.opts.BackupDir, r.FileName) || !ownedFile {
		return false
	}
	f, e := os.Open(r.LocalPath)
	if e != nil {
		return false
	}
	header := make([]byte, len("age-encryption.org/v1\n"))
	_, e = io.ReadFull(f, header)
	_ = f.Close()
	if e != nil || string(header) != "age-encryption.org/v1\n" {
		return false
	}
	hash, size, e := hashFile(r.LocalPath)
	if e != nil || size <= int64(len(header)) || size > backup.MaxArchiveSize {
		return false
	}
	if r.SHA256 != hash || r.SizeBytes != size {
		return false
	}
	r.SHA256, r.SizeBytes, r.LocalState = hash, size, "verified"
	return true
}
func (m *Manager) saveFinal(r Run) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m.save(ctx, r)
}

type catalogEntry struct {
	Run        Run    `json:"run"`
	LocalPath  string `json:"local_path"`
	ObjectKey  string `json:"object_key"`
	VaultProof string `json:"vault_proof,omitempty"`
}

func (m *Manager) writeCatalog(r Run) error {
	proof, e := m.opts.Vault.EncryptBackupCredentials("catalog:"+r.ID, "relaylab/backup-catalog/v1")
	if e != nil {
		return e
	}
	return durablejson.Write(filepath.Join(m.opts.BackupDir, "catalog", r.ID+".json"), catalogEntry{Run: r, LocalPath: r.LocalPath, ObjectKey: r.ObjectKey, VaultProof: proof})
}

func (m *Manager) save(ctx context.Context, r Run) error {
	r.HeartbeatAt = time.Now().UTC()
	if e := m.opts.Store.SaveBackupRun(ctx, r); e != nil {
		return e
	}
	return m.writeCatalog(r)
}
func (m *Manager) instanceID() string {
	path := filepath.Join(m.opts.BackupDir, "instance-id")
	b, e := os.ReadFile(path)
	if e == nil && len(strings.TrimSpace(string(b))) == 32 {
		return strings.TrimSpace(string(b))
	}
	id := domain.NewID()
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e == nil {
		f.WriteString(id)
		f.Sync()
		f.Close()
		return id
	}
	b, _ = os.ReadFile(path)
	return strings.TrimSpace(string(b))
}
func (m *Manager) RetryUpload(ctx context.Context, id string) (Run, error) {
	m.mu.Lock()
	if m.activeID != "" || m.closed {
		m.mu.Unlock()
		return Run{}, ErrBusy
	}
	m.activeID = id
	m.wg.Add(1)
	m.mu.Unlock()
	reset := true
	defer func() {
		if reset {
			m.mu.Lock()
			m.activeID = ""
			m.mu.Unlock()
			m.wg.Done()
		}
	}()
	release, e := operationlock.Acquire(m.opts.DataDir)
	if e != nil {
		return Run{}, ErrBusy
	}
	launched := false
	defer func() {
		if !launched {
			release()
		}
	}()
	r, e := m.Record(ctx, id)
	if e != nil {
		return r, e
	}
	if !terminal(r.State) || r.StorageID == "" || r.LocalState != "verified" || r.RemoteState == "verified" {
		return r, errors.New("该备份没有待重试的S3副本")
	}
	if !m.ownedPath(r.LocalPath) {
		return r, errors.New("本地路径不属于备份目录")
	}
	hash, size, e := hashFile(r.LocalPath)
	if e != nil || hash != r.SHA256 || size != r.SizeBytes {
		return r, errors.New("本地包已改变，拒绝上传")
	}
	p, e := m.opts.Store.BackupPolicy(ctx)
	if e != nil {
		return r, e
	}
	if p.ReconciliationRequired {
		return r, ErrFrozen
	}
	if r.PolicySnapshot != nil {
		p = *r.PolicySnapshot
	}
	leave := func() {}
	if m.opts.Enter != nil {
		var ok bool
		leave, ok = m.opts.Enter()
		if !ok {
			return r, ErrBusy
		}
	}
	r.State = "uploading"
	r.ErrorCode = ""
	r.Message = ""
	r.FinishedAt = nil
	if e = m.save(ctx, r); e != nil {
		leave()
		return r, e
	}
	go func() {
		defer m.wg.Done()
		defer release()
		defer leave()
		defer func() { m.mu.Lock(); m.activeID = ""; m.mu.Unlock() }()
		m.execute(r, p, true)
	}()
	reset = false
	launched = true
	return r, nil
}
func (m *Manager) ownedPath(path string) bool {
	rel, e := filepath.Rel(m.opts.BackupDir, path)
	if e != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	info, e := os.Lstat(path)
	return e == nil && info.Mode().IsRegular()
}
func (m *Manager) DownloadPath(ctx context.Context, id string) (string, error) {
	r, e := m.Record(ctx, id)
	if e != nil {
		return "", e
	}
	if r.LocalState != "verified" || !m.ownedPath(r.LocalPath) {
		return "", errors.New("本地副本不可用")
	}
	h, n, e := hashFile(r.LocalPath)
	if e != nil || h != r.SHA256 || n != r.SizeBytes {
		return "", errors.New("备份文件校验失败")
	}
	return r.LocalPath, nil
}

// OpenDownload pins a verified descriptor while a concurrent delete unlinks its name.
func (m *Manager) OpenDownload(ctx context.Context, id string) (*os.File, Run, error) {
	release, e := operationlock.Acquire(m.opts.DataDir)
	if e != nil {
		return nil, Run{}, ErrBusy
	}
	defer release()
	r, e := m.Record(ctx, id)
	if e != nil {
		return nil, r, e
	}
	if r.LocalState != "verified" || !m.ownedPath(r.LocalPath) {
		return nil, r, errors.New("本地副本不可用")
	}
	f, e := os.Open(r.LocalPath)
	if e != nil {
		return nil, r, e
	}
	h := sha256.New()
	n, e := io.Copy(h, f)
	if e != nil || n != r.SizeBytes || hex.EncodeToString(h.Sum(nil)) != r.SHA256 {
		f.Close()
		return nil, r, errors.New("备份文件校验失败")
	}
	if _, e = f.Seek(0, io.SeekStart); e != nil {
		f.Close()
		return nil, r, e
	}
	return f, r, nil
}
