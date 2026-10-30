package backups

import (
	"context"
	"crypto/sha256"
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
	tr := &http.Transport{Proxy: nil, TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 30 * time.Second, IdleConnTimeout: 30 * time.Second, MaxIdleConnsPerHost: 2, MaxConnsPerHost: 2, DisableCompression: true, MaxResponseHeaderBytes: 1 << 20}
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, errors.New("S3连接地址无效")
		}
		if forbiddenStorageHost(host) {
			return nil, errors.New("该存储域名禁止访问")
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil || len(ips) == 0 {
			return nil, errors.New("S3域名解析失败")
		}
		for _, ip := range ips {
			if !publicAddress(ip) && !allowedHost(host, allowed) {
				return nil, errors.New("S3域名解析至非公共地址")
			}
		}
		d := net.Dialer{Timeout: 15 * time.Second}
		for _, ip := range ips {
			conn, e := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if e == nil {
				return conn, nil
			}
		}
		return nil, errors.New("S3连接失败")
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
func s3Client(v Storage, c storageCredentials, allowed []string) *s3.Client {
	cfg := aws.Config{Region: v.Region, Credentials: credentials.NewStaticCredentialsProvider(c.AccessKeyID, c.SecretAccessKey, ""), HTTPClient: storageHTTP(allowed), RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired, ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired}
	return s3.NewFromConfig(cfg, func(o *s3.Options) { o.BaseEndpoint = aws.String(v.Endpoint); o.UsePathStyle = v.ForcePathStyle })
}
func (m *Manager) inputCredentials(ctx context.Context, in StorageInput) (StorageInput, error) {
	old, cipher, err := m.opts.Store.BackupStorage(ctx, "")
	if err == nil && in.AccessKeyID == "" && in.SecretAccessKey == "" && in.Endpoint == old.Endpoint {
		plain, e := m.opts.Vault.DecryptBackupCredentials(old.ID, cipher)
		if e != nil {
			return in, e
		}
		var c storageCredentials
		if e = json.Unmarshal([]byte(plain), &c); e != nil {
			return in, errors.New("存储凭据损坏")
		}
		in.AccessKeyID, in.SecretAccessKey = c.AccessKeyID, c.SecretAccessKey
	} else if err == nil && in.Endpoint == old.Endpoint && in.SecretAccessKey == "" {
		plain, e := m.opts.Vault.DecryptBackupCredentials(old.ID, cipher)
		if e != nil {
			return in, e
		}
		var c storageCredentials
		if e = json.Unmarshal([]byte(plain), &c); e != nil {
			return in, errors.New("存储凭据损坏")
		}
		if c.AccessKeyID != in.AccessKeyID {
			return in, errors.New("更换Access Key时需同时填写Secret")
		}
		in.SecretAccessKey = c.SecretAccessKey
	}
	if in.AccessKeyID == "" || in.SecretAccessKey == "" {
		return in, errors.New("请填写Access Key和Secret；更换端点时需重新填写")
	}
	return in, nil
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
	v := Storage{ID: domain.NewID(), Endpoint: in.Endpoint, Region: in.Region, Bucket: in.Bucket, Prefix: in.Prefix, ForcePathStyle: in.ForcePathStyle, AccessKeyMask: security.Mask(in.AccessKeyID), SecretConfigured: true, CreatedAt: time.Now().UTC(), Health: domain.BackupStorageHealth{Status: "untested", Message: "尚未验证当前配置"}}
	b, _ := json.Marshal(storageCredentials{in.AccessKeyID, in.SecretAccessKey})
	cipher, err := m.opts.Vault.EncryptBackupCredentials(v.ID, string(b))
	if err != nil {
		return Storage{}, err
	}
	if v, err = m.opts.Store.SaveBackupStorageAllocated(ctx, v, cipher, true); err != nil {
		return Storage{}, err
	}
	return v, durablejson.Write(filepath.Join(m.opts.BackupDir, "storages", v.ID+".json"), storageCatalog{v, cipher})
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
	return v, s3Client(v, c, m.opts.AllowedStorageHosts), nil
}
func (m *Manager) TestStorage(ctx context.Context, in StorageInput) (err error) {
	in, err = validateStorage(in, m.opts.AllowedStorageHosts)
	if err != nil {
		return err
	}
	in, err = m.inputCredentials(ctx, in)
	if err != nil {
		return err
	}
	healthID := m.matchingStorage(ctx, in)
	defer func() {
		if healthID != "" {
			m.recordStorageHealth(healthID, "connection_test", err)
		}
	}()
	v := Storage{Endpoint: in.Endpoint, Region: in.Region, Bucket: in.Bucket, Prefix: in.Prefix, ForcePathStyle: in.ForcePathStyle}
	client := s3Client(v, storageCredentials{in.AccessKeyID, in.SecretAccessKey}, m.opts.AllowedStorageHosts)
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	key := in.Prefix + "/.connection-test-" + domain.NewID()
	payload := "relaylab-storage-check-" + domain.NewID()
	if _, err = client.PutObject(ctx, &s3.PutObjectInput{Bucket: &v.Bucket, Key: &key, Body: strings.NewReader(payload), ContentLength: aws.Int64(int64(len(payload)))}); err != nil {
		return errors.New("S3写入测试失败，请检查端点和写权限")
	}
	var checkErr error
	response, e := client.GetObject(ctx, &s3.GetObjectInput{Bucket: &v.Bucket, Key: &key})
	if e != nil {
		checkErr = errors.New("S3读取测试失败")
	} else {
		b, readErr := io.ReadAll(io.LimitReader(response.Body, 4097))
		response.Body.Close()
		if readErr != nil || string(b) != payload {
			checkErr = errors.New("S3测试内容校验失败")
		}
	}
	cleanup, cancelCleanup := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelCleanup()
	if _, err = client.DeleteObject(cleanup, &s3.DeleteObjectInput{Bucket: &v.Bucket, Key: &key}); err != nil {
		return errors.New("S3测试对象删除失败，请检查删除权限，可能有测试对象残留")
	}
	return checkErr
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

func (m *Manager) matchingStorage(ctx context.Context, in StorageInput) string {
	v, cipher, e := m.opts.Store.BackupStorage(ctx, "")
	if e != nil {
		return ""
	}
	if v.Endpoint != in.Endpoint || v.Region != in.Region || v.Bucket != in.Bucket || v.Prefix != in.Prefix || v.ForcePathStyle != in.ForcePathStyle {
		return ""
	}
	plain, e := m.opts.Vault.DecryptBackupCredentials(v.ID, cipher)
	if e != nil {
		return ""
	}
	var credentials storageCredentials
	if json.Unmarshal([]byte(plain), &credentials) != nil {
		return ""
	}
	if credentials.AccessKeyID != in.AccessKeyID || credentials.SecretAccessKey != in.SecretAccessKey {
		return ""
	}
	return v.ID
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
		h.Message = "最近一次存储操作失败，请检查备份记录或重新测试连接"
	}
	if m.opts.Store.UpdateBackupStorageHealth(ctx, id, h) != nil {
		return
	}
	v, cipher, e := m.opts.Store.BackupStorage(ctx, id)
	if e == nil {
		_ = durablejson.Write(filepath.Join(m.opts.BackupDir, "storages", id+".json"), storageCatalog{v, cipher})
	}
}
