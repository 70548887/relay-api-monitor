package backups

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"relaylab/internal/backup"
	"relaylab/internal/operationlock"
)

func (m *Manager) allRecords(ctx context.Context) ([]Run, error) {
	out := []Run{}
	for offset := 0; ; offset += 1000 {
		runs, e := m.List(ctx, 1000, offset)
		if e != nil {
			return nil, e
		}
		for _, summary := range runs {
			r, err := m.Record(ctx, summary.ID)
			if err != nil {
				return nil, err
			}
			out = append(out, r)
		}
		if len(runs) < 1000 {
			return out, nil
		}
	}
}
func (m *Manager) Protect(ctx context.Context, id string, enabled bool) error {
	release, e := operationlock.Acquire(m.opts.DataDir)
	if e != nil {
		return ErrBusy
	}
	defer release()
	r, e := m.Record(ctx, id)
	if e != nil {
		return e
	}
	if !terminal(r.State) {
		return ErrBusy
	}
	r.Protected = enabled
	if !enabled {
		r.MonthlyArchive = false
		r.ArchiveReleased = true
		r.ExpiresAt = nil
	}
	return m.save(ctx, r)
}

// AcknowledgeMissing preserves the protected record after an administrator has
// explicitly confirmed that a local-only file was not carried to a new volume.
// It never deletes a file or cloud object and cannot waive a cloud validation.
func (m *Manager) AcknowledgeMissing(ctx context.Context, id string) error {
	release, e := operationlock.Acquire(m.opts.DataDir)
	if e != nil {
		return ErrBusy
	}
	defer release()
	r, e := m.Record(ctx, id)
	if e != nil {
		return e
	}
	if r.LocalState != "missing" || r.StorageID != "" || r.RemoteState != "" || !terminal(r.State) {
		return errors.New("仅可确认已核对为遗失的纯本地备份，云端副本不可跳过校验")
	}
	r.State = "unavailable"
	r.Protected = true
	r.ErrorCode = "local_copy_loss_acknowledged"
	r.Message = "已由管理员确认旧本地副本遗失，索引与归档记录已保护保留"
	return m.save(ctx, r)
}
func (m *Manager) Delete(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	release, e := operationlock.Acquire(m.opts.DataDir)
	if e != nil {
		return ErrBusy
	}
	defer release()
	p, e := m.opts.Store.BackupPolicy(ctx)
	if e != nil {
		return e
	}
	if p.ReconciliationRequired {
		return ErrFrozen
	}
	return m.delete(ctx, id, false)
}
func (m *Manager) delete(ctx context.Context, id string, automatic bool) error {
	r, e := m.Record(ctx, id)
	if e != nil {
		return e
	}
	if !terminal(r.State) {
		return ErrBusy
	}
	if r.Protected || r.MonthlyArchive && !automatic {
		return ErrProtected
	}
	if !r.CloudImported && r.StorageID != "" && r.ObjectKey != "" && r.RemoteState != "deleted" {
		v, c, err := m.storedClient(ctx, r.StorageID)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(r.ObjectKey, v.Prefix+"/") || !strings.Contains(r.ObjectKey, "/"+r.ID+"/") {
			return errors.New("S3对象所有权校验失败")
		}
		if _, err = c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &v.Bucket, Key: &r.ObjectKey}); err != nil {
			r.ErrorCode = "delete_failed"
			r.Message = "S3删除失败，索引已保留，可重试"
			m.saveFinal(r)
			return errors.New("S3删除失败")
		}
		r.RemoteState = "deleted"
		if e = m.save(ctx, r); e != nil {
			return e
		}
	}
	if r.LocalPath != "" {
		if m.ownedPath(r.LocalPath) {
			if e = os.Remove(r.LocalPath); e != nil {
				r.ErrorCode = "delete_failed"
				r.Message = "本地删除失败，索引已保留"
				m.saveFinal(r)
				return errors.New("本地文件删除失败")
			}
		} else if _, e = os.Stat(r.LocalPath); !os.IsNotExist(e) {
			return errors.New("本地副本路径校验失败")
		}
	}
	// A tombstone outside SQLite preserves protection/slot history over restore.
	r.LocalState = "deleted"
	r.RemoteState = "deleted"
	r.State = "deleted"
	if e = m.save(ctx, r); e != nil {
		return e
	}
	return m.opts.Store.DeleteBackupRun(ctx, id)
}
func (m *Manager) cleanup(ctx context.Context, p Schedule) {
	if p.ReconciliationRequired {
		return
	}
	runs, e := m.allRecords(ctx)
	if e != nil {
		return
	}
	for _, r := range retentionCandidates(runs, p, time.Now().UTC()) {
		_ = m.delete(ctx, r.ID, true)
	}
}

type RetentionPreview struct {
	DeleteCount int      `json:"delete_count"`
	LocalBytes  int64    `json:"local_bytes"`
	RemoteBytes int64    `json:"remote_bytes"`
	BackupIDs   []string `json:"backup_ids"`
}

func (m *Manager) PreviewRetention(ctx context.Context, p Schedule) (RetentionPreview, error) {
	if e := validateSchedule(&p); e != nil {
		return RetentionPreview{}, e
	}
	runs, e := m.allRecords(ctx)
	if e != nil {
		return RetentionPreview{}, e
	}
	out := RetentionPreview{BackupIDs: []string{}}
	for _, r := range retentionCandidates(runs, p, time.Now().UTC()) {
		out.DeleteCount++
		out.BackupIDs = append(out.BackupIDs, r.ID)
		if r.LocalState == "verified" {
			out.LocalBytes += r.SizeBytes
		}
		if r.RemoteState == "verified" {
			out.RemoteBytes += r.SizeBytes
		}
	}
	return out, nil
}
func retentionCandidates(runs []Run, p Schedule, now time.Time) []Run {
	if p.ReconciliationRequired {
		return nil
	}
	ordinary, finite := 0, 0
	lastVerified := ""
	for _, r := range runs {
		if r.State == "succeeded" && (r.LocalState == "verified" || r.RemoteState == "verified") {
			lastVerified = r.ID
			break
		}
	}
	out := []Run{}
	for _, r := range runs {
		if r.State != "succeeded" || r.Protected {
			continue
		}
		candidate := false
		if r.MonthlyArchive {
			if r.ArchiveRetainCount > 0 {
				finite++
				limit := r.ArchiveRetainCount
				if p.MonthlyEnabled && p.MonthlyRetainCount > 0 {
					limit = p.MonthlyRetainCount
				}
				candidate = finite > limit
			}
		} else {
			ordinary++
			candidate = p.RetainCount > 0 && ordinary > p.RetainCount || p.RetainDays > 0 && now.After(r.StartedAt.AddDate(0, 0, p.RetainDays))
		}
		if candidate && r.ID != lastVerified {
			out = append(out, r)
		}
	}
	return out
}

func (m *Manager) periodicCleanup(now time.Time) bool {
	release, e := operationlock.Acquire(m.opts.DataDir)
	if e != nil {
		return false
	}
	defer release()
	leave := func() {}
	if m.opts.Enter != nil {
		var ok bool
		leave, ok = m.opts.Enter()
		if !ok {
			return false
		}
	}
	defer leave()
	ctx, cancel := context.WithTimeout(m.ctx, 2*time.Minute)
	defer cancel()
	// The operation lock excludes live archive writers. Restore staging has a
	// separate lifecycle and is never handled by this backup-only cleanup.
	m.cleanupTemporaryFiles(now)
	p, e := m.opts.Store.BackupPolicy(ctx)
	if e != nil {
		return false
	}
	if p.ReconciliationRequired {
		return true
	}
	m.cleanup(ctx, p)
	return true
}

// Reconcile imports only the application's durable catalog, never unknown S3 keys.
func (m *Manager) Reconcile(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	release, e := operationlock.Acquire(m.opts.DataDir)
	if e != nil {
		return ErrBusy
	}
	defer release()
	// Reconciliation is itself a cleanup fence. A missing external catalog on a
	// new volume must never turn stale database claims into verified copies.
	m.policyMu.Lock()
	if err := m.opts.Store.FreezeBackupStorageSelection(ctx); err != nil {
		m.policyMu.Unlock()
		return err
	}
	policy, err := m.opts.Store.BackupPolicy(ctx)
	if err == nil {
		policy.Enabled = false
		policy.NextRunAt = nil
		policy.ReconciliationRequired = true
		policy.ReconciliationWarnings = nil
		err = m.opts.Store.SaveBackupPolicy(ctx, policy)
	}
	m.policyMu.Unlock()
	if err != nil {
		return err
	}
	// Only references belonging to the current restored dataset can demand an
	// old storage revision. Unreferenced catalogs may use a different master key.
	known, e := m.allRecords(ctx)
	if e != nil {
		return e
	}
	current := map[string]Run{}
	for _, r := range known {
		current[r.ID] = r
		if r.StorageID != "" && r.RemoteState != "deleted" {
			if e = m.ensureReferencedStorage(ctx, r.StorageID); e != nil {
				return e
			}
		}
	}
	warnings := []string{}
	warn := func(message string) {
		if len(warnings) < 100 {
			warnings = append(warnings, message)
		}
	}
	entries, e := os.ReadDir(filepath.Join(m.opts.BackupDir, "catalog"))
	if e != nil {
		return e
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		b, e := readCatalogFile(filepath.Join(m.opts.BackupDir, "catalog", entry.Name()), 1<<20)
		if e != nil {
			return e
		}
		if len(b) > 1<<20 {
			return errors.New("备份索引超出大小限制")
		}
		var item catalogEntry
		if e = json.Unmarshal(b, &item); e != nil {
			return errors.New("备份索引损坏，清理继续暂停")
		}
		r := item.Run
		if entry.Name() != r.ID+".json" || len(r.ID) != 32 {
			return errors.New("备份索引ID不匹配")
		}
		if !m.catalogOwnedByCurrentVault(item, current) {
			warn("已保留不属于当前数据集或缺少归属证明的目录记录：" + r.ID)
			continue
		}
		if r.State != "deleted" && r.StorageID != "" && r.RemoteState != "deleted" {
			if e = m.ensureReferencedStorage(ctx, r.StorageID); e != nil {
				if _, ok := current[r.ID]; ok {
					return e
				}
				warn("目录记录的旧存储凭据不可用，已保留待核对：" + r.ID)
				continue
			}
		}
		if r.State == "deleted" {
			if len(r.ArchiveDates) > 0 {
				if _, e = m.opts.Store.ClaimBackupArchiveDates(ctx, r.ID, r.ArchiveDates, r.ArchiveRetainCount == 0); e != nil {
					return e
				}
			}
			if _, e = m.Record(ctx, r.ID); e == nil {
				if e = m.opts.Store.DeleteBackupRun(ctx, r.ID); e != nil {
					return e
				}
			}
			continue
		}
		r.LocalPath = item.LocalPath
		r.ObjectKey = item.ObjectKey
		if e = m.rebindLocalFile(&r); e != nil {
			return e
		}
		if !terminal(r.State) {
			// Recover only with the digest durably recorded before publication,
			// even if restoring SQLite already terminated the old queue row.
			local := m.recoverLocalFile(&r)
			remote := false
			if r.StorageID != "" && len(r.SHA256) == 64 && r.SizeBytes > 0 {
				remote = m.verifyRemote(ctx, &r) == nil
			}
			if _, _, e = m.opts.Store.CreateBackupRun(ctx, r); e != nil {
				return e
			}
			if (r.StorageID == "" && local) || remote {
				policy := Schedule{}
				if r.PolicySnapshot != nil {
					policy = *r.PolicySnapshot
				}
				r.State = "succeeded"
				r.ErrorCode = ""
				r.Message = "已核对中断时发布的备份与原归档策略"
				now := time.Now().UTC()
				r.FinishedAt = &now
				if _, e = m.complete(ctx, r, policy); e != nil {
					return e
				}
				continue
			}
			r.State = "interrupted"
			r.ErrorCode = "process_interrupted"
			r.Message = "原备份未完整发布或云端尚未校验"
			if r.LocalState == "unverified" {
				r.ErrorCode = "local_verification_required"
				r.Message = "原备份缺少可信摘要或校验不一致，请导入预检"
			}
		}
		if _, _, e = m.opts.Store.CreateBackupRun(ctx, r); e != nil {
			return e
		}
		if e = m.opts.Store.SaveBackupRun(ctx, r); e != nil {
			return e
		}
		if len(r.ArchiveDates) > 0 {
			if _, e = m.opts.Store.ClaimBackupArchiveDates(ctx, r.ID, r.ArchiveDates, r.ArchiveRetainCount == 0); e != nil {
				return e
			}
		}
	}
	// A restored SQLite snapshot can contain backup rows which were not copied
	// into the new volume's catalog. Verify the union, not just external files.
	storageEntries, e := os.ReadDir(filepath.Join(m.opts.BackupDir, "storages"))
	if e != nil {
		return e
	}
	for _, entry := range storageEntries {
		id := strings.TrimSuffix(entry.Name(), ".json")
		if entry.IsDir() || id == entry.Name() || len(id) != 32 {
			continue
		}
		if _, _, err = m.opts.Store.BackupStorage(ctx, id); errors.Is(err, sql.ErrNoRows) {
			warn("已保留未被当前数据集引用的旧存储目录：" + id)
		} else if err != nil {
			return err
		}
	}
	records, e := m.allRecords(ctx)
	if e != nil {
		return e
	}
	for _, summary := range records {
		r, err := m.Record(ctx, summary.ID)
		if err != nil {
			return err
		}
		if err = m.reconcileCopies(ctx, &r); err != nil {
			return err
		}
	}
	m.policyMu.Lock()
	defer m.policyMu.Unlock()
	p, e := m.opts.Store.BackupPolicy(ctx)
	if e != nil {
		return e
	}
	p.ReconciliationRequired = false
	p.ReconciliationWarnings = warnings
	p.Enabled = false
	p.NextRunAt = nil
	return m.opts.Store.SaveBackupPolicy(ctx, p)
}

func (m *Manager) reconcileCopies(ctx context.Context, r *Run) error {
	if r.State == "unavailable" && r.Protected && r.LocalState == "missing" && r.StorageID == "" && r.RemoteState == "" {
		return nil
	}
	claimsLocal := r.LocalState == "verified" || r.LocalState == "missing"
	claimsRemote := r.RemoteState == "verified"
	if !claimsLocal && !claimsRemote {
		if r.State == "succeeded" {
			return errors.New("成功备份没有可验证副本，清理继续暂停")
		}
		// Snapshotting jobs recovered as interrupted never claimed a completed
		// file and can be preserved without inventing a recovery point.
		return nil
	}
	localVerified := false
	if claimsLocal {
		if e := m.rebindLocalFile(r); e != nil {
			return e
		}
	}
	if claimsLocal && m.ownedPath(r.LocalPath) {
		hash, size, err := hashFile(r.LocalPath)
		if err != nil || hash != r.SHA256 || size != r.SizeBytes {
			return errors.New("本地索引哈希不匹配，清理继续暂停")
		}
		localVerified = true
		r.LocalState = "verified"
	}
	if claimsLocal && !localVerified {
		r.LocalState = "missing"
		if err := m.save(ctx, *r); err != nil {
			return err
		}
	}
	remoteVerified := false
	if claimsRemote {
		if err := m.verifyRemote(ctx, r); err != nil {
			return err
		}
		remoteVerified = true
	}
	if !localVerified && !remoteVerified {
		return errors.New("已登记备份的本地副本缺失，且云端不可验证；清理继续暂停")
	}
	return m.save(ctx, *r)
}

func (m *Manager) catalogOwnedByCurrentVault(item catalogEntry, current map[string]Run) bool {
	if item.VaultProof != "" {
		plain, e := m.opts.Vault.DecryptBackupCredentials("catalog:"+item.Run.ID, item.VaultProof)
		return e == nil && plain == "relaylab/backup-catalog/v1"
	}
	// Compatibility with pre-proof candidates is limited to rows already in
	// the validated restored database; a detached old file cannot claim ownership.
	r, ok := current[item.Run.ID]
	return ok && r.RequestID == item.Run.RequestID && r.CreatedAt.Equal(item.Run.CreatedAt) && r.FileName == item.Run.FileName && r.StorageID == item.Run.StorageID
}

func (m *Manager) ensureReferencedStorage(ctx context.Context, id string) error {
	if _, cipher, e := m.opts.Store.BackupStorage(ctx, id); e == nil {
		if _, e = m.opts.Vault.DecryptBackupCredentials(id, cipher); e != nil {
			return errors.New("当前备份引用的存储修订无法解密")
		}
		return nil
	} else if !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if len(id) != 32 || strings.ContainsAny(id, "/\\") {
		return errors.New("当前备份引用的存储标识无效")
	}
	b, e := readCatalogFile(filepath.Join(m.opts.BackupDir, "storages", id+".json"), 16384)
	if e != nil {
		return errors.New("当前备份引用的存储目录缺失，请导入对应配置")
	}
	var item storageCatalog
	if json.Unmarshal(b, &item) != nil || item.Storage.ID != id {
		return errors.New("当前备份引用的存储目录损坏")
	}
	if _, e = m.opts.Vault.DecryptBackupCredentials(id, item.Cipher); e != nil {
		return errors.New("当前备份引用的存储目录属于另一主密钥，请核对恢复数据集")
	}
	_, e = m.opts.Store.SaveBackupStorageAllocated(ctx, item.Storage, item.Cipher, false)
	return e
}

// A relocated backup is rebound only to the exact registered basename after
// its original length and SHA-256 match. Neither legacy paths nor links escape
// the current instance backup directory.
func (m *Manager) rebindLocalFile(r *Run) error {
	if r.LocalState == "deleted" || r.State == "deleted" {
		return nil
	}
	if r.FileName == "" || r.FileName != filepath.Base(r.FileName) || strings.ContainsAny(r.FileName, "/\\") || len(r.FileName) > 512 {
		return nil
	}
	paths := []string{r.LocalPath, filepath.Join(m.opts.BackupDir, r.FileName)}
	for _, path := range paths {
		if !m.ownedPath(path) {
			continue
		}
		resolved, e := filepath.EvalSymlinks(path)
		if e != nil {
			continue
		}
		root, e := filepath.EvalSymlinks(m.opts.BackupDir)
		if e != nil {
			return e
		}
		rel, e := filepath.Rel(root, resolved)
		if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return errors.New("备份副本路径逃逸当前目录")
		}
		if len(r.SHA256) != 64 || r.SizeBytes <= 0 {
			return nil
		}
		hash, size, e := hashFile(path)
		if e != nil || hash != r.SHA256 || size != r.SizeBytes {
			return errors.New("迁移后的本地备份摘要或大小不匹配，清理继续暂停")
		}
		r.LocalPath = path
		r.LocalState = "verified"
		return nil
	}
	return nil
}
func readCatalogFile(path string, limit int64) ([]byte, error) {
	info, e := os.Lstat(path)
	if e != nil {
		return nil, e
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("备份索引类型或大小不正确")
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, limit+1))
	if e != nil {
		return nil, e
	}
	if int64(len(b)) > limit {
		return nil, errors.New("备份索引超出限制")
	}
	return b, nil
}

// Materialize returns a private immutable copy owned by the caller; it must be removed.
func (m *Manager) Materialize(ctx context.Context, id string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	release, e := operationlock.Acquire(m.opts.DataDir)
	if e != nil {
		return "", ErrBusy
	}
	defer release()
	r, e := m.Record(ctx, id)
	if e != nil {
		return "", e
	}
	if r.SizeBytes <= 0 || r.SizeBytes > 3<<30 {
		return "", errors.New("没有可用的云端备份")
	}
	if e = backup.EnsureSpace(filepath.Join(m.opts.BackupDir, ".staging"), uint64(r.SizeBytes)+(64<<20)); e != nil {
		return "", e
	}
	var source io.ReadCloser
	if r.LocalState == "verified" && m.ownedPath(r.LocalPath) {
		source, e = os.Open(r.LocalPath)
	} else {
		if r.RemoteState != "verified" {
			return "", errors.New("备份副本不可用")
		}
		v, c, err := m.storedClient(ctx, r.StorageID)
		if err != nil {
			return "", err
		}
		out, err := c.GetObject(ctx, &s3.GetObjectInput{Bucket: &v.Bucket, Key: &r.ObjectKey})
		if err != nil {
			return "", errors.New("云端备份下载失败")
		}
		source = out.Body
	}
	if e != nil {
		return "", e
	}
	defer source.Close()
	f, e := os.CreateTemp(filepath.Join(m.opts.BackupDir, ".staging"), "download-*.zip.age")
	if e != nil {
		return "", e
	}
	path := f.Name()
	if e = backup.ProtectFile(path); e != nil {
		f.Close()
		os.Remove(path)
		return "", e
	}
	good := false
	defer func() {
		f.Close()
		if !good {
			os.Remove(path)
		}
	}()
	h := sha256.New()
	n, e := io.Copy(io.MultiWriter(f, h), io.LimitReader(source, r.SizeBytes+1))
	if e != nil || n != r.SizeBytes || hex.EncodeToString(h.Sum(nil)) != r.SHA256 {
		return "", errors.New("下载备份校验失败")
	}
	if e = f.Sync(); e != nil {
		return "", e
	}
	if e = f.Close(); e != nil {
		return "", e
	}
	good = true
	return path, nil
}
