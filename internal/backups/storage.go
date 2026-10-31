package backups

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"path/filepath"
	"relaylab/internal/domain"
	"relaylab/internal/durablejson"
	"relaylab/internal/security"
	"relaylab/internal/upstream"
)

type storageCredentials struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
}
type storageCatalog struct {
	Storage Storage `json:"storage"`
	Cipher  string  `json:"encrypted_credentials"`
}

var bucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

func forbiddenStorageHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return host == "api.zxcbug.com" || strings.HasSuffix(host, ".api.zxcbug.com")
}

func validateStorage(in StorageInput, allowed []string) (StorageInput, error) {
	in.Endpoint = strings.TrimRight(strings.TrimSpace(in.Endpoint), "/")
	u, err := url.Parse(in.Endpoint)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.Path != "" {
		return in, errors.New("S3端点必须为无路径、凭据或查询参数的HTTPS地址")
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if forbiddenStorageHost(host) {
		return in, errors.New("该存储域名禁止访问")
	}
	permitted := allowedHost(host, allowed)
	if u.Scheme != "https" && !(u.Scheme == "http" && permitted) {
		return in, errors.New("S3端点必须使用HTTPS；内网存储需部署配置明确允许")
	}
	if ip, err := netip.ParseAddr(host); err == nil && !publicAddress(ip) && !permitted {
		return in, errors.New("S3端点不是公共地址")
	}
	if !bucketPattern.MatchString(in.Bucket) {
		return in, errors.New("存储桶名称格式不正确")
	}
	in.Prefix = strings.Trim(in.Prefix, "/")
	if in.Prefix == "" {
		in.Prefix = "backups"
	}
	if strings.Contains(in.Prefix, "..") || strings.ContainsAny(in.Prefix, "\\\x00\r\n") || len(in.Prefix) > 500 {
		return in, errors.New("存储前缀不正确")
	}
	if in.Region == "" {
		in.Region = "auto"
	}
	if len(in.Region) > 100 || len(in.AccessKeyID) > 512 || len(in.SecretAccessKey) > 2048 {
		return in, errors.New("存储配置过长")
	}
	return in, nil
}
func allowedHost(host string, allowed []string) bool {
	for _, v := range allowed {
		if strings.EqualFold(strings.TrimSuffix(v, "."), host) {
			return true
		}
	}
	return false
}
func publicAddress(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	for _, p := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "2001:db8::/32"} {
		if netip.MustParsePrefix(p).Contains(ip) {
			return false
		}
	}
	return true
}
func storageHTTP(allowed []string) *http.Client {
	resolver, _ := upstream.NewDNSResolver("")
	return storageHTTPWithResolver(allowed, resolver)
}

func storageHTTPWithResolver(allowed []string, resolver upstream.DNSResolver) *http.Client {
	tr := &http.Transport{Proxy: nil, TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 30 * time.Second, IdleConnTimeout: 30 * time.Second, MaxIdleConnsPerHost: 2, MaxConnsPerHost: 2, DisableCompression: true, MaxResponseHeaderBytes: 1 << 20}
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, &upstream.Error{Code: "blocked_target", Message: "S3连接地址无效"}
		}
		if forbiddenStorageHost(host) {
			return nil, &upstream.Error{Code: "protected_target", Message: "该存储域名禁止访问"}
		}
		ips, err := resolver(ctx, host)
		if err != nil {
			var typed *upstream.Error
			if errors.As(err, &typed) {
				return nil, typed
			}
			if errors.Is(ctx.Err(), context.Canceled) {
				return nil, &upstream.Error{Code: "canceled", Message: "S3连接已取消"}
			}
			return nil, &upstream.Error{Code: "dns_error", Message: "S3域名解析失败，请检查DNS设置"}
		}
		if len(ips) == 0 {
			return nil, &upstream.Error{Code: "dns_error", Message: "S3域名未返回可用地址"}
		}
		for _, ip := range ips {
			if !publicAddress(ip) && !allowedHost(host, allowed) {
				if upstream.IsSyntheticDNSAddress(ip) {
					return nil, &upstream.Error{Code: "fake_ip_blocked", Message: "S3域名解析为代理Fake-IP；请配置upstream.dns_over_https或在代理中关闭该域名Fake-IP"}
				}
				return nil, &upstream.Error{Code: "blocked_target", Message: "S3域名解析至非公共地址"}
			}
		}
		d := net.Dialer{Timeout: 15 * time.Second}
		timedOut := false
		for _, ip := range ips {
			conn, e := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if e == nil {
				return conn, nil
			}
			var netErr net.Error
			timedOut = timedOut || errors.As(e, &netErr) && netErr.Timeout()
			if ctx.Err() != nil {
				break
			}
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, &upstream.Error{Code: "canceled", Message: "S3连接已取消"}
		}
		if timedOut || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, &upstream.Error{Code: "connection_timeout", Message: "S3连接超时，请检查网络出口"}
		}
		return nil, &upstream.Error{Code: "connection_error", Message: "S3连接失败，请检查网络出口"}
	}
	return &http.Client{Timeout: 30 * time.Minute, Transport: boundedStorageTransport{tr}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("S3重定向已拒绝") }}
}

type boundedStorageTransport struct{ base http.RoundTripper }
type limitedStorageBody struct {
	io.Reader
	io.Closer
}

func (t boundedStorageTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	request := r.Clone(r.Context())
	if r.Body != nil {
		request.Body = newPacedStorageBody(r.Context(), r.Body, 32<<20)
	}
	out, e := t.base.RoundTrip(request)
	if e == nil && out.Body != nil {
		out.Body = newPacedStorageBody(r.Context(), out.Body, 32<<20)
		if r.URL.Query().Get("list-type") == "2" {
			out.Body = limitedStorageBody{io.LimitReader(out.Body, 1<<20), out.Body}
		}
	}
	if e == nil && out.StatusCode >= 300 {
		out.Body = limitedStorageBody{io.LimitReader(out.Body, 64<<10), out.Body}
	}
	return out, e
}
func (m *Manager) s3Client(v Storage, c storageCredentials) *s3.Client {
	cfg := aws.Config{Region: v.Region, Credentials: credentials.NewStaticCredentialsProvider(c.AccessKeyID, c.SecretAccessKey, ""), HTTPClient: storageHTTPWithResolver(m.opts.AllowedStorageHosts, m.storageResolver), RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired, ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired}
	return s3.NewFromConfig(cfg, func(o *s3.Options) { o.BaseEndpoint = aws.String(v.Endpoint); o.UsePathStyle = v.ForcePathStyle })
}
func (m *Manager) inputCredentials(ctx context.Context, in StorageInput) (StorageInput, error) {
	old, cipher, err := m.opts.Store.BackupStorage(ctx, "")
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return in, err
	}
	if err == nil && normalizedStorageEndpoint(in.Endpoint) == normalizedStorageEndpoint(old.Endpoint) && (in.AccessKeyID == "" || in.SecretAccessKey == "") {
		plain, e := m.opts.Vault.DecryptBackupCredentials(old.ID, cipher)
		if e != nil {
			return in, e
		}
		var c storageCredentials
		if e = json.Unmarshal([]byte(plain), &c); e != nil {
			return in, errors.New("存储凭据损坏")
		}
		if in.AccessKeyID == "" {
			in.AccessKeyID = c.AccessKeyID
		}
		if in.SecretAccessKey == "" {
			if c.AccessKeyID != in.AccessKeyID {
				return in, errors.New("更换Access Key时需同时填写Secret")
			}
			in.SecretAccessKey = c.SecretAccessKey
		}
	}
	if in.AccessKeyID == "" || in.SecretAccessKey == "" {
		return in, errors.New("请填写Access Key和Secret；更换端点时需重新填写")
	}
	return in, nil
}

func normalizedStorageEndpoint(endpoint string) string {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(endpoint), "/"))
	if err != nil {
		return endpoint
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	return parsed.String()
}

func (m *Manager) sameCurrentStorage(ctx context.Context, in StorageInput) (Storage, bool, error) {
	current, cipher, err := m.opts.Store.BackupStorage(ctx, "")
	if errors.Is(err, sql.ErrNoRows) {
		return Storage{}, false, nil
	}
	if err != nil {
		return Storage{}, false, err
	}
	plain, err := m.opts.Vault.DecryptBackupCredentials(current.ID, cipher)
	if err != nil {
		return Storage{}, false, err
	}
	var credentials storageCredentials
	if json.Unmarshal([]byte(plain), &credentials) != nil {
		return Storage{}, false, errors.New("存储凭据损坏")
	}
	region := current.Region
	if region == "" {
		region = "auto"
	}
	prefix := strings.Trim(current.Prefix, "/")
	if prefix == "" {
		prefix = "backups"
	}
	same := normalizedStorageEndpoint(in.Endpoint) == normalizedStorageEndpoint(current.Endpoint) && in.Region == region && in.Bucket == current.Bucket && in.Prefix == prefix && in.ForcePathStyle == current.ForcePathStyle && in.AccessKeyID == credentials.AccessKeyID && in.SecretAccessKey == credentials.SecretAccessKey
	return current, same, nil
}

func (m *Manager) SaveStorage(ctx context.Context, in StorageInput) (Storage, error) {
	m.policyMu.Lock()
	defer m.policyMu.Unlock()
	var err error
	in, err = validateStorage(in, m.opts.AllowedStorageHosts)
	if err != nil {
		return Storage{}, err
	}
	in, err = m.inputCredentials(ctx, in)
	if err != nil {
		return Storage{}, err
	}
	if current, same, err := m.sameCurrentStorage(ctx, in); err != nil {
		return Storage{}, err
	} else if same {
		// Repeated saves must not create a new identity or overwrite health
		// concurrently updated by an in-flight connection test.
		current, cipher, err := m.opts.Store.BackupStorage(ctx, current.ID)
		if err != nil {
			return Storage{}, err
		}
		return current, m.ensureStorageCatalog(current, cipher)
	}
	v := Storage{ID: domain.NewID(), Endpoint: in.Endpoint, Region: in.Region, Bucket: in.Bucket, Prefix: in.Prefix, ForcePathStyle: in.ForcePathStyle, AccessKeyMask: security.Mask(in.AccessKeyID), SecretConfigured: true, CreatedAt: time.Now().UTC(), Health: domain.BackupStorageHealth{Status: "untested", Message: "尚未验证当前配置"}}
	b, _ := json.Marshal(storageCredentials{in.AccessKeyID, in.SecretAccessKey})
	cipher, err := m.opts.Vault.EncryptBackupCredentials(v.ID, string(b))
	if err != nil {
		return Storage{}, err
	}
	if v, err = m.opts.Store.SaveBackupStorageAllocated(ctx, v, cipher, true); err != nil {
		return Storage{}, err
	}
	return v, m.ensureStorageCatalog(v, cipher)
}

// A DB commit can succeed before its recovery catalog is written. Retrying a
// save repairs a missing catalog but never replaces unrelated or corrupt data.
// The caller holds policyMu, also used by saveStorageHealth, so the repaired
// file cannot overwrite a concurrent connection test's newer health snapshot.
func (m *Manager) ensureStorageCatalog(current Storage, cipher string) error {
	path := filepath.Join(m.opts.BackupDir, "storages", current.ID+".json")
	raw, err := readCatalogFile(path, 16384)
	if os.IsNotExist(err) {
		if durablejson.Write(path, storageCatalog{current, cipher}) != nil {
			return errors.New("存储配置已保存，但恢复目录写入失败；请检查备份目录权限后重试")
		}
		return nil
	}
	if err != nil {
		return errors.New("存储恢复目录无法读取或不是受支持的普通文件，请核对备份目录")
	}
	var catalog storageCatalog
	if json.Unmarshal(raw, &catalog) != nil {
		return errors.New("存储恢复目录损坏，已保留原文件，请核对后重试")
	}
	stored := catalog.Storage
	if catalog.Cipher != cipher || stored.ID != current.ID || stored.Revision != current.Revision || stored.Endpoint != current.Endpoint || stored.Region != current.Region || stored.Bucket != current.Bucket || stored.Prefix != current.Prefix || stored.ForcePathStyle != current.ForcePathStyle || stored.AccessKeyMask != current.AccessKeyMask || stored.SecretConfigured != current.SecretConfigured || !stored.CreatedAt.Equal(current.CreatedAt) {
		return errors.New("存储恢复目录与当前修订身份不一致，已保留原文件，请核对后重试")
	}
	return nil
}
func (m *Manager) storedClient(ctx context.Context, id string) (Storage, *s3.Client, error) {
	v, cipher, err := m.opts.Store.BackupStorage(ctx, id)
	if err != nil {
		return v, nil, errors.New("未配置S3存储")
	}
	if _, err = validateStorage(StorageInput{Endpoint: v.Endpoint, Region: v.Region, Bucket: v.Bucket, Prefix: v.Prefix, ForcePathStyle: v.ForcePathStyle}, m.opts.AllowedStorageHosts); err != nil {
		return v, nil, errors.New("保存的存储地址不符合当前请求出口策略")
	}
	plain, err := m.opts.Vault.DecryptBackupCredentials(v.ID, cipher)
	if err != nil {
		return v, nil, err
	}
	var c storageCredentials
	if json.Unmarshal([]byte(plain), &c) != nil {
		return v, nil, errors.New("存储凭据损坏")
	}
	return v, m.s3Client(v, c), nil
}
func (m *Manager) TestStorage(ctx context.Context, in StorageInput) error {
	_, err := m.TestStorageDetailed(ctx, in)
	return err
}

func (m *Manager) TestStorageDetailed(parent context.Context, in StorageInput) (result domain.BackupStorageTestResult, resultErr error) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	result.Status, result.Stage, result.TestScope = "failed", "validating", "draft"
	healthID := ""
	defer func() {
		result.DurationMS = time.Since(started).Milliseconds()
		if resultErr == nil {
			result.Status, result.Stage, result.Message = "ok", "completed", "对象写入、读取校验和删除均已通过"
		} else {
			failure := storageFailure(parent, result.Stage, resultErr)
			result.Status, result.Stage, result.ErrorCode, result.Message, result.HTTPStatus = "failed", failure.Stage, failure.Code, failure.Message, failure.HTTPStatus
			resultErr = failure
		}
		if healthID != "" {
			m.recordStorageTestHealth(healthID, result)
		}
	}()
	var err error
	in, err = validateStorage(in, m.opts.AllowedStorageHosts)
	if err != nil {
		return result, &StorageError{Code: "invalid_storage_config", Stage: "validating", Message: "S3 配置格式或请求出口策略不符合要求，请检查表单"}
	}
	// Capture credentials and their current revision together. Saving another
	// configuration may proceed once this short snapshot read is complete; the
	// network request and its health result stay bound to this exact revision.
	m.policyMu.Lock()
	in, err = m.inputCredentials(ctx, in)
	var testedStorage Storage
	var matchesSaved bool
	var bindingErr error
	if err == nil {
		testedStorage, matchesSaved, bindingErr = m.sameCurrentStorage(ctx, in)
	}
	m.policyMu.Unlock()
	if err != nil {
		if ctx.Err() != nil {
			return result, storageFailure(ctx, "validating", ctx.Err())
		}
		return result, &StorageError{Code: "invalid_storage_credentials", Stage: "validating", Message: "S3 凭据不完整或无法读取；更换端点或 Access Key 后需重新填写 Secret"}
	}
	if bindingErr != nil {
		return result, &StorageError{Code: "storage_metadata_failed", Stage: "validating", Message: "无法核对测试配置的所属修订，未发送云端请求"}
	}
	if matchesSaved {
		healthID = testedStorage.ID
		result.TestScope, result.StorageID, result.StorageRevision = "saved", testedStorage.ID, testedStorage.Revision
	}
	v := Storage{Endpoint: in.Endpoint, Region: in.Region, Bucket: in.Bucket, Prefix: in.Prefix, ForcePathStyle: in.ForcePathStyle}
	client := m.s3Client(v, storageCredentials{in.AccessKeyID, in.SecretAccessKey})
	if ctx.Err() != nil {
		return result, storageFailure(ctx, "validating", ctx.Err())
	}
	key := in.Prefix + "/.connection-test-" + domain.NewID()
	payload := "relaylab-storage-check-" + domain.NewID()
	cleanupNeeded := true
	// A PUT may commit even when its acknowledgement is lost. Once it is
	// attempted, always clean only this operation's random key independently
	// of the request deadline, including cancellation and ambiguous failures.
	defer func() {
		if !cleanupNeeded {
			result.CleanupMessage = "测试对象未创建，已有同名对象保持不变"
			return
		}
		cleanup, cancelCleanup := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancelCleanup()
		if _, err := client.DeleteObject(cleanup, &s3.DeleteObjectInput{Bucket: &v.Bucket, Key: &key}); err != nil {
			failure := storageFailure(cleanup, "cleaning", err)
			result.CleanupErrorCode = failure.Code
			result.CleanupMessage = "测试对象清理未获确认，可能有测试对象残留；" + failure.Message
			if resultErr == nil {
				resultErr = failure
			}
		} else {
			result.CleanupMessage = "本次测试对象已清理"
		}
	}()
	result.Stage = "writing"
	if _, err := client.PutObject(ctx, &s3.PutObjectInput{Bucket: &v.Bucket, Key: &key, Body: strings.NewReader(payload), ContentLength: aws.Int64(int64(len(payload))), IfNoneMatch: aws.String("*")}, func(options *s3.Options) { options.RetryMaxAttempts = 1 }); err != nil {
		if storageWritePreconditionFailed(err) {
			cleanupNeeded = false
		}
		return result, storageFailure(ctx, "writing", err)
	}
	result.Stage = "reading"
	response, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: &v.Bucket, Key: &key})
	if err != nil {
		return result, storageFailure(ctx, "reading", err)
	}
	b, readErr := io.ReadAll(io.LimitReader(response.Body, 4097))
	response.Body.Close()
	if readErr != nil {
		return result, storageFailure(ctx, "reading", readErr)
	}
	result.Stage = "verifying"
	if string(b) != payload {
		return result, &StorageError{Code: "content_mismatch", Stage: "verifying", Message: "S3 测试内容校验不一致，请检查存储服务"}
	}
	result.Stage = "cleaning"
	return result, nil
}
func hashFile(path string) (string, int64, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", 0, e
	}
	defer f.Close()
	h := sha256.New()
	n, e := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, e
}
func (m *Manager) upload(ctx context.Context, r *Run) (err error) {
	defer func() { m.recordStorageHealth(r.StorageID, "upload", err) }()
	v, c, e := m.storedClient(ctx, r.StorageID)
	if e != nil {
		return e
	}
	// A retry must never overwrite an object that reached storage before a
	// disconnect or process crash. Re-read it first and accept only this hash.
	present, e := m.remoteExistsAndMatches(ctx, c, v, r)
	if e != nil {
		return e
	}
	if present {
		r.RemoteState = "verified"
		r.TransferredBytes = r.SizeBytes
		return nil
	}
	f, e := os.Open(r.LocalPath)
	if e != nil {
		return errors.New("本地备份文件不可读")
	}
	defer f.Close()
	if _, e = c.PutObject(ctx, &s3.PutObjectInput{Bucket: &v.Bucket, Key: &r.ObjectKey, Body: f, ContentLength: &r.SizeBytes, ContentType: aws.String("application/octet-stream"), IfNoneMatch: aws.String("*")}); e != nil {
		if exists, checkErr := m.remoteExistsAndMatches(ctx, c, v, r); checkErr == nil && exists {
			r.RemoteState = "verified"
			r.TransferredBytes = r.SizeBytes
			return nil
		}
		return errors.New("S3上传失败，本地包已保留；未覆盖已有对象")
	}
	r.TransferredBytes = r.SizeBytes
	r.State = "verifying_remote"
	r.RemoteState = "verifying"
	if e = m.opts.Store.SaveBackupRun(ctx, *r); e != nil {
		return e
	}
	return m.verifyRemote(ctx, r)
}
func (m *Manager) verifyRemote(ctx context.Context, r *Run) (err error) {
	defer func() { m.recordStorageHealth(r.StorageID, "verify", err) }()
	v, c, e := m.storedClient(ctx, r.StorageID)
	if e != nil {
		return e
	}
	exists, e := m.remoteExistsAndMatches(ctx, c, v, r)
	if e != nil {
		return e
	}
	if !exists {
		return errors.New("S3回读对象不存在")
	}
	r.RemoteState = "verified"
	return nil
}

func (m *Manager) remoteExistsAndMatches(ctx context.Context, c *s3.Client, v Storage, r *Run) (bool, error) {
	if r.SizeBytes <= 0 || len(r.SHA256) != 64 {
		return false, errors.New("备份校验元信息不完整")
	}
	out, e := c.GetObject(ctx, &s3.GetObjectInput{Bucket: &v.Bucket, Key: &r.ObjectKey})
	if e != nil {
		var missing *types.NoSuchKey
		var status interface{ HTTPStatusCode() int }
		if errors.As(e, &missing) || (errors.As(e, &status) && status.HTTPStatusCode() == 404) {
			return false, nil
		}
		return false, errors.New("S3回读校验失败")
	}
	defer out.Body.Close()
	h := sha256.New()
	n, e := io.Copy(h, io.LimitReader(out.Body, r.SizeBytes+1))
	if e != nil || n != r.SizeBytes || hex.EncodeToString(h.Sum(nil)) != r.SHA256 {
		return true, errors.New("S3回读大小或SHA-256不匹配，已拒绝覆盖同名对象")
	}
	return true, nil
}

func (m *Manager) recordStorageHealth(id, operation string, cause error) {
	if id == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	now := time.Now().UTC()
	h := domain.BackupStorageHealth{CheckedAt: &now, Status: "ok", Message: "最近一次存储操作成功", Operation: operation}
	if cause != nil {
		h.Status = "failed"
		failure := storageFailure(ctx, operation, cause)
		h.Message, h.Stage, h.ErrorCode, h.HTTPStatus = failure.Message, failure.Stage, failure.Code, failure.HTTPStatus
	}
	m.saveStorageHealth(ctx, id, h)
}

func (m *Manager) recordStorageTestHealth(id string, result domain.BackupStorageTestResult) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	now := time.Now().UTC()
	h := domain.BackupStorageHealth{CheckedAt: &now, Status: result.Status, Message: result.Message, Operation: "connection_test", Stage: result.Stage, ErrorCode: result.ErrorCode, DurationMS: result.DurationMS, HTTPStatus: result.HTTPStatus, CleanupErrorCode: result.CleanupErrorCode, CleanupMessage: result.CleanupMessage}
	m.saveStorageHealth(ctx, id, h)
}

func (m *Manager) saveStorageHealth(ctx context.Context, id string, h domain.BackupStorageHealth) {
	m.policyMu.Lock()
	defer m.policyMu.Unlock()
	if m.opts.Store.UpdateBackupStorageHealth(ctx, id, h) != nil {
		return
	}
	v, cipher, e := m.opts.Store.BackupStorage(ctx, id)
	if e == nil {
		_ = durablejson.Write(filepath.Join(m.opts.BackupDir, "storages", id+".json"), storageCatalog{v, cipher})
	}
}
