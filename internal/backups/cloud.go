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
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"relaylab/internal/backup"
	"relaylab/internal/domain"
	"relaylab/internal/durablejson"
	"relaylab/internal/operationlock"
)

var cloudIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

type CloudCandidate struct {
	ID           string    `json:"id"`
	FileName     string    `json:"file_name"`
	SizeBytes    int64     `json:"size_bytes"`
	LastModified time.Time `json:"last_modified"`
	Imported     bool      `json:"imported"`
	Status       string    `json:"status"`
	Message      string    `json:"message,omitempty"`
}
type CloudPage struct {
	Items             []CloudCandidate `json:"items"`
	ContinuationToken string           `json:"continuation_token,omitempty"`
	Truncated         bool             `json:"truncated"`
}
type cloudRegistration struct {
	Candidate CloudCandidate `json:"candidate"`
	StorageID string         `json:"storage_id"`
	ObjectKey string         `json:"object_key"`
	ETag      string         `json:"etag"`
	ExpiresAt time.Time      `json:"expires_at"`
}

func (m *Manager) cloudRoot() string { return filepath.Join(m.opts.BackupDir, "cloud-index") }
func (m *Manager) cloudRegistrationPath(id string) string {
	return filepath.Join(m.cloudRoot(), id+".json")
}
func cloudRequestID(v cloudRegistration) string {
	h := sha256.Sum256([]byte(v.StorageID + "\x00" + v.ObjectKey + "\x00" + v.ETag))
	return "cloud:" + hex.EncodeToString(h[:])
}
func (m *Manager) cloudExisting(ctx context.Context, requestID string) (Run, error) {
	var id string
	e := m.opts.Store.DB.QueryRowContext(ctx, "SELECT id FROM backup_runs WHERE request_id=?", requestID).Scan(&id)
	if e != nil {
		return Run{}, e
	}
	return m.Record(ctx, id)
}

// Scan registers opaque, expiring candidates. An admin request can only import
// objects returned by this scan, never an arbitrary key or filesystem path.
func (m *Manager) ScanCloud(ctx context.Context, continuation string) (CloudPage, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	page := CloudPage{Items: []CloudCandidate{}}
	if len(continuation) > 8192 {
		return page, errors.New("云端分页标识过长")
	}
	v, c, e := m.storedClient(ctx, "")
	if e != nil {
		return page, e
	}
	input := &s3.ListObjectsV2Input{Bucket: &v.Bucket, Prefix: aws.String(v.Prefix + "/"), MaxKeys: aws.Int32(200)}
	if continuation != "" {
		input.ContinuationToken = &continuation
	}
	out, e := c.ListObjectsV2(ctx, input)
	if e != nil {
		m.recordStorageHealth(v.ID, "list", e)
		return page, errors.New("云端目录读取失败，请检查该前缀的列表权限")
	}
	if len(out.Contents) > 200 || len(aws.ToString(out.NextContinuationToken)) > 8192 {
		return page, errors.New("云端目录响应超过限制")
	}
	if e = os.MkdirAll(m.cloudRoot(), 0700); e != nil {
		return page, e
	}
	if e = backup.ProtectDirectory(m.cloudRoot()); e != nil {
		return page, e
	}
	now := time.Now().UTC()
	for _, object := range out.Contents {
		key := aws.ToString(object.Key)
		size := aws.ToInt64(object.Size)
		if !strings.HasPrefix(key, v.Prefix+"/") || !strings.HasSuffix(key, ".zip.age") || len(key) > 2048 || size <= 0 || size > backup.MaxArchiveSize {
			continue
		}
		reg := cloudRegistration{StorageID: v.ID, ObjectKey: key, ETag: aws.ToString(object.ETag), ExpiresAt: now.Add(time.Hour)}
		reg.Candidate = CloudCandidate{ID: domain.NewID(), FileName: path.Base(key), SizeBytes: size, LastModified: aws.ToTime(object.LastModified), Status: "pending", Message: "待核对；导入需要恢复私钥与完整校验"}
		if len(reg.ETag) > 512 {
			continue
		}
		if existing, e := m.cloudExisting(ctx, cloudRequestID(reg)); e == nil && existing.State == "succeeded" {
			reg.Candidate.Imported = true
			reg.Candidate.Status = "imported"
			reg.Candidate.Message = "已导入为受保护备份"
		} else if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return page, e
		}
		if e = durablejson.Write(m.cloudRegistrationPath(reg.Candidate.ID), reg); e != nil {
			return page, e
		}
		page.Items = append(page.Items, reg.Candidate)
	}
	page.Truncated = aws.ToBool(out.IsTruncated)
	page.ContinuationToken = aws.ToString(out.NextContinuationToken)
	if page.Truncated && page.ContinuationToken == "" {
		return page, errors.New("云端目录缺少下一页标识")
	}
	m.recordStorageHealth(v.ID, "list", nil)
	// Cleanup contains no key material and only known, expired registrations.
	entries, _ := os.ReadDir(m.cloudRoot())
	for _, entry := range entries {
		if entry.IsDir() || !cloudIDPattern.MatchString(strings.TrimSuffix(entry.Name(), ".json")) {
			continue
		}
		info, e := entry.Info()
		if e == nil && info.Mode().IsRegular() && now.Sub(info.ModTime()) > 2*time.Hour {
			_ = os.Remove(filepath.Join(m.cloudRoot(), entry.Name()))
		}
	}
	return page, nil
}

func (m *Manager) ImportCloud(ctx context.Context, candidateID, identity string) (Run, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	if !cloudIDPattern.MatchString(candidateID) {
		return Run{}, errors.New("云端候选不存在，请重新扫描")
	}
	release, e := operationlock.Acquire(m.opts.DataDir)
	if e != nil {
		return Run{}, ErrBusy
	}
	defer release()
	leave := func() {}
	if m.opts.Enter != nil {
		var ok bool
		leave, ok = m.opts.Enter()
		if !ok {
			return Run{}, ErrBusy
		}
	}
	defer leave()
	b, e := readCatalogFile(m.cloudRegistrationPath(candidateID), 16<<10)
	if e != nil {
		return Run{}, errors.New("云端候选已过期，请重新扫描")
	}
	var reg cloudRegistration
	if e = json.Unmarshal(b, &reg); e != nil || reg.Candidate.ID != candidateID || time.Now().After(reg.ExpiresAt) {
		return Run{}, errors.New("云端候选已过期或损坏，请重新扫描")
	}
	var previous *Run
	if existing, e := m.cloudExisting(ctx, cloudRequestID(reg)); e == nil {
		if existing.State == "succeeded" {
			return existing, nil
		}
		if !existing.CloudImported {
			return Run{}, errors.New("云端导入标识已被其他记录占用")
		}
		previous = &existing
	} else if !errors.Is(e, sql.ErrNoRows) {
		return Run{}, e
	}
	v, c, e := m.storedClient(ctx, reg.StorageID)
	if e != nil {
		return Run{}, e
	}
	if !strings.HasPrefix(reg.ObjectKey, v.Prefix+"/") || !strings.HasSuffix(reg.ObjectKey, ".zip.age") || reg.Candidate.SizeBytes <= 0 || reg.Candidate.SizeBytes > backup.MaxArchiveSize {
		return Run{}, errors.New("云端候选路径或大小无效")
	}
	if strings.TrimSpace(identity) == "" {
		return Run{}, errors.New("请输入该备份对应的恢复私钥")
	}
	if e = backup.EnsureSpace(m.opts.BackupDir, uint64(reg.Candidate.SizeBytes)*2+(64<<20)); e != nil {
		return Run{}, e
	}
	request := &s3.GetObjectInput{Bucket: &v.Bucket, Key: &reg.ObjectKey}
	if reg.ETag != "" {
		request.IfMatch = &reg.ETag
	}
	object, e := c.GetObject(ctx, request)
	if e != nil {
		return Run{}, errors.New("云端备份下载失败或内容已改变，请重新扫描")
	}
	defer object.Body.Close()
	f, e := os.CreateTemp(filepath.Join(m.opts.BackupDir, ".staging"), "download-cloud-*.zip.age")
	if e != nil {
		return Run{}, e
	}
	temp := f.Name()
	defer os.Remove(temp)
	if e = backup.ProtectFile(temp); e != nil {
		f.Close()
		return Run{}, e
	}
	h := sha256.New()
	n, e := io.Copy(io.MultiWriter(f, h), io.LimitReader(object.Body, reg.Candidate.SizeBytes+1))
	if se := f.Sync(); e == nil {
		e = se
	}
	if ce := f.Close(); e == nil {
		e = ce
	}
	if e != nil || n != reg.Candidate.SizeBytes {
		return Run{}, errors.New("云端备份接收不完整或大小改变")
	}
	probe, e := os.Open(temp)
	if e != nil {
		return Run{}, e
	}
	header := make([]byte, 22)
	_, e = io.ReadFull(probe, header)
	_ = probe.Close()
	if e != nil || !strings.HasPrefix(string(header), "age-encryption.org/v1\n") {
		return Run{}, errors.New("云端对象不是受支持的加密备份")
	}
	validated, e := backup.Validate(ctx, temp, identity, filepath.Join(m.opts.BackupDir, ".staging"))
	identity = ""
	if e != nil {
		return Run{}, errors.New("云端备份或恢复私钥校验失败，未导入")
	}
	defer validated.Close()
	if validated.Manifest.FormatVersion != 2 || validated.Manifest.BackupID == "" || validated.Manifest.KeyID == "" {
		return Run{}, errors.New("该云端对象缺少可识别的加密备份元信息，请手动下载后预检")
	}
	id := domain.NewID()
	now := time.Now().UTC()
	started := validated.Manifest.CreatedAt
	if started.IsZero() {
		started = reg.Candidate.LastModified
	}
	if started.IsZero() {
		started = now
	}
	run := Run{ID: id, RequestID: cloudRequestID(reg), State: "succeeded", Trigger: "imported", CreatedAt: now, StartedAt: started, FinishedAt: &now, HeartbeatAt: now, Version: validated.Manifest.AppVersion, DatabaseSchema: validated.Manifest.DatabaseSchema, RecipientID: validated.Manifest.KeyID, FileName: "relaylab_cloud_" + id + ".zip.age", SizeBytes: n, SHA256: hex.EncodeToString(h.Sum(nil)), LocalState: "verified", RemoteState: "verified", StorageID: v.ID, ObjectKey: reg.ObjectKey, Protected: true, CloudImported: true, Message: "云端备份已校验导入；源云对象只读，恢复需另行执行"}
	run.LocalPath = filepath.Join(m.opts.BackupDir, run.FileName)
	if previous != nil {
		run.ID = previous.ID
		run.FileName = previous.FileName
		run.LocalPath = previous.LocalPath
		run.CreatedAt = previous.CreatedAt
	}
	if !strings.HasPrefix(run.FileName, "relaylab_cloud_") || run.FileName != filepath.Base(run.FileName) || filepath.Clean(run.LocalPath) != filepath.Join(m.opts.BackupDir, run.FileName) {
		return Run{}, errors.New("云端导入的本地路径无效")
	}
	// Reserve the trusted digest and source reference before publication. An
	// interrupted import can retry this same record and never creates duplicates.
	run.State = "local_ready"
	run.LocalState = "pending"
	run.FinishedAt = nil
	if previous == nil {
		saved, fresh, err := m.opts.Store.CreateBackupRun(ctx, run)
		if err != nil {
			return Run{}, err
		}
		if !fresh {
			return saved, nil
		}
	}
	if e = m.save(ctx, run); e != nil {
		return Run{}, errors.New("云端校验已完成，记录暂存失败；未发布本地文件")
	}
	// Publish a validated encrypted copy without replacing any existing path.
	if _, statErr := os.Lstat(run.LocalPath); statErr == nil {
		if !m.ownedPath(run.LocalPath) {
			return Run{}, errors.New("本地导入文件路径无效")
		}
		existingHash, existingSize, err := hashFile(run.LocalPath)
		if err != nil || existingHash != run.SHA256 || existingSize != run.SizeBytes {
			return Run{}, errors.New("本地导入文件已改变，拒绝覆盖")
		}
	} else if !os.IsNotExist(statErr) {
		return Run{}, statErr
	} else if e = backup.PublishVerifiedFile(temp, run.LocalPath); e != nil {
		run.State = "failed"
		run.Message = "本地导入文件发布失败，可重试同一候选"
		m.saveFinal(run)
		return Run{}, e
	}
	run.State = "succeeded"
	run.LocalState = "verified"
	run.FinishedAt = &now
	if e = m.save(ctx, run); e != nil {
		return Run{}, errors.New("云端文件已保留，但索引提交失败，请核对备份目录")
	}
	return run, nil
}
